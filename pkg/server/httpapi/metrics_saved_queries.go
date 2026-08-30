package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/metricslibrary"
)

// The Project's saved MetricsQL expressions.
//
// One permission opens the routes — `cluster.metrics.read`, the same one that
// opens a chart — and a second decides who may write the shared list:
// `cluster.metrics.manage`. That split is enforced inside the service rather
// than by a second piece of middleware, because it depends on the row: an
// operator with only the read permission keeps their own private entries, and
// the same request shape either does or does not need curation depending on the
// visibility it carries.
//
// Reads are not audited, for the same reason opening a chart is not: an
// expression names series, not data, and every one of them is confined to a
// Cluster the reader may already read. Writes are, because sharing one changes
// what everybody in the Project sees.

const maxSavedMetricsQueryRequestBytes = 32 * 1024

type metricsSavedQueryService interface {
	List(ctx context.Context, projectID, userID string) ([]metricslibrary.SavedQuery, error)
	Create(ctx context.Context, projectID, userID string, input metricslibrary.Input) (
		metricslibrary.SavedQuery, error,
	)
	Update(ctx context.Context, projectID, userID, id string, input metricslibrary.Input) (
		metricslibrary.SavedQuery, error,
	)
	Delete(ctx context.Context, projectID, userID, id string) (
		metricslibrary.SavedQuery, error,
	)
}

// metricsSavedQueryServiceOrNil keeps a nil *metricslibrary.Service from
// becoming a non-nil interface holding a nil pointer, which would pass the
// handler's readiness check and then panic on the first call.
func metricsSavedQueryServiceOrNil(
	service *metricslibrary.Service,
) metricsSavedQueryService {
	if service == nil {
		return nil
	}
	return service
}

type metricsSavedQueryHandler struct {
	baseHandler
	service metricsSavedQueryService
}

func newMetricsSavedQueryHandler(
	logger *slog.Logger,
	service metricsSavedQueryService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *metricsSavedQueryHandler {
	return &metricsSavedQueryHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

type metricsSavedQueryRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Expression  string `json:"expression"`
	Visibility  string `json:"visibility"`
}

func (request metricsSavedQueryRequest) input() metricslibrary.Input {
	return metricslibrary.Input{
		Name:        request.Name,
		Description: request.Description,
		Expression:  request.Expression,
		Visibility:  request.Visibility,
	}
}

type metricsSavedQueryResponse struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	OwnerUserID string `json:"owner_user_id"`
	// OwnerDisplayName is empty when the author's account has been deleted, and
	// the Console says so rather than showing a blank byline.
	OwnerDisplayName string `json:"owner_display_name"`
	Visibility       string `json:"visibility"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Expression       string `json:"expression"`
	// Editable is what the reader may do with this entry. The Console uses it
	// to hide an action rather than to decide one: every write path checks the
	// same rules again.
	Editable  bool      `json:"editable"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type metricsSavedQueryListResponse struct {
	Queries []metricsSavedQueryResponse `json:"queries"`
	// Limit is how many entries this Project may hold in total, so a Console
	// can say how much room is left before a save is refused.
	Limit int `json:"limit"`
}

func presentSavedMetricsQuery(item metricslibrary.SavedQuery) metricsSavedQueryResponse {
	return metricsSavedQueryResponse{
		ID:               item.ID,
		ProjectID:        item.ProjectID,
		OwnerUserID:      item.OwnerUserID,
		OwnerDisplayName: item.OwnerDisplayName,
		Visibility:       item.Visibility,
		Name:             item.Name,
		Description:      item.Description,
		Expression:       item.Expression,
		Editable:         item.Editable,
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
	}
}

func (handler *metricsSavedQueryHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !handler.ready(c) {
		return
	}
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request",
			"saved metrics query list does not accept query parameters")
		return
	}
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := handler.operationContext(c)
	items, err := handler.service.List(ctx, c.Param("project_id"), identity.User.ID)
	cancel()
	if handler.respondSavedQueryError(c, "list saved metrics queries", err) {
		return
	}
	queries := make([]metricsSavedQueryResponse, 0, len(items))
	for _, item := range items {
		queries = append(queries, presentSavedMetricsQuery(item))
	}
	writeSuccess(c, http.StatusOK, metricsSavedQueryListResponse{
		Queries: queries,
		Limit:   metricslibrary.MaxPerProject,
	})
}

