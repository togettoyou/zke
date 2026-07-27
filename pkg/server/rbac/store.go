package rbac

import (
	"context"

	"github.com/togettoyou/zke/pkg/server/store"
)

// Store is the persistence surface authorization needs. It is deliberately
// read-only: an authorization decision must never mutate state.
type Store interface {
	ListRoleBindings(ctx context.Context, userID string) ([]store.RoleBinding, error)
	FindProjectTenant(ctx context.Context, projectID string) (string, error)
	FindClusterAuthorizationScope(ctx context.Context, clusterID string) (store.ClusterAuthorizationScope, error)
}
