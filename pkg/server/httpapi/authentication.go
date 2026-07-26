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
	apiresponse "github.com/togettoyou/zke/pkg/server/httpapi/response"
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
	logger       *slog.Logger
	service      *auth.Service
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

func newAuthHandler(
	logger *slog.Logger,
	service *auth.Service,
	config AuthenticationConfig,
) *authHandler {
	return &authHandler{
		logger:  logger,
		service: service,
		config:  config,
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
	c.JSON(http.StatusOK, authenticationResponse{
		User:      responseUser(result.User),
		ExpiresAt: responseTime(result.ExpiresAt),
	})
}

func (handler *authHandler) me(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	c.JSON(http.StatusOK, authenticationResponse{
		User:      responseUser(identity.User),
		ExpiresAt: responseTime(identity.ExpiresAt),
	})
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
	c.Status(http.StatusNoContent)
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

func (handler *authHandler) operationContext(
	c *gin.Context,
) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), handler.config.OperationTimeout)
}

func (handler *authHandler) serviceError(c *gin.Context, operation string, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		handler.logger.Warn(operation+" timed out",
			slog.String("request_id", httpmiddleware.RequestID(c)),
		)
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
		return
	}
	handler.logger.Error(operation,
		slog.String("request_id", httpmiddleware.RequestID(c)),
		slog.String("error", err.Error()),
	)
	writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
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

func writeError(c *gin.Context, status int, code string, message string) {
	apiresponse.WriteError(c, status, code, message, httpmiddleware.RequestID(c))
}
