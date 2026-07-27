package agentmanagement

import (
	"context"

	"github.com/togettoyou/zke/pkg/server/store"
)

// Store is the persistence surface Cluster connection management needs.
type Store interface {
	Revoke(ctx context.Context, params store.RevokeAgentParams) (store.RevokeAgentResult, error)
}
