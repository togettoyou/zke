package agentprotocol

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
)

func TestMetricsIngestHelloRejectsForeignEncoding(t *testing.T) {
	t.Parallel()

	valid := &agentv1.MetricsIngestHello{
		Collector:       "vmagent",
		PayloadEncoding: agentv1.MetricsPayloadEncoding_METRICS_PAYLOAD_ENCODING_PROMETHEUS_REMOTE_WRITE_SNAPPY,
	}
	if err := validateMetricsIngestHello(valid); err != nil {
		t.Fatal(err)
	}
	rejected := map[string]*agentv1.MetricsIngestHello{
		"nil":              nil,
		"empty collector":  {PayloadEncoding: valid.GetPayloadEncoding()},
		"padded collector": {Collector: " vmagent", PayloadEncoding: valid.GetPayloadEncoding()},
		"unset encoding":   {Collector: "vmagent"},
	}
	for name, hello := range rejected {
		if err := validateMetricsIngestHello(hello); !errors.Is(err, ErrStreamProtocol) {
			t.Errorf("%s: error = %v, want ErrStreamProtocol", name, err)
		}
	}
}

func TestMetricsIngestBatchRequiresMonotonicBoundedBatches(t *testing.T) {
	t.Parallel()

	if err := validateMetricsIngestBatch(
		&agentv1.MetricsIngestBatch{BatchId: 3, PayloadSize: 16},
		3,
		64,
	); err != nil {
		t.Fatal(err)
	}
	rejected := map[string]*agentv1.MetricsIngestBatch{
		"replayed identifier": {BatchId: 2, PayloadSize: 16},
		"skipped identifier":  {BatchId: 4, PayloadSize: 16},
		"empty payload":       {BatchId: 3},
		"oversized payload":   {BatchId: 3, PayloadSize: 65},
	}
	for name, batch := range rejected {
		if err := validateMetricsIngestBatch(batch, 3, 64); !errors.Is(err, ErrStreamProtocol) {
			t.Errorf("%s: error = %v, want ErrStreamProtocol", name, err)
		}
	}
}

func TestMetricsIngestAckKeepsRetryHintWithThrottlingOnly(t *testing.T) {
	t.Parallel()

	accepted := &agentv1.MetricsIngestAck{
		BatchId: 1,
		Result:  agentv1.ResultCode_RESULT_CODE_OK,
	}
	if err := validateMetricsIngestAck(accepted, 1); err != nil {
		t.Fatal(err)
	}
	throttled := &agentv1.MetricsIngestAck{
		BatchId:          1,
		Result:           agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
		Reason:           "SampleRateExceeded",
		Message:          "cluster sample rate limit reached",
		RetryAfterMillis: 5_000,
	}
	if err := validateMetricsIngestAck(throttled, 1); err != nil {
		t.Fatal(err)
	}
	rejected := map[string]*agentv1.MetricsIngestAck{
		"mismatched batch": {BatchId: 2, Result: agentv1.ResultCode_RESULT_CODE_OK},
		"unset result":     {BatchId: 1},
		"explained accept": {
			BatchId: 1,
			Result:  agentv1.ResultCode_RESULT_CODE_OK,
			Reason:  "Accepted",
			Message: "accepted",
		},
		"silent rejection": {
			BatchId: 1,
			Result:  agentv1.ResultCode_RESULT_CODE_INTERNAL,
		},
		// A retry hint on anything but throttling would tell the collector to
		// replay a batch the Server has already refused on its merits.
		"retry hint on invalid payload": {
			BatchId:          1,
			Result:           agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			Reason:           "PayloadInvalid",
			Message:          "payload is not a remote write request",
			RetryAfterMillis: 1_000,
		},
	}
	for name, ack := range rejected {
		if err := validateMetricsIngestAck(ack, 1); !errors.Is(err, ErrStreamProtocol) {
			t.Errorf("%s: error = %v, want ErrStreamProtocol", name, err)
		}
	}
}

