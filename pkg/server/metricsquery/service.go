package metricsquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/metricsingest"
	"github.com/togettoyou/zke/pkg/server/metricsqlguard"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/validation"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

var (
	ErrInvalidInput = errors.New("invalid metrics query")
	ErrUnknownQuery = errors.New("unknown metrics query")
	ErrDenied       = errors.New("metrics query is not permitted for the requested Cluster")
	ErrNoVisibility = errors.New("no Cluster is visible for metrics")
	ErrUnavailable  = errors.New("metrics storage is unavailable")
	ErrTimeout      = errors.New("metrics query timed out")
)

const (
	DefaultMaxPoints          = 1500
	DefaultMaxSeries          = 500
	DefaultMaxRange           = 7 * 24 * time.Hour
	DefaultMinStep            = 15 * time.Second
	DefaultQueryTimeout       = 30 * time.Second
	DefaultRateWindow         = 5 * time.Minute
	maxResponseBytes    int64 = 32 * 1024 * 1024
	maxTopN                   = 50
)

// Visibility is the resolved scope a caller may query. It is this package's
// own type rather than the RBAC one so the query path can be exercised without
// a database, and so the only authorization input stays visible in one place.
type Visibility struct {
	Global     bool
	TenantIDs  []string
	ProjectIDs []string
}

func (visibility Visibility) empty() bool {
	return !visibility.Global &&
		len(visibility.TenantIDs) == 0 &&
		len(visibility.ProjectIDs) == 0
}

type VisibilityResolver interface {
	ResolveMetricsVisibility(context.Context, string) (Visibility, error)
}

// RBACVisibility adapts the RBAC service. The permission is fixed here rather
// than passed in: a second caller asking for metrics scope under a different
// permission would be a different boundary, and it should have to say so in
// code rather than in an argument.
type RBACVisibility struct {
	Service *rbac.Service
}

func (adapter RBACVisibility) ResolveMetricsVisibility(
	ctx context.Context,
	userID string,
) (Visibility, error) {
	resolved, err := adapter.Service.ResolveVisibility(
		ctx,
		userID,
		rbac.PermissionClusterMetricsRead,
	)
	if err != nil {
		return Visibility{}, err
	}
	return Visibility{
		Global:     resolved.IsGlobal(),
		TenantIDs:  resolved.TenantIDs(),
		ProjectIDs: resolved.ProjectIDs(),
	}, nil
}

type ClusterScopeStore interface {
	GetVisibleCluster(
		context.Context,
		store.VisibleClusterParams,
		string,
	) (store.ClusterScope, error)
}

type Config struct {
	// QueryURL is the storage backend's Prometheus-compatible query base, for
	// example http://victoriametrics:8428/prometheus. Only the Server calls
	// it; the browser reaches it through this service or not at all.
	QueryURL     string
	QueryTimeout time.Duration
	MaxPoints    int
	MaxSeries    int
	MaxRange     time.Duration
	MinStep      time.Duration
	RateWindow   time.Duration
	HTTPClient   *http.Client
	Now          func() time.Time
	// Budget is the ingest gateway's view of each Cluster. It may be nil, in
	// which case a query never reports throttling — the read path must keep
	// working when the write path is not part of this process.
	Budget IngestBudget
}

// IngestBudget reports what the ingest gateway knows about a Cluster. The read
// path consults it so a hole in a chart can say why it is there: samples the
// Server refused never reached storage, and without this the answer is
// indistinguishable from a Cluster that was simply idle.
type IngestBudget interface {
	ClusterState(clusterID string) (metricsingest.ClusterState, bool)
}

type Service struct {
	config        Config
	authorization VisibilityResolver
	clusters      ClusterScopeStore
	client        *http.Client
	logger        *slog.Logger
	now           func() time.Time
	// explore bounds how many ad-hoc queries are running. Catalogue queries
	// are not counted: their cost is known from their template, and the panels
	// that issue them are already bounded by how many fit on a screen.
	explore *admission
}

