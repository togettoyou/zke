package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/auth"
	apiresponse "github.com/togettoyou/zke/pkg/server/httpapi/response"
)

func TestLoginSetsProtectedAuthenticationCookies(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().UTC().Add(8 * time.Hour)
	handler := newAuthHandler(
		discardLogger(),
		nil,
		nil,
		AuthenticationConfig{CookieSecure: true},
	)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	handler.setAuthenticationCookies(context, auth.LoginResult{
		User: auth.User{
			ID:          "user-1",
			Username:    "admin",
			DisplayName: "Administrator",
		},
		SessionID:    "session-1",
		SessionToken: "session-token",
		CSRFToken:    "csrf-token",
		ExpiresAt:    expiresAt,
	})

	if strings.Contains(response.Body.String(), "session-token") ||
		strings.Contains(response.Body.String(), "csrf-token") {
		t.Fatal("authentication response exposed a token in the JSON body")
	}

	cookies := response.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookie count = %d, want 2", len(cookies))
	}
	sessionCookie := findCookie(t, cookies, sessionCookieName)
	if !sessionCookie.HttpOnly || !sessionCookie.Secure ||
		sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie attributes = %#v", sessionCookie)
	}
	csrfCookie := findCookie(t, cookies, csrfCookieName)
	if csrfCookie.HttpOnly || !csrfCookie.Secure ||
		csrfCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("CSRF cookie attributes = %#v", csrfCookie)
	}
}

func TestMeRequiresAuthentication(t *testing.T) {
	t.Parallel()

	router := testRouter(func(context.Context) error { return nil })
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)

	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	assertErrorCode(t, response, "unauthenticated")
}

func TestLoginRejectsUnknownJSONField(t *testing.T) {
	t.Parallel()

	router := testRouter(func(context.Context) error { return nil })
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		strings.NewReader(`{"username":"admin","password":"password","extra":true}`),
	)

	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	assertErrorCode(t, response, "invalid_request")
}

func TestLoginRejectsCrossOriginRequest(t *testing.T) {
	t.Parallel()

	router := testRouter(func(context.Context) error { return nil })
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"https://zke.example/api/v1/auth/login",
		strings.NewReader(`{"username":"admin","password":"password"}`),
	)
	request.Header.Set("Origin", "https://attacker.example")

	router.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
	assertErrorCode(t, response, "cross_origin_forbidden")
}

func TestServiceErrorReturnsTimeout(t *testing.T) {
	t.Parallel()

	handler := newAuthHandler(
		discardLogger(),
		nil,
		nil,
		defaultAuthenticationTestConfig(),
	)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	handler.serviceError(context, "test operation", contextDeadlineExceeded())

	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusGatewayTimeout)
	}
	assertErrorCode(t, response, "timeout")
}

func contextDeadlineExceeded() error {
	return context.DeadlineExceeded
}

func defaultAuthenticationTestConfig() AuthenticationConfig {
	return AuthenticationConfig{
		CookieSecure:          true,
		OperationTimeout:      5 * time.Second,
		LoginRateLimitWindow:  time.Minute,
		MaxAttemptsPerAccount: 5,
		MaxAttemptsPerSource:  20,
	}
}

func findCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("cookie %q not found", name)
	return nil
}

func assertErrorCode(t *testing.T, response *httptest.ResponseRecorder, expected string) {
	t.Helper()
	var body apiresponse.Error
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != response.Code {
		t.Fatalf("response code = %d, want HTTP status %d", body.Code, response.Code)
	}
	if body.Data.ErrorCode != expected {
		t.Fatalf("error code = %q, want %q", body.Data.ErrorCode, expected)
	}
}
