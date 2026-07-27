package httpapi

import (
	"context"
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
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func TestProjectAuthorizationMiddleware(t *testing.T) {
	databaseURL := requireHTTPTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openHTTPTestDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var tenantID, allowedProjectID, deniedProjectID string
	if err := pool.QueryRow(ctx, `
INSERT INTO tenants (id, name, status)
VALUES (gen_random_uuid(), 'Tenant A', 'active')
RETURNING id::text
`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO projects (id, tenant_id, name, status)
VALUES (gen_random_uuid(), $1, 'Allowed Project', 'active')
RETURNING id::text
`, tenantID).Scan(&allowedProjectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO projects (id, tenant_id, name, status)
VALUES (gen_random_uuid(), $1, 'Denied Project', 'active')
RETURNING id::text
`, tenantID).Scan(&deniedProjectID); err != nil {
		t.Fatal(err)
	}

	password := []byte("a sufficiently long viewer passphrase")
	passwordHash, err := auth.HashPassword(password, auth.DefaultPasswordParams())
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `
INSERT INTO users (
    id,
    username_normalized,
    display_name,
    password_hash,
    status,
    password_changed_at
)
VALUES (
    gen_random_uuid(),
    'project-viewer',
    'Project Viewer',
    $1,
    'active',
    now()
)
RETURNING id::text
`, passwordHash).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (
    id,
    subject_id,
    role,
    scope_type,
    tenant_id,
    project_id
)
VALUES (gen_random_uuid(), $1, 'viewer', 'project', $2, $3)
`, userID, tenantID, allowedProjectID); err != nil {
		t.Fatal(err)
	}

	authService := auth.NewService(store.NewAuthStore(pool), auth.ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
	})
	login, err := authService.Login(ctx, auth.LoginInput{
		Username:  "project-viewer",
		Password:  password,
		RequestID: "request-project-viewer-login",
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	handler := New(
		discardLogger(),
		Dependencies{
			ReadinessCheck: pool.Ping,
			AuthService:    authService,
		},
		Config{
			Authentication: defaultAuthenticationTestConfig(),
		},
	)
	router, ok := handler.(*gin.Engine)
	if !ok {
		t.Fatalf("handler type = %T, want *gin.Engine", handler)
	}
	authentication := httpmiddleware.NewAuthentication(
		discardLogger(),
		authService,
		httpmiddleware.AuthenticationConfig{
			OperationTimeout: 5 * time.Second,
		},
	)
	authorization := httpmiddleware.NewAuthorization(
		discardLogger(),
		rbac.NewService(store.NewRBACStore(pool)),
		audit.NewService(store.NewAuditStore(pool), rbac.NewService(store.NewRBACStore(pool))),
		httpmiddleware.AuthorizationConfig{
			OperationTimeout: 5 * time.Second,
		},
	)
	router.GET(
		"/test/projects/:project_id/clusters",
		authentication.RequireAuthentication,
		authorization.RequireProject(rbac.PermissionClusterRead, "project_id"),
		func(c *gin.Context) {
			c.Status(http.StatusNoContent)
		},
	)
	router.POST(
		"/test/projects/:project_id/cluster-enrollments",
		authentication.RequireAuthentication,
		authorization.RequireProject(
			rbac.PermissionClusterEnrollmentCreate,
			"project_id",
		),
		func(c *gin.Context) {
			c.Status(http.StatusNoContent)
		},
	)
	timeoutAuthorization := httpmiddleware.NewAuthorization(
		discardLogger(),
		rbac.NewService(store.NewRBACStore(pool)),
		nil,
		httpmiddleware.AuthorizationConfig{OperationTimeout: 0},
	)
	router.GET(
		"/test/timeout/projects/:project_id/clusters",
		authentication.RequireAuthentication,
		timeoutAuthorization.RequireProject(
			rbac.PermissionClusterRead,
			"project_id",
		),
		func(c *gin.Context) {
			c.Status(http.StatusNoContent)
		},
	)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "project viewer reads bound project",
			method:     http.MethodGet,
			path:       "/test/projects/" + allowedProjectID + "/clusters",
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "project viewer cannot cross project",
			method:     http.MethodGet,
			path:       "/test/projects/" + deniedProjectID + "/clusters",
			wantStatus: http.StatusForbidden,
			wantCode:   "forbidden",
		},
		{
			name:       "project viewer cannot create enrollment",
			method:     http.MethodPost,
			path:       "/test/projects/" + allowedProjectID + "/cluster-enrollments",
			wantStatus: http.StatusForbidden,
			wantCode:   "forbidden",
		},
		{
			name:       "invalid project ID is rejected",
			method:     http.MethodGet,
			path:       "/test/projects/not-a-uuid/clusters",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "authorization timeout is reported",
			method:     http.MethodGet,
			path:       "/test/timeout/projects/" + allowedProjectID + "/clusters",
			wantStatus: http.StatusGatewayTimeout,
			wantCode:   "timeout",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, nil)
			request.AddCookie(&http.Cookie{
				Name:  sessionCookieName,
				Value: login.SessionToken,
			})
			router.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s",
					response.Code,
					test.wantStatus,
					response.Body,
				)
			}
			if test.wantCode != "" {
				assertErrorCode(t, response, test.wantCode)
			}
		})
	}

	var deniedAuditCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_events
WHERE actor_user_id = $1
  AND project_id = $2
  AND action = 'cluster.enrollment.create'
  AND result = 'denied'
`, userID, allowedProjectID).Scan(&deniedAuditCount); err != nil {
		t.Fatal(err)
	}
	if deniedAuditCount != 1 {
		t.Fatalf("authorization denial audit count = %d, want 1", deniedAuditCount)
	}
}
