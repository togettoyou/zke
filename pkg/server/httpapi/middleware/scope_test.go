package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

// A Cluster route names only the Cluster in its path. The Tenant and Project
// the authorization check already resolved must reach the log line, otherwise
// a Cluster operation is recorded with no tenant to attribute it to.
func TestScopeAttributesIncludeTheResolvedScope(t *testing.T) {
	t.Parallel()

	const (
		userID    = "00000000-0000-0000-0000-000000000001"
		tenantID  = "00000000-0000-0000-0000-0000000000a1"
		projectID = "00000000-0000-0000-0000-0000000000b1"
		clusterID = "00000000-0000-0000-0000-0000000000c1"
	)

	var attributes []any
	router := gin.New()
	router.GET("/clusters/:cluster_id", func(c *gin.Context) {
		c.Set(authIdentityKey, auth.Identity{User: auth.User{ID: userID}})
		setResolvedScope(c, rbac.ResolvedScope{
			TenantID:  tenantID,
			ProjectID: projectID,
		})
		attributes = ScopeAttributes(c)
		c.Status(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/clusters/"+clusterID, nil),
	)

	found := map[string]string{}
	for _, attribute := range attributes {
		typed, ok := attribute.(slog.Attr)
		if !ok {
			t.Fatalf("scope attribute %v is not an slog.Attr", attribute)
		}
		found[typed.Key] = typed.Value.String()
	}
	for key, want := range map[string]string{
		"actor_user_id": userID,
		"tenant_id":     tenantID,
		"project_id":    projectID,
		"cluster_id":    clusterID,
	} {
		if found[key] != want {
			t.Errorf("scope attribute %s = %q, want %q", key, found[key], want)
		}
	}
}

// A route that authorizes nothing scoped must not invent a scope.
func TestScopeAttributesOmitAnUnresolvedScope(t *testing.T) {
	t.Parallel()

	router := gin.New()
	var attributes []any
	router.GET("/users", func(c *gin.Context) {
		attributes = ScopeAttributes(c)
		c.Status(http.StatusNoContent)
	})
	router.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/users", nil),
	)
	if len(attributes) != 0 {
		t.Fatalf("scope attributes = %v, want none", attributes)
	}
}

// The memo has to travel on the request context, because handlers derive their
// own timeout context from it before querying. Replacing c.Request is what
// makes that work; keeping the value only on the gin context would not.
func TestRoleBindingCacheReplacesTheRequestContext(t *testing.T) {
	t.Parallel()

	router := gin.New()
	var before, after context.Context
	router.Use(func(c *gin.Context) {
		before = c.Request.Context()
		c.Next()
	})
	router.Use(RoleBindingCache())
	router.GET("/users", func(c *gin.Context) {
		after = c.Request.Context()
		c.Status(http.StatusNoContent)
	})
	router.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/users", nil),
	)
	if before == nil || after == nil || before == after {
		t.Fatal("role binding cache did not replace the request context")
	}
}
