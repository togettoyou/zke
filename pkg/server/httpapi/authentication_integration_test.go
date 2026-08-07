package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
)

func TestAuthenticationHTTPFlow(t *testing.T) {
	databaseURL := requireHTTPTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openHTTPTestDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	authStore := store.NewAuthStore(pool)
	password := "a sufficiently long admin passphrase"
	if _, err := auth.CreateInitialAdmin(ctx, authStore, auth.InitialAdminInput{
		Username:    "admin",
		DisplayName: "ZKE Administrator",
		Password:    []byte(password),
	}); err != nil {
		t.Fatal(err)
	}

	authService := auth.NewService(authStore, auth.ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
	})
	router := New(
		discardLogger(),
		Dependencies{
			ReadinessCheck: pool.Ping,
			AuthService:    authService,
			RBACService:    rbac.NewService(store.NewRBACStore(pool)),
		},
		Config{
			Authentication: AuthenticationConfig{
				OperationTimeout:      5 * time.Second,
				LoginRateLimitWindow:  time.Minute,
				MaxAttemptsPerAccount: 5,
				MaxAttemptsPerSource:  20,
			},
		},
	)

	loginResponse := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		strings.NewReader(
			`{"username":"admin","password":"a sufficiently long admin passphrase"}`,
		),
	)
	loginRequest.RemoteAddr = "192.0.2.50:1234"
	router.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status = %d, want %d: %s",
			loginResponse.Code,
			http.StatusOK,
			loginResponse.Body,
		)
	}
	var loginBody authenticationResponse
	if err := decodeSuccessResponse(loginResponse, &loginBody); err != nil {
		t.Fatal(err)
	}
	assertUTC8Time(t, "login expires_at", loginBody.ExpiresAt)
	sessionCookie := findCookie(t, loginResponse.Result().Cookies(), sessionCookieName)
	csrfCookie := findCookie(t, loginResponse.Result().Cookies(), csrfCookieName)

	meResponse := httptest.NewRecorder()
	meRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	meRequest.AddCookie(sessionCookie)
	router.ServeHTTP(meResponse, meRequest)
	if meResponse.Code != http.StatusOK {
		t.Fatalf("me status = %d, want %d: %s",
			meResponse.Code,
			http.StatusOK,
			meResponse.Body,
		)
	}
	var meBody currentSessionResponse
	if err := decodeSuccessResponse(meResponse, &meBody); err != nil {
		t.Fatal(err)
	}
	assertUTC8Time(t, "current session expires_at", meBody.ExpiresAt)
	if len(meBody.Capabilities) != 1 ||
		meBody.Capabilities[0].Role != "admin" ||
		meBody.Capabilities[0].ScopeType != "global" ||
		len(meBody.Capabilities[0].Permissions) == 0 {
		t.Fatalf("unexpected current capabilities: %+v", meBody.Capabilities)
	}

	missingCSRFResponse := httptest.NewRecorder()
	missingCSRFRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	missingCSRFRequest.AddCookie(sessionCookie)
	router.ServeHTTP(missingCSRFResponse, missingCSRFRequest)
	if missingCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF status = %d, want %d",
			missingCSRFResponse.Code,
			http.StatusForbidden,
		)
	}
	assertErrorCode(t, missingCSRFResponse, "csrf_invalid")

	logoutResponse := httptest.NewRecorder()
	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutRequest.AddCookie(sessionCookie)
	logoutRequest.Header.Set(csrfHeaderName, csrfCookie.Value)
	router.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusOK {
		t.Fatalf("logout status = %d, want %d: %s",
			logoutResponse.Code,
			http.StatusOK,
			logoutResponse.Body,
		)
	}
	var logoutData any
	if err := decodeSuccessResponse(logoutResponse, &logoutData); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range logoutResponse.Result().Cookies() {
		if cookie.MaxAge != -1 {
			t.Fatalf("cleared cookie MaxAge = %d, want -1", cookie.MaxAge)
		}
	}

	revokedResponse := httptest.NewRecorder()
	revokedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	revokedRequest.AddCookie(sessionCookie)
	router.ServeHTTP(revokedResponse, revokedRequest)
	if revokedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d, want %d",
			revokedResponse.Code,
			http.StatusUnauthorized,
		)
	}
}

