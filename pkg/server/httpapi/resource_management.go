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

type updateResourceRequest struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Confirm bool   `json:"confirm"`
}

type updateClusterRequest struct {
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

func (handler *resourceManagementHandler) getTenant(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetTenant(
		ctx, identity.User.ID, c.Param("tenant_id"),
	)
	cancel()
	if handler.handleResourceError(c, "get tenant", err) {
		return
	}
	c.JSON(http.StatusOK, responseTenant(result, false))
}

func (handler *resourceManagementHandler) updateTenant(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request updateResourceRequest
	if err := decodeJSONRequest(c, &request, maxCreateResourceRequestBytes); err != nil {
		handler.recordResourceFailure(c, identity.User.ID, "tenant.update", "tenant", "global")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid tenant request")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateTenant(ctx, resourcemanagement.UpdateTenantInput{
		TenantID: c.Param("tenant_id"), Name: request.Name, Status: request.Status,
		Confirm: request.Confirm, ActorUserID: identity.User.ID,
		RequestID: httpmiddleware.RequestID(c), Now: time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.recordResourceFailure(c, identity.User.ID, "tenant.update", "tenant", "global")
	}
	if handler.handleResourceError(c, "update tenant", err) {
		return
	}
	c.JSON(http.StatusOK, responseTenant(result, false))
}

func (handler *resourceManagementHandler) deleteTenant(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(c, &request, maxCreateResourceRequestBytes); err != nil ||
		!request.Confirm {
		handler.recordResourceFailure(c, identity.User.ID, "tenant.delete", "tenant", "global")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.DeleteTenant(ctx, resourcemanagement.DeleteTenantInput{
		TenantID: c.Param("tenant_id"), Confirm: request.Confirm,
		ActorUserID: identity.User.ID, RequestID: httpmiddleware.RequestID(c),
		Now: time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.recordResourceFailure(c, identity.User.ID, "tenant.delete", "tenant", "global")
	}
	if handler.handleResourceError(c, "delete tenant", err) {
		return
	}
	c.JSON(http.StatusOK, responseTenant(result, false))
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

func (handler *resourceManagementHandler) getProject(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetProject(ctx, c.Param("project_id"))
	cancel()
	if handler.handleResourceError(c, "get project", err) {
		return
	}
	c.JSON(http.StatusOK, responseProject(result, false))
}

func (handler *resourceManagementHandler) updateProject(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request updateResourceRequest
	if err := decodeJSONRequest(c, &request, maxCreateResourceRequestBytes); err != nil {
		handler.recordResourceFailure(c, identity.User.ID, "project.update", "project", "project")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid project request")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateProject(ctx, resourcemanagement.UpdateProjectInput{
		ProjectID: c.Param("project_id"), Name: request.Name, Status: request.Status,
		Confirm: request.Confirm, ActorUserID: identity.User.ID,
		RequestID: httpmiddleware.RequestID(c), Now: time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.recordResourceFailure(c, identity.User.ID, "project.update", "project", "project")
	}
	if handler.handleResourceError(c, "update project", err) {
		return
	}
	c.JSON(http.StatusOK, responseProject(result, false))
}

func (handler *resourceManagementHandler) deleteProject(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(c, &request, maxCreateResourceRequestBytes); err != nil ||
		!request.Confirm {
		handler.recordResourceFailure(c, identity.User.ID, "project.delete", "project", "project")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.DeleteProject(ctx, resourcemanagement.DeleteProjectInput{
		ProjectID: c.Param("project_id"), Confirm: request.Confirm,
		ActorUserID: identity.User.ID, RequestID: httpmiddleware.RequestID(c),
		Now: time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.recordResourceFailure(c, identity.User.ID, "project.delete", "project", "project")
	}
	if handler.handleResourceError(c, "delete project", err) {
		return
	}
	c.JSON(http.StatusOK, responseProject(result, false))
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

func (handler *resourceManagementHandler) updateCluster(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request updateClusterRequest
	if err := decodeJSONRequest(c, &request, maxCreateResourceRequestBytes); err != nil {
		handler.recordResourceFailure(c, identity.User.ID, "cluster.update", "cluster", "cluster")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid cluster request")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateCluster(ctx, resourcemanagement.UpdateClusterInput{
		ClusterID: c.Param("cluster_id"), Name: request.Name,
		ActorUserID: identity.User.ID, RequestID: httpmiddleware.RequestID(c),
		Now: time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.recordResourceFailure(c, identity.User.ID, "cluster.update", "cluster", "cluster")
	}
	if handler.handleResourceError(c, "update cluster", err) {
		return
	}
	c.JSON(http.StatusOK, responseCluster(result))
}

func (handler *resourceManagementHandler) deleteCluster(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(c, &request, maxCreateResourceRequestBytes); err != nil ||
		!request.Confirm {
		handler.recordResourceFailure(c, identity.User.ID, "cluster.delete", "cluster", "cluster")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.DeleteCluster(ctx, resourcemanagement.DeleteClusterInput{
		ClusterID: c.Param("cluster_id"), Confirm: request.Confirm,
		ActorUserID: identity.User.ID, RequestID: httpmiddleware.RequestID(c),
		Now: time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.recordResourceFailure(c, identity.User.ID, "cluster.delete", "cluster", "cluster")
	}
	if handler.handleResourceError(c, "delete cluster", err) {
		return
	}
	c.JSON(http.StatusOK, responseCluster(result))
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

func (handler *resourceManagementHandler) handleResourceError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, resourcemanagement.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid resource request")
	case errors.Is(err, resourcemanagement.ErrDenied):
		writeError(c, http.StatusForbidden, "forbidden", "permission denied")
	case errors.Is(err, resourcemanagement.ErrNotFound):
		writeError(c, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, resourcemanagement.ErrStateConflict):
		writeError(c, http.StatusConflict, "resource_state_conflict", "resource state conflicts with the request")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	default:
		handler.internalError(c, operation, err)
	}
	return true
}

func (handler *resourceManagementHandler) recordResourceFailure(
	c *gin.Context,
	userID string,
	action string,
	targetType string,
	scopeType string,
) {
	if handler.auditService == nil {
		return
	}
	ctx, cancel := handler.operationContext(c)
	defer cancel()
	var err error
	switch scopeType {
	case "project":
		err = handler.auditService.RecordProjectEvent(ctx, audit.ProjectEventInput{
			ActorUserID: userID, ProjectID: c.Param("project_id"), Action: action,
			Result: "failed", RequestID: httpmiddleware.RequestID(c),
		})
	case "cluster":
		err = handler.auditService.RecordClusterEvent(ctx, audit.ClusterEventInput{
			ActorUserID: userID, ClusterID: c.Param("cluster_id"), Action: action,
			Result: "failed", RequestID: httpmiddleware.RequestID(c),
		})
	default:
		err = handler.auditService.RecordGlobalEvent(ctx, audit.GlobalEventInput{
			ActorUserID: userID, Action: action, TargetType: targetType,
			Result: "failed", RequestID: httpmiddleware.RequestID(c),
		})
	}
	if err != nil {
		handler.logger.Error(
			"record resource mutation failure audit",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("action", action),
			slog.String("error", err.Error()),
		)
	}
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
