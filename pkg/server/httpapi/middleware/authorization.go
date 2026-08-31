package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/auditctx"
)

type AuthorizationConfig struct {
	OperationTimeout time.Duration
}

type Authorization struct {
	logger       *slog.Logger
	service      *rbac.Service
	auditService *audit.Service
	config       AuthorizationConfig
}

func NewAuthorization(
	logger *slog.Logger,
	service *rbac.Service,
	auditService *audit.Service,
	config AuthorizationConfig,
) *Authorization {
	return &Authorization{
		logger:       logger,
		service:      service,
		auditService: auditService,
		config:       config,
	}
}

func (authorization *Authorization) RequireGlobal(
	permission rbac.Permission,
) gin.HandlerFunc {
	return authorization.require(permission, "global", "", func(
		ctx context.Context,
		userID string,
		_ *gin.Context,
	) (rbac.ResolvedScope, rbac.Permission, error) {
		return rbac.ResolvedScope{}, permission, authorization.service.AuthorizeGlobal(
			ctx,
			userID,
			permission,
		)
	})
}

func (authorization *Authorization) RequireGlobalAdministrator(c *gin.Context) {
	identity, exists := Identity(c)
	if !exists {
		writeError(c, http.StatusUnauthorized, "unauthenticated", "authentication required")
		c.Abort()
		return
	}
	operationContext, cancel := context.WithTimeout(c.Request.Context(), authorization.config.OperationTimeout)
	allowed, err := authorization.service.IsGlobalAdministrator(operationContext, identity.User.ID)
	cancel()
	if err != nil {
		authorization.logger.Error("authorize global administrator",
			slog.String("request_id", RequestID(c)), slog.String("user_id", identity.User.ID),
			slog.String("error", err.Error()))
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
		c.Abort()
		return
	}
	if !allowed {
		authorization.recordGlobalAdministratorDenied(c, identity.User.ID)
		writeError(c, http.StatusForbidden, "global_admin_required", "global administrator required")
		c.Abort()
		return
	}
	c.Next()
}

func (authorization *Authorization) recordGlobalAdministratorDenied(c *gin.Context, userID string) {
	if authorization.auditService == nil {
		return
	}
	auditContext, cancel := auditctx.Detach(c.Request.Context(), authorization.config.OperationTimeout)
	defer cancel()
	if err := authorization.auditService.RecordGlobalEvent(auditContext, audit.GlobalEventInput{
		ActorUserID: userID,
		Action:      auditaction.PlatformSettingsManage,
		TargetType:  auditaction.TargetPlatformSettings,
		Result:      "denied",
		RequestID:   RequestID(c),
		ActorIP:     c.ClientIP(),
		Detail:      map[string]string{"scope_type": "global"},
	}); err != nil {
		authorization.logger.Error("record global administrator denial",
			slog.String("request_id", RequestID(c)), slog.String("user_id", userID), slog.String("error", err.Error()))
	}
}

func (authorization *Authorization) RequireTenant(
	permission rbac.Permission,
	tenantParameter string,
) gin.HandlerFunc {
	return authorization.require(permission, "tenant", tenantParameter, func(
		ctx context.Context,
		userID string,
		c *gin.Context,
	) (rbac.ResolvedScope, rbac.Permission, error) {
		tenantID := c.Param(tenantParameter)
		return rbac.ResolvedScope{TenantID: tenantID}, permission,
			authorization.service.AuthorizeTenant(
				ctx,
				userID,
				permission,
				tenantID,
			)
	})
}

func (authorization *Authorization) RequireProject(
	permission rbac.Permission,
	projectParameter string,
) gin.HandlerFunc {
	return authorization.require(permission, "project", projectParameter, func(
		ctx context.Context,
		userID string,
		c *gin.Context,
	) (rbac.ResolvedScope, rbac.Permission, error) {
		resolved, err := authorization.service.AuthorizeProject(
			ctx,
			userID,
			permission,
			c.Param(projectParameter),
		)
		return resolved, permission, err
	})
}

