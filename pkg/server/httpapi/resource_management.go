package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/resourcemanagement"
)

const maxCreateResourceRequestBytes = 16 * 1024

type resourceManagementHandler struct {
	logger           *slog.Logger
	service          *resourcemanagement.Service
	auditService     *audit.Service
	operationTimeout time.Duration
}

type createResourceRequest struct {
	Name string `json:"name"`
}

type tenantResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Replayed  bool      `json:"replayed,omitempty"`
}

type projectResponse struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Replayed  bool      `json:"replayed,omitempty"`
}

type clusterResponse struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	ProjectID  string     `json:"project_id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func newResourceManagementHandler(
	logger *slog.Logger,
	service *resourcemanagement.Service,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *resourceManagementHandler {
	return &resourceManagementHandler{
		logger:           logger,
		service:          service,
		auditService:     auditService,
		operationTimeout: operationTimeout,
	}
}

func (handler *resourceManagementHandler) listTenants(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListTenants(ctx, identity.User.ID)
	cancel()
	switch {
	case errors.Is(err, resourcemanagement.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid tenant query")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	case err != nil:
		handler.internalError(c, "list visible tenants", err)
	default:
		response := make([]tenantResponse, 0, len(result))
		for _, item := range result {
			response = append(response, responseTenant(item, false))
		}
		c.JSON(http.StatusOK, gin.H{"tenants": response})
	}
}

func (handler *resourceManagementHandler) createTenant(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request createResourceRequest
	if err := decodeJSONRequest(c, &request, maxCreateResourceRequestBytes); err != nil {
		handler.recordTenantFailure(c, identity.User.ID)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid tenant request")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateTenant(
		ctx,
		resourcemanagement.CreateTenantInput{
			Name:           request.Name,
			ActorUserID:    identity.User.ID,
			RequestID:      httpmiddleware.RequestID(c),
			IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
			Now:            time.Now().UTC(),
		},
	)
	cancel()
	switch {
	case errors.Is(err, resourcemanagement.ErrInvalidInput):
		handler.recordTenantFailure(c, identity.User.ID)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid tenant request")
	case errors.Is(err, resourcemanagement.ErrIdempotencyConflict):
		writeError(c, http.StatusConflict, "idempotency_conflict", "idempotency key was already used")
	case errors.Is(err, resourcemanagement.ErrDenied):
		handler.recordTenantFailure(c, identity.User.ID)
		writeError(c, http.StatusForbidden, "forbidden", "permission denied")
	case errors.Is(err, context.DeadlineExceeded):
		handler.recordTenantFailure(c, identity.User.ID)
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	case err != nil:
		handler.recordTenantFailure(c, identity.User.ID)
		handler.internalError(c, "create tenant", err)
	default:
		status := http.StatusCreated
		if result.Replayed {
			status = http.StatusOK
		}
		c.JSON(status, responseTenant(result.Tenant, result.Replayed))
	}
}

func (handler *resourceManagementHandler) listProjects(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListProjects(
		ctx,
		identity.User.ID,
		c.Param("tenant_id"),
	)
	cancel()
	switch {
	case errors.Is(err, resourcemanagement.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid tenant")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	case err != nil:
		handler.internalError(c, "list visible projects", err)
	default:
		response := make([]projectResponse, 0, len(result))
		for _, item := range result {
			response = append(response, responseProject(item, false))
		}
		c.JSON(http.StatusOK, gin.H{"projects": response})
	}
}

func (handler *resourceManagementHandler) createProject(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request createResourceRequest
	if err := decodeJSONRequest(c, &request, maxCreateResourceRequestBytes); err != nil {
		handler.recordProjectFailure(c, identity.User.ID)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid project request")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateProject(
		ctx,
		resourcemanagement.CreateProjectInput{
			TenantID:       c.Param("tenant_id"),
			Name:           request.Name,
			ActorUserID:    identity.User.ID,
			RequestID:      httpmiddleware.RequestID(c),
			IdempotencyKey: c.GetHeader(idempotencyKeyHeaderName),
			Now:            time.Now().UTC(),
		},
	)
	cancel()
	switch {
	case errors.Is(err, resourcemanagement.ErrInvalidInput):
		handler.recordProjectFailure(c, identity.User.ID)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid project request")
	case errors.Is(err, resourcemanagement.ErrNotFound):
		handler.recordProjectFailure(c, identity.User.ID)
		writeError(c, http.StatusNotFound, "not_found", "tenant not found")
	case errors.Is(err, resourcemanagement.ErrStateConflict):
		handler.recordProjectFailure(c, identity.User.ID)
		writeError(c, http.StatusConflict, "resource_state_conflict", "tenant is not active")
	case errors.Is(err, resourcemanagement.ErrIdempotencyConflict):
		writeError(c, http.StatusConflict, "idempotency_conflict", "idempotency key was already used")
	case errors.Is(err, resourcemanagement.ErrDenied):
		handler.recordProjectFailure(c, identity.User.ID)
		writeError(c, http.StatusForbidden, "forbidden", "permission denied")
	case errors.Is(err, context.DeadlineExceeded):
		handler.recordProjectFailure(c, identity.User.ID)
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	case err != nil:
		handler.recordProjectFailure(c, identity.User.ID)
		handler.internalError(c, "create project", err)
	default:
		status := http.StatusCreated
		if result.Replayed {
			status = http.StatusOK
		}
		c.JSON(status, responseProject(result.Project, result.Replayed))
	}
}

func (handler *resourceManagementHandler) listClusters(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListClusters(ctx, c.Param("project_id"))
	cancel()
	switch {
	case errors.Is(err, resourcemanagement.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid project")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	case err != nil:
		handler.internalError(c, "list project clusters", err)
	default:
		response := make([]clusterResponse, 0, len(result))
		for _, item := range result {
			response = append(response, responseCluster(item))
		}
		c.JSON(http.StatusOK, gin.H{"clusters": response})
	}
}

func (handler *resourceManagementHandler) getCluster(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetCluster(ctx, c.Param("cluster_id"))
	cancel()
	switch {
	case errors.Is(err, resourcemanagement.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid cluster")
	case errors.Is(err, resourcemanagement.ErrNotFound):
		writeError(c, http.StatusNotFound, "not_found", "cluster not found")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	case err != nil:
		handler.internalError(c, "get cluster", err)
	default:
		c.JSON(http.StatusOK, responseCluster(result))
	}
}

func (handler *resourceManagementHandler) recordTenantFailure(
	c *gin.Context,
	userID string,
) {
	if handler.auditService == nil {
		return
	}
	ctx, cancel := handler.operationContext(c)
	defer cancel()
	if err := handler.auditService.RecordGlobalEvent(ctx, audit.GlobalEventInput{
		ActorUserID: userID,
		Action:      audit.ActionTenantCreate,
		TargetType:  "tenant",
		Result:      "failed",
		RequestID:   httpmiddleware.RequestID(c),
	}); err != nil {
		handler.logger.Error(
			"record tenant creation failure audit",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("user_id", userID),
			slog.String("error", err.Error()),
		)
	}
}

func (handler *resourceManagementHandler) recordProjectFailure(
	c *gin.Context,
	userID string,
) {
	if handler.auditService == nil {
		return
	}
	ctx, cancel := handler.operationContext(c)
	defer cancel()
	if err := handler.auditService.RecordTenantEvent(ctx, audit.TenantEventInput{
		ActorUserID: userID,
		TenantID:    c.Param("tenant_id"),
		Action:      audit.ActionProjectCreate,
		TargetType:  "project",
		Result:      "failed",
		RequestID:   httpmiddleware.RequestID(c),
	}); err != nil {
		handler.logger.Error(
			"record project creation failure audit",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("user_id", userID),
			slog.String("tenant_id", c.Param("tenant_id")),
			slog.String("error", err.Error()),
		)
	}
}

func (handler *resourceManagementHandler) operationContext(
	c *gin.Context,
) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), handler.operationTimeout)
}

func (handler *resourceManagementHandler) internalError(
	c *gin.Context,
	operation string,
	err error,
) {
	handler.logger.Error(
		operation,
		slog.String("request_id", httpmiddleware.RequestID(c)),
		slog.String("error", err.Error()),
	)
	writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
}

func responseTenant(
	item resourcemanagement.Tenant,
	replayed bool,
) tenantResponse {
	return tenantResponse{
		ID:        item.ID,
		Name:      item.Name,
		Status:    item.Status,
		CreatedAt: responseTime(item.CreatedAt),
		UpdatedAt: responseTime(item.UpdatedAt),
		Replayed:  replayed,
	}
}

func responseProject(
	item resourcemanagement.Project,
	replayed bool,
) projectResponse {
	return projectResponse{
		ID:        item.ID,
		TenantID:  item.TenantID,
		Name:      item.Name,
		Status:    item.Status,
		CreatedAt: responseTime(item.CreatedAt),
		UpdatedAt: responseTime(item.UpdatedAt),
		Replayed:  replayed,
	}
}

func responseCluster(item resourcemanagement.Cluster) clusterResponse {
	return clusterResponse{
		ID:         item.ID,
		TenantID:   item.TenantID,
		ProjectID:  item.ProjectID,
		Name:       item.Name,
		Status:     item.Status,
		LastSeenAt: responseTimePointer(item.LastSeenAt),
		CreatedAt:  responseTime(item.CreatedAt),
		UpdatedAt:  responseTime(item.UpdatedAt),
	}
}
