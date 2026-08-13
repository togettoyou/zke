package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/accessmanagement"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
)

func TestAccessManagementHTTPFlow(t *testing.T) {
	databaseURL := requireHTTPTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openHTTPTestDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	adminPassword := []byte("a sufficiently long access administrator passphrase")
	admin, err := auth.CreateFirstGlobalAdministrator(
		ctx,
		store.NewAuthStore(pool),
		auth.FirstGlobalAdministratorInput{
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
			).WithPermissionAuthority(rbacService),
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
	if err := decodeSuccessResponse(created, &managed); err != nil {
		t.Fatal(err)
	}
	if managed.ID == "" || managed.Username != "managed-user" ||
		managed.Status != "active" {
		t.Fatalf("unexpected managed user: %+v", managed)
	}
	assertUTC8Time(t, "managed user created_at", managed.CreatedAt)

	updatedUser := accessAPIRequest(
		router,
		http.MethodPut,
		"/api/v1/users/"+managed.ID,
		`{"display_name":"Updated Managed User"}`,
		adminLogin,
	)
	if updatedUser.Code != http.StatusOK {
		t.Fatalf("update user status = %d: %s", updatedUser.Code, updatedUser.Body)
	}
	if err := decodeSuccessResponse(updatedUser, &managed); err != nil {
		t.Fatal(err)
	}
	if managed.DisplayName != "Updated Managed User" {
		t.Fatalf("updated display name = %q", managed.DisplayName)
	}
	filteredUsers := accessAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/users?limit=1&offset=0&q=updated&status=active",
		"",
		adminLogin,
	)
	if filteredUsers.Code != http.StatusOK {
		t.Fatalf("filtered user list status = %d: %s", filteredUsers.Code, filteredUsers.Body)
	}
	var filteredUsersBody struct {
		Users      []managedUserResponse `json:"users"`
		Pagination listMetadata          `json:"pagination"`
	}
	if err := decodeSuccessResponse(filteredUsers, &filteredUsersBody); err != nil {
		t.Fatal(err)
	}
	if len(filteredUsersBody.Users) != 1 ||
		filteredUsersBody.Users[0].ID != managed.ID ||
		filteredUsersBody.Pagination.Total != 1 ||
		filteredUsersBody.Pagination.Limit != 1 ||
		filteredUsersBody.Pagination.HasMore {
		t.Fatalf("unexpected filtered user page: %+v", filteredUsersBody)
	}
	invalidUserQuery := accessAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/users?status=unknown",
		"",
		adminLogin,
	)
	if invalidUserQuery.Code != http.StatusBadRequest {
		t.Fatalf("invalid user query status = %d: %s", invalidUserQuery.Code, invalidUserQuery.Body)
	}
	userDetail := accessAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/users/"+managed.ID,
		"",
		adminLogin,
	)
	if userDetail.Code != http.StatusOK {
		t.Fatalf("get user status = %d: %s", userDetail.Code, userDetail.Body)
	}

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
	deniedPasswordChange := accessAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/auth/password",
		`{"current_password":"`+userPassword+`","new_password":"a replacement managed user password","confirm":true}`,
		userLogin,
	)
	if deniedPasswordChange.Code != http.StatusForbidden {
		t.Fatalf(
			"unbound password change status = %d: %s",
			deniedPasswordChange.Code,
			deniedPasswordChange.Body,
		)
	}
	assertErrorCode(t, deniedPasswordChange, "forbidden")

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
	if err := decodeSuccessResponse(createdBinding, &binding); err != nil {
		t.Fatal(err)
	}
	bindingDetail := accessAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/role-bindings/"+binding.ID,
		"",
		adminLogin,
	)
	if bindingDetail.Code != http.StatusOK {
		t.Fatalf("get role binding status = %d: %s", bindingDetail.Code, bindingDetail.Body)
	}
	var detailedBinding roleBindingResponse
	if err := decodeSuccessResponse(bindingDetail, &detailedBinding); err != nil {
		t.Fatal(err)
	}
	if detailedBinding.ID != binding.ID || detailedBinding.SubjectID != managed.ID {
		t.Fatalf("unexpected role binding detail: %+v", detailedBinding)
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
	viewerSession := accessAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/auth/me",
		"",
		userLogin,
	)
	if viewerSession.Code != http.StatusOK {
		t.Fatalf("viewer session status = %d: %s", viewerSession.Code, viewerSession.Body)
	}
	var viewerSessionBody currentSessionResponse
	if err := decodeSuccessResponse(viewerSession, &viewerSessionBody); err != nil {
		t.Fatal(err)
	}
	if len(viewerSessionBody.Capabilities) != 1 ||
		viewerSessionBody.Capabilities[0].Role != "viewer" ||
		viewerSessionBody.Capabilities[0].ScopeType != "global" ||
		!containsPermission(viewerSessionBody.Capabilities[0].Permissions, "tenant.read") ||
		containsPermission(viewerSessionBody.Capabilities[0].Permissions, "user.read") {
		t.Fatalf("unexpected viewer capabilities: %+v", viewerSessionBody.Capabilities)
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
	if err := decodeSuccessResponse(scopedAudit, &scopedAuditBody); err != nil {
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
	if err := decodeSuccessResponse(auditResult, &auditBody); err != nil {
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
		Pagination listMetadata         `json:"pagination"`
	}
	if err := decodeSuccessResponse(firstPageResponse, &firstPage); err != nil {
		t.Fatal(err)
	}
	// The total must describe every visible audit event, not the page, and a
	// first page of one out of many must report that more remain.
	if len(firstPage.Events) != 1 ||
		firstPage.Pagination.Total < 2 ||
		firstPage.Pagination.Offset != 0 ||
		!firstPage.Pagination.HasMore {
		t.Fatalf("unexpected first audit page: %+v", firstPage)
	}
	secondPageResponse := accessAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/audit-events?limit=1&offset=1",
		"",
		adminLogin,
	)
	if secondPageResponse.Code != http.StatusOK {
		t.Fatalf("second audit page status = %d: %s", secondPageResponse.Code, secondPageResponse.Body)
	}
	var secondPage struct {
		Events     []auditEventResponse `json:"audit_events"`
		Pagination listMetadata         `json:"pagination"`
	}
	if err := decodeSuccessResponse(secondPageResponse, &secondPage); err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Events) != 1 ||
		secondPage.Events[0].ID == firstPage.Events[0].ID {
		t.Fatalf("unexpected second audit page: %+v", secondPage)
	}
	if secondPage.Pagination.Offset != 1 ||
		secondPage.Pagination.Total != firstPage.Pagination.Total {
		t.Fatalf("unexpected second audit pagination: %+v", secondPage.Pagination)
	}
	// Paging past the end must still report the filtered total rather than
	// collapsing it to zero, which is what a window-count would do.
	beyondResponse := accessAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/audit-events?limit=1&offset=100000",
		"",
		adminLogin,
	)
	if beyondResponse.Code != http.StatusOK {
		t.Fatalf("beyond-end audit page status = %d: %s", beyondResponse.Code, beyondResponse.Body)
	}
	var beyondPage struct {
		Events     []auditEventResponse `json:"audit_events"`
		Pagination listMetadata         `json:"pagination"`
	}
	if err := decodeSuccessResponse(beyondResponse, &beyondPage); err != nil {
		t.Fatal(err)
	}
	if len(beyondPage.Events) != 0 ||
		beyondPage.Pagination.Total != firstPage.Pagination.Total ||
		beyondPage.Pagination.HasMore {
		t.Fatalf("unexpected beyond-end audit page: %+v", beyondPage)
	}

	deletedBinding := accessAPIRequest(
		router,
		http.MethodDelete,
		"/api/v1/role-bindings/"+binding.ID,
		`{"confirm":true}`,
		adminLogin,
	)
	if deletedBinding.Code != http.StatusOK {
		t.Fatalf("delete role binding status = %d: %s", deletedBinding.Code, deletedBinding.Body)
	}
	var deletedBindingData any
	if err := decodeSuccessResponse(deletedBinding, &deletedBindingData); err != nil {
		t.Fatal(err)
	}
	deletedUser := accessAPIRequest(
		router,
		http.MethodDelete,
		"/api/v1/users/"+managed.ID,
		`{"confirm":true}`,
		adminLogin,
	)
	if deletedUser.Code != http.StatusOK {
		t.Fatalf("delete user status = %d: %s", deletedUser.Code, deletedUser.Body)
	}
	var deletedManagedUser managedUserResponse
	if err := decodeSuccessResponse(deletedUser, &deletedManagedUser); err != nil {
		t.Fatal(err)
	}
	if deletedManagedUser.ID != managed.ID ||
		deletedManagedUser.Username != managed.Username ||
		deletedManagedUser.Status != "active" {
		t.Fatalf("deleted user snapshot = %+v", deletedManagedUser)
	}
	var deletedUserRows, deletedSessionRows, deletedBindingRows int
	if err := pool.QueryRow(ctx, `
SELECT
    (SELECT count(*) FROM users WHERE id = $1),
    (SELECT count(*) FROM user_sessions WHERE user_id = $1),
    (SELECT count(*) FROM role_bindings WHERE subject_id = $1)
`, managed.ID).Scan(
		&deletedUserRows,
		&deletedSessionRows,
		&deletedBindingRows,
	); err != nil {
		t.Fatal(err)
	}
	if deletedUserRows != 0 || deletedSessionRows != 0 || deletedBindingRows != 0 {
		t.Fatalf(
			"deleted user/access rows = %d/%d/%d, want 0/0/0",
			deletedUserRows,
			deletedSessionRows,
			deletedBindingRows,
		)
	}
	if _, err := authService.Login(ctx, auth.LoginInput{
		Username: managed.Username, Password: []byte(newPassword),
		RequestID: "request-managed-after-delete", Now: time.Now().UTC(),
	}); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("deleted user login error = %v, want invalid credentials", err)
	}
	deletedUserDetail := accessAPIRequest(
		router,
		http.MethodGet,
		"/api/v1/users/"+managed.ID,
		"",
		adminLogin,
	)
	if deletedUserDetail.Code != http.StatusNotFound {
		t.Fatalf(
			"get physically deleted user status = %d: %s",
			deletedUserDetail.Code,
			deletedUserDetail.Body,
		)
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

	// Deleting yourself is refused too, and reports the operation that was
	// actually refused rather than borrowing the disable code.
	selfDelete := accessAPIRequest(
		router,
		http.MethodDelete,
		"/api/v1/users/"+admin.ID,
		`{"confirm":true}`,
		adminLogin,
	)
	if selfDelete.Code != http.StatusConflict {
		t.Fatalf("self delete status = %d: %s", selfDelete.Code, selfDelete.Body)
	}
	assertErrorCode(t, selfDelete, "self_delete_forbidden")

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

func containsPermission(permissions []string, expected string) bool {
	for _, permission := range permissions {
		if permission == expected {
			return true
		}
	}
	return false
}
