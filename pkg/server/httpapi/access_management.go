package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/accessmanagement"
	"github.com/togettoyou/zke/pkg/server/audit"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
)

const maxAccessManagementRequestBytes = 8 * 1024

type accessManagementHandler struct {
	logger           *slog.Logger
	service          *accessmanagement.Service
	auditService     *audit.Service
	operationTimeout time.Duration
}

type createUserRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
}

type updateUserRequest struct {
	DisplayName string `json:"display_name"`
}

type setUserStatusRequest struct {
	Status  string `json:"status"`
	Confirm bool   `json:"confirm"`
}

type confirmRequest struct {
	Confirm bool `json:"confirm"`
}

type resetPasswordRequest struct {
	Password string `json:"password"`
	Confirm  bool   `json:"confirm"`
}

type managedUserResponse struct {
	ID                string     `json:"id"`
	Username          string     `json:"username"`
	DisplayName       string     `json:"display_name"`
	Status            string     `json:"status"`
	FailedLoginCount  int        `json:"failed_login_count"`
	LockedAt          *time.Time `json:"locked_at,omitempty"`
	LockExpiresAt     *time.Time `json:"lock_expires_at,omitempty"`
	PasswordChangedAt time.Time  `json:"password_changed_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type createRoleBindingRequest struct {
	SubjectID string `json:"subject_id"`
	Role      string `json:"role"`
	ScopeType string `json:"scope_type"`
	TenantID  string `json:"tenant_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Confirm   bool   `json:"confirm"`
}

