package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/agentmanagement"
	"github.com/togettoyou/zke/pkg/server/audit"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
)

const maxRevokeAgentRequestBytes = 4 * 1024

type agentManagementHandler struct {
	baseHandler
	service *agentmanagement.Service
}

var agentManagementErrors = []errorMapping{
	{agentmanagement.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "invalid Cluster"},
	{agentmanagement.ErrNotFound, http.StatusNotFound, "not_found", "Cluster connection not found"},
}

type revokeAgentRequest struct {
	Confirm bool `json:"confirm"`
}

type revokeAgentResponse struct {
	ClusterID        string    `json:"cluster_id"`
	ConnectionStatus string    `json:"connection_status"`
	RevokedAt        time.Time `json:"revoked_at"`
	AlreadyRevoked   bool      `json:"already_revoked"`
}

func newAgentManagementHandler(
	logger *slog.Logger,
	service *agentmanagement.Service,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *agentManagementHandler {
	return &agentManagementHandler{
		baseHandler: newBaseHandler(logger, auditService, operationTimeout),
		service:     service,
	}
}

func (handler *agentManagementHandler) revoke(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	if handler.service == nil {
		handler.recordRevokeFailure(c, identity.User.ID)
		writeError(
			c,
			http.StatusServiceUnavailable,
			"unavailable",
			"Cluster connection management is unavailable",
		)
		return
	}
	var request revokeAgentRequest
	if err := decodeJSONRequest(
		c,
		&request,
		maxRevokeAgentRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordRevokeFailure(c, identity.User.ID)
		writeError(
			c,
			http.StatusBadRequest,
			"confirmation_required",
			"Cluster connection revocation requires explicit confirmation",
		)
		return
	}
	operationContext, cancel := handler.operationContext(c)
	result, err := handler.service.Revoke(
		operationContext,
		agentmanagement.RevokeInput{
			ClusterID:   c.Param("cluster_id"),
			ActorUserID: identity.User.ID,
			RequestID:   httpmiddleware.RequestID(c),
			Now:         time.Now().UTC(),
		},
	)
	cancel()
	if err != nil {
		handler.recordRevokeFailure(c, identity.User.ID)
	}
	if handler.respondError(c, "revoke Cluster connection", err, agentManagementErrors...) {
		return
	}
	writeSuccess(c, http.StatusOK, revokeAgentResponse{
		ClusterID:        result.ClusterID,
		ConnectionStatus: "revoked",
		RevokedAt:        responseTime(result.RevokedAt),
		AlreadyRevoked:   result.AlreadyRevoked,
	})
}

func (handler *agentManagementHandler) recordRevokeFailure(
	c *gin.Context,
	userID string,
) {
	handler.recordFailure(c, failedOperation{
		Scope:       auditScopeCluster,
		ActorUserID: userID,
		Action:      audit.ActionClusterConnectionRevoke,
	})
}
