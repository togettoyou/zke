package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/rbac"
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
	) (rbac.ResolvedScope, error) {
		return rbac.ResolvedScope{}, authorization.service.AuthorizeGlobal(
			ctx,
			userID,
			permission,
		)
	})
}

func (authorization *Authorization) RequireTenant(
	permission rbac.Permission,
	tenantParameter string,
) gin.HandlerFunc {
	return authorization.require(permission, "tenant", tenantParameter, func(
		ctx context.Context,
		userID string,
		c *gin.Context,
	) (rbac.ResolvedScope, error) {
		tenantID := c.Param(tenantParameter)
		return rbac.ResolvedScope{TenantID: tenantID},
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
	) (rbac.ResolvedScope, error) {
		return authorization.service.AuthorizeProject(
			ctx,
			userID,
			permission,
			c.Param(projectParameter),
		)
	})
}

func (authorization *Authorization) RequireCluster(
	permission rbac.Permission,
	clusterParameter string,
) gin.HandlerFunc {
	return authorization.require(permission, "cluster", clusterParameter, func(
		ctx context.Context,
		userID string,
		c *gin.Context,
	) (rbac.ResolvedScope, error) {
		return authorization.service.AuthorizeCluster(
			ctx,
			userID,
			permission,
			c.Param(clusterParameter),
		)
	})
}

func (authorization *Authorization) require(
	permission rbac.Permission,
	scopeType string,
	scopeParameter string,
	check func(context.Context, string, *gin.Context) (rbac.ResolvedScope, error),
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
		resolved, err := check(operationContext, identity.User.ID, c)
		cancelOperation()
		if err == nil {
			// The check already resolved which Tenant and Project own the
			// target. Keeping it means logs and audit records downstream carry
			// the full scope without paying for the same lookup again.
			setResolvedScope(c, resolved)
			c.Next()
			return
		}
		if errors.Is(err, rbac.ErrDenied) {
			authorization.recordDenied(c, identity.User.ID, permission, scopeType, scopeParameter)
			authorization.logger.Warn(
				"HTTP authorization denied",
				authorization.logAttributes(
					c,
					identity.User.ID,
					permission,
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
					permission,
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
			permission,
			scopeType,
			scopeParameter,
		)
		attributes = append(attributes, slog.String("error", err.Error()))
		authorization.logger.Error("authorize HTTP request", attributes...)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
		c.Abort()
	}
}

func (authorization *Authorization) recordDenied(
	c *gin.Context,
	userID string,
	permission rbac.Permission,
	scopeType string,
	scopeParameter string,
) {
	if authorization.auditService == nil {
		return
	}
	auditContext, cancelAudit := context.WithTimeout(
		c.Request.Context(),
		authorization.config.OperationTimeout,
	)
	var err error
	switch scopeType {
	case "global":
		err = authorization.auditService.RecordGlobalEvent(
			auditContext,
			audit.GlobalEventInput{
				ActorUserID: userID,
				Action:      string(permission),
				TargetType:  permissionTargetType(permission),
				Result:      "denied",
				RequestID:   RequestID(c),
			},
		)
	case "tenant":
		err = authorization.auditService.RecordTenantEvent(
			auditContext,
			audit.TenantEventInput{
				ActorUserID: userID,
				TenantID:    c.Param(scopeParameter),
				Action:      string(permission),
				TargetType:  permissionTargetType(permission),
				Result:      "denied",
				RequestID:   RequestID(c),
			},
		)
	case "project":
		err = authorization.auditService.RecordProjectEvent(
			auditContext,
			audit.ProjectEventInput{
				ActorUserID: userID,
				ProjectID:   c.Param(scopeParameter),
				Action:      string(permission),
				Result:      "denied",
				RequestID:   RequestID(c),
			},
		)
	case "cluster":
		err = authorization.auditService.RecordClusterEvent(
			auditContext,
			audit.ClusterEventInput{
				ActorUserID: userID,
				ClusterID:   c.Param(scopeParameter),
				Action:      string(permission),
				Result:      "denied",
				RequestID:   RequestID(c),
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

func permissionTargetType(permission rbac.Permission) string {
	value := string(permission)
	for index, character := range value {
		if character == '.' {
			return value[:index]
		}
	}
	return "resource"
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