func (authorization *Authorization) RequireCluster(
	permission rbac.Permission,
	clusterParameter string,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		authorization.require(permission, "cluster", clusterParameter, func(
			ctx context.Context,
			userID string,
			c *gin.Context,
		) (rbac.ResolvedScope, rbac.Permission, error) {
			resolved, exists := ResolvedScope(c)
			if !exists {
				var err error
				resolved, err = authorization.service.ResolveClusterScope(ctx, c.Param(clusterParameter))
				if err != nil {
					return resolved, permission, err
				}
			}
			effective := authorization.effectiveClusterPermission(c, permission, resolved.AgentNamespace)
			return resolved, effective, authorization.service.AuthorizeResolvedCluster(ctx, userID, effective, resolved)
		})(c)
	}
}

// effectiveClusterPermission keeps ordinary resource powers out of the two
// namespaces that host Kubernetes control-plane add-ons and the ZKE Agent.
// Family-specific permissions such as Secret and RBAC remain unchanged and are
// combined with the namespace permission by RequireProtectedNamespaceAccess.
func (authorization *Authorization) effectiveClusterPermission(
	c *gin.Context,
	permission rbac.Permission,
	agentNamespace string,
) rbac.Permission {
	protected := authorization.protectedNamespacePermission(c, agentNamespace)
	switch permission {
	case rbac.PermissionClusterNamespaceManage,
		rbac.PermissionClusterResourceCreate,
		rbac.PermissionClusterResourceUpdate,
		rbac.PermissionClusterResourceDelete:
		// A Node is Cluster-scoped, so no protected-Namespace grant can ever
		// apply to one; the two branches cannot both match.
		if isNodeObjectMutation(c) {
			return rbac.PermissionClusterNodeManage
		}
		if protected != "" {
			return protected
		}
		if isNamespaceObjectMutation(c) {
			return rbac.PermissionClusterNamespaceManage
		}
		return permission
	default:
		return permission
	}
}

// A write to the Node object itself through the generic resource routes: its
// YAML, its labels, its taints, `spec.unschedulable`. Node labels and taints
// decide where every workload in the Cluster may run, so that write answers to
// `cluster.node.manage` rather than to the ordinary resource permissions —
// exactly as a Namespace write answers to `cluster.namespace.manage`.
func isNodeObjectMutation(c *gin.Context) bool {
	return c.Request.Method != http.MethodGet &&
		strings.Contains(c.FullPath(), "/kubernetes/resources") &&
		c.Query("group") == "" && c.Query("version") == "v1" &&
		c.Query("resource") == "nodes"
}

func isNamespaceObjectMutation(c *gin.Context) bool {
	return c.Request.Method != http.MethodGet &&
		strings.Contains(c.FullPath(), "/kubernetes/resources") &&
		c.Query("group") == "" && c.Query("version") == "v1" &&
		c.Query("resource") == "namespaces"
}

// RequireProtectedNamespaceAccess adds the namespace boundary to sensitive
// families whose own permission must remain in force (Secrets, Kubernetes RBAC,
// Pod exec and port-forward). General resource and Namespace lifecycle routes
// are switched to the namespace permission directly by RequireCluster.
func (authorization *Authorization) RequireProtectedNamespaceAccess(
	clusterParameter string,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !protectedNamespaceRequestNeedsAdditionalGate(c) {
			c.Next()
			return
		}
		authorization.require(rbac.PermissionClusterRead, "cluster", clusterParameter, func(
			ctx context.Context,
			userID string,
			c *gin.Context,
		) (rbac.ResolvedScope, rbac.Permission, error) {
			resolved, err := authorization.service.ResolveClusterScope(ctx, c.Param(clusterParameter))
			if err != nil {
				return resolved, rbac.PermissionClusterRead, err
			}
			permission := authorization.protectedNamespacePermission(c, resolved.AgentNamespace)
			if permission == "" {
				return resolved, "", nil
			}
			return resolved, permission, authorization.service.AuthorizeResolvedCluster(ctx, userID, permission, resolved)
		})(c)
	}
}

