package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AIRuntimeStore holds only the account-state predicate needed by detached
// AIOps work. Background jobs deliberately do not retain a browser Session or
// its bearer token; they run as the initiating user and recheck this row plus
// current RBAC before every model or evidence step.
type AIRuntimeStore struct {
	pool *pgxpool.Pool
}

func NewAIRuntimeStore(pool *pgxpool.Pool) *AIRuntimeStore {
	return &AIRuntimeStore{pool: pool}
}

func (store *AIRuntimeStore) IsAIUserActive(ctx context.Context, userID string) (bool, error) {
	var active bool
	if err := store.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM users WHERE id = $1::uuid AND status = 'active')",
		userID,
	).Scan(&active); err != nil {
		return false, fmt.Errorf("check AIOps user state: %w", err)
	}
	return active, nil
}
