package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/metricsquery"
)

const (
	maxQueryClusterIDs = 200
	maxStepSeconds     = 24 * 60 * 60
)

type metricsQueryService interface {
	Catalog() []metricsquery.Definition
	Query(context.Context, metricsquery.Input) (metricsquery.Result, error)
}

type observabilityMetricsHandler struct {
	baseHandler
	service metricsQueryService
}

func newObservabilityMetricsHandler(
	logger *slog.Logger,
	service metricsQueryService,
	operationTimeout time.Duration,
) *observabilityMetricsHandler {
	return &observabilityMetricsHandler{
		baseHandler: newBaseHandler(logger, nil, operationTimeout),
		service:     service,
	}
}

func metricsQueryServiceOrNil(service *metricsquery.Service) metricsQueryService {
	if service == nil {
		return nil
	}
	return service
}

type metricsQueryDefinitionResponse struct {
	Name              string   `json:"name"`
	Title             string   `json:"title"`
	Kind              string   `json:"kind"`
	Unit              string   `json:"unit"`
	Dimensions        []string `json:"dimensions"`
	RequiresNamespace bool     `json:"requires_namespace"`
	SupportsTop       bool     `json:"supports_top"`
}

type metricsQueryCatalogResponse struct {
	Queries []metricsQueryDefinitionResponse `json:"queries"`
}

type metricsQuerySeriesResponse struct {
	ClusterID   string            `json:"cluster_id"`
	ClusterName string            `json:"cluster_name"`
	Labels      map[string]string `json:"labels"`
	// Points are [unix_seconds, value] pairs where value is null for a step
	// the storage had no sample for. Clients must draw the hole rather than
	// bridge it: a gap is a collection outage, not a flat line.
	Points [][2]any `json:"points"`
}

type metricsQueryResponse struct {
	Query       string                       `json:"query"`
	Title       string                       `json:"title"`
	Kind        string                       `json:"kind"`
	Unit        string                       `json:"unit"`
	Start       time.Time                    `json:"start"`
	End         time.Time                    `json:"end"`
	StepSeconds int64                        `json:"step_seconds"`
	ClusterIDs  []string                     `json:"cluster_ids"`
	Series      []metricsQuerySeriesResponse `json:"series"`
	Truncated   bool                         `json:"truncated"`
}

func (handler *observabilityMetricsHandler) catalog(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if handler.service == nil {
		writeError(
			c,
			http.StatusServiceUnavailable,
			"metrics_disabled",
			"metrics collection is not enabled on this Server",
		)
		return
	}
	definitions := handler.service.Catalog()
	queries := make([]metricsQueryDefinitionResponse, 0, len(definitions))
	for _, definition := range definitions {
		dimensions := definition.Dimensions
		if dimensions == nil {
			dimensions = []string{}
		}
		queries = append(queries, metricsQueryDefinitionResponse{
			Name:              definition.Name,
			Title:             definition.Title,
			Kind:              string(definition.Kind),
			Unit:              string(definition.Unit),
			Dimensions:        dimensions,
			RequiresNamespace: definition.RequiresNamespace,
			SupportsTop:       definition.SupportsTop,
		})
	}
	writeSuccess(c, http.StatusOK, metricsQueryCatalogResponse{Queries: queries})
}

