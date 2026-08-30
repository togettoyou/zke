package metricsquery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/togettoyou/zke/pkg/server/metricsingest"
	"github.com/togettoyou/zke/pkg/server/metricsqlguard"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

// Explore runs expressions an operator wrote, against one Cluster.
//
// The catalogue exists because a fixed template's scope filter cannot be
// escaped and its cost is known before it runs. Explore gives that up on
// purpose — an operator debugging an incident needs to ask a question nobody
// wrote a panel for — and buys both properties back a different way:
//
//   - Scope. Every selector is rewritten to name the target Cluster before the
//     expression leaves this process, and the storage is told the same filter
//     again through `extra_label`. Neither depends on what the author wrote;
//     a `zke_cluster_id` they typed themselves is replaced, not intersected.
//   - Cost. The window, the step and the point count are bounded by exactly
//     the arithmetic the catalogue is bounded by; the series ceiling is the
//     same; the number of expressions in one request is capped; and a caller
//     may only have a few of these in flight at once.
//
// What it does not do is authorize anything new. The target Cluster is resolved
// through the same `cluster.metrics.read` visibility as every chart, so an
// expression is a way of asking a different question about data the caller
// could already read — never a way of reaching data they could not.

const (
	// MaxExploreQueries bounds one request.
	//
	// Explore draws several expressions on shared axes, which is what it is
	// for; past a handful the chart stops being readable before the cost
	// becomes the problem. Five is the point where both are still true.
	MaxExploreQueries = 5
	// exploreConcurrency bounds how many of one request's expressions reach
	// storage at the same time. They are independent queries, so running them
	// in sequence would make a five-expression request five times as slow for
	// no reason; running all of them at once turns one click into a burst.
	exploreConcurrency = 3
	// maxExploreInFlightPerUser bounds how many Explore requests one caller can
	// have running. An expression's cost cannot be predicted from its text, so
	// the protection storage gets is a limit on how many unpredictable things
	// one person can start — a held Execute key, or a Console that retries.
	maxExploreInFlightPerUser = 2
	// maxExploreInFlight bounds the Server as a whole, so a Project full of
	// operators cannot collectively do what one of them is stopped from doing.
	maxExploreInFlight = 16
)

// ErrBusy marks a request refused because too many ad-hoc queries are already
// running. It is a temporary condition and says so: the caller should try
// again, not change what they asked for.
var ErrBusy = errors.New("too many ad-hoc metrics queries are already running")

// Explore error codes. They are codes rather than sentences for the same reason
// the issue reasons are: each leads the operator somewhere different, and the
// Console owns the words. `invalid_expression` and `rejected` carry a detail
// that is safe to show — the first is this Server describing the author's own
// text, the second is the storage quoting it back.
const (
	ExploreErrorInvalidExpression = "invalid_expression"
	ExploreErrorRejected          = "rejected"
	ExploreErrorUnavailable       = "storage_unavailable"
	ExploreErrorTimeout           = "timeout"
)

// ExploreWarningLikelyInvalid marks an expression whose shape almost always
// means something other than what was written — an instant vector implicitly
// converted to a range one, as in `rate(sum(x))`. VictoriaMetrics recognises
// these; the query still runs, and the Console says the answer is probably not
// the one that was wanted.
const ExploreWarningLikelyInvalid = "likely_invalid"

// ExploreQuery is one expression with the reference the caller knows it by.
//
// RefID is the caller's own label — `A`, `B` — echoed back so an answer can be
// matched to the row that asked for it without depending on ordering.
type ExploreQuery struct {
	RefID      string
	Expression string
}

type ExploreInput struct {
	UserID    string
	ClusterID string
	Kind      Kind
	Start     time.Time
	End       time.Time
	Step      time.Duration
	Queries   []ExploreQuery
}

// ExploreError is why one expression produced no answer. The other expressions
// in the same request are unaffected: a typo in the second row should not blank
// the first row's chart.
type ExploreError struct {
	Code   string
	Detail string
}

type ExploreOutcome struct {
	RefID string
	// Expression is what the author wrote. EffectiveExpression is what ran,
	// with the Cluster filter in it — returned because Explore is the one place
	// the Server rewrites somebody's own query, and a rewrite the author cannot
	// see is a rewrite they cannot trust. It names only the Cluster they were
	// just authorized for.
	Expression          string
	EffectiveExpression string
	ResultType          string
	Series              []Series
	Truncated           bool
	Duration            time.Duration
	Warning             string
	Error               *ExploreError
}

