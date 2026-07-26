package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type lockedEnrollment struct {
	ID            string
	TenantID      string
	ProjectID     string
	ClusterName   string
	TenantStatus  string
	ProjectStatus string
	ExpiresAt     time.Time
	ConsumedAt    *time.Time
	RevokedAt     *time.Time
}

func (store *EnrollmentStore) BeginAgentEnrollment(
	ctx context.Context,
	input BeginAgentEnrollmentParams,
) (AgentEnrollmentAttempt, error) {
	if len(input.TokenDigest) != sha256.Size {
		return AgentEnrollmentAttempt{}, errors.New("enrollment token digest must use SHA-256")
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" ||
		len(input.CSRFingerprint) != sha256.Size ||
		strings.TrimSpace(input.RequestID) == "" ||
		input.Now.IsZero() {
		return AgentEnrollmentAttempt{}, errors.New("agent enrollment attempt fields are required")
	}

	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentEnrollmentAttempt{}, fmt.Errorf("begin agent enrollment transaction: %w", err)
	}
	defer rollbackTransaction(transaction)

	enrollment, err := lockEnrollmentByTokenDigest(ctx, transaction, input.TokenDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		if auditErr := auditRejectedAgentEnrollment(
			ctx,
			transaction,
			nil,
			input.RequestID,
		); auditErr != nil {
			return AgentEnrollmentAttempt{}, auditErr
		}
		if commitErr := transaction.Commit(ctx); commitErr != nil {
			return AgentEnrollmentAttempt{}, fmt.Errorf(
				"commit rejected agent enrollment audit: %w",
				commitErr,
			)
		}
		return AgentEnrollmentAttempt{}, ErrEnrollmentTokenRejected
	}
	if err != nil {
		return AgentEnrollmentAttempt{}, err
	}

	attempt, found, err := findEnrollmentAttempt(
		ctx,
		transaction,
		enrollment,
		input.IdempotencyKey,
	)
	if err != nil {
		return AgentEnrollmentAttempt{}, err
	}
	if found && !bytes.Equal(attempt.CSRFingerprint, input.CSRFingerprint) {
		if err := auditRejectedAgentEnrollment(
			ctx,
			transaction,
			&enrollment,
			input.RequestID,
		); err != nil {
			return AgentEnrollmentAttempt{}, err
		}
		if err := transaction.Commit(ctx); err != nil {
			return AgentEnrollmentAttempt{}, fmt.Errorf(
				"commit conflicting agent enrollment audit: %w",
				err,
			)
		}
		return AgentEnrollmentAttempt{}, ErrEnrollmentAttemptConflict
	}
	if found && attempt.Status == EnrollmentAttemptSucceeded {
		if err := transaction.Commit(ctx); err != nil {
			return AgentEnrollmentAttempt{}, fmt.Errorf(
				"commit recovered agent enrollment result: %w",
				err,
			)
		}
		return attempt, nil
	}
	if found && attempt.Status == EnrollmentAttemptFailed {
		return AgentEnrollmentAttempt{}, ErrEnrollmentAttemptFailed
	}
	if !enrollment.isUsable(input.Now) {
		if err := auditRejectedAgentEnrollment(
			ctx,
			transaction,
			&enrollment,
			input.RequestID,
		); err != nil {
			return AgentEnrollmentAttempt{}, err
		}
		if err := transaction.Commit(ctx); err != nil {
			return AgentEnrollmentAttempt{}, fmt.Errorf(
				"commit rejected agent enrollment audit: %w",
				err,
			)
		}
		return AgentEnrollmentAttempt{}, ErrEnrollmentTokenRejected
	}
	if found {
		if err := transaction.Commit(ctx); err != nil {
			return AgentEnrollmentAttempt{}, fmt.Errorf(
				"commit recovered pending agent enrollment attempt: %w",
				err,
			)
		}
		return attempt, nil
	}

	attempt = AgentEnrollmentAttempt{
		EnrollmentID:   enrollment.ID,
		TenantID:       enrollment.TenantID,
		ProjectID:      enrollment.ProjectID,
		ClusterName:    enrollment.ClusterName,
		IdempotencyKey: input.IdempotencyKey,
		CSRFingerprint: append([]byte(nil), input.CSRFingerprint...),
		Status:         EnrollmentAttemptPending,
	}
	err = transaction.QueryRow(ctx, `
INSERT INTO enrollment_attempts (
    id,
    enrollment_id,
    idempotency_key,
    csr_fingerprint,
    status
)
VALUES (gen_random_uuid(), $1, $2, $3, 'pending')
RETURNING id::text
`,
		attempt.EnrollmentID,
		attempt.IdempotencyKey,
		attempt.CSRFingerprint,
	).Scan(&attempt.ID)
	if err != nil {
		return AgentEnrollmentAttempt{}, fmt.Errorf("create agent enrollment attempt: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return AgentEnrollmentAttempt{}, fmt.Errorf(
			"commit agent enrollment attempt: %w",
			err,
		)
	}
	return attempt, nil
}

