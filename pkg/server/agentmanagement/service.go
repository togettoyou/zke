package agentmanagement

import (
	"context"
	"errors"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

var (
	ErrInvalidInput = errors.New("invalid Agent management input")
	ErrNotFound     = store.ErrAgentNotFound
)

type Service struct {
	store  *store.AgentManagementStore
	events StatusEventPublisher
}

type StatusEventPublisher interface {
	PublishAgentStatusChange(
		tenantID string,
		projectID string,
		clusterID string,
		agentID string,
	)
}

type RevokeInput struct {
	ClusterID   string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type RevokeResult struct {
	ClusterID      string
	RevokedAt      time.Time
	AlreadyRevoked bool
}

func NewService(
	agentStore *store.AgentManagementStore,
	events ...StatusEventPublisher,
) *Service {
	service := &Service{store: agentStore}
	if len(events) > 0 {
		service.events = events[0]
	}
	return service
}

func (service *Service) Revoke(
	ctx context.Context,
	input RevokeInput,
) (RevokeResult, error) {
	if !validation.IsUUID(input.ClusterID) ||
		!validation.IsUUID(input.ActorUserID) ||
		input.RequestID == "" ||
		input.Now.IsZero() {
		return RevokeResult{}, ErrInvalidInput
	}
	result, err := service.store.Revoke(ctx, store.RevokeAgentParams{
		ClusterID:   input.ClusterID,
		ActorUserID: input.ActorUserID,
		RequestID:   input.RequestID,
		Now:         input.Now,
	})
	if err != nil {
		return RevokeResult{}, err
	}
	if service.events != nil && !result.AlreadyRevoked {
		service.events.PublishAgentStatusChange(
			result.TenantID,
			result.ProjectID,
			result.ClusterID,
			result.AgentID,
		)
	}
	return RevokeResult{
		ClusterID:      result.ClusterID,
		RevokedAt:      result.RevokedAt,
		AlreadyRevoked: result.AlreadyRevoked,
	}, nil
}
