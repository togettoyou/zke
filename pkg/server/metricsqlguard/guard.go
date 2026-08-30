// Package metricsqlguard confines a MetricsQL expression written by an
// operator to one Cluster.
//
// The Console's chart sections ask for named queries whose scope filter is part
// of the template. Explore is the other half of that story: an operator writes
// their own expression, and the Server has to make it describe exactly one
// Cluster no matter what they wrote. That is this package.
//
// The rule it enforces is a rewrite, not a validation: every series selector in
// the expression comes out carrying `zke_cluster_id="<target>"`, and any filter
// the author wrote on that label is removed first. Removing rather than
// intersecting is deliberate — an operator who pasted somebody else's
// expression, filter and all, gets their own Cluster's answer instead of an
// empty chart they would spend ten minutes explaining.
//
// The rewrite runs on VictoriaMetrics' own parser rather than on a lexer
// written here. The storage speaks MetricsQL, not PromQL, and the distance
// between the two is exactly where a hand-written guard goes wrong: `1_000`,
// `8Ki`, `1.5h`, `[5i]`, `[300]`, `foo\-bar`, `{a="1" or b="2"}`, `WITH`
// templates, `keep_metric_names`, `q == (1, 2)` and Unicode metric names are
// all valid input this Server has to place a filter inside. A guard that
// tokenised the text itself would have to re-derive that grammar and would fall
// behind it at every VictoriaMetrics release — and falling behind means either
// rejecting a query that works or, far worse, failing to recognise a selector
// and leaving it unscoped. Parsing with the same library the storage parses
// with removes that whole class of divergence: `Parse` expands WITH templates,
// `VisitAll` reaches every selector including those inside subqueries, `@` and
// `offset` modifiers, and `AppendString` prints something the storage will read
// back the way this package meant it.
//
// It is not the only thing standing between a caller and another Cluster's
// samples. Two more checks sit behind it: the rewritten expression is re-parsed
// and refused unless every selector in it carries the filter, and the query
// path sends the same filter to VictoriaMetrics as an `extra_label`, which
// applies it server-side to every selector it can see. A leak needs all three
// to fail at once.
package metricsqlguard

import (
	"errors"
	"fmt"
	"strings"

	"github.com/VictoriaMetrics/metricsql"
)

// MaxExpressionBytes bounds one expression.
//
// It is a limit on what a person types, not on what a query costs: cost is
// bounded by the time range, the step and the series ceiling. Four kilobytes is
// far more than any hand-written expression and small enough that parsing it is
// never the expensive part of a request.
const MaxExpressionBytes = 4096

// ErrUnsupported marks an expression this Server declines to run. It is
// distinct from a syntax error: the expression may parse, but not into
// something the guard is willing to hand to storage.
var ErrUnsupported = errors.New("expression is not supported by the metrics guard")

// SyntaxError is a parse failure. The message is VictoriaMetrics' own, which
// quotes the expression it was given — the author's own text, so returning it
// discloses nothing they did not just type.
type SyntaxError struct {
	Message string
}

func (err *SyntaxError) Error() string {
	return "MetricsQL 解析失败：" + err.Message
}

// unsupported builds an ErrUnsupported carrying a reason an operator can act
// on. The reason is about their own expression, so it is safe to show them.
func unsupported(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, fmt.Sprintf(format, arguments...))
}

// Enforced is a rewritten expression and what the guard noticed about it.
type Enforced struct {
	// Expression is what should be sent to storage. It is VictoriaMetrics'
	// own rendering of the parsed query, so it is normalised rather than
	// byte-identical to what the author typed.
	Expression string
	// LikelyInvalid marks a query VictoriaMetrics' parser recognises as one of
	// the shapes that almost always mean something other than what was
	// written — `rate(sum(x))` and friends, where an instant vector is
	// implicitly converted to a range one. It is a warning and not a refusal:
	// the query runs, and the Console says the answer is probably not the one
	// that was wanted.
	LikelyInvalid bool
	// Selectors counts the series selectors the filter was applied to. Zero
	// means the expression reads no series at all — `time()`, a bare number —
	// which is allowed because there is nothing there to scope.
	Selectors int
}

