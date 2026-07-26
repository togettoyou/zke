package audit

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

const (
	ActionAgentEnrollmentCreate = "agent.enrollment.create"
	ActionAgentRevoke           = "agent.revoke"
	ActionTenantCreate          = "tenant.create"
	ActionProjectCreate         = "project.create"
)

type Service struct {
	store         *store.AuditStore
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
	Cursor     string
	Limit      int
}

type QueryResult struct {
	Events     []Event
	NextCursor string
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

func NewService(
	auditStore *store.AuditStore,
	authorization ...*rbac.Service,
) *Service {
	service := &Service{store: auditStore}
	if len(authorization) > 0 {
		service.authorization = authorization[0]
	}
	return service
}

func (service *Service) Query(
	ctx context.Context,
	input QueryInput,
) (QueryResult, error) {
	if service.authorization == nil ||
		!validation.IsUUID(input.UserID) ||
		input.Limit < 1 ||
		input.Limit > 100 ||
		!validOptionalUUID(input.TenantID) ||
		!validOptionalUUID(input.ProjectID) ||
		!validOptionalUUID(input.ClusterID) ||
		!validAuditEnum(input.ActorType, "", "user", "agent", "system") ||
		!validAuditEnum(input.Result, "", "succeeded", "failed", "denied") {
		return QueryResult{}, ErrInvalidQuery
	}
	beforeAt, beforeID, err := decodeCursor(input.Cursor)
	if err != nil {
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
	records, err := service.store.ListRecords(ctx, store.ListAuditRecordsParams{
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
		BeforeAt:      beforeAt,
		BeforeID:      beforeID,
		Limit:         input.Limit + 1,
	})
	if err != nil {
		return QueryResult{}, err
	}
	var nextCursor string
	if len(records) > input.Limit {
		records = records[:input.Limit]
		last := records[len(records)-1]
		nextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	events := make([]Event, 0, len(records))
	for _, item := range records {
		events = append(events, eventFromStore(item))
	}
	return QueryResult{Events: events, NextCursor: nextCursor}, nil
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

func encodeCursor(at time.Time, id string) string {
	value := strconv.FormatInt(at.UnixNano(), 10) + ":" + id
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeCursor(value string) (*time.Time, string, error) {
	if value == "" {
		return nil, "", nil
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return nil, "", err
	}
	parts := strings.Split(string(decoded), ":")
	if len(parts) != 2 || !validation.IsUUID(parts[1]) {
		return nil, "", errors.New("invalid audit cursor")
	}
	nanoseconds, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, "", fmt.Errorf("parse audit cursor: %w", err)
	}
	at := time.Unix(0, nanoseconds).UTC()
	return &at, parts[1], nil
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
