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
	// The permission set the bound role carries, resolved by the same query
	// that reads the binding. Carrying it here rather than looking the role up
	// afterwards keeps an authorization decision to one round trip and removes
	// the window in which a role edited between the two reads would be
	// evaluated against a permission set it no longer has.
	Permissions []string
}

type ClusterAuthorizationScope struct {
	TenantID       string
	ProjectID      string
	AgentNamespace string
}

func NewRBACStore(pool *pgxpool.Pool) *RBACStore {
	return &RBACStore{pool: pool}
}
