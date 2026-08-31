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

type Service struct {
	store         Store
	authorization *rbac.Service
}

// Event mirrors the store record, names included: an audit trail that outlives
// its subjects has to carry what they were called.
type Event struct {
	ID            string
	ActorType     string
	ActorUserID   string
	ActorUserName string
	ActorAgentID  string
	ScopeType     string
	TenantID      string
	TenantName    string
	ProjectID     string
	ProjectName   string
	ClusterID     string
	ClusterName   string
	Action        string
	TargetType    string
	TargetID      string
	TargetName    string
	Result        string
	RequestID     string
	ActorIP       string
	Detail        map[string]string
	CreatedAt     time.Time
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
	// Actions, Since and DetailContains are internal correlation filters. The
	// HTTP audit list deliberately remains an exact one-action filter; AIOps
	// needs a closed mutation set plus the `mutating=true` marker on its own tool
	// audit rows without pulling unrelated reads into model context.
	Actions        []string
	Since          time.Time
	DetailContains map[string]string
	Page           pagination.Request
}

type QueryResult struct {
	Events []Event
	Page   pagination.Result
}

var ErrInvalidQuery = errors.New("invalid audit query")

// ActorIP and Detail are optional on every event input. ActorIP is the client
// address of the request behind the event; Detail is the structured reason for
// its Result, as short stable keys. See the store event types for what may and
// may not be put in Detail.
type ProjectEventInput struct {
	ActorUserID string
	ProjectID   string
	ProjectName string
	Action      string
	TargetType  string
	TargetID    string
	TargetName  string
	Result      string
	RequestID   string
	ActorIP     string
	Detail      map[string]string
}

type GlobalEventInput struct {
	ActorUserID string
	Action      string
	TargetType  string
	TargetID    string
	TargetName  string
	Result      string
	RequestID   string
	ActorIP     string
	Detail      map[string]string
}

type TenantEventInput struct {
	ActorUserID string
	TenantID    string
	TenantName  string
	Action      string
	TargetType  string
	TargetID    string
	TargetName  string
	Result      string
	RequestID   string
	ActorIP     string
	Detail      map[string]string
}

type AgentEventInput struct {
	ActorUserID string
	AgentID     string
	Action      string
	Result      string
	RequestID   string
	ActorIP     string
	Detail      map[string]string
}

type ClusterEventInput struct {
	ActorUserID string
	ClusterID   string
	ClusterName string
	Action      string
	TargetType  string
	TargetID    string
	TargetName  string
	Result      string
	RequestID   string
	ActorIP     string
	Detail      map[string]string
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
		!validAuditEnum(input.Result, "", "succeeded", "failed", "denied") ||
		!validAuditActions(input.Actions) || len(input.DetailContains) > 16 {
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
		GlobalVisible:  visibility.IsGlobal(),
		TenantIDs:      visibility.TenantIDs(),
		ProjectIDs:     visibility.ProjectIDs(),
		ActorType:      input.ActorType,
		Result:         input.Result,
		Action:         input.Action,
		TargetType:     input.TargetType,
		RequestID:      input.RequestID,
		TenantID:       input.TenantID,
		ProjectID:      input.ProjectID,
		ClusterID:      input.ClusterID,
		Actions:        append([]string{}, input.Actions...),
		Since:          input.Since,
		DetailContains: cloneDetail(input.DetailContains),
		Page:           input.Page,
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

func validAuditActions(actions []string) bool {
	if len(actions) > 64 {
		return false
	}
	for _, action := range actions {
		if !auditaction.Known(action) {
			return false
		}
	}
	return true
}

func cloneDetail(detail map[string]string) map[string]string {
	if len(detail) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(detail))
	for key, value := range detail {
		cloned[key] = value
	}
	return cloned
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
		ID:            item.ID,
		ActorType:     item.ActorType,
		ActorUserID:   item.ActorUserID,
		ActorUserName: item.ActorUserName,
		ActorAgentID:  item.ActorAgentID,
		ScopeType:     item.ScopeType,
		TenantID:      item.TenantID,
		TenantName:    item.TenantName,
		ProjectID:     item.ProjectID,
		ProjectName:   item.ProjectName,
		ClusterID:     item.ClusterID,
		ClusterName:   item.ClusterName,
		Action:        item.Action,
		TargetType:    item.TargetType,
		TargetID:      item.TargetID,
		TargetName:    item.TargetName,
		Result:        item.Result,
		RequestID:     item.RequestID,
		ActorIP:       item.ActorIP,
		Detail:        item.Detail,
		CreatedAt:     item.CreatedAt,
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
		TargetID:    validAuditTargetID(input.TargetID),
		TargetName:  input.TargetName,
		Result:      input.Result,
		RequestID:   input.RequestID,
		ActorIP:     input.ActorIP,
		Detail:      input.Detail,
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
			TenantName:  input.TenantName,
			Action:      input.Action,
			TargetType:  input.TargetType,
			TargetID:    validAuditTargetID(input.TargetID),
			TargetName:  input.TargetName,
			Result:      input.Result,
			RequestID:   input.RequestID,
			ActorIP:     input.ActorIP,
			Detail:      input.Detail,
		})
	}
	return service.RecordGlobalEvent(ctx, GlobalEventInput{
		ActorUserID: input.ActorUserID,
		Action:      input.Action,
		TargetType:  input.TargetType,
		TargetID:    input.TargetID,
		TargetName:  input.TargetName,
		Result:      input.Result,
		RequestID:   input.RequestID,
		ActorIP:     input.ActorIP,
		Detail:      input.Detail,
	})
}

