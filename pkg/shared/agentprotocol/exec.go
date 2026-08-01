package agentprotocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	DefaultMaxPodExecInputBytes  uint64 = 16 * 1024 * 1024
	DefaultMaxPodExecOutputBytes uint64 = 32 * 1024 * 1024
	MaxPodExecDimension          uint32 = 4096
	podExecChunkBytes                   = 32 * 1024
)

var ErrPodExecOutputLimit = errors.New("Pod Exec output limit reached")

type PodExecSize struct {
	Columns uint32
	Rows    uint32
}

type PodExecSizeQueue interface {
	Next() *PodExecSize
}

type PodExecHandler func(
	context.Context,
	*agentv1.PodExecRequest,
	io.Reader,
	io.Writer,
	io.Writer,
	PodExecSizeQueue,
) (*agentv1.PodExecResponse, <-chan *agentv1.PodExecExit, error)

type PodExecPeer interface {
	Receive(context.Context) (*agentv1.PodExecFrame, error)
	Send(context.Context, *agentv1.PodExecFrame) error
}

func PodExecStreamHandler(
	maxInputBytes uint64,
	maxOutputBytes uint64,
	handler PodExecHandler,
) IncomingStreamHandler {
	if maxInputBytes == 0 {
		maxInputBytes = DefaultMaxPodExecInputBytes
	}
	if maxOutputBytes == 0 {
		maxOutputBytes = DefaultMaxPodExecOutputBytes
	}
	return func(
		ctx context.Context,
		stream *quic.Stream,
		header *agentv1.StreamHeader,
	) error {
		if handler == nil {
			return &StreamFailure{Code: StreamErrorUnsupported, Err: ErrStreamUnsupported}
		}
		request := &agentv1.PodExecRequest{}
		if err := ReadMessage(stream, request); err != nil {
			return fmt.Errorf("%w: read PodExecRequest: %w", ErrStreamProtocol, err)
		}
		if err := validatePodExecRequest(header, request, maxInputBytes, maxOutputBytes); err != nil {
			return err
		}

		execContext, cancelExec := context.WithCancel(ctx)
		defer cancelExec()
		stdinReader, stdinWriter := io.Pipe()
		defer stdinReader.Close()
		defer stdinWriter.Close()
		sizes := newPodExecSizeQueue(execContext, request.GetColumns(), request.GetRows())
		writer := &podExecFrameWriter{
			stream:  stream,
			maximum: request.GetMaxOutputBytes(),
			ready:   make(chan struct{}),
		}
		responseWritten := false
		defer func() {
			if !responseWritten {
				writer.release()
			}
		}()
		response, exits, err := handler(
			execContext,
			request,
			stdinReader,
			writer.output(agentv1.PodExecOutputStream_POD_EXEC_OUTPUT_STREAM_STDOUT),
			writer.output(agentv1.PodExecOutputStream_POD_EXEC_OUTPUT_STREAM_STDERR),
			sizes,
		)
		if err != nil {
			return err
		}
		if err := validatePodExecResponse(response, request, exits != nil); err != nil {
			return err
		}
		if err := WriteMessage(stream, response); err != nil {
			return err
		}
		responseWritten = true
		writer.release()
		if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
			return nil
		}

		inputErrors := make(chan error, 1)
		go func() {
			inputErrors <- receivePodExecInput(
				execContext,
				stream,
				stdinWriter,
				sizes,
				request.GetMaxInputBytes(),
			)
		}()
		for {
			select {
			case inputErr := <-inputErrors:
				if inputErr == nil || errors.Is(inputErr, io.EOF) {
					_ = stdinWriter.Close()
					inputErrors = nil
					continue
				}
				cancelExec()
				return inputErr
			case exit := <-exits:
				if err := validatePodExecExit(exit, writer.bytes.Load(), writer.maximum); err != nil {
					return err
				}
				if err := writer.write(&agentv1.PodExecFrame{
					Message: &agentv1.PodExecFrame_Exit{Exit: exit},
				}); err != nil {
					return err
				}
				stream.CancelRead(StreamErrorCanceled)
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func DoPodExec(
	ctx context.Context,
	connection *quic.Conn,
	header *agentv1.StreamHeader,
	request *agentv1.PodExecRequest,
	peer PodExecPeer,
	maxInputBytes uint64,
	maxOutputBytes uint64,
) (*agentv1.PodExecResponse, *agentv1.PodExecExit, error) {
	if connection == nil || peer == nil {
		return nil, nil, errors.New("Agent Connection and Pod Exec peer are required")
	}
	if maxInputBytes == 0 {
		maxInputBytes = DefaultMaxPodExecInputBytes
	}
	if maxOutputBytes == 0 {
		maxOutputBytes = DefaultMaxPodExecOutputBytes
	}
	if err := validateStreamHeader(header); err != nil ||
		header.GetKind() != agentv1.StreamKind_STREAM_KIND_POD_EXEC ||
		validatePodExecRequest(header, request, maxInputBytes, maxOutputBytes) != nil {
		return nil, nil, ErrStreamProtocol
	}

	stream, err := connection.OpenStreamSync(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("open Agent Pod Exec Stream: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			AbortStream(stream, StreamErrorCanceled)
		}
	}()
	stopCancellation := context.AfterFunc(ctx, func() {
		AbortStream(stream, streamErrorCode(ctx.Err(), ctx))
	})
	defer stopCancellation()
	if deadline, ok := ctx.Deadline(); ok {
		if err := stream.SetDeadline(deadline); err != nil {
			return nil, nil, err
		}
	} else if err := stream.SetDeadline(time.Now().Add(
		time.Duration(header.GetTimeoutMillis()) * time.Millisecond,
	)); err != nil {
		return nil, nil, err
	}
	if err := WriteMessage(stream, header); err != nil {
		return nil, nil, err
	}
	if err := WriteMessage(stream, request); err != nil {
		return nil, nil, err
	}

	response := &agentv1.PodExecResponse{}
	if err := ReadMessage(stream, response); err != nil {
		return nil, nil, err
	}
	if err := validatePodExecResponse(response, request, response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK); err != nil {
		return nil, nil, err
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		_ = stream.Close()
		if err := requireStreamEOF(stream); err != nil {
			return nil, nil, err
		}
		finished = true
		return response, nil, nil
	}

	operationContext, cancelOperation := context.WithCancel(ctx)
	defer cancelOperation()
	inputErrors := make(chan error, 1)
	go func() {
		var sent uint64
		for {
			frame, receiveErr := peer.Receive(operationContext)
			if receiveErr != nil {
				inputErrors <- receiveErr
				return
			}
			if err := validatePodExecClientFrame(frame, &sent, request.GetMaxInputBytes()); err != nil {
				inputErrors <- err
				return
			}
			if err := WriteMessage(stream, frame); err != nil {
				inputErrors <- err
				return
			}
		}
	}()

	type outputResult struct {
		frame    *agentv1.PodExecFrame
		exit     *agentv1.PodExecExit
		err      error
		finished bool
	}
	outputResults := make(chan outputResult, 1)
	go func() {
		send := func(result outputResult) bool {
			select {
			case outputResults <- result:
				return true
			case <-operationContext.Done():
				return false
			}
		}
		var received uint64
		for {
			frame := &agentv1.PodExecFrame{}
			if readErr := ReadMessage(stream, frame); readErr != nil {
				send(outputResult{err: readErr})
				return
			}
			switch message := frame.GetMessage().(type) {
			case *agentv1.PodExecFrame_Output:
				if validationErr := validatePodExecOutput(message.Output, &received, request.GetMaxOutputBytes()); validationErr != nil {
					send(outputResult{err: validationErr})
					return
				}
				if !send(outputResult{frame: frame}) {
					return
				}
			case *agentv1.PodExecFrame_Exit:
				if validationErr := validatePodExecExit(message.Exit, received, request.GetMaxOutputBytes()); validationErr != nil {
					send(outputResult{err: validationErr})
					return
				}
				send(outputResult{frame: frame, exit: message.Exit, finished: true})
				return
			default:
				send(outputResult{err: ErrStreamProtocol})
				return
			}
		}
	}()
	for {
		select {
		case inputErr := <-inputErrors:
			if inputErr == nil {
				inputErr = io.EOF
			}
			return response, nil, inputErr
		case result := <-outputResults:
			if result.err != nil {
				return response, nil, result.err
			}
			if err := peer.Send(operationContext, result.frame); err != nil {
				return response, nil, err
			}
			if result.finished {
				cancelOperation()
				_ = stream.Close()
				if err := requireStreamEOF(stream); err != nil {
					return response, nil, err
				}
				finished = true
				return response, result.exit, nil
			}
		case <-ctx.Done():
			return response, nil, ctx.Err()
		}
	}
}

func validatePodExecRequest(
	header *agentv1.StreamHeader,
	request *agentv1.PodExecRequest,
	maxInputBytes uint64,
	maxOutputBytes uint64,
) error {
	if request == nil || header.GetIdempotencyKey() != "" ||
		len(k8svalidation.IsDNS1123Label(request.GetNamespace())) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(request.GetPodName())) != 0 ||
		len(k8svalidation.IsDNS1123Label(request.GetContainer())) != 0 ||
		request.GetPodUid() == "" || len(request.GetPodUid()) > maxPodUIDLength ||
		strings.TrimSpace(request.GetPodUid()) != request.GetPodUid() ||
		!request.GetTty() || !validPodExecSize(request.GetColumns(), request.GetRows()) ||
		request.GetMaxInputBytes() == 0 || request.GetMaxInputBytes() > maxInputBytes ||
		request.GetMaxOutputBytes() == 0 || request.GetMaxOutputBytes() > maxOutputBytes {
		return ErrStreamProtocol
	}
	return nil
}

func validatePodExecResponse(
	response *agentv1.PodExecResponse,
	request *agentv1.PodExecRequest,
	hasExit bool,
) error {
	if response == nil || response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrStreamProtocol
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		if !hasExit || response.GetKubernetesStatusCode() < 200 ||
			response.GetKubernetesStatusCode() >= 300 ||
			response.GetPodUid() != request.GetPodUid() ||
			response.GetContainer() != request.GetContainer() ||
			response.GetReason() != "" || response.GetMessage() != "" {
			return ErrStreamProtocol
		}
		return nil
	}
	if hasExit || response.GetReason() == "" || response.GetMessage() == "" ||
		response.GetPodUid() != "" || response.GetContainer() != "" {
		return ErrStreamProtocol
	}
	return nil
}

func validatePodExecClientFrame(
	frame *agentv1.PodExecFrame,
	received *uint64,
	maximum uint64,
) error {
	if frame == nil || received == nil {
		return ErrStreamProtocol
	}
	switch message := frame.GetMessage().(type) {
	case *agentv1.PodExecFrame_Input:
		data := message.Input.GetData()
		if len(data) == 0 || len(data) > podExecChunkBytes ||
			*received > maximum || uint64(len(data)) > maximum-*received {
			return ErrStreamProtocol
		}
		*received += uint64(len(data))
	case *agentv1.PodExecFrame_Resize:
		if !validPodExecSize(message.Resize.GetColumns(), message.Resize.GetRows()) {
			return ErrStreamProtocol
		}
	case *agentv1.PodExecFrame_CloseInput:
	default:
		return ErrStreamProtocol
	}
	return nil
}

func validatePodExecOutput(
	output *agentv1.PodExecOutput,
	received *uint64,
	maximum uint64,
) error {
	if output == nil || received == nil ||
		(output.GetStream() != agentv1.PodExecOutputStream_POD_EXEC_OUTPUT_STREAM_STDOUT &&
			output.GetStream() != agentv1.PodExecOutputStream_POD_EXEC_OUTPUT_STREAM_STDERR) ||
		len(output.GetData()) == 0 || len(output.GetData()) > podExecChunkBytes ||
		*received > maximum || uint64(len(output.GetData())) > maximum-*received {
		return ErrStreamProtocol
	}
	*received += uint64(len(output.GetData()))
	return nil
}

func validatePodExecExit(exit *agentv1.PodExecExit, outputBytes uint64, maximum uint64) error {
	if exit == nil || exit.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED ||
		exit.GetOutputBytes() != outputBytes || outputBytes > maximum {
		return ErrStreamProtocol
	}
	if exit.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		if exit.GetExitCode() < 0 || exit.GetReason() != "" || exit.GetMessage() != "" ||
			exit.GetOutputLimitReached() {
			return ErrStreamProtocol
		}
	} else if exit.GetExitCode() != 0 || exit.GetReason() == "" || exit.GetMessage() == "" {
		return ErrStreamProtocol
	}
	if exit.GetOutputLimitReached() &&
		(exit.GetResult() != agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED || outputBytes != maximum) {
		return ErrStreamProtocol
	}
	return nil
}

