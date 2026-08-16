package agentprotocol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

const (
	DefaultMaxMetricsBatchBytes uint64 = 4 * 1024 * 1024
	// Protocol ceiling. A deployment may lower its batch size but never raise
	// it past this, so an Agent can reject an implausible Server limit before
	// it agrees to buffer that much per batch.
	MaxMetricsBatchBytesCeiling uint64 = 16 * 1024 * 1024
	// One batch at a time. The protocol carries a window so a later version can
	// widen it without a new Stream kind, but a lock-step exchange needs no
	// batch bookkeeping on either side and already produces the backpressure
	// this link exists to apply: while a batch is unacknowledged, the collector
	// inside the Cluster is the one that waits.
	DefaultMaxMetricsInFlightBatches uint32 = 1
	DefaultMetricsIngestTimeout             = 30 * time.Minute
	maxMetricsCollectorLength               = 128
)

var (
	ErrMetricsIngestClosed   = errors.New("Agent Metrics Ingest Stream is closed")
	ErrMetricsBatchTooLarge  = errors.New("Agent metrics batch exceeds the negotiated maximum size")
	ErrMetricsIngestRejected = errors.New("Server rejected the Agent Metrics Ingest Stream")
)

// MetricsIngestResult is the Server's verdict on one batch. It is a value
// rather than an error because a rejection is a normal outcome the Agent must
// forward to its collector, not a failure of the Stream.
type MetricsIngestResult struct {
	Result           agentv1.ResultCode
	Reason           string
	Message          string
	RetryAfterMillis uint64
}

// MetricsIngestHandler consumes exactly one batch payload. The reader is
// bounded to the declared payload size; anything left unread is drained before
// the next batch, so a handler that stops early cannot desynchronise the
// Stream.
type MetricsIngestHandler func(
	context.Context,
	*agentv1.MetricsIngestBatch,
	io.Reader,
) MetricsIngestResult

// MetricsIngestStreamHandler serves the Agent-initiated Metrics Ingest Stream
// on the Server. The Stream stays open for the life of the Connection and
// carries many batches; each one is acknowledged before the next is read.
func MetricsIngestStreamHandler(
	maxBatchBytes uint64,
	handler MetricsIngestHandler,
) IncomingStreamHandler {
	if maxBatchBytes == 0 {
		maxBatchBytes = DefaultMaxMetricsBatchBytes
	}
	return func(
		ctx context.Context,
		stream *quic.Stream,
		header *agentv1.StreamHeader,
	) error {
		if handler == nil {
			return &StreamFailure{
				Code: StreamErrorUnsupported,
				Err:  ErrStreamUnsupported,
			}
		}
		if header.GetIdempotencyKey() != "" {
			return ErrStreamProtocol
		}
		hello := &agentv1.MetricsIngestHello{}
		if err := ReadMessage(stream, hello); err != nil {
			return fmt.Errorf(
				"%w: read MetricsIngestHello: %w",
				ErrStreamProtocol,
				err,
			)
		}
		if err := validateMetricsIngestHello(hello); err != nil {
			return err
		}
		if err := WriteMessage(stream, &agentv1.MetricsIngestReady{
			Result:             agentv1.ResultCode_RESULT_CODE_OK,
			MaxBatchBytes:      maxBatchBytes,
			MaxInFlightBatches: DefaultMaxMetricsInFlightBatches,
		}); err != nil {
			return err
		}

		var expectedBatchID uint64
		for {
			batch := &agentv1.MetricsIngestBatch{}
			if err := ReadMessage(stream, batch); err != nil {
				if errors.Is(err, io.EOF) {
					// The Agent finished sending. Its half of the Stream is
					// closed; the framework closes ours.
					return nil
				}
				return fmt.Errorf(
					"%w: read MetricsIngestBatch: %w",
					ErrStreamProtocol,
					err,
				)
			}
			expectedBatchID++
			if err := validateMetricsIngestBatch(
				batch,
				expectedBatchID,
				maxBatchBytes,
			); err != nil {
				return err
			}
			payload := &io.LimitedReader{
				R: stream,
				N: int64(batch.GetPayloadSize()),
			}
			result := handler(ctx, batch, payload)
			if payload.N > 0 {
				if _, err := io.CopyN(io.Discard, payload, payload.N); err != nil {
					return fmt.Errorf(
						"%w: truncated metrics batch payload: %w",
						ErrStreamProtocol,
						err,
					)
				}
			}
			ack := &agentv1.MetricsIngestAck{
				BatchId:          batch.GetBatchId(),
				Result:           result.Result,
				Reason:           result.Reason,
				Message:          result.Message,
				RetryAfterMillis: result.RetryAfterMillis,
			}
			if err := validateMetricsIngestAck(ack, batch.GetBatchId()); err != nil {
				return err
			}
			if err := WriteMessage(stream, ack); err != nil {
				return err
			}
		}
	}
}

