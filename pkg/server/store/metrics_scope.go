package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MetricsScopeStore answers one question: may this caller's metrics query
// read this Cluster. It is separate from the Cluster status store because the
// answer feeds a label filter rather than a list a user reads: it carries no
// Agent, certificate or connection state, and it must stay cheap enough to run
// on every query — and every chart on screen is a query.
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

// ErrClusterNotVisible reports a Cluster the caller may not read metrics for.
// It does not distinguish "does not exist" from "not yours": telling them apart
// would let somebody probe for Cluster identifiers outside their scope.
var ErrClusterNotVisible = errors.New("cluster is not visible for metrics")

// VisibleClusterParams describes a resolved RBAC visibility. Global sees
// everything; otherwise the two sets are unioned, which matches how a
// Tenant-scoped binding and a Project-scoped binding combine.
type VisibleClusterParams struct {
	Global     bool
	TenantIDs  []string
	ProjectIDs []string
}

// GetVisibleCluster resolves one Cluster inside a visibility, or reports that
// it is out of scope.
//
// One row rather than the whole visible list: a query names exactly one target
// Cluster, and a Console page holding a dozen charts would otherwise read every
// Cluster the caller can see a dozen times over to answer a membership test
// that the primary key already answers.
func (store *MetricsScopeStore) GetVisibleCluster(
	ctx context.Context,
	params VisibleClusterParams,
	clusterID string,
) (ClusterScope, error) {
	if !params.Global && len(params.TenantIDs) == 0 && len(params.ProjectIDs) == 0 {
		return ClusterScope{}, ErrClusterNotVisible
	}
	var scope ClusterScope
	err := store.pool.QueryRow(
		ctx,
		`
SELECT id, name, tenant_id, project_id, status
FROM clusters
WHERE id = $1
  AND (
       $2
    OR tenant_id = ANY ($3::uuid[])
    OR project_id = ANY ($4::uuid[])
  )
`,
		clusterID,
		params.Global,
		params.TenantIDs,
		params.ProjectIDs,
	).Scan(
		&scope.ClusterID,
		&scope.ClusterName,
		&scope.TenantID,
		&scope.ProjectID,
		&scope.Status,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClusterScope{}, ErrClusterNotVisible
	}
	if err != nil {
		return ClusterScope{}, fmt.Errorf("get visible Cluster: %w", err)
	}
	return scope, nil
}
