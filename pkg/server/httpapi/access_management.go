package httpapi

import (
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
	baseHandler
	service *accessmanagement.Service
}

// accessManagementErrors maps this resource's sentinels onto the API contract.
var accessManagementErrors = []errorMapping{
	{accessmanagement.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid access management request"},
	{accessmanagement.ErrNotFound, http.StatusNotFound, "not_found", "access management target not found"},
	{accessmanagement.ErrSelfDisable, http.StatusConflict, "self_disable_forbidden", "the authenticated user cannot disable itself"},
	{accessmanagement.ErrSelfDelete, http.StatusConflict, "self_delete_forbidden", "the authenticated user cannot delete itself"},
	{accessmanagement.ErrLastAdmin, http.StatusConflict, "last_global_admin", "the last active global administrator must be preserved"},
	{accessmanagement.ErrConflict, http.StatusConflict, "resource_conflict", "access management state conflicts with the request"},
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

// roleBindingSubject names the account a binding is about. Nested rather than
// flattened so that "absent" is one missing object rather than two empty
// strings a caller has to agree on how to interpret.
type roleBindingSubject struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type roleBindingResponse struct {
	ID        string `json:"id"`
	SubjectID string `json:"subject_id"`
	// Omitted when the subject row no longer exists; `subject_id` is always
	// present, so a caller can still identify and remove an orphaned binding.
	Subject   *roleBindingSubject `json:"subject,omitempty"`
	Role      string              `json:"role"`
	ScopeType string              `json:"scope_type"`
	TenantID  string              `json:"tenant_id,omitempty"`
	ProjectID string              `json:"project_id,omitempty"`
	CreatedAt time.Time           `json:"created_at"`
	Replayed  bool                `json:"replayed,omitempty"`
}

func newAccessManagementHandler(
	logger *slog.Logger,
	service *accessmanagement.Service,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *accessManagementHandler {
	return &accessManagementHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *accessManagementHandler) listUsers(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	query, err := parseListQuery(c, listFilters{search: true, status: true})
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid user query")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListUsers(ctx, accessmanagement.ListUsersInput{
		Status: query.Status,
		Search: query.Search,
		Now:    time.Now().UTC(),
		Page:   query.Page,
	})
	cancel()
	if handler.respondError(c, "list users", err, accessManagementErrors...) {
		return
	}
	response := make([]managedUserResponse, 0, len(result.Users))
	for _, item := range result.Users {
		response = append(response, responseManagedUser(item))
	}
	writeSuccess(c, http.StatusOK, gin.H{
		"users":      response,
		"pagination": responsePagination(result.Page),
	})
}

func (handler *accessManagementHandler) getUser(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetUser(ctx, c.Param("user_id"), time.Now().UTC())
	cancel()
	if handler.respondErrorAccess(c, "get user", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseManagedUser(result))
}

func (handler *accessManagementHandler) createUser(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request createUserRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil {
		handler.recordAccessFailure(c, identity.User.ID, "user.create", "user")
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
		handler.recordAccessFailure(c, identity.User.ID, "user.create", "user")
	}
	if handler.respondErrorAccess(c, "create user", err) {
		return
	}
	writeSuccess(c, http.StatusCreated, responseManagedUser(result))
}

func (handler *accessManagementHandler) updateUser(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request updateUserRequest
	if err := decodeJSONRequest(c, &request, maxAccessManagementRequestBytes); err != nil {
		handler.recordAccessFailure(c, identity.User.ID, "user.update", "user")
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
		handler.recordAccessFailure(c, identity.User.ID, "user.update", "user")
	}
	if handler.respondErrorAccess(c, "update user", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseManagedUser(result))
}

func (handler *accessManagementHandler) deleteUser(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(c, &request, maxAccessManagementRequestBytes); err != nil ||
		!request.Confirm {
		handler.recordAccessFailure(c, identity.User.ID, "user.delete", "user")
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
		handler.recordAccessFailure(c, identity.User.ID, "user.delete", "user")
	}
	if handler.respondErrorAccess(c, "delete user", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseManagedUser(result))
}

func (handler *accessManagementHandler) setUserStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request setUserStatusRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordAccessFailure(c, identity.User.ID, "user.status.update", "user")
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
		handler.recordAccessFailure(c, identity.User.ID, "user.status.update", "user")
	}
	if handler.respondErrorAccess(c, "update user status", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseManagedUser(result))
}

