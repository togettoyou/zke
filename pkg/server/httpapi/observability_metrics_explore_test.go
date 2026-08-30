package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/metricsquery"
)

type fakeMetricsQueryService struct {
	explore func(context.Context, metricsquery.ExploreInput) (metricsquery.ExploreResult, error)
}

func (service *fakeMetricsQueryService) Catalog() []metricsquery.Definition { return nil }

func (service *fakeMetricsQueryService) Query(
	context.Context,
	metricsquery.Input,
) (metricsquery.Result, error) {
	return metricsquery.Result{}, nil
}

func (service *fakeMetricsQueryService) Explore(
	ctx context.Context,
	input metricsquery.ExploreInput,
) (metricsquery.ExploreResult, error) {
	return service.explore(ctx, input)
}

func exploreTestRouter(service metricsQueryService) http.Handler {
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	router := gin.New()
	handler := newObservabilityMetricsHandler(discardLogger(), service, 5*time.Second)
	router.POST("/api/v1/observability/metrics/explore", handler.explore)
	return router
}

func postExplore(handler http.Handler, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/observability/metrics/explore",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	return response
}

func TestExploreHandlerReturnsEachExpressionsOwnOutcome(t *testing.T) {
	t.Parallel()

	value := 12.5
	service := &fakeMetricsQueryService{explore: func(
		_ context.Context,
		input metricsquery.ExploreInput,
	) (metricsquery.ExploreResult, error) {
		if input.ClusterID != testHTTPClusterID {
			t.Fatalf("cluster ID = %q", input.ClusterID)
		}
		if input.Kind != metricsquery.KindRange || input.Step != time.Minute {
			t.Fatalf("input = %+v", input)
		}
		if len(input.Queries) != 2 {
			t.Fatalf("queries = %+v", input.Queries)
		}
		return metricsquery.ExploreResult{
			ClusterID:   input.ClusterID,
			ClusterName: "prod-sh",
			Kind:        metricsquery.KindRange,
			StepSeconds: 60,
			Queries: []metricsquery.ExploreOutcome{
				{
					RefID:               "A",
					Expression:          "up",
					EffectiveExpression: `up{zke_cluster_id="x"}`,
					ResultType:          "matrix",
					Duration:            7 * time.Millisecond,
					Warning:             metricsquery.ExploreWarningLikelyInvalid,
					Series: []metricsquery.Series{{
						Labels: map[string]string{"node": "a"},
						Points: []metricsquery.Point{
							{UnixSeconds: 100, Value: &value},
							{UnixSeconds: 160},
						},
					}},
				},
				{
					RefID:      "B",
					Expression: "sum by (node",
					Error: &metricsquery.ExploreError{
						Code:   metricsquery.ExploreErrorInvalidExpression,
						Detail: "MetricsQL 解析失败",
					},
				},
			},
		}, nil
	}}

	response := postExplore(exploreTestRouter(service), `{
		"cluster_id": "`+testHTTPClusterID+`",
		"kind": "range",
		"start": "2026-08-30T00:00:00Z",
		"end": "2026-08-30T01:00:00Z",
		"step_seconds": 60,
		"queries": [
			{"ref_id": "A", "expression": "up"},
			{"ref_id": "B", "expression": "sum by (node"}
		]
	}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	var result metricsExploreResponse
	if err := decodeSuccessResponse(response, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Queries) != 2 {
		t.Fatalf("queries = %+v", result.Queries)
	}
	first := result.Queries[0]
	if first.RefID != "A" || first.EffectiveExpression != `up{zke_cluster_id="x"}` {
		t.Fatalf("first outcome = %+v", first)
	}
	if first.DurationMs != 7 || first.Warning != metricsquery.ExploreWarningLikelyInvalid {
		t.Fatalf("first outcome = %+v", first)
	}
	// A gap has to survive the wire as null: a client that received a zero
	// would draw a collection outage as a value.
	if len(first.Series) != 1 || len(first.Series[0].Points) != 2 {
		t.Fatalf("series = %+v", first.Series)
	}
	if first.Series[0].Points[1][1] != nil {
		t.Fatalf("a gap was serialised as %v", first.Series[0].Points[1][1])
	}
	second := result.Queries[1]
	if second.Error == nil || second.Error.Code != metricsquery.ExploreErrorInvalidExpression {
		t.Fatalf("second outcome = %+v", second.Error)
	}
	if second.Error.Detail == "" {
		t.Error("a parse failure carried nothing the author can act on")
	}
}

func TestExploreHandlerRejectsMalformedRequests(t *testing.T) {
	t.Parallel()

	service := &fakeMetricsQueryService{explore: func(
		context.Context,
		metricsquery.ExploreInput,
	) (metricsquery.ExploreResult, error) {
		t.Fatal("the service was called for a request the handler should have refused")
		return metricsquery.ExploreResult{}, nil
	}}
	router := exploreTestRouter(service)

	valid := `{"ref_id": "A", "expression": "up"}`
	cases := map[string]string{
		"no cluster":        `{"queries": [` + valid + `]}`,
		"no expressions":    `{"cluster_id": "` + testHTTPClusterID + `", "queries": []}`,
		"unknown kind":      `{"cluster_id": "` + testHTTPClusterID + `", "kind": "table", "queries": [` + valid + `]}`,
		"bad timestamp":     `{"cluster_id": "` + testHTTPClusterID + `", "end": "yesterday", "queries": [` + valid + `]}`,
		"bad step":          `{"cluster_id": "` + testHTTPClusterID + `", "step_seconds": 999999, "queries": [` + valid + `]}`,
		"empty reference":   `{"cluster_id": "` + testHTTPClusterID + `", "queries": [{"ref_id": "", "expression": "up"}]}`,
		"unknown field":     `{"cluster_id": "` + testHTTPClusterID + `", "queries": [` + valid + `], "extra": 1}`,
		"not json":          `nonsense`,
		"duplicate ref ids": `{"cluster_id": "` + testHTTPClusterID + `", "queries": [` + valid + `, ` + valid + `]}`,
	}
	for name, body := range cases {
		response := postExplore(router, body)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d: %s", name, response.Code, response.Body)
		}
	}

	// One past the ceiling, built from the constant so the two cannot drift.
	var many strings.Builder
	many.WriteString(`{"cluster_id": "` + testHTTPClusterID + `", "queries": [`)
	for index := 0; index <= metricsquery.MaxExploreQueries; index++ {
		if index > 0 {
			many.WriteByte(',')
		}
		many.WriteString(`{"ref_id": "r`)
		many.WriteString(string(rune('a' + index)))
		many.WriteString(`", "expression": "up"}`)
	}
	many.WriteString(`]}`)
	if response := postExplore(router, many.String()); response.Code != http.StatusBadRequest {
		t.Errorf("too many expressions: status = %d: %s", response.Code, response.Body)
	}
}

func TestExploreHandlerMapsServiceFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		err    error
		status int
		code   string
	}{
		{metricsquery.ErrDenied, http.StatusForbidden, "forbidden"},
		{metricsquery.ErrNoVisibility, http.StatusForbidden, "no_visible_cluster"},
		{metricsquery.ErrBusy, http.StatusTooManyRequests, "too_many_requests"},
		{metricsquery.ErrInvalidInput, http.StatusBadRequest, "invalid_request"},
		{metricsquery.ErrUnavailable, http.StatusServiceUnavailable, "storage_unavailable"},
		{metricsquery.ErrTimeout, http.StatusGatewayTimeout, "timeout"},
	}
	body := `{"cluster_id": "` + testHTTPClusterID + `", "queries": [{"ref_id": "A", "expression": "up"}]}`
	for _, testCase := range cases {
		service := &fakeMetricsQueryService{explore: func(
			context.Context,
			metricsquery.ExploreInput,
		) (metricsquery.ExploreResult, error) {
			return metricsquery.ExploreResult{}, testCase.err
		}}
		response := postExplore(exploreTestRouter(service), body)
		if response.Code != testCase.status {
			t.Errorf("%v: status = %d: %s", testCase.err, response.Code, response.Body)
			continue
		}
		assertErrorCode(t, response, testCase.code)
	}
}

// Without metrics storage the route stays registered and says so, rather than
// disappearing from a Console built against this Server.
func TestExploreHandlerReportsDisabledMetrics(t *testing.T) {
	t.Parallel()

	response := postExplore(
		exploreTestRouter(metricsQueryServiceOrNil(nil)),
		`{"cluster_id": "`+testHTTPClusterID+`", "queries": [{"ref_id": "A", "expression": "up"}]}`,
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	assertErrorCode(t, response, "metrics_disabled")
}
