package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	apiresponse "github.com/togettoyou/zke/pkg/server/httpapi/response"
)

const requestIDKey = "request_id"

func RequestID(c *gin.Context) string {
	value, exists := c.Get(requestIDKey)
	if !exists {
		return ""
	}
	id, _ := value.(string)
	return id
}

func RequestTimeout(timeout time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestContext, cancelRequest := context.WithTimeout(
			c.Request.Context(),
			timeout,
		)
		defer cancelRequest()
		c.Request = c.Request.WithContext(requestContext)
		c.Next()
	}
}

func RequestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		id := newRequestID()
		c.Set(requestIDKey, id)
		c.Header("X-Request-ID", id)

		c.Next()

		attributes := []any{
			slog.String("request_id", id),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", c.Writer.Status()),
			slog.Duration("duration", time.Since(started)),
		}
		logger.Info("HTTP request completed", append(attributes, ScopeAttributes(c)...)...)
	}
}

// ScopeAttributes reports the authenticated actor and the resource scope the
// request addresses, so that every log line can be correlated to a tenant,
// project or cluster without cross-cluster ambiguity.
//
// A Cluster or Project route names only its own identifier in the path, so the
// owning Tenant and Project are taken from what the authorization middleware
// already resolved. Without that, a Cluster operation would log a cluster_id
// with no tenant to attribute it to.
func ScopeAttributes(c *gin.Context) []any {
	var attributes []any
	if identity, exists := Identity(c); exists {
		attributes = append(
			attributes,
			slog.String("actor_user_id", identity.User.ID),
		)
	}
	resolved, _ := ResolvedScope(c)
	for _, item := range []struct {
		name     string
		resolved string
	}{
		{"tenant_id", resolved.TenantID},
		{"project_id", resolved.ProjectID},
		{"cluster_id", ""},
	} {
		value := c.Param(item.name)
		if value == "" {
			value = item.resolved
		}
		if value != "" {
			attributes = append(attributes, slog.String(item.name, value))
		}
	}
	return attributes
}

func Recovery(logger *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		id := RequestID(c)
		logger.Error("HTTP request panic recovered",
			slog.String("request_id", id),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.String("panic", fmt.Sprint(recovered)),
			slog.String("stack", string(debug.Stack())),
		)
		apiresponse.WriteError(
			c,
			http.StatusInternalServerError,
			"internal_error",
			"internal server error",
			id,
		)
		c.Abort()
	})
}

func newRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(value[:])
}
