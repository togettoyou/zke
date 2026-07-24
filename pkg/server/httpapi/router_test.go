package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	apiresponse "github.com/togettoyou/zke/pkg/server/httpapi/response"
)

func TestHealth(t *testing.T) {
	t.Parallel()

	router := testRouter(func(context.Context) error { return nil })
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("X-Request-ID header is empty")
	}
}

func TestReadinessUnavailable(t *testing.T) {
	t.Parallel()

	router := testRouter(func(context.Context) error {
		return errors.New("database unavailable")
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	router.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestRecoveryReturnsInternalServerError(t *testing.T) {
	t.Parallel()

	configureGinMode.Do(func() {
		gin.SetMode(gin.ReleaseMode)
	})
	router := gin.New()
	logger := discardLogger()
	router.Use(httpmiddleware.Recovery(logger), httpmiddleware.RequestLogger(logger))
	router.GET("/panic", func(*gin.Context) {
		panic("test panic")
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}

	var body apiresponse.Error
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != "internal_error" {
		t.Errorf("code = %q, want %q", body.Code, "internal_error")
	}
	if body.Message != "internal server error" {
		t.Errorf("message = %q, want %q", body.Message, "internal server error")
	}
	if body.RequestID == "" {
		t.Fatal("request_id is empty")
	}
	if body.RequestID != response.Header().Get("X-Request-ID") {
		t.Errorf("request_id = %q, header = %q", body.RequestID, response.Header().Get("X-Request-ID"))
	}
}

func TestRoutesAreRegisteredCentrally(t *testing.T) {
	t.Parallel()

	engine := testRouter(func(context.Context) error { return nil })
	routes, ok := engine.(*gin.Engine)
	if !ok {
		t.Fatalf("handler type = %T, want *gin.Engine", engine)
	}

	actual := make(map[string]bool)
	for _, route := range routes.Routes() {
		actual[route.Method+" "+route.Path] = true
	}
	for _, expected := range []string{
		"GET /healthz",
		"GET /readyz",
		"POST /api/v1/auth/login",
		"GET /api/v1/auth/me",
		"POST /api/v1/auth/logout",
		"POST /api/v1/projects/:project_id/agent-enrollments",
	} {
		if !actual[expected] {
			t.Errorf("route %q is not registered", expected)
		}
	}
}

func testRouter(readinessCheck ReadinessCheck) http.Handler {
	authService := auth.NewService(nil, auth.ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
	})
	return New(
		discardLogger(),
		Dependencies{
			ReadinessCheck: readinessCheck,
			AuthService:    authService,
		},
		Config{
			Authentication: defaultAuthenticationTestConfig(),
		},
	)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}
