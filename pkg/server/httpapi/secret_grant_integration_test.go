package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
)

// The Secret grant a Kubernetes RBAC write is allowed to hand out.
//
// A PolicyRule mentioning `secrets` gives every subject bound to the role what
// `cluster.secret.read` gives its holder, so writing one requires holding it.
// The rule itself is unit-tested; what this covers is the wiring — that the
// middleware resolves the caller's real bindings on the target Cluster, and that
// holding `cluster.rbac.manage` alone resolves to no grant at all.
//
// Custom roles are what make the case expressible: before them, no role could
// carry `cluster.rbac.manage` without also carrying every Secret permission.
func TestResolveClusterSecretGrantReflectsTheCallersBindings(t *testing.T) {
	databaseURL := requireHTTPTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	pool := openHTTPTestDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	var tenantID, projectID, clusterID string
	if err := pool.QueryRow(ctx, `
INSERT INTO tenants (id, name, status)
VALUES (gen_random_uuid(), 'Grant Tenant', 'active')
RETURNING id::text
`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO projects (id, tenant_id, name, status)
VALUES (gen_random_uuid(), $1, 'Grant Project', 'active')
RETURNING id::text
`, tenantID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO clusters (id, tenant_id, project_id, name, status)
VALUES (gen_random_uuid(), $1, $2, 'grant-cluster', 'pending')
RETURNING id::text
`, tenantID, projectID).Scan(&clusterID); err != nil {
		t.Fatal(err)
	}

	// Two roles that differ only in whether they carry a Secret permission.
	if _, err := pool.Exec(ctx, `
INSERT INTO roles (id, name, display_name, builtin, permissions)
VALUES
    (gen_random_uuid(), 'rbac-only', 'RBAC 管理', false,
     ARRAY['cluster.rbac.read', 'cluster.rbac.manage']),
    (gen_random_uuid(), 'rbac-and-secret-read', 'RBAC 管理与 Secret 读取', false,
     ARRAY['cluster.rbac.read', 'cluster.rbac.manage', 'cluster.secret.read'])
`); err != nil {
		t.Fatal(err)
	}

	authService := auth.NewService(store.NewAuthStore(pool), auth.ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
	})
	login := func(username string, role string) auth.LoginResult {
		t.Helper()
		password := []byte("a sufficiently long grant test passphrase")
		passwordHash, err := auth.HashPassword(password, auth.DefaultPasswordParams())
		if err != nil {
			t.Fatal(err)
		}
		var userID string
		if err := pool.QueryRow(ctx, `
INSERT INTO users (
    id, username_normalized, display_name, password_hash, status, password_changed_at
)
VALUES (gen_random_uuid(), $1, $1, $2, 'active', now())
RETURNING id::text
`, username, passwordHash).Scan(&userID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type, tenant_id, project_id)
VALUES (gen_random_uuid(), $1, $2, 'project', $3, $4)
`, userID, role, tenantID, projectID); err != nil {
			t.Fatal(err)
		}
		result, err := authService.Login(ctx, auth.LoginInput{
			Username:  username,
			Password:  password,
			RequestID: "request-" + username,
			Now:       time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	rbacOnly := login("rbac-only-user", "rbac-only")
	secretReader := login("secret-reading-user", "rbac-and-secret-read")

	rbacService := rbac.NewService(store.NewRBACStore(pool))
	handler := New(
		discardLogger(),
		Dependencies{ReadinessCheck: pool.Ping, AuthService: authService},
		Config{Authentication: defaultAuthenticationTestConfig()},
	)
	router, ok := handler.(*gin.Engine)
	if !ok {
		t.Fatalf("handler type = %T, want *gin.Engine", handler)
	}
	authentication := httpmiddleware.NewAuthentication(
		discardLogger(),
		authService,
		httpmiddleware.AuthenticationConfig{OperationTimeout: 5 * time.Second},
	)
	authorization := httpmiddleware.NewAuthorization(
		discardLogger(),
		rbacService,
		audit.NewService(store.NewAuditStore(pool), rbacService),
		httpmiddleware.AuthorizationConfig{OperationTimeout: 5 * time.Second},
	)
	// The same composition the authorization write routes use.
	router.GET(
		"/test/clusters/:cluster_id/grant",
		authentication.RequireAuthentication,
		authorization.RequireCluster(rbac.PermissionClusterRBACManage, "cluster_id"),
		authorization.ResolveClusterSecretGrant("cluster_id"),
		func(c *gin.Context) {
			grant := httpmiddleware.ClusterSecretGrant(c)
			c.JSON(http.StatusOK, gin.H{"read": grant.Read, "manage": grant.Manage})
		},
	)

	request := func(login auth.LoginResult) (int, bool, bool) {
		t.Helper()
		response := httptest.NewRecorder()
		httpRequest := httptest.NewRequest(
			http.MethodGet, "/test/clusters/"+clusterID+"/grant", nil,
		)
		httpRequest.AddCookie(&http.Cookie{
			Name:  sessionCookieName,
			Value: login.SessionToken,
		})
		router.ServeHTTP(response, httpRequest)
		if response.Code != http.StatusOK {
			return response.Code, false, false
		}
		var body struct {
			Read   bool `json:"read"`
			Manage bool `json:"manage"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return response.Code, body.Read, body.Manage
	}

	// Holding cluster.rbac.manage is not holding anything about Secrets. The
	// request is served — the route's own permission is satisfied — and grants
	// nothing, so a rule mentioning Secrets would be refused.
	status, read, manage := request(rbacOnly)
	if status != http.StatusOK || read || manage {
		t.Fatalf("rbac-only grant: status = %d, read = %v, manage = %v", status, read, manage)
	}

	status, read, manage = request(secretReader)
	if status != http.StatusOK || !read || manage {
		t.Fatalf("secret reader grant: status = %d, read = %v, manage = %v", status, read, manage)
	}
}