func (authorization *Authorization) protectedNamespacePermission(c *gin.Context, agentNamespace string) rbac.Permission {
	namespace := c.Param("namespace_name")
	namespaceObjectTarget := false
	if namespace == "" {
		namespace = c.Query("namespace")
	}
	// Generic Namespace writes are cluster-scoped, so their target name is the
	// resource path segment rather than the namespace query parameter.
	if namespace == "" && c.Param("resource_name") != "" &&
		c.Query("group") == "" && c.Query("version") == "v1" &&
		c.Query("resource") == "namespaces" {
		namespace = c.Param("resource_name")
		namespaceObjectTarget = true
	}
	if namespace == "" && c.Request.Method == http.MethodPost &&
		(c.FullPath() == "/api/v1/clusters/:cluster_id/namespaces" ||
			(c.FullPath() == "/api/v1/clusters/:cluster_id/kubernetes/resources" &&
				c.Query("group") == "" && c.Query("version") == "v1" &&
				c.Query("resource") == "namespaces")) {
		namespace = namespaceCreateTarget(c)
		namespaceObjectTarget = true
	}
	if namespace == "" {
		return ""
	}
	if agentNamespace != "" && namespace == agentNamespace {
		return rbac.PermissionClusterAgentNamespaceManage
	}
	if strings.HasPrefix(namespace, "kube-") ||
		(namespace == "default" && (namespaceObjectTarget || isNamespaceLifecycleRoute(c) ||
			(c.Request.Method == http.MethodPost &&
				c.FullPath() == "/api/v1/clusters/:cluster_id/namespaces"))) {
		return rbac.PermissionClusterSystemNamespaceManage
	}
	return ""
}

const namespaceCreateTargetContextKey = "zke.namespace_create_target"

func namespaceCreateTarget(c *gin.Context) string {
	if cached, exists := c.Get(namespaceCreateTargetContextKey); exists {
		value, _ := cached.(string)
		return value
	}
	if c.Request.Body == nil {
		c.Set(namespaceCreateTargetContextKey, "")
		return ""
	}
	// The Namespace create handler has the same 64 KiB ceiling. Restore the body
	// exactly so this authorization look-ahead is invisible to normal decoding.
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 64*1024+1))
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) > 64*1024 {
		c.Set(namespaceCreateTargetContextKey, "")
		return ""
	}
	var request struct {
		Name     string `json:"name"`
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &request) != nil {
		request.Name = ""
	}
	if request.Name == "" {
		request.Name = request.Metadata.Name
	}
	c.Set(namespaceCreateTargetContextKey, request.Name)
	return request.Name
}

func isNamespaceLifecycleRoute(c *gin.Context) bool {
	return c.FullPath() == "/api/v1/clusters/:cluster_id/namespaces/:namespace_name" &&
		c.Request.Method != http.MethodGet
}

func protectedNamespaceRequestNeedsAdditionalGate(c *gin.Context) bool {
	path := c.FullPath()
	if c.Request.Method != http.MethodGet {
		// A release write is a Secret write — Helm's storage is a Secret — and
		// it writes every object the chart renders into the same Namespace. So
		// installing into `kube-system` or the Agent's own Namespace needs the
		// same additional grant that writing a Secret there needs.
		return strings.Contains(path, "/secrets") ||
			strings.Contains(path, "/helm-releases") ||
			strings.Contains(path, "/authorization") ||
			strings.Contains(path, "/terminal-sessions") ||
			strings.Contains(path, "/access-sessions")
	}
	// Reading a Secret retains cluster.secret.read and additionally requires the
	// protected namespace grant. Other reads remain ordinary cluster.read.
	//
	// A Helm release is a Secret of type `helm.sh/release.v1` reached under a
	// name of its own, so the gate follows what is read rather than what the
	// path is called: reading a release in `kube-system` is reading a Secret in
	// `kube-system`, and a route name must not be a way around that.
	return strings.Contains(path, "/secrets") ||
		strings.Contains(path, "/helm-releases") ||
		strings.Contains(path, "/terminal-sessions")
}

