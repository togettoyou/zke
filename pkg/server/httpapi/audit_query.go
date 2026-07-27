package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

type auditQueryHandler struct {
	logger           *slog.Logger
	service          *audit.Service
	operationTimeout time.Duration
}

type auditEventResponse struct {
	ID           string    `json:"id"`
	ActorType    string    `json:"actor_type"`
	ActorUserID  string    `json:"actor_user_id,omitempty"`
	ActorAgentID string    `json:"actor_agent_id,omitempty"`
	ScopeType    string    `json:"scope_type"`
	TenantID     string    `json:"tenant_id,omitempty"`
	ProjectID    string    `json:"project_id,omitempty"`
	ClusterID    string    `json:"cluster_id,omitempty"`
	Action       string    `json:"action"`
	TargetType   string    `json:"target_type"`
	TargetID     string    `json:"target_id,omitempty"`
	Result       string    `json:"result"`
	RequestID    string    `json:"request_id"`
	CreatedAt    time.Time `json:"created_at"`
}

func newAuditQueryHandler(
	logger *slog.Logger,
	service *audit.Service,
	operationTimeout time.Duration,
) *auditQueryHandler {
	return &auditQueryHandler{
		logger:           logger,
		service:          service,
		operationTimeout: operationTimeout,
	}
}

func (handler *auditQueryHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	limit := 50
	if value := c.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid_request", "invalid audit query")
			return
		}
		limit = parsed
	}
	ctx, cancel := context.WithTimeout(
		c.Request.Context(),
		handler.operationTimeout,
	)
	result, err := handler.service.Query(ctx, audit.QueryInput{
		UserID:     identity.User.ID,
		ActorType:  c.Query("actor_type"),
		Result:     c.Query("result"),
		Action:     c.Query("action"),
		TargetType: c.Query("target_type"),
		RequestID:  c.Query("request_id"),
		TenantID:   c.Query("tenant_id"),
		ProjectID:  c.Query("project_id"),
		ClusterID:  c.Query("cluster_id"),
		Cursor:     c.Query("cursor"),
		Limit:      limit,
	})
	cancel()
	switch {
	case errors.Is(err, audit.ErrInvalidQuery):
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid audit query")
	case errors.Is(err, rbac.ErrDenied):
		handler.recordDenied(c, identity.User.ID)
		writeError(c, http.StatusForbidden, "forbidden", "permission denied")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(c, http.StatusGatewayTimeout, "timeout", "request timed out")
	case err != nil:
		handler.logger.Error(
			"query audit events",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("error", err.Error()),
		)
		writeError(c, http.StatusInternalServerError, "internal_error", "internal server error")
	default:
		events := make([]auditEventResponse, 0, len(result.Events))
		for _, item := range result.Events {
			events = append(events, auditEventResponse{
				ID:           item.ID,
				ActorType:    item.ActorType,
				ActorUserID:  item.ActorUserID,
				ActorAgentID: item.ActorAgentID,
				ScopeType:    item.ScopeType,
				TenantID:     item.TenantID,
				ProjectID:    item.ProjectID,
				ClusterID:    item.ClusterID,
				Action:       item.Action,
				TargetType:   item.TargetType,
				TargetID:     item.TargetID,
				Result:       item.Result,
				RequestID:    item.RequestID,
				CreatedAt:    responseTime(item.CreatedAt),
			})
		}
		writeSuccess(c, http.StatusOK, gin.H{
			"audit_events": events,
			"next_cursor":  result.NextCursor,
		})
	}
}

func (handler *auditQueryHandler) recordDenied(
	c *gin.Context,
	userID string,
) {
	ctx, cancel := context.WithTimeout(
		c.Request.Context(),
		handler.operationTimeout,
	)
	err := handler.service.RecordGlobalEvent(ctx, audit.GlobalEventInput{
		ActorUserID: userID,
		Action:      string(rbac.PermissionAuditRead),
		TargetType:  "audit_event",
		Result:      "denied",
		RequestID:   httpmiddleware.RequestID(c),
	})
	cancel()
	if err != nil {
		handler.logger.Error(
			"record audit query denial",
			slog.String("request_id", httpmiddleware.RequestID(c)),
			slog.String("error", err.Error()),
		)
	}
}
