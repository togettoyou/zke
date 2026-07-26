package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/agentstatus"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

type agentStatusHandler struct {
	logger           *slog.Logger
	service          *agentstatus.Service
	operationTimeout time.Duration
	authService      *auth.Service
	rbacService      *rbac.Service
}

type clusterConnectionResponse struct {
	Status                      string     `json:"status"`
	LifecycleStatus             string     `json:"lifecycle_status"`
	HealthStatus                string     `json:"health_status"`
	Version                     string     `json:"version"`
	ProtocolVersion             string     `json:"protocol_version"`
	LastSeenAt                  *time.Time `json:"last_seen_at,omitempty"`
	CertificateSerial           string     `json:"certificate_serial"`
	CertificateExpiresAt        time.Time  `json:"certificate_expires_at"`
	CertificateRemainingSeconds int64      `json:"certificate_remaining_seconds"`
	CertificateStatus           string     `json:"certificate_status"`
	ConnectionID                string     `json:"connection_id,omitempty"`
	ConnectedAt                 *time.Time `json:"connected_at,omitempty"`
	LastHeartbeatAt             *time.Time `json:"last_heartbeat_at,omitempty"`
	LastDisconnectedAt          *time.Time `json:"last_disconnected_at,omitempty"`
	LastDisconnectReason        string     `json:"last_disconnect_reason,omitempty"`
}

type agentStatusResponse struct {
	ID         string                    `json:"id"`
	TenantID   string                    `json:"tenant_id"`
	ProjectID  string                    `json:"project_id"`
	Name       string                    `json:"name"`
	Status     string                    `json:"status"`
	CreatedAt  time.Time                 `json:"created_at"`
	UpdatedAt  time.Time                 `json:"updated_at"`
	Connection clusterConnectionResponse `json:"connection"`
}

func newAgentStatusHandler(
	logger *slog.Logger,
	service *agentstatus.Service,
	authService *auth.Service,
	rbacService *rbac.Service,
	operationTimeout time.Duration,
) *agentStatusHandler {
	return &agentStatusHandler{
		logger: logger, service: service, authService: authService,
		rbacService: rbacService, operationTimeout: operationTimeout,
	}
}

func (handler *agentStatusHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	query, queryErr := parseListQuery(c)
	if queryErr != nil ||
		!allowed(query.Status, "pending", "active", "revoked") ||
		query.Role != "" || query.ScopeType != "" {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Cluster query")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Cluster status is unavailable")
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
			"list Cluster connection status",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("project_id", c.Param("project_id")),
			slog.String("error", err.Error()),
		)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	default:
		response := make([]agentStatusResponse, 0, len(result))
		for _, item := range result {
			if query.Status != "" && item.ClusterStatus != query.Status {
				continue
			}
			if !containsFold(query.Search, item.ClusterID, item.ClusterName) {
				continue
			}
			response = append(response, responseAgentStatus(item))
		}
		response, pagination := paginate(response, query)
		c.JSON(http.StatusOK, gin.H{
			"clusters":   response,
			"pagination": pagination,
		})
	}
}

func (handler *agentStatusHandler) getCluster(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Cluster status is unavailable")
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
		writeError(c, http.StatusNotFound, "not_found", "Cluster not found")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	case err != nil:
		handler.logger.Error(
			"get Cluster connection status",
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
		ID:        item.ClusterID,
		TenantID:  item.TenantID,
		ProjectID: item.ProjectID,
		Name:      item.ClusterName,
		Status:    item.ClusterStatus,
		CreatedAt: responseTime(item.ClusterCreatedAt),
		UpdatedAt: responseTime(item.ClusterUpdatedAt),
		Connection: clusterConnectionResponse{
			Status:                      item.ConnectionStatus,
			LifecycleStatus:             item.LifecycleStatus,
			HealthStatus:                item.HealthStatus,
			Version:                     item.AgentVersion,
			ProtocolVersion:             item.ProtocolVersion,
			LastSeenAt:                  responseTimePointer(item.LastSeenAt),
			CertificateSerial:           item.CertificateSerial,
			CertificateExpiresAt:        responseTime(item.CertificateExpiresAt),
			CertificateRemainingSeconds: item.CertificateRemainingSeconds,
			CertificateStatus:           item.CertificateStatus,
			ConnectionID:                item.ConnectionID,
			ConnectedAt:                 responseTimePointer(item.ConnectedAt),
			LastHeartbeatAt:             responseTimePointer(item.LastHeartbeatAt),
			LastDisconnectedAt:          responseTimePointer(item.LastDisconnectedAt),
			LastDisconnectReason:        item.LastDisconnectReason,
		},
	}
}
