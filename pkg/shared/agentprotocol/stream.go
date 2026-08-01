package agentprotocol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

const (
	StreamErrorProtocol          quic.StreamErrorCode = 1
	StreamErrorUnsupported       quic.StreamErrorCode = 2
	StreamErrorResourceExhausted quic.StreamErrorCode = 3
	StreamErrorCanceled          quic.StreamErrorCode = 4
	StreamErrorTimeout           quic.StreamErrorCode = 5
	StreamErrorInternal          quic.StreamErrorCode = 6
	StreamErrorBodyTooLarge      quic.StreamErrorCode = 7
)

const (
	DefaultStreamHeaderTimeout = 5 * time.Second
	DefaultMaxStreamTimeout    = 2 * time.Minute
	MaxIdempotencyKeyLength    = 256
)

var (
	ErrStreamProtocol          = errors.New("Agent business Stream protocol error")
	ErrStreamUnsupported       = errors.New("Agent business Stream kind is unsupported")
	ErrStreamResourceExhausted = errors.New("Agent business Stream capacity is exhausted")
	ErrStreamBodyTooLarge      = errors.New("Agent business Stream body exceeds maximum size")
)

type IncomingStreamHandler func(
	context.Context,
	*quic.Stream,
	*agentv1.StreamHeader,
) error

type StreamHandlerConfig struct {
	MaxConcurrent int
	MaxTimeout    time.Duration
	Handle        IncomingStreamHandler
}

type StreamServerConfig struct {
	HeaderTimeout time.Duration
	MaxTimeout    time.Duration
	Handlers      map[agentv1.StreamKind]StreamHandlerConfig
	OnError       func(*agentv1.StreamHeader, error)
}

type StreamServer struct {
	headerTimeout time.Duration
	maxTimeout    time.Duration
	handlers      map[agentv1.StreamKind]streamHandler
	onError       func(*agentv1.StreamHeader, error)
}

type streamHandler struct {
	handle     IncomingStreamHandler
	admissions chan struct{}
	maxTimeout time.Duration
}

type streamAcceptor interface {
	AcceptStream(context.Context) (*quic.Stream, error)
}

func NewStreamServer(config StreamServerConfig) (*StreamServer, error) {
	headerTimeout := config.HeaderTimeout
	if headerTimeout <= 0 {
		headerTimeout = DefaultStreamHeaderTimeout
	}
	maxTimeout := config.MaxTimeout
	if maxTimeout <= 0 {
		maxTimeout = DefaultMaxStreamTimeout
	}
	if headerTimeout > maxTimeout {
		return nil, errors.New("Agent business Stream header timeout exceeds maximum timeout")
	}
	handlers := make(map[agentv1.StreamKind]streamHandler, len(config.Handlers))
	for kind, configured := range config.Handlers {
		if kind == agentv1.StreamKind_STREAM_KIND_UNSPECIFIED {
			return nil, errors.New("Agent business Stream handler kind is unspecified")
		}
		if configured.Handle == nil {
			return nil, fmt.Errorf("Agent business Stream %s handler is nil", kind)
		}
		if configured.MaxConcurrent <= 0 {
			return nil, fmt.Errorf(
				"Agent business Stream %s concurrency must be greater than zero",
				kind,
			)
		}
		handlerMaxTimeout := configured.MaxTimeout
		if handlerMaxTimeout <= 0 {
			handlerMaxTimeout = maxTimeout
		}
		if handlerMaxTimeout > maxTimeout {
			return nil, fmt.Errorf(
				"Agent business Stream %s timeout exceeds the Server maximum",
				kind,
			)
		}
		if headerTimeout > handlerMaxTimeout {
			return nil, fmt.Errorf(
				"Agent business Stream %s header timeout exceeds its maximum timeout",
				kind,
			)
		}
		handlers[kind] = streamHandler{
			handle:     configured.Handle,
			admissions: make(chan struct{}, configured.MaxConcurrent),
			maxTimeout: handlerMaxTimeout,
		}
	}
	return &StreamServer{
		headerTimeout: headerTimeout,
		maxTimeout:    maxTimeout,
		handlers:      handlers,
		onError:       config.OnError,
	}, nil
}

func (server *StreamServer) Serve(ctx context.Context, acceptor streamAcceptor) error {
	if server == nil || acceptor == nil {
		return errors.New("Agent business Stream Server is not configured")
	}
	var handlers sync.WaitGroup
	defer handlers.Wait()
	for {
		stream, err := acceptor.AcceptStream(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept Agent business Stream: %w", err)
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			server.serveStream(ctx, stream)
		}()
	}
}

