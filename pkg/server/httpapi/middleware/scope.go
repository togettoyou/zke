package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

const resolvedScopeKey = "authorized_resource_scope"

// RoleBindingCache installs a request-scoped memo for RoleBinding lookups.
//
// It belongs on short request handling only. A long-lived stream must not use
// it: the stream re-resolves the caller's visibility periodically precisely so
// that a withdrawn RoleBinding ends it, and a memo with no expiry would defeat
// that.
func RoleBindingCache() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(
			rbac.WithBindingCache(c.Request.Context()),
		)
		c.Next()
	}
}

func setResolvedScope(c *gin.Context, scope rbac.ResolvedScope) {
	if scope.TenantID == "" && scope.ProjectID == "" {
		return
	}
	c.Set(resolvedScopeKey, scope)
}

// ResolvedScope reports the Tenant, Project and per-Cluster runtime scope the
// authorization middleware resolved this request's target to, if the route
// authorized a scoped permission.
func ResolvedScope(c *gin.Context) (rbac.ResolvedScope, bool) {
	value, exists := c.Get(resolvedScopeKey)
	if !exists {
		return rbac.ResolvedScope{}, false
	}
	scope, valid := value.(rbac.ResolvedScope)
	return scope, valid
}
