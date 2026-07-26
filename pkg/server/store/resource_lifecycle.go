package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func (store *ResourceManagementStore) UpdateTenant(
	ctx context.Context,
	params UpdateTenantParams,
) (TenantResource, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TenantResource{}, fmt.Errorf("begin tenant update: %w", err)
	}
	defer rollbackTransaction(transaction)
	item, err := scanTenant(transaction.QueryRow(ctx, `
UPDATE tenants
SET name = $2, status = $3, updated_at = GREATEST(updated_at, $5)
WHERE id = $1
  AND EXISTS (SELECT 1 FROM users WHERE id = $4 AND status = 'active')
RETURNING id::text, name, status, created_at, updated_at
`, params.TenantID, params.Name, params.Status, params.ActorUserID, params.Now))
	if errors.Is(err, pgx.ErrNoRows) {
		return TenantResource{}, ErrTenantNotFound
	}
	if err != nil {
		return TenantResource{}, fmt.Errorf("update tenant: %w", err)
	}
	if params.Status == "suspended" {
		if err := suspendTenantResources(ctx, transaction, params.TenantID, params.Now); err != nil {
			return TenantResource{}, err
		}
	}
	if err := insertResourceMutationAudit(
		ctx, transaction, params.ActorUserID, "global", "", "", "",
		"tenant.update", "tenant", params.TenantID, params.RequestID, params.Now,
	); err != nil {
		return TenantResource{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return TenantResource{}, fmt.Errorf("commit tenant update: %w", err)
	}
	return item, nil
}

func (store *ResourceManagementStore) DeleteTenant(
	ctx context.Context,
	params DeleteTenantParams,
) (TenantResource, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TenantResource{}, fmt.Errorf("begin tenant deletion: %w", err)
	}
	defer rollbackTransaction(transaction)
	item, err := scanTenant(transaction.QueryRow(ctx, `
UPDATE tenants
SET status = 'suspended', updated_at = GREATEST(updated_at, $3)
WHERE id = $1
  AND EXISTS (SELECT 1 FROM users WHERE id = $2 AND status = 'active')
RETURNING id::text, name, status, created_at, updated_at
`, params.TenantID, params.ActorUserID, params.Now))
	if errors.Is(err, pgx.ErrNoRows) {
		return TenantResource{}, ErrTenantNotFound
	}
	if err != nil {
		return TenantResource{}, fmt.Errorf("suspend tenant: %w", err)
	}
	if err := suspendTenantResources(ctx, transaction, params.TenantID, params.Now); err != nil {
		return TenantResource{}, err
	}
	if err := insertResourceMutationAudit(
		ctx, transaction, params.ActorUserID, "global", "", "", "",
		"tenant.delete", "tenant", params.TenantID, params.RequestID, params.Now,
	); err != nil {
		return TenantResource{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return TenantResource{}, fmt.Errorf("commit tenant deletion: %w", err)
	}
	return item, nil
}

func (store *ResourceManagementStore) UpdateProject(
	ctx context.Context,
	params UpdateProjectParams,
) (ProjectResource, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectResource{}, fmt.Errorf("begin project update: %w", err)
	}
	defer rollbackTransaction(transaction)
	item, err := scanProject(transaction.QueryRow(ctx, `
UPDATE projects AS project
SET name = $2, status = $3, updated_at = GREATEST(project.updated_at, $5)
FROM tenants AS tenant
WHERE project.id = $1
  AND tenant.id = project.tenant_id
  AND ($3 <> 'active' OR tenant.status = 'active')
  AND EXISTS (SELECT 1 FROM users WHERE id = $4 AND status = 'active')
RETURNING project.id::text, project.tenant_id::text, project.name,
    project.status, project.created_at, project.updated_at
`, params.ProjectID, params.Name, params.Status, params.ActorUserID, params.Now))
	if errors.Is(err, pgx.ErrNoRows) {
		var tenantStatus string
		queryErr := transaction.QueryRow(ctx, `
SELECT tenant.status
FROM projects AS project
JOIN tenants AS tenant ON tenant.id = project.tenant_id
WHERE project.id = $1
`, params.ProjectID).Scan(&tenantStatus)
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return ProjectResource{}, ErrProjectNotFound
		}
		if queryErr != nil {
			return ProjectResource{}, fmt.Errorf("check project update target: %w", queryErr)
		}
		if params.Status == "active" && tenantStatus != "active" {
			return ProjectResource{}, ErrResourceStateConflict
		}
		return ProjectResource{}, ErrResourceCreationNotAllowed
	}
	if err != nil {
		return ProjectResource{}, fmt.Errorf("update project: %w", err)
	}
	if params.Status == "suspended" {
		if err := suspendProjectResources(ctx, transaction, params.ProjectID, params.Now); err != nil {
			return ProjectResource{}, err
		}
	}
	if err := insertResourceMutationAudit(
		ctx, transaction, params.ActorUserID, "tenant", item.TenantID, "", "",
		"project.update", "project", item.ID, params.RequestID, params.Now,
	); err != nil {
		return ProjectResource{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ProjectResource{}, fmt.Errorf("commit project update: %w", err)
	}
	return item, nil
}