func (handler *metricsSavedQueryHandler) create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	var request metricsSavedQueryRequest
	if decodeJSONRequest(c, &request, maxSavedMetricsQueryRequestBytes) != nil {
		handler.record(c, actor.User.ID, auditaction.MetricsSavedQueryCreate, "", request, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "查询保存请求无效")
		return
	}
	if !handler.ready(c) {
		handler.record(c, actor.User.ID, auditaction.MetricsSavedQueryCreate, "", request, "failed")
		return
	}
	ctx, cancel := handler.operationContext(c)
	item, err := handler.service.Create(
		ctx,
		c.Param("project_id"),
		actor.User.ID,
		request.input(),
	)
	cancel()
	if err != nil {
		handler.record(c, actor.User.ID, auditaction.MetricsSavedQueryCreate, "", request, "failed")
	}
	if handler.respondSavedQueryError(c, "create saved metrics query", err) {
		return
	}
	handler.record(c, actor.User.ID, auditaction.MetricsSavedQueryCreate, item.ID, request, "succeeded")
	writeSuccess(c, http.StatusCreated, presentSavedMetricsQuery(item))
}

func (handler *metricsSavedQueryHandler) update(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	id := c.Param("saved_query_id")
	var request metricsSavedQueryRequest
	if decodeJSONRequest(c, &request, maxSavedMetricsQueryRequestBytes) != nil {
		handler.record(c, actor.User.ID, auditaction.MetricsSavedQueryUpdate, id, request, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "查询保存请求无效")
		return
	}
	if !handler.ready(c) {
		handler.record(c, actor.User.ID, auditaction.MetricsSavedQueryUpdate, id, request, "failed")
		return
	}
	ctx, cancel := handler.operationContext(c)
	item, err := handler.service.Update(
		ctx,
		c.Param("project_id"),
		actor.User.ID,
		id,
		request.input(),
	)
	cancel()
	if err != nil {
		handler.record(c, actor.User.ID, auditaction.MetricsSavedQueryUpdate, id, request, "failed")
	}
	if handler.respondSavedQueryError(c, "update saved metrics query", err) {
		return
	}
	handler.record(c, actor.User.ID, auditaction.MetricsSavedQueryUpdate, item.ID, request, "succeeded")
	writeSuccess(c, http.StatusOK, presentSavedMetricsQuery(item))
}

func (handler *metricsSavedQueryHandler) remove(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	id := c.Param("saved_query_id")
	if !handler.ready(c) {
		handler.record(c, actor.User.ID, auditaction.MetricsSavedQueryDelete, id,
			metricsSavedQueryRequest{}, "failed")
		return
	}
	ctx, cancel := handler.operationContext(c)
	item, err := handler.service.Delete(ctx, c.Param("project_id"), actor.User.ID, id)
	cancel()
	if err != nil {
		handler.record(c, actor.User.ID, auditaction.MetricsSavedQueryDelete, id,
			metricsSavedQueryRequest{}, "failed")
	}
	if handler.respondSavedQueryError(c, "delete saved metrics query", err) {
		return
	}
	handler.record(
		c,
		actor.User.ID,
		auditaction.MetricsSavedQueryDelete,
		item.ID,
		metricsSavedQueryRequest{Name: item.Name, Visibility: item.Visibility},
		"succeeded",
	)
	c.Status(http.StatusNoContent)
}

func (handler *metricsSavedQueryHandler) ready(c *gin.Context) bool {
	if handler.service != nil {
		return true
	}
	writeError(
		c,
		http.StatusServiceUnavailable,
		"metrics_disabled",
		"metrics collection is not enabled on this Server",
	)
	return false
}

// record writes one library change to the Project's audit trail.
//
// The entry's name and visibility are recorded; the expression is not. What an
// auditor needs from this trail is that somebody changed what the Project
// shares and which entry it was — the text itself is in the row, readable by
// anyone who may read the library, and copying it into every audit event would
// bloat the trail without answering a question anyone asks of it.
func (handler *metricsSavedQueryHandler) record(
	c *gin.Context,
	actorID string,
	action string,
	targetID string,
	request metricsSavedQueryRequest,
	result string,
) {
	detail := map[string]string{}
	if request.Visibility != "" {
		detail["visibility"] = request.Visibility
	}
	handler.recordOperation(c, auditedOperation{
		Scope:       auditScopeProject,
		ActorUserID: actorID,
		Action:      action,
		TargetType:  auditaction.TargetMetricsSavedQuery,
		TargetID:    targetID,
		TargetName:  request.Name,
		Result:      result,
		Detail:      detail,
	})
}

func (handler *metricsSavedQueryHandler) respondSavedQueryError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	return handler.respondError(c, operation, err,
		errorMapping{
			metricslibrary.ErrInvalidInput,
			http.StatusBadRequest,
			"invalid_request",
			"查询保存请求无效",
		},
		errorMapping{
			metricslibrary.ErrNotFound,
			http.StatusNotFound,
			"not_found",
			"该保存的查询不存在",
		},
		errorMapping{
			metricslibrary.ErrConflict,
			http.StatusConflict,
			"conflict",
			"同名的查询已存在",
		},
		errorMapping{
			metricslibrary.ErrDenied,
			http.StatusForbidden,
			"forbidden",
			"没有权限修改该查询",
		},
		errorMapping{
			metricslibrary.ErrLimit,
			http.StatusConflict,
			"limit_reached",
			"该项目保存的查询数量已达上限",
		},
	)
}