type ExploreResult struct {
	ClusterID   string
	ClusterName string
	Kind        Kind
	Start       time.Time
	End         time.Time
	StepSeconds int64
	Queries     []ExploreOutcome
	// Issues describes the Cluster rather than any one expression — that the
	// Server is refusing its batches, most importantly, since every hole in
	// every chart below would then be the Server's doing.
	Issues []Issue
}

// admission counts what is running, globally and per caller.
type admission struct {
	mutex    sync.Mutex
	total    int
	perUser  map[string]int
	maxTotal int
	maxUser  int
}

func newAdmission(maxTotal, maxUser int) *admission {
	return &admission{
		perUser:  make(map[string]int),
		maxTotal: maxTotal,
		maxUser:  maxUser,
	}
}

func (limiter *admission) enter(userID string) bool {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	if limiter.total >= limiter.maxTotal || limiter.perUser[userID] >= limiter.maxUser {
		return false
	}
	limiter.total++
	limiter.perUser[userID]++
	return true
}

func (limiter *admission) leave(userID string) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	limiter.total--
	limiter.perUser[userID]--
	if limiter.perUser[userID] <= 0 {
		// Deleted rather than left at zero: the map would otherwise grow by one
		// entry per user who ever opened Explore and never shrink.
		delete(limiter.perUser, userID)
	}
}

// Explore validates, scopes and runs one request's expressions.
//
// A failure that belongs to the request — no target, a window that is too wide,
// too many expressions — is returned as an error and nothing runs. A failure
// that belongs to one expression is returned inside that expression's outcome,
// because the others are still worth drawing.
func (service *Service) Explore(
	ctx context.Context,
	input ExploreInput,
) (ExploreResult, error) {
	if !validation.IsUUID(input.UserID) {
		return ExploreResult{}, ErrInvalidInput
	}
	if len(input.Queries) == 0 {
		return ExploreResult{}, fmt.Errorf(
			"%w: at least one expression is required",
			ErrInvalidInput,
		)
	}
	if len(input.Queries) > MaxExploreQueries {
		return ExploreResult{}, fmt.Errorf(
			"%w: at most %d expressions may be run together",
			ErrInvalidInput,
			MaxExploreQueries,
		)
	}
	if input.Kind != KindRange && input.Kind != KindInstant {
		return ExploreResult{}, fmt.Errorf("%w: unknown query kind", ErrInvalidInput)
	}
	resolved, err := service.resolveWindow(input.Kind, input.Start, input.End, input.Step)
	if err != nil {
		return ExploreResult{}, err
	}
	// Scope before admission: a request that was never going to be allowed
	// should not occupy one of the slots that protects storage.
	scope, err := service.resolveCluster(ctx, input.UserID, input.ClusterID)
	if err != nil {
		return ExploreResult{}, err
	}
	if !service.explore.enter(input.UserID) {
		return ExploreResult{}, ErrBusy
	}
	defer service.explore.leave(input.UserID)

	outcomes := make([]ExploreOutcome, len(input.Queries))
	var waiting sync.WaitGroup
	slots := make(chan struct{}, exploreConcurrency)
	for index, query := range input.Queries {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			outcomes[index] = service.exploreOne(ctx, query, scope.ClusterID, input.Kind, resolved)
		}()
	}
	waiting.Wait()

	return ExploreResult{
		ClusterID:   scope.ClusterID,
		ClusterName: scope.ClusterName,
		Kind:        input.Kind,
		Start:       resolved.Start,
		End:         resolved.End,
		StepSeconds: int64(resolved.Step / time.Second),
		Queries:     outcomes,
		Issues:      service.clusterIssues(scope.ClusterID, scope.ClusterName),
	}, nil
}

// exploreOne scopes and runs a single expression. It never returns an error:
// everything that can go wrong with one expression belongs in its own outcome.
func (service *Service) exploreOne(
	ctx context.Context,
	query ExploreQuery,
	clusterID string,
	kind Kind,
	resolved window,
) ExploreOutcome {
	outcome := ExploreOutcome{RefID: query.RefID, Expression: query.Expression}

	enforced, err := metricsqlguard.Enforce(
		query.Expression,
		metricsingest.ClusterLabel,
		clusterID,
	)
	if err != nil {
		// The detail describes the author's own text back to them, which is
		// the only way an expression editor is usable at all.
		outcome.Error = &ExploreError{
			Code:   ExploreErrorInvalidExpression,
			Detail: guardDetail(err),
		}
		return outcome
	}
	outcome.EffectiveExpression = enforced.Expression
	if enforced.LikelyInvalid {
		outcome.Warning = ExploreWarningLikelyInvalid
	}

	samples, elapsed, err := service.execute(ctx, storageRequest{
		Name:       "explore/" + query.RefID,
		Expression: enforced.Expression,
		Kind:       kind,
		ClusterID:  clusterID,
		Window:     resolved,
	})
	outcome.Duration = elapsed
	if err != nil {
		outcome.Error = exploreError(err)
		return outcome
	}
	outcome.ResultType = samples.Data.ResultType
	outcome.Series, outcome.Truncated = service.exploreSeries(samples, kind, resolved)
	return outcome
}