// Enforce returns the expression with label=value applied to every series
// selector in it.
//
// The caller is responsible for value being something it may disclose to the
// author: it is spliced into the returned expression, which the Console shows
// as the query that actually ran. In this Server that is always the identifier
// of a Cluster the caller has already been authorized to read.
func Enforce(expression, label, value string) (Enforced, error) {
	trimmed := strings.TrimSpace(expression)
	if trimmed == "" {
		return Enforced{}, unsupported("表达式不能为空")
	}
	if len(trimmed) > MaxExpressionBytes {
		return Enforced{}, unsupported("表达式超过 %d 字节上限", MaxExpressionBytes)
	}
	if !validLabelName(label) {
		// A programming error rather than an input one: the label is a
		// constant in this Server, never something a request carries.
		return Enforced{}, errors.New("metrics guard label name is invalid")
	}
	parsed, err := metricsql.Parse(trimmed)
	if err != nil {
		return Enforced{}, &SyntaxError{Message: err.Error()}
	}
	selectors := scope(parsed, label, value)
	rewritten := string(parsed.AppendString(nil))

	// The guard checks its own work by reading the result back the way storage
	// will. Everything above depends on VisitAll having reached every selector
	// and on AppendString having printed what was built; this depends on
	// neither, so a change in either — a new expression type, a printing
	// quirk — turns into a refused query rather than a query that quietly
	// describes the wrong Cluster.
	if err := verify(rewritten, label, value, selectors); err != nil {
		return Enforced{}, err
	}
	return Enforced{
		Expression:    rewritten,
		LikelyInvalid: metricsql.IsLikelyInvalid(parsed),
		Selectors:     selectors,
	}, nil
}

// Validate reports whether an expression can be stored and later run under this
// guard. It rewrites against a placeholder target and throws the result away:
// the point is to refuse a saved query that would only fail the first time
// somebody ran it.
func Validate(expression, label string) error {
	_, err := Enforce(expression, label, placeholderTarget)
	return err
}

// placeholderTarget stands in for a real Cluster when an expression is checked
// without one. The nil UUID is not a Cluster identifier this Server ever
// issues, so a rewrite that escaped into a real query would select nothing.
const placeholderTarget = "00000000-0000-0000-0000-000000000000"

// scope applies the filter to every selector and reports how many there were.
func scope(parsed metricsql.Expr, label, value string) int {
	enforced := metricsql.LabelFilter{Label: label, Value: value}
	selectors := 0
	metricsql.VisitAll(parsed, func(expr metricsql.Expr) {
		selector, ok := expr.(*metricsql.MetricExpr)
		if !ok {
			return
		}
		selectors++
		if len(selector.LabelFilterss) == 0 {
			selector.LabelFilterss = [][]metricsql.LabelFilter{{enforced}}
			return
		}
		// One group per `or` alternative, and each of them is a separate way
		// for a series to be selected — so each of them needs the filter.
		// Adding it once would leave the other alternatives unscoped.
		for index, group := range selector.LabelFilterss {
			rewritten := make([]metricsql.LabelFilter, 0, len(group)+1)
			for _, filter := range group {
				if filter.Label == label {
					continue
				}
				rewritten = append(rewritten, filter)
			}
			// Appended rather than prepended: a filter on `__name__` has to
			// stay first in its group, which is where the parser put it.
			selector.LabelFilterss[index] = append(rewritten, enforced)
		}
	})
	return selectors
}

// verify re-parses the guard's output and insists that every selector in it
// carries exactly one filter on the guard's label, matching the target
// exactly.
func verify(rewritten, label, value string, selectors int) error {
	parsed, err := metricsql.Parse(rewritten)
	if err != nil {
		return unsupported("重写后的表达式无法解析，已拒绝执行")
	}
	found := 0
	scoped := true
	metricsql.VisitAll(parsed, func(expr metricsql.Expr) {
		selector, ok := expr.(*metricsql.MetricExpr)
		if !ok {
			return
		}
		found++
		if len(selector.LabelFilterss) == 0 {
			scoped = false
			return
		}
		for _, group := range selector.LabelFilterss {
			matched := 0
			for _, filter := range group {
				if filter.Label != label {
					continue
				}
				if filter.Value != value || filter.IsRegexp || filter.IsNegative {
					scoped = false
					return
				}
				matched++
			}
			if matched != 1 {
				scoped = false
				return
			}
		}
	})
	if !scoped || found != selectors {
		return unsupported("无法为该表达式注入集群过滤条件，已拒绝执行")
	}
	return nil
}

func validLabelName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		switch {
		case character == '_',
			character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9' && index > 0:
		default:
			return false
		}
	}
	return true
}
