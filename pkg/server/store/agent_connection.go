package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/togettoyou/zke/pkg/server/auditaction"
)

var (
	ErrAgentCredentialRejected = errors.New("Agent credential rejected")
	ErrAgentScopeSuspended     = errors.New("Agent scope suspended")
)

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
	Identity          AgentConnectionIdentity
	CertificateSerial string
	HealthStatus      string
	Now               time.Time
}

type RenewAgentCredentialParams struct {
	Identity                 AgentConnectionIdentity
	CurrentCertificateSerial string
	CSRFingerprint           []byte
	NewCertificateSerial     string
	CertificatePEM           string
	CertificateExpiresAt     time.Time
	RequestID                string
	Now                      time.Time
}

type AgentCredentialResult struct {
	Serial       string
	PEM          string
	ExpiresAt    time.Time
	CredentialID string
}

type AgentConnectionRevocation struct {
	TenantID          string `json:"tenant_id"`
	ProjectID         string `json:"project_id"`
	AgentID           string `json:"agent_id"`
	ClusterID         string `json:"cluster_id"`
	CertificateSerial string `json:"certificate_serial"`
	Reason            string `json:"reason"`
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
      -- The whole chain has to be live. Suspension no longer revokes anything,
      -- so this is what actually holds a frozen Tenant, Project or Cluster
      -- closed; the Agent keeps its identity and reconnects when it is resumed.
      AND cluster.status <> 'suspended'
      AND EXISTS (
          SELECT 1 FROM projects AS p
          WHERE p.tenant_id = a.tenant_id
            AND p.id = a.project_id
            AND p.status = 'active'
      )
      AND EXISTS (
          SELECT 1 FROM tenants AS tn
          WHERE tn.id = a.tenant_id AND tn.status = 'active'
      )
      AND credential.serial = $5
      AND credential.revoked_at IS NULL
      AND credential.expires_at > $9
),
revoked_credentials AS (
    UPDATE agent_credentials
    SET revoked_at = $9
    WHERE tenant_id = $1
      AND project_id = $2
      AND cluster_id = $3
      AND agent_id = $4
      AND serial <> $5
      AND revoked_at IS NULL
      AND EXISTS (SELECT 1 FROM valid_agent)
),
updated_agent AS (
    UPDATE agents
    SET version = $6,
        protocol_version = $7,
        lifecycle_status = 'active',
        health_status = $8,
        active_credential_serial = $5,
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
  AND status <> 'suspended'
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
		return store.classifyAgentConnectionRejection(
			ctx,
			store.pool,
			params.Identity,
			params.CertificateSerial,
			params.Now,
		)
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
    SET health_status = $6,
        last_seen_at = $7,
        updated_at = $7
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
      AND EXISTS (
          SELECT 1
          FROM agent_credentials
          WHERE tenant_id = $1
            AND project_id = $2
            AND cluster_id = $3
            AND agent_id = $4
            AND serial = $5
            AND revoked_at IS NULL
            AND expires_at > $7
      )
      -- Suspending a Tenant or Project writes nothing to the Agents below it,
      -- so this is where they find out: the next heartbeat is refused and the
      -- connection ends, without any credential being revoked.
      AND EXISTS (
          SELECT 1 FROM projects
          WHERE projects.tenant_id = $1 AND projects.id = $2
            AND projects.status = 'active'
      )
      AND EXISTS (
          SELECT 1 FROM tenants
          WHERE tenants.id = $1 AND tenants.status = 'active'
      )
    RETURNING id
)
UPDATE clusters
SET last_seen_at = $7,
    updated_at = $7
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
		params.CertificateSerial,
		params.HealthStatus,
		params.Now,
	)
	if err != nil {
		return err
	}
	if commandTag.RowsAffected() != 1 {
		return store.classifyAgentConnectionRejection(
			ctx,
			store.pool,
			params.Identity,
			params.CertificateSerial,
			params.Now,
		)
	}
	return nil
}

