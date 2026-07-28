package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

type AgentStatusStore struct {
	pool *pgxpool.Pool
}

// ListProjectAgentCertificatesParams filters and pages the Cluster list of one
// project. Connection liveness is merged in afterwards from memory, so only
// the persisted Cluster attributes can be filtered here.
type ListProjectAgentCertificatesParams struct {
	ProjectID string
	Status    string
	Search    string
	Page      pagination.Request
}

type ProjectAgentCertificate struct {
	TenantID             string
	ProjectID            string
	ClusterID            string
	ClusterName          string
	ClusterStatus        string
	ClusterCreatedAt     time.Time
	ClusterUpdatedAt     time.Time
	AgentID              string
	AgentVersion         string
	ProtocolVersion      string
	LifecycleStatus      string
	HealthStatus         string
	LastSeenAt           *time.Time
	CertificateSerial    string
	CertificateExpiresAt time.Time
	CertificateRevokedAt *time.Time
}

func NewAgentStatusStore(pool *pgxpool.Pool) *AgentStatusStore {
	return &AgentStatusStore{pool: pool}
}

// projectAgentFilterSQL selects the representative Agent per Cluster together
// with its active credential. The Cluster status and name filters are applied
// here so paging happens over the filtered set rather than in Server memory.
const projectAgentFilterSQL = `
FROM agents AS agent
JOIN clusters AS cluster
  ON cluster.tenant_id = agent.tenant_id
 AND cluster.project_id = agent.project_id
 AND cluster.id = agent.cluster_id
JOIN LATERAL (
    SELECT serial, expires_at, revoked_at
    FROM agent_credentials
    WHERE tenant_id = agent.tenant_id
      AND project_id = agent.project_id
      AND cluster_id = agent.cluster_id
      AND agent_id = agent.id
    ORDER BY
      (serial = agent.active_credential_serial) DESC,
      created_at DESC
    LIMIT 1
) AS credential ON true
WHERE agent.project_id = $1
  AND agent.id = (
      SELECT candidate.id
      FROM agents AS candidate
      WHERE candidate.cluster_id = agent.cluster_id
      ORDER BY
          (candidate.lifecycle_status <> 'revoked') DESC,
          candidate.created_at DESC
      LIMIT 1
  )
  AND ($2 = '' OR cluster.status = $2)
  AND (
    $3 = ''
    OR position($3 IN lower(cluster.name)) > 0
    OR position($3 IN cluster.id::text) > 0
  )
`

func (store *AgentStatusStore) ListProjectAgentCertificates(
	ctx context.Context,
	params ListProjectAgentCertificatesParams,
) ([]ProjectAgentCertificate, int, error) {
	return queryPage(
		ctx,
		store.pool,
		"SELECT count(*) "+projectAgentFilterSQL,
		`
SELECT
    agent.tenant_id::text,
    agent.project_id::text,
    agent.cluster_id::text,
    cluster.name,
    cluster.status,
    cluster.created_at,
    cluster.updated_at,
    agent.id::text,
    agent.version,
    agent.protocol_version,
    agent.lifecycle_status,
    agent.health_status,
    agent.last_seen_at,
    credential.serial,
    credential.expires_at,
    credential.revoked_at
`+projectAgentFilterSQL+`
ORDER BY cluster.name, agent.id
LIMIT $4 OFFSET $5
`,
		[]any{params.ProjectID, params.Status, params.Search},
		params.Page,
		scanProjectAgentCertificate,
		"project Agent certificates",
	)
}

func scanProjectAgentCertificate(
	rows pgx.Rows,
) (ProjectAgentCertificate, error) {
	var item ProjectAgentCertificate
	err := rows.Scan(
		&item.TenantID,
		&item.ProjectID,
		&item.ClusterID,
		&item.ClusterName,
		&item.ClusterStatus,
		&item.ClusterCreatedAt,
		&item.ClusterUpdatedAt,
		&item.AgentID,
		&item.AgentVersion,
		&item.ProtocolVersion,
		&item.LifecycleStatus,
		&item.HealthStatus,
		&item.LastSeenAt,
		&item.CertificateSerial,
		&item.CertificateExpiresAt,
		&item.CertificateRevokedAt,
	)
	return item, err
}