func (store *EnrollmentStore) CompleteAgentEnrollment(
	ctx context.Context,
	input CompleteAgentEnrollmentParams,
) (AgentEnrollmentResult, error) {
	if strings.TrimSpace(input.EnrollmentID) == "" ||
		strings.TrimSpace(input.AttemptID) == "" ||
		strings.TrimSpace(input.IdempotencyKey) == "" ||
		len(input.CSRFingerprint) != sha256.Size ||
		strings.TrimSpace(input.ClusterID) == "" ||
		strings.TrimSpace(input.AgentID) == "" ||
		strings.TrimSpace(input.AgentVersion) == "" ||
		strings.TrimSpace(input.ProtocolVersion) == "" ||
		strings.TrimSpace(input.CertificateSerial) == "" ||
		strings.TrimSpace(input.CertificatePEM) == "" ||
		!input.CertificateExpiresAt.After(input.Now) ||
		strings.TrimSpace(input.RequestID) == "" ||
		input.Now.IsZero() {
		return AgentEnrollmentResult{}, errors.New("completed agent enrollment fields are required")
	}

	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentEnrollmentResult{}, fmt.Errorf(
			"begin completed agent enrollment transaction: %w",
			err,
		)
	}
	defer rollbackTransaction(transaction)

	enrollment, err := lockEnrollmentByID(ctx, transaction, input.EnrollmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentEnrollmentResult{}, ErrEnrollmentAttemptNotFound
	}
	if err != nil {
		return AgentEnrollmentResult{}, err
	}
	attempt, err := lockEnrollmentAttempt(
		ctx,
		transaction,
		enrollment,
		input.AttemptID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentEnrollmentResult{}, ErrEnrollmentAttemptNotFound
	}
	if err != nil {
		return AgentEnrollmentResult{}, err
	}
	if attempt.IdempotencyKey != input.IdempotencyKey ||
		!bytes.Equal(attempt.CSRFingerprint, input.CSRFingerprint) {
		return AgentEnrollmentResult{}, ErrEnrollmentAttemptConflict
	}
	if attempt.Status == EnrollmentAttemptSucceeded {
		if err := transaction.Commit(ctx); err != nil {
			return AgentEnrollmentResult{}, fmt.Errorf(
				"commit recovered agent enrollment result: %w",
				err,
			)
		}
		return *attempt.Result, nil
	}
	if attempt.Status == EnrollmentAttemptFailed {
		return AgentEnrollmentResult{}, ErrEnrollmentAttemptFailed
	}
	if !enrollment.isUsable(input.Now) {
		if err := auditRejectedAgentEnrollment(
			ctx,
			transaction,
			&enrollment,
			input.RequestID,
		); err != nil {
			return AgentEnrollmentResult{}, err
		}
		if err := transaction.Commit(ctx); err != nil {
			return AgentEnrollmentResult{}, fmt.Errorf(
				"commit rejected agent enrollment completion audit: %w",
				err,
			)
		}
		return AgentEnrollmentResult{}, ErrEnrollmentTokenRejected
	}

	result := AgentEnrollmentResult{
		ClusterID:            input.ClusterID,
		AgentID:              input.AgentID,
		CertificatePEM:       input.CertificatePEM,
		CertificateExpiresAt: input.CertificateExpiresAt,
	}
	_, err = transaction.Exec(ctx, `
INSERT INTO clusters (
    id,
    tenant_id,
    project_id,
    name,
    status
)
VALUES ($1, $2, $3, $4, 'pending')
`,
		result.ClusterID,
		enrollment.TenantID,
		enrollment.ProjectID,
		enrollment.ClusterName,
	)
	if err != nil {
		return AgentEnrollmentResult{}, fmt.Errorf("create enrolled cluster: %w", err)
	}
	_, err = transaction.Exec(ctx, `
INSERT INTO agents (
    id,
    tenant_id,
    project_id,
    cluster_id,
    version,
    protocol_version,
    lifecycle_status,
    health_status
)
VALUES ($1, $2, $3, $4, $5, $6, 'pending', 'unknown')
`,
		result.AgentID,
		enrollment.TenantID,
		enrollment.ProjectID,
		result.ClusterID,
		input.AgentVersion,
		input.ProtocolVersion,
	)
	if err != nil {
		return AgentEnrollmentResult{}, fmt.Errorf("create enrolled agent: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
INSERT INTO agent_credentials (
    id,
    tenant_id,
    project_id,
    cluster_id,
    agent_id,
    serial,
    csr_fingerprint,
    certificate_pem,
    expires_at
)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8)
`,
		enrollment.TenantID,
		enrollment.ProjectID,
		result.ClusterID,
		result.AgentID,
		input.CertificateSerial,
		input.CSRFingerprint,
		input.CertificatePEM,
		input.CertificateExpiresAt,
	); err != nil {
		return AgentEnrollmentResult{}, fmt.Errorf("store agent credential: %w", err)
	}

	responseJSON, err := json.Marshal(result)
	if err != nil {
		return AgentEnrollmentResult{}, fmt.Errorf("encode agent enrollment result: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE enrollments
SET consumed_at = $2
WHERE id = $1
`, enrollment.ID, input.Now); err != nil {
		return AgentEnrollmentResult{}, fmt.Errorf("consume agent enrollment token: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE enrollment_attempts
SET
    status = 'succeeded',
    response_json = $2,
    updated_at = $3
WHERE id = $1
`, attempt.ID, responseJSON, input.Now); err != nil {
		return AgentEnrollmentResult{}, fmt.Errorf("complete agent enrollment attempt: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id,
    actor_type,
    actor_agent_id,
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
    'agent',
    $1,
    'cluster',
    $2,
    $3,
    $4,
    'agent.enroll',
    'agent',
    $1,
    'succeeded',
    $5
)
`,
		result.AgentID,
		enrollment.TenantID,
		enrollment.ProjectID,
		result.ClusterID,
		input.RequestID,
	); err != nil {
		return AgentEnrollmentResult{}, fmt.Errorf("audit completed agent enrollment: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return AgentEnrollmentResult{}, fmt.Errorf(
			"commit completed agent enrollment: %w",
			err,
		)
	}
	return result, nil
}