func (store *AgentConnectionStore) RenewCredential(
	ctx context.Context,
	params RenewAgentCredentialParams,
) (AgentCredentialResult, error) {
	if strings.TrimSpace(params.CurrentCertificateSerial) == "" ||
		len(params.CSRFingerprint) != sha256.Size ||
		strings.TrimSpace(params.NewCertificateSerial) == "" ||
		strings.TrimSpace(params.CertificatePEM) == "" ||
		!params.CertificateExpiresAt.After(params.Now) ||
		strings.TrimSpace(params.RequestID) == "" ||
		params.Now.IsZero() {
		return AgentCredentialResult{}, errors.New(
			"Agent credential renewal fields are required",
		)
	}

	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentCredentialResult{}, fmt.Errorf(
			"begin Agent credential renewal: %w",
			err,
		)
	}
	defer rollbackTransaction(transaction)

	var currentCredentialID string
	err = transaction.QueryRow(ctx, `
SELECT credential.id::text
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
WHERE credential.tenant_id = $1
  AND credential.project_id = $2
  AND credential.cluster_id = $3
  AND credential.agent_id = $4
  AND credential.serial = $5
  AND credential.revoked_at IS NULL
  AND credential.expires_at > $6
  AND agent.lifecycle_status = 'active'
  AND cluster.status = 'active'
  AND EXISTS (
      SELECT 1 FROM projects
      WHERE projects.tenant_id = credential.tenant_id
        AND projects.id = credential.project_id
        AND projects.status = 'active'
  )
  AND EXISTS (
      SELECT 1 FROM tenants
      WHERE tenants.id = credential.tenant_id AND tenants.status = 'active'
  )
FOR UPDATE OF credential
`,
		params.Identity.TenantID,
		params.Identity.ProjectID,
		params.Identity.ClusterID,
		params.Identity.AgentID,
		params.CurrentCertificateSerial,
		params.Now,
	).Scan(&currentCredentialID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentCredentialResult{}, store.classifyAgentConnectionRejection(
			ctx,
			transaction,
			params.Identity,
			params.CurrentCertificateSerial,
			params.Now,
		)
	}
	if err != nil {
		return AgentCredentialResult{}, fmt.Errorf(
			"lock current Agent credential for renewal: %w",
			err,
		)
	}

	var existing AgentCredentialResult
	var existingRevokedAt *time.Time
	err = transaction.QueryRow(ctx, `
SELECT id::text, serial, certificate_pem, expires_at, revoked_at
FROM agent_credentials
WHERE tenant_id = $1
  AND project_id = $2
  AND cluster_id = $3
  AND agent_id = $4
  AND csr_fingerprint = $5
`,
		params.Identity.TenantID,
		params.Identity.ProjectID,
		params.Identity.ClusterID,
		params.Identity.AgentID,
		params.CSRFingerprint,
	).Scan(
		&existing.CredentialID,
		&existing.Serial,
		&existing.PEM,
		&existing.ExpiresAt,
		&existingRevokedAt,
	)
	if err == nil {
		if existingRevokedAt != nil || !existing.ExpiresAt.After(params.Now) {
			return AgentCredentialResult{}, ErrAgentCredentialRejected
		}
		if err := transaction.Commit(ctx); err != nil {
			return AgentCredentialResult{}, fmt.Errorf(
				"commit replayed Agent credential renewal: %w",
				err,
			)
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return AgentCredentialResult{}, fmt.Errorf(
			"find replayed Agent credential renewal: %w",
			err,
		)
	}

	var credentialID string
	err = transaction.QueryRow(ctx, `
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
RETURNING id::text
`,
		params.Identity.TenantID,
		params.Identity.ProjectID,
		params.Identity.ClusterID,
		params.Identity.AgentID,
		params.NewCertificateSerial,
		params.CSRFingerprint,
		params.CertificatePEM,
		params.CertificateExpiresAt,
	).Scan(&credentialID)
	if err != nil {
		return AgentCredentialResult{}, fmt.Errorf(
			"store renewed Agent credential: %w",
			err,
		)
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
    $5,
    $6,
    $7,
    'succeeded',
    $8
)
`,
		params.Identity.AgentID,
		params.Identity.TenantID,
		params.Identity.ProjectID,
		params.Identity.ClusterID,
		auditaction.AgentCertificateRenew,
		auditaction.TargetAgentCredential,
		credentialID,
		params.RequestID,
	); err != nil {
		return AgentCredentialResult{}, fmt.Errorf(
			"audit renewed Agent credential: %w",
			err,
		)
	}
	if err := transaction.Commit(ctx); err != nil {
		return AgentCredentialResult{}, fmt.Errorf(
			"commit Agent credential renewal: %w",
			err,
		)
	}
	return AgentCredentialResult{
		CredentialID: credentialID,
		Serial:       params.NewCertificateSerial,
		PEM:          params.CertificatePEM,
		ExpiresAt:    params.CertificateExpiresAt,
	}, nil
}

type agentConnectionQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// classifyAgentConnectionRejection separates a reversible scope suspension
// from a permanently unusable identity. Callers use the distinction to keep
// retrying while a Tenant, Project or Cluster is suspended without weakening
// credential, expiry or Agent revocation checks.
func (store *AgentConnectionStore) classifyAgentConnectionRejection(
	ctx context.Context,
	queryer agentConnectionQueryer,
	identity AgentConnectionIdentity,
	certificateSerial string,
	now time.Time,
) error {
	var tenantStatus, projectStatus, clusterStatus string
	err := queryer.QueryRow(ctx, `
SELECT tenant.status, project.status, cluster.status
FROM agents AS agent
JOIN tenants AS tenant ON tenant.id = agent.tenant_id
JOIN projects AS project
  ON project.tenant_id = agent.tenant_id
 AND project.id = agent.project_id
JOIN clusters AS cluster
  ON cluster.tenant_id = agent.tenant_id
 AND cluster.project_id = agent.project_id
 AND cluster.id = agent.cluster_id
JOIN agent_credentials AS credential
  ON credential.tenant_id = agent.tenant_id
 AND credential.project_id = agent.project_id
 AND credential.cluster_id = agent.cluster_id
 AND credential.agent_id = agent.id
WHERE agent.tenant_id = $1
  AND agent.project_id = $2
  AND agent.cluster_id = $3
  AND agent.id = $4
  AND agent.lifecycle_status <> 'revoked'
  AND credential.serial = $5
  AND credential.revoked_at IS NULL
  AND credential.expires_at > $6
`,
		identity.TenantID,
		identity.ProjectID,
		identity.ClusterID,
		identity.AgentID,
		certificateSerial,
		now,
	).Scan(&tenantStatus, &projectStatus, &clusterStatus)
	if err == nil &&
		(tenantStatus == "suspended" ||
			projectStatus == "suspended" ||
			clusterStatus == "suspended") {
		return ErrAgentScopeSuspended
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("classify Agent connection rejection: %w", err)
	}
	return ErrAgentCredentialRejected
}

// WatchRevocations delivers revocation notifications until ctx is cancelled or
// the listening connection fails. onReady is called once the LISTEN is
// established.
//
// It reports readiness through a callback rather than by closing a channel so
// that the caller can call it again after a failure: a supervisor that retries
// cannot hand the same channel to a second attempt.
func (store *AgentConnectionStore) WatchRevocations(
	ctx context.Context,
	onReady func(),
	handle func(AgentConnectionRevocation),
) error {
	if handle == nil {
		return errors.New("Agent connection revocation handler is required")
	}
	connection, err := store.pool.Acquire(ctx)
	if err != nil {
		return errors.New("acquire PostgreSQL connection for Agent revocations")
	}
	defer connection.Release()
	if _, err := connection.Exec(
		ctx,
		"LISTEN zke_agent_connection_revocations",
	); err != nil {
		return errors.New("listen for Agent connection revocations")
	}
	if onReady != nil {
		onReady()
	}
	defer func() {
		cleanupContext, cancelCleanup := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cancelCleanup()
		_, _ = connection.Exec(
			cleanupContext,
			"UNLISTEN zke_agent_connection_revocations",
		)
	}()

	for {
		notification, err := connection.Conn().WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf(
				"wait for Agent connection revocation: %w",
				err,
			)
		}
		var event AgentConnectionRevocation
		if err := json.Unmarshal([]byte(notification.Payload), &event); err != nil {
			return errors.New("decode Agent connection revocation")
		}
		if strings.TrimSpace(event.AgentID) == "" &&
			strings.TrimSpace(event.ClusterID) == "" {
			return errors.New("Agent connection revocation target is missing")
		}
		handle(event)
	}
}
