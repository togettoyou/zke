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
	DefaultMaxResourceWatchEventBytes uint64 = 48 * 1024
	DefaultMaxResourceWatchTotalBytes uint64 = 32 * 1024 * 1024
	DefaultMaxResourceWatchEvents     uint64 = 10_000
	MaxResourceWatchInitialEvents     uint32 = 500
	maxResourceWatchSelectorLength           = 2048
	maxResourceWatchVersionLength            = 256
)

// ResourceWatchSource yields already serialized Kubernetes objects. Returning
// SourceError preserves Kubernetes watch termination details in the trailer.
type ResourceWatchSource interface {
	Next(context.Context) (*agentv1.ResourceWatchEvent, error)
	Close() error
}

type ResourceWatchSourceError struct {
	Result               agentv1.ResultCode
	KubernetesStatusCode int32
	Reason               string
	Message              string
}

func (failure *ResourceWatchSourceError) Error() string {
	if failure == nil {
		return "Resource Watch failed"
	}
	return failure.Message
}

type ResourceWatchHandler func(
	context.Context,
	*agentv1.ResourceWatchRequest,
) (*agentv1.ResourceWatchResponse, ResourceWatchSource, error)

type ResourceWatchSink interface {
	Start(*agentv1.ResourceWatchResponse) error
	Event(*agentv1.ResourceWatchEvent) error
}

func ResourceWatchStreamHandler(handler ResourceWatchHandler) IncomingStreamHandler {
	return func(ctx context.Context, stream *quic.Stream, header *agentv1.StreamHeader) error {
		if handler == nil {
			return &StreamFailure{Code: StreamErrorUnsupported, Err: ErrStreamUnsupported}
		}
		request := &agentv1.ResourceWatchRequest{}
		if err := ReadMessage(stream, request); err != nil {
			return fmt.Errorf("%w: read ResourceWatchRequest: %w", ErrStreamProtocol, err)
		}
		if err := validateResourceWatchRequest(header, request); err != nil {
			return err
		}
		if err := requireStreamEOF(stream); err != nil {
			return err
		}
		response, source, err := handler(ctx, request)
		if err != nil {
			return err
		}
		if err := validateResourceWatchResponse(response, source != nil); err != nil {
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
		defer func() { _ = source.Close() }()
		return relayResourceWatch(ctx, stream, source, request, response.GetResourceVersion())
	}
}

func relayResourceWatch(
	ctx context.Context,
	destination io.Writer,
	source ResourceWatchSource,
	request *agentv1.ResourceWatchRequest,
	initialResourceVersion string,
) error {
	var eventsSent, bytesSent uint64
	lastVersion := initialResourceVersion
	if lastVersion == "" {
		lastVersion = request.GetResourceVersion()
	}
	for {
		if eventsSent >= request.GetMaxEvents() || bytesSent >= request.GetMaxTotalBytes() {
			return writeResourceWatchTrailer(destination, &agentv1.ResourceWatchTrailer{
				Result: agentv1.ResultCode_RESULT_CODE_OK, EventsSent: eventsSent,
				BytesSent: bytesSent, LastResourceVersion: lastVersion, LimitReached: true,
			})
		}
		event, err := source.Next(ctx)
		if err != nil {
			trailer := resourceWatchTrailerForError(err, ctx, eventsSent, bytesSent, lastVersion)
			return writeResourceWatchTrailer(destination, trailer)
		}
		if event == nil || !validResourceWatchEventType(event.GetType()) ||
			len(event.GetObject()) == 0 || uint64(len(event.GetObject())) > request.GetMaxEventBytes() ||
			uint64(len(event.GetObject())) > request.GetMaxTotalBytes()-bytesSent ||
			len(event.GetResourceVersion()) > maxResourceWatchVersionLength {
			return writeResourceWatchTrailer(destination, resourceWatchTrailerForError(
				&ResourceWatchSourceError{Result: agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
					Reason: "WatchEventTooLarge", Message: "Kubernetes watch event exceeds configured limits"},
				ctx, eventsSent, bytesSent, lastVersion,
			))
		}
		if err := WriteMessage(destination, &agentv1.ResourceWatchFrame{
			Message: &agentv1.ResourceWatchFrame_Event{Event: event},
		}); err != nil {
			return err
		}
		eventsSent++
		bytesSent += uint64(len(event.GetObject()))
		if event.GetResourceVersion() != "" {
			lastVersion = event.GetResourceVersion()
		}
	}
}

func writeResourceWatchTrailer(destination io.Writer, trailer *agentv1.ResourceWatchTrailer) error {
	return WriteMessage(destination, &agentv1.ResourceWatchFrame{
		Message: &agentv1.ResourceWatchFrame_Trailer{Trailer: trailer},
	})
}

func resourceWatchTrailerForError(
	err error, ctx context.Context, events, bytes uint64, lastVersion string,
) *agentv1.ResourceWatchTrailer {
	trailer := &agentv1.ResourceWatchTrailer{
		Result: agentv1.ResultCode_RESULT_CODE_INTERNAL, Reason: "WatchFailed",
		Message: "Kubernetes resource watch failed", EventsSent: events,
		BytesSent: bytes, LastResourceVersion: lastVersion,
	}
	if errors.Is(err, io.EOF) {
		trailer.Result, trailer.Reason, trailer.Message = agentv1.ResultCode_RESULT_CODE_OK, "", ""
		return trailer
	}
	var sourceError *ResourceWatchSourceError
	if errors.As(err, &sourceError) &&
		sourceError.Result != agentv1.ResultCode_RESULT_CODE_UNSPECIFIED &&
		sourceError.Reason != "" && sourceError.Message != "" {
		trailer.Result = sourceError.Result
		trailer.KubernetesStatusCode = sourceError.KubernetesStatusCode
		trailer.Reason = sourceError.Reason
		trailer.Message = sourceError.Message
		return trailer
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		trailer.Result, trailer.Reason, trailer.Message = agentv1.ResultCode_RESULT_CODE_TIMEOUT, "Timeout", "Kubernetes resource watch timed out"
	} else if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		trailer.Result, trailer.Reason, trailer.Message = agentv1.ResultCode_RESULT_CODE_CANCELED, "Canceled", "Kubernetes resource watch was canceled"
	}
	return trailer
}

