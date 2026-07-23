package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const readinessTimeout = 2 * time.Second

type ReadinessCheck func(context.Context) error

type errorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

var configureGinMode sync.Once

func New(logger *slog.Logger, readinessCheck ReadinessCheck) http.Handler {
	configureGinMode.Do(func() {
		gin.SetMode(gin.ReleaseMode)
	})
	router := gin.New()
	router.Use(recovery(logger), requestLogger(logger))

	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), readinessTimeout)
		defer cancel()

		if err := readinessCheck(ctx); err != nil {
			logger.Warn("readiness check failed", slog.String("request_id", requestID(c)))
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	return router
}

func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		id := newRequestID()
		c.Set("request_id", id)
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

func recovery(logger *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		id := requestID(c)
		logger.Error("HTTP request panic recovered",
			slog.String("request_id", id),
		)
		c.AbortWithStatusJSON(http.StatusInternalServerError, errorResponse{
			Code:      "internal_error",
			Message:   "internal server error",
			RequestID: id,
		})
	})
}

func requestID(c *gin.Context) string {
	value, exists := c.Get("request_id")
	if !exists {
		return ""
	}
	id, _ := value.(string)
	return id
}

func newRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(value[:])
}
