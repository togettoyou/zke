package agentprotocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	MaxPodPortForwardBytes   uint64 = 1024 * 1024 * 1024
	podPortForwardChunkBytes        = 32 * 1024
)

var (
	ErrPodPortForwardClientLimit = errors.New("Pod port forward client byte limit reached")
	ErrPodPortForwardPodLimit    = errors.New("Pod port forward Pod byte limit reached")
)

type PodPortForwardConnection interface {
	io.ReadWriteCloser
}

type PodPortForwardHandler func(
	context.Context,
	*agentv1.PodPortForwardRequest,
) (*agentv1.PodPortForwardResponse, PodPortForwardConnection, error)

type PodPortForwardPeer interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
}

func PodPortForwardStreamHandler(
	maxClientBytes uint64,
	maxPodBytes uint64,
	handler PodPortForwardHandler,
) IncomingStreamHandler {
	return func(ctx context.Context, stream *quic.Stream, header *agentv1.StreamHeader) error {
		if handler == nil {
			return &StreamFailure{Code: StreamErrorUnsupported, Err: ErrStreamUnsupported}
		}
		request := &agentv1.PodPortForwardRequest{}
		if err := ReadMessage(stream, request); err != nil {
			return fmt.Errorf("%w: read PodPortForwardRequest: %w", ErrStreamProtocol, err)
		}
		if validatePodPortForwardRequest(header, request) != nil || maxClientBytes == 0 || maxPodBytes == 0 ||
			request.GetMaxClientBytes() > maxClientBytes || request.GetMaxPodBytes() > maxPodBytes {
			return ErrStreamProtocol
		}
		forward, cancel := context.WithCancel(ctx)
		defer cancel()
		response, connection, err := handler(forward, request)
		if err != nil {
			return err
		}
		if connection != nil {
			defer connection.Close()
		}
		if validatePodPortForwardResponse(response, request, connection != nil) != nil {
			return ErrStreamProtocol
		}
		if err := WriteMessage(stream, response); err != nil || response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
			return err
		}
		return servePodPortForward(forward, cancel, stream, connection, request)
	}
}

func DoPodPortForward(
	ctx context.Context,
	connection *quic.Conn,
	header *agentv1.StreamHeader,
	request *agentv1.PodPortForwardRequest,
	peer PodPortForwardPeer,
) (*agentv1.PodPortForwardResponse, *agentv1.PodPortForwardExit, error) {
	if connection == nil || peer == nil || validateStreamHeader(header) != nil ||
		validatePodPortForwardRequest(header, request) != nil {
		return nil, nil, ErrStreamProtocol
	}
	stream, err := connection.OpenStreamSync(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer stream.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := stream.SetDeadline(deadline); err != nil {
			return nil, nil, err
		}
	}
	if err := WriteMessage(stream, header); err != nil {
		return nil, nil, err
	}
	if err := WriteMessage(stream, request); err != nil {
		return nil, nil, err
	}
	response := &agentv1.PodPortForwardResponse{}
	if err := ReadMessage(stream, response); err != nil {
		return nil, nil, err
	}
	if err := validatePodPortForwardResponse(response, request, response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK); err != nil ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		return response, nil, err
	}
	forward, cancel := context.WithCancel(ctx)
	defer cancel()
	inputErrors := make(chan error, 1)
	go func() {
		var inputBytes uint64
		for {
			data, readErr := peer.Read(forward)
			if readErr != nil {
				_ = WriteMessage(stream, &agentv1.PodPortForwardFrame{
					Message: &agentv1.PodPortForwardFrame_CloseInput{CloseInput: &agentv1.PodPortForwardCloseInput{}},
				})
				inputErrors <- readErr
				return
			}
			if len(data) == 0 {
				inputErrors <- ErrStreamProtocol
				return
			}
			if inputBytes+uint64(len(data)) > request.GetMaxClientBytes() {
				inputErrors <- ErrPodPortForwardClientLimit
				return
			}
			for offset := 0; offset < len(data); offset += podPortForwardChunkBytes {
				end := min(offset+podPortForwardChunkBytes, len(data))
				if writeErr := WriteMessage(stream, &agentv1.PodPortForwardFrame{
					Message: &agentv1.PodPortForwardFrame_Data{Data: &agentv1.PodPortForwardData{Data: data[offset:end]}},
				}); writeErr != nil {
					inputErrors <- writeErr
					return
				}
			}
			inputBytes += uint64(len(data))
		}
	}()
	type receivedFrame struct {
		frame *agentv1.PodPortForwardFrame
		err   error
	}
	received := make(chan receivedFrame, 1)
	go func() {
		for {
			frame := &agentv1.PodPortForwardFrame{}
			err := ReadMessage(stream, frame)
			select {
			case received <- receivedFrame{frame: frame, err: err}:
			case <-forward.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case inputErr := <-inputErrors:
			if inputErr != nil && !errors.Is(inputErr, io.EOF) {
				return response, nil, inputErr
			}
			inputErrors = nil
		case incoming := <-received:
			if incoming.err != nil {
				return response, nil, incoming.err
			}
			if exit := incoming.frame.GetExit(); exit != nil {
				return response, exit, nil
			}
			dataFrame := incoming.frame.GetData()
			if dataFrame == nil || len(dataFrame.GetData()) == 0 {
				return response, nil, ErrStreamProtocol
			}
			if err := peer.Write(forward, dataFrame.GetData()); err != nil {
				return response, nil, err
			}
		case <-forward.Done():
			return response, nil, forward.Err()
		}
	}
}

