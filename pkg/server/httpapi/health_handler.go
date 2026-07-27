package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
)

const readinessTimeout = 2 * time.Second

type healthHandler struct {
	logger         *slog.Logger
	readinessCheck ReadinessCheck
}

func newHealthHandler(
	logger *slog.Logger,
	readinessCheck ReadinessCheck,
) *healthHandler {
	return &healthHandler{
		logger:         logger,
		readinessCheck: readinessCheck,
	}
}

func (handler *healthHandler) health(c *gin.Context) {
	writeSuccess(c, http.StatusOK, gin.H{"status": "ok"})
}

func (handler *healthHandler) ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), readinessTimeout)
	defer cancel()

	if err := handler.readinessCheck(ctx); err != nil {
		handler.logger.Warn("readiness check failed",
			slog.String("request_id", httpmiddleware.RequestID(c)),
		)
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Server is not ready")
		return
	}
	writeSuccess(c, http.StatusOK, gin.H{"status": "ok"})
}