func TestCurrentUserPasswordChange(t *testing.T) {
	databaseURL := requireHTTPTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openHTTPTestDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	authStore := store.NewAuthStore(pool)
	const oldPassword = "a sufficiently long original password"
	const newPassword = "a sufficiently long replacement password"
	if _, err := auth.CreateInitialAdmin(ctx, authStore, auth.InitialAdminInput{
		Username: "password-admin", DisplayName: "Password Administrator",
		Password: []byte(oldPassword),
	}); err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(authStore, auth.ServiceConfig{
		SessionIdleTimeout: 30 * time.Minute, SessionAbsoluteTimeout: 8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
	})
	rbacService := rbac.NewService(store.NewRBACStore(pool))
	router := New(discardLogger(), Dependencies{
		ReadinessCheck: pool.Ping, AuthService: authService, RBACService: rbacService,
	}, Config{Authentication: defaultAuthenticationTestConfig()})

	login, err := authService.Login(ctx, auth.LoginInput{
		Username: "password-admin", Password: []byte(oldPassword),
		RequestID: "password-change-login", Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	invalidNewPasswordResponse := httptest.NewRecorder()
	invalidNewPasswordRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/password",
		strings.NewReader(
			`{"current_password":"`+oldPassword+
				`","new_password":"too short","confirm":true}`,
		),
	)
	invalidNewPasswordRequest.AddCookie(&http.Cookie{
		Name: sessionCookieName, Value: login.SessionToken,
	})
	invalidNewPasswordRequest.Header.Set(csrfHeaderName, login.CSRFToken)
	router.ServeHTTP(invalidNewPasswordResponse, invalidNewPasswordRequest)
	if invalidNewPasswordResponse.Code != http.StatusBadRequest {
		t.Fatalf(
			"invalid new password status = %d: %s",
			invalidNewPasswordResponse.Code,
			invalidNewPasswordResponse.Body,
		)
	}
	assertErrorCode(t, invalidNewPasswordResponse, "invalid_new_password")
	if _, err := authService.Authenticate(
		ctx, login.SessionToken, time.Now().UTC(),
	); err != nil {
		t.Fatalf("session after rejected password change: %v", err)
	}

	unchangedPasswordResponse := httptest.NewRecorder()
	unchangedPasswordRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/password",
		strings.NewReader(
			`{"current_password":"`+oldPassword+
				`","new_password":"`+oldPassword+`","confirm":true}`,
		),
	)
	unchangedPasswordRequest.AddCookie(&http.Cookie{
		Name: sessionCookieName, Value: login.SessionToken,
	})
	unchangedPasswordRequest.Header.Set(csrfHeaderName, login.CSRFToken)
	router.ServeHTTP(unchangedPasswordResponse, unchangedPasswordRequest)
	if unchangedPasswordResponse.Code != http.StatusBadRequest {
		t.Fatalf(
			"unchanged password status = %d: %s",
			unchangedPasswordResponse.Code,
			unchangedPasswordResponse.Body,
		)
	}
	assertErrorCode(t, unchangedPasswordResponse, "password_unchanged")

	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/password",
		strings.NewReader(
			`{"current_password":"`+oldPassword+
				`","new_password":"`+newPassword+`","confirm":true}`,
		),
	)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: login.SessionToken})
	request.Header.Set(csrfHeaderName, login.CSRFToken)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("password change status = %d: %s", response.Code, response.Body)
	}
	var passwordChangeData any
	if err := decodeSuccessResponse(response, &passwordChangeData); err != nil {
		t.Fatal(err)
	}

	if _, err := authService.Authenticate(
		ctx, login.SessionToken, time.Now().UTC(),
	); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("old session authentication error = %v, want unauthenticated", err)
	}
	if _, err := authService.Login(ctx, auth.LoginInput{
		Username: "password-admin", Password: []byte(oldPassword),
		RequestID: "old-password-login", Now: time.Now().UTC(),
	}); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("old password login error = %v, want invalid credentials", err)
	}
	if _, err := authService.Login(ctx, auth.LoginInput{
		Username: "password-admin", Password: []byte(newPassword),
		RequestID: "new-password-login", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("new password login: %v", err)
	}
	var auditCount int
	var succeededAuditCount int
	var failedAuditCount int
	if err := pool.QueryRow(ctx, `
SELECT
    count(*),
    count(*) FILTER (WHERE result = 'succeeded'),
    count(*) FILTER (WHERE result = 'failed')
FROM audit_events
WHERE action = 'auth.password.change'
`).Scan(&auditCount, &succeededAuditCount, &failedAuditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 3 || succeededAuditCount != 1 || failedAuditCount != 2 {
		t.Fatalf(
			"password change audit counts = total %d, succeeded %d, failed %d",
			auditCount,
			succeededAuditCount,
			failedAuditCount,
		)
	}
}

func TestLoginRateLimitAuditsDenialOnce(t *testing.T) {
	databaseURL := requireHTTPTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openHTTPTestDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	authStore := store.NewAuthStore(pool)
	if _, err := auth.CreateInitialAdmin(ctx, authStore, auth.InitialAdminInput{
		Username:    "admin",
		DisplayName: "ZKE Administrator",
		Password:    []byte("a sufficiently long admin passphrase"),
	}); err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(authStore, auth.ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
	})
	router := New(
		discardLogger(),
		Dependencies{
			ReadinessCheck: pool.Ping,
			AuthService:    authService,
		},
		Config{
			Authentication: AuthenticationConfig{
				OperationTimeout:      5 * time.Second,
				LoginRateLimitWindow:  time.Minute,
				MaxAttemptsPerAccount: 1,
				MaxAttemptsPerSource:  10,
			},
		},
	)

	for attempt, expectedStatus := range []int{
		http.StatusUnauthorized,
		http.StatusTooManyRequests,
		http.StatusTooManyRequests,
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/auth/login",
			strings.NewReader(`{"username":"admin","password":"wrong password"}`),
		)
		request.RemoteAddr = "192.0.2.60:1234"
		router.ServeHTTP(response, request)

		if response.Code != expectedStatus {
			t.Fatalf("attempt %d status = %d, want %d: %s",
				attempt+1,
				response.Code,
				expectedStatus,
				response.Body,
			)
		}
		if expectedStatus == http.StatusTooManyRequests &&
			response.Header().Get("Retry-After") == "" {
			t.Fatalf("attempt %d Retry-After header is empty", attempt+1)
		}
	}

	var deniedAuditCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_events
