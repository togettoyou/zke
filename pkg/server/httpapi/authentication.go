package httpapi

import (
	"context"
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
	"github.com/togettoyou/zke/pkg/shared/buildinfo"
)

const (
	sessionCookieName    = httpmiddleware.SessionCookieName
	csrfCookieName       = httpmiddleware.CSRFCookieName
	csrfHeaderName       = httpmiddleware.CSRFHeaderName
	maxLoginRequestBytes = 4 * 1024
)

type AuthenticationConfig struct {
	OperationTimeout      time.Duration
	LoginRateLimitWindow  time.Duration
	MaxAttemptsPerAccount int
	MaxAttemptsPerSource  int
}

type authHandler struct {
	baseHandler
	service      *auth.Service
	rbacService  *rbac.Service
	aiops        aiopsAvailability
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

// sessionFeaturesResponse reports which optional capabilities this deployment
// has switched on, for the caller who is not allowed to read the settings that
// switched them.
//
// A fact about the deployment rather than about the caller, so it says only
// whether the capability is there — never how it is configured.
type sessionFeaturesResponse struct {
	AIOps bool `json:"aiops"`
}

type currentSessionResponse struct {
	User      userResponse `json:"user"`
	ExpiresAt time.Time    `json:"expires_at"`
	// Rides on the session rather than on a public endpoint of its own: the
	// Console needs the Server version to compare against each Agent's, and an
	// unauthenticated caller has no reason to learn which build is deployed.
	ServerVersion string               `json:"server_version"`
	Capabilities  []capabilityResponse `json:"capabilities"`
	// Rides along for the same reason, and to answer it in the same breath as
	// the permissions: the launcher decides which application icons exist from
	// this one response, and a capability answered on a route of its own would
	// arrive after the desktop had already drawn itself without it.
	Features sessionFeaturesResponse `json:"features"`
}

// aiopsAvailability answers whether the deployment has AIOps switched on and
// pointed at an endpoint.
//
// Narrowed to the one question the session needs so that reading it cannot
// reach the endpoint itself, which stays inside the administrator-only platform
// settings.
type aiopsAvailability interface {
	Enabled(ctx context.Context) (bool, error)
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
	aiops aiopsAvailability,
	config AuthenticationConfig,
) *authHandler {
	return &authHandler{
		baseHandler: newBaseHandler(logger, nil, config.OperationTimeout),
		service:     service,
		rbacService: rbacService,
		aiops:       aiops,
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
				c.ClientIP(),
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
		ActorIP:   c.ClientIP(),
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
		User:          responseUser(identity.User),
		ExpiresAt:     responseTime(identity.ExpiresAt),
		ServerVersion: buildinfo.Version(),
		Capabilities:  capabilities,
		Features:      sessionFeaturesResponse{AIOps: handler.aiopsEnabled(c)},
	})
}

// aiopsEnabled answers the AIOps feature flag without being able to fail the
// session.
//
// The whole Console renders off this response, so an optional subsystem must
// not be able to take the shell down with it: a lookup that errors is reported
// as "not offered", which is what the Console already showed when it asked the
// AIOps route directly and got nothing back. The reason is logged, because the
// visible symptom — one missing icon — does not point at its own cause.
func (handler *authHandler) aiopsEnabled(c *gin.Context) bool {
	if handler.aiops == nil {
		return false
	}
	operationContext, cancelOperation := handler.operationContext(c)
	defer cancelOperation()
	enabled, err := handler.aiops.Enabled(operationContext)
	if err != nil {
		handler.logger.WarnContext(
			c.Request.Context(),
			"read AIOps availability for session",
			slog.String("error", err.Error()),
		)
		return false
	}
	return enabled
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
		ActorIP: c.ClientIP(),
		Now:     time.Now().UTC(),
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
		httpmiddleware.ClearAuthenticationCookies(c)
		writeError(c, http.StatusUnauthorized, "unauthenticated", "authentication required")
	case err != nil:
		handler.serviceError(c, "change current user password", err)
	default:
		httpmiddleware.ClearAuthenticationCookies(c)
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

	httpmiddleware.ClearAuthenticationCookies(c)
	writeSuccess(c, http.StatusOK, nil)
}

func (handler *authHandler) setAuthenticationCookies(
	c *gin.Context,
	result auth.LoginResult,
) {
	maxAge := max(1, int(math.Ceil(time.Until(result.ExpiresAt).Seconds())))
	secure := httpmiddleware.RequestIsHTTPS(c.Request)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookieName,
		Value:    result.SessionToken,
		Path:     "/",
		Expires:  result.ExpiresAt,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     csrfCookieName,
		Value:    result.CSRFToken,
		Path:     "/",
		Expires:  result.ExpiresAt,
		MaxAge:   maxAge,
		HttpOnly: false,
		Secure:   secure,
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