func (service *Service) RecordProjectEvent(
	ctx context.Context,
	input ProjectEventInput,
) error {
	if !validBaseEvent(
		input.ActorUserID,
		input.Action,
		input.Result,
		input.RequestID,
	) {
		return errors.New("audit event fields are invalid")
	}
	targetType := input.TargetType
	if strings.TrimSpace(targetType) == "" {
		targetType = auditaction.TargetProject
	}
	targetID := input.TargetID
	if targetID == "" && targetType == auditaction.TargetProject {
		targetID = input.ProjectID
	}
	targetID = validAuditTargetID(targetID)
	if validation.IsUUID(input.ProjectID) {
		return service.store.RecordProjectEvent(ctx, store.ProjectAuditEvent{
			ActorUserID: input.ActorUserID,
			ProjectID:   input.ProjectID,
			ProjectName: input.ProjectName,
			Action:      input.Action,
			TargetType:  targetType,
			TargetID:    targetID,
			TargetName:  input.TargetName,
			Result:      input.Result,
			RequestID:   input.RequestID,
			ActorIP:     input.ActorIP,
			Detail:      input.Detail,
		})
	}
	return service.store.RecordGlobalEvent(ctx, store.GlobalAuditEvent{
		ActorUserID: input.ActorUserID,
		Action:      input.Action,
		TargetType:  targetType,
		TargetID:    targetID,
		TargetName:  input.TargetName,
		Result:      input.Result,
		RequestID:   input.RequestID,
		ActorIP:     input.ActorIP,
		Detail:      input.Detail,
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
	targetType := input.TargetType
	if strings.TrimSpace(targetType) == "" {
		targetType = auditaction.TargetCluster
	}
	targetID := input.TargetID
	if targetID == "" && targetType == auditaction.TargetCluster {
		targetID = input.ClusterID
	}
	targetID = validAuditTargetID(targetID)
	if validation.IsUUID(input.ClusterID) {
		return service.store.RecordClusterEvent(ctx, store.ClusterAuditEvent{
			ActorUserID: input.ActorUserID,
			ClusterID:   input.ClusterID,
			ClusterName: input.ClusterName,
			Action:      input.Action,
			TargetType:  targetType,
			TargetID:    targetID,
			TargetName:  input.TargetName,
			Result:      input.Result,
			RequestID:   input.RequestID,
			ActorIP:     input.ActorIP,
			Detail:      input.Detail,
		})
	}
	return service.RecordGlobalEvent(ctx, GlobalEventInput{
		ActorUserID: input.ActorUserID,
		Action:      input.Action,
		TargetType:  targetType,
		TargetID:    targetID,
		TargetName:  input.TargetName,
		Result:      input.Result,
		RequestID:   input.RequestID,
		ActorIP:     input.ActorIP,
		Detail:      input.Detail,
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
		ActorIP:     input.ActorIP,
		Detail:      input.Detail,
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
		(result == "succeeded" || result == "failed" || result == "denied")
}

// validAuditTargetID keeps a malformed request path from destroying the audit
// event that records its failure. Target ids are UUID columns; an invalid path
// segment is request context, not a resource identity that can be stored there.
func validAuditTargetID(value string) string {
	if validation.IsUUID(value) {
		return value
	}
	return ""
}