func DoResourceWatch(
	ctx context.Context,
	connection *quic.Conn,
	header *agentv1.StreamHeader,
	request *agentv1.ResourceWatchRequest,
	sink ResourceWatchSink,
) (*agentv1.ResourceWatchResponse, *agentv1.ResourceWatchTrailer, error) {
	if connection == nil || sink == nil {
		return nil, nil, errors.New("Agent connection and Resource Watch sink are required")
	}
	if err := validateStreamHeader(header); err != nil {
		return nil, nil, err
	}
	if header.GetKind() != agentv1.StreamKind_STREAM_KIND_RESOURCE_WATCH || validateResourceWatchRequest(header, request) != nil {
		return nil, nil, ErrStreamProtocol
	}
	stream, err := connection.OpenStreamSync(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("open Agent Resource Watch Stream: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			AbortStream(stream, StreamErrorCanceled)
		}
	}()
	stopCancellation := context.AfterFunc(ctx, func() { AbortStream(stream, streamErrorCode(ctx.Err(), ctx)) })
	defer stopCancellation()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(time.Duration(header.GetTimeoutMillis()) * time.Millisecond)
	}
	if err := stream.SetDeadline(deadline); err != nil {
		return nil, nil, err
	}
	if err := WriteMessage(stream, header); err != nil {
		return nil, nil, err
	}
	if err := WriteMessage(stream, request); err != nil {
		return nil, nil, err
	}
	if err := stream.Close(); err != nil {
		return nil, nil, fmt.Errorf("finish Agent Resource Watch request: %w", err)
	}
	response := &agentv1.ResourceWatchResponse{}
	if err := ReadMessage(stream, response); err != nil {
		return nil, nil, err
	}
	if err := validateResourceWatchResponse(response, response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK); err != nil {
		return nil, nil, err
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		if err := requireStreamEOF(stream); err != nil {
			return nil, nil, err
		}
		finished = true
		return response, nil, nil
	}
	if err := sink.Start(response); err != nil {
		return response, nil, err
	}
	var events, bytes uint64
	lastVersion := request.GetResourceVersion()
	for {
		frame := &agentv1.ResourceWatchFrame{}
		if err := ReadMessage(stream, frame); err != nil {
			return response, nil, err
		}
		switch message := frame.GetMessage().(type) {
		case *agentv1.ResourceWatchFrame_Event:
			event := message.Event
			if event == nil || !validResourceWatchEventType(event.GetType()) ||
				len(event.GetObject()) == 0 || uint64(len(event.GetObject())) > request.GetMaxEventBytes() ||
				uint64(len(event.GetObject())) > request.GetMaxTotalBytes()-bytes || events >= request.GetMaxEvents() {
				return response, nil, ErrStreamProtocol
			}
			if err := sink.Event(event); err != nil {
				return response, nil, err
			}
			events++
			bytes += uint64(len(event.GetObject()))
			if event.GetResourceVersion() != "" {
				lastVersion = event.GetResourceVersion()
			}
		case *agentv1.ResourceWatchFrame_Trailer:
			trailer := message.Trailer
			if trailer == nil || trailer.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED ||
				trailer.GetEventsSent() != events || trailer.GetBytesSent() != bytes ||
				trailer.GetLastResourceVersion() != lastVersion ||
				(trailer.GetResult() == agentv1.ResultCode_RESULT_CODE_OK && (trailer.GetReason() != "" || trailer.GetMessage() != "")) ||
				(trailer.GetResult() != agentv1.ResultCode_RESULT_CODE_OK && (trailer.GetReason() == "" || trailer.GetMessage() == "")) {
				return response, nil, ErrStreamProtocol
			}
			if trailer.GetLimitReached() && (trailer.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
				(events != request.GetMaxEvents() && bytes != request.GetMaxTotalBytes())) {
				return response, nil, ErrStreamProtocol
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

func validResourceWatchEventType(eventType agentv1.ResourceWatchEventType) bool {
	switch eventType {
	case agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_ADDED,
		agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_MODIFIED,
		agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_DELETED,
		agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_BOOKMARK:
		return true
	default:
		return false
	}
}

func validateResourceWatchRequest(header *agentv1.StreamHeader, request *agentv1.ResourceWatchRequest) error {
	resource := request.GetResource()
	if request == nil || header.GetIdempotencyKey() != "" || resource == nil ||
		resource.GetVersion() == "" || resource.GetResource() == "" ||
		len(resource.GetGroup()) > 253 || len(resource.GetVersion()) > 63 || len(resource.GetResource()) > 253 ||
		len(k8svalidation.IsDNS1123Label(request.GetNamespace())) != 0 ||
		len(request.GetFieldSelector()) > maxResourceWatchSelectorLength ||
		strings.TrimSpace(request.GetFieldSelector()) != request.GetFieldSelector() ||
		len(request.GetResourceVersion()) > maxResourceWatchVersionLength ||
		(!request.GetIncludeInitialEvents() && !request.GetFollow()) ||
		request.GetInitialEventLimit() == 0 || request.GetInitialEventLimit() > MaxResourceWatchInitialEvents ||
		request.GetMaxEventBytes() == 0 || request.GetMaxEventBytes() > DefaultMaxResourceWatchEventBytes ||
		request.GetMaxTotalBytes() == 0 || request.GetMaxTotalBytes() > DefaultMaxResourceWatchTotalBytes ||
		request.GetMaxEvents() == 0 || request.GetMaxEvents() > DefaultMaxResourceWatchEvents {
		return ErrStreamProtocol
	}
	return nil
}

func validateResourceWatchResponse(response *agentv1.ResourceWatchResponse, hasSource bool) error {
	if response == nil || response.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrStreamProtocol
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		if !hasSource || response.GetKubernetesStatusCode() < 200 || response.GetKubernetesStatusCode() >= 300 ||
			response.GetContentType() != "application/json" || response.GetReason() != "" || response.GetMessage() != "" {
			return ErrStreamProtocol
		}
	} else if hasSource || response.GetReason() == "" || response.GetMessage() == "" ||
		response.GetContentType() != "" || response.GetResourceVersion() != "" || response.GetInitialEventsTruncated() {
		return ErrStreamProtocol
	}
	return nil
}