func NewService(
	config Config,
	authorization VisibilityResolver,
	clusters ClusterScopeStore,
	logger *slog.Logger,
) (*Service, error) {
	if authorization == nil || clusters == nil || logger == nil {
		return nil, errors.New("metrics query dependencies are required")
	}
	parsed, err := url.Parse(strings.TrimSpace(config.QueryURL))
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New(
			"metrics storage query URL must be an absolute http or https URL",
		)
	}
	config.QueryURL = strings.TrimRight(parsed.String(), "/")
	if config.QueryTimeout <= 0 {
		config.QueryTimeout = DefaultQueryTimeout
	}
	if config.MaxPoints <= 0 {
		config.MaxPoints = DefaultMaxPoints
	}
	if config.MaxSeries <= 0 {
		config.MaxSeries = DefaultMaxSeries
	}
	if config.MaxRange <= 0 {
		config.MaxRange = DefaultMaxRange
	}
	if config.MinStep <= 0 {
		config.MinStep = DefaultMinStep
	}
	if config.RateWindow <= 0 {
		config.RateWindow = DefaultRateWindow
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: config.QueryTimeout}
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		config:        config,
		authorization: authorization,
		clusters:      clusters,
		client:        client,
		logger:        logger,
		now:           now,
		explore:       newAdmission(maxExploreInFlight, maxExploreInFlightPerUser),
	}, nil
}

// Input is one catalogue query with its parameters.
//
// ClusterID is required and names exactly one Cluster. Charts are read one
// Cluster at a time for the same reason the container service is operated one
// Cluster at a time: a curve that adds two Clusters together is a number that
// exists nowhere, and two Clusters drawn side by side on shared axes are two
// questions in one picture. Naming the target also makes the scope filter an
// equality on a single identifier rather than an alternation over a set.
type Input struct {
	UserID    string
	Name      string
	ClusterID string
	Namespace string
	Start     time.Time
	End       time.Time
	Step      time.Duration
	Top       int
}

type Point struct {
	// UnixSeconds is the grid position. Value is nil where the storage had no
	// sample: a gap is information, and filling it with the previous value
	// would present a collection outage as a flat line.
	UnixSeconds int64
	Value       *float64
}

type Series struct {
	ClusterID   string
	ClusterName string
	Labels      map[string]string
	Points      []Point
}

type Result struct {
	Query string
	Title string
	// Expression is the portable form shown in the Console. It omits the
	// mandatory Cluster label; the scoped expression is still the only one sent
	// to storage.
	Expression  string
	Unit        Unit
	Kind        Kind
	Start       time.Time
	End         time.Time
	StepSeconds int64
	Series      []Series
	// ClusterID and ClusterName are the Cluster the answer describes, echoed
	// back so a caller can tell an empty chart apart from a chart it is reading
	// under the wrong heading.
	ClusterID   string
	ClusterName string
	Truncated   bool
	// Partial marks an answer that does not describe the whole requested scope.
	// Issues says why. A reader who cannot tell a real trough from a refused
	// batch will draw the wrong conclusion from the same picture.
	Partial bool
	Issues  []Issue
}

// Issue reasons. They are codes rather than sentences because each one leads
// the operator somewhere different, and the Console owns the words.
const (
	// IssueNoData marks a target Cluster that returned nothing. It is not a
	// failure — collection may simply not be installed there — so it does not
	// make an answer partial.
	IssueNoData = "no_data"
	// IssueThrottled marks a Cluster whose batches this Server is refusing.
	// Whatever gap the chart shows for it is the Server's doing.
	IssueThrottled = "throttled"
	// IssueSeriesTruncated marks an answer cut down to the series ceiling.
	IssueSeriesTruncated = "series_truncated"
)

type Issue struct {
	// ClusterID is empty for an issue that belongs to the answer as a whole
	// rather than to the target Cluster.
	ClusterID   string
	ClusterName string
	Reason      string
	// Detail narrows the reason — for throttling, which budget was exceeded.
	// It never carries payload, label values or backend messages.
	Detail string
}

func (service *Service) Catalog() []Definition {
	return Catalog()
}