// guardDetail renders a guard refusal for the operator. A syntax error carries
// VictoriaMetrics' own message, which quotes what was typed; an unsupported
// expression carries this Server's reason. Both are about the author's text.
func guardDetail(err error) string {
	var syntax *metricsqlguard.SyntaxError
	if errors.As(err, &syntax) {
		return syntax.Error()
	}
	if errors.Is(err, metricsqlguard.ErrUnsupported) {
		if _, reason, found := strings.Cut(err.Error(), ": "); found {
			return reason
		}
	}
	return "表达式无法执行"
}

func exploreError(err error) *ExploreError {
	var rejected *QueryRejected
	switch {
	case errors.As(err, &rejected):
		return &ExploreError{Code: ExploreErrorRejected, Detail: rejected.Message}
	case errors.Is(err, ErrTimeout):
		return &ExploreError{Code: ExploreErrorTimeout}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &ExploreError{Code: ExploreErrorTimeout}
	default:
		return &ExploreError{Code: ExploreErrorUnavailable}
	}
}

// exploreSeries turns one storage answer into series.
//
// Unlike a catalogue query it keeps every label the storage returned rather
// than projecting the ones a panel declared: the author chose what to group by,
// and dropping labels would leave two rows in the table looking identical.
func (service *Service) exploreSeries(
	response *promResponse,
	kind Kind,
	resolved window,
) ([]Series, bool) {
	if points, ok := response.scalarPoint(); ok {
		if kind == KindRange {
			points = alignToGrid(points, resolved.Start, resolved.End, resolved.Step)
		}
		return []Series{{Labels: map[string]string{}, Points: points}}, false
	}
	results := response.labelledSeries()
	truncated := false
	if len(results) > service.config.MaxSeries {
		results = results[:service.config.MaxSeries]
		truncated = true
	}
	series := make([]Series, 0, len(results))
	for _, result := range results {
		labels := make(map[string]string, len(result.Metric))
		for name, value := range result.Metric {
			labels[name] = value
		}
		points := samplePoints(result.Value, result.Values)
		if kind == KindRange {
			points = alignToGrid(points, resolved.Start, resolved.End, resolved.Step)
		}
		series = append(series, Series{
			ClusterID: labels[metricsingest.ClusterLabel],
			Labels:    labels,
			Points:    points,
		})
	}
	// Ordered by their labels so the legend, the table and the colours stay put
	// between two runs of the same query. Storage returns whatever order its
	// index produced, which is stable in practice and not promised.
	sort.SliceStable(series, func(first, second int) bool {
		return seriesKey(series[first]) < seriesKey(series[second])
	})
	if truncated {
		service.logger.Debug(
			"ad-hoc metrics query was truncated",
			slog.Int("limit", service.config.MaxSeries),
		)
	}
	return series, truncated
}

func seriesKey(series Series) string {
	names := make([]string, 0, len(series.Labels))
	for name := range series.Labels {
		names = append(names, name)
	}
	sort.Strings(names)
	var builder strings.Builder
	for _, name := range names {
		builder.WriteString(name)
		builder.WriteByte('=')
		builder.WriteString(series.Labels[name])
		builder.WriteByte(',')
	}
	return builder.String()
}

// clusterIssues reports what the Server knows about the target Cluster
// independently of any expression. Only throttling qualifies: "no data" is a
// property of one query here, not of the Cluster, because an expression that
// returns nothing is usually an expression that asked for nothing.
func (service *Service) clusterIssues(clusterID, clusterName string) []Issue {
	issues := make([]Issue, 0, 1)
	if service.config.Budget == nil {
		return issues
	}
	budget, known := service.config.Budget.ClusterState(clusterID)
	if !known || !budget.Throttled {
		return issues
	}
	return append(issues, Issue{
		ClusterID:   clusterID,
		ClusterName: clusterName,
		Reason:      IssueThrottled,
		Detail:      budget.Reason,
	})
}