func (handler *observabilityMetricsHandler) query(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if handler.service == nil {
		writeError(
			c,
			http.StatusServiceUnavailable,
			"metrics_disabled",
			"metrics collection is not enabled on this Server",
		)
		return
	}
	input, ok := parseMetricsQuery(c)
	if !ok {
		return
	}
	identity, _ := httpmiddleware.Identity(c)
	input.UserID = identity.User.ID

	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.Query(ctx, input)
	cancel()
	if handler.respondError(
		c,
		"query metrics",
		err,
		errorMapping{
			target:  metricsquery.ErrUnknownQuery,
			status:  http.StatusNotFound,
			code:    "unknown_query",
			message: "该指标查询不存在",
		},
		errorMapping{
			target:  metricsquery.ErrInvalidInput,
			status:  http.StatusBadRequest,
			code:    "invalid_request",
			message: "指标查询参数无效",
		},
		errorMapping{
			target:  metricsquery.ErrDenied,
			status:  http.StatusForbidden,
			code:    "forbidden",
			message: "请求的集群超出当前权限范围",
		},
		errorMapping{
			// Not an error state: the caller simply has no Cluster with the
			// metrics permission, which the Console shows as an empty scope
			// rather than as a failure.
			target:  metricsquery.ErrNoVisibility,
			status:  http.StatusForbidden,
			code:    "no_visible_cluster",
			message: "当前权限范围内没有可读取指标的集群",
		},
		errorMapping{
			target:  metricsquery.ErrTooManyTargets,
			status:  http.StatusBadRequest,
			code:    "scope_too_wide",
			message: "查询覆盖的集群过多，请缩小范围",
		},
		errorMapping{
			target:  metricsquery.ErrUnavailable,
			status:  http.StatusServiceUnavailable,
			code:    "storage_unavailable",
			message: "指标存储当前不可用",
		},
		errorMapping{
			target:  metricsquery.ErrTimeout,
			status:  http.StatusGatewayTimeout,
			code:    "timeout",
			message: "指标查询超时",
		},
	) {
		return
	}

	series := make([]metricsQuerySeriesResponse, 0, len(result.Series))
	for _, item := range result.Series {
		points := make([][2]any, 0, len(item.Points))
		for _, point := range item.Points {
			if point.Value == nil {
				points = append(points, [2]any{point.UnixSeconds, nil})
				continue
			}
			points = append(points, [2]any{point.UnixSeconds, *point.Value})
		}
		labels := item.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		series = append(series, metricsQuerySeriesResponse{
			ClusterID:   item.ClusterID,
			ClusterName: item.ClusterName,
			Labels:      labels,
			Points:      points,
		})
	}
	writeSuccess(c, http.StatusOK, metricsQueryResponse{
		Query:       result.Query,
		Title:       result.Title,
		Kind:        string(result.Kind),
		Unit:        string(result.Unit),
		Start:       result.Start,
		End:         result.End,
		StepSeconds: result.StepSeconds,
		ClusterIDs:  result.ClusterIDs,
		Series:      series,
		Truncated:   result.Truncated,
	})
}

// parseMetricsQuery validates transport-level shape only. Whether the caller
// may read the named Clusters is decided by the service, which is the single
// place that resolves scope.
func parseMetricsQuery(c *gin.Context) (metricsquery.Input, bool) {
	input := metricsquery.Input{
		Name:      strings.TrimSpace(c.Query("name")),
		Namespace: strings.TrimSpace(c.Query("namespace")),
	}
	if input.Name == "" {
		writeError(c, http.StatusBadRequest, "invalid_request", "name is required")
		return metricsquery.Input{}, false
	}
	if raw := strings.TrimSpace(c.Query("cluster_ids")); raw != "" {
		for _, clusterID := range strings.Split(raw, ",") {
			clusterID = strings.TrimSpace(clusterID)
			if clusterID == "" {
				continue
			}
			input.ClusterIDs = append(input.ClusterIDs, clusterID)
		}
		if len(input.ClusterIDs) > maxQueryClusterIDs {
			writeError(
				c,
				http.StatusBadRequest,
				"invalid_request",
				"too many cluster identifiers",
			)
			return metricsquery.Input{}, false
		}
	}
	start, ok := parseQueryTime(c, "start")
	if !ok {
		return metricsquery.Input{}, false
	}
	end, ok := parseQueryTime(c, "end")
	if !ok {
		return metricsquery.Input{}, false
	}
	input.Start = start
	input.End = end
	if raw := strings.TrimSpace(c.Query("step_seconds")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 || seconds > maxStepSeconds {
			writeError(c, http.StatusBadRequest, "invalid_request", "step_seconds is invalid")
			return metricsquery.Input{}, false
		}
		input.Step = time.Duration(seconds) * time.Second
	}
	if raw := strings.TrimSpace(c.Query("top")); raw != "" {
		top, err := strconv.Atoi(raw)
		if err != nil || top <= 0 {
			writeError(c, http.StatusBadRequest, "invalid_request", "top is invalid")
			return metricsquery.Input{}, false
		}
		input.Top = top
	}
	return input, true
}

func parseQueryTime(c *gin.Context, name string) (time.Time, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return time.Time{}, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeError(
			c,
			http.StatusBadRequest,
			"invalid_request",
			name+" must be an RFC 3339 timestamp",
		)
		return time.Time{}, false
	}
	return parsed.UTC(), true
}
