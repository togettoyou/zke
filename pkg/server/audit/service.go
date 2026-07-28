package audit

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/pagination"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

// Aliases, not a second definition: the vocabulary lives in
// `pkg/server/auditaction`, which the store can import without a cycle.
const (
	ActionClusterEnrollmentCreate   = auditaction.ClusterEnrollmentCreate
	ActionClusterEnrollmentRevoke   = auditaction.ClusterEnrollmentRevoke
	ActionClusterConnectionRevoke   = auditaction.ClusterConnectionRevoke
	ActionClusterConnectionReenroll = auditaction.ClusterConnectionReenroll
	ActionTenantCreate              = auditaction.TenantCreate
	ActionProjectCreate             = auditaction.ProjectCreate
)

type Service struct {
	store         Store
	authorization *rbac.Service
}

type Event struct {
	ID           string
	ActorType    string
	ActorUserID  string
	ActorAgentID string
	ScopeType    string
	TenantID     string
	ProjectID    string
	ClusterID    string
	Action       string
	TargetType   string
	TargetID     string
	Result       string
	RequestID    string
	CreatedAt    time.Time
}

type QueryInput struct {
	UserID     string
	ActorType  string
	Result     string
	Action     string
	TargetType string
	RequestID  string
	TenantID   string
	ProjectID  string
	ClusterID  string
	Page       pagination.Request
}

type QueryResult struct {
	Events []Event
	Page   pagination.Result
}

var ErrInvalidQuery = errors.New("invalid audit query")

type ProjectEventInput struct {
	ActorUserID string
	ProjectID   string
	Action      string
	Result      string
	RequestID   string
}

type GlobalEventInput struct {
	ActorUserID string
	Action      string
	TargetType  string
	Result      string
	RequestID   string
}

type TenantEventInput struct {
	ActorUserID string
	TenantID    string
	Action      string
	TargetType  string
	Result      string
	RequestID   string
}

type AgentEventInput struct {
	ActorUserID string
	AgentID     string
	Action      string
	Result      string
	RequestID   string
}

type ClusterEventInput struct {
	ActorUserID string
	ClusterID   string
	Action      string
	Result      string
	RequestID   string
}

// NewService requires the authorization service rather than accepting it
// optionally: Query cannot filter an audit trail to the caller's visible scope
// without it, and a missing dependency must fail at composition time instead of
// surfacing as a rejected query at runtime.
func NewService(auditStore Store, authorization *rbac.Service) *Service {
	return &Service{store: auditStore, authorization: authorization}
}

// Query returns one page of the audit events the caller may read. Visibility
// is resolved once and pushed into the query so the reported total counts only
// events inside the caller's Tenant and Project scope.
func (service *Service) Query(
	ctx context.Context,
	input QueryInput,
) (QueryResult, error) {
	if !validation.IsUUID(input.UserID) ||
		input.Page.Validate() != nil ||
		!validOptionalUUID(input.TenantID) ||
		!validOptionalUUID(input.ProjectID) ||
		!validOptionalUUID(input.ClusterID) ||
		!validAuditEnum(input.ActorType, "", "user", "agent", "system") ||
		!validAuditEnum(input.Result, "", "succeeded", "failed", "denied") {
		return QueryResult{}, ErrInvalidQuery
	}
	visibility, err := service.authorization.ResolveVisibility(
		ctx,
		input.UserID,
		rbac.PermissionAuditRead,
	)
	if err != nil {
		return QueryResult{}, err
	}
	if !visibility.HasAny() {
		return QueryResult{}, rbac.ErrDenied
	}
	records, total, err := service.store.ListRecords(ctx, store.ListAuditRecordsParams{
		GlobalVisible: visibility.IsGlobal(),
		TenantIDs:     visibility.TenantIDs(),
		ProjectIDs:    visibility.ProjectIDs(),
		ActorType:     input.ActorType,
		Result:        input.Result,
		Action:        input.Action,
		TargetType:    input.TargetType,
		RequestID:     input.RequestID,
		TenantID:      input.TenantID,
		ProjectID:     input.ProjectID,
		ClusterID:     input.ClusterID,
		Page:          input.Page,
	})
	if err != nil {
		return QueryResult{}, err
	}
	events := make([]Event, 0, len(records))
	for _, item := range records {
		events = append(events, eventFromStore(item))
	}
	return QueryResult{
		Events: events,
		Page:   pagination.NewResult(input.Page, total, len(events)),
	}, nil
}

func validOptionalUUID(value string) bool {
	return value == "" || validation.IsUUID(value)
}

