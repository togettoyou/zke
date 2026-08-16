package metricsingest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang/snappy"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/metricsingest"
	"google.golang.org/protobuf/encoding/protowire"
)

// The wire format ZKE writes is only really settled by a real storage backend:
// a hand-rolled encoder that a test double accepts can still be rejected by
// VictoriaMetrics, and the scope label has to survive a round trip through it
// to mean anything.
//
// Point ZKE_TEST_METRICS_STORAGE_URL at a disposable instance, for example
//
//	docker run -d -p 8428:8428 victoriametrics/victoria-metrics:v1.149.0
//	ZKE_TEST_METRICS_STORAGE_URL=http://127.0.0.1:8428 go test ./pkg/server/metricsingest/
func storageBaseURL(t *testing.T) string {
	t.Helper()
	base := strings.TrimRight(os.Getenv("ZKE_TEST_METRICS_STORAGE_URL"), "/")
	if base == "" {
		t.Skip("ZKE_TEST_METRICS_STORAGE_URL is not configured")
	}
	return base
}

func TestIngestedSamplesReachStorageUnderTheServerScopeLabel(t *testing.T) {
	base := storageBaseURL(t)
	gateway, err := metricsingest.New(
		metricsingest.Config{
			WriteURL:     base + "/api/v1/write",
			WriteTimeout: 10 * time.Second,
		},
		slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatal(err)
	}

	// A unique metric name per run keeps repeated runs against the same
	// instance from reading each other's samples.
	metric := fmt.Sprintf("zke_ingest_probe_%d", time.Now().UnixNano())
	clusterID := "00000000-0000-4000-8000-0000000000aa"
	sampledAt := time.Now().Add(-30 * time.Second).UnixMilli()
	batch := snappy.Encode(nil, writeRequest(metric, map[string]string{
		"node": "node-1",
		// Claiming another Cluster's identity. The Server must replace it.
		metricsingest.ClusterLabel: "00000000-0000-4000-8000-0000000000bb",
	}, 42, sampledAt))

	outcome := gateway.IngestMetrics(
		context.Background(),
		agentconn.MetricsScope{ClusterID: clusterID},
		strings.NewReader(string(batch)),
		uint64(len(batch)),
	)
	if outcome.Result != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("ingest outcome = %+v", outcome)
	}

	series := queryUntilFound(t, base, metric)
	if series[metricsingest.ClusterLabel] != clusterID {
		t.Fatalf(
			"stored %s = %q, want the Server's connection identity %q",
			metricsingest.ClusterLabel,
			series[metricsingest.ClusterLabel],
			clusterID,
		)
	}
	if series["node"] != "node-1" {
		t.Fatalf("stored node label = %q", series["node"])
	}
}

// queryUntilFound polls the backend: an accepted remote write is queryable
// shortly after, not instantly, and failing on the first miss would make this
// test flaky for a reason that has nothing to do with ZKE.
func queryUntilFound(t *testing.T, base string, metric string) map[string]string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		labels, found := queryOnce(t, base, metric)
		if found {
			return labels
		}
		if time.Now().After(deadline) {
			t.Fatalf("metric %s never became queryable", metric)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func queryOnce(t *testing.T, base string, metric string) (map[string]string, bool) {
	t.Helper()
	response, err := http.PostForm(
		base+"/prometheus/api/v1/query",
		url.Values{"query": {metric}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("storage query status %d: %s", response.StatusCode, body)
	}
	var decoded struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Status != "success" || len(decoded.Data.Result) == 0 {
		return nil, false
	}
	return decoded.Data.Result[0].Metric, true
}

// writeRequest encodes a one-series Prometheus remote write request. It is
// written by hand for the same reason the package parses by hand: pulling in
// the Prometheus client libraries to produce three fields would be a large
// dependency for a frozen format.
func writeRequest(
	metric string,
	labels map[string]string,
	value float64,
	timestampMS int64,
) []byte {
	all := map[string]string{"__name__": metric}
	for name, labelValue := range labels {
		all[name] = labelValue
	}
	series := []byte{}
	for name, labelValue := range all {
		label := protowire.AppendTag(nil, 1, protowire.BytesType)
		label = protowire.AppendString(label, name)
		label = protowire.AppendTag(label, 2, protowire.BytesType)
		label = protowire.AppendString(label, labelValue)
		series = protowire.AppendTag(series, 1, protowire.BytesType)
		series = protowire.AppendBytes(series, label)
	}
	sample := protowire.AppendTag(nil, 1, protowire.Fixed64Type)
	sample = protowire.AppendFixed64(sample, math.Float64bits(value))
	sample = protowire.AppendTag(sample, 2, protowire.VarintType)
	sample = protowire.AppendVarint(sample, uint64(timestampMS))
	series = protowire.AppendTag(series, 2, protowire.BytesType)
	series = protowire.AppendBytes(series, sample)

	request := protowire.AppendTag(nil, 1, protowire.BytesType)
	return protowire.AppendBytes(request, series)
}
