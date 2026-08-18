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
	Query       string
	Title       string
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
	window, err := service.resolveWindow(definition, &input)
	if err != nil {
		return Result{}, err
	}
	scope, err := service.resolveCluster(ctx, input)
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
		Window:    window,
	})

	samples, err := service.execute(ctx, definition, expression, input)
	if err != nil {
		return Result{}, err
	}
	names := map[string]string{scope.ClusterID: scope.ClusterName}
	series, truncated := service.buildSeries(definition, samples, names, input)
	issues, partial := service.collectIssues(input, scope, series, truncated)
	return Result{
		Query:       definition.Name,
		Title:       definition.Title,
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

// resolveWindow validates the time parameters and reports the rate window to
// use. An instant query has no range, so its parameters are ignored rather
// than rejected: the caller asks for a named query, not for a shape.
func (service *Service) resolveWindow(
	definition Definition,
	input *Input,
) (string, error) {
	if definition.Kind == KindInstant {
		if input.End.IsZero() {
			input.End = service.now()
		}
		input.Start = input.End
		input.Step = 0
		return formatWindow(service.config.RateWindow), nil
	}
	if input.End.IsZero() {
		input.End = service.now()
	}
	if input.Start.IsZero() || !input.End.After(input.Start) {
		return "", fmt.Errorf("%w: time range is empty", ErrInvalidInput)
	}
	span := input.End.Sub(input.Start)
	if span > service.config.MaxRange {
		return "", fmt.Errorf(
			"%w: time range exceeds %s",
			ErrInvalidInput,
			service.config.MaxRange,
		)
	}
	if input.Step <= 0 {
		input.Step = service.config.MinStep
	}
	if input.Step < service.config.MinStep {
		return "", fmt.Errorf(
			"%w: step is below %s",
			ErrInvalidInput,
			service.config.MinStep,
		)
	}
	if input.Step%time.Second != 0 {
		return "", fmt.Errorf("%w: step must be whole seconds", ErrInvalidInput)
	}
	if int(span/input.Step)+1 > service.config.MaxPoints {
		return "", fmt.Errorf(
			"%w: range and step would return more than %d points",
			ErrInvalidInput,
			service.config.MaxPoints,
		)
	}
	// A rate window shorter than the step samples less than the interval it
	// reports on; one step is the smallest window that covers the whole
	// interval, and the configured window is the floor.
	window := service.config.RateWindow
	if input.Step > window {
		window = input.Step
	}
	return formatWindow(window), nil
}

func (service *Service) resolveCluster(
	ctx context.Context,
	input Input,
) (store.ClusterScope, error) {
	if !validation.IsUUID(input.ClusterID) {
		return store.ClusterScope{}, fmt.Errorf(
			"%w: a Cluster identifier is required",
			ErrInvalidInput,
		)
	}
	visibility, err := service.authorization.ResolveMetricsVisibility(
		ctx,
		input.UserID,
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
		input.ClusterID,
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
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
			Values [][]json.RawMessage
		} `json:"result"`
	} `json:"data"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
}

func (service *Service) execute(
	ctx context.Context,
	definition Definition,
	expression string,
	input Input,
) (*promResponse, error) {
	values := url.Values{}
	values.Set("query", expression)
	endpoint := service.config.QueryURL + "/api/v1/query"
	if definition.Kind == KindRange {
		endpoint = service.config.QueryURL + "/api/v1/query_range"
		values.Set("start", strconv.FormatInt(input.Start.Unix(), 10))
		values.Set("end", strconv.FormatInt(input.End.Unix(), 10))
		values.Set("step", strconv.FormatInt(int64(input.Step/time.Second), 10))
	} else {
		values.Set("time", strconv.FormatInt(input.End.Unix(), 10))
	}

	queryContext, cancel := context.WithTimeout(ctx, service.config.QueryTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		queryContext,
		http.MethodPost,
		endpoint,
		strings.NewReader(values.Encode()),
	)
	if err != nil {
		return nil, err
	}
	// POST rather than GET: the expression plus a long Cluster matcher can
	// exceed what an intermediary allows in a URL.
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := service.client.Do(request)
	if err != nil {
		if queryContext.Err() != nil && ctx.Err() == nil {
			return nil, ErrTimeout
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		service.logger.Warn(
			"metrics storage query failed",
			slog.String("query", definition.Name),
			slog.String("error", err.Error()),
		)
		return nil, ErrUnavailable
	}
	defer func() {
		_, _ = io.CopyN(io.Discard, response.Body, 4096)
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		service.logger.Warn(
			"metrics storage rejected a query",
			slog.String("query", definition.Name),
			slog.Int("status", response.StatusCode),
		)
		return nil, ErrUnavailable
	}
	decoded := &promResponse{}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
	if err := decoder.Decode(decoded); err != nil {
		service.logger.Warn(
			"metrics storage returned an unreadable response",
			slog.String("query", definition.Name),
			slog.String("error", err.Error()),
		)
		return nil, ErrUnavailable
	}
	if decoded.Status != "success" {
		// The backend's own message is not returned: it can quote the
		// expression, which is Server-internal.
		service.logger.Warn(
			"metrics storage reported a query error",
			slog.String("query", definition.Name),
			slog.String("error_type", decoded.ErrorType),
		)
		return nil, ErrUnavailable
	}
	return decoded, nil
}

func (service *Service) buildSeries(
	definition Definition,
	response *promResponse,
	names map[string]string,
	input Input,
) ([]Series, bool) {
	truncated := false
	results := response.Data.Result
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
