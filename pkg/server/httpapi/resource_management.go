package httpapi

import (
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
	baseHandler
	service *resourcemanagement.Service
}

var resourceManagementErrors = []errorMapping{
	{resourcemanagement.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid resource request"},
	{resourcemanagement.ErrDenied, http.StatusForbidden, "forbidden", "permission denied"},
	{resourcemanagement.ErrNotFound, http.StatusNotFound, "not_found", "resource not found"},
	{resourcemanagement.ErrStateConflict, http.StatusConflict, "resource_state_conflict", "resource state conflicts with the request"},
	{resourcemanagement.ErrIdempotencyConflict, http.StatusConflict, "idempotency_conflict", "idempotency key was already used"},
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
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *resourceManagementHandler) listTenants(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	query, queryErr := parseListQuery(c, listFilters{search: true, status: true})
	if queryErr != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid tenant query")
		return
	}
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListTenants(ctx, resourcemanagement.ListTenantsInput{
		UserID: identity.User.ID,
		Status: query.Status,
		Search: query.Search,
		Page:   query.Page,
	})
	cancel()
	if handler.respondError(c, "list visible tenants", err, resourceManagementErrors...) {
		return
	}
	response := make([]tenantResponse, 0, len(result.Tenants))
	for _, item := range result.Tenants {
		response = append(response, responseTenant(item, false))
	}
	writeSuccess(c, http.StatusOK, gin.H{
		"tenants":    response,
		"pagination": responsePagination(result.Page),
	})
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
	if err != nil && !errors.Is(err, resourcemanagement.ErrIdempotencyConflict) {
		handler.recordTenantFailure(c, identity.User.ID)
	}
	if handler.respondError(c, "create tenant", err, resourceManagementErrors...) {
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeSuccess(c, status, responseTenant(result.Tenant, result.Replayed))
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
	writeSuccess(c, http.StatusOK, responseTenant(result, false))
}

func (handler *resourceManagementHandler) updateTenant(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request updateResourceRequest
	if err := decodeJSONRequest(c, &request, maxCreateResourceRequestBytes); err != nil {
		handler.recordResourceFailure(c, identity.User.ID, "tenant.update", "tenant", auditScopeGlobal)
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
		handler.recordResourceFailure(c, identity.User.ID, "tenant.update", "tenant", auditScopeGlobal)
	}
	if handler.handleResourceError(c, "update tenant", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseTenant(result, false))
}

func (handler *resourceManagementHandler) deleteTenant(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(c, &request, maxCreateResourceRequestBytes); err != nil ||
		!request.Confirm {
		handler.recordResourceFailure(c, identity.User.ID, "tenant.delete", "tenant", auditScopeGlobal)
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
		handler.recordResourceFailure(c, identity.User.ID, "tenant.delete", "tenant", auditScopeGlobal)
	}
	if handler.handleResourceError(c, "delete tenant", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseTenant(result, false))
}

func (handler *resourceManagementHandler) listProjects(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	query, queryErr := parseListQuery(c, listFilters{search: true, status: true})
	if queryErr != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid project query")
		return
	}
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListProjects(ctx, resourcemanagement.ListProjectsInput{
		UserID:   identity.User.ID,
		TenantID: c.Param("tenant_id"),
		Status:   query.Status,
		Search:   query.Search,
		Page:     query.Page,
	})
	cancel()
	if handler.respondError(c, "list visible projects", err, resourceManagementErrors...) {
		return
	}
	response := make([]projectResponse, 0, len(result.Projects))
	for _, item := range result.Projects {
		response = append(response, responseProject(item, false))
	}
	writeSuccess(c, http.StatusOK, gin.H{
		"projects":   response,
		"pagination": responsePagination(result.Page),
	})
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
	if err != nil && !errors.Is(err, resourcemanagement.ErrIdempotencyConflict) {
		handler.recordProjectFailure(c, identity.User.ID)
	}
	if handler.respondError(c, "create project", err, resourceManagementErrors...) {
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeSuccess(c, status, responseProject(result.Project, result.Replayed))
}

func (handler *resourceManagementHandler) getProject(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetProject(ctx, c.Param("project_id"))
	cancel()
	if handler.handleResourceError(c, "get project", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseProject(result, false))
}

func (handler *resourceManagementHandler) updateProject(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request updateResourceRequest
	if err := decodeJSONRequest(c, &request, maxCreateResourceRequestBytes); err != nil {
		handler.recordResourceFailure(c, identity.User.ID, "project.update", "project", auditScopeProject)
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
		handler.recordResourceFailure(c, identity.User.ID, "project.update", "project", auditScopeProject)
	}
	if handler.handleResourceError(c, "update project", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseProject(result, false))
}

func (handler *resourceManagementHandler) deleteProject(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(c, &request, maxCreateResourceRequestBytes); err != nil ||
		!request.Confirm {
		handler.recordResourceFailure(c, identity.User.ID, "project.delete", "project", auditScopeProject)
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
		handler.recordResourceFailure(c, identity.User.ID, "project.delete", "project", auditScopeProject)
	}
	if handler.handleResourceError(c, "delete project", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseProject(result, false))
}

func (handler *resourceManagementHandler) updateCluster(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request updateClusterRequest
	if err := decodeJSONRequest(c, &request, maxCreateResourceRequestBytes); err != nil {
		handler.recordResourceFailure(c, identity.User.ID, "cluster.update", "cluster", auditScopeCluster)
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
		handler.recordResourceFailure(c, identity.User.ID, "cluster.update", "cluster", auditScopeCluster)
	}
	if handler.handleResourceError(c, "update cluster", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseCluster(result))
}

func (handler *resourceManagementHandler) deleteCluster(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(c, &request, maxCreateResourceRequestBytes); err != nil ||
		!request.Confirm {
		handler.recordResourceFailure(c, identity.User.ID, "cluster.delete", "cluster", auditScopeCluster)
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
		handler.recordResourceFailure(c, identity.User.ID, "cluster.delete", "cluster", auditScopeCluster)
	}
	if handler.handleResourceError(c, "delete cluster", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseCluster(result))
}

func (handler *resourceManagementHandler) recordTenantFailure(
	c *gin.Context,
	userID string,
) {
	handler.recordFailure(c, failedOperation{
		Scope:       auditScopeGlobal,
		ActorUserID: userID,
		Action:      audit.ActionTenantCreate,
		TargetType:  "tenant",
	})
}

func (handler *resourceManagementHandler) recordProjectFailure(
	c *gin.Context,
	userID string,
) {
	handler.recordFailure(c, failedOperation{
		Scope:       auditScopeTenant,
		ActorUserID: userID,
		Action:      audit.ActionProjectCreate,
		TargetType:  "project",
	})
}

func (handler *resourceManagementHandler) handleResourceError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	return handler.respondError(c, operation, err, resourceManagementErrors...)
}

func (handler *resourceManagementHandler) recordResourceFailure(
	c *gin.Context,
	userID string,
	action string,
	targetType string,
	scope auditScope,
) {
	handler.recordFailure(c, failedOperation{
		Scope:       scope,
		ActorUserID: userID,
		Action:      action,
		TargetType:  targetType,
	})
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
