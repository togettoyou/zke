package audit

import (
	"context"
	"errors"
	"strings"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

const (
	ActionAgentEnrollmentCreate = "agent.enrollment.create"
	ActionAgentRevoke           = "agent.revoke"
)

type Service struct {
	store *store.AuditStore
}

type ProjectEventInput struct {
	ActorUserID string
	ProjectID   string
	Action      string
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

func NewService(auditStore *store.AuditStore) *Service {
	return &Service{store: auditStore}
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
