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
	var data struct {
		Status string `json:"status"`
	}
	if err := decodeSuccessResponse(response, &data); err != nil {
		t.Fatal(err)
	}
	if data.Status != "ok" {
		t.Fatalf("health status = %q, want ok", data.Status)
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
	var body apiresponse.Error
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != response.Code ||
		body.Data.ErrorCode != "unavailable" ||
		body.Data.RequestID == "" {
		t.Fatalf("unexpected readiness error envelope: %+v", body)
	}
}

func TestRoutingErrorsUseUniformEnvelope(t *testing.T) {
	t.Parallel()

	router := testRouter(func(context.Context) error { return nil })
	testCases := []struct {
		name      string
		method    string
		path      string
		status    int
		errorCode string
	}{
		{
			name:      "unknown route",
			method:    http.MethodGet,
			path:      "/unknown",
			status:    http.StatusNotFound,
			errorCode: "not_found",
		},
		{
			name:      "unsupported method",
			method:    http.MethodPost,
			path:      "/healthz",
			status:    http.StatusMethodNotAllowed,
			errorCode: "method_not_allowed",
		},
		{
			name:      "trailing slash is not redirected",
			method:    http.MethodGet,
			path:      "/healthz/",
			status:    http.StatusNotFound,
			errorCode: "not_found",
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			response := httptest.NewRecorder()
			request := httptest.NewRequest(
				testCase.method,
				testCase.path,
				nil,
			)
			router.ServeHTTP(response, request)

			if response.Code != testCase.status {
				t.Fatalf(
					"status = %d, want %d: %s",
					response.Code,
					testCase.status,
					response.Body,
				)
			}
			var body apiresponse.Error
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != testCase.status ||
				body.Data.ErrorCode != testCase.errorCode ||
				body.Data.RequestID == "" {
				t.Fatalf("unexpected routing error envelope: %+v", body)
			}
		})
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
	if body.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want %d", body.Code, http.StatusInternalServerError)
	}
	if body.Message != "internal server error" {
		t.Errorf("message = %q, want %q", body.Message, "internal server error")
	}
	if body.Data.ErrorCode != "internal_error" {
		t.Errorf("error_code = %q, want %q", body.Data.ErrorCode, "internal_error")
	}
	if body.Data.RequestID == "" {
		t.Fatal("request_id is empty")
	}
	if body.Data.RequestID != response.Header().Get("X-Request-ID") {
		t.Errorf("request_id = %q, header = %q", body.Data.RequestID, response.Header().Get("X-Request-ID"))
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
		"POST /api/v1/auth/password",
		"GET /api/v1/users",
		"POST /api/v1/users",
		"GET /api/v1/users/:user_id",
		"PUT /api/v1/users/:user_id",
		"DELETE /api/v1/users/:user_id",
		"PUT /api/v1/users/:user_id/status",
		"POST /api/v1/users/:user_id/unlock",
		"POST /api/v1/users/:user_id/password-reset",
		"GET /api/v1/role-bindings",
		"POST /api/v1/role-bindings",
		"DELETE /api/v1/role-bindings/:role_binding_id",
		"GET /api/v1/role-bindings/:role_binding_id",
		"GET /api/v1/audit-events",
		"GET /api/v1/events",
		"GET /api/v1/tenants",
		"POST /api/v1/tenants",
		"GET /api/v1/tenants/:tenant_id",
		"PUT /api/v1/tenants/:tenant_id",
		"DELETE /api/v1/tenants/:tenant_id",
		"GET /api/v1/tenants/:tenant_id/projects",
		"POST /api/v1/tenants/:tenant_id/projects",
		"GET /api/v1/projects/:project_id",
		"PUT /api/v1/projects/:project_id",
		"DELETE /api/v1/projects/:project_id",
		"GET /api/v1/projects/:project_id/clusters",
		"GET /api/v1/clusters/:cluster_id",
		"PUT /api/v1/clusters/:cluster_id",
		"DELETE /api/v1/clusters/:cluster_id",
		"POST /api/v1/projects/:project_id/cluster-enrollments",
		"GET /api/v1/projects/:project_id/cluster-enrollments",
		"GET /api/v1/projects/:project_id/cluster-enrollments/:enrollment_id",
		"DELETE /api/v1/projects/:project_id/cluster-enrollments/:enrollment_id",
		"POST /api/v1/projects/:project_id/cluster-installations",
		"POST /api/v1/clusters/:cluster_id/connection/revoke",
		"POST /api/v1/clusters/:cluster_id/connection/reenroll",
		"POST /agent-api/v1/enroll",
		"GET /agent-install/v1/manifest",
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