func validAuditEnum(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func eventFromStore(item store.AuditRecord) Event {
	return Event{
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
		CreatedAt:    item.CreatedAt,
	}
}

func (service *Service) RecordGlobalEvent(
	ctx context.Context,
	input GlobalEventInput,
) error {
	if !validBaseEvent(
		input.ActorUserID,
		input.Action,
		input.Result,
		input.RequestID,
	) || strings.TrimSpace(input.TargetType) == "" {
		return errors.New("audit event fields are invalid")
	}
	return service.store.RecordGlobalEvent(ctx, store.GlobalAuditEvent{
		ActorUserID: input.ActorUserID,
		Action:      input.Action,
		TargetType:  input.TargetType,
		Result:      input.Result,
		RequestID:   input.RequestID,
	})
}

func (service *Service) RecordTenantEvent(
	ctx context.Context,
	input TenantEventInput,
) error {
	if !validBaseEvent(
		input.ActorUserID,
		input.Action,
		input.Result,
		input.RequestID,
	) || strings.TrimSpace(input.TargetType) == "" {
		return errors.New("audit event fields are invalid")
	}
	if validation.IsUUID(input.TenantID) {
		return service.store.RecordTenantEvent(ctx, store.TenantAuditEvent{
			ActorUserID: input.ActorUserID,
			TenantID:    input.TenantID,
			Action:      input.Action,
			TargetType:  input.TargetType,
			Result:      input.Result,
			RequestID:   input.RequestID,
		})
	}
	return service.RecordGlobalEvent(ctx, GlobalEventInput{
		ActorUserID: input.ActorUserID,
		Action:      input.Action,
		TargetType:  input.TargetType,
		Result:      input.Result,
		RequestID:   input.RequestID,
	})
}

func (service *Service) RecordProjectEvent(
	ctx context.Context,
	input ProjectEventInput,
) error {
	if !validation.IsUUID(input.ActorUserID) ||
		strings.TrimSpace(input.Action) == "" ||
		strings.TrimSpace(input.RequestID) == "" ||
		(input.Result != "failed" && input.Result != "denied") {
		return errors.New("audit event fields are invalid")
	}
	if validation.IsUUID(input.ProjectID) {
		return service.store.RecordProjectEvent(ctx, store.ProjectAuditEvent{
			ActorUserID: input.ActorUserID,
			ProjectID:   input.ProjectID,
			Action:      input.Action,
			Result:      input.Result,
			RequestID:   input.RequestID,
		})
	}
	return service.store.RecordGlobalEvent(ctx, store.GlobalAuditEvent{
		ActorUserID: input.ActorUserID,
		Action:      input.Action,
		TargetType:  "project",
		Result:      input.Result,
		RequestID:   input.RequestID,
	})
}

func (service *Service) RecordClusterEvent(
	ctx context.Context,
	input ClusterEventInput,
) error {
	if !validBaseEvent(
		input.ActorUserID,
		input.Action,
		input.Result,
		input.RequestID,
	) {
		return errors.New("audit event fields are invalid")
	}
	if validation.IsUUID(input.ClusterID) {
		return service.store.RecordClusterEvent(ctx, store.ClusterAuditEvent{
			ActorUserID: input.ActorUserID,
			ClusterID:   input.ClusterID,
			Action:      input.Action,
			Result:      input.Result,
			RequestID:   input.RequestID,
		})
	}
	return service.RecordGlobalEvent(ctx, GlobalEventInput{
		ActorUserID: input.ActorUserID,
		Action:      input.Action,
		TargetType:  "cluster",
		Result:      input.Result,
		RequestID:   input.RequestID,
	})
}

func (service *Service) RecordAgentEvent(
	ctx context.Context,
	input AgentEventInput,
) error {
	if !validation.IsUUID(input.ActorUserID) ||
		!validation.IsUUID(input.AgentID) ||
		strings.TrimSpace(input.Action) == "" ||
		strings.TrimSpace(input.RequestID) == "" ||
		(input.Result != "failed" && input.Result != "denied") {
		return errors.New("audit event fields are invalid")
	}
	return service.store.RecordAgentEvent(ctx, store.AgentAuditEvent{
		ActorUserID: input.ActorUserID,
		AgentID:     input.AgentID,
		Action:      input.Action,
		Result:      input.Result,
		RequestID:   input.RequestID,
	})
}

func validBaseEvent(
	actorUserID string,
	action string,
	result string,
	requestID string,
) bool {
	return validation.IsUUID(actorUserID) &&
		strings.TrimSpace(action) != "" &&
		strings.TrimSpace(requestID) != "" &&
		(result == "failed" || result == "denied")
}
