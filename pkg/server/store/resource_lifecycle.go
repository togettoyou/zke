package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/togettoyou/zke/pkg/server/auditaction"
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
	var previousStatus string
	if err := transaction.QueryRow(ctx, `
SELECT status
FROM tenants
WHERE id = $1
FOR UPDATE
`, params.TenantID).Scan(&previousStatus); errors.Is(err, pgx.ErrNoRows) {
		return TenantResource{}, ErrTenantNotFound
	} else if err != nil {
		return TenantResource{}, fmt.Errorf("lock tenant update: %w", err)
	}
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
	// An UPDATE cannot absorb the conflict the way the insert does, so the
	// unique index reports it. Nothing further is read from this transaction,
	// which the failed statement has already aborted.
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "23505" {
		return TenantResource{}, ErrTenantNameConflict
	}
	if err != nil {
		return TenantResource{}, fmt.Errorf("update tenant: %w", err)
	}
	if err := insertResourceMutationAudit(
		ctx, transaction, params.ActorUserID, "global", "", "", "",
		lifecycleAuditAction(
			previousStatus,
			item.Status,
			auditaction.TenantUpdate,
			auditaction.TenantSuspend,
			auditaction.TenantResume,
		),
		auditaction.TargetTenant, params.TenantID, params.RequestID, params.Now,
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
SELECT id::text, name, status, created_at, updated_at
FROM tenants
WHERE id = $1
  AND EXISTS (SELECT 1 FROM users WHERE id = $2 AND status = 'active')
FOR UPDATE
`, params.TenantID, params.ActorUserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return TenantResource{}, ErrTenantNotFound
	}
	if err != nil {
		return TenantResource{}, fmt.Errorf("lock tenant deletion: %w", err)
	}
	// Audited before anything is removed: the trigger that snapshots the Tenant
	// and actor names into the event can only read rows that still exist, and
	// this event is the last record that the Tenant was ever here.
	if err := insertResourceMutationAudit(
		ctx, transaction, params.ActorUserID, "global", "", "", "",
		auditaction.TenantDelete, auditaction.TargetTenant, params.TenantID, params.RequestID, params.Now,
	); err != nil {
		return TenantResource{}, err
	}
	if err := deleteTenantTree(ctx, transaction, params.TenantID); err != nil {
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
	var previousStatus string
	if err := transaction.QueryRow(ctx, `
SELECT status
FROM projects
WHERE id = $1
FOR UPDATE
`, params.ProjectID).Scan(&previousStatus); errors.Is(err, pgx.ErrNoRows) {
		return ProjectResource{}, ErrProjectNotFound
	} else if err != nil {
		return ProjectResource{}, fmt.Errorf("lock project update: %w", err)
	}
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
	// The unique index reports a rename onto a taken name; the failed statement
	// has already aborted this transaction and nothing further is read from it.
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "23505" {
		return ProjectResource{}, ErrProjectNameConflict
	}
	if err != nil {
		return ProjectResource{}, fmt.Errorf("update project: %w", err)
	}
	if err := insertResourceMutationAudit(
		ctx, transaction, params.ActorUserID, "tenant", item.TenantID, "", "",
		lifecycleAuditAction(
			previousStatus,
			item.Status,
			auditaction.ProjectUpdate,
			auditaction.ProjectSuspend,
			auditaction.ProjectResume,
		),
		auditaction.TargetProject, item.ID, params.RequestID, params.Now,
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
SELECT id::text, tenant_id::text, name, status, created_at, updated_at
FROM projects
WHERE id = $1
  AND EXISTS (SELECT 1 FROM users WHERE id = $2 AND status = 'active')
FOR UPDATE
`, params.ProjectID, params.ActorUserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectResource{}, ErrProjectNotFound
	}
	if err != nil {
		return ProjectResource{}, fmt.Errorf("lock project deletion: %w", err)
	}
	if err := insertResourceMutationAudit(
		ctx, transaction, params.ActorUserID, "tenant", item.TenantID, "", "",
		auditaction.ProjectDelete, auditaction.TargetProject, item.ID, params.RequestID, params.Now,
	); err != nil {
		return ProjectResource{}, err
	}
	if err := deleteProjectTree(ctx, transaction, params.ProjectID); err != nil {
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
	if _, err := transaction.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		params.ClusterID,
	); err != nil {
		return ClusterResource{}, fmt.Errorf("lock Cluster update: %w", err)
	}
	var projectID, previousStatus string
	if err := transaction.QueryRow(ctx, `
SELECT project_id::text, status
FROM clusters
WHERE id = $1
FOR UPDATE
`, params.ClusterID).Scan(&projectID, &previousStatus); errors.Is(err, pgx.ErrNoRows) {
		return ClusterResource{}, ErrClusterNotFound
	} else if err != nil {
		return ClusterResource{}, fmt.Errorf("lock cluster update: %w", err)
	}
	if _, err := transaction.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || lower($2), 0))",
		projectID,
		params.Name,
	); err != nil {
		return ClusterResource{}, fmt.Errorf("lock Cluster update name: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE enrollments
SET revoked_at = $3
WHERE project_id = $1
  AND consumed_at IS NULL
  AND revoked_at IS NULL
  AND expires_at <= $3
  AND (
      lower(cluster_name) = lower($2)
      OR cluster_id = $4
  )
`, projectID, params.Name, params.Now, params.ClusterID); err != nil {
		return ClusterResource{}, fmt.Errorf("release expired Cluster enrollment name: %w", err)
	}
	var enrollmentNameTaken bool
	if err := transaction.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM enrollments
    WHERE project_id = $1
      AND lower(cluster_name) = lower($2)
      AND cluster_id IS DISTINCT FROM $3::uuid
      AND consumed_at IS NULL
      AND revoked_at IS NULL
      AND expires_at > $4
)
`, projectID, params.Name, params.ClusterID, params.Now).Scan(&enrollmentNameTaken); err != nil {
		return ClusterResource{}, fmt.Errorf("check Cluster enrollment name: %w", err)
	}
	if enrollmentNameTaken {
		return ClusterResource{}, ErrClusterNameConflict
	}
	item, err := scanCluster(transaction.QueryRow(ctx, `
UPDATE clusters AS cluster
SET name = $2,
    -- A pending Cluster that is resumed stays pending: it has never connected,
    -- and only an Agent activating it may call it active.
    status = CASE
        WHEN $5 = 'suspended' THEN 'suspended'
        WHEN cluster.status = 'suspended' THEN 'pending'
        ELSE cluster.status
    END,
    updated_at = GREATEST(cluster.updated_at, $4)
FROM projects AS project, tenants AS tenant
WHERE cluster.id = $1
  AND project.id = cluster.project_id
  AND tenant.id = cluster.tenant_id
  AND project.status = 'active'
  AND tenant.status = 'active'
  AND EXISTS (SELECT 1 FROM users WHERE id = $3 AND status = 'active')
RETURNING cluster.id::text, cluster.tenant_id::text, cluster.project_id::text,
    cluster.name, cluster.status, cluster.last_seen_at,
    cluster.created_at, cluster.updated_at
`, params.ClusterID, params.Name, params.ActorUserID, params.Now, params.Status))
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
	var clusterNameConflict *pgconn.PgError
	if errors.As(err, &clusterNameConflict) && clusterNameConflict.Code == "23505" {
		return ClusterResource{}, ErrClusterNameConflict
	}
	if err != nil {
		return ClusterResource{}, fmt.Errorf("update cluster: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE enrollments
SET cluster_name = $2
WHERE cluster_id = $1
  AND consumed_at IS NULL
  AND revoked_at IS NULL
  AND expires_at > $3
`, params.ClusterID, params.Name, params.Now); err != nil {
		var enrollmentNameConflict *pgconn.PgError
		if errors.As(err, &enrollmentNameConflict) &&
			enrollmentNameConflict.Code == "23505" {
			return ClusterResource{}, ErrClusterNameConflict
		}
		return ClusterResource{}, fmt.Errorf("rename active Cluster enrollment: %w", err)
	}
	if err := insertResourceMutationAudit(
		ctx, transaction, params.ActorUserID, "cluster", item.TenantID,
		item.ProjectID, item.ID,
		lifecycleAuditAction(
			previousStatus,
			item.Status,
			auditaction.ClusterUpdate,
			auditaction.ClusterSuspend,
			auditaction.ClusterResume,
		),
		auditaction.TargetCluster, item.ID,
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
SELECT id::text, tenant_id::text, project_id::text, name, status,
    last_seen_at, created_at, updated_at
FROM clusters
WHERE id = $1
  AND EXISTS (SELECT 1 FROM users WHERE id = $2 AND status = 'active')
FOR UPDATE
`, params.ClusterID, params.ActorUserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ClusterResource{}, ErrClusterNotFound
	}
	if err != nil {
		return ClusterResource{}, fmt.Errorf("lock cluster deletion: %w", err)
	}
	if err := insertResourceMutationAudit(
		ctx, transaction, params.ActorUserID, "cluster", item.TenantID,
		item.ProjectID, item.ID, auditaction.ClusterDelete, auditaction.TargetCluster, item.ID,
		params.RequestID, params.Now,
	); err != nil {
		return ClusterResource{}, err
	}
	if err := deleteClusterTree(ctx, transaction, params.ClusterID); err != nil {
		return ClusterResource{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ClusterResource{}, fmt.Errorf("commit cluster deletion: %w", err)
	}
	return item, nil
}

func lifecycleAuditAction(
	previousStatus string,
	nextStatus string,
	updateAction string,
	suspendAction string,
	resumeAction string,
) string {
	switch {
	case previousStatus != "suspended" && nextStatus == "suspended":
		return suspendAction
	case previousStatus == "suspended" && nextStatus != "suspended":
		return resumeAction
	default:
		return updateAction
	}
}

// Deletion removes rows; it does not mark them.
//
// Order follows the foreign keys inwards, because they are still real between
// these tables — only the audit trail was cut loose. Deleting the Agent rows is
// what ends any live connection: the AFTER DELETE trigger on `agents` notifies
// the connection manager, which drops the session immediately rather than
// waiting for the credential to be noticed missing at the next heartbeat.
//
// Callers write their audit event before calling these, while the names can
// still be read.
func deleteClusterTree(
	ctx context.Context,
	transaction pgx.Tx,
	clusterID string,
) error {
	for _, statement := range []struct {
		sql         string
		description string
	}{
		{`DELETE FROM enrollment_attempts
WHERE enrollment_id IN (SELECT id FROM enrollments WHERE cluster_id = $1)`,
			"cluster enrollment attempts"},
		{"DELETE FROM enrollments WHERE cluster_id = $1", "cluster enrollments"},
		{"DELETE FROM agent_credentials WHERE cluster_id = $1", "cluster Agent credentials"},
		{"DELETE FROM agents WHERE cluster_id = $1", "cluster Agents"},
		{"DELETE FROM clusters WHERE id = $1", "cluster"},
	} {
		if _, err := transaction.Exec(ctx, statement.sql, clusterID); err != nil {
			return fmt.Errorf("delete %s: %w", statement.description, err)
		}
	}
	return nil
}

func deleteProjectTree(
	ctx context.Context,
	transaction pgx.Tx,
	projectID string,
) error {
	// A narrower Cluster deletion locks Cluster then Enrollment. Lock every
	// Cluster first so deleting the whole Project follows the same order instead
	// of taking an Enrollment that a concurrent Cluster deletion is waiting for.
	if _, err := transaction.Exec(ctx, `
SELECT id
FROM clusters
WHERE project_id = $1
ORDER BY id
FOR UPDATE
`, projectID); err != nil {
		return fmt.Errorf("lock project clusters for deletion: %w", err)
	}
	for _, statement := range []struct {
		sql         string
		description string
	}{
		{`DELETE FROM enrollment_attempts
WHERE enrollment_id IN (SELECT id FROM enrollments WHERE project_id = $1)`,
			"project enrollment attempts"},
		{"DELETE FROM enrollments WHERE project_id = $1", "project enrollments"},
		{"DELETE FROM agent_credentials WHERE project_id = $1", "project Agent credentials"},
		{"DELETE FROM agents WHERE project_id = $1", "project Agents"},
		{"DELETE FROM clusters WHERE project_id = $1", "project clusters"},
		{"DELETE FROM project_creation_requests WHERE project_id = $1", "project creation request"},
		{"DELETE FROM role_bindings WHERE project_id = $1", "project role bindings"},
		{"DELETE FROM projects WHERE id = $1", "project"},
	} {
		if _, err := transaction.Exec(ctx, statement.sql, projectID); err != nil {
			return fmt.Errorf("delete %s: %w", statement.description, err)
		}
	}
	return nil
}

func deleteTenantTree(
	ctx context.Context,
	transaction pgx.Tx,
	tenantID string,
) error {
	// Follow the hierarchy before touching dependants. Project and Cluster
	// deletion use the same order, so concurrent deletion at a narrower scope
	// can finish without forming a cycle with this transaction.
	if _, err := transaction.Exec(ctx, `
SELECT id
FROM projects
WHERE tenant_id = $1
ORDER BY id
FOR UPDATE
`, tenantID); err != nil {
		return fmt.Errorf("lock tenant projects for deletion: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
SELECT id
FROM clusters
WHERE tenant_id = $1
ORDER BY project_id, id
FOR UPDATE
`, tenantID); err != nil {
		return fmt.Errorf("lock tenant clusters for deletion: %w", err)
	}
	for _, statement := range []struct {
		sql         string
		description string
	}{
		{`DELETE FROM enrollment_attempts
WHERE enrollment_id IN (SELECT id FROM enrollments WHERE tenant_id = $1)`,
			"tenant enrollment attempts"},
		{"DELETE FROM enrollments WHERE tenant_id = $1", "tenant enrollments"},
		{"DELETE FROM agent_credentials WHERE tenant_id = $1", "tenant Agent credentials"},
		{"DELETE FROM agents WHERE tenant_id = $1", "tenant Agents"},
		{"DELETE FROM clusters WHERE tenant_id = $1", "tenant clusters"},
		{"DELETE FROM project_creation_requests WHERE tenant_id = $1", "tenant project creation requests"},
		{"DELETE FROM tenant_creation_requests WHERE tenant_id = $1", "tenant creation request"},
		{"DELETE FROM role_bindings WHERE tenant_id = $1", "tenant role bindings"},
		{"DELETE FROM projects WHERE tenant_id = $1", "tenant projects"},
		{"DELETE FROM tenants WHERE id = $1", "tenant"},
	} {
		if _, err := transaction.Exec(ctx, statement.sql, tenantID); err != nil {
			return fmt.Errorf("delete %s: %w", statement.description, err)
		}
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
