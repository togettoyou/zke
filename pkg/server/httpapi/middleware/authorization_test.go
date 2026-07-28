package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

func TestAuthorizationRequiresAuthenticatedIdentity(t *testing.T) {
	t.Parallel()

	router := gin.New()
	authorization := NewAuthorization(
		discardAuthorizationLogger(),
		rbac.NewService(nil),
		nil,
		AuthorizationConfig{OperationTimeout: time.Second},
	)
	router.GET(
		"/global",
		authorization.RequireGlobal(rbac.PermissionClusterRead),
		func(c *gin.Context) {
			c.Status(http.StatusNoContent)
		},
	)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/global", nil)
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestAuthorizationRejectsInvalidProjectIDBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(authIdentityKey, auth.Identity{
			User: auth.User{ID: "00000000-0000-0000-0000-000000000001"},
		})
		c.Next()
	})
	authorization := NewAuthorization(
		discardAuthorizationLogger(),
		nil,
		nil,
		AuthorizationConfig{OperationTimeout: time.Second},
	)
	router.GET(
		"/projects/:project_id",
		authorization.RequireProject(rbac.PermissionClusterRead, "project_id"),
		func(c *gin.Context) {
			c.Status(http.StatusNoContent)
		},
	)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/projects/not-a-uuid", nil)
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func discardAuthorizationLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// A denial names the object the permission guards, and that name reaches the
// audit trail's `target_type` filter. Every permission must therefore map to a
// published target type — an unmapped one would record a value the Console
// cannot offer, which is how the previous name-splitting version produced the
// non-existent target types `rbac` and `audit`.
func TestPermissionTargetTypesArePublished(t *testing.T) {
	t.Parallel()

	for _, permission := range rbac.Permissions() {
		targetType := permissionTargetType(permission)
		if !auditaction.KnownTargetType(targetType) {
			t.Errorf(
				"permission %q maps to target type %q, which is not published",
				permission,
				targetType,
			)
		}
	}
}
