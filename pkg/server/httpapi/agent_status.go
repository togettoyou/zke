package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/agentstatus"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

type agentStatusHandler struct {
	baseHandler
	service     *agentstatus.Service
	authService *auth.Service
	rbacService *rbac.Service
}

var agentStatusErrors = []errorMapping{
	{agentstatus.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid Cluster query"},
	{agentstatus.ErrNotFound, http.StatusNotFound, "not_found", "Cluster not found"},
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
	ID             string                    `json:"id"`
	TenantID       string                    `json:"tenant_id"`
	ProjectID      string                    `json:"project_id"`
	Name           string                    `json:"name"`
	AgentNamespace string                    `json:"agent_namespace"`
	Status         string                    `json:"status"`
	CreatedAt      time.Time                 `json:"created_at"`
	UpdatedAt      time.Time                 `json:"updated_at"`
	Connection     clusterConnectionResponse `json:"connection"`
}

func newAgentStatusHandler(
	logger *slog.Logger,
	service *agentstatus.Service,
	authService *auth.Service,
	rbacService *rbac.Service,
	operationTimeout time.Duration,
) *agentStatusHandler {
	return &agentStatusHandler{
		baseHandler: newBaseHandler(logger, nil, operationTimeout),
		service:     service,
		authService: authService,
		rbacService: rbacService,
	}
}

func (handler *agentStatusHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	query, queryErr := parseListQuery(c, listFilters{search: true, status: true})
	if queryErr != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Cluster query")
		return
	}
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Cluster status is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.ListProject(ctx, agentstatus.ListProjectInput{
		ProjectID: c.Param("project_id"),
		Status:    query.Status,
		Search:    query.Search,
		Now:       time.Now().UTC(),
		Page:      query.Page,
	})
	cancel()
	if handler.respondError(c, "list Cluster connection status", err, agentStatusErrors...) {
		return
	}
	response := make([]agentStatusResponse, 0, len(result.Agents))
	for _, item := range result.Agents {
		response = append(response, responseAgentStatus(item))
	}
	writeSuccess(c, http.StatusOK, gin.H{
		"clusters":   response,
		"pagination": responsePagination(result.Page),
	})
}

func (handler *agentStatusHandler) getCluster(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if handler.service == nil {
		writeError(c, http.StatusServiceUnavailable, "unavailable", "Cluster status is unavailable")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.GetCluster(
		ctx,
		c.Param("cluster_id"),
		time.Now().UTC(),
	)
	cancel()
	if handler.respondError(c, "get Cluster connection status", err, agentStatusErrors...) {
		return
	}
	writeSuccess(c, http.StatusOK, responseAgentStatus(result))
}

func responseAgentStatus(item agentstatus.Agent) agentStatusResponse {
	return agentStatusResponse{
		ID:             item.ClusterID,
		TenantID:       item.TenantID,
		ProjectID:      item.ProjectID,
		Name:           item.ClusterName,
		AgentNamespace: item.AgentNamespace,
		Status:         item.ClusterStatus,
		CreatedAt:      responseTime(item.ClusterCreatedAt),
		UpdatedAt:      responseTime(item.ClusterUpdatedAt),
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
