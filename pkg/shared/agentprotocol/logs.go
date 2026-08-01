package agentprotocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	DefaultMaxPodLogBytes uint64 = 16 * 1024 * 1024
	MaxPodLogTailLines    int64  = 5000
	MaxPodLogSinceSeconds int64  = 7 * 24 * 60 * 60
	podLogChunkBytes             = 32 * 1024
	maxPodUIDLength              = 256
	maxEmptyPodLogReads          = 100
	podLogContentType            = "text/plain"
)

type PodLogsHandler func(
	context.Context,
	*agentv1.PodLogsRequest,
) (*agentv1.PodLogsResponse, io.ReadCloser, error)

// PodLogsStreamHandler serves one bounded Pod log request. Data is framed so
// that a clean Kubernetes EOF, an Agent-side failure and a configured byte
// limit remain distinguishable without buffering a snapshot or a follow
// stream in memory.
func PodLogsStreamHandler(
	maxBytes uint64,
	handler PodLogsHandler,
) IncomingStreamHandler {
	if maxBytes == 0 {
		maxBytes = DefaultMaxPodLogBytes
	}
	return func(
		ctx context.Context,
		stream *quic.Stream,
		header *agentv1.StreamHeader,
	) error {
		if handler == nil {
			return &StreamFailure{Code: StreamErrorUnsupported, Err: ErrStreamUnsupported}
		}
		request := &agentv1.PodLogsRequest{}
		if err := ReadMessage(stream, request); err != nil {
			return fmt.Errorf("%w: read PodLogsRequest: %w", ErrStreamProtocol, err)
		}
		if err := validatePodLogsRequest(header, request, maxBytes); err != nil {
			return err
		}
		if err := requireStreamEOF(stream); err != nil {
			return err
		}

		response, source, err := handler(ctx, request)
		if err != nil {
			return err
		}
		if err := validatePodLogsResponse(response, request, source != nil); err != nil {
			if source != nil {
				_ = source.Close()
			}
			return err
		}
		if err := WriteMessage(stream, response); err != nil {
			if source != nil {
				_ = source.Close()
			}
			return err
		}
		if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
			return nil
		}
		defer source.Close()
		return relayPodLogs(ctx, stream, source, request.GetMaxBytes())
	}
}

func relayPodLogs(
	ctx context.Context,
	stream io.Writer,
	source io.Reader,
	maxBytes uint64,
) error {
	buffer := make([]byte, podLogChunkBytes)
	var sent uint64
	emptyReads := 0
	for {
		remaining := maxBytes - sent
		if remaining == 0 {
			return WriteMessage(stream, &agentv1.PodLogsFrame{
				Message: &agentv1.PodLogsFrame_Trailer{Trailer: &agentv1.PodLogsTrailer{
					Result:       agentv1.ResultCode_RESULT_CODE_OK,
					BytesSent:    sent,
					LimitReached: true,
				}},
			})
		}
		readBuffer := buffer
		if remaining < uint64(len(readBuffer)) {
			readBuffer = readBuffer[:remaining]
		}
		n, readErr := source.Read(readBuffer)
		if n < 0 || n > len(readBuffer) {
			return errors.New("Pod log source returned an invalid byte count")
		}
		if n > 0 {
			emptyReads = 0
			if err := WriteMessage(stream, &agentv1.PodLogsFrame{
				Message: &agentv1.PodLogsFrame_Chunk{Chunk: &agentv1.PodLogsChunk{
					Data: append([]byte(nil), readBuffer[:n]...),
				}},
			}); err != nil {
				return err
			}
			sent += uint64(n)
		} else if readErr == nil {
			emptyReads++
			if emptyReads >= maxEmptyPodLogReads {
				readErr = io.ErrNoProgress
			}
		}
		if readErr == nil {
			select {
			case <-ctx.Done():
				readErr = ctx.Err()
			default:
			}
		}
		if readErr == nil {
			continue
		}
		trailer := podLogsTrailerForError(readErr, ctx, sent)
		if errors.Is(readErr, io.EOF) && sent == maxBytes {
			trailer.LimitReached = true
		}
		if err := WriteMessage(stream, &agentv1.PodLogsFrame{
			Message: &agentv1.PodLogsFrame_Trailer{Trailer: trailer},
		}); err != nil {
			return err
		}
		return nil
	}
}

