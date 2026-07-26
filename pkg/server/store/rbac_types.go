package store

import "github.com/jackc/pgx/v5/pgxpool"

type RBACStore struct {
	pool *pgxpool.Pool
}

type RoleBinding struct {
	Role      string
	ScopeType string
	TenantID  string
	ProjectID string
}

type ClusterAuthorizationScope struct {
	TenantID  string
	ProjectID string
}

func NewRBACStore(pool *pgxpool.Pool) *RBACStore {
	return &RBACStore{pool: pool}
}