func (store *ResourceManagementStore) DeleteProject(
	ctx context.Context,
	params DeleteProjectParams,
) (ProjectResource, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectResource{}, fmt.Errorf("begin project deletion: %w", err)
	}
	defer rollbackTransaction(transaction)
	item, err := scanProject(transaction.QueryRow(ctx, `
UPDATE projects
SET status = 'suspended', updated_at = GREATEST(updated_at, $3)
WHERE id = $1
  AND EXISTS (SELECT 1 FROM users WHERE id = $2 AND status = 'active')
RETURNING id::text, tenant_id::text, name, status, created_at, updated_at
`, params.ProjectID, params.ActorUserID, params.Now))
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectResource{}, ErrProjectNotFound
	}
	if err != nil {
		return ProjectResource{}, fmt.Errorf("suspend project: %w", err)
	}
	if err := suspendProjectResources(ctx, transaction, params.ProjectID, params.Now); err != nil {
		return ProjectResource{}, err
	}
	if err := insertResourceMutationAudit(
		ctx, transaction, params.ActorUserID, "tenant", item.TenantID, "", "",
		"project.delete", "project", item.ID, params.RequestID, params.Now,
	); err != nil {
		return ProjectResource{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ProjectResource{}, fmt.Errorf("commit project deletion: %w", err)
	}
	return item, nil
}

func (store *ResourceManagementStore) UpdateCluster(
	ctx context.Context,
	params UpdateClusterParams,
) (ClusterResource, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ClusterResource{}, fmt.Errorf("begin cluster update: %w", err)
	}
	defer rollbackTransaction(transaction)
	item, err := scanCluster(transaction.QueryRow(ctx, `
UPDATE clusters AS cluster
SET name = $2, updated_at = GREATEST(cluster.updated_at, $4)
FROM projects AS project, tenants AS tenant
WHERE cluster.id = $1
  AND project.id = cluster.project_id
  AND tenant.id = cluster.tenant_id
  AND cluster.status <> 'revoked'
  AND project.status = 'active'
  AND tenant.status = 'active'
  AND EXISTS (SELECT 1 FROM users WHERE id = $3 AND status = 'active')
RETURNING cluster.id::text, cluster.tenant_id::text, cluster.project_id::text,
    cluster.name, cluster.status, cluster.last_seen_at,
    cluster.created_at, cluster.updated_at
`, params.ClusterID, params.Name, params.ActorUserID, params.Now))
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if queryErr := transaction.QueryRow(
			ctx,
			"SELECT EXISTS (SELECT 1 FROM clusters WHERE id = $1)",
			params.ClusterID,
		).Scan(&exists); queryErr != nil {
			return ClusterResource{}, fmt.Errorf("check cluster update target: %w", queryErr)
		}
		if !exists {
			return ClusterResource{}, ErrClusterNotFound
		}
		return ClusterResource{}, ErrResourceStateConflict
	}
	if err != nil {
		return ClusterResource{}, fmt.Errorf("update cluster: %w", err)
	}
	if err := insertResourceMutationAudit(
		ctx, transaction, params.ActorUserID, "cluster", item.TenantID,
		item.ProjectID, item.ID, "cluster.update", "cluster", item.ID,
		params.RequestID, params.Now,
	); err != nil {
		return ClusterResource{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ClusterResource{}, fmt.Errorf("commit cluster update: %w", err)
	}
	return item, nil
}

func (store *ResourceManagementStore) DeleteCluster(
	ctx context.Context,
	params DeleteClusterParams,
) (ClusterResource, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ClusterResource{}, fmt.Errorf("begin cluster deletion: %w", err)
	}
	defer rollbackTransaction(transaction)
	item, err := scanCluster(transaction.QueryRow(ctx, `
UPDATE clusters
SET status = 'revoked', updated_at = GREATEST(updated_at, $3)
WHERE id = $1
  AND EXISTS (SELECT 1 FROM users WHERE id = $2 AND status = 'active')
RETURNING id::text, tenant_id::text, project_id::text, name, status,
    last_seen_at, created_at, updated_at
`, params.ClusterID, params.ActorUserID, params.Now))
	if errors.Is(err, pgx.ErrNoRows) {
		return ClusterResource{}, ErrClusterNotFound
	}
	if err != nil {
		return ClusterResource{}, fmt.Errorf("revoke cluster: %w", err)
	}
	if err := revokeClusterResources(ctx, transaction, params.ClusterID, params.Now); err != nil {
		return ClusterResource{}, err
	}
	if err := insertResourceMutationAudit(
		ctx, transaction, params.ActorUserID, "cluster", item.TenantID,
		item.ProjectID, item.ID, "cluster.delete", "cluster", item.ID,
		params.RequestID, params.Now,
	); err != nil {
		return ClusterResource{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ClusterResource{}, fmt.Errorf("commit cluster deletion: %w", err)
	}
	return item, nil
}

