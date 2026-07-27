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
func ScopeAttributes(c *gin.Context) []any {
	var attributes []any
	if identity, exists := Identity(c); exists {
		attributes = append(
			attributes,
			slog.String("actor_user_id", identity.User.ID),
		)
	}
	for _, parameter := range []string{"tenant_id", "project_id", "cluster_id"} {
		if value := c.Param(parameter); value != "" {
			attributes = append(attributes, slog.String(parameter, value))
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
