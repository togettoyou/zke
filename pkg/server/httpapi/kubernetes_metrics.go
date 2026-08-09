package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

type kubernetesMetricsService interface {
	ListNodeMetrics(context.Context, string) (kubernetesresource.NodeMetricsSnapshot, error)
	ListPodMetrics(context.Context, string, string) (kubernetesresource.PodMetricsSnapshot, error)
}

type kubernetesMetricsHandler struct {
	baseHandler
	service kubernetesMetricsService
}

func newKubernetesMetricsHandler(
	logger *slog.Logger,
	service kubernetesMetricsService,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *kubernetesMetricsHandler {
	return &kubernetesMetricsHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *kubernetesMetricsHandler) nodes(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "Node metrics do not accept query parameters")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes metrics query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListNodeMetrics(ctx, c.Param("cluster_id"))
	cancel()
	if handler.respondMetricsError(c, "list Kubernetes Node metrics", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesMetricsHandler) pods(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "Pod metrics do not accept query parameters")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Kubernetes metrics query is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListPodMetrics(
		ctx,
		c.Param("cluster_id"),
		c.Param("namespace_name"),
	)
	cancel()
	if handler.respondMetricsError(c, "list Kubernetes Pod metrics", err) {
		return
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *kubernetesMetricsHandler) respondMetricsError(
	c *gin.Context,
	operation string,
	err error,
) bool {
	resourceHandler := kubernetesResourceHandler{baseHandler: handler.baseHandler}
	return resourceHandler.respondResourceError(c, operation, err)
}
