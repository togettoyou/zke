package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/auth"
	apiresponse "github.com/togettoyou/zke/pkg/server/httpapi/response"
)

const (
	SessionCookieName = "zke_session"
	CSRFCookieName    = "zke_csrf"
	CSRFHeaderName    = "X-CSRF-Token"

	authIdentityKey = "authenticated_identity"
)

type AuthenticationConfig struct {
	CookieSecure     bool
	OperationTimeout time.Duration
}

type Authentication struct {
	logger  *slog.Logger
	service *auth.Service
	config  AuthenticationConfig
}

func NewAuthentication(
	logger *slog.Logger,
	service *auth.Service,
	config AuthenticationConfig,
) *Authentication {
	return &Authentication{
		logger:  logger,
		service: service,
		config:  config,
	}
}

func (authMiddleware *Authentication) RequireAuthentication(c *gin.Context) {
	sessionCookie, err := c.Request.Cookie(SessionCookieName)
	if err != nil || sessionCookie.Value == "" {
		writeError(c, http.StatusUnauthorized, "unauthenticated", "authentication required")
		c.Abort()
		return
	}

	operationContext, cancelOperation := authMiddleware.operationContext(c)
	identity, err := authMiddleware.service.Authenticate(
		operationContext,
		sessionCookie.Value,
		time.Now().UTC(),
	)
	cancelOperation()
	if errors.Is(err, auth.ErrUnauthenticated) {
		ClearAuthenticationCookies(c, authMiddleware.config.CookieSecure)
		writeError(c, http.StatusUnauthorized, "unauthenticated", "authentication required")
		c.Abort()
		return
	}
	if err != nil {
		authMiddleware.serviceError(c, "resolve authenticated session", err)
		c.Abort()
		return
	}

	c.Set(authIdentityKey, identity)
	c.Next()
}

func (authMiddleware *Authentication) RequireCSRF(c *gin.Context) {
	identity, exists := Identity(c)
	if !exists ||
		!authMiddleware.service.CSRFTokenMatches(identity, c.GetHeader(CSRFHeaderName)) {
		writeError(c, http.StatusForbidden, "csrf_invalid", "CSRF token is invalid")
		c.Abort()
		return
	}
	c.Next()
}

func Identity(c *gin.Context) (auth.Identity, bool) {
	value, exists := c.Get(authIdentityKey)
	if !exists {
		return auth.Identity{}, false
	}
	identity, valid := value.(auth.Identity)
	return identity, valid
}

func ClearAuthenticationCookies(c *gin.Context, cookieSecure bool) {
	expired := time.Unix(1, 0).UTC()
	for _, cookie := range []*http.Cookie{
		{
			Name:     SessionCookieName,
			Path:     "/",
			Expires:  expired,
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   cookieSecure,
			SameSite: http.SameSiteLaxMode,
		},
		{
			Name:     CSRFCookieName,
			Path:     "/",
			Expires:  expired,
			MaxAge:   -1,
			HttpOnly: false,
			Secure:   cookieSecure,
			SameSite: http.SameSiteStrictMode,
		},
	} {
		http.SetCookie(c.Writer, cookie)
	}
}

func (authMiddleware *Authentication) operationContext(
	c *gin.Context,
) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), authMiddleware.config.OperationTimeout)
}

func (authMiddleware *Authentication) serviceError(
	c *gin.Context,
	operation string,
	err error,
) {
	if errors.Is(err, context.DeadlineExceeded) {
		authMiddleware.logger.Warn(operation+" timed out",
			slog.String("request_id", RequestID(c)),
		)
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
		return
	}
	authMiddleware.logger.Error(operation,
		slog.String("request_id", RequestID(c)),
		slog.String("error", err.Error()),
	)
	writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
}

func writeError(c *gin.Context, status int, code string, message string) {
	apiresponse.WriteError(c, status, code, message, RequestID(c))
}