func (handler *accessManagementHandler) unlockUser(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordAccessFailure(c, identity.User.ID, "user.unlock", "user")
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
		handler.recordAccessFailure(c, identity.User.ID, "user.unlock", "user")
	}
	if handler.respondErrorAccess(c, "unlock user", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseManagedUser(result))
}

func (handler *accessManagementHandler) resetPassword(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request resetPasswordRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil || !request.Confirm {
		request.Password = ""
		handler.recordAccessFailure(c, identity.User.ID, "user.password.reset", "user")
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
		handler.recordAccessFailure(c, identity.User.ID, "user.password.reset", "user")
	}
	if handler.respondErrorAccess(c, "reset user password", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseManagedUser(result))
}

func (handler *accessManagementHandler) listRoleBindings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	query, err := parseListQuery(c, listFilters{
		search:    true,
		role:      true,
		scopeType: true,
	})
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid role binding query")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListRoleBindings(
		ctx,
		accessmanagement.ListRoleBindingsInput{
			Role:      query.Role,
			ScopeType: query.ScopeType,
			Search:    query.Search,
			Page:      query.Page,
		},
	)
	cancel()
	if handler.respondError(c, "list role bindings", err, accessManagementErrors...) {
		return
	}
	response := make([]roleBindingResponse, 0, len(result.RoleBindings))
	for _, item := range result.RoleBindings {
		response = append(response, responseRoleBinding(item, false))
	}
	writeSuccess(c, http.StatusOK, gin.H{
		"role_bindings": response,
		"pagination":    responsePagination(result.Page),
	})
}

func (handler *accessManagementHandler) getRoleBinding(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetRoleBinding(ctx, c.Param("role_binding_id"))
	cancel()
	if handler.respondErrorAccess(c, "get role binding", err) {
		return
	}
	writeSuccess(c, http.StatusOK, responseRoleBinding(result, false))
}

func (handler *accessManagementHandler) createRoleBinding(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request createRoleBindingRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordAccessFailure(c, identity.User.ID, "role_binding.create", "role_binding")
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
		handler.recordAccessFailure(c, identity.User.ID, "role_binding.create", "role_binding")
	}
	if handler.respondErrorAccess(c, "create role binding", err) {
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeSuccess(c, status, responseRoleBinding(result.RoleBinding, result.Replayed))
}

func (handler *accessManagementHandler) deleteRoleBinding(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordAccessFailure(c, identity.User.ID, "role_binding.delete", "role_binding")
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
		handler.recordAccessFailure(c, identity.User.ID, "role_binding.delete", "role_binding")
	}
	if handler.respondErrorAccess(c, "delete role binding", err) {
		return
	}
	writeSuccess(c, http.StatusOK, nil)
}

func (handler *accessManagementHandler) respondErrorAccess(
	c *gin.Context,
	operation string,
	err error,
) bool {
	return handler.respondError(c, operation, err, accessManagementErrors...)
}

// recordAccessFailure records a refused global access management operation.
func (handler *accessManagementHandler) recordAccessFailure(
	c *gin.Context,
	actorUserID string,
	action string,
	targetType string,
) {
	handler.recordFailure(c, failedOperation{
		Scope:       auditScopeGlobal,
		ActorUserID: actorUserID,
		Action:      action,
		TargetType:  targetType,
	})
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
	var subject *roleBindingSubject
	if item.SubjectUsername != "" {
		subject = &roleBindingSubject{
			ID:          item.SubjectID,
			Username:    item.SubjectUsername,
			DisplayName: item.SubjectDisplayName,
		}
	}
	return roleBindingResponse{
		ID:        item.ID,
		SubjectID: item.SubjectID,
		Subject:   subject,
		Role:      item.Role,
		ScopeType: item.ScopeType,
		TenantID:  item.TenantID,
		ProjectID: item.ProjectID,
		CreatedAt: responseTime(item.CreatedAt),
		Replayed:  replayed,
	}
}
