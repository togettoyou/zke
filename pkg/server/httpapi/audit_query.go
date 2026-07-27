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

// auditQueryFilters are the audit trail's own selectors. They go through the
// shared list parser like every other filter so an unsupported or misspelled
// parameter is refused instead of widening the result set unnoticed.
var auditQueryFilters = []string{
	"actor_type",
	"result",
	"action",
	"target_type",
	"request_id",
	"tenant_id",
	"project_id",
	"cluster_id",
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
	query, queryErr := parseListQuery(c, listFilters{extra: auditQueryFilters})
	if queryErr != nil {
		writeError(c, http.StatusBadRequest, "invalid_request", "invalid audit query")
		return
	}
	ctx, cancel := handler.operationContext(c)
	result, err := handler.service.Query(ctx, audit.QueryInput{
		UserID:     identity.User.ID,
		ActorType:  query.Filter("actor_type"),
		Result:     query.Filter("result"),
		Action:     query.Filter("action"),
		TargetType: query.Filter("target_type"),
		RequestID:  query.Filter("request_id"),
		TenantID:   query.Filter("tenant_id"),
		ProjectID:  query.Filter("project_id"),
		ClusterID:  query.Filter("cluster_id"),
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
