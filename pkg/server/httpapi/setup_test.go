package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/auth"
)

type recordingSetupService struct {
	required   bool
	setupError error
	setupInput auth.AdministratorSetupInput
	loginInput auth.LoginInput
}

func (service *recordingSetupService) SetupRequired(context.Context) (bool, error) {
	return service.required, nil
}

func (service *recordingSetupService) SetupAdministrator(
	_ context.Context,
	input auth.AdministratorSetupInput,
) (auth.User, error) {
	input.Password = append([]byte(nil), input.Password...)
	service.setupInput = input
	if service.setupError != nil {
		return auth.User{}, service.setupError
	}
	return auth.User{ID: "user-1", Username: input.Username, DisplayName: input.Username}, nil
}

func (service *recordingSetupService) Login(
	_ context.Context,
	input auth.LoginInput,
) (auth.LoginResult, error) {
	input.Password = append([]byte(nil), input.Password...)
	service.loginInput = input
	return auth.LoginResult{
		User:         auth.User{ID: "user-1", Username: input.Username, DisplayName: input.Username},
		SessionToken: "session-token", CSRFToken: "csrf-token",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}, nil
}

func TestSetupHandlerReportsStateAndCreatesSession(t *testing.T) {
	t.Parallel()
	service := &recordingSetupService{required: true}
	router := setupTestRouter(service)

	statusResponse := httptest.NewRecorder()
	router.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/api/v1/setup", nil))
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("setup status = %d: %s", statusResponse.Code, statusResponse.Body)
	}
	var status setupStatusResponse
	if err := decodeSuccessResponse(statusResponse, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Required {
		t.Fatal("setup status did not report the missing administrator")
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/setup", strings.NewReader(
		`{"username":"chosen-owner","password":"a manually selected administrator password"}`,
	))
	request.RemoteAddr = "192.0.2.90:1234"
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("setup = %d: %s", response.Code, response.Body)
	}
	if service.setupInput.Username != "chosen-owner" ||
		string(service.setupInput.Password) != "a manually selected administrator password" {
		t.Fatalf("unexpected setup input: %#v", service.setupInput)
	}
	if service.loginInput.Username != service.setupInput.Username {
		t.Fatalf("login username = %q, setup username = %q", service.loginInput.Username, service.setupInput.Username)
	}
	if strings.Contains(response.Body.String(), "administrator password") ||
		strings.Contains(response.Body.String(), "session-token") ||
		strings.Contains(response.Body.String(), "csrf-token") {
		t.Fatal("setup response exposed a password or authentication token")
	}
	findCookie(t, response.Result().Cookies(), sessionCookieName)
	findCookie(t, response.Result().Cookies(), csrfCookieName)
}

func TestSetupHandlerRejectsCompletedSetup(t *testing.T) {
	t.Parallel()
	service := &recordingSetupService{setupError: auth.ErrSetupAlreadyCompleted}
	router := setupTestRouter(service)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/setup", strings.NewReader(
		`{"username":"another-owner","password":"another sufficiently long password"}`,
	))
	request.RemoteAddr = "192.0.2.91:1234"
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("completed setup = %d: %s", response.Code, response.Body)
	}
	assertErrorCode(t, response, "setup_completed")
	if service.loginInput.Username != "" {
		t.Fatal("completed setup attempted to establish a session")
	}
}

func setupTestRouter(service setupService) http.Handler {
	config := defaultAuthenticationTestConfig()
	authHandler := newAuthHandler(discardLogger(), nil, nil, config)
	handler := newSetupHandler(discardLogger(), service, authHandler, config)
	router := gin.New()
	router.GET("/api/v1/setup", handler.status)
	router.POST("/api/v1/setup", handler.initialize)
	return router
}