func (service *Service) Query(ctx context.Context, input Input) (Result, error) {
	definition, found := lookup(input.Name)
	if !found {
		return Result{}, ErrUnknownQuery
	}
	if !validation.IsUUID(input.UserID) {
		return Result{}, ErrInvalidInput
	}
	if input.Namespace != "" &&
		len(k8svalidation.IsDNS1123Label(input.Namespace)) != 0 {
		return Result{}, fmt.Errorf("%w: namespace is invalid", ErrInvalidInput)
	}
	if input.Top < 0 || input.Top > maxTopN {
		return Result{}, fmt.Errorf("%w: top is out of range", ErrInvalidInput)
	}
	if input.Top > 0 && !definition.SupportsTop {
		return Result{}, fmt.Errorf("%w: query does not support top", ErrInvalidInput)
	}
	if input.Top <= 0 && definition.RequiresTop {
		return Result{}, fmt.Errorf("%w: query requires top", ErrInvalidInput)
	}
	if input.Namespace != "" && !definition.SupportsNamespace {
		// Refused rather than ignored: a caller that believes it narrowed the
		// answer would read a Cluster-wide number as a Namespace one.
		return Result{}, fmt.Errorf("%w: query does not support namespace", ErrInvalidInput)
	}
	if input.Namespace == "" && definition.RequiresNamespace {
		return Result{}, fmt.Errorf("%w: query requires namespace", ErrInvalidInput)
	}
	resolved, err := service.resolveWindow(
		definition.Kind,
		input.Start,
		input.End,
		input.Step,
	)
	if err != nil {
		return Result{}, err
	}
	input.Start, input.End, input.Step = resolved.Start, resolved.End, resolved.Step
	scope, err := service.resolveCluster(ctx, input.UserID, input.ClusterID)
	if err != nil {
		return Result{}, err
	}

	// An equality rather than a regular expression: the target is one validated
	// UUID, so there is nothing to alternate over, and an exact match is the
	// cheapest lookup the storage has — it resolves through the label index
	// instead of being evaluated against candidate values.
	//
	// The filter is built here rather than accepted from the caller, which is
	// what makes the scope boundary structural. The identifier reaching this
	// line has already been rejected unless it is a UUID and unless the caller
	// may read it, so it cannot carry a quote or a metacharacter.
	matcher := fmt.Sprintf(
		`%s=%q`,
		metricsingest.ClusterLabel,
		scope.ClusterID,
	)
	expression := definition.build(matcher, buildParams{
		Namespace: input.Namespace,
		Top:       input.Top,
		Window:    resolved.Rate,
	})
	displayExpression, err := metricsqlguard.WithoutLabel(
		expression,
		metricsingest.ClusterLabel,
	)
	if err != nil {
		return Result{}, fmt.Errorf("build display expression for %q: %w", definition.Name, err)
	}

	samples, _, err := service.execute(ctx, storageRequest{
		Name:       definition.Name,
		Expression: expression,
		Kind:       definition.Kind,
		ClusterID:  scope.ClusterID,
		Window:     resolved,
	})
	if err != nil {
		var rejected *QueryRejected
		if errors.As(err, &rejected) {
			// A catalogue query the storage refuses is a defect in this
			// Server, not in the request: the expression came from a template
			// nobody outside this process can influence. The caller is told
			// the storage is unavailable, and the reason is in the log.
			service.logger.Warn(
				"metrics storage rejected a catalogue query",
				slog.String("query", definition.Name),
				slog.String("error_type", rejected.Type),
			)
			return Result{}, ErrUnavailable
		}
		return Result{}, err
	}
	names := map[string]string{scope.ClusterID: scope.ClusterName}
	series, truncated := service.buildSeries(definition, samples, names, input)
	issues, partial := service.collectIssues(input, scope, series, truncated)
	return Result{
		Query:       definition.Name,
		Title:       definition.Title,
		Expression:  displayExpression,
		Unit:        definition.Unit,
		Kind:        definition.Kind,
		Start:       input.Start,
		End:         input.End,
		StepSeconds: int64(input.Step / time.Second),
		Series:      series,
		ClusterID:   scope.ClusterID,
		ClusterName: scope.ClusterName,
		Truncated:   truncated,
		Partial:     partial,
		Issues:      issues,
	}, nil
}

