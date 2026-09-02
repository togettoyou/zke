package metricsqlguard

import (
	"errors"
	"strings"
	"testing"

	"github.com/VictoriaMetrics/metricsql"
)

const (
	testLabel  = "zke_cluster_id"
	testTarget = "64b4fa9d-21b6-4268-98a2-c6e1acd9ac67"
)

// scoped is what the guard appends to every alternative of every selector.
var scopedFilter = testLabel + `="` + testTarget + `"`

func enforce(t *testing.T, expression string) string {
	t.Helper()
	result, err := Enforce(expression, testLabel, testTarget)
	if err != nil {
		t.Fatalf("Enforce(%q) returned error: %v", expression, err)
	}
	return result.Expression
}

func TestWithoutLabelRemovesClusterIdentityEverywhere(t *testing.T) {
	t.Parallel()
	expression := `sum by (zke_cluster_id, namespace) (rate(a{zke_cluster_id="cluster",job="api"}[5m])) / on (zke_cluster_id, namespace) group_left(zke_cluster_id, pod) b{zke_cluster_id=~".*"}`
	got, err := WithoutLabel(expression, testLabel)
	if err != nil {
		t.Fatalf("WithoutLabel returned error: %v", err)
	}
	if strings.Contains(got, testLabel) {
		t.Fatalf("display expression still contains %s: %s", testLabel, got)
	}
	if !strings.Contains(got, `a{job="api"}`) || !strings.Contains(got, "by(namespace)") {
		t.Fatalf("display expression lost non-cluster semantics: %s", got)
	}
}

