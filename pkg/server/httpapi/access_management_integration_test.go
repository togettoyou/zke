package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/accessmanagement"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func TestAccessManagementHTTPFlow(t *testing.T) {
	databaseURL := requireHTTPTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openHTTPTestDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	adminPassword := []byte("a sufficiently long access administrator passphrase")
	admin, err := auth.CreateInitialAdmin(
		ctx,
		store.NewAuthStore(pool),
		auth.InitialAdminInput{
			Username:    "access-admin",
			DisplayName: "Access Administrator",
			Password:    adminPassword,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(store.NewAuthStore(pool), auth.ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
		MaxFailedLoginAttempts:      3,
		AccountLockDuration:         time.Hour,
	})
	adminLogin, err := authService.Login(ctx, auth.LoginInput{
		Username:  admin.Username,
		Password:  adminPassword,
		RequestID: "request-access-admin-login",
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	rbacService := rbac.NewService(store.NewRBACStore(pool))
	auditService := audit.NewService(store.NewAuditStore(pool), rbacService)
	router := New(
		discardLogger(),
		Dependencies{
			ReadinessCheck: pool.Ping,
			AuthService:    authService,
			AuditService:   auditService,
			RBACService:    rbacService,
			AccessManagementService: accessmanagement.NewService(
				store.NewAccessManagementStore(pool),
				accessmanagement.Config{MaxConcurrentPasswordHashes: 1},
			),
		},
		Config{Authentication: defaultAuthenticationTestConfig()},
	)

	userPassword := "a sufficiently long managed user password"
	created := accessAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/users",
		`{"username":"Managed-User","display_name":"Managed User","password":"`+
			userPassword+`"}`,
		adminLogin,
	)
	if created.Code != http.StatusCreated {
		t.Fatalf("create user status = %d: %s", created.Code, created.Body)
	}
	var managed managedUserResponse
	if err := json.Unmarshal(created.Body.Bytes(), &managed); err != nil {
		t.Fatal(err)
	}
	if managed.ID == "" || managed.Username != "managed-user" ||
		managed.Status != "active" {
		t.Fatalf("unexpected managed user: %+v", managed)
	}
	assertUTC8Time(t, "managed user created_at", managed.CreatedAt)

	duplicate := accessAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/users",
		`{"username":"managed-user","display_name":"Duplicate","password":"`+
			userPassword+`"}`,
		adminLogin,
	)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate user status = %d: %s", duplicate.Code, duplicate.Body)
	}

	userLogin, err := authService.Login(ctx, auth.LoginInput{
		Username:  managed.Username,
		Password:  []byte(userPassword),
		RequestID: "request-managed-login",
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	deniedUsers := accessAPIRequest(
		router, http.MethodGet, "/api/v1/users", "", userLogin,
	)
	if deniedUsers.Code != http.StatusForbidden {
		t.Fatalf("unbound user list status = %d: %s", deniedUsers.Code, deniedUsers.Body)
	}
	deniedAudit := accessAPIRequest(
		router, http.MethodGet, "/api/v1/audit-events", "", userLogin,
	)
	if deniedAudit.Code != http.StatusForbidden {
		t.Fatalf("unbound audit status = %d: %s", deniedAudit.Code, deniedAudit.Body)
	}

	createdBinding := accessAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/role-bindings",
		`{"subject_id":"`+managed.ID+`","role":"viewer","scope_type":"global","confirm":true}`,
		adminLogin,
	)
	if createdBinding.Code != http.StatusCreated {
		t.Fatalf("create role binding status = %d: %s", createdBinding.Code, createdBinding.Body)
	}
	var binding roleBindingResponse
	if err := json.Unmarshal(createdBinding.Body.Bytes(), &binding); err != nil {
		t.Fatal(err)
	}
	replayedBinding := accessAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/role-bindings",
		`{"subject_id":"`+managed.ID+`","role":"viewer","scope_type":"global","confirm":true}`,
		adminLogin,
	)
	if replayedBinding.Code != http.StatusOK {
		t.Fatalf("replay role binding status = %d: %s", replayedBinding.Code, replayedBinding.Body)
	}
	var tenantID string
	if err := pool.QueryRow(ctx, `
INSERT INTO tenants (id, name, status)
VALUES (gen_random_uuid(), 'Scoped Audit Tenant', 'active')
RETURNING id::text
`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	tenantBindingResponse := accessAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/role-bindings",
		`{"subject_id":"`+managed.ID+`","role":"admin","scope_type":"tenant","tenant_id":"`+
			tenantID+`","confirm":true}`,
		adminLogin,
	)
	if tenantBindingResponse.Code != http.StatusCreated {
		t.Fatalf(
			"create tenant role binding status = %d: %s",
			tenantBindingResponse.Code,
			tenantBindingResponse.Body,
		)
	}
	scopedAudit := accessAPIRequest(
		router, http.MethodGet, "/api/v1/audit-events?limit=100", "", userLogin,
	)
	if scopedAudit.Code != http.StatusOK {
		t.Fatalf("scoped audit status = %d: %s", scopedAudit.Code, scopedAudit.Body)
	}
	var scopedAuditBody struct {
		Events []auditEventResponse `json:"audit_events"`
	}
	if err := json.Unmarshal(scopedAudit.Body.Bytes(), &scopedAuditBody); err != nil {
		t.Fatal(err)
	}
	if len(scopedAuditBody.Events) == 0 {
		t.Fatal("scoped audit query returned no tenant events")
	}
	for _, event := range scopedAuditBody.Events {
		if event.TenantID != tenantID || event.ScopeType == "global" {
			t.Fatalf("scoped audit leaked an event: %+v", event)
		}
	}

	for attempt := 1; attempt <= 3; attempt++ {
		_, err := authService.Login(ctx, auth.LoginInput{
			Username:  managed.Username,
			Password:  []byte("an intentionally incorrect password"),
			RequestID: "request-managed-failure-" + string(rune('0'+attempt)),
			Now:       time.Now().UTC().Add(time.Duration(attempt) * time.Millisecond),
		})
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("failed login %d error = %v", attempt, err)
		}
	}
	var lockedStatus string
	var failedCount int
	if err := pool.QueryRow(ctx, `
SELECT status, failed_login_count
FROM users
WHERE id = $1
`, managed.ID).Scan(&lockedStatus, &failedCount); err != nil {
		t.Fatal(err)
	}
	if lockedStatus != "locked" || failedCount != 3 {
		t.Fatalf("locked user state = %s/%d, want locked/3", lockedStatus, failedCount)
	}
	if _, err := authService.Authenticate(
		ctx,
		userLogin.SessionToken,
		time.Now().UTC(),
	); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("locked user session error = %v, want unauthenticated", err)
	}

	unlocked := accessAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/users/"+managed.ID+"/unlock",
		`{"confirm":true}`,
		adminLogin,
	)
	if unlocked.Code != http.StatusOK {
		t.Fatalf("unlock user status = %d: %s", unlocked.Code, unlocked.Body)
	}
	if _, err := authService.Login(ctx, auth.LoginInput{
		Username:  managed.Username,
		Password:  []byte(userPassword),
		RequestID: "request-managed-after-unlock",
		Now:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("login after unlock: %v", err)
	}

	newPassword := "a different sufficiently long managed password"
	reset := accessAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/users/"+managed.ID+"/password-reset",
		`{"password":"`+newPassword+`","confirm":true}`,
		adminLogin,
	)
	if reset.Code != http.StatusOK {
		t.Fatalf("reset password status = %d: %s", reset.Code, reset.Body)
	}
	if _, err := authService.Login(ctx, auth.LoginInput{
		Username:  managed.Username,
		Password:  []byte(userPassword),
		RequestID: "request-managed-old-password",
		Now:       time.Now().UTC(),
	}); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("old password error = %v, want invalid credentials", err)
	}
	newPasswordLogin, err := authService.Login(ctx, auth.LoginInput{
		Username:  managed.Username,
		Password:  []byte(newPassword),
		RequestID: "request-managed-new-password",
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("new password login: %v", err)
	}
	disabled := accessAPIRequest(
		router,
		http.MethodPut,
		"/api/v1/users/"+managed.ID+"/status",
		`{"status":"disabled","confirm":true}`,
		adminLogin,
	)
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable user status = %d: %s", disabled.Code, disabled.Body)
	}
	if _, err := authService.Authenticate(
		ctx,
		newPasswordLogin.SessionToken,
		time.Now().UTC(),
	); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("disabled user session error = %v, want unauthenticated", err)
	}
	enabled := accessAPIRequest(
		router,
		http.MethodPut,
		"/api/v1/users/"+managed.ID+"/status",
		`{"status":"active","confirm":true}`,
		adminLogin,
	)
	if enabled.Code != http.StatusOK {
		t.Fatalf("enable user status = %d: %s", enabled.Code, enabled.Body)
	}

	auditResult := accessAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/audit-events?action=user.password.reset&limit=10",
		"",
		adminLogin,
	)
	if auditResult.Code != http.StatusOK {
		t.Fatalf("audit query status = %d: %s", auditResult.Code, auditResult.Body)
	}
	var auditBody struct {
		Events []auditEventResponse `json:"audit_events"`
	}
	if err := json.Unmarshal(auditResult.Body.Bytes(), &auditBody); err != nil {
		t.Fatal(err)
	}
	if len(auditBody.Events) != 1 ||
		auditBody.Events[0].TargetID != managed.ID {
		t.Fatalf("unexpected password reset audit: %+v", auditBody.Events)
	}
	firstPageResponse := accessAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/audit-events?limit=1",
		"",
		adminLogin,
	)
	if firstPageResponse.Code != http.StatusOK {
		t.Fatalf("first audit page status = %d: %s", firstPageResponse.Code, firstPageResponse.Body)
	}
	var firstPage struct {
		Events     []auditEventResponse `json:"audit_events"`
		NextCursor string               `json:"next_cursor"`
	}
	if err := json.Unmarshal(firstPageResponse.Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Events) != 1 || firstPage.NextCursor == "" {
		t.Fatalf("unexpected first audit page: %+v", firstPage)
	}
	secondPageResponse := accessAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/audit-events?limit=1&cursor="+url.QueryEscape(firstPage.NextCursor),
		"",
		adminLogin,
	)
	if secondPageResponse.Code != http.StatusOK {
		t.Fatalf("second audit page status = %d: %s", secondPageResponse.Code, secondPageResponse.Body)
	}
	var secondPage struct {
		Events []auditEventResponse `json:"audit_events"`
	}
	if err := json.Unmarshal(secondPageResponse.Body.Bytes(), &secondPage); err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Events) != 1 ||
		secondPage.Events[0].ID == firstPage.Events[0].ID {
		t.Fatalf("unexpected second audit page: %+v", secondPage)
	}

	deletedBinding := accessAPIRequest(
		router,
		http.MethodDelete,
		"/api/v1/role-bindings/"+binding.ID,
		`{"confirm":true}`,
		adminLogin,
	)
	if deletedBinding.Code != http.StatusNoContent {
		t.Fatalf("delete role binding status = %d: %s", deletedBinding.Code, deletedBinding.Body)
	}
	selfDisable := accessAPIRequest(
		router,
		http.MethodPut,
		"/api/v1/users/"+admin.ID+"/status",
		`{"status":"disabled","confirm":true}`,
		adminLogin,
	)
	if selfDisable.Code != http.StatusConflict {
		t.Fatalf("self disable status = %d: %s", selfDisable.Code, selfDisable.Body)
	}
	assertErrorCode(t, selfDisable, "self_disable_forbidden")

	var adminBindingID string
	if err := pool.QueryRow(ctx, `
SELECT id::text
FROM role_bindings
WHERE subject_id = $1 AND role = 'admin' AND scope_type = 'global'
`, admin.ID).Scan(&adminBindingID); err != nil {
		t.Fatal(err)
	}
	lastAdmin := accessAPIRequest(
		router,
		http.MethodDelete,
		"/api/v1/role-bindings/"+adminBindingID,
		`{"confirm":true}`,
		adminLogin,
	)
	if lastAdmin.Code != http.StatusConflict {
		t.Fatalf("last admin deletion status = %d: %s", lastAdmin.Code, lastAdmin.Body)
	}
	assertErrorCode(t, lastAdmin, "last_global_admin")
}

func accessAPIRequest(
	handler http.Handler,
	method string,
	path string,
	body string,
	login auth.LoginResult,
) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.AddCookie(&http.Cookie{
		Name:  sessionCookieName,
		Value: login.SessionToken,
	})
	if method != http.MethodGet {
		request.Header.Set(csrfHeaderName, login.CSRFToken)
	}
	handler.ServeHTTP(response, request)
	return response
}
