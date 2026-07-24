package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
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

		logger.Info("HTTP request completed",
			slog.String("request_id", id),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", c.Writer.Status()),
			slog.Duration("duration", time.Since(started)),
		)
	}
}

func Recovery(logger *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		id := RequestID(c)
		logger.Error("HTTP request panic recovered",
			slog.String("request_id", id),
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
