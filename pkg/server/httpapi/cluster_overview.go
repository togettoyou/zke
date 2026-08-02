package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/clusteroverview"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

type clusterOverviewService interface {
	Get(context.Context, string) (clusteroverview.Overview, error)
}

type clusterOverviewHandler struct {
	baseHandler
	service clusterOverviewService
}

func newClusterOverviewHandler(
	logger *slog.Logger,
	service clusterOverviewService,
	operationTimeout time.Duration,
) *clusterOverviewHandler {
	return &clusterOverviewHandler{
		baseHandler: newBaseHandler(logger, nil, operationTimeout),
		service:     service,
	}
}

func (handler *clusterOverviewHandler) get(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.URL.Query()) != 0 {
		writeError(c, http.StatusBadRequest, "invalid_request", "Cluster overview does not accept query parameters")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Cluster overview is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.Get(ctx, c.Param("cluster_id"))
	cancel()
	if handler.respondOverviewError(c, err) {
		return
	}
	if result.Partial {
		attributes := []any{
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.Int("issue_count", len(result.Issues)),
		}
		handler.logger.Warn(
			"Kubernetes Cluster overview is partial",
			append(attributes, httpmiddleware.ScopeAttributes(c)...)...,
		)
	}
	writeSuccess(c, http.StatusOK, result)
}

func (handler *clusterOverviewHandler) respondOverviewError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, kubernetesresource.ErrRequestCapacity) ||
		errors.Is(err, kubernetesresource.ErrResponseBudget) {
		c.Header("Retry-After", "1")
	}
	return handler.respondError(
		c,
		"get Kubernetes Cluster overview",
		err,
		errorMapping{kubernetesresource.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid Cluster overview request"},
		errorMapping{kubernetesresource.ErrAgentNotConnected, http.StatusServiceUnavailable, "agent_not_connected", "Cluster Agent is not connected"},
		errorMapping{kubernetesresource.ErrAgentUnsupported, http.StatusServiceUnavailable, "agent_capability_unavailable", "Cluster Agent does not support resource queries"},
		errorMapping{kubernetesresource.ErrRequestCapacity, http.StatusTooManyRequests, "resource_capacity_exhausted", "resource query capacity is exhausted"},
		errorMapping{kubernetesresource.ErrResponseBudget, http.StatusTooManyRequests, "response_budget_exhausted", "Server response buffer budget is exhausted"},
		errorMapping{kubernetesresource.ErrClusterUnavailable, http.StatusServiceUnavailable, "cluster_api_unavailable", "Kubernetes API is unavailable"},
		errorMapping{kubernetesresource.ErrClusterTimeout, http.StatusGatewayTimeout, "cluster_api_timeout", "Kubernetes API request timed out"},
		errorMapping{kubernetesresource.ErrClusterUnauthenticated, http.StatusBadGateway, "cluster_api_unauthenticated", "Agent Kubernetes credentials were rejected"},
		errorMapping{kubernetesresource.ErrClusterAccessDenied, http.StatusBadGateway, "cluster_api_forbidden", "Agent is not allowed to read Cluster overview resources"},
		errorMapping{kubernetesresource.ErrResponseTooLarge, http.StatusBadGateway, "agent_response_too_large", "Agent response exceeded the configured limit"},
		errorMapping{kubernetesresource.ErrInvalidResponse, http.StatusBadGateway, "invalid_agent_response", "Agent returned an invalid resource response"},
		errorMapping{kubernetesresource.ErrUpstreamFailure, http.StatusBadGateway, "cluster_api_error", "Kubernetes resource query failed"},
	)
}
