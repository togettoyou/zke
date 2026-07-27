package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (store *EnrollmentStore) GetClusterEnrollmentTarget(
	ctx context.Context,
	clusterID string,
) (ClusterEnrollmentTarget, error) {
	var result ClusterEnrollmentTarget
	err := store.pool.QueryRow(ctx, `
SELECT project_id::text, name
FROM clusters
WHERE id = $1 AND status <> 'revoked'
`, clusterID).Scan(&result.ProjectID, &result.ClusterName)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClusterEnrollmentTarget{}, ErrClusterNotFound
	}
	if err != nil {
		return ClusterEnrollmentTarget{}, fmt.Errorf("get Cluster reenrollment target: %w", err)
	}
	return result, nil
}

// enrollmentFilterSQL derives the lifecycle status the same way the service
// does, but in SQL, so that a status filter can be applied before paging
// instead of after loading every enrollment in the project.
const enrollmentFilterSQL = `
FROM enrollments
WHERE project_id = $1
  AND (
    $2 = ''
    OR $2 = CASE
        WHEN revoked_at IS NOT NULL THEN 'revoked'
        WHEN consumed_at IS NOT NULL THEN 'consumed'
        WHEN expires_at <= $3 THEN 'expired'
        ELSE 'active'
    END
  )
  AND (
    $4 = ''
    OR position($4 IN id::text) > 0
    OR position($4 IN COALESCE(cluster_id::text, '')) > 0
    OR position($4 IN lower(cluster_name)) > 0
  )
`

func (store *EnrollmentStore) ListEnrollments(
	ctx context.Context,
	params ListEnrollmentsParams,
) ([]Enrollment, int, error) {
	return queryPage(
		ctx,
		store.pool,
		"SELECT count(*) "+enrollmentFilterSQL,
		`
SELECT id::text, tenant_id::text, project_id::text,
    COALESCE(cluster_id::text, ''), cluster_name,
    created_by_user_id::text, expires_at, consumed_at, revoked_at, created_at
`+enrollmentFilterSQL+`
ORDER BY created_at DESC, id
LIMIT $5 OFFSET $6
`,
		[]any{params.ProjectID, params.Status, params.Now, params.Search},
		params.Page,
		func(rows pgx.Rows) (Enrollment, error) {
			return scanManagedEnrollment(rows)
		},
		"Cluster enrollments",
	)
}

func (store *EnrollmentStore) GetEnrollment(
	ctx context.Context,
	projectID string,
	enrollmentID string,
) (Enrollment, error) {
	item, err := scanManagedEnrollment(store.pool.QueryRow(ctx, `
SELECT id::text, tenant_id::text, project_id::text,
    COALESCE(cluster_id::text, ''), cluster_name,
    created_by_user_id::text, expires_at, consumed_at, revoked_at, created_at
FROM enrollments
WHERE project_id = $1 AND id = $2
`, projectID, enrollmentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Enrollment{}, ErrEnrollmentNotFound
	}
	if err != nil {
		return Enrollment{}, fmt.Errorf("get Cluster enrollment: %w", err)
	}
	return item, nil
}

func (store *EnrollmentStore) RevokeEnrollment(
	ctx context.Context,
	params RevokeEnrollmentParams,
) (Enrollment, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Enrollment{}, fmt.Errorf("begin Cluster enrollment revocation: %w", err)
	}
	defer rollbackTransaction(transaction)
	item, err := scanManagedEnrollment(transaction.QueryRow(ctx, `
SELECT id::text, tenant_id::text, project_id::text,
    COALESCE(cluster_id::text, ''), cluster_name,
    created_by_user_id::text, expires_at, consumed_at, revoked_at, created_at
FROM enrollments
WHERE project_id = $1 AND id = $2
FOR UPDATE
`, params.ProjectID, params.EnrollmentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Enrollment{}, ErrEnrollmentNotFound
	}
	if err != nil {
		return Enrollment{}, fmt.Errorf("lock Cluster enrollment: %w", err)
	}
	if item.ConsumedAt != nil {
		return Enrollment{}, ErrEnrollmentStateConflict
	}
	if item.RevokedAt == nil {
		item.RevokedAt = &params.Now
		if _, err := transaction.Exec(ctx, `
UPDATE enrollments SET revoked_at = $2 WHERE id = $1
`, item.ID, params.Now); err != nil {
			return Enrollment{}, fmt.Errorf("revoke Cluster enrollment: %w", err)
		}
	}
	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id, actor_type, actor_user_id, scope_type, tenant_id, project_id,
    action, target_type, target_id, result, request_id, created_at
)
VALUES (
    gen_random_uuid(), 'user', $1, 'project', $2, $3,
    'cluster.enrollment.revoke', 'enrollment', $4, 'succeeded', $5, $6
)
`, params.ActorUserID, item.TenantID, item.ProjectID, item.ID,
		params.RequestID, params.Now); err != nil {
		return Enrollment{}, fmt.Errorf("audit Cluster enrollment revocation: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return Enrollment{}, fmt.Errorf("commit Cluster enrollment revocation: %w", err)
	}
	return item, nil
}

func scanManagedEnrollment(row rowScanner) (Enrollment, error) {
	var item Enrollment
	err := row.Scan(
		&item.ID, &item.TenantID, &item.ProjectID, &item.ClusterID,
		&item.ClusterName,
		&item.CreatedByUserID, &item.ExpiresAt, &item.ConsumedAt,
		&item.RevokedAt, &item.CreatedAt,
	)
	return item, err
}
