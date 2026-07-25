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
	store *store.AgentManagementStore
}

type RevokeInput struct {
	AgentID     string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type RevokeResult struct {
	AgentID        string
	RevokedAt      time.Time
	AlreadyRevoked bool
}

func NewService(agentStore *store.AgentManagementStore) *Service {
	return &Service{store: agentStore}
}

func (service *Service) Revoke(
	ctx context.Context,
	input RevokeInput,
) (RevokeResult, error) {
	if !validation.IsUUID(input.AgentID) ||
		!validation.IsUUID(input.ActorUserID) ||
		input.RequestID == "" ||
		input.Now.IsZero() {
		return RevokeResult{}, ErrInvalidInput
	}
	result, err := service.store.Revoke(ctx, store.RevokeAgentParams{
		AgentID:     input.AgentID,
		ActorUserID: input.ActorUserID,
		RequestID:   input.RequestID,
		Now:         input.Now,
	})
	if err != nil {
		return RevokeResult{}, err
	}
	return RevokeResult{
		AgentID:        result.AgentID,
		RevokedAt:      result.RevokedAt,
		AlreadyRevoked: result.AlreadyRevoked,
	}, nil
}
