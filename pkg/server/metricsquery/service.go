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
	ErrInvalidInput   = errors.New("invalid metrics query")
	ErrUnknownQuery   = errors.New("unknown metrics query")
	ErrDenied         = errors.New("metrics query is not permitted for the requested Clusters")
	ErrNoVisibility   = errors.New("no Cluster is visible for metrics")
	ErrTooManyTargets = errors.New("metrics query covers too many Clusters")
	ErrUnavailable    = errors.New("metrics storage is unavailable")
	ErrTimeout        = errors.New("metrics query timed out")
)

const (
	DefaultMaxClusters        = 200
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
	ListVisibleClusters(
		context.Context,
		store.ListVisibleClustersParams,
	) ([]store.ClusterScope, error)
}

type Config struct {
	// QueryURL is the storage backend's Prometheus-compatible query base, for
	// example http://victoriametrics:8428/prometheus. Only the Server calls
	// it; the browser reaches it through this service or not at all.
	QueryURL     string
	QueryTimeout time.Duration
	MaxClusters  int
	MaxPoints    int
	MaxSeries    int
	MaxRange     time.Duration
	MinStep      time.Duration
	RateWindow   time.Duration
	HTTPClient   *http.Client
	Now          func() time.Time
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
	if config.MaxClusters <= 0 {
		config.MaxClusters = DefaultMaxClusters
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

// Input is one catalogue query with its parameters. ClusterIDs narrows the
// answer; leaving it empty means "every Cluster this caller may read", which
// is the landing view of a multi-cluster application.
type Input struct {
	UserID     string
	Name       string
	ClusterIDs []string
	Namespace  string
	Start      time.Time
	End        time.Time
	Step       time.Duration
	Top        int
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
	// ClusterIDs are the Clusters the query actually covered, so a caller can
	// tell "no data" from "not in scope".
	ClusterIDs []string
	Truncated  bool
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
	window, err := service.resolveWindow(definition, &input)
	if err != nil {
		return Result{}, err
	}
	scopes, err := service.resolveScope(ctx, input)
	if err != nil {
		return Result{}, err
	}

	clusterIDs := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		clusterIDs = append(clusterIDs, scope.ClusterID)
	}
	// Every identifier is a validated UUID, so the matcher cannot carry
	// anything but identifiers. The filter is built here rather than accepted
	// from the caller, which is what makes the scope boundary structural.
	matcher := fmt.Sprintf(
		`%s=~"%s"`,
		metricsingest.ClusterLabel,
		strings.Join(clusterIDs, "|"),
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
	names := make(map[string]string, len(scopes))
	for _, scope := range scopes {
		names[scope.ClusterID] = scope.ClusterName
	}
	series, truncated := service.buildSeries(definition, samples, names, input)
	return Result{
		Query:       definition.Name,
		Title:       definition.Title,
		Unit:        definition.Unit,
		Kind:        definition.Kind,
		Start:       input.Start,
		End:         input.End,
		StepSeconds: int64(input.Step / time.Second),
		Series:      series,
		ClusterIDs:  clusterIDs,
		Truncated:   truncated,
	}, nil
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

func (service *Service) resolveScope(
	ctx context.Context,
	input Input,
) ([]store.ClusterScope, error) {
	visibility, err := service.authorization.ResolveMetricsVisibility(
		ctx,
		input.UserID,
	)
	if err != nil {
		return nil, err
	}
	if visibility.empty() {
		return nil, ErrNoVisibility
	}
	scopes, err := service.clusters.ListVisibleClusters(
		ctx,
		store.ListVisibleClustersParams{
			Global:     visibility.Global,
			TenantIDs:  visibility.TenantIDs,
			ProjectIDs: visibility.ProjectIDs,
			// One over the limit, so a scope that is too wide is detected
			// rather than quietly cut off at the limit.
			Limit: service.config.MaxClusters + 1,
		},
	)
	if err != nil {
		return nil, err
	}
	if len(input.ClusterIDs) == 0 {
		if len(scopes) == 0 {
			return nil, ErrNoVisibility
		}
		if len(scopes) > service.config.MaxClusters {
			return nil, ErrTooManyTargets
		}
		return scopes, nil
	}
	if len(input.ClusterIDs) > service.config.MaxClusters {
		return nil, ErrTooManyTargets
	}
	allowed := make(map[string]store.ClusterScope, len(scopes))
	for _, scope := range scopes {
		allowed[scope.ClusterID] = scope
	}
	selected := make([]store.ClusterScope, 0, len(input.ClusterIDs))
	seen := make(map[string]struct{}, len(input.ClusterIDs))
	for _, clusterID := range input.ClusterIDs {
		if !validation.IsUUID(clusterID) {
			return nil, fmt.Errorf("%w: Cluster identifier is invalid", ErrInvalidInput)
		}
		if _, duplicate := seen[clusterID]; duplicate {
			continue
		}
		seen[clusterID] = struct{}{}
		scope, permitted := allowed[clusterID]
		if !permitted {
			// Refused rather than dropped: silently narrowing the answer would
			// present a partial view as the whole one.
			return nil, ErrDenied
		}
		selected = append(selected, scope)
	}
	if len(selected) == 0 {
		return nil, ErrDenied
	}
	return selected, nil
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
