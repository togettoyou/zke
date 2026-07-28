package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/togettoyou/zke/pkg/server/auditaction"
)

var ErrAgentNotFound = errors.New("Agent not found")

type AgentManagementStore struct {
	pool *pgxpool.Pool
}

type RevokeAgentParams struct {
	ClusterID   string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type RevokeAgentResult struct {
	AgentID        string
	TenantID       string
	ProjectID      string
	ClusterID      string
	RevokedAt      time.Time
	AlreadyRevoked bool
}

func NewAgentManagementStore(pool *pgxpool.Pool) *AgentManagementStore {
	return &AgentManagementStore{pool: pool}
}

func (store *AgentManagementStore) Revoke(
	ctx context.Context,
	params RevokeAgentParams,
) (RevokeAgentResult, error) {
	if strings.TrimSpace(params.ClusterID) == "" ||
		strings.TrimSpace(params.ActorUserID) == "" ||
		strings.TrimSpace(params.RequestID) == "" ||
		params.Now.IsZero() {
		return RevokeAgentResult{}, errors.New("Agent revocation fields are required")
	}
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return RevokeAgentResult{}, fmt.Errorf("begin Agent revocation: %w", err)
	}
	defer rollbackTransaction(transaction)

	var result RevokeAgentResult
	var lifecycleStatus string
	err = transaction.QueryRow(ctx, `
SELECT
    id::text,
    tenant_id::text,
    project_id::text,
    cluster_id::text,
    lifecycle_status,
    updated_at
FROM agents
WHERE cluster_id = $1
ORDER BY (lifecycle_status <> 'revoked') DESC, created_at DESC
LIMIT 1
FOR UPDATE
`, params.ClusterID).Scan(
		&result.AgentID,
		&result.TenantID,
		&result.ProjectID,
		&result.ClusterID,
		&lifecycleStatus,
		&result.RevokedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RevokeAgentResult{}, ErrAgentNotFound
	}
	if err != nil {
		return RevokeAgentResult{}, fmt.Errorf("lock Agent for revocation: %w", err)
	}

	result.AlreadyRevoked = lifecycleStatus == "revoked"
	if !result.AlreadyRevoked {
		if _, err := transaction.Exec(ctx, `
UPDATE agents
SET lifecycle_status = 'revoked',
    health_status = 'unknown',
    updated_at = $2
WHERE id = $1
`, result.AgentID, params.Now); err != nil {
			return RevokeAgentResult{}, fmt.Errorf("revoke Agent: %w", err)
		}
		if _, err := transaction.Exec(ctx, `
UPDATE agent_credentials
SET revoked_at = $2
WHERE agent_id = $1
  AND revoked_at IS NULL
`, result.AgentID, params.Now); err != nil {
			return RevokeAgentResult{}, fmt.Errorf(
				"revoke Agent credentials: %w",
				err,
			)
		}
		result.RevokedAt = params.Now
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
    'cluster',
    $2,
    $3,
    $4,
    $5,
    $6,
    $4,
    'succeeded',
    $7
)
`,
		params.ActorUserID,
		result.TenantID,
		result.ProjectID,
		result.ClusterID,
		auditaction.ClusterConnectionRevoke,
		auditaction.TargetCluster,
		params.RequestID,
	); err != nil {
		return RevokeAgentResult{}, fmt.Errorf("audit Cluster connection revocation: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return RevokeAgentResult{}, fmt.Errorf("commit Agent revocation: %w", err)
	}
	return result, nil
}
