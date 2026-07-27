package httpapi

import (
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

const (
	sessionCookieName    = httpmiddleware.SessionCookieName
	csrfCookieName       = httpmiddleware.CSRFCookieName
	csrfHeaderName       = httpmiddleware.CSRFHeaderName
	maxLoginRequestBytes = 4 * 1024
)

type AuthenticationConfig struct {
	CookieSecure          bool
	OperationTimeout      time.Duration
	LoginRateLimitWindow  time.Duration
	MaxAttemptsPerAccount int
	MaxAttemptsPerSource  int
}

type authHandler struct {
	baseHandler
	service      *auth.Service
	rbacService  *rbac.Service
	config       AuthenticationConfig
	loginLimiter *loginLimiter
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type userResponse struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type authenticationResponse struct {
	User      userResponse `json:"user"`
	ExpiresAt time.Time    `json:"expires_at"`
}

type capabilityResponse struct {
	Role        string   `json:"role"`
	ScopeType   string   `json:"scope_type"`
	TenantID    string   `json:"tenant_id,omitempty"`
	ProjectID   string   `json:"project_id,omitempty"`
	Permissions []string `json:"permissions"`
}

type currentSessionResponse struct {
	User         userResponse         `json:"user"`
	ExpiresAt    time.Time            `json:"expires_at"`
	Capabilities []capabilityResponse `json:"capabilities"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
	Confirm         bool   `json:"confirm"`
}

func newAuthHandler(
	logger *slog.Logger,
	service *auth.Service,
	rbacService *rbac.Service,
	config AuthenticationConfig,
) *authHandler {
	return &authHandler{
		baseHandler: newBaseHandler(logger, nil, config.OperationTimeout),
		service:     service,
		rbacService: rbacService,
		config:      config,
		loginLimiter: newLoginLimiter(loginLimiterConfig{
			window:                config.LoginRateLimitWindow,
			maxAttemptsPerAccount: config.MaxAttemptsPerAccount,
			maxAttemptsPerSource:  config.MaxAttemptsPerSource,
		}),
	}
}

func (handler *authHandler) login(c *gin.Context) {
	c.Header("Cache-Control", "no-store")

	var input loginRequest
	if err := decodeJSONRequest(c, &input, maxLoginRequestBytes); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid login request")
		return
	}

	accountKey := strings.TrimSpace(input.Username)
	if normalized, err := auth.NormalizeUsername(input.Username); err == nil {
		accountKey = normalized
	}
	allowed, retry, shouldAudit := handler.loginLimiter.allow(
		clientAddress(c.Request),
		accountKey,
		time.Now(),
	)
	if !allowed {
		if shouldAudit {
			operationContext, cancelOperation := handler.operationContext(c)
			if err := handler.service.RecordLoginDenied(
				operationContext,
				httpmiddleware.RequestID(c),
			); err != nil {
				cancelOperation()
				handler.serviceError(c, "record rate-limited login audit", err)
				return
			}
			cancelOperation()
		}
		retrySeconds := max(1, int(math.Ceil(retry.Seconds())))
		c.Header("Retry-After", strconv.Itoa(retrySeconds))
		writeError(
			c,
			http.StatusTooManyRequests,
			"too_many_requests",
			"too many login attempts",
		)
		return
	}

	password := []byte(input.Password)
	input.Password = ""
	defer clear(password)
	operationContext, cancelOperation := handler.operationContext(c)
	result, err := handler.service.Login(operationContext, auth.LoginInput{
		Username:  input.Username,
		Password:  password,
		RequestID: httpmiddleware.RequestID(c),
		Now:       time.Now().UTC(),
	})
	cancelOperation()
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeError(
			c,
			http.StatusUnauthorized,
			"invalid_credentials",
			"invalid username or password",
		)
		return
	}
	if err != nil {
		handler.serviceError(c, "authenticate user", err)
		return
	}

	handler.setAuthenticationCookies(c, result)
	writeSuccess(c, http.StatusOK, authenticationResponse{
		User:      responseUser(result.User),
		ExpiresAt: responseTime(result.ExpiresAt),
	})
}

func (handler *authHandler) me(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	capabilities := make([]capabilityResponse, 0)
	if handler.rbacService != nil {
		operationContext, cancelOperation := handler.operationContext(c)
		items, err := handler.rbacService.ListCapabilities(
			operationContext,
			identity.User.ID,
		)
		cancelOperation()
		if err != nil {
			handler.serviceError(c, "resolve current user capabilities", err)
			return
		}
		for _, item := range items {
			permissions := make([]string, 0, len(item.Permissions))
			for _, permission := range item.Permissions {
				permissions = append(permissions, string(permission))
			}
			capabilities = append(capabilities, capabilityResponse{
				Role: item.Role, ScopeType: item.ScopeType,
				TenantID: item.TenantID, ProjectID: item.ProjectID,
				Permissions: permissions,
			})
		}
	}
	writeSuccess(c, http.StatusOK, currentSessionResponse{
		User:         responseUser(identity.User),
		ExpiresAt:    responseTime(identity.ExpiresAt),
		Capabilities: capabilities,
	})
}

func (handler *authHandler) changePassword(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	var request changePasswordRequest
	if err := decodeJSONRequest(c, &request, maxLoginRequestBytes); err != nil {
		request.CurrentPassword = ""
		request.NewPassword = ""
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid password change request")
		return
	}
	if !request.Confirm {
		request.CurrentPassword = ""
		request.NewPassword = ""
		writeError(c, http.StatusBadRequest, "confirmation_required", "explicit confirmation is required")
		return
	}
	currentPassword := []byte(request.CurrentPassword)
	newPassword := []byte(request.NewPassword)
	request.CurrentPassword = ""
	request.NewPassword = ""
	defer clear(currentPassword)
	defer clear(newPassword)

	operationContext, cancelOperation := handler.operationContext(c)
	err := handler.service.ChangePassword(operationContext, auth.ChangePasswordInput{
		Identity: identity, CurrentPassword: currentPassword,
		NewPassword: newPassword, RequestID: httpmiddleware.RequestID(c),
		Now: time.Now().UTC(),
	})
	cancelOperation()
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(c, http.StatusBadRequest, "invalid_current_password", "current password is invalid")
	case errors.Is(err, auth.ErrInvalidNewPassword):
		writeError(c, http.StatusBadRequest, "invalid_new_password", "new password does not satisfy password policy")
	case errors.Is(err, auth.ErrPasswordUnchanged):
		writeError(c, http.StatusBadRequest, "password_unchanged", "new password must differ from current password")
	case errors.Is(err, auth.ErrUnauthenticated):
		httpmiddleware.ClearAuthenticationCookies(c, handler.config.CookieSecure)
		writeError(c, http.StatusUnauthorized, "unauthenticated", "authentication required")
	case err != nil:
		handler.serviceError(c, "change current user password", err)
	default:
		httpmiddleware.ClearAuthenticationCookies(c, handler.config.CookieSecure)
		writeSuccess(c, http.StatusOK, nil)
	}
}

func (handler *authHandler) logout(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	operationContext, cancelOperation := handler.operationContext(c)
	err := handler.service.Logout(
		operationContext,
		identity,
		httpmiddleware.RequestID(c),
		time.Now().UTC(),
	)
	cancelOperation()
	if err != nil && !errors.Is(err, auth.ErrUnauthenticated) {
		handler.serviceError(c, "revoke authenticated session", err)
		return
	}

	httpmiddleware.ClearAuthenticationCookies(c, handler.config.CookieSecure)
	writeSuccess(c, http.StatusOK, nil)
}

func (handler *authHandler) setAuthenticationCookies(
	c *gin.Context,
	result auth.LoginResult,
) {
	maxAge := max(1, int(math.Ceil(time.Until(result.ExpiresAt).Seconds())))
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookieName,
		Value:    result.SessionToken,
		Path:     "/",
		Expires:  result.ExpiresAt,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   handler.config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     csrfCookieName,
		Value:    result.CSRFToken,
		Path:     "/",
		Expires:  result.ExpiresAt,
		MaxAge:   maxAge,
		HttpOnly: false,
		Secure:   handler.config.CookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (handler *authHandler) serviceError(c *gin.Context, operation string, err error) {
	handler.respondError(c, operation, err)
}

func responseUser(user auth.User) userResponse {
	return userResponse{
		ID:          user.ID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
	}
}

func clientAddress(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return request.RemoteAddr
}
