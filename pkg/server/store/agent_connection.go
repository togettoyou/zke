package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrAgentCredentialRejected = errors.New("Agent credential rejected")

type AgentConnectionStore struct {
	pool *pgxpool.Pool
}

type AgentConnectionIdentity struct {
	TenantID  string
	ProjectID string
	ClusterID string
	AgentID   string
}

type ActivateAgentConnectionParams struct {
	Identity          AgentConnectionIdentity
	CertificateSerial string
	AgentVersion      string
	ProtocolVersion   string
	HealthStatus      string
	Now               time.Time
}

type RecordAgentHeartbeatParams struct {
	Identity     AgentConnectionIdentity
	HealthStatus string
	Now          time.Time
}

func NewAgentConnectionStore(pool *pgxpool.Pool) *AgentConnectionStore {
	return &AgentConnectionStore{pool: pool}
}

func (store *AgentConnectionStore) Activate(
	ctx context.Context,
	params ActivateAgentConnectionParams,
) error {
	commandTag, err := store.pool.Exec(
		ctx,
		`
WITH valid_agent AS (
    SELECT a.id
    FROM agents AS a
    JOIN clusters AS cluster
      ON cluster.tenant_id = a.tenant_id
     AND cluster.project_id = a.project_id
     AND cluster.id = a.cluster_id
    JOIN agent_credentials AS credential
      ON credential.tenant_id = a.tenant_id
     AND credential.project_id = a.project_id
     AND credential.cluster_id = a.cluster_id
     AND credential.agent_id = a.id
    WHERE a.tenant_id = $1
      AND a.project_id = $2
      AND a.cluster_id = $3
      AND a.id = $4
      AND a.lifecycle_status <> 'revoked'
      AND cluster.status <> 'revoked'
      AND credential.serial = $5
      AND credential.revoked_at IS NULL
      AND credential.expires_at > $9
),
updated_agent AS (
    UPDATE agents
    SET version = $6,
        protocol_version = $7,
        lifecycle_status = 'active',
        health_status = $8,
        last_seen_at = $9,
        updated_at = $9
    WHERE id IN (SELECT id FROM valid_agent)
    RETURNING id
)
UPDATE clusters
SET status = 'active',
    last_seen_at = $9,
    updated_at = $9
WHERE tenant_id = $1
  AND project_id = $2
  AND id = $3
  AND status <> 'revoked'
  AND EXISTS (SELECT 1 FROM updated_agent)
`,
		params.Identity.TenantID,
		params.Identity.ProjectID,
		params.Identity.ClusterID,
		params.Identity.AgentID,
		params.CertificateSerial,
		params.AgentVersion,
		params.ProtocolVersion,
		params.HealthStatus,
		params.Now,
	)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() != 1 {
		return ErrAgentCredentialRejected
	}
	return nil
}

func (store *AgentConnectionStore) RecordHeartbeat(
	ctx context.Context,
	params RecordAgentHeartbeatParams,
) error {
	commandTag, err := store.pool.Exec(
		ctx,
		`
WITH updated_agent AS (
    UPDATE agents
    SET health_status = $5,
        last_seen_at = $6,
        updated_at = $6
    WHERE tenant_id = $1
      AND project_id = $2
      AND cluster_id = $3
      AND id = $4
      AND lifecycle_status = 'active'
      AND EXISTS (
          SELECT 1
          FROM clusters
          WHERE tenant_id = $1
            AND project_id = $2
            AND id = $3
            AND status = 'active'
      )
    RETURNING id
)
UPDATE clusters
SET last_seen_at = $6,
    updated_at = $6
WHERE tenant_id = $1
  AND project_id = $2
  AND id = $3
  AND status = 'active'
  AND EXISTS (SELECT 1 FROM updated_agent)
`,
		params.Identity.TenantID,
		params.Identity.ProjectID,
		params.Identity.ClusterID,
		params.Identity.AgentID,
		params.HealthStatus,
		params.Now,
	)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() != 1 {
		return ErrAgentCredentialRejected
	}
	return nil
}