// collectIssues explains the difference between what was asked for and what
// came back.
func (service *Service) collectIssues(
	input Input,
	scope store.ClusterScope,
	series []Series,
	truncated bool,
) ([]Issue, bool) {
	issues := make([]Issue, 0, 2)
	partial := false

	// Throttling first: a Cluster the Server is refusing has no data for a
	// reason the operator can act on, and reporting it as "no data" would send
	// them to restart a collector that is doing its job.
	throttled := false
	if service.config.Budget != nil {
		if budget, known := service.config.Budget.ClusterState(
			scope.ClusterID,
		); known && budget.Throttled {
			issues = append(issues, Issue{
				ClusterID:   scope.ClusterID,
				ClusterName: scope.ClusterName,
				Reason:      IssueThrottled,
				Detail:      budget.Reason,
			})
			partial = true
			throttled = true
		}
	}
	if !throttled && len(series) == 0 {
		issues = append(issues, Issue{
			ClusterID:   scope.ClusterID,
			ClusterName: scope.ClusterName,
			Reason:      IssueNoData,
		})
	}
	if truncated {
		issues = append(issues, Issue{Reason: IssueSeriesTruncated})
		partial = true
	}
	return issues, partial
}

// window is a validated time range plus the rate window a query over it should
// use. It is the only place the range rules live, so an ad-hoc expression and a
// catalogue query are bounded by exactly the same arithmetic.
type window struct {
	Start time.Time
	End   time.Time
	Step  time.Duration
	// Rate is the lookbehind a rate() over this window needs, formatted the way
	// an expression writes it. It is only meaningful to the catalogue, whose
	// templates ask for it; an ad-hoc expression carries its own.
	Rate string
}

// resolveWindow validates the time parameters and reports the window to ask
// for. An instant query has no range, so its parameters are ignored rather
// than rejected: the caller asks for a point in time, not for a shape.
func (service *Service) resolveWindow(
	kind Kind,
	start time.Time,
	end time.Time,
	step time.Duration,
) (window, error) {
	if end.IsZero() {
		end = service.now()
	}
	if kind == KindInstant {
		return window{
			Start: end,
			End:   end,
			Rate:  formatWindow(service.config.RateWindow),
		}, nil
	}
	if start.IsZero() || !end.After(start) {
		return window{}, fmt.Errorf("%w: time range is empty", ErrInvalidInput)
	}
	span := end.Sub(start)
	if span > service.config.MaxRange {
		return window{}, fmt.Errorf(
			"%w: time range exceeds %s",
			ErrInvalidInput,
			service.config.MaxRange,
		)
	}
	if step <= 0 {
		step = service.config.MinStep
	}
	if step < service.config.MinStep {
		return window{}, fmt.Errorf(
			"%w: step is below %s",
			ErrInvalidInput,
			service.config.MinStep,
		)
	}
	if step%time.Second != 0 {
		return window{}, fmt.Errorf("%w: step must be whole seconds", ErrInvalidInput)
	}
	// Storage answers a range query on its own grid — multiples of the step
	// counted from the Unix epoch — and moves an unaligned start onto it as
	// soon as a request is long enough to be worth caching, which is most of
	// them. alignToGrid rebuilds the grid from the start that was asked for, so
	// a start off that grid lines up with nothing storage returned and every
	// point became a hole: an empty chart for exactly the window that was full
	// when the same bounds were typed by hand.
	//
	// Snapping here rather than filling the grid from the returned timestamps
	// keeps the answer the shape the caller asked for — one point per step,
	// gaps included — and keeps it identical whether or not storage decided the
	// request was cacheable.
	//
	// Down, never up: a window that grew forwards would report samples from
	// after the end the caller named.
	if aligned := alignDown(start.Unix(), int64(step/time.Second)); aligned != start.Unix() {
		start = time.Unix(aligned, 0).UTC()
		span = end.Sub(start)
	}
	if int(span/step)+1 > service.config.MaxPoints {
		return window{}, fmt.Errorf(
			"%w: range and step would return more than %d points",
			ErrInvalidInput,
			service.config.MaxPoints,
		)
	}
	// A rate window shorter than the step samples less than the interval it
	// reports on; one step is the smallest window that covers the whole
	// interval, and the configured window is the floor.
	rate := service.config.RateWindow
	if step > rate {
		rate = step
	}
	return window{Start: start, End: end, Step: step, Rate: formatWindow(rate)}, nil
}