func lockEnrollmentByTokenDigest(
	ctx context.Context,
	transaction pgx.Tx,
	tokenDigest []byte,
) (lockedEnrollment, error) {
	return scanLockedEnrollment(transaction.QueryRow(ctx, `
SELECT
    enrollment.id::text,
    enrollment.tenant_id::text,
    enrollment.project_id::text,
    enrollment.cluster_name,
    tenant.status,
    project.status,
    enrollment.expires_at,
    enrollment.consumed_at,
    enrollment.revoked_at
FROM enrollments AS enrollment
JOIN tenants AS tenant ON tenant.id = enrollment.tenant_id
JOIN projects AS project
  ON project.tenant_id = enrollment.tenant_id
 AND project.id = enrollment.project_id
WHERE enrollment.token_digest = $1
FOR UPDATE OF enrollment, tenant, project
`, tokenDigest))
}

func lockEnrollmentByID(
	ctx context.Context,
	transaction pgx.Tx,
	enrollmentID string,
) (lockedEnrollment, error) {
	return scanLockedEnrollment(transaction.QueryRow(ctx, `
SELECT
    enrollment.id::text,
    enrollment.tenant_id::text,
    enrollment.project_id::text,
    enrollment.cluster_name,
    tenant.status,
    project.status,
    enrollment.expires_at,
    enrollment.consumed_at,
    enrollment.revoked_at
FROM enrollments AS enrollment
JOIN tenants AS tenant ON tenant.id = enrollment.tenant_id
JOIN projects AS project
  ON project.tenant_id = enrollment.tenant_id
 AND project.id = enrollment.project_id
WHERE enrollment.id = $1
FOR UPDATE OF enrollment, tenant, project
`, enrollmentID))
}

func scanLockedEnrollment(row pgx.Row) (lockedEnrollment, error) {
	var enrollment lockedEnrollment
	err := row.Scan(
		&enrollment.ID,
		&enrollment.TenantID,
		&enrollment.ProjectID,
		&enrollment.ClusterName,
		&enrollment.TenantStatus,
		&enrollment.ProjectStatus,
		&enrollment.ExpiresAt,
		&enrollment.ConsumedAt,
		&enrollment.RevokedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return lockedEnrollment{}, err
		}
		return lockedEnrollment{}, fmt.Errorf("lock enrollment: %w", err)
	}
	return enrollment, nil
}

func (enrollment lockedEnrollment) isUsable(now time.Time) bool {
	return enrollment.TenantStatus == "active" &&
		enrollment.ProjectStatus == "active" &&
		enrollment.ConsumedAt == nil &&
		enrollment.RevokedAt == nil &&
		enrollment.ExpiresAt.After(now)
}