// MetricsIngestSession is the Agent side of the Stream. Sends are serialised:
// the payload bytes follow their descriptor on the same Stream, so a second
// concurrent send would interleave two payloads into one byte range.
type MetricsIngestSession struct {
	stream        *quic.Stream
	maxBatchBytes uint64

	mutex       sync.Mutex
	nextBatchID uint64
	closed      bool
}

// OpenMetricsIngest opens the Stream and completes the Hello exchange. The
// returned session is usable until Close, an unrecoverable send failure or the
// Connection ending.
func OpenMetricsIngest(
	ctx context.Context,
	connection *quic.Conn,
	header *agentv1.StreamHeader,
	collector string,
	maxBatchBytes uint64,
) (*MetricsIngestSession, error) {
	if connection == nil {
		return nil, errors.New("Agent Connection is required")
	}
	if maxBatchBytes == 0 {
		maxBatchBytes = DefaultMaxMetricsBatchBytes
	}
	if err := validateStreamHeader(header); err != nil {
		return nil, err
	}
	if header.GetKind() != agentv1.StreamKind_STREAM_KIND_METRICS_INGEST ||
		header.GetIdempotencyKey() != "" {
		return nil, ErrStreamProtocol
	}
	hello := &agentv1.MetricsIngestHello{
		Collector:       collector,
		PayloadEncoding: agentv1.MetricsPayloadEncoding_METRICS_PAYLOAD_ENCODING_PROMETHEUS_REMOTE_WRITE_SNAPPY,
	}
	if err := validateMetricsIngestHello(hello); err != nil {
		return nil, err
	}

	stream, err := connection.OpenStreamSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("open Agent Metrics Ingest Stream: %w", err)
	}
	established := false
	defer func() {
		if !established {
			AbortStream(stream, StreamErrorCanceled)
		}
	}()
	// The Stream outlives whatever request happens to open it, so its deadline
	// comes from the negotiated session lifetime rather than from ctx. ctx
	// bounds the handshake only, and that cancellation is unregistered as soon
	// as the handshake is done — otherwise the first request to finish would
	// tear down a session meant to carry many more.
	stopCancellation := context.AfterFunc(ctx, func() {
		AbortStream(stream, streamErrorCode(ctx.Err(), ctx))
	})
	defer stopCancellation()
	if err := stream.SetDeadline(time.Now().Add(
		time.Duration(header.GetTimeoutMillis()) * time.Millisecond,
	)); err != nil {
		return nil, err
	}
	if err := WriteMessage(stream, header); err != nil {
		return nil, err
	}
	if err := WriteMessage(stream, hello); err != nil {
		return nil, err
	}
	ready := &agentv1.MetricsIngestReady{}
	if err := ReadMessage(stream, ready); err != nil {
		return nil, err
	}
	if err := validateMetricsIngestReady(ready); err != nil {
		return nil, err
	}
	if ready.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		return nil, fmt.Errorf(
			"%w: %s",
			ErrMetricsIngestRejected,
			ready.GetReason(),
		)
	}
	established = true
	return &MetricsIngestSession{
		stream:        stream,
		maxBatchBytes: min(maxBatchBytes, ready.GetMaxBatchBytes()),
	}, nil
}

func (session *MetricsIngestSession) MaxBatchBytes() uint64 {
	if session == nil {
		return 0
	}
	return session.maxBatchBytes
}

// Send forwards one collector payload and returns the Server's verdict. A
// transport or protocol failure closes the session: after a partial write or
// an unread acknowledgement the two sides no longer agree on where the next
// batch begins, so the Stream cannot be reused.
func (session *MetricsIngestSession) Send(
	ctx context.Context,
	payload []byte,
) (*agentv1.MetricsIngestAck, error) {
	if session == nil {
		return nil, ErrMetricsIngestClosed
	}
	if len(payload) == 0 {
		return nil, errors.New("metrics batch payload is required")
	}
	if uint64(len(payload)) > session.maxBatchBytes {
		return nil, ErrMetricsBatchTooLarge
	}
	session.mutex.Lock()
	defer session.mutex.Unlock()
	if session.closed {
		return nil, ErrMetricsIngestClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		AbortStream(session.stream, streamErrorCode(ctx.Err(), ctx))
	})
	defer stopCancellation()

	session.nextBatchID++
	batchID := session.nextBatchID
	ack, err := session.exchange(batchID, payload)
	if err != nil {
		session.closed = true
		AbortStream(session.stream, streamErrorCode(err, ctx))
		return nil, err
	}
	return ack, nil
}