func (store *AgentStatusStore) GetClusterAgentCertificate(
	ctx context.Context,
	clusterID string,
) (ProjectAgentCertificate, error) {
	var item ProjectAgentCertificate
	err := store.pool.QueryRow(ctx, `
SELECT
    agent.tenant_id::text,
    agent.project_id::text,
    agent.cluster_id::text,
    cluster.name,
    cluster.status,
    cluster.created_at,
    cluster.updated_at,
    agent.id::text,
    agent.version,
    agent.protocol_version,
    agent.lifecycle_status,
    agent.health_status,
    agent.last_seen_at,
    credential.serial,
    credential.expires_at,
    credential.revoked_at
FROM agents AS agent
JOIN clusters AS cluster
  ON cluster.tenant_id = agent.tenant_id
 AND cluster.project_id = agent.project_id
 AND cluster.id = agent.cluster_id
JOIN LATERAL (
    SELECT serial, expires_at, revoked_at
    FROM agent_credentials
    WHERE tenant_id = agent.tenant_id
      AND project_id = agent.project_id
      AND cluster_id = agent.cluster_id
      AND agent_id = agent.id
    ORDER BY
      (serial = agent.active_credential_serial) DESC,
      created_at DESC
    LIMIT 1
) AS credential ON true
WHERE agent.cluster_id = $1
  AND agent.id = (
      SELECT candidate.id
      FROM agents AS candidate
      WHERE candidate.cluster_id = agent.cluster_id
      ORDER BY
          (candidate.lifecycle_status <> 'revoked') DESC,
          candidate.created_at DESC
      LIMIT 1
  )
`, clusterID).Scan(
		&item.TenantID,
		&item.ProjectID,
		&item.ClusterID,
		&item.ClusterName,
		&item.ClusterStatus,
		&item.ClusterCreatedAt,
		&item.ClusterUpdatedAt,
		&item.AgentID,
		&item.AgentVersion,
		&item.ProtocolVersion,
		&item.LifecycleStatus,
		&item.HealthStatus,
		&item.LastSeenAt,
		&item.CertificateSerial,
		&item.CertificateExpiresAt,
		&item.CertificateRevokedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectAgentCertificate{}, ErrAgentNotFound
	}
	if err != nil {
		return ProjectAgentCertificate{}, fmt.Errorf("get cluster Agent certificate: %w", err)
	}
	return item, nil
}

func (store *AgentStatusStore) ListExpiringAgentCertificates(
	ctx context.Context,
	deadline time.Time,
) ([]ProjectAgentCertificate, error) {
	rows, err := store.pool.Query(ctx, `
SELECT
    credential.tenant_id::text,
    credential.project_id::text,
    credential.cluster_id::text,
    cluster.name,
    cluster.status,
    cluster.created_at,
    cluster.updated_at,
    credential.agent_id::text,
    agent.version,
    agent.protocol_version,
    agent.lifecycle_status,
    agent.health_status,
    agent.last_seen_at,
    credential.serial,
    credential.expires_at,
    credential.revoked_at
FROM agent_credentials AS credential
JOIN agents AS agent
  ON agent.tenant_id = credential.tenant_id
 AND agent.project_id = credential.project_id
 AND agent.cluster_id = credential.cluster_id
 AND agent.id = credential.agent_id
JOIN clusters AS cluster
  ON cluster.tenant_id = credential.tenant_id
 AND cluster.project_id = credential.project_id
 AND cluster.id = credential.cluster_id
WHERE credential.revoked_at IS NULL
  AND credential.expires_at <= $1
  AND agent.lifecycle_status <> 'revoked'
  AND cluster.status <> 'suspended'
ORDER BY credential.expires_at
`, deadline)
	if err != nil {
		return nil, fmt.Errorf("list expiring Agent certificates: %w", err)
	}
	defer rows.Close()
	var result []ProjectAgentCertificate
	for rows.Next() {
		var item ProjectAgentCertificate
		if err := rows.Scan(
			&item.TenantID,
			&item.ProjectID,
			&item.ClusterID,
			&item.ClusterName,
			&item.ClusterStatus,
			&item.ClusterCreatedAt,
			&item.ClusterUpdatedAt,
			&item.AgentID,
			&item.AgentVersion,
			&item.ProtocolVersion,
			&item.LifecycleStatus,
			&item.HealthStatus,
			&item.LastSeenAt,
			&item.CertificateSerial,
			&item.CertificateExpiresAt,
			&item.CertificateRevokedAt,
		); err != nil {
			return nil, fmt.Errorf("scan expiring Agent certificate: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expiring Agent certificates: %w", err)
	}
	return result, nil
}