func servePodPortForward(
	ctx context.Context,
	cancel context.CancelFunc,
	stream *quic.Stream,
	connection PodPortForwardConnection,
	request *agentv1.PodPortForwardRequest,
) error {
	clientBytes := &atomic.Uint64{}
	podBytes := &atomic.Uint64{}
	errorsChannel := make(chan error, 2)
	go func() {
		errorsChannel <- receivePortForwardFrames(ctx, stream, connection, request.GetMaxClientBytes(), clientBytes)
	}()
	go func() {
		errorsChannel <- sendPortForwardFrames(ctx, stream, connection, request.GetMaxPodBytes(), podBytes)
	}()
	firstErr := <-errorsChannel
	cancel()
	_ = connection.Close()
	stream.CancelRead(StreamErrorCanceled)
	// Both pumps must be gone before the terminal frame is written; otherwise a
	// last data frame and the exit frame can concurrently corrupt framing.
	secondErr := <-errorsChannel
	err := meaningfulPodPortForwardError(firstErr, secondErr)
	exit := &agentv1.PodPortForwardExit{
		Result:      agentv1.ResultCode_RESULT_CODE_OK,
		ClientBytes: clientBytes.Load(),
		PodBytes:    podBytes.Load(),
	}
	switch {
	case errors.Is(err, ErrPodPortForwardClientLimit):
		exit.Result = agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED
		exit.Reason = "ClientByteLimitReached"
		exit.Message = "Pod port forward client byte limit reached"
		exit.ClientLimitReached = true
	case errors.Is(err, ErrPodPortForwardPodLimit):
		exit.Result = agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED
		exit.Reason = "PodByteLimitReached"
		exit.Message = "Pod port forward Pod byte limit reached"
		exit.PodLimitReached = true
	case err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled):
		exit.Result = agentv1.ResultCode_RESULT_CODE_INTERNAL
		exit.Reason = "ForwardingFailed"
		exit.Message = "Pod port forwarding ended unexpectedly"
	}
	return WriteMessage(stream, &agentv1.PodPortForwardFrame{
		Message: &agentv1.PodPortForwardFrame_Exit{Exit: exit},
	})
}

func meaningfulPodPortForwardError(first, second error) error {
	for _, err := range []error{first, second} {
		if errors.Is(err, ErrPodPortForwardClientLimit) || errors.Is(err, ErrPodPortForwardPodLimit) {
			return err
		}
	}
	// The first pump decides the session lifecycle. Once it ends normally we
	// cancel and close the shared TCP connection, so the other pump is expected
	// to report a close error that must not turn a normal EOF into a failure.
	if first == nil || errors.Is(first, io.EOF) || errors.Is(first, context.Canceled) {
		return first
	}
	for _, err := range []error{first, second} {
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return first
}

func receivePortForwardFrames(ctx context.Context, stream io.Reader, destination io.Writer, maximum uint64, count *atomic.Uint64) error {
	for {
		frame := &agentv1.PodPortForwardFrame{}
		if err := ReadMessage(stream, frame); err != nil {
			return err
		}
		if frame.GetCloseInput() != nil {
			return io.EOF
		}
		dataFrame := frame.GetData()
		if dataFrame == nil || len(dataFrame.GetData()) == 0 || len(dataFrame.GetData()) > podPortForwardChunkBytes {
			return ErrStreamProtocol
		}
		data := dataFrame.GetData()
		if count.Load()+uint64(len(data)) > maximum {
			return ErrPodPortForwardClientLimit
		}
		if _, err := destination.Write(data); err != nil {
			return err
		}
		count.Add(uint64(len(data)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

func sendPortForwardFrames(ctx context.Context, stream io.Writer, source io.Reader, maximum uint64, count *atomic.Uint64) error {
	buffer := make([]byte, podPortForwardChunkBytes)
	for {
		read, err := source.Read(buffer)
		if read > 0 {
			if count.Load()+uint64(read) > maximum {
				return ErrPodPortForwardPodLimit
			}
			if writeErr := WriteMessage(stream, &agentv1.PodPortForwardFrame{
				Message: &agentv1.PodPortForwardFrame_Data{Data: &agentv1.PodPortForwardData{Data: buffer[:read]}},
			}); writeErr != nil {
				return writeErr
			}
			count.Add(uint64(read))
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

func validatePodPortForwardRequest(header *agentv1.StreamHeader, request *agentv1.PodPortForwardRequest) error {
	if header == nil || header.GetKind() != agentv1.StreamKind_STREAM_KIND_POD_PORT_FORWARD ||
		request == nil || len(k8svalidation.IsDNS1123Label(request.GetNamespace())) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(request.GetPodName())) != 0 || request.GetPodUid() == "" ||
		request.GetPort() == 0 || request.GetPort() > 65535 || request.GetMaxClientBytes() == 0 ||
		request.GetMaxClientBytes() > MaxPodPortForwardBytes || request.GetMaxPodBytes() == 0 ||
		request.GetMaxPodBytes() > MaxPodPortForwardBytes {
		return ErrStreamProtocol
	}
	return nil
}

func validatePodPortForwardResponse(response *agentv1.PodPortForwardResponse, request *agentv1.PodPortForwardRequest, hasConnection bool) error {
	if response == nil || response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED ||
		(response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK) != hasConnection {
		return ErrStreamProtocol
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK &&
		(response.GetPodUid() != request.GetPodUid() || response.GetPort() != request.GetPort()) {
		return ErrStreamProtocol
	}
	return nil
}
