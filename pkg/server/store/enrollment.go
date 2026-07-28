package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/togettoyou/zke/pkg/server/auditaction"
)

func (store *EnrollmentStore) FindActiveEnrollmentByTokenDigest(
	ctx context.Context,
	tokenDigest []byte,
	now time.Time,
) (ActiveEnrollment, error) {
	if len(tokenDigest) != sha256.Size || now.IsZero() {
		return ActiveEnrollment{}, errors.New("active enrollment lookup fields are required")
	}
	var enrollment ActiveEnrollment
	err := store.pool.QueryRow(ctx, `
SELECT
    enrollment.id::text,
    enrollment.tenant_id::text,
    enrollment.project_id::text,
    COALESCE(enrollment.cluster_id::text, ''),
    enrollment.cluster_name,
    enrollment.expires_at
FROM enrollments AS enrollment
JOIN tenants AS tenant ON tenant.id = enrollment.tenant_id
JOIN projects AS project
  ON project.tenant_id = enrollment.tenant_id
 AND project.id = enrollment.project_id
WHERE enrollment.token_digest = $1
  AND enrollment.consumed_at IS NULL
  AND enrollment.revoked_at IS NULL
  AND enrollment.expires_at > $2
  AND tenant.status = 'active'
  AND project.status = 'active'
`, tokenDigest, now).Scan(
		&enrollment.ID,
		&enrollment.TenantID,
		&enrollment.ProjectID,
		&enrollment.ClusterID,
		&enrollment.ClusterName,
		&enrollment.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActiveEnrollment{}, ErrEnrollmentTokenRejected
	}
	if err != nil {
		return ActiveEnrollment{}, fmt.Errorf("find active enrollment: %w", err)
	}
	return enrollment, nil
}

func (store *EnrollmentStore) CreateEnrollment(
	ctx context.Context,
	input CreateEnrollmentParams,
) (Enrollment, error) {
	if strings.TrimSpace(input.ProjectID) == "" ||
		strings.TrimSpace(input.ClusterName) == "" ||
		strings.TrimSpace(input.CreatedByUserID) == "" ||
		strings.TrimSpace(input.RequestID) == "" ||
		strings.TrimSpace(input.IdempotencyKey) == "" ||
		input.ExpiresAt.IsZero() {
		return Enrollment{}, errors.New("enrollment creation fields are required")
	}
	if len(input.TokenDigest) != sha256.Size {
		return Enrollment{}, errors.New("enrollment token digest must use SHA-256")
	}

	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Enrollment{}, fmt.Errorf("begin enrollment creation transaction: %w", err)
	}
	defer rollbackTransaction(transaction)
	if input.ClusterID != "" {
		if _, err := transaction.Exec(
			ctx,
			"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
			input.ClusterID,
		); err != nil {
			return Enrollment{}, fmt.Errorf("lock Cluster reenrollment creation: %w", err)
		}
	}

	var created Enrollment
	err = transaction.QueryRow(ctx, `
INSERT INTO enrollments (
    id,
    tenant_id,
    project_id,
    cluster_id,
    cluster_name,
    token_digest,
    created_by_user_id,
    idempotency_key,
    expires_at
)
SELECT
    gen_random_uuid(),
    project.tenant_id,
    project.id,
    NULLIF($3, '')::uuid,
    $4,
    $5,
    users.id,
    $6,
    $7
FROM projects AS project
JOIN tenants AS tenant ON tenant.id = project.tenant_id
JOIN users ON users.id = $2
LEFT JOIN clusters AS cluster
  ON cluster.tenant_id = project.tenant_id
 AND cluster.project_id = project.id
 AND cluster.id = NULLIF($3, '')::uuid
WHERE project.id = $1
  AND project.status = 'active'
  AND tenant.status = 'active'
  AND users.status = 'active'
  AND (
      $3 = ''
      OR (
          cluster.id IS NOT NULL
          AND cluster.status <> 'revoked'
          AND NOT EXISTS (
              SELECT 1 FROM agents
              WHERE agents.cluster_id = cluster.id
                AND agents.lifecycle_status <> 'revoked'
          )
          AND NOT EXISTS (
              SELECT 1 FROM enrollments AS existing
              WHERE existing.cluster_id = cluster.id
                AND existing.consumed_at IS NULL
                AND existing.revoked_at IS NULL
                AND existing.expires_at > CURRENT_TIMESTAMP
          )
      )
  )
ON CONFLICT (created_by_user_id, project_id, idempotency_key) DO NOTHING
RETURNING
    id::text,
    tenant_id::text,
    project_id::text,
    COALESCE(cluster_id::text, ''),
    cluster_name,
    created_by_user_id::text,
    expires_at,
    created_at
`,
		input.ProjectID,
		input.CreatedByUserID,
		input.ClusterID,
		input.ClusterName,
		input.TokenDigest,
		input.IdempotencyKey,
		input.ExpiresAt,
	).Scan(
		&created.ID,
		&created.TenantID,
		&created.ProjectID,
		&created.ClusterID,
		&created.ClusterName,
		&created.CreatedByUserID,
		&created.ExpiresAt,
		&created.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		var conflict bool
		if queryErr := transaction.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM enrollments
    WHERE created_by_user_id = $1
      AND project_id = $2
      AND idempotency_key = $3
)
`,
			input.CreatedByUserID,
			input.ProjectID,
			input.IdempotencyKey,
		).Scan(&conflict); queryErr != nil {
			return Enrollment{}, fmt.Errorf("check enrollment idempotency conflict: %w", queryErr)
		}
		if conflict {
			return Enrollment{}, ErrEnrollmentIdempotencyConflict
		}
		return Enrollment{}, ErrEnrollmentCreationDenied
	}
	if err != nil {
		return Enrollment{}, fmt.Errorf("insert enrollment: %w", err)
	}

	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id,
    actor_type,
    actor_user_id,
    scope_type,
    tenant_id,
    project_id,
    cluster_id,
    action,
    target_type,
    target_id,
    result,
    request_id
)
VALUES (
    gen_random_uuid(),
    'user',
    $1,
    CASE WHEN $4 = '' THEN 'project' ELSE 'cluster' END,
    $2,
    $3,
    NULLIF($4, '')::uuid,
    CASE WHEN $4 = '' THEN $5 ELSE $6 END,
    $7,
    $8,
    'succeeded',
    $9
)
`,
		created.CreatedByUserID,
		created.TenantID,
		created.ProjectID,
		created.ClusterID,
		auditaction.ClusterEnrollmentCreate,
		auditaction.ClusterConnectionReenroll,
		auditaction.TargetEnrollment,
		created.ID,
		input.RequestID,
	); err != nil {
		return Enrollment{}, fmt.Errorf("audit enrollment creation: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return Enrollment{}, fmt.Errorf("commit enrollment creation transaction: %w", err)
	}
	return created, nil
}