func TestEnforceInjectsTheClusterFilter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		expression string
		want       string
	}{
		{
			name:       "bare metric name",
			expression: `node_memory_working_set_bytes`,
			want:       `node_memory_working_set_bytes{` + scopedFilter + `}`,
		},
		{
			name:       "aggregation over a bare metric name",
			expression: `sum by (zke_cluster_id) (node_memory_working_set_bytes)`,
			want:       `sum(node_memory_working_set_bytes{` + scopedFilter + `}) by(zke_cluster_id)`,
		},
		{
			name:       "existing filters are kept",
			expression: `up{job="kubelet"}`,
			want:       `up{job="kubelet",` + scopedFilter + `}`,
		},
		{
			name:       "empty filter list",
			expression: `up{}`,
			want:       `up{` + scopedFilter + `}`,
		},
		{
			name:       "name-only selector",
			expression: `{__name__="up"}`,
			want:       `up{` + scopedFilter + `}`,
		},
		{
			name:       "range vector inside a call",
			expression: `rate(http_requests_total[5m])`,
			want:       `rate(http_requests_total{` + scopedFilter + `}[5m])`,
		},
		{
			name:       "both sides of a binary operation",
			expression: `node_cpu_usage_seconds_total / node_cpu_capacity`,
			want: `node_cpu_usage_seconds_total{` + scopedFilter + `} / ` +
				`node_cpu_capacity{` + scopedFilter + `}`,
		},
		{
			name:       "vector matching keeps grouping labels alone",
			expression: `a / on(instance) group_left(job) b`,
			want: `a{` + scopedFilter + `} / on(instance) group_left(job) ` +
				`b{` + scopedFilter + `}`,
		},
		{
			name:       "recording rule names carrying colons",
			expression: `level:node_cpu:rate5m`,
			want:       `level:node_cpu:rate5m{` + scopedFilter + `}`,
		},
		{
			name:       "subquery",
			expression: `max_over_time(rate(x[5m])[1h:1m])`,
			want:       `max_over_time(rate(x{` + scopedFilter + `}[5m])[1h:1m])`,
		},
		{
			name:       "offset and at modifiers",
			expression: `up offset 5m @ end()`,
			want:       `up{` + scopedFilter + `} offset 5m @ end()`,
		},
		{
			name:       "at modifier holding a subexpression",
			expression: `foo @ (end() - 1h)`,
			want:       `foo{` + scopedFilter + `} @ (end() - 1h)`,
		},
		{
			name:       "set operators are not metric names",
			expression: `up unless on(instance) down`,
			want:       `up{` + scopedFilter + `} unless on(instance) down{` + scopedFilter + `}`,
		},
		{
			name:       "string arguments are left alone",
			expression: `label_replace(up, "dst", "$1", "instance", "(.*)")`,
			want:       `label_replace(up{` + scopedFilter + `}, "dst", "$1", "instance", "(.*)")`,
		},
		{
			name:       "scalar expressions have nothing to filter",
			expression: `time() - 3600`,
			want:       `time() - 3600`,
		},
		// The MetricsQL extensions. Each of these is a shape a PromQL-only
		// guard would either reject or misread.
		{
			name:       "MetricsQL default operator",
			expression: `foo default bar`,
			want:       `foo{` + scopedFilter + `} default bar{` + scopedFilter + `}`,
		},
		{
			name:       "MetricsQL keep_metric_names",
			expression: `rate(x[5m]) keep_metric_names`,
			want:       `rate(x{` + scopedFilter + `}[5m]) keep_metric_names`,
		},
		{
			name:       "MetricsQL aggregate limit",
			expression: `topk(5, x) limit 3`,
			want:       `topk(5, x{` + scopedFilter + `}) limit 3`,
		},
		{
			name:       "MetricsQL offset after an aggregation",
			expression: `sum(foo) offset 24h`,
			want:       `sum(foo{` + scopedFilter + `}) offset 24h`,
		},
		{
			name:       "MetricsQL step-relative duration",
			expression: `rate(x[5i])`,
			want:       `rate(x{` + scopedFilter + `}[5i])`,
		},
		{
			name:       "MetricsQL fractional duration",
			expression: `avg_over_time(x[1.5h])`,
			want:       `avg_over_time(x{` + scopedFilter + `}[1.5h])`,
		},
		{
			name:       "MetricsQL bare-number duration",
			expression: `rate(x[300])`,
			want:       `rate(x{` + scopedFilter + `}[300])`,
		},
		{
			name:       "MetricsQL underscore-delimited numbers",
			expression: `node_load1 > 1_000`,
			want:       `node_load1{` + scopedFilter + `} > 1_000`,
		},
		{
			name:       "MetricsQL numeric constant list",
			expression: `x{a="1"} == (1, 2)`,
			want:       `x{a="1",` + scopedFilter + `} == (1, 2)`,
		},
		{
			name:       "MetricsQL escaped characters in a metric name",
			expression: `foo\-bar`,
			want:       `foo\-bar{` + scopedFilter + `}`,
		},
		{
			// The guard has to see through the template: a filter placed on
			// the reference rather than on what it expands to would scope
			// nothing.
			name:       "MetricsQL WITH template",
			expression: `WITH (f = {job="x"}) up{f}`,
			want:       `up{job="x",` + scopedFilter + `}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := enforce(t, testCase.expression); got != testCase.want {
				t.Errorf("Enforce(%q)\n got: %s\nwant: %s", testCase.expression, got, testCase.want)
			}
		})
	}
}

// The guard's reason for existing: whatever the author wrote about the scope
// label, the expression that runs names the Cluster the Server chose.
func TestEnforceReplacesAnAuthorSuppliedClusterFilter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		expression string
		want       string
	}{
		{
			name:       "only filter",
			expression: `up{zke_cluster_id="11111111-1111-1111-1111-111111111111"}`,
			want:       `up{` + scopedFilter + `}`,
		},
		{
			name:       "among others",
			expression: `up{job="a", zke_cluster_id="somebody-else", instance="b"}`,
			want:       `up{job="a",instance="b",` + scopedFilter + `}`,
		},
		{
			name:       "regular expression filter",
			expression: `up{zke_cluster_id=~".*"}`,
			want:       `up{` + scopedFilter + `}`,
		},
		{
			name:       "negative filter",
			expression: `up{zke_cluster_id!="` + testTarget + `"}`,
			want:       `up{` + scopedFilter + `}`,
		},
		{
			name:       "quoted label name",
			expression: `up{"zke_cluster_id"="somebody-else"}`,
			want:       `up{` + scopedFilter + `}`,
		},
		{
			name:       "repeated in several selectors",
			expression: `up{zke_cluster_id="a"} + down{zke_cluster_id="b"}`,
			want:       `up{` + scopedFilter + `} + down{` + scopedFilter + `}`,
		},
		{
			// Every alternative of an or-filter is its own way for a series to
			// be selected, so every one of them has to carry the filter.
			name:       "inside every alternative of an or filter",
			expression: `{job="a" or zke_cluster_id="elsewhere", job="b"}`,
			want:       `{job="a",` + scopedFilter + ` or job="b",` + scopedFilter + `}`,
		},
		{
			name:       "hidden inside a WITH template",
			expression: `WITH (f = {zke_cluster_id="elsewhere"}) up{f}`,
			want:       `up{` + scopedFilter + `}`,
		},
		{
			name:       "hidden inside a subquery",
			expression: `max_over_time(rate(x{zke_cluster_id="elsewhere"}[5m])[1h:1m])`,
			want:       `max_over_time(rate(x{` + scopedFilter + `}[5m])[1h:1m])`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := enforce(t, testCase.expression); got != testCase.want {
				t.Errorf("Enforce(%q)\n got: %s\nwant: %s", testCase.expression, got, testCase.want)
			}
		})
	}
}

// Everything below checks the property rather than a particular rendering: any
// expression the guard accepts describes the target Cluster and nothing else.
func TestEverySelectorEndsUpScoped(t *testing.T) {
	t.Parallel()

	corpus := []string{
		`up`,
		`up{job="a"}`,
		`{__name__=~"node_.+"}`,
		`sum by (node) (rate(node_cpu_usage_seconds_total[5m]))`,
		`a and b or c unless d`,
		`histogram_quantile(0.9, sum by (le) (rate(x[5m])))`,
		`max_over_time((a/b)[1h:1m])`,
		`label_replace(a, "x", "$1", "y", "(.*)") + on(x) group_right(y) b`,
		`(a offset 1h) / (b @ 1700000000)`,
		`topk(5, sum by (pod) (container_memory_working_set_bytes{namespace="default"}))`,
		`a{x="1"} or b{y="2"} or {z="3"}`,
		`quantile_over_time(0.5, node_load1[1h]) > bool 2`,
		`count(up == 1) / count(up)`,
		`WITH (f = {job="x"}, q = rate(y[5m])) sum(q) + count(up{f})`,
		`rate(x{a="1" or b="2"}[5m])`,
		`sum(rate(x[5m])) by (node) > 0 default 0`,
		`x @ (end() - 1h) offset 5i`,
		`union(a, b, c)`,
	}
	for _, expression := range corpus {
		assertEverySelectorScoped(t, expression, enforce(t, expression))
	}
}

// assertEverySelectorScoped re-reads the guard's own output the way storage
// will and insists that every way a series can be selected names the target.
func assertEverySelectorScoped(t *testing.T, source, rewritten string) {
	t.Helper()
	parsed, err := metricsql.Parse(rewritten)
	if err != nil {
		t.Fatalf("%q produced %q, which does not parse: %v", source, rewritten, err)
	}
	metricsql.VisitAll(parsed, func(expr metricsql.Expr) {
		selector, ok := expr.(*metricsql.MetricExpr)
		if !ok {
			return
		}
		if len(selector.LabelFilterss) == 0 {
			t.Fatalf("%q left a selector with no filters in %q", source, rewritten)
		}
		for _, group := range selector.LabelFilterss {
			matched := 0
			for _, filter := range group {
				if filter.Label != testLabel {
					continue
				}
				if filter.Value != testTarget || filter.IsRegexp || filter.IsNegative {
					t.Fatalf("%q left the filter %v in %q", source, filter, rewritten)
				}
				matched++
			}
			if matched != 1 {
				t.Fatalf("%q left an alternative with %d cluster filters in %q",
					source, matched, rewritten)
			}
		}
	})
}

// Running the guard over its own output must not change it. A rewrite that
// accumulated filters would grow an expression every time a saved query was
// re-run through the same path.
func TestEnforceIsIdempotent(t *testing.T) {
	t.Parallel()

	for _, expression := range []string{
		`up`,
		`up{job="a"}`,
		`sum by (node) (rate(x[5m]))`,
		`{a="1" or b="2"}`,
		`a / on(x) group_left b`,
		`WITH (f = {job="x"}) up{f}`,
	} {
		once := enforce(t, expression)
		twice := enforce(t, once)
		if once != twice {
			t.Errorf("Enforce is not idempotent for %q:\n once: %s\ntwice: %s",
				expression, once, twice)
		}
	}
}

func TestEnforceRejectsWhatItCannotScope(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		expression string
		wantSyntax bool
	}{
		{name: "empty", expression: "   "},
		{name: "comment only", expression: "# nothing here", wantSyntax: true},
		{name: "unclosed brace", expression: `up{job="a"`, wantSyntax: true},
		{name: "unclosed string", expression: `up{job="a}`, wantSyntax: true},
		{name: "stray closing paren", expression: `sum(up))`, wantSyntax: true},
		{name: "mismatched brackets", expression: `rate(x[5m)]`, wantSyntax: true},
		{name: "unknown function", expression: `not_a_function(up)`, wantSyntax: true},
		{name: "too long", expression: `up{job="` + strings.Repeat("a", MaxExpressionBytes) + `"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := Enforce(testCase.expression, testLabel, testTarget)
			if err == nil {
				t.Fatalf("Enforce(%q) accepted an expression it should refuse", testCase.expression)
			}
			var syntax *SyntaxError
			if testCase.wantSyntax != errors.As(err, &syntax) {
				t.Fatalf("Enforce(%q) error = %v, want syntax error = %v",
					testCase.expression, err, testCase.wantSyntax)
			}
			if !testCase.wantSyntax && !errors.Is(err, ErrUnsupported) {
				t.Fatalf("Enforce(%q) error = %v, want ErrUnsupported", testCase.expression, err)
			}
		})
	}
}

func TestEnforceRefusesAnInvalidGuardLabel(t *testing.T) {
	t.Parallel()

	for _, label := range []string{"", "1bad", `a"b`, "a-b"} {
		if _, err := Enforce("up", label, testTarget); err == nil {
			t.Errorf("Enforce accepted the guard label %q", label)
		}
	}
}

// The warning VictoriaMetrics itself raises for implicit conversions travels
// with the answer rather than blocking it.
func TestEnforceReportsLikelyInvalidExpressions(t *testing.T) {
	t.Parallel()

	suspicious, err := Enforce(`rate(sum(x))`, testLabel, testTarget)
	if err != nil {
		t.Fatalf("Enforce returned %v", err)
	}
	if !suspicious.LikelyInvalid {
		t.Error("rate(sum(x)) was not reported as likely invalid")
	}
	ordinary, err := Enforce(`sum(rate(x[5m]))`, testLabel, testTarget)
	if err != nil {
		t.Fatalf("Enforce returned %v", err)
	}
	if ordinary.LikelyInvalid {
		t.Error("sum(rate(x[5m])) was reported as likely invalid")
	}
}

func TestEnforceCountsSelectors(t *testing.T) {
	t.Parallel()

	cases := map[string]int{
		`time()`:      0,
		`up`:          1,
		`a + b`:       2,
		`sum(a) / 60`: 1,
	}
	for expression, want := range cases {
		result, err := Enforce(expression, testLabel, testTarget)
		if err != nil {
			t.Fatalf("Enforce(%q): %v", expression, err)
		}
		if result.Selectors != want {
			t.Errorf("Enforce(%q).Selectors = %d, want %d", expression, result.Selectors, want)
		}
	}
}

func TestValidateAcceptsWhatEnforceCanRewrite(t *testing.T) {
	t.Parallel()

	if err := Validate(`sum by (node) (rate(x[5m]))`, testLabel); err != nil {
		t.Fatalf("Validate returned %v", err)
	}
	if err := Validate(`sum by (node`, testLabel); err == nil {
		t.Fatal("Validate accepted an unbalanced expression")
	}
}

// FuzzEnforce checks the two properties that hold for any input at all: the
// guard never panics, and anything it accepts comes back scoped.
func FuzzEnforce(f *testing.F) {
	for _, seed := range []string{
		`up`,
		`up{job="a"}`,
		`{__name__="up",zke_cluster_id="x"}`,
		`sum by (a) (rate(x[5m]))`,
		`a # }{`,
		"a{b=`c`}",
		`a{b="\"}"}`,
		`WITH (f={a="1"}) up{f}`,
		`1.5h`,
		`x[5i] offset -1h @ start()`,
		`{a="1" or b="2"}`,
		`foo\-bar{a="1"} == (1, 2)`,
		`rate(x[300]) keep_metric_names`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, expression string) {
		result, err := Enforce(expression, testLabel, testTarget)
		if err != nil {
			return
		}
		assertEverySelectorScoped(t, expression, result.Expression)
		// Re-running the guard must not accumulate anything. The check above
		// already carries most of that — one filter per alternative, never two
		// — and this adds the textual half: the rewrite reaches a fixed point.
		//
		// The fixed point is taken from the second round rather than the
		// first, because the parser folds constants as it prints. `1/0` comes
		// back as `+Inf` and then as `Inf`, which is a rendering settling down
		// rather than the guard changing its mind.
		//
		// Skipped where the filters pushed the expression past the byte cap:
		// that is a refusal, not a different rewrite.
		if len(result.Expression) > MaxExpressionBytes {
			return
		}
		// A refusal on the way round is an acceptable outcome, and the only
		// one the fuzzer finds: VictoriaMetrics' printer escapes a few exotic
		// identifiers into a form its own parser will not read back, and the
		// self-check in Enforce turns that into a refused query rather than an
		// expression nobody can predict the meaning of. Fail-closed is the
		// property under test here, not "always succeeds".
		second, err := Enforce(result.Expression, testLabel, testTarget)
		if err != nil {
			return
		}
		assertEverySelectorScoped(t, expression, second.Expression)
		if len(second.Expression) > MaxExpressionBytes {
			return
		}
		third, err := Enforce(second.Expression, testLabel, testTarget)
		if err != nil {
			return
		}
		if third.Expression != second.Expression {
			t.Fatalf("guard never settles for %q:\n second: %s\n third: %s",
				expression, second.Expression, third.Expression)
		}
	})
}