func (authorization *Authorization) require(
	permission rbac.Permission,
	scopeType string,
	scopeParameter string,
	check func(context.Context, string, *gin.Context) (rbac.ResolvedScope, rbac.Permission, error),
) gin.HandlerFunc {
	return func(c *gin.Context) {
		identity, exists := Identity(c)
		if !exists {
			writeError(c, http.StatusUnauthorized, "unauthenticated", "authentication required")
			c.Abort()
			return
		}

		operationContext, cancelOperation := context.WithTimeout(
			c.Request.Context(),
			authorization.config.OperationTimeout,
		)
		resolved, checkedPermission, err := check(operationContext, identity.User.ID, c)
		cancelOperation()
		if checkedPermission == "" {
			checkedPermission = permission
		}
		if err == nil {
			// The check already resolved which Tenant and Project own the
			// target. Keeping it means logs and audit records downstream carry
			// the full scope without paying for the same lookup again.
			setResolvedScope(c, resolved)
			c.Next()
			return
		}
		if errors.Is(err, rbac.ErrDenied) {
			authorization.recordDenied(
				c,
				identity.User.ID,
				checkedPermission,
				permission,
				scopeType,
				scopeParameter,
			)
			authorization.logger.Warn(
				"HTTP authorization denied",
				authorization.logAttributes(
					c,
					identity.User.ID,
					checkedPermission,
					scopeType,
					scopeParameter,
				)...,
			)
			writeError(c, http.StatusForbidden, "forbidden", "permission denied")
			c.Abort()
			return
		}
		if errors.Is(err, rbac.ErrInvalidScope) {
			writeError(c, http.StatusBadRequest, "invalid_request", "invalid resource scope")
			c.Abort()
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			authorization.logger.Warn(
				"HTTP authorization timed out",
				authorization.logAttributes(
					c,
					identity.User.ID,
					checkedPermission,
					scopeType,
					scopeParameter,
				)...,
			)
			writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
			c.Abort()
			return
		}

		attributes := authorization.logAttributes(
			c,
			identity.User.ID,
			checkedPermission,
			scopeType,
			scopeParameter,
		)
		attributes = append(attributes, slog.String("error", err.Error()))
		authorization.logger.Error("authorize HTTP request", attributes...)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
		c.Abort()
	}
}

// recordDenied writes the refusal. `permission` is the one actually checked and
// becomes the event's action; `requested` is the one the route declared, which
// can differ because effectiveClusterPermission substitutes a stricter
// permission inside protected namespaces. Only the checked one would otherwise
// survive, and a reader would see a denial for a permission the caller never
// asked for -- so the requested one is recorded alongside it when they diverge.
func (authorization *Authorization) recordDenied(
	c *gin.Context,
	userID string,
	permission rbac.Permission,
	requested rbac.Permission,
	scopeType string,
	scopeParameter string,
) {
	if authorization.auditService == nil {
		return
	}
	actorIP := c.ClientIP()
	detail := map[string]string{"scope_type": scopeType}
	if requested != "" && requested != permission {
		detail["requested_permission"] = string(requested)
	}
	// Detached from the request: a denial must be recorded even when the caller
	// hangs up before the write lands, or the party being audited decides which
	// refusals get a record.
	auditContext, cancelAudit := auditctx.Detach(
		c.Request.Context(),
		authorization.config.OperationTimeout,
	)
	var err error
	targetType := permissionTargetType(permission)
	targetID := c.Param(targetType + "_id")
	switch scopeType {
	case "global":
		err = authorization.auditService.RecordGlobalEvent(
			auditContext,
			audit.GlobalEventInput{
				ActorUserID: userID,
				Action:      string(permission),
				TargetType:  targetType,
				TargetID:    targetID,
				Result:      "denied",
				RequestID:   RequestID(c),
				ActorIP:     actorIP,
				Detail:      detail,
			},
		)
	case "tenant":
		err = authorization.auditService.RecordTenantEvent(
			auditContext,
			audit.TenantEventInput{
				ActorUserID: userID,
				TenantID:    c.Param(scopeParameter),
				Action:      string(permission),
				TargetType:  targetType,
				TargetID:    targetID,
				Result:      "denied",
				RequestID:   RequestID(c),
				ActorIP:     actorIP,
				Detail:      detail,
			},
		)
	case "project":
		err = authorization.auditService.RecordProjectEvent(
			auditContext,
			audit.ProjectEventInput{
				ActorUserID: userID,
				ProjectID:   c.Param(scopeParameter),
				Action:      string(permission),
				TargetType:  targetType,
				TargetID:    targetID,
				Result:      "denied",
				RequestID:   RequestID(c),
				ActorIP:     actorIP,
				Detail:      detail,
			},
		)
	case "cluster":
		err = authorization.auditService.RecordClusterEvent(
			auditContext,
			audit.ClusterEventInput{
				ActorUserID: userID,
				ClusterID:   c.Param(scopeParameter),
				Action:      string(permission),
				TargetType:  targetType,
				TargetID:    targetID,
				Result:      "denied",
				RequestID:   RequestID(c),
				ActorIP:     actorIP,
				Detail:      detail,
			},
		)
	case "agent":
		err = authorization.auditService.RecordAgentEvent(
			auditContext,
			audit.AgentEventInput{
				ActorUserID: userID,
				AgentID:     c.Param(scopeParameter),
				Action:      string(permission),
				Result:      "denied",
				RequestID:   RequestID(c),
				ActorIP:     actorIP,
				Detail:      detail,
			},
		)
	}
	cancelAudit()
	if err != nil {
		attributes := authorization.logAttributes(
			c,
			userID,
			permission,
			scopeType,
			scopeParameter,
		)
		attributes = append(attributes, slog.String("error", err.Error()))
		authorization.logger.Error("record HTTP authorization denial audit", attributes...)
	}
}