func podLogsTrailerForError(
	err error,
	ctx context.Context,
	bytesSent uint64,
) *agentv1.PodLogsTrailer {
	trailer := &agentv1.PodLogsTrailer{
		Result:    agentv1.ResultCode_RESULT_CODE_INTERNAL,
		Reason:    "LogStreamFailed",
		Message:   "Kubernetes Pod log stream failed",
		BytesSent: bytesSent,
	}
	if errors.Is(err, io.EOF) {
		trailer.Result = agentv1.ResultCode_RESULT_CODE_OK
		trailer.Reason = ""
		trailer.Message = ""
		return trailer
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		errors.Is(err, context.DeadlineExceeded) {
		trailer.Result = agentv1.ResultCode_RESULT_CODE_TIMEOUT
		trailer.Reason = "Timeout"
		trailer.Message = "Kubernetes Pod log stream timed out"
	} else if errors.Is(ctx.Err(), context.Canceled) ||
		errors.Is(err, context.Canceled) {
		trailer.Result = agentv1.ResultCode_RESULT_CODE_CANCELED
		trailer.Reason = "Canceled"
		trailer.Message = "Kubernetes Pod log stream was canceled"
	}
	return trailer
}

// DoPodLogs opens one Pod Logs Stream and writes only validated log chunks to
// destination. An error response is returned before destination is touched,
// allowing the HTTP layer to retain a structured non-200 response.
func DoPodLogs(
	ctx context.Context,
	connection *quic.Conn,
	header *agentv1.StreamHeader,
	request *agentv1.PodLogsRequest,
	destination io.Writer,
	maxBytes uint64,
) (*agentv1.PodLogsResponse, *agentv1.PodLogsTrailer, error) {
	if connection == nil {
		return nil, nil, errors.New("Agent Connection is required")
	}
	if destination == nil {
		return nil, nil, errors.New("Pod log destination is required")
	}
	if maxBytes == 0 {
		maxBytes = DefaultMaxPodLogBytes
	}
	if err := validateStreamHeader(header); err != nil {
		return nil, nil, err
	}
	if header.GetKind() != agentv1.StreamKind_STREAM_KIND_POD_LOGS {
		return nil, nil, ErrStreamProtocol
	}
	if err := validatePodLogsRequest(header, request, maxBytes); err != nil {
		return nil, nil, err
	}

	stream, err := connection.OpenStreamSync(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("open Agent Pod Logs Stream: %w", err)
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
	if err := stream.Close(); err != nil {
		return nil, nil, fmt.Errorf("finish Agent Pod Logs request: %w", err)
	}

	response := &agentv1.PodLogsResponse{}
	if err := ReadMessage(stream, response); err != nil {
		return nil, nil, err
	}
	if err := validatePodLogsResponse(response, request, response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK); err != nil {
		return nil, nil, err
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		if err := requireStreamEOF(stream); err != nil {
			return nil, nil, err
		}
		finished = true
		return response, nil, nil
	}

	var received uint64
	for {
		frame := &agentv1.PodLogsFrame{}
		if err := ReadMessage(stream, frame); err != nil {
			return response, nil, err
		}
		switch message := frame.GetMessage().(type) {
		case *agentv1.PodLogsFrame_Chunk:
			data := message.Chunk.GetData()
			if len(data) == 0 || len(data) > podLogChunkBytes ||
				uint64(len(data)) > maxBytes-received {
				return response, nil, ErrStreamProtocol
			}
			written, err := destination.Write(data)
			received += uint64(written)
			if err != nil {
				return response, nil, err
			}
			if written != len(data) {
				return response, nil, io.ErrShortWrite
			}
		case *agentv1.PodLogsFrame_Trailer:
			trailer := message.Trailer
			if err := validatePodLogsTrailer(trailer, received, maxBytes); err != nil {
				return response, nil, err
			}
			if err := requireStreamEOF(stream); err != nil {
				return response, nil, err
			}
			finished = true
			return response, trailer, nil
		default:
			return response, nil, ErrStreamProtocol
		}
	}
}

func validatePodLogsRequest(
	header *agentv1.StreamHeader,
	request *agentv1.PodLogsRequest,
	maxBytes uint64,
) error {
	if request == nil || header.GetIdempotencyKey() != "" ||
		len(k8svalidation.IsDNS1123Label(request.GetNamespace())) != 0 ||
		len(k8svalidation.IsDNS1123Subdomain(request.GetPodName())) != 0 ||
		len(k8svalidation.IsDNS1123Label(request.GetContainer())) != 0 ||
		request.GetPodUid() == "" ||
		len(request.GetPodUid()) > maxPodUIDLength ||
		strings.TrimSpace(request.GetPodUid()) != request.GetPodUid() ||
		request.GetMaxBytes() == 0 || request.GetMaxBytes() > maxBytes {
		return ErrStreamProtocol
	}
	if request.TailLines != nil &&
		(request.GetTailLines() < 0 || request.GetTailLines() > MaxPodLogTailLines) {
		return ErrStreamProtocol
	}
	if request.SinceSeconds != nil &&
		(request.GetSinceSeconds() < 1 || request.GetSinceSeconds() > MaxPodLogSinceSeconds) {
		return ErrStreamProtocol
	}
	return nil
}

func validatePodLogsResponse(
	response *agentv1.PodLogsResponse,
	request *agentv1.PodLogsRequest,
	hasSource bool,
) error {
	if response == nil || response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrStreamProtocol
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		if !hasSource || response.GetKubernetesStatusCode() < 200 ||
			response.GetKubernetesStatusCode() >= 300 ||
			response.GetPodUid() != request.GetPodUid() ||
			response.GetContainer() != request.GetContainer() ||
			response.GetFollow() != request.GetFollow() ||
			response.GetContentType() != podLogContentType ||
			response.GetReason() != "" || response.GetMessage() != "" {
			return ErrStreamProtocol
		}
		return nil
	}
	if hasSource || response.GetReason() == "" || response.GetMessage() == "" ||
		response.GetPodUid() != "" || response.GetContainer() != "" ||
		response.GetFollow() || response.GetContentType() != "" {
		return ErrStreamProtocol
	}
	return nil
}

func validatePodLogsTrailer(
	trailer *agentv1.PodLogsTrailer,
	received uint64,
	maxBytes uint64,
) error {
	if trailer == nil || trailer.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED ||
		trailer.GetBytesSent() != received || received > maxBytes {
		return ErrStreamProtocol
	}
	if trailer.GetLimitReached() {
		if trailer.GetResult() != agentv1.ResultCode_RESULT_CODE_OK || received != maxBytes {
			return ErrStreamProtocol
		}
	}
	if trailer.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		if trailer.GetReason() != "" || trailer.GetMessage() != "" {
			return ErrStreamProtocol
		}
	} else if trailer.GetReason() == "" || trailer.GetMessage() == "" {
		return ErrStreamProtocol
	}
	return nil
}