func suspendTenantResources(
	ctx context.Context,
	transaction pgx.Tx,
	tenantID string,
	now time.Time,
) error {
	if _, err := transaction.Exec(ctx, `
UPDATE enrollments
SET revoked_at = COALESCE(revoked_at, $2)
WHERE tenant_id = $1 AND consumed_at IS NULL
`, tenantID, now); err != nil {
		return fmt.Errorf("revoke tenant enrollments: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE agent_credentials SET revoked_at = COALESCE(revoked_at, $2)
WHERE tenant_id = $1
`, tenantID, now); err != nil {
		return fmt.Errorf("revoke tenant Agent credentials: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE agents
SET lifecycle_status = 'revoked', health_status = 'unknown',
    updated_at = GREATEST(updated_at, $2)
WHERE tenant_id = $1
`, tenantID, now); err != nil {
		return fmt.Errorf("revoke tenant Agents: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE clusters SET status = 'revoked', updated_at = GREATEST(updated_at, $2)
WHERE tenant_id = $1
`, tenantID, now); err != nil {
		return fmt.Errorf("revoke tenant clusters: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE projects SET status = 'suspended', updated_at = GREATEST(updated_at, $2)
WHERE tenant_id = $1
`, tenantID, now); err != nil {
		return fmt.Errorf("suspend tenant projects: %w", err)
	}
	return nil
}

func suspendProjectResources(
	ctx context.Context,
	transaction pgx.Tx,
	projectID string,
	now time.Time,
) error {
	if _, err := transaction.Exec(ctx, `
UPDATE enrollments
SET revoked_at = COALESCE(revoked_at, $2)
WHERE project_id = $1 AND consumed_at IS NULL
`, projectID, now); err != nil {
		return fmt.Errorf("revoke project enrollments: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE agent_credentials SET revoked_at = COALESCE(revoked_at, $2)
WHERE project_id = $1
`, projectID, now); err != nil {
		return fmt.Errorf("revoke project Agent credentials: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE agents
SET lifecycle_status = 'revoked', health_status = 'unknown',
    updated_at = GREATEST(updated_at, $2)
WHERE project_id = $1
`, projectID, now); err != nil {
		return fmt.Errorf("revoke project Agents: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE clusters SET status = 'revoked', updated_at = GREATEST(updated_at, $2)
WHERE project_id = $1
`, projectID, now); err != nil {
		return fmt.Errorf("revoke project clusters: %w", err)
	}
	return nil
}

func revokeClusterResources(
	ctx context.Context,
	transaction pgx.Tx,
	clusterID string,
	now time.Time,
) error {
	if _, err := transaction.Exec(ctx, `
UPDATE agent_credentials SET revoked_at = COALESCE(revoked_at, $2)
WHERE cluster_id = $1
`, clusterID, now); err != nil {
		return fmt.Errorf("revoke cluster Agent credentials: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE agents
SET lifecycle_status = 'revoked', health_status = 'unknown',
    updated_at = GREATEST(updated_at, $2)
WHERE cluster_id = $1
`, clusterID, now); err != nil {
		return fmt.Errorf("revoke cluster Agents: %w", err)
	}
	return nil
}

func insertResourceMutationAudit(
	ctx context.Context,
	transaction pgx.Tx,
	actorUserID string,
	scopeType string,
	tenantID string,
	projectID string,
	clusterID string,
	action string,
	targetType string,
	targetID string,
	requestID string,
	now time.Time,
) error {
	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id, actor_type, actor_user_id, scope_type, tenant_id, project_id,
    cluster_id, action, target_type, target_id, result, request_id, created_at
)
VALUES (
    gen_random_uuid(), 'user', $1, $2, NULLIF($3, '')::uuid,
    NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, $6, $7, $8,
    'succeeded', $9, $10
)
`, actorUserID, scopeType, tenantID, projectID, clusterID, action,
		targetType, targetID, requestID, now); err != nil {
		return fmt.Errorf("audit resource mutation: %w", err)
	}
	return nil
}