func findEnrollmentAttempt(
	ctx context.Context,
	transaction pgx.Tx,
	enrollment lockedEnrollment,
	idempotencyKey string,
) (AgentEnrollmentAttempt, bool, error) {
	attempt, err := scanEnrollmentAttempt(transaction.QueryRow(ctx, `
SELECT
    id::text,
    enrollment_id::text,
    idempotency_key,
    csr_fingerprint,
    status,
    response_json
FROM enrollment_attempts
WHERE enrollment_id = $1
  AND idempotency_key = $2
`, enrollment.ID, idempotencyKey), enrollment)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentEnrollmentAttempt{}, false, nil
	}
	if err != nil {
		return AgentEnrollmentAttempt{}, false, err
	}
	return attempt, true, nil
}

func lockEnrollmentAttempt(
	ctx context.Context,
	transaction pgx.Tx,
	enrollment lockedEnrollment,
	attemptID string,
) (AgentEnrollmentAttempt, error) {
	return scanEnrollmentAttempt(transaction.QueryRow(ctx, `
SELECT
    id::text,
    enrollment_id::text,
    idempotency_key,
    csr_fingerprint,
    status,
    response_json
FROM enrollment_attempts
WHERE enrollment_id = $1
  AND id = $2
FOR UPDATE
`, enrollment.ID, attemptID), enrollment)
}

func scanEnrollmentAttempt(
	row pgx.Row,
	enrollment lockedEnrollment,
) (AgentEnrollmentAttempt, error) {
	var attempt AgentEnrollmentAttempt
	var responseJSON []byte
	err := row.Scan(
		&attempt.ID,
		&attempt.EnrollmentID,
		&attempt.IdempotencyKey,
		&attempt.CSRFingerprint,
		&attempt.Status,
		&responseJSON,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentEnrollmentAttempt{}, err
		}
		return AgentEnrollmentAttempt{}, fmt.Errorf("read agent enrollment attempt: %w", err)
	}
	attempt.TenantID = enrollment.TenantID
	attempt.ProjectID = enrollment.ProjectID
	attempt.ClusterName = enrollment.ClusterName
	if attempt.Status == EnrollmentAttemptSucceeded {
		if len(responseJSON) == 0 {
			return AgentEnrollmentAttempt{}, errors.New(
				"succeeded agent enrollment attempt has no stored response",
			)
		}
		var result AgentEnrollmentResult
		if err := json.Unmarshal(responseJSON, &result); err != nil {
			return AgentEnrollmentAttempt{}, fmt.Errorf(
				"decode stored agent enrollment result: %w",
				err,
			)
		}
		attempt.Result = &result
	}
	return attempt, nil
}

func auditRejectedAgentEnrollment(
	ctx context.Context,
	transaction pgx.Tx,
	enrollment *lockedEnrollment,
	requestID string,
) error {
	var tenantID, projectID, targetID any
	scopeType := "global"
	if enrollment != nil {
		scopeType = "project"
		tenantID = enrollment.TenantID
		projectID = enrollment.ProjectID
		targetID = enrollment.ID
	}
	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id,
    actor_type,
    scope_type,
    tenant_id,
    project_id,
    action,
    target_type,
    target_id,
    result,
    request_id
)
VALUES (
    gen_random_uuid(),
    'system',
    $1,
    $2,
    $3,
    'agent.enroll',
    'enrollment',
    $4,
    'denied',
    $5
)
`, scopeType, tenantID, projectID, targetID, requestID); err != nil {
		return fmt.Errorf("audit rejected agent enrollment: %w", err)
	}
	return nil
}

func (store *EnrollmentStore) RecordAgentEnrollmentFailure(
	ctx context.Context,
	enrollmentID string,
	requestID string,
) error {
	if strings.TrimSpace(enrollmentID) == "" ||
		strings.TrimSpace(requestID) == "" {
		return errors.New("Agent enrollment failure audit fields are required")
	}
	commandTag, err := store.pool.Exec(ctx, `
INSERT INTO audit_events (
    id,
    actor_type,
    scope_type,
    tenant_id,
    project_id,
    action,
    target_type,
    target_id,
    result,
    request_id
)
SELECT
    gen_random_uuid(),
    'system',
    'project',
    tenant_id,
    project_id,
    'agent.enroll',
    'enrollment',
    id,
    'failed',
    $2
FROM enrollments
WHERE id = $1
`, enrollmentID, requestID)
	if err != nil {
		return fmt.Errorf("record Agent enrollment failure audit: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return ErrEnrollmentAttemptNotFound
	}
	return nil
}