WHERE action = 'auth.login'
  AND result = 'denied'
`).Scan(&deniedAuditCount); err != nil {
		t.Fatal(err)
	}
	if deniedAuditCount != 1 {
		t.Fatalf("rate-limit denied audit count = %d, want 1", deniedAuditCount)
	}
}

func requireHTTPTestDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("ZKE_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("ZKE_TEST_DATABASE_URL is required in CI")
		}
		t.Skip("ZKE_TEST_DATABASE_URL is not configured")
	}
	return databaseURL
}

func openHTTPTestDatabase(
	t *testing.T,
	ctx context.Context,
	databaseURL string,
) *pgxpool.Pool {
	t.Helper()

	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}

	var randomValue [8]byte
	if _, err := rand.Read(randomValue[:]); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	schemaName := "zke_http_test_" + hex.EncodeToString(randomValue[:])
	quotedSchemaName := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchemaName); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		_, _ = adminPool.Exec(ctx, "DROP SCHEMA "+quotedSchemaName+" CASCADE")
		adminPool.Close()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		_, _ = adminPool.Exec(ctx, "DROP SCHEMA "+quotedSchemaName+" CASCADE")
		adminPool.Close()
		t.Fatal(err)
	}

	t.Cleanup(func() {
		pool.Close()
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(
			cleanupContext,
			"DROP SCHEMA "+quotedSchemaName+" CASCADE",
		); err != nil {
			t.Errorf("drop HTTP test schema: %v", err)
		}
		adminPool.Close()
	})
	return pool
}
