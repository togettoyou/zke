package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
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

// auditActionResponse is one entry of the audit action vocabulary. The group is
// declared by the Server rather than split from the name, because the names are
// not a reliable tree — `cluster.delete` and `cluster.enrollment.create` sit at
// different depths in the same family.
type auditActionResponse struct {
	Name  string `json:"name"`
	Group string `json:"group"`
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
		TargetType:  auditaction.TargetAuditEvent,
		Result:      "denied",
	})
}

// listActions reports the audit action vocabulary.
//
// The filter it feeds is an exact match, so a free-text box only ever worked for
// someone who already knew the spelling. The vocabulary is closed and the Server
// owns it, so the Server is what should say what the values are — a list copied
// into the Console would be a second definition that nothing keeps in step.
//
// Deliberately not derived from the caller's permissions: those say what an
// operator may do, these say what happened, and they are different vocabularies.
// The list is also not filtered by what the caller can see — it is the shape of
// the system, not its contents, and the events themselves remain scoped.
// The target types ride along for the same reason: `target_type` is a second
// closed vocabulary behind a second exact-match filter, and publishing one while
// leaving the other to be guessed only moves the problem.
func (handler *auditQueryHandler) listActions(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	actions := make([]auditActionResponse, 0, len(auditaction.All()))
	for _, item := range auditaction.All() {
		actions = append(actions, auditActionResponse{Name: item.Name, Group: item.Group})
	}
	writeSuccess(c, http.StatusOK, gin.H{
		"audit_actions":      actions,
		"audit_target_types": auditaction.TargetTypes(),
	})
}
