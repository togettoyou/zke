package httpapi

import (
	"context"
	"errors"
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
	logger           *slog.Logger
	service          *agentmanagement.Service
	auditService     *audit.Service
	operationTimeout time.Duration
}

type revokeAgentRequest struct {
	Confirm bool `json:"confirm"`
}

type revokeAgentResponse struct {
	AgentID         string    `json:"agent_id"`
	LifecycleStatus string    `json:"lifecycle_status"`
	RevokedAt       time.Time `json:"revoked_at"`
	AlreadyRevoked  bool      `json:"already_revoked"`
}

func newAgentManagementHandler(
	logger *slog.Logger,
	service *agentmanagement.Service,
	auditService *audit.Service,
	operationTimeout time.Duration,
) *agentManagementHandler {
	return &agentManagementHandler{
		logger:           logger,
		service:          service,
		auditService:     auditService,
		operationTimeout: operationTimeout,
	}
}

func (handler *agentManagementHandler) revoke(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	if handler.service == nil {
		handler.recordFailure(c, identity.User.ID)
		writeError(
			c,
			http.StatusServiceUnavailable,
			"unavailable",
			"Agent management is unavailable",
		)
		return
	}
	var request revokeAgentRequest
	if err := decodeJSONRequest(
		c,
		&request,
		maxRevokeAgentRequestBytes,
	); err != nil || !request.Confirm {
		handler.recordFailure(c, identity.User.ID)
		writeError(
			c,
			http.StatusBadRequest,
			"confirmation_required",
			"Agent revocation requires explicit confirmation",
		)
		return
	}
	operationContext, cancel := context.WithTimeout(
		c.Request.Context(),
		handler.operationTimeout,
	)
	result, err := handler.service.Revoke(
		operationContext,
		agentmanagement.RevokeInput{
			AgentID:     c.Param("agent_id"),
			ActorUserID: identity.User.ID,
			RequestID:   httpmiddleware.RequestID(c),
			Now:         time.Now().UTC(),
		},
	)
	cancel()
	switch {
	case errors.Is(err, agentmanagement.ErrInvalidInput):
		handler.recordFailure(c, identity.User.ID)
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid Agent")
	case errors.Is(err, agentmanagement.ErrNotFound):
		handler.recordFailure(c, identity.User.ID)
		writeError(c, http.StatusNotFound, "not_found", "Agent not found")
	case errors.Is(err, context.DeadlineExceeded):
		handler.recordFailure(c, identity.User.ID)
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	case err != nil:
		handler.recordFailure(c, identity.User.ID)
		handler.logger.Error(
			"revoke Agent",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("agent_id", c.Param("agent_id")),
			slog.String("error", err.Error()),
		)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	default:
		c.JSON(http.StatusOK, revokeAgentResponse{
			AgentID:         result.AgentID,
			LifecycleStatus: "revoked",
			RevokedAt:       result.RevokedAt,
			AlreadyRevoked:  result.AlreadyRevoked,
		})
	}
}

func (handler *agentManagementHandler) recordFailure(
	c *gin.Context,
	userID string,
) {
	if handler.auditService == nil {
		return
	}
	auditContext, cancel := context.WithTimeout(
		c.Request.Context(),
		handler.operationTimeout,
	)
	defer cancel()
	if err := handler.auditService.RecordAgentEvent(
		auditContext,
		audit.AgentEventInput{
			ActorUserID: userID,
			AgentID:     c.Param("agent_id"),
			Action:      audit.ActionAgentRevoke,
			Result:      "failed",
			RequestID:   httpmiddleware.RequestID(c),
		},
	); err != nil {
		handler.logger.Error(
			"record Agent revocation failure audit",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("user_id", userID),
			slog.String("agent_id", c.Param("agent_id")),
			slog.String("error", err.Error()),
		)
	}
}
