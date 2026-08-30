package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/metricsquery"
)

// Explore: the route that accepts an expression instead of a query name.
//
// It is a POST because an expression is a body and not an identifier — several
// of them, each long enough to outgrow what an intermediary will carry in a
// URL — and because a body is where a CSRF token is already required. It is
// deliberately not cacheable and not idempotent-looking: nothing about it
// should end up in a proxy log or a browser history entry.
//
// The response mirrors the request one entry at a time. A typo in the second
// expression must not blank the first one's chart, so an expression that failed
// carries its own error and the others carry their answers.

const maxExploreRequestBytes = 64 * 1024

type metricsExploreQueryRequest struct {
	// RefID is the caller's own label for this row, echoed back so an answer
	// can be matched to the row that asked for it without relying on order.
	RefID      string `json:"ref_id"`
	Expression string `json:"expression"`
}

type metricsExploreRequest struct {
	ClusterID   string                       `json:"cluster_id"`
	Kind        string                       `json:"kind"`
	Start       string                       `json:"start"`
	End         string                       `json:"end"`
	StepSeconds int                          `json:"step_seconds"`
	Queries     []metricsExploreQueryRequest `json:"queries"`
}

type metricsExploreErrorResponse struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type metricsExploreQueryResponse struct {
	RefID      string `json:"ref_id"`
	Expression string `json:"expression"`
	// EffectiveExpression is what actually ran. Returned because this is the
	// one place the Server rewrites somebody's own query, and a rewrite the
	// author cannot read is a rewrite they cannot check. It names only the
	// Cluster they were just authorized to read.
	EffectiveExpression string                       `json:"effective_expression"`
	ResultType          string                       `json:"result_type"`
	Series              []metricsQuerySeriesResponse `json:"series"`
	Truncated           bool                         `json:"truncated"`
	DurationMs          int64                        `json:"duration_ms"`
	Warning             string                       `json:"warning"`
	Error               *metricsExploreErrorResponse `json:"error"`
}

type metricsExploreResponse struct {
	ClusterID   string                        `json:"cluster_id"`
	ClusterName string                        `json:"cluster_name"`
	Kind        string                        `json:"kind"`
	Start       time.Time                     `json:"start"`
	End         time.Time                     `json:"end"`
	StepSeconds int64                         `json:"step_seconds"`
	Queries     []metricsExploreQueryResponse `json:"queries"`
	Issues      []metricsQueryIssueResponse   `json:"issues"`
}

func (handler *observabilityMetricsHandler) explore(c *gin.Context) {
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
	var request metricsExploreRequest
	if decodeJSONRequest(c, &request, maxExploreRequestBytes) != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "指标查询参数无效")
		return
	}
	input, ok := parseMetricsExplore(c, request)
	if !ok {
		return
	}
	identity, _ := httpmiddleware.Identity(c)
	input.UserID = identity.User.ID

	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.Explore(ctx, input)
	cancel()
	if handler.respondError(
		c,
		"explore metrics",
		err,
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
			target:  metricsquery.ErrNoVisibility,
			status:  http.StatusForbidden,
			code:    "no_visible_cluster",
			message: "当前权限范围内没有可读取指标的集群",
		},
		errorMapping{
			// A queue depth, not a quota: the caller should wait, not change
			// what they asked for.
			target:  metricsquery.ErrBusy,
			status:  http.StatusTooManyRequests,
			code:    "too_many_requests",
			message: "并发的自定义查询过多，请稍后重试",
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

	queries := make([]metricsExploreQueryResponse, 0, len(result.Queries))
	for _, outcome := range result.Queries {
		item := metricsExploreQueryResponse{
			RefID:               outcome.RefID,
			Expression:          outcome.Expression,
			EffectiveExpression: outcome.EffectiveExpression,
			ResultType:          outcome.ResultType,
			Series:              metricsSeriesResponse(outcome.Series),
			Truncated:           outcome.Truncated,
			DurationMs:          outcome.Duration.Milliseconds(),
			Warning:             outcome.Warning,
		}
		if outcome.Error != nil {
			item.Error = &metricsExploreErrorResponse{
				Code:   outcome.Error.Code,
				Detail: outcome.Error.Detail,
			}
		}
		queries = append(queries, item)
	}
	writeSuccess(c, http.StatusOK, metricsExploreResponse{
		ClusterID:   result.ClusterID,
		ClusterName: result.ClusterName,
		Kind:        string(result.Kind),
		Start:       result.Start,
		End:         result.End,
		StepSeconds: result.StepSeconds,
		Queries:     queries,
		Issues:      metricsIssuesResponse(result.Issues),
	})
}

// parseMetricsExplore validates transport-level shape only. Whether the caller
// may read the named Cluster, and whether an expression can be scoped to it,
// are both decided by the service.
func parseMetricsExplore(
	c *gin.Context,
	request metricsExploreRequest,
) (metricsquery.ExploreInput, bool) {
	input := metricsquery.ExploreInput{
		ClusterID: strings.TrimSpace(request.ClusterID),
		Kind:      metricsquery.Kind(strings.TrimSpace(request.Kind)),
	}
	if input.ClusterID == "" {
		writeError(c, http.StatusBadRequest, "invalid_request", "cluster_id is required")
		return metricsquery.ExploreInput{}, false
	}
	if input.Kind == "" {
		input.Kind = metricsquery.KindRange
	}
	if input.Kind != metricsquery.KindRange && input.Kind != metricsquery.KindInstant {
		writeError(c, http.StatusBadRequest, "invalid_request", "kind is invalid")
		return metricsquery.ExploreInput{}, false
	}
	start, ok := parseExploreTime(c, "start", request.Start)
	if !ok {
		return metricsquery.ExploreInput{}, false
	}
	end, ok := parseExploreTime(c, "end", request.End)
	if !ok {
		return metricsquery.ExploreInput{}, false
	}
	input.Start = start
	input.End = end
	if request.StepSeconds != 0 {
		if request.StepSeconds < 0 || request.StepSeconds > maxStepSeconds {
			writeError(c, http.StatusBadRequest, "invalid_request", "step_seconds is invalid")
			return metricsquery.ExploreInput{}, false
		}
		input.Step = time.Duration(request.StepSeconds) * time.Second
	}
	if len(request.Queries) == 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "至少需要一条表达式")
		return metricsquery.ExploreInput{}, false
	}
	if len(request.Queries) > metricsquery.MaxExploreQueries {
		writeError(
			c,
			http.StatusBadRequest,
			"invalid_request",
			"一次最多执行 "+strconv.Itoa(metricsquery.MaxExploreQueries)+" 条表达式",
		)
		return metricsquery.ExploreInput{}, false
	}
	seen := make(map[string]struct{}, len(request.Queries))
	for _, query := range request.Queries {
		reference := strings.TrimSpace(query.RefID)
		// A reference is how an answer finds its row. Two rows sharing one
		// would make the pairing ambiguous, and the Console would draw an
		// answer under the wrong expression.
		if reference == "" || len(reference) > 16 {
			writeError(c, http.StatusBadRequest, "invalid_request", "ref_id is invalid")
			return metricsquery.ExploreInput{}, false
		}
		if _, duplicate := seen[reference]; duplicate {
			writeError(c, http.StatusBadRequest, "invalid_request", "ref_id must be unique")
			return metricsquery.ExploreInput{}, false
		}
		seen[reference] = struct{}{}
		input.Queries = append(input.Queries, metricsquery.ExploreQuery{
			RefID:      reference,
			Expression: query.Expression,
		})
	}
	return input, true
}

func parseExploreTime(c *gin.Context, name, raw string) (time.Time, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, true
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
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
