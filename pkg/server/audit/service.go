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
	ActionTenantCreate          = "tenant.create"
	ActionProjectCreate         = "project.create"
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

func NewService(auditStore *store.AuditStore) *Service {
	return &Service{store: auditStore}
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
