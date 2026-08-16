package agentconn

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

type stubMetricsSink struct {
	scopes chan MetricsScope
}

func (stub stubMetricsSink) IngestMetrics(
	_ context.Context,
	scope MetricsScope,
	_ io.Reader,
	_ uint64,
) agentprotocol.MetricsIngestResult {
	stub.scopes <- scope
	return agentprotocol.MetricsIngestResult{
		Result: agentv1.ResultCode_RESULT_CODE_OK,
	}
}

func testMetricsManager(sink MetricsSink, ingestStreams int) *Manager {
	return &Manager{
		config: Config{
			HandshakeTimeout:             10 * time.Second,
			ResourceRequestTimeout:       2 * time.Minute,
			PodExecRequestTimeout:        15 * time.Minute,
			PodPortForwardRequestTimeout: time.Hour,
			MetricsIngestTimeout:         30 * time.Minute,
			MaxMetricsBatchBytes:         agentprotocol.DefaultMaxMetricsBatchBytes,
			MetricsSink:                  sink,
		},
		logger:                  slog.New(slog.DiscardHandler),
		metricsIngestAdmissions: make(chan struct{}, ingestStreams),
	}
}

func TestStreamServerOffersMetricsIngestOnlyWithASink(t *testing.T) {
	t.Parallel()

	scope := MetricsScope{ClusterID: "00000000-0000-4000-8000-000000000001"}
	withSink := testMetricsManager(
		stubMetricsSink{scopes: make(chan MetricsScope, 1)},
		1,
	).incomingStreamHandlers(&scope)
	if _, registered := withSink[agentv1.StreamKind_STREAM_KIND_METRICS_INGEST]; !registered {
		t.Fatal("a Server with a sink does not accept metrics ingest")
	}

	// Without a sink there is nowhere to put a batch, so the Stream must not be
	// accepted at all: accepting it and refusing every batch would leave a
	// collecting Agent holding a Stream open for nothing.
	withoutSink := testMetricsManager(nil, 1).incomingStreamHandlers(&scope)
	if len(withoutSink) != 0 {
		t.Fatal("a Server without a sink accepts an Agent-initiated Stream")
	}

	// A Connection whose Agent did not negotiate the capability gets the
	// dispatcher that accepts nothing, even though this Server has a sink.
	unnegotiated := testMetricsManager(
		stubMetricsSink{scopes: make(chan MetricsScope, 1)},
		1,
	).incomingStreamHandlers(nil)
	if len(unnegotiated) != 0 {
		t.Fatal("an unnegotiated Connection accepts metrics ingest")
	}
}

func TestMetricsIngestHandlerRefusesBeyondTheInstanceBudget(t *testing.T) {
	t.Parallel()

	manager := testMetricsManager(
		stubMetricsSink{scopes: make(chan MetricsScope, 1)},
		1,
	)
	// The only slot is taken by another Cluster's Stream. The per-Connection
	// dispatcher cannot see that, which is why the budget is enforced inside
	// the handler.
	manager.metricsIngestAdmissions <- struct{}{}

	handler := manager.metricsIngestHandler(MetricsScope{ClusterID: "cluster"})
	err := handler(context.Background(), nil, &agentv1.StreamHeader{})
	if !errors.Is(err, agentprotocol.ErrStreamResourceExhausted) {
		t.Fatalf("error = %v, want ErrStreamResourceExhausted", err)
	}
}
