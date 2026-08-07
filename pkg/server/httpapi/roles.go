package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/accessmanagement"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

/*
 * Roles, on endpoints of their own.
 *
 * They sit under `rbac.read` and `rbac.manage` beside the bindings, because a
 * role and a binding are two halves of one question — what a permission set is,
 * and who holds it. What makes these endpoints different from the bindings is
 * reach: editing a role changes what everybody already bound to it can do,
 * without a single binding being touched. That is why every write here takes an
 * explicit confirmation and writes an audit record naming the role.
 *
 * The permission dictionary is served alongside them. The Console cannot offer
 * a permission picker without knowing the vocabulary, and hard-coding it in the
 * browser is how a permission added to the Server becomes one nobody can grant.
 */

type roleResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	Builtin     bool     `json:"builtin"`
	Permissions []string `json:"permissions"`
	// How many bindings name this role. The Console shows it next to the delete
	// action, which is refused while it is above zero.
	BindingCount int       `json:"binding_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type createRoleRequest struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	Confirm     bool     `json:"confirm"`
}

type updateRoleRequest struct {
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	Confirm     bool     `json:"confirm"`
}

// roleErrors extends the access management mappings with the refusals only
// roles produce. They are separate status codes on purpose: a builtin role, a
// role somebody still holds, and a role carrying more than its author does are
// three different things to fix.
// The escalation refusal itself lives in accessManagementErrors, because the
// binding endpoints raise it too. 403 rather than 400 wherever it appears: the
// request is well formed and the caller authenticated — what it lacks is the
// permissions it is trying to hand out.
var roleErrors = append([]errorMapping{
	{accessmanagement.ErrRoleBuiltin, http.StatusConflict, "builtin_role", "内置角色不可修改或删除"},
	{accessmanagement.ErrRoleInUse, http.StatusConflict, "role_in_use", "角色仍被绑定，请先删除相关绑定"},
}, accessManagementErrors...)

func (handler *accessManagementHandler) listRoles(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	query, err := parseListQuery(c, listFilters{
		search: true,
		extra:  []string{"builtin"},
	})
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid role query")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListRoles(ctx, accessmanagement.ListRolesInput{
		Builtin: query.extra["builtin"],
		Search:  query.Search,
		Page:    query.Page,
	})
	cancel()
	if handler.respondError(c, "list roles", err, roleErrors...) {
		return
	}
	response := make([]roleResponse, 0, len(result.Roles))
	for _, item := range result.Roles {
		response = append(response, responseRole(item))
	}
	writeSuccess(c, http.StatusOK, gin.H{
		"roles":      response,
		"pagination": responsePagination(result.Page),
	})
}

func (handler *accessManagementHandler) getRole(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetRole(ctx, c.Param("role_id"))
	cancel()
	if handler.respondError(c, "get role", err, roleErrors...) {
		return
	}
	writeSuccess(c, http.StatusOK, responseRole(result))
}

func (handler *accessManagementHandler) createRole(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request createRoleRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordAccessFailure(c, identity.User.ID, auditaction.RoleCreate, auditaction.TargetRole)
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.CreateRole(ctx, accessmanagement.CreateRoleInput{
		Name:        request.Name,
		DisplayName: request.DisplayName,
		Description: request.Description,
		Permissions: request.Permissions,
		Confirm:     request.Confirm,
		ActorUserID: identity.User.ID,
		RequestID:   httpmiddleware.RequestID(c),
		Now:         time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.recordAccessFailure(c, identity.User.ID, auditaction.RoleCreate, auditaction.TargetRole)
	}
	if handler.respondError(c, "create role", err, roleErrors...) {
		return
	}
	writeSuccess(c, http.StatusCreated, responseRole(result))
}

func (handler *accessManagementHandler) updateRole(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request updateRoleRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordAccessFailure(c, identity.User.ID, auditaction.RoleUpdate, auditaction.TargetRole)
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.UpdateRole(ctx, accessmanagement.UpdateRoleInput{
		RoleID:      c.Param("role_id"),
		DisplayName: request.DisplayName,
		Description: request.Description,
		Permissions: request.Permissions,
		Confirm:     request.Confirm,
		ActorUserID: identity.User.ID,
		RequestID:   httpmiddleware.RequestID(c),
		Now:         time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.recordAccessFailure(c, identity.User.ID, auditaction.RoleUpdate, auditaction.TargetRole)
	}
	if handler.respondError(c, "update role", err, roleErrors...) {
		return
	}
	writeSuccess(c, http.StatusOK, responseRole(result))
}

func (handler *accessManagementHandler) deleteRole(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request confirmRequest
	if err := decodeJSONRequest(
		c, &request, maxAccessManagementRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordAccessFailure(c, identity.User.ID, auditaction.RoleDelete, auditaction.TargetRole)
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	ctx, cancel := handler.operationContext(c)
	_, err := handler.service.DeleteRole(ctx, accessmanagement.DeleteRoleInput{
		RoleID:      c.Param("role_id"),
		Confirm:     request.Confirm,
		ActorUserID: identity.User.ID,
		RequestID:   httpmiddleware.RequestID(c),
		Now:         time.Now().UTC(),
	})
	cancel()
	if err != nil {
		handler.recordAccessFailure(c, identity.User.ID, auditaction.RoleDelete, auditaction.TargetRole)
	}
	if handler.respondError(c, "delete role", err, roleErrors...) {
		return
	}
	writeSuccess(c, http.StatusOK, nil)
}

/*
 * The permission vocabulary.
 *
 * Served rather than published as a constant in the Console because the two
 * would drift, and the drift is silent in the direction that matters: a
 * permission the Server enforces but the Console does not list is one no role
 * can be given, and nothing anywhere reports it.
 *
 * The response carries `held` — whether the caller holds the permission
 * globally — because that is what decides whether they may put it in a role.
 * Without it the Console can only offer every permission and let the Server
 * refuse the save, which teaches an operator the rule one rejection at a time.
 */
func (handler *accessManagementHandler) listPermissions(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	ctx, cancel := handler.operationContext(c)
	held, err := handler.permissions.GlobalPermissions(ctx, identity.User.ID)
	cancel()
	if handler.respondError(c, "list permissions", err, roleErrors...) {
		return
	}
	response := make([]gin.H, 0, len(rbac.Permissions()))
	for _, permission := range rbac.Permissions() {
		_, granted := held[permission]
		response = append(response, gin.H{
			"name": string(permission),
			"held": granted,
		})
	}
	writeSuccess(c, http.StatusOK, gin.H{"permissions": response})
}

func responseRole(item accessmanagement.Role) roleResponse {
	permissions := item.Permissions
	if permissions == nil {
		permissions = []string{}
	}
	return roleResponse{
		ID:           item.ID,
		Name:         item.Name,
		DisplayName:  item.DisplayName,
		Description:  item.Description,
		Builtin:      item.Builtin,
		Permissions:  permissions,
		BindingCount: item.BindingCount,
		CreatedAt:    responseTime(item.CreatedAt),
		UpdatedAt:    responseTime(item.UpdatedAt),
	}
}