type roleBindingResponse struct {
	ID        string    `json:"id"`
	SubjectID string    `json:"subject_id"`
	Role      string    `json:"role"`
	ScopeType string    `json:"scope_type"`
	TenantID  string    `json:"tenant_id,omitempty"`
	ProjectID string    `json:"project_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Replayed  bool      `json:"replayed,omitempty"`
}

func newAccessManagementHandler(
	logger *slog.Logger,
	service *accessmanagement.Service,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *accessManagementHandler {
	return &accessManagementHandler{
		logger:           logger,
		service:          service,
		auditService:     auditService,
		operationTimeout: operationTimeout,
	}
}

func (handler *accessManagementHandler) listUsers(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	query, err := parseListQuery(c)
	if err != nil || !allowed(query.Status, "active", "locked", "disabled") ||
		query.Role != "" || query.ScopeType != "" {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid user query")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListUsers(ctx)
	cancel()
	if handler.handleError(c, "list users", err) {
		return
	}
	response := make([]managedUserResponse, 0, len(result))
	for _, item := range result {
		if query.Status != "" && item.Status != query.Status {
			continue
		}
		if !containsFold(query.Search, item.Username, item.DisplayName, item.ID) {
			continue
		}
		response = append(response, responseManagedUser(item))
	}
	response, pagination := paginate(response, query)
	c.JSON(http.StatusOK, gin.H{"users": response, "pagination": pagination})
}

func (handler *accessManagementHandler) getUser(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetUser(ctx, c.Param("user_id"))
	cancel()
	if handler.handleError(c, "get user", err) {
		return
	}
	c.JSON(http.StatusOK, responseManagedUser(result))
}

func (handler *accessManagementHandler) createUser(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request createUserRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil {
		handler.recordFailure(c, identity.User.ID, "user.create", "user")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid user request")
		return
	}
	password := []byte(request.Password)
	request.Password = ""
	defer clear(password)
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateUser(ctx, accessmanagement.CreateUserInput{
		Username:    request.Username,
		DisplayName: request.DisplayName,
		Password:    password,
		ActorUserID: identity.User.ID,
		RequestID:   httpmiddleware.RequestID(c),
		Now:         time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.recordFailure(c, identity.User.ID, "user.create", "user")
	}
	if handler.handleError(c, "create user", err) {
		return
	}
	c.JSON(http.StatusCreated, responseManagedUser(result))
}

func (handler *accessManagementHandler) updateUser(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request updateUserRequest
	if err := decodeJSONRequest(c, &request, maxAccessManagementRequestBytes); err != nil {
		handler.recordFailure(c, identity.User.ID, "user.update", "user")
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid user request")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateUser(ctx, accessmanagement.UpdateUserInput{
		UserID: c.Param("user_id"), DisplayName: request.DisplayName,
		ActorUserID: identity.User.ID, RequestID: httpmiddleware.RequestID(c),
		Now: time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.recordFailure(c, identity.User.ID, "user.update", "user")
	}
	if handler.handleError(c, "update user", err) {
		return
	}
	c.JSON(http.StatusOK, responseManagedUser(result))
}

func (handler *accessManagementHandler) deleteUser(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(c, &request, maxAccessManagementRequestBytes); err != nil ||
		!request.Confirm {
		handler.recordFailure(c, identity.User.ID, "user.delete", "user")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.DeleteUser(ctx, accessmanagement.DeleteUserInput{
		UserID: c.Param("user_id"), Confirm: request.Confirm,
		ActorUserID: identity.User.ID, RequestID: httpmiddleware.RequestID(c),
		Now: time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.recordFailure(c, identity.User.ID, "user.delete", "user")
	}
	if handler.handleError(c, "delete user", err) {
		return
	}
	c.JSON(http.StatusOK, responseManagedUser(result))
}

func (handler *accessManagementHandler) setUserStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request setUserStatusRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordFailure(c, identity.User.ID, "user.status.update", "user")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.SetUserStatus(
		ctx,
		accessmanagement.SetUserStatusInput{
			UserID:      c.Param("user_id"),
			Status:      request.Status,
			ActorUserID: identity.User.ID,
			RequestID:   httpmiddleware.RequestID(c),
			Now:         time.Now().UTC(),
		},
	)
	cancel()
	if err != nil {
		handler.recordFailure(c, identity.User.ID, "user.status.update", "user")
	}
	if handler.handleError(c, "update user status", err) {
		return
	}
	c.JSON(http.StatusOK, responseManagedUser(result))
}

func (handler *accessManagementHandler) unlockUser(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordFailure(c, identity.User.ID, "user.unlock", "user")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UnlockUser(
		ctx,
		accessmanagement.ConfirmUserActionInput{
			UserID:      c.Param("user_id"),
			Confirm:     request.Confirm,
			ActorUserID: identity.User.ID,
			RequestID:   httpmiddleware.RequestID(c),
			Now:         time.Now().UTC(),
		},
	)
	cancel()
	if err != nil {
		handler.recordFailure(c, identity.User.ID, "user.unlock", "user")
	}
	if handler.handleError(c, "unlock user", err) {
		return
	}
	c.JSON(http.StatusOK, responseManagedUser(result))
}

func (handler *accessManagementHandler) resetPassword(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request resetPasswordRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil || !request.Confirm {
		request.Password = ""
		handler.recordFailure(c, identity.User.ID, "user.password.reset", "user")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	password := []byte(request.Password)
	request.Password = ""
	defer clear(password)
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ResetUserPassword(
		ctx,
		accessmanagement.ResetPasswordInput{
			UserID:      c.Param("user_id"),
			Password:    password,
			Confirm:     request.Confirm,
			ActorUserID: identity.User.ID,
			RequestID:   httpmiddleware.RequestID(c),
			Now:         time.Now().UTC(),
		},
	)
	cancel()
	if err != nil {
		handler.recordFailure(c, identity.User.ID, "user.password.reset", "user")
	}
	if handler.handleError(c, "reset user password", err) {
		return
	}
	c.JSON(http.StatusOK, responseManagedUser(result))
}

func (handler *accessManagementHandler) listRoleBindings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	query, err := parseListQuery(c)
	if err != nil || query.Status != "" ||
		!allowed(query.Role, "admin", "viewer") ||
		!allowed(query.ScopeType, "global", "tenant", "project") {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid role binding query")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListRoleBindings(ctx)
	cancel()
	if handler.handleError(c, "list role bindings", err) {
		return
	}
	response := make([]roleBindingResponse, 0, len(result))
	for _, item := range result {
		if query.Role != "" && item.Role != query.Role {
			continue
		}
		if query.ScopeType != "" && item.ScopeType != query.ScopeType {
			continue
		}
		if !containsFold(
			query.Search,
			item.ID,
			item.SubjectID,
			item.TenantID,
			item.ProjectID,
		) {
			continue
		}
		response = append(response, responseRoleBinding(item, false))
	}
	response, pagination := paginate(response, query)
	c.JSON(http.StatusOK, gin.H{
		"role_bindings": response,
		"pagination":    pagination,
	})
}

func (handler *accessManagementHandler) getRoleBinding(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetRoleBinding(ctx, c.Param("role_binding_id"))
	cancel()
	if handler.handleError(c, "get role binding", err) {
		return
	}
	c.JSON(http.StatusOK, responseRoleBinding(result, false))
}

func (handler *accessManagementHandler) createRoleBinding(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request createRoleBindingRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordFailure(c, identity.User.ID, "role_binding.create", "role_binding")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateRoleBinding(
		ctx,
		accessmanagement.CreateRoleBindingInput{
			SubjectID:   request.SubjectID,
			Role:        request.Role,
			ScopeType:   request.ScopeType,
			TenantID:    request.TenantID,
			ProjectID:   request.ProjectID,
			ActorUserID: identity.User.ID,
			RequestID:   httpmiddleware.RequestID(c),
			Now:         time.Now().UTC(),
		},
	)
	cancel()
	if err != nil {
		handler.recordFailure(c, identity.User.ID, "role_binding.create", "role_binding")
	}
	if handler.handleError(c, "create role binding", err) {
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	c.JSON(status, responseRoleBinding(result.RoleBinding, result.Replayed))
}

func (handler *accessManagementHandler) deleteRoleBinding(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordFailure(c, identity.User.ID, "role_binding.delete", "role_binding")
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	ctx, cancel := handler.operationContext(c)
	_, err := handler.service.DeleteRoleBinding(
		ctx,
		accessmanagement.DeleteRoleBindingInput{
			BindingID:   c.Param("role_binding_id"),
			Confirm:     request.Confirm,
			ActorUserID: identity.User.ID,
			RequestID:   httpmiddleware.RequestID(c),
			Now:         time.Now().UTC(),
		},
	)
	cancel()
	if err != nil {
		handler.recordFailure(c, identity.User.ID, "role_binding.delete", "role_binding")
	}
	if handler.handleError(c, "delete role binding", err) {
		return
	}
	c.Status(http.StatusNoContent)
}

func (handler *accessManagementHandler) recordFailure(
	c *gin.Context,
	actorUserID string,
	action string,
	targetType string,
) {
	if handler.auditService == nil {
		return
	}
	ctx, cancel := context.WithTimeout(
		c.Request.Context(),
		handler.operationTimeout,
	)
	err := handler.auditService.RecordGlobalEvent(ctx, audit.GlobalEventInput{
		ActorUserID: actorUserID,
		Action:      action,
		TargetType:  targetType,
		Result:      "failed",
		RequestID:   httpmiddleware.RequestID(c),
	})
	cancel()
	if err != nil {
		handler.logger.Error(
			"record access management failure audit",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("action", action),
			slog.String("error", err.Error()),
		)
	}
}

func (handler *accessManagementHandler) operationContext(
	c *gin.Context,
) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), handler.operationTimeout)
}

func (handler *accessManagementHandler) handleError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, accessmanagement.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid access management request")
	case errors.Is(err, accessmanagement.ErrNotFound):
		writeError(c, http.StatusNotFound, "not_found", "access management target not found")
	case errors.Is(err, accessmanagement.ErrSelfDisable):
		writeError(c, http.StatusConflict, "self_disable_forbidden", "the authenticated user cannot disable itself")
	case errors.Is(err, accessmanagement.ErrLastAdmin):
		writeError(c, http.StatusConflict, "last_global_admin", "the last active global administrator must be preserved")
	case errors.Is(err, accessmanagement.ErrConflict):
		writeError(c, http.StatusConflict, "resource_conflict", "access management state conflicts with the request")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	default:
		handler.logger.Error(
			operation,
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("error", err.Error()),
		)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	}
	return true
}

func responseManagedUser(item accessmanagement.User) managedUserResponse {
	return managedUserResponse{
		ID:                item.ID,
		Username:          item.Username,
		DisplayName:       item.DisplayName,
		Status:            item.Status,
		FailedLoginCount:  item.FailedLoginCount,
		LockedAt:          responseTimePointer(item.LockedAt),
		LockExpiresAt:     responseTimePointer(item.LockExpiresAt),
		PasswordChangedAt: responseTime(item.PasswordChangedAt),
		CreatedAt:         responseTime(item.CreatedAt),
		UpdatedAt:         responseTime(item.UpdatedAt),
	}
}

func responseRoleBinding(
	item accessmanagement.RoleBinding,
	replayed bool,
) roleBindingResponse {
	return roleBindingResponse{
		ID:        item.ID,
		SubjectID: item.SubjectID,
		Role:      item.Role,
		ScopeType: item.ScopeType,
		TenantID:  item.TenantID,
		ProjectID: item.ProjectID,
		CreatedAt: responseTime(item.CreatedAt),
		Replayed:  replayed,
	}
}
