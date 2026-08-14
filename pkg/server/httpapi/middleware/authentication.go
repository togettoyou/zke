package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/auth"
	apiresponse "github.com/togettoyou/zke/pkg/server/httpapi/response"
)

const (
	SessionCookieName = "zke_session"
	CSRFCookieName    = "zke_csrf"
	CSRFHeaderName    = "X-CSRF-Token"

	authIdentityKey     = "authenticated_identity"
	authSessionTokenKey = "authenticated_session_token"
)

type AuthenticationConfig struct {
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
		ClearAuthenticationCookies(c)
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
	c.Set(authSessionTokenKey, sessionCookie.Value)
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

// SessionToken reports the session token this request authenticated with. A
// long-lived stream needs it to re-check that the session is still valid, and
// keeping the cookie mechanics here means no handler has to parse cookies.
func SessionToken(c *gin.Context) (string, bool) {
	value, exists := c.Get(authSessionTokenKey)
	if !exists {
		return "", false
	}
	token, valid := value.(string)
	return token, valid && token != ""
}

// RequestIsHTTPS reports whether the browser reached this Server over HTTPS. It
// decides the Secure attribute of the session cookies, which is why it is not a
// deployment setting: an operator who terminates TLS at a gateway has no reason
// to also remember a boolean here, and the one who forgets it silently loses the
// attribute -- nothing breaks over HTTPS without Secure, so the mistake is
// invisible until the session cookie leaves over plaintext.
//
// Native TLS is authoritative. Otherwise the request arrived from a gateway that
// terminated TLS and forwarded plain HTTP, and X-Forwarded-Proto is the only
// remaining signal. Trusting it cannot downgrade anyone: forging the header adds
// Secure to the forger's own session, and removing it requires control over the
// victim's request headers, which already implies control over their browser. A
// gateway that forwards neither TLS nor the header leaves cookies without Secure.
func RequestIsHTTPS(request *http.Request) bool {
	if request.TLS != nil {
		return true
	}
	// Each proxy in a chain appends to the header, so the browser-facing scheme
	// is the first entry.
	clientFacing, _, _ := strings.Cut(request.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(clientFacing), "https")
}

func ClearAuthenticationCookies(c *gin.Context) {
	expired := time.Unix(1, 0).UTC()
	secure := RequestIsHTTPS(c.Request)
	for _, cookie := range []*http.Cookie{
		{
			Name:     SessionCookieName,
			Path:     "/",
			Expires:  expired,
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		},
		{
			Name:     CSRFCookieName,
			Path:     "/",
			Expires:  expired,
			MaxAge:   -1,
			HttpOnly: false,
			Secure:   secure,
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
