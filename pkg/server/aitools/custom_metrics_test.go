package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/metricsquery"
)

type customMetricsStub struct {
	got    metricsquery.ExploreInput
	result metricsquery.ExploreResult
}

func (*customMetricsStub) Catalog() []metricsquery.Definition { return nil }

func (*customMetricsStub) Query(
	context.Context, metricsquery.Input,
) (metricsquery.Result, error) {
	panic("not called")
}

func (stub *customMetricsStub) Explore(
	_ context.Context, input metricsquery.ExploreInput,
) (metricsquery.ExploreResult, error) {
	stub.got = input
	return stub.result, nil
}

func TestCustomMetricsUsesTheSessionClusterAndReturnsEffectiveExpression(t *testing.T) {
	t.Parallel()
	value := -4.0
	stub := &customMetricsStub{result: metricsquery.ExploreResult{
		ClusterID: testClusterID, Kind: metricsquery.KindRange,
		Start: time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 30, 2, 0, 0, 0, time.UTC),
		Queries: []metricsquery.ExploreOutcome{{
			RefID: "A", Expression: `up{zke_cluster_id="elsewhere"}`,
			EffectiveExpression: `up{zke_cluster_id="` + testClusterID + `"}`,
			Series: []metricsquery.Series{{
				Labels: map[string]string{"job": "kubelet"},
				Points: []metricsquery.Point{{UnixSeconds: 1, Value: &value}},
			}},
		}},
	}}
	catalogue := New(Dependencies{Metrics: stub}, Config{})

	result, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolQueryCustomMetrics, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{"expression":"up{zke_cluster_id=\"elsewhere\"}","minutes":60}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if stub.got.ClusterID != testClusterID || stub.got.UserID != testUserID {
		t.Fatalf("Explore input scope = %+v", stub.got)
	}
	if len(stub.got.Queries) != 1 || stub.got.Queries[0].Expression !=
		`up{zke_cluster_id="elsewhere"}` {
		t.Fatalf("Explore input queries = %+v", stub.got.Queries)
	}
	if !strings.Contains(result.Text, `"effective_expression"`) ||
		!strings.Contains(result.Text, testClusterID) ||
		!strings.Contains(result.Text, `"max": -4`) {
		t.Fatalf("tool result = %s", result.Text)
	}
	if result.Failed || len(result.Evidence) != 1 {
		t.Fatalf("tool result metadata = %+v", result)
	}
	evidence := result.Evidence[0]
	if evidence.Cluster != testClusterID || evidence.Expression !=
		`up{zke_cluster_id="elsewhere"}` || evidence.Query != "" {
		t.Fatalf("evidence = %+v", evidence)
	}
}

func TestCustomMetricsSchemaDoesNotAcceptAClusterID(t *testing.T) {
	t.Parallel()
	catalogue := New(Dependencies{Metrics: &customMetricsStub{}}, Config{})
	var schema map[string]any
	for _, spec := range catalogue.Specs() {
		if spec.Name == toolQueryCustomMetrics {
			if err := json.Unmarshal(spec.Schema, &schema); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	if _, present := properties["cluster_id"]; present {
		t.Fatalf("custom metrics schema exposes cluster_id: %+v", schema)
	}
	if _, present := properties["expression"]; !present {
		t.Fatalf("custom metrics schema has no expression: %+v", schema)
	}
	_, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolQueryCustomMetrics, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{"expression":"up","cluster_id":"elsewhere"}`),
	})
	if !errors.Is(err, airuntime.ErrInvalidInput) {
		t.Fatalf("cluster_id argument error = %v, want ErrInvalidInput", err)
	}
}

func TestCustomMetricsReturnsAnExpressionFailureToTheModel(t *testing.T) {
	t.Parallel()
	stub := &customMetricsStub{result: metricsquery.ExploreResult{
		Kind: metricsquery.KindRange,
		Queries: []metricsquery.ExploreOutcome{{
			RefID: "A", Expression: "sum(", Error: &metricsquery.ExploreError{
				Code: metricsquery.ExploreErrorInvalidExpression, Detail: "MetricsQL 解析失败",
			},
		}},
	}}
	catalogue := New(Dependencies{Metrics: stub}, Config{})

	result, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolQueryCustomMetrics, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{"expression":"sum("}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed || len(result.Evidence) != 0 ||
		!strings.Contains(result.Text, metricsquery.ExploreErrorInvalidExpression) {
		t.Fatalf("failed result = %+v", result)
	}
}