// permissionTargetType names the kind of object a refused permission guards.
//
// Declared, not split off the permission name. The first segment is not the
// target: `rbac.manage` guards RoleBindings and `audit.read` guards audit
// events, so splitting produced the target types `rbac` and `audit`, which name
// no object the audit trail holds and appear in no published vocabulary.
func permissionTargetType(permission rbac.Permission) string {
	switch permission {
	case rbac.PermissionTenantCreate,
		rbac.PermissionTenantRead,
		rbac.PermissionTenantManage:
		return auditaction.TargetTenant
	case rbac.PermissionProjectCreate,
		rbac.PermissionProjectRead,
		rbac.PermissionProjectManage:
		return auditaction.TargetProject
	case rbac.PermissionApplicationManage:
		return auditaction.TargetCustomApplication
	case rbac.PermissionClusterRead,
		rbac.PermissionClusterPodLogsRead,
		rbac.PermissionClusterPodExec,
		rbac.PermissionClusterPodTerminalRecordingCreate,
		rbac.PermissionClusterPodTerminalRecordingRead,
		rbac.PermissionClusterPodPortForward,
		rbac.PermissionClusterEventRead,
		rbac.PermissionClusterManage,
		rbac.PermissionClusterConnectionRevoke:
		return auditaction.TargetCluster
	case rbac.PermissionClusterNamespaceManage,
		rbac.PermissionClusterSystemNamespaceManage,
		rbac.PermissionClusterAgentNamespaceManage,
		rbac.PermissionClusterNodeManage,
		rbac.PermissionClusterNodeDrain,
		rbac.PermissionClusterResourceCreate,
		rbac.PermissionClusterResourceUpdate,
		rbac.PermissionClusterResourceDelete:
		return auditaction.TargetKubernetesResource
	case rbac.PermissionClusterEnrollmentCreate,
		rbac.PermissionClusterEnrollmentRead,
		rbac.PermissionClusterEnrollmentRevoke:
		return auditaction.TargetEnrollment
	case rbac.PermissionUserRead,
		rbac.PermissionUserManage,
		rbac.PermissionUserPasswordChange:
		return auditaction.TargetUser
	case rbac.PermissionRBACRead, rbac.PermissionRBACManage:
		return auditaction.TargetRoleBinding
	case rbac.PermissionAuditRead:
		return auditaction.TargetAuditEvent
	default:
		// Unreachable while every permission is mapped, which
		// TestPermissionTargetTypesArePublished asserts. Falling back to the
		// least specific real target keeps an unmapped permission inside the
		// published vocabulary instead of inventing a value for it.
		return auditaction.TargetUser
	}
}

func (authorization *Authorization) logAttributes(
	c *gin.Context,
	userID string,
	permission rbac.Permission,
	scopeType string,
	scopeParameter string,
) []any {
	attributes := []any{
		slog.String("request_id", RequestID(c)),
		slog.String("user_id", userID),
		slog.String("permission", string(permission)),
		slog.String("scope_type", scopeType),
	}
	if scopeParameter != "" {
		attributes = append(
			attributes,
			slog.String(scopeParameter, c.Param(scopeParameter)),
		)
	}
	return attributes
}
