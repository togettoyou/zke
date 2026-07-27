package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

type auditQueryHandler struct {
	baseHandler
	service *audit.Service
}

var auditQueryErrors = []errorMapping{
	{audit.ErrInvalidQuery, http.StatusBadRequest, "invalid_request", "invalid audit query"},
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
		baseHandler: newBaseHandler(logger, service, operationTimeout),
		service:     service,
	}
}

func (handler *auditQueryHandler) list(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	identity, _ := httpmiddleware.Identity(c)
	query, queryErr := parseListQuery(c, listFilters{})
	if queryErr != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid audit query")
		return
	}
	ctx, cancel := handler.operationContext(c)
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
		Page:       query.Page,
	})
	cancel()
	if errors.Is(err, rbac.ErrDenied) {
		handler.recordDenied(c, identity.User.ID)
		writeError(c, http.StatusForbidden, "forbidden", "permission denied")
		return
	}
	if handler.respondError(c, "query audit events", err, auditQueryErrors...) {
		return
	}
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
		"pagination":   responsePagination(result.Page),
	})
}

func (handler *auditQueryHandler) recordDenied(c *gin.Context, userID string) {
	handler.recordFailure(c, failedOperation{
		Scope:       auditScopeGlobal,
		ActorUserID: userID,
		Action:      string(rbac.PermissionAuditRead),
		TargetType:  "audit_event",
		Result:      "denied",
	})
}