func validPodExecSize(columns uint32, rows uint32) bool {
	return columns > 0 && columns <= MaxPodExecDimension &&
		rows > 0 && rows <= MaxPodExecDimension
}

func receivePodExecInput(
	ctx context.Context,
	stream io.Reader,
	stdin *io.PipeWriter,
	sizes *podExecSizeQueue,
	maximum uint64,
) error {
	var received uint64
	closed := false
	for {
		frame := &agentv1.PodExecFrame{}
		if err := ReadMessage(stream, frame); err != nil {
			return err
		}
		if err := validatePodExecClientFrame(frame, &received, maximum); err != nil {
			return err
		}
		switch message := frame.GetMessage().(type) {
		case *agentv1.PodExecFrame_Input:
			if closed {
				return ErrStreamProtocol
			}
			if _, err := stdin.Write(message.Input.GetData()); err != nil {
				return err
			}
		case *agentv1.PodExecFrame_Resize:
			sizes.Push(message.Resize.GetColumns(), message.Resize.GetRows())
		case *agentv1.PodExecFrame_CloseInput:
			if closed {
				return ErrStreamProtocol
			}
			closed = true
			if err := stdin.Close(); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

type podExecSizeQueue struct {
	ctx    context.Context
	values chan PodExecSize
}

func newPodExecSizeQueue(ctx context.Context, columns uint32, rows uint32) *podExecSizeQueue {
	queue := &podExecSizeQueue{ctx: ctx, values: make(chan PodExecSize, 1)}
	queue.Push(columns, rows)
	return queue
}

func (queue *podExecSizeQueue) Push(columns uint32, rows uint32) {
	value := PodExecSize{Columns: columns, Rows: rows}
	select {
	case <-queue.values:
	default:
	}
	select {
	case queue.values <- value:
	default:
	}
}

func (queue *podExecSizeQueue) Next() *PodExecSize {
	select {
	case value := <-queue.values:
		return &value
	case <-queue.ctx.Done():
		return nil
	}
}

type podExecFrameWriter struct {
	stream  io.Writer
	maximum uint64
	ready   chan struct{}
	once    sync.Once
	mutex   sync.Mutex
	bytes   atomic.Uint64
	limit   atomic.Bool
}

func (writer *podExecFrameWriter) output(stream agentv1.PodExecOutputStream) io.Writer {
	return &podExecOutputWriter{parent: writer, stream: stream}
}

func (writer *podExecFrameWriter) write(frame *agentv1.PodExecFrame) error {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return WriteMessage(writer.stream, frame)
}

func (writer *podExecFrameWriter) writeOutput(
	stream agentv1.PodExecOutputStream,
	data []byte,
) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	current := writer.bytes.Load()
	remaining := writer.maximum - min(current, writer.maximum)
	allowed := min(uint64(len(data)), remaining)
	if allowed > 0 {
		if err := WriteMessage(writer.stream, &agentv1.PodExecFrame{
			Message: &agentv1.PodExecFrame_Output{Output: &agentv1.PodExecOutput{
				Stream: stream,
				Data:   append([]byte(nil), data[:allowed]...),
			}},
		}); err != nil {
			return 0, err
		}
		writer.bytes.Add(allowed)
	}
	if allowed < uint64(len(data)) {
		writer.limit.Store(true)
		return int(allowed), ErrPodExecOutputLimit
	}
	return int(allowed), nil
}

func (writer *podExecFrameWriter) release() {
	writer.once.Do(func() { close(writer.ready) })
}

type podExecOutputWriter struct {
	parent *podExecFrameWriter
	stream agentv1.PodExecOutputStream
}

func (writer *podExecOutputWriter) Write(data []byte) (int, error) {
	<-writer.parent.ready
	written := 0
	for len(data) > 0 {
		chunk := data
		if len(chunk) > podExecChunkBytes {
			chunk = chunk[:podExecChunkBytes]
		}
		chunkWritten, err := writer.parent.writeOutput(writer.stream, chunk)
		written += chunkWritten
		if err != nil {
			return written, err
		}
		data = data[len(chunk):]
	}
	return written, nil
}
