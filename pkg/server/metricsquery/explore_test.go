package metricsquery

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VictoriaMetrics/metricsql"
	"github.com/togettoyou/zke/pkg/server/metricsingest"
	"github.com/togettoyou/zke/pkg/server/store"
)

// capturedExplore records every request the backend received, since one Explore
// call issues several.
type capturedExplore struct {
	mutex       sync.Mutex
	expressions []string
	extraLabels []string
	paths       []string
}

func (captured *capturedExplore) record(expression, extraLabel, path string) {
	captured.mutex.Lock()
	defer captured.mutex.Unlock()
	captured.expressions = append(captured.expressions, expression)
	captured.extraLabels = append(captured.extraLabels, extraLabel)
	captured.paths = append(captured.paths, path)
}

func (captured *capturedExplore) snapshot() ([]string, []string, []string) {
	captured.mutex.Lock()
	defer captured.mutex.Unlock()
	return append([]string(nil), captured.expressions...),
		append([]string(nil), captured.extraLabels...),
		append([]string(nil), captured.paths...)
}

func exploreService(
	t *testing.T,
	captured *capturedExplore,
	respond func(expression string) (int, string),
) *Service {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			if err := request.ParseForm(); err != nil {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			expression := request.Form.Get("query")
			captured.record(expression, request.Form.Get("extra_label"), request.URL.Path)
			status, body := respond(expression)
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

func emptyMatrix(string) (int, string) {
	return http.StatusOK, `{"status":"success","data":{"resultType":"matrix","result":[]}}`
}

func exploreInput(expressions ...string) ExploreInput {
	end := time.Unix(1_755_216_000, 0).UTC()
	input := ExploreInput{
		UserID:    userID,
		ClusterID: clusterOne,
		Kind:      KindRange,
		Start:     end.Add(-2 * time.Minute),
		End:       end,
		Step:      time.Minute,
	}
	for index, expression := range expressions {
		input.Queries = append(input.Queries, ExploreQuery{
			RefID:      string(rune('A' + index)),
			Expression: expression,
		})
	}
	return input
}

// The property the whole feature rests on: whatever the author wrote, the
// expression that reaches storage describes the Cluster the Server resolved.
func TestExploreScopesEverySelectorToTheTargetCluster(t *testing.T) {
	t.Parallel()

	authored := []string{
		`node_memory_working_set_bytes`,
		`sum by (zke_cluster_id) (node_memory_working_set_bytes)`,
		`up{zke_cluster_id="` + clusterTwo + `"}`,
		`rate(a[5m]) / on(node) b{zke_cluster_id=~".*"}`,
		`{__name__="up" or zke_cluster_id="` + clusterTwo + `"}`,
	}
	captured := &capturedExplore{}
	service := exploreService(t, captured, emptyMatrix)
	result, err := service.Explore(context.Background(), exploreInput(authored...))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Queries) != len(authored) {
		t.Fatalf("got %d outcomes, want %d", len(result.Queries), len(authored))
	}
	for _, outcome := range result.Queries {
		if outcome.Error != nil {
			t.Fatalf("%q failed: %+v", outcome.Expression, outcome.Error)
		}
	}
	expressions, extraLabels, paths := captured.snapshot()
	if len(expressions) != len(authored) {
		t.Fatalf("backend saw %d queries, want %d", len(expressions), len(authored))
	}
	for _, expression := range expressions {
		assertScopedToCluster(t, expression, clusterOne)
		// The Cluster the caller could have asked for but did not must not
		// appear: the scope is the target, not the caller's whole visibility.
		if strings.Contains(expression, clusterTwo) {
			t.Fatalf("expression reaches an unrequested Cluster: %s", expression)
		}
	}
	// The storage is told the same thing independently, so a selector the
	// rewrite somehow missed still matches nothing.
	for _, extra := range extraLabels {
		if extra != metricsingest.ClusterLabel+"="+clusterOne {
			t.Fatalf("extra_label = %q", extra)
		}
	}
	for _, path := range paths {
		if !strings.HasSuffix(path, "/api/v1/query_range") {
			t.Fatalf("range query hit %s", path)
		}
	}
}

func assertScopedToCluster(t *testing.T, expression, clusterID string) {
	t.Helper()
	parsed, err := metricsql.Parse(expression)
	if err != nil {
		t.Fatalf("storage was sent an unparseable expression %q: %v", expression, err)
	}
	metricsql.VisitAll(parsed, func(expr metricsql.Expr) {
		selector, ok := expr.(*metricsql.MetricExpr)
		if !ok {
			return
		}
		if len(selector.LabelFilterss) == 0 {
			t.Fatalf("unscoped selector in %q", expression)
		}
		for _, group := range selector.LabelFilterss {
			scoped := false
			for _, filter := range group {
				if filter.Label != metricsingest.ClusterLabel {
					continue
				}
				if filter.Value != clusterID || filter.IsRegexp || filter.IsNegative {
					t.Fatalf("selector in %q carries %v", expression, filter)
				}
				scoped = true
			}
			if !scoped {
				t.Fatalf("unscoped alternative in %q", expression)
			}
		}
	})
}

// The rewritten expression goes back to the author. It is the one place the
// Server changes somebody's own query, and a rewrite they cannot read is a
// rewrite they cannot check.
func TestExploreReturnsTheExpressionThatRan(t *testing.T) {
	t.Parallel()

	captured := &capturedExplore{}
	service := exploreService(t, captured, emptyMatrix)
	result, err := service.Explore(
		context.Background(),
		exploreInput(`node_memory_working_set_bytes`),
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome := result.Queries[0]
	if outcome.Expression != `node_memory_working_set_bytes` {
		t.Errorf("Expression = %q", outcome.Expression)
	}
	want := `node_memory_working_set_bytes{` + metricsingest.ClusterLabel + `="` + clusterOne + `"}`
	if outcome.EffectiveExpression != want {
		t.Errorf("EffectiveExpression = %q, want %q", outcome.EffectiveExpression, want)
	}
	expressions, _, _ := captured.snapshot()
	if expressions[0] != outcome.EffectiveExpression {
		t.Errorf("the reported expression is not the one that ran: %q vs %q",
			outcome.EffectiveExpression, expressions[0])
	}
}

// One bad expression does not blank the others.
func TestExploreIsolatesPerExpressionFailures(t *testing.T) {
	t.Parallel()

	captured := &capturedExplore{}
	service := exploreService(t, captured, emptyMatrix)
	result, err := service.Explore(
		context.Background(),
		exploreInput(`up`, `sum by (node`, `node_load1`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Queries[0].Error != nil || result.Queries[2].Error != nil {
		t.Fatal("a valid expression failed because another one did not parse")
	}
	failed := result.Queries[1]
	if failed.Error == nil || failed.Error.Code != ExploreErrorInvalidExpression {
		t.Fatalf("second outcome = %+v", failed.Error)
	}
	if failed.Error.Detail == "" {
		t.Error("a parse failure carried nothing the author can act on")
	}
	// An expression that never parsed must never have been sent.
	expressions, _, _ := captured.snapshot()
	if len(expressions) != 2 {
		t.Fatalf("backend saw %d queries, want 2", len(expressions))
	}
}

// A query the storage refuses is reported as the storage's answer about the
// author's own expression, not as "the storage is down" — the two send an
// operator to completely different places.
func TestExploreReportsStorageRejections(t *testing.T) {
	t.Parallel()

	captured := &capturedExplore{}
	service := exploreService(t, captured, func(string) (int, string) {
		return http.StatusUnprocessableEntity,
			`{"status":"error","errorType":"422","error":"cannot select more than 30000 time series"}`
	})
	result, err := service.Explore(context.Background(), exploreInput(`{__name__=~".+"}`))
	if err != nil {
		t.Fatal(err)
	}
	outcome := result.Queries[0]
	if outcome.Error == nil || outcome.Error.Code != ExploreErrorRejected {
		t.Fatalf("outcome = %+v", outcome.Error)
	}
	if !strings.Contains(outcome.Error.Detail, "30000 time series") {
		t.Errorf("detail = %q", outcome.Error.Detail)
	}
}

// A catalogue query the storage refuses is a defect in this Server, not in the
// request, so it stays an unavailability rather than repeating an expression
// nobody outside this process wrote.
func TestCatalogueQueryHidesStorageRejections(t *testing.T) {
	t.Parallel()

	captured := &capturedQuery{}
	service := testService(
		t,
		`{"status":"error","errorType":"422","error":"internal detail"}`,
		http.StatusUnprocessableEntity,
		captured,
	)
	_, err := service.Query(context.Background(), rangeInput("cluster_cpu_usage"))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Query() error = %v, want ErrUnavailable", err)
	}
	if strings.Contains(err.Error(), "internal detail") {
		t.Errorf("the backend's message reached the caller: %v", err)
	}
}

func TestExploreRefusesRequestsItWillNotRun(t *testing.T) {
	t.Parallel()

	captured := &capturedExplore{}
	service := exploreService(t, captured, emptyMatrix)

	cases := map[string]func(ExploreInput) ExploreInput{
		"no expressions": func(input ExploreInput) ExploreInput {
			input.Queries = nil
			return input
		},
		"too many expressions": func(input ExploreInput) ExploreInput {
			for len(input.Queries) <= MaxExploreQueries {
				input.Queries = append(input.Queries, ExploreQuery{RefID: "X", Expression: "up"})
			}
			return input
		},
		"unknown kind": func(input ExploreInput) ExploreInput {
			input.Kind = "table"
			return input
		},
		"empty window": func(input ExploreInput) ExploreInput {
			input.Start = input.End
			return input
		},
		"step below the floor": func(input ExploreInput) ExploreInput {
			input.Step = time.Second
			return input
		},
	}
	for name, mutate := range cases {
		if _, err := service.Explore(
			context.Background(),
			mutate(exploreInput("up")),
		); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Explore(%s) error = %v, want ErrInvalidInput", name, err)
		}
	}

	// A Cluster outside the caller's visibility is refused before anything
	// runs, and without saying whether it exists.
	outside := exploreInput("up")
	outside.ClusterID = foreign
	if _, err := service.Explore(context.Background(), outside); !errors.Is(err, ErrDenied) {
		t.Errorf("Explore(foreign cluster) error = %v, want ErrDenied", err)
	}
	if expressions, _, _ := captured.snapshot(); len(expressions) != 0 {
		t.Errorf("a refused request still reached storage: %v", expressions)
	}
}

// An instant query asks the instant endpoint and keeps its own shape.
func TestExploreSupportsInstantQueries(t *testing.T) {
	t.Parallel()

	captured := &capturedExplore{}
	service := exploreService(t, captured, func(string) (int, string) {
		return http.StatusOK, `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"__name__":"up","node":"a"},"value":[1755216000,"1"]}]}}`
	})
	input := exploreInput("up")
	input.Kind = KindInstant
	result, err := service.Explore(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	_, _, paths := captured.snapshot()
	if !strings.HasSuffix(paths[0], "/api/v1/query") {
		t.Fatalf("instant query hit %s", paths[0])
	}
	outcome := result.Queries[0]
	if outcome.ResultType != "vector" || len(outcome.Series) != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
	// Every label is kept, including __name__: the author chose what to group
	// by, and a projection would make two table rows look identical.
	if outcome.Series[0].Labels["__name__"] != "up" ||
		outcome.Series[0].Labels["node"] != "a" {
		t.Fatalf("labels = %v", outcome.Series[0].Labels)
	}
}

// A scalar answer is still an answer. `time()` is a reasonable thing to type
// into an expression box, and it has no series in it at all.
func TestExploreHandlesScalarAnswers(t *testing.T) {
	t.Parallel()

	captured := &capturedExplore{}
	service := exploreService(t, captured, func(string) (int, string) {
		return http.StatusOK,
			`{"status":"success","data":{"resultType":"scalar","result":[1755216000,"42"]}}`
	})
	input := exploreInput("time()")
	input.Kind = KindInstant
	result, err := service.Explore(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	outcome := result.Queries[0]
	if outcome.Error != nil {
		t.Fatalf("scalar answer failed: %+v", outcome.Error)
	}
	if len(outcome.Series) != 1 || len(outcome.Series[0].Points) != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if value := outcome.Series[0].Points[0].Value; value == nil || *value != 42 {
		t.Fatalf("scalar value = %v", value)
	}
}

// The warning VictoriaMetrics raises for implicit conversions travels with the
// answer instead of blocking it.
func TestExploreCarriesTheLikelyInvalidWarning(t *testing.T) {
	t.Parallel()

	captured := &capturedExplore{}
	service := exploreService(t, captured, emptyMatrix)
	result, err := service.Explore(context.Background(), exploreInput(`rate(sum(up))`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Queries[0].Warning != ExploreWarningLikelyInvalid {
		t.Errorf("Warning = %q", result.Queries[0].Warning)
	}
	if result.Queries[0].Error != nil {
		t.Error("a likely-invalid expression was refused instead of warned about")
	}
}

// Series past the ceiling are cut, and the answer says so rather than pretending
// it is the whole picture.
func TestExploreTruncatesAtTheSeriesCeiling(t *testing.T) {
	t.Parallel()

	var builder strings.Builder
	builder.WriteString(`{"status":"success","data":{"resultType":"vector","result":[`)
	for index := range DefaultMaxSeries + 5 {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(`{"metric":{"node":"n`)
		builder.WriteString(string(rune('a' + index%26)))
		builder.WriteString(string(rune('a' + index/26)))
		builder.WriteString(`"},"value":[1755216000,"1"]}`)
	}
	builder.WriteString(`]}}`)

	captured := &capturedExplore{}
	service := exploreService(t, captured, func(string) (int, string) {
		return http.StatusOK, builder.String()
	})
	input := exploreInput("up")
	input.Kind = KindInstant
	result, err := service.Explore(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	outcome := result.Queries[0]
	if !outcome.Truncated {
		t.Error("an answer past the ceiling did not report truncation")
	}
	if len(outcome.Series) != DefaultMaxSeries {
		t.Errorf("kept %d series, want %d", len(outcome.Series), DefaultMaxSeries)
	}
}

// The per-caller ceiling is what protects storage from one person holding the
// Execute key down: an expression's cost cannot be predicted from its text, so
// what is bounded is how many unpredictable things one caller can start.
func TestExploreLimitsConcurrentRequestsPerCaller(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	arrived := make(chan struct{}, maxExploreInFlightPerUser)
	captured := &capturedExplore{}
	service := exploreService(t, captured, func(string) (int, string) {
		arrived <- struct{}{}
		<-release
		return emptyMatrix("")
	})

	var running sync.WaitGroup
	for range maxExploreInFlightPerUser {
		running.Add(1)
		go func() {
			defer running.Done()
			_, _ = service.Explore(context.Background(), exploreInput("up"))
		}()
	}
	for range maxExploreInFlightPerUser {
		<-arrived
	}
	if _, err := service.Explore(
		context.Background(),
		exploreInput("up"),
	); !errors.Is(err, ErrBusy) {
		t.Errorf("Explore() beyond the per-caller ceiling error = %v, want ErrBusy", err)
	}
	close(release)
	running.Wait()

	// The slots are given back, so the next request is accepted.
	if _, err := service.Explore(context.Background(), exploreInput("up")); err != nil {
		t.Errorf("Explore() after the earlier ones finished error = %v", err)
	}
}
