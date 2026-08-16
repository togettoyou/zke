package metricsquery

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/metricsingest"
	"github.com/togettoyou/zke/pkg/server/store"
)

const (
	userID     = "00000000-0000-4000-8000-000000000001"
	clusterOne = "00000000-0000-4000-8000-000000000011"
	clusterTwo = "00000000-0000-4000-8000-000000000012"
	foreign    = "00000000-0000-4000-8000-000000000099"
)

type stubVisibility struct {
	visibility Visibility
	err        error
}

func (stub stubVisibility) ResolveMetricsVisibility(
	context.Context,
	string,
) (Visibility, error) {
	return stub.visibility, stub.err
}

type stubClusters struct {
	scopes []store.ClusterScope
	err    error
}

func (stub stubClusters) ListVisibleClusters(
	context.Context,
	store.ListVisibleClustersParams,
) ([]store.ClusterScope, error) {
	return stub.scopes, stub.err
}

type capturedQuery struct {
	expression string
	path       string
	calls      int
}

type stubBudget struct {
	states map[string]metricsingest.ClusterState
}

func (stub stubBudget) ClusterState(
	clusterID string,
) (metricsingest.ClusterState, bool) {
	state, known := stub.states[clusterID]
	return state, known
}

func testService(
	t *testing.T,
	body string,
	status int,
	captured *capturedQuery,
) *Service {
	t.Helper()
	return testServiceWithBudget(t, body, status, captured, nil)
}

