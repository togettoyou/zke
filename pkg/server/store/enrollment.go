package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
    enrollment.expires_at,
    enrollment.endpoint_profile_id::text,
    enrollment.endpoint_profile_revision,
    enrollment.registration_url,
    enrollment.quic_address,
    enrollment.registration_ca_certificate_pem,
    enrollment.agent_image,
    enrollment.agent_namespace,
    enrollment.agent_image_pull_policy
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
  AND (
      enrollment.cluster_id IS NULL
      OR EXISTS (
          SELECT 1
          FROM clusters
          WHERE clusters.id = enrollment.cluster_id
            AND clusters.tenant_id = enrollment.tenant_id
            AND clusters.project_id = enrollment.project_id
            AND clusters.status <> 'suspended'
      )
  )
`, tokenDigest, now).Scan(
		&enrollment.ID,
		&enrollment.TenantID,
		&enrollment.ProjectID,
		&enrollment.ClusterID,
		&enrollment.ClusterName,
		&enrollment.ExpiresAt,
		&enrollment.Snapshot.EndpointProfileID,
		&enrollment.Snapshot.EndpointProfileRevision,
		&enrollment.Snapshot.RegistrationURL,
		&enrollment.Snapshot.QUICAddress,
		&enrollment.Snapshot.RegistrationCACertificatePEM,
		&enrollment.Snapshot.AgentImage,
		&enrollment.Snapshot.AgentNamespace,
		&enrollment.Snapshot.AgentImagePullPolicy,
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
	if strings.TrimSpace(input.Snapshot.EndpointProfileID) == "" ||
		input.Snapshot.EndpointProfileRevision <= 0 ||
		strings.TrimSpace(input.Snapshot.RegistrationURL) == "" ||
		strings.TrimSpace(input.Snapshot.QUICAddress) == "" ||
		strings.TrimSpace(input.Snapshot.AgentImage) == "" ||
		strings.TrimSpace(input.Snapshot.AgentNamespace) == "" ||
		strings.TrimSpace(input.Snapshot.AgentImagePullPolicy) == "" {
		return Enrollment{}, errors.New("enrollment configuration snapshot is required")
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
	// An outstanding enrollment holds the Cluster name it will create, and the
	// unique index that enforces that cannot include expiry: `now()` is not
	// immutable, so a lapsed token would keep its claim forever.
	//
	// Revoking the lapsed holder here is what closes that gap. It runs before
	// the insert and inside the same transaction, so the name is either released
	// and taken in one step or neither. The advisory lock covers the window
	// between the two for concurrent submissions of the same name.
	if _, err := transaction.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || lower($2), 0))",
		input.ProjectID,
		input.ClusterName,
	); err != nil {
		return Enrollment{}, fmt.Errorf("lock Cluster enrollment name: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE enrollments
SET revoked_at = $3
WHERE project_id = $1
  AND lower(cluster_name) = lower($2)
  AND consumed_at IS NULL
  AND revoked_at IS NULL
  AND expires_at <= $3
`, input.ProjectID, input.ClusterName, time.Now().UTC()); err != nil {
		return Enrollment{}, fmt.Errorf("release expired Cluster enrollment name: %w", err)
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
    expires_at,
    endpoint_profile_id,
    endpoint_profile_revision,
    registration_url,
    quic_address,
    registration_ca_certificate_pem,
    agent_image,
    agent_namespace,
    agent_image_pull_policy
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
    $7,
    $8::uuid,
    $9,
    $10,
    $11,
    $12,
    $13,
    $14,
    $15
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
      -- A first enrollment names a Cluster that does not exist yet, so the name
      -- is checked here rather than at the unique index the Agent would meet
      -- hours later, holding a token for a name someone else has taken.
      $3 <> ''
      OR NOT EXISTS (
          SELECT 1
          FROM clusters AS existing
          WHERE existing.project_id = project.id
            AND lower(existing.name) = lower($4)
      )
  )
  AND (
      $3 = ''
      OR (
          cluster.id IS NOT NULL
          AND cluster.status <> 'suspended'
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
    created_at,
    endpoint_profile_id::text,
    endpoint_profile_revision
`,
		input.ProjectID,
		input.CreatedByUserID,
		input.ClusterID,
		input.ClusterName,
		input.TokenDigest,
		input.IdempotencyKey,
		input.ExpiresAt,
		input.Snapshot.EndpointProfileID,
		input.Snapshot.EndpointProfileRevision,
		input.Snapshot.RegistrationURL,
		input.Snapshot.QUICAddress,
		input.Snapshot.RegistrationCACertificatePEM,
		input.Snapshot.AgentImage,
		input.Snapshot.AgentNamespace,
		input.Snapshot.AgentImagePullPolicy,
	).Scan(
		&created.ID,
		&created.TenantID,
		&created.ProjectID,
		&created.ClusterID,
		&created.ClusterName,
		&created.CreatedByUserID,
		&created.ExpiresAt,
		&created.CreatedAt,
		&created.EndpointProfileID,
		&created.EndpointProfileRevision,
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
		if input.ClusterID == "" {
			// A name is taken either by a Cluster that exists or by another
			// outstanding enrollment that will create one.
			var nameTaken bool
			if queryErr := transaction.QueryRow(ctx, `
SELECT
    EXISTS (
        SELECT 1 FROM clusters
        WHERE project_id = $1 AND lower(name) = lower($2)
    )
    OR EXISTS (
        SELECT 1 FROM enrollments
        WHERE project_id = $1
          AND lower(cluster_name) = lower($2)
          AND consumed_at IS NULL
          AND revoked_at IS NULL
    )
`, input.ProjectID, input.ClusterName).Scan(&nameTaken); queryErr != nil {
				return Enrollment{}, fmt.Errorf("check enrollment cluster name: %w", queryErr)
			}
			if nameTaken {
				return Enrollment{}, ErrClusterNameConflict
			}
		}
		return Enrollment{}, ErrEnrollmentCreationDenied
	}
	// The unique index catches what the pre-checks raced with.
	var nameConflict *pgconn.PgError
	if errors.As(err, &nameConflict) && nameConflict.Code == "23505" {
		return Enrollment{}, ErrClusterNameConflict
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