func (server *StreamServer) serveStream(parent context.Context, stream *quic.Stream) {
	header := &agentv1.StreamHeader{}
	if err := stream.SetReadDeadline(time.Now().Add(server.headerTimeout)); err != nil {
		AbortStream(stream, StreamErrorInternal)
		server.report(nil, err)
		return
	}
	if err := ReadMessage(stream, header); err != nil {
		AbortStream(stream, StreamErrorProtocol)
		server.report(
			nil,
			fmt.Errorf("%w: read StreamHeader: %w", ErrStreamProtocol, err),
		)
		return
	}
	if err := validateStreamHeader(header); err != nil {
		AbortStream(stream, StreamErrorProtocol)
		server.report(header, err)
		return
	}
	configured, ok := server.handlers[header.GetKind()]
	if !ok {
		AbortStream(stream, StreamErrorUnsupported)
		server.report(
			header,
			fmt.Errorf("%w: %s", ErrStreamUnsupported, header.GetKind()),
		)
		return
	}
	select {
	case configured.admissions <- struct{}{}:
		defer func() { <-configured.admissions }()
	default:
		AbortStream(stream, StreamErrorResourceExhausted)
		server.report(header, ErrStreamResourceExhausted)
		return
	}

	timeout := time.Duration(header.GetTimeoutMillis()) * time.Millisecond
	timeout = min(timeout, configured.maxTimeout)
	handlerContext, cancelHandler := context.WithTimeout(parent, timeout)
	stopStreamCancellation := context.AfterFunc(stream.Context(), cancelHandler)
	defer func() {
		stopStreamCancellation()
		cancelHandler()
	}()
	if err := stream.SetDeadline(time.Now().Add(timeout)); err != nil {
		AbortStream(stream, StreamErrorInternal)
		server.report(header, err)
		return
	}
	if err := configured.handle(handlerContext, stream, header); err != nil {
		code := streamErrorCode(err, handlerContext)
		AbortStream(stream, code)
		server.report(header, err)
		return
	}
	if handlerContext.Err() != nil {
		err := handlerContext.Err()
		AbortStream(stream, streamErrorCode(err, handlerContext))
		server.report(header, err)
		return
	}
	if err := stream.Close(); err != nil {
		server.report(header, err)
	}
}

func validateStreamHeader(header *agentv1.StreamHeader) error {
	if header == nil ||
		header.GetProtocolVersion() != ProtocolVersion ||
		header.GetKind() == agentv1.StreamKind_STREAM_KIND_UNSPECIFIED ||
		!validation.IsUUID(header.GetRequestId()) ||
		header.GetTimeoutMillis() == 0 ||
		len(header.GetIdempotencyKey()) > MaxIdempotencyKeyLength ||
		strings.TrimSpace(header.GetIdempotencyKey()) != header.GetIdempotencyKey() {
		return ErrStreamProtocol
	}
	if header.GetTimeoutMillis() >
		uint64((time.Duration(1<<63-1) / time.Millisecond)) {
		return ErrStreamProtocol
	}
	return nil
}

func streamErrorCode(err error, ctx context.Context) quic.StreamErrorCode {
	var streamFailure *StreamFailure
	if errors.As(err, &streamFailure) {
		return streamFailure.Code
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		errors.Is(err, context.DeadlineExceeded) {
		return StreamErrorTimeout
	}
	if errors.Is(ctx.Err(), context.Canceled) ||
		errors.Is(err, context.Canceled) {
		return StreamErrorCanceled
	}
	if errors.Is(err, ErrStreamBodyTooLarge) {
		return StreamErrorBodyTooLarge
	}
	if errors.Is(err, ErrStreamProtocol) {
		return StreamErrorProtocol
	}
	return StreamErrorInternal
}

func AbortStream(stream *quic.Stream, code quic.StreamErrorCode) {
	if stream == nil {
		return
	}
	stream.CancelRead(code)
	stream.CancelWrite(code)
}

type StreamFailure struct {
	Code quic.StreamErrorCode
	Err  error
}

func (failure *StreamFailure) Error() string {
	if failure == nil || failure.Err == nil {
		return "Agent business Stream failed"
	}
	return failure.Err.Error()
}

func (failure *StreamFailure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.Err
}

func (server *StreamServer) report(header *agentv1.StreamHeader, err error) {
	if server.onError != nil && err != nil {
		server.onError(header, err)
	}
}