func testServiceWithBudget(
	t *testing.T,
	body string,
	status int,
	captured *capturedQuery,
	budget IngestBudget,
) *Service {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			if err := request.ParseForm(); err != nil {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			captured.calls++
			captured.expression = request.Form.Get("query")
			captured.path = request.URL.Path
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(body))
		},
	))
	t.Cleanup(backend.Close)

	service, err := NewService(
		Config{
			QueryURL:   backend.URL + "/prometheus",
			HTTPClient: backend.Client(),
			MinStep:    15 * time.Second,
			Budget:     budget,
			Now: func() time.Time {
				return time.Unix(1_755_216_000, 0).UTC()
			},
		},
		stubVisibility{visibility: Visibility{Global: true}},
		stubClusters{scopes: []store.ClusterScope{
			{ClusterID: clusterOne, ClusterName: "prod-sh", Status: "active"},
			{ClusterID: clusterTwo, ClusterName: "prod-bj", Status: "active"},
		}},
		slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func rangeInput(name string) Input {
	end := time.Unix(1_755_216_000, 0).UTC()
	return Input{
		UserID: userID,
		Name:   name,
		Start:  end.Add(-2 * time.Minute),
		End:    end,
		Step:   time.Minute,
	}
}

func TestQueryScopesEveryVisibleClusterWhenNoneIsRequested(t *testing.T) {
	t.Parallel()

	captured := &capturedQuery{}
	service := testService(t, `{"status":"success","data":{"resultType":"matrix","result":[]}}`, http.StatusOK, captured)
	result, err := service.Query(context.Background(), rangeInput("cluster_cpu_usage"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(captured.expression, clusterOne) ||
		!strings.Contains(captured.expression, clusterTwo) {
		t.Fatalf("expression does not cover both Clusters: %s", captured.expression)
	}
	if !strings.Contains(captured.expression, `zke_cluster_id=~"`) {
		t.Fatalf("expression carries no scope matcher: %s", captured.expression)
	}
	if !strings.HasSuffix(captured.path, "/api/v1/query_range") {
		t.Fatalf("range query hit %s", captured.path)
	}
	if len(result.ClusterIDs) != 2 {
		t.Fatalf("covered Clusters = %v", result.ClusterIDs)
	}
}

func TestQueryRefusesClustersOutsideVisibilityWithoutCallingStorage(t *testing.T) {
	t.Parallel()

	captured := &capturedQuery{}
	service := testService(t, `{"status":"success","data":{"resultType":"matrix","result":[]}}`, http.StatusOK, captured)
	input := rangeInput("cluster_cpu_usage")
	input.ClusterIDs = []string{clusterOne, foreign}
	if _, err := service.Query(context.Background(), input); !errors.Is(err, ErrDenied) {
		t.Fatalf("error = %v, want ErrDenied", err)
	}
	// A refused scope must never reach storage: the query would otherwise run
	// with the permitted subset and look like a complete answer.
	if captured.calls != 0 {
		t.Fatal("a denied query reached the storage backend")
	}
}

func TestQueryRejectsUnknownNamesAndUnusableRanges(t *testing.T) {
	t.Parallel()

	captured := &capturedQuery{}
	service := testService(t, `{"status":"success","data":{"resultType":"matrix","result":[]}}`, http.StatusOK, captured)
	cases := map[string]struct {
		mutate func(*Input)
		want   error
	}{
		"unknown query": {
			mutate: func(input *Input) { input.Name = "cluster_disk_usage" },
			want:   ErrUnknownQuery,
		},
		"inverted range": {
			mutate: func(input *Input) { input.Start = input.End.Add(time.Minute) },
			want:   ErrInvalidInput,
		},
		"step below the floor": {
			mutate: func(input *Input) { input.Step = time.Second },
			want:   ErrInvalidInput,
		},
		"too many points": {
			mutate: func(input *Input) {
				input.Start = input.End.Add(-24 * time.Hour)
				input.Step = 15 * time.Second
			},
			want: ErrInvalidInput,
		},
		"top on a query without it": {
			mutate: func(input *Input) { input.Top = 5 },
			want:   ErrInvalidInput,
		},
		"invalid namespace": {
			mutate: func(input *Input) { input.Namespace = "Not A Namespace" },
			want:   ErrInvalidInput,
		},
		// A Namespace on a query that cannot carry one is refused rather than
		// dropped: silently answering Cluster-wide would be read as a Namespace
		// number.
		"namespace on a query without it": {
			mutate: func(input *Input) { input.Namespace = "kube-system" },
			want:   ErrInvalidInput,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := rangeInput("cluster_cpu_usage")
			testCase.mutate(&input)
			if _, err := service.Query(context.Background(), input); !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestQueryReportsGapsAsExplicitHoles(t *testing.T) {
	t.Parallel()

	// Storage returns the first and last point of the requested grid; the
	// middle step is missing because collection stopped.
	body := `{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{"zke_cluster_id":"` + clusterOne + `"},
		 "values":[[1755215880,"1200"],[1755216000,"1400"]]}
	]}}`
	captured := &capturedQuery{}
	service := testService(t, body, http.StatusOK, captured)
	result, err := service.Query(context.Background(), rangeInput("cluster_cpu_usage"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Series) != 1 {
		t.Fatalf("series = %d, want 1", len(result.Series))
	}
	points := result.Series[0].Points
	if len(points) != 3 {
		t.Fatalf("points = %d, want the full grid of 3", len(points))
	}
	if points[0].Value == nil || *points[0].Value != 1200 {
		t.Fatalf("first point = %+v", points[0])
	}
	// The hole is what makes a collection outage visible instead of drawn as
	// a straight line between the two samples around it.
	if points[1].Value != nil {
		t.Fatalf("missing sample was filled in: %+v", points[1])
	}
	if points[2].Value == nil || *points[2].Value != 1400 {
		t.Fatalf("last point = %+v", points[2])
	}
	if result.Series[0].ClusterName != "prod-sh" {
		t.Fatalf("cluster name = %q", result.Series[0].ClusterName)
	}
}

func TestQueryTranslatesStorageFailures(t *testing.T) {
	t.Parallel()

	captured := &capturedQuery{}
	service := testService(t, `{"status":"error","errorType":"bad_data"}`, http.StatusOK, captured)
	if _, err := service.Query(
		context.Background(),
		rangeInput("cluster_cpu_usage"),
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}

	failing := &capturedQuery{}
	unavailable := testService(t, `{}`, http.StatusBadGateway, failing)
	if _, err := unavailable.Query(
		context.Background(),
		rangeInput("cluster_cpu_usage"),
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

func TestInstantQueryIgnoresRangeParameters(t *testing.T) {
	t.Parallel()

	body := `{"status":"success","data":{"resultType":"vector","result":[
		{"metric":{"zke_cluster_id":"` + clusterOne + `"},"value":[1755216000,"1"]}
	]}}`
	captured := &capturedQuery{}
	service := testService(t, body, http.StatusOK, captured)
	result, err := service.Query(context.Background(), Input{
		UserID: userID,
		Name:   "collection_health",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(captured.path, "/api/v1/query") {
		t.Fatalf("instant query hit %s", captured.path)
	}
	if len(result.Series) != 1 || len(result.Series[0].Points) != 1 {
		t.Fatalf("unexpected instant result: %+v", result.Series)
	}
}

func TestPodQueriesRequireATopBound(t *testing.T) {
	t.Parallel()

	captured := &capturedQuery{}
	service := testService(t, `{"status":"success","data":{"resultType":"matrix","result":[]}}`, http.StatusOK, captured)
	for _, name := range []string{"pod_cpu_usage", "pod_memory_usage"} {
		captured.calls = 0
		input := rangeInput(name)
		if _, err := service.Query(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s without a top bound: error = %v, want ErrInvalidInput", name, err)
		}
		if captured.calls != 0 {
			t.Fatalf("%s reached storage unbounded", name)
		}
		input.Top = 10
		if _, err := service.Query(context.Background(), input); err != nil {
			t.Fatalf("%s with a top bound: %v", name, err)
		}
		if !strings.HasPrefix(captured.expression, "topk(10,") {
			t.Fatalf("%s expression is not bounded: %s", name, captured.expression)
		}
	}
}

func TestNamespaceNarrowsTheExpressionWithoutLosingTheScopeFilter(t *testing.T) {
	t.Parallel()

	captured := &capturedQuery{}
	service := testService(t, `{"status":"success","data":{"resultType":"matrix","result":[]}}`, http.StatusOK, captured)
	input := rangeInput("namespace_memory_usage")
	input.Namespace = "kube-system"
	if _, err := service.Query(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(captured.expression, `namespace="kube-system"`) {
		t.Fatalf("namespace filter missing: %s", captured.expression)
	}
	// The scope filter has to survive the narrowing, or a Namespace name would
	// be a way to widen the answer past the caller's Clusters.
	if !strings.Contains(captured.expression, clusterOne) ||
		!strings.Contains(captured.expression, clusterTwo) {
		t.Fatalf("scope filter lost: %s", captured.expression)
	}
}

func TestQueryReportsSilentClustersWithoutCallingThemPartial(t *testing.T) {
	t.Parallel()

	body := `{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{"zke_cluster_id":"` + clusterOne + `"},"values":[[1755216000,"1"]]}
	]}}`
	captured := &capturedQuery{}
	service := testService(t, body, http.StatusOK, captured)
	result, err := service.Query(context.Background(), rangeInput("cluster_cpu_usage"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Issues) != 1 ||
		result.Issues[0].Reason != IssueNoData ||
		result.Issues[0].ClusterID != clusterTwo {
		t.Fatalf("issues = %+v", result.Issues)
	}
	// A Cluster that has nothing to report is not a failure of the query, and
	// marking it partial would put a warning on every chart in a deployment
	// where collection is only installed somewhere.
	if result.Partial {
		t.Fatal("a silent Cluster made the answer partial")
	}
}

func TestQueryReportsThrottledClustersAsPartial(t *testing.T) {
	t.Parallel()

	body := `{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{"zke_cluster_id":"` + clusterOne + `"},"values":[[1755216000,"1"]]}
	]}}`
	captured := &capturedQuery{}
	service := testServiceWithBudget(t, body, http.StatusOK, captured, stubBudget{
		states: map[string]metricsingest.ClusterState{
			clusterTwo: {
				Throttled: true,
				Reason:    metricsingest.ThrottleReasonCardinality,
			},
		},
	})
	result, err := service.Query(context.Background(), rangeInput("cluster_cpu_usage"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Partial {
		t.Fatal("a throttled Cluster must make the answer partial")
	}
	if len(result.Issues) != 1 ||
		result.Issues[0].Reason != IssueThrottled ||
		result.Issues[0].ClusterID != clusterTwo ||
		result.Issues[0].Detail != metricsingest.ThrottleReasonCardinality {
		t.Fatalf("issues = %+v", result.Issues)
	}
	// The reason must not be reported as "no data": the samples exist, this
	// Server refused them, and only one of those is fixed in the Cluster.
	if result.Issues[0].Reason == IssueNoData {
		t.Fatal("a refused Cluster was reported as silent")
	}
}

func TestTopQueriesDoNotReportRankedOutClustersAsSilent(t *testing.T) {
	t.Parallel()

	body := `{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{"zke_cluster_id":"` + clusterOne + `","node":"node-1"},"values":[[1755216000,"1"]]}
	]}}`
	captured := &capturedQuery{}
	service := testService(t, body, http.StatusOK, captured)
	input := rangeInput("node_cpu_usage")
	input.Top = 1
	result, err := service.Query(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Issues) != 0 {
		t.Fatalf("a Top N answer reported ranked-out Clusters as issues: %+v", result.Issues)
	}
}

func TestNewServiceRequiresAnAbsoluteQueryURL(t *testing.T) {
	t.Parallel()

	for _, queryURL := range []string{"", "/prometheus", "ftp://host"} {
		if _, err := NewService(
			Config{QueryURL: queryURL},
			stubVisibility{},
			stubClusters{},
			slog.New(slog.DiscardHandler),
		); err == nil {
			t.Fatalf("query URL %q was accepted", queryURL)
		}
	}
}
