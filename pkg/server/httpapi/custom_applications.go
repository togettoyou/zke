package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/customapplications"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
)

const maxCustomApplicationRequestBytes = 80 * 1024

type customApplicationService interface {
	List(context.Context, string) ([]customapplications.Application, error)
	Get(context.Context, string, string) (customapplications.Application, error)
	Create(context.Context, string, string, customapplications.Input) (customapplications.Application, error)
	Update(context.Context, string, string, customapplications.Input) (customapplications.Application, error)
	Delete(context.Context, string, string) (customapplications.Application, error)
}

type customApplicationHandler struct {
	baseHandler
	service customApplicationService
}

func newCustomApplicationHandler(
	logger *slog.Logger,
	service customApplicationService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *customApplicationHandler {
	return &customApplicationHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

type customApplicationRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	LogoURL     string `json:"logo_url"`
}

func (request customApplicationRequest) input(idempotencyKey string) customapplications.Input {
	return customapplications.Input{
		Name: request.Name, Description: request.Description,
		URL: request.URL, LogoURL: request.LogoURL, IdempotencyKey: idempotencyKey,
	}
}

type customApplicationResponse struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	LogoURL     string    `json:"logo_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type customApplicationListResponse struct {
	Applications []customApplicationResponse `json:"applications"`
	Limit        int                         `json:"limit"`
}

func presentCustomApplication(item customapplications.Application) customApplicationResponse {
	return customApplicationResponse{
		ID: item.ID, ProjectID: item.ProjectID, Name: item.Name,
		Description: item.Description, URL: item.URL, LogoURL: item.LogoURL,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func (handler *customApplicationHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "自定义应用列表不接受查询参数")
		return
	}
	ctx, cancel := handler.operationContext(c)
	items, err := handler.service.List(ctx, c.Param("project_id"))
	cancel()
	if handler.respondCustomApplicationError(c, "list custom applications", err) {
		return
	}
	applications := make([]customApplicationResponse, 0, len(items))
	for _, item := range items {
		applications = append(applications, presentCustomApplication(item))
	}
	writeSuccess(c, http.StatusOK, customApplicationListResponse{
		Applications: applications,
		Limit:        customapplications.MaxPerProject,
	})
}

func (handler *customApplicationHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ctx, cancel := handler.operationContext(c)
	item, err := handler.service.Get(ctx, c.Param("project_id"), c.Param("application_id"))
	cancel()
	if handler.respondCustomApplicationError(c, "get custom application", err) {
		return
	}
	writeSuccess(c, http.StatusOK, presentCustomApplication(item))
}

func (handler *customApplicationHandler) create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	var request customApplicationRequest
	if decodeJSONRequest(c, &request, maxCustomApplicationRequestBytes) != nil {
		handler.record(c, actor.User.ID, auditaction.CustomApplicationCreate, "", request, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "自定义应用创建请求无效")
		return
	}
	ctx, cancel := handler.operationContext(c)
	item, err := handler.service.Create(
		ctx, c.Param("project_id"), actor.User.ID,
		request.input(c.GetHeader(idempotencyKeyHeaderName)),
	)
	cancel()
	if err != nil {
		handler.record(c, actor.User.ID, auditaction.CustomApplicationCreate, "", request, "failed")
	}
	if handler.respondCustomApplicationError(c, "create custom application", err) {
		return
	}
	handler.record(c, actor.User.ID, auditaction.CustomApplicationCreate, item.ID, request, "succeeded")
	writeSuccess(c, http.StatusCreated, presentCustomApplication(item))
}

func (handler *customApplicationHandler) update(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	id := c.Param("application_id")
	var request customApplicationRequest
	if decodeJSONRequest(c, &request, maxCustomApplicationRequestBytes) != nil {
		handler.record(c, actor.User.ID, auditaction.CustomApplicationUpdate, id, request, "failed")
		writeError(c, http.StatusBadRequest, "invalid_request", "自定义应用更新请求无效")
		return
	}
	ctx, cancel := handler.operationContext(c)
	item, err := handler.service.Update(ctx, c.Param("project_id"), id, request.input(""))
	cancel()
	if err != nil {
		handler.record(c, actor.User.ID, auditaction.CustomApplicationUpdate, id, request, "failed")
	}
	if handler.respondCustomApplicationError(c, "update custom application", err) {
		return
	}
	handler.record(c, actor.User.ID, auditaction.CustomApplicationUpdate, item.ID, request, "succeeded")
	writeSuccess(c, http.StatusOK, presentCustomApplication(item))
}

func (handler *customApplicationHandler) remove(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actor, _ := httpmiddleware.Identity(c)
	id := c.Param("application_id")
	ctx, cancel := handler.operationContext(c)
	item, err := handler.service.Delete(ctx, c.Param("project_id"), id)
	cancel()
	if err != nil {
		handler.record(c, actor.User.ID, auditaction.CustomApplicationDelete, id, customApplicationRequest{}, "failed")
	}
	if handler.respondCustomApplicationError(c, "delete custom application", err) {
		return
	}
	handler.record(c, actor.User.ID, auditaction.CustomApplicationDelete, item.ID,
		customApplicationRequest{Name: item.Name}, "succeeded")
	c.Status(http.StatusNoContent)
}

func (handler *customApplicationHandler) record(
	c *gin.Context,
	actorID string,
	action string,
	targetID string,
	request customApplicationRequest,
	result string,
) {
	detail := map[string]string{}
	if origin := applicationOrigin(request.URL); origin != "" {
		detail["origin"] = origin
	}
	handler.recordOperation(c, auditedOperation{
		Scope: auditScopeProject, ActorUserID: actorID, Action: action,
		TargetType: auditaction.TargetCustomApplication, TargetID: targetID,
		TargetName: request.Name, Result: result, Detail: detail,
	})
}

// Only the origin belongs in audit detail. Query strings and paths routinely
// carry dashboard variables or one-time tokens and are not needed to identify
// which external system was configured.
func applicationOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func (handler *customApplicationHandler) respondCustomApplicationError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	return handler.respondError(c, operation, err,
		errorMapping{customapplications.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "自定义应用请求无效"},
		errorMapping{customapplications.ErrNotFound, http.StatusNotFound, "not_found", "该自定义应用不存在"},
		errorMapping{customapplications.ErrConflict, http.StatusConflict, "conflict", "同名的自定义应用已存在"},
		errorMapping{customapplications.ErrLimit, http.StatusConflict, "limit_reached", "该项目的自定义应用数量已达上限"},
		errorMapping{customapplications.ErrIdempotencyConflict, http.StatusConflict, "idempotency_conflict", "Idempotency-Key 已用于不同的自定义应用"},
	)
}
