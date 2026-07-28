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
	ErrNotFound     = errors.New("Cluster connection not found")
)

type Service struct {
	store  Store
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

// NewService takes the status event publisher as a required parameter. A
// revocation that never reaches the Console leaves it showing a Cluster as
// connected, so a missing publisher must be visible at composition time rather
// than as a Console that quietly stops converging.
func NewService(agentStore Store, events StatusEventPublisher) *Service {
	return &Service{store: agentStore, events: events}
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
	if errors.Is(err, store.ErrAgentNotFound) {
		return RevokeResult{}, ErrNotFound
	}
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