func TestMetricsIngestReadyRejectsLimitsAboveTheProtocolCeiling(t *testing.T) {
	t.Parallel()

	if err := validateMetricsIngestReady(&agentv1.MetricsIngestReady{
		Result:             agentv1.ResultCode_RESULT_CODE_OK,
		MaxBatchBytes:      MaxMetricsBatchBytesCeiling,
		MaxInFlightBatches: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := validateMetricsIngestReady(&agentv1.MetricsIngestReady{
		Result:             agentv1.ResultCode_RESULT_CODE_OK,
		MaxBatchBytes:      MaxMetricsBatchBytesCeiling + 1,
		MaxInFlightBatches: 1,
	}); !errors.Is(err, ErrStreamProtocol) {
		t.Error("a Server limit above the protocol ceiling was accepted")
	}
	if err := validateMetricsIngestReady(&agentv1.MetricsIngestReady{
		Result:  agentv1.ResultCode_RESULT_CODE_FORBIDDEN,
		Reason:  "MetricsCollectionDisabled",
		Message: "metrics collection is not enabled for this Cluster",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRealQUICMetricsIngestCarriesBatchesAndVerdicts(t *testing.T) {
	client, server, stop := openStreamTestConnection(t)
	defer stop()

	var mutex sync.Mutex
	var received [][]byte
	streamServer, err := NewStreamServer(StreamServerConfig{
		HeaderTimeout: 500 * time.Millisecond,
		MaxTimeout:    5 * time.Second,
		Handlers: map[agentv1.StreamKind]StreamHandlerConfig{
			agentv1.StreamKind_STREAM_KIND_METRICS_INGEST: {
				MaxConcurrent: 1,
				Handle: MetricsIngestStreamHandler(
					1024,
					func(
						_ context.Context,
						batch *agentv1.MetricsIngestBatch,
						payload io.Reader,
					) MetricsIngestResult {
						body, err := io.ReadAll(payload)
						if err != nil {
							return MetricsIngestResult{
								Result:  agentv1.ResultCode_RESULT_CODE_INTERNAL,
								Reason:  "ReadFailed",
								Message: "payload could not be read",
							}
						}
						mutex.Lock()
						received = append(received, body)
						mutex.Unlock()
						if batch.GetBatchId() == 2 {
							return MetricsIngestResult{
								Result:           agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
								Reason:           "SampleRateExceeded",
								Message:          "cluster sample rate limit reached",
								RetryAfterMillis: 2_000,
							}
						}
						return MetricsIngestResult{
							Result: agentv1.ResultCode_RESULT_CODE_OK,
						}
					},
				),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	serveContext, cancelServe := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- streamServer.Serve(serveContext, server) }()
	defer func() {
		cancelServe()
		_ = client.CloseWithError(0, "test complete")
		select {
		case err := <-serveDone:
			if err != nil {
				t.Errorf("stop Metrics Ingest Stream Server: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Metrics Ingest Stream Server did not stop")
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := OpenMetricsIngest(
		ctx,
		client,
		&agentv1.StreamHeader{
			ProtocolVersion: ProtocolVersion,
			Kind:            agentv1.StreamKind_STREAM_KIND_METRICS_INGEST,
			RequestId:       "00000000-0000-4000-8000-000000000040",
			TimeoutMillis:   5_000,
		},
		"vmagent",
		4096,
	)
	if err != nil {
		t.Fatal(err)
	}
	// The Agent must adopt the smaller of the two limits, not its own.
	if session.MaxBatchBytes() != 1024 {
		t.Fatalf("negotiated batch limit = %d, want 1024", session.MaxBatchBytes())
	}

	accepted, err := session.Send(ctx, []byte("first-batch"))
	if err != nil {
		t.Fatal(err)
	}
	if accepted.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		accepted.GetBatchId() != 1 {
		t.Fatalf("unexpected acknowledgement: %+v", accepted)
	}
	throttled, err := session.Send(ctx, []byte("second-batch"))
	if err != nil {
		t.Fatal(err)
	}
	if throttled.GetResult() != agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED ||
		throttled.GetRetryAfterMillis() != 2_000 {
		t.Fatalf("unexpected throttling acknowledgement: %+v", throttled)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	mutex.Lock()
	defer mutex.Unlock()
	if len(received) != 2 ||
		string(received[0]) != "first-batch" ||
		string(received[1]) != "second-batch" {
		t.Fatalf("Server received %q", received)
	}
}

func TestMetricsIngestRejectsBatchesAboveTheNegotiatedLimit(t *testing.T) {
	t.Parallel()

	session := &MetricsIngestSession{maxBatchBytes: 8}
	if _, err := session.Send(
		context.Background(),
		make([]byte, 9),
	); !errors.Is(err, ErrMetricsBatchTooLarge) {
		t.Fatalf("error = %v, want ErrMetricsBatchTooLarge", err)
	}
	// Nothing was written, so the identifier sequence must not have advanced.
	if session.nextBatchID != 0 {
		t.Fatalf("batch identifier = %d, want 0", session.nextBatchID)
	}
}
