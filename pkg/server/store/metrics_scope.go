package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MetricsScopeStore answers one question: which Clusters may a caller's
// metrics query cover. It is separate from the Cluster status store because
// the answer feeds a label filter rather than a list a user reads: it carries
// no Agent, certificate or connection state, and it must stay cheap enough to
// run on every query.
type MetricsScopeStore struct {
	pool *pgxpool.Pool
}

func NewMetricsScopeStore(pool *pgxpool.Pool) *MetricsScopeStore {
	return &MetricsScopeStore{pool: pool}
}

// ClusterScope is one Cluster a query may read, with the ownership the Server
// holds today. Ownership is resolved per query rather than stored with the
// samples, because a Cluster can be moved between Projects and the samples
// written yesterday cannot be relabelled.
type ClusterScope struct {
	ClusterID   string
	ClusterName string
	TenantID    string
	ProjectID   string
	Status      string
}

// ListVisibleClustersParams describes a resolved RBAC visibility. Global sees
// everything; otherwise the two sets are unioned, which matches how a
// Tenant-scoped binding and a Project-scoped binding combine.
type ListVisibleClustersParams struct {
	Global     bool
	TenantIDs  []string
	ProjectIDs []string
	// Limit bounds the answer. A query covering more Clusters than this is
	// refused rather than silently narrowed, so nobody reads a partial view as
	// a complete one.
	Limit int
}

func (store *MetricsScopeStore) ListVisibleClusters(
	ctx context.Context,
	params ListVisibleClustersParams,
) ([]ClusterScope, error) {
	if !params.Global && len(params.TenantIDs) == 0 && len(params.ProjectIDs) == 0 {
		return nil, nil
	}
	if params.Limit <= 0 {
		return nil, fmt.Errorf("visible Cluster limit must be positive")
	}
	rows, err := store.pool.Query(
		ctx,
		`
SELECT id, name, tenant_id, project_id, status
FROM clusters
WHERE $1
   OR tenant_id = ANY ($2::uuid[])
   OR project_id = ANY ($3::uuid[])
ORDER BY name, id
LIMIT $4
`,
		params.Global,
		params.TenantIDs,
		params.ProjectIDs,
		params.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list visible Clusters: %w", err)
	}
	defer rows.Close()

	var scopes []ClusterScope
	for rows.Next() {
		var scope ClusterScope
		if err := rows.Scan(
			&scope.ClusterID,
			&scope.ClusterName,
			&scope.TenantID,
			&scope.ProjectID,
			&scope.Status,
		); err != nil {
			return nil, fmt.Errorf("scan visible Cluster: %w", err)
		}
		scopes = append(scopes, scope)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read visible Clusters: %w", err)
	}
	return scopes, nil
}