func (service *Service) resolveCluster(
	ctx context.Context,
	userID string,
	clusterID string,
) (store.ClusterScope, error) {
	if !validation.IsUUID(clusterID) {
		return store.ClusterScope{}, fmt.Errorf(
			"%w: a Cluster identifier is required",
			ErrInvalidInput,
		)
	}
	visibility, err := service.authorization.ResolveMetricsVisibility(
		ctx,
		userID,
	)
	if err != nil {
		return store.ClusterScope{}, err
	}
	if visibility.empty() {
		return store.ClusterScope{}, ErrNoVisibility
	}
	scope, err := service.clusters.GetVisibleCluster(
		ctx,
		store.VisibleClusterParams{
			Global:     visibility.Global,
			TenantIDs:  visibility.TenantIDs,
			ProjectIDs: visibility.ProjectIDs,
		},
		clusterID,
	)
	if errors.Is(err, store.ErrClusterNotVisible) {
		// One answer for "no such Cluster" and "not yours". Separating them
		// would turn this route into a way to discover Cluster identifiers
		// outside the caller's scope.
		return store.ClusterScope{}, ErrDenied
	}
	if err != nil {
		return store.ClusterScope{}, err
	}
	return scope, nil
}

type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		// Result is left undecoded because its shape depends on resultType: a
		// matrix and a vector are lists of labelled series, while a scalar and
		// a string are one bare `[timestamp, "value"]` pair. Decoding it into
		// the series shape unconditionally would turn `time()` — a perfectly
		// good expression for Explore — into an unreadable response.
		Result json.RawMessage `json:"result"`
	} `json:"data"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
}

type promSeries struct {
	Metric map[string]string   `json:"metric"`
	Value  []json.RawMessage   `json:"value"`
	Values [][]json.RawMessage `json:"values"`
}

// labelledSeries decodes the matrix and vector shapes. Anything else — a
// scalar, a string — has no series in it and returns none, which is the honest
// answer rather than an error: the query succeeded, it just did not describe
// any series.
func (response *promResponse) labelledSeries() []promSeries {
	if response.Data.ResultType != "matrix" && response.Data.ResultType != "vector" {
		return nil
	}
	var decoded []promSeries
	if err := json.Unmarshal(response.Data.Result, &decoded); err != nil {
		return nil
	}
	return decoded
}

// scalarPoint decodes the one-value shapes into a single point, so a scalar
// answer can be drawn and tabulated like any other.
func (response *promResponse) scalarPoint() ([]Point, bool) {
	if response.Data.ResultType != "scalar" && response.Data.ResultType != "string" {
		return nil, false
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(response.Data.Result, &raw); err != nil {
		return nil, false
	}
	point, ok := decodePoint(raw)
	if !ok {
		return nil, false
	}
	return []Point{point}, true
}

// QueryRejected is an answer the storage refused on the query's merits: a
// parse failure, a limit the expression walked into, a cardinality ceiling.
//
// It is distinct from ErrUnavailable, which means the storage could not be
// reached or did not answer. The difference is the whole point for Explore: one
// of them is something the author can fix by editing what they wrote, and the
// other is not about them at all.
type QueryRejected struct {
	Type    string
	Message string
}

func (err *QueryRejected) Error() string {
	if err.Message == "" {
		return "metrics storage rejected the query"
	}
	return "metrics storage rejected the query: " + err.Message
}

// maxRejectionMessageBytes bounds what is carried back from the storage. Its
// message quotes the expression, which for Explore is the author's own text —
// but it is still a string from another process, and a chart panel is not the
// place for a kilobyte of it.
const maxRejectionMessageBytes = 512

// storageRequest is one call to the storage backend.
type storageRequest struct {
	// Name identifies the request in the log. It is a catalogue query name or
	// the Explore reference, never the expression itself.
	Name       string
	Expression string
	Kind       Kind
	// ClusterID is the scope the storage is told to apply on its own, in
	// addition to the filter already present in Expression.
	ClusterID string
	Window    window
}

func (service *Service) execute(
	ctx context.Context,
	request storageRequest,
) (*promResponse, time.Duration, error) {
	values := url.Values{}
	values.Set("query", request.Expression)
	// The same scope filter again, this time applied by the storage to every
	// selector it parses.
	//
	// For a catalogue query this is redundant: the filter is part of the
	// template. For Explore it is the second of the two independent barriers
	// between a caller and another Cluster's samples — the guard rewrites the
	// expression, and this makes the storage narrow whatever it actually
	// parsed. A selector the guard somehow failed to reach still matches
	// nothing here, and the cost is one form field.
	if request.ClusterID != "" {
		values.Set(
			"extra_label",
			metricsingest.ClusterLabel+"="+request.ClusterID,
		)
	}
	endpoint := service.config.QueryURL + "/api/v1/query"
	if request.Kind == KindRange {
		endpoint = service.config.QueryURL + "/api/v1/query_range"
		values.Set("start", strconv.FormatInt(request.Window.Start.Unix(), 10))
		values.Set("end", strconv.FormatInt(request.Window.End.Unix(), 10))
		values.Set("step", strconv.FormatInt(int64(request.Window.Step/time.Second), 10))
	} else {
		values.Set("time", strconv.FormatInt(request.Window.End.Unix(), 10))
	}

	queryContext, cancel := context.WithTimeout(ctx, service.config.QueryTimeout)
	defer cancel()
	started := time.Now()
	httpRequest, err := http.NewRequestWithContext(
		queryContext,
		http.MethodPost,
		endpoint,
		strings.NewReader(values.Encode()),
	)
	if err != nil {
		return nil, 0, err
	}
	// POST rather than GET: the expression plus a long Cluster matcher can
	// exceed what an intermediary allows in a URL.
	httpRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpRequest.Header.Set("Accept", "application/json")
	response, err := service.client.Do(httpRequest)
	if err != nil {
		elapsed := time.Since(started)
		if queryContext.Err() != nil && ctx.Err() == nil {
			return nil, elapsed, ErrTimeout
		}
		if ctx.Err() != nil {
			return nil, elapsed, ctx.Err()
		}
		service.logger.Warn(
			"metrics storage query failed",
			slog.String("query", request.Name),
			slog.String("error", err.Error()),
		)
		return nil, elapsed, ErrUnavailable
	}
	defer func() {
		_, _ = io.CopyN(io.Discard, response.Body, 4096)
		_ = response.Body.Close()
	}()
	decoded := &promResponse{}
	decodeErr := json.NewDecoder(
		io.LimitReader(response.Body, maxResponseBytes),
	).Decode(decoded)
	elapsed := time.Since(started)

	if response.StatusCode != http.StatusOK {
		// A rejected query answers with a status the storage chose and a body
		// explaining why. Reading that body is what lets Explore say "this
		// expression asks for too many series" instead of "storage is
		// unavailable", which would send the operator to look at a healthy
		// process.
		if decodeErr == nil && decoded.Status == "error" {
			return nil, elapsed, rejection(decoded)
		}
		service.logger.Warn(
			"metrics storage rejected a query",
			slog.String("query", request.Name),
			slog.Int("status", response.StatusCode),
		)
		return nil, elapsed, ErrUnavailable
	}
	if decodeErr != nil {
		service.logger.Warn(
			"metrics storage returned an unreadable response",
			slog.String("query", request.Name),
			slog.String("error", decodeErr.Error()),
		)
		return nil, elapsed, ErrUnavailable
	}
	if decoded.Status != "success" {
		return nil, elapsed, rejection(decoded)
	}
	return decoded, elapsed, nil
}

// rejection turns the storage's error body into an error this Server is willing
// to repeat. Control characters go, because the message ends up in a log line
// and in a panel, and the length is capped.
func rejection(decoded *promResponse) *QueryRejected {
	message := strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' {
			return ' '
		}
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, decoded.Error)
	message = strings.TrimSpace(message)
	if len(message) > maxRejectionMessageBytes {
		message = message[:maxRejectionMessageBytes] + "…"
	}
	return &QueryRejected{Type: decoded.ErrorType, Message: message}
}

func (service *Service) buildSeries(
	definition Definition,
	response *promResponse,
	names map[string]string,
	input Input,
) ([]Series, bool) {
	truncated := false
	results := response.labelledSeries()
	if len(results) > service.config.MaxSeries {
		results = results[:service.config.MaxSeries]
		truncated = true
	}
	series := make([]Series, 0, len(results))
	for _, result := range results {
		clusterID := result.Metric[metricsingest.ClusterLabel]
		labels := make(map[string]string, len(definition.Dimensions))
		for _, dimension := range definition.Dimensions {
			if value, ok := result.Metric[dimension]; ok {
				labels[dimension] = value
			}
		}
		points := samplePoints(result.Value, result.Values)
		if definition.Kind == KindRange {
			points = alignToGrid(points, input.Start, input.End, input.Step)
		}
		series = append(series, Series{
			ClusterID:   clusterID,
			ClusterName: names[clusterID],
			Labels:      labels,
			Points:      points,
		})
	}
	slices.SortFunc(series, func(first, second Series) int {
		if first.ClusterName != second.ClusterName {
			return strings.Compare(first.ClusterName, second.ClusterName)
		}
		return strings.Compare(first.ClusterID, second.ClusterID)
	})
	return series, truncated
}

func samplePoints(instant []json.RawMessage, ranged [][]json.RawMessage) []Point {
	if len(instant) == 2 {
		if point, ok := decodePoint(instant); ok {
			return []Point{point}
		}
		return nil
	}
	points := make([]Point, 0, len(ranged))
	for _, raw := range ranged {
		if point, ok := decodePoint(raw); ok {
			points = append(points, point)
		}
	}
	return points
}

func decodePoint(raw []json.RawMessage) (Point, bool) {
	if len(raw) != 2 {
		return Point{}, false
	}
	var timestamp float64
	if err := json.Unmarshal(raw[0], &timestamp); err != nil {
		return Point{}, false
	}
	var encoded string
	if err := json.Unmarshal(raw[1], &encoded); err != nil {
		return Point{}, false
	}
	value, err := strconv.ParseFloat(encoded, 64)
	if err != nil {
		// NaN and Inf arrive as strings Prometheus can print but a JSON
		// number cannot carry. They mean "no usable value", which is the same
		// thing a gap means to a reader.
		return Point{UnixSeconds: int64(timestamp)}, true
	}
	return Point{UnixSeconds: int64(timestamp), Value: &value}, true
}

// alignDown places a Unix second on the largest multiple of step at or below
// it, which is the grid storage answers a range query on.
func alignDown(seconds, step int64) int64 {
	offset := seconds % step
	if offset < 0 {
		offset += step
	}
	return seconds - offset
}

// alignToGrid places samples on the requested step grid and leaves an explicit
// hole wherever storage had nothing. Clients can then draw a break instead of
// connecting across an outage.
func alignToGrid(
	points []Point,
	start time.Time,
	end time.Time,
	step time.Duration,
) []Point {
	if step <= 0 {
		return points
	}
	byTimestamp := make(map[int64]*float64, len(points))
	for _, point := range points {
		byTimestamp[point.UnixSeconds] = point.Value
	}
	stepSeconds := int64(step / time.Second)
	grid := make([]Point, 0, (end.Unix()-start.Unix())/stepSeconds+1)
	for timestamp := start.Unix(); timestamp <= end.Unix(); timestamp += stepSeconds {
		grid = append(grid, Point{
			UnixSeconds: timestamp,
			Value:       byTimestamp[timestamp],
		})
	}
	return grid
}

func formatWindow(window time.Duration) string {
	seconds := int64(window / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return strconv.FormatInt(seconds, 10) + "s"
}
