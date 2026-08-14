package middleware

import (
	"context"
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
	"github.com/togettoyou/zke/pkg/server/store"
)

const (
	authorizationTestUserID    = "00000000-0000-4000-8000-000000000001"
	authorizationTestTenantID  = "00000000-0000-4000-8000-000000000002"
	authorizationTestProjectID = "00000000-0000-4000-8000-000000000003"
	authorizationTestClusterID = "00000000-0000-4000-8000-000000000004"
)

type authorizationStore struct {
	permissions []string
}

func (source authorizationStore) ListRoleBindings(context.Context, string) ([]store.RoleBinding, error) {
	return []store.RoleBinding{{
		ScopeType:   "project",
		TenantID:    authorizationTestTenantID,
		ProjectID:   authorizationTestProjectID,
		Permissions: source.permissions,
	}}, nil
}

func (authorizationStore) FindProjectTenant(context.Context, string) (string, error) {
	return authorizationTestTenantID, nil
}

func (authorizationStore) FindClusterAuthorizationScope(context.Context, string) (store.ClusterAuthorizationScope, error) {
	return store.ClusterAuthorizationScope{
		TenantID:       authorizationTestTenantID,
		ProjectID:      authorizationTestProjectID,
		AgentNamespace: "cluster-agent",
	}, nil
}

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

func TestProtectedNamespaceGateResolvesTheTargetClustersAgentNamespace(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name        string
		namespace   string
		permissions []string
		wantStatus  int
	}{
		{
			name:        "ordinary Namespace needs only Secret read",
			namespace:   "workloads",
			permissions: []string{string(rbac.PermissionClusterSecretRead)},
			wantStatus:  http.StatusNoContent,
		},
		{
			name:        "Cluster Agent Namespace needs its independent grant",
			namespace:   "cluster-agent",
			permissions: []string{string(rbac.PermissionClusterSecretRead)},
			wantStatus:  http.StatusForbidden,
		},
		{
			name:      "Cluster Agent Namespace accepts both grants",
			namespace: "cluster-agent",
			permissions: []string{
				string(rbac.PermissionClusterSecretRead),
				string(rbac.PermissionClusterAgentNamespaceManage),
			},
			wantStatus: http.StatusNoContent,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(authIdentityKey, auth.Identity{User: auth.User{ID: authorizationTestUserID}})
				c.Next()
			})
			authorization := NewAuthorization(
				discardAuthorizationLogger(),
				rbac.NewService(authorizationStore{permissions: testCase.permissions}),
				nil,
				AuthorizationConfig{OperationTimeout: time.Second},
			)
			router.Use(authorization.RequireProtectedNamespaceAccess("cluster_id"))
			router.GET(
				"/clusters/:cluster_id/namespaces/:namespace_name/secrets/:secret_name",
				authorization.RequireCluster(rbac.PermissionClusterSecretRead, "cluster_id"),
				func(c *gin.Context) { c.Status(http.StatusNoContent) },
			)

			response := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodGet,
				"/clusters/"+authorizationTestClusterID+"/namespaces/"+testCase.namespace+"/secrets/credential",
				nil,
			)
			router.ServeHTTP(response, request)
			if response.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, testCase.wantStatus, response.Body)
			}
		})
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
