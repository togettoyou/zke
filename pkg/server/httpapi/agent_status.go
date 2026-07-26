package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/agentstatus"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
)

type agentStatusHandler struct {
	logger           *slog.Logger
	service          *agentstatus.Service
	operationTimeout time.Duration
}

type agentStatusResponse struct {
	ClusterID                   string     `json:"cluster_id"`
	ClusterName                 string     `json:"cluster_name"`
	AgentID                     string     `json:"agent_id"`
	LifecycleStatus             string     `json:"lifecycle_status"`
	HealthStatus                string     `json:"health_status"`
	LastSeenAt                  *time.Time `json:"last_seen_at,omitempty"`
	CertificateSerial           string     `json:"certificate_serial"`
	CertificateExpiresAt        time.Time  `json:"certificate_expires_at"`
	CertificateRemainingSeconds int64      `json:"certificate_remaining_seconds"`
	CertificateStatus           string     `json:"certificate_status"`
	ConnectionStatus            string     `json:"connection_status"`
	ConnectionID                string     `json:"connection_id,omitempty"`
	ConnectedAt                 *time.Time `json:"connected_at,omitempty"`
	LastHeartbeatAt             *time.Time `json:"last_heartbeat_at,omitempty"`
	LastDisconnectedAt          *time.Time `json:"last_disconnected_at,omitempty"`
	LastDisconnectReason        string     `json:"last_disconnect_reason,omitempty"`
}

func newAgentStatusHandler(
	logger *slog.Logger,
	service *agentstatus.Service,
	operationTimeout time.Duration,
) *agentStatusHandler {
	return &agentStatusHandler{
		logger: logger, service: service, operationTimeout: operationTimeout,
	}
}

func (handler *agentStatusHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Agent status is unavailable")
		return
	}
	operationContext, cancel := context.WithTimeout(
		c.Request.Context(),
		handler.operationTimeout,
	)
	result, err := handler.service.ListProject(
		operationContext,
		c.Param("project_id"),
		time.Now().UTC(),
	)
	cancel()
	switch {
	case errors.Is(err, agentstatus.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid project")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	case err != nil:
		handler.logger.Error(
			"list Agent certificate status",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("project_id", c.Param("project_id")),
			slog.String("error", err.Error()),
		)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	default:
		response := make([]agentStatusResponse, 0, len(result))
		for _, item := range result {
			response = append(response, responseAgentStatus(item))
		}
		c.JSON(http.StatusOK, gin.H{"agents": response})
	}
}

func (handler *agentStatusHandler) getCluster(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Agent status is unavailable")
		return
	}
	operationContext, cancel := context.WithTimeout(
		c.Request.Context(),
		handler.operationTimeout,
	)
	result, err := handler.service.GetCluster(
		operationContext,
		c.Param("cluster_id"),
		time.Now().UTC(),
	)
	cancel()
	switch {
	case errors.Is(err, agentstatus.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid cluster")
	case errors.Is(err, agentstatus.ErrNotFound):
		writeError(c, http.StatusNotFound, "not_found", "Agent not found")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	case err != nil:
		handler.logger.Error(
			"get cluster Agent status",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("cluster_id", c.Param("cluster_id")),
			slog.String("error", err.Error()),
		)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	default:
		c.JSON(http.StatusOK, responseAgentStatus(result))
	}
}

func responseAgentStatus(item agentstatus.Agent) agentStatusResponse {
	return agentStatusResponse{
		ClusterID:                   item.ClusterID,
		ClusterName:                 item.ClusterName,
		AgentID:                     item.AgentID,
		LifecycleStatus:             item.LifecycleStatus,
		HealthStatus:                item.HealthStatus,
		LastSeenAt:                  responseTimePointer(item.LastSeenAt),
		CertificateSerial:           item.CertificateSerial,
		CertificateExpiresAt:        responseTime(item.CertificateExpiresAt),
		CertificateRemainingSeconds: item.CertificateRemainingSeconds,
		CertificateStatus:           item.CertificateStatus,
		ConnectionStatus:            item.ConnectionStatus,
		ConnectionID:                item.ConnectionID,
		ConnectedAt:                 responseTimePointer(item.ConnectedAt),
		LastHeartbeatAt:             responseTimePointer(item.LastHeartbeatAt),
		LastDisconnectedAt:          responseTimePointer(item.LastDisconnectedAt),
		LastDisconnectReason:        item.LastDisconnectReason,
	}
}