func (session *MetricsIngestSession) exchange(
	batchID uint64,
	payload []byte,
) (*agentv1.MetricsIngestAck, error) {
	if err := WriteMessage(session.stream, &agentv1.MetricsIngestBatch{
		BatchId:     batchID,
		PayloadSize: uint64(len(payload)),
	}); err != nil {
		return nil, err
	}
	if err := writeAll(session.stream, payload); err != nil {
		return nil, fmt.Errorf("write metrics batch payload: %w", err)
	}
	ack := &agentv1.MetricsIngestAck{}
	if err := ReadMessage(session.stream, ack); err != nil {
		return nil, err
	}
	if err := validateMetricsIngestAck(ack, batchID); err != nil {
		return nil, err
	}
	return ack, nil
}

// Close ends the Stream normally. Batches already acknowledged stay
// acknowledged; nothing is in flight, because sends are lock-step.
func (session *MetricsIngestSession) Close() error {
	if session == nil {
		return nil
	}
	session.mutex.Lock()
	defer session.mutex.Unlock()
	if session.closed {
		return nil
	}
	session.closed = true
	return session.stream.Close()
}

// Abort ends the Stream without pretending the exchange finished cleanly.
func (session *MetricsIngestSession) Abort() {
	if session == nil {
		return
	}
	session.mutex.Lock()
	defer session.mutex.Unlock()
	if session.closed {
		return
	}
	session.closed = true
	AbortStream(session.stream, StreamErrorCanceled)
}

func validateMetricsIngestHello(hello *agentv1.MetricsIngestHello) error {
	if hello == nil ||
		hello.GetCollector() == "" ||
		len(hello.GetCollector()) > maxMetricsCollectorLength ||
		strings.TrimSpace(hello.GetCollector()) != hello.GetCollector() ||
		hello.GetPayloadEncoding() !=
			agentv1.MetricsPayloadEncoding_METRICS_PAYLOAD_ENCODING_PROMETHEUS_REMOTE_WRITE_SNAPPY {
		return ErrStreamProtocol
	}
	return nil
}

func validateMetricsIngestReady(ready *agentv1.MetricsIngestReady) error {
	if ready == nil ||
		ready.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrStreamProtocol
	}
	if ready.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		if ready.GetReason() == "" || ready.GetMessage() == "" ||
			ready.GetMaxBatchBytes() != 0 || ready.GetMaxInFlightBatches() != 0 {
			return ErrStreamProtocol
		}
		return nil
	}
	if ready.GetReason() != "" || ready.GetMessage() != "" ||
		ready.GetMaxBatchBytes() == 0 ||
		ready.GetMaxBatchBytes() > MaxMetricsBatchBytesCeiling ||
		ready.GetMaxInFlightBatches() == 0 {
		return ErrStreamProtocol
	}
	return nil
}

func validateMetricsIngestBatch(
	batch *agentv1.MetricsIngestBatch,
	expectedBatchID uint64,
	maxBatchBytes uint64,
) error {
	if batch == nil ||
		batch.GetBatchId() != expectedBatchID ||
		batch.GetPayloadSize() == 0 ||
		batch.GetPayloadSize() > maxBatchBytes {
		return ErrStreamProtocol
	}
	return nil
}

func validateMetricsIngestAck(
	ack *agentv1.MetricsIngestAck,
	batchID uint64,
) error {
	if ack == nil || ack.GetBatchId() != batchID ||
		ack.GetResult() == agentv1.ResultCode_RESULT_CODE_UNSPECIFIED {
		return ErrStreamProtocol
	}
	if ack.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		if ack.GetReason() != "" || ack.GetMessage() != "" ||
			ack.GetRetryAfterMillis() != 0 {
			return ErrStreamProtocol
		}
		return nil
	}
	if ack.GetReason() == "" || ack.GetMessage() == "" {
		return ErrStreamProtocol
	}
	if ack.GetRetryAfterMillis() != 0 &&
		ack.GetResult() != agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED {
		return ErrStreamProtocol
	}
	return nil
}
