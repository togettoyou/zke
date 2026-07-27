package store_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

func TestAgentConnectionStoreActivationAndHeartbeat(t *testing.T) {
	databaseURL := os.Getenv("ZKE_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("ZKE_TEST_DATABASE_URL is required in CI")
		}
		t.Skip("ZKE_TEST_DATABASE_URL is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	const (
		tenantID     = "00000000-0000-4000-8000-000000000001"
		projectID    = "00000000-0000-4000-8000-000000000002"
		clusterID    = "00000000-0000-4000-8000-000000000003"
		agentID      = "00000000-0000-4000-8000-000000000004"
		credentialID = "00000000-0000-4000-8000-000000000005"
	)
	now := time.Now().UTC().Truncate(time.Microsecond)
	batch := &pgx.Batch{}
	batch.Queue(
		"INSERT INTO tenants (id, name, status) VALUES ($1, 'tenant', 'active')",
		tenantID,
	)
	batch.Queue(
		`INSERT INTO projects (id, tenant_id, name, status)
VALUES ($2, $1, 'project', 'active')`,
		tenantID,
		projectID,
	)
	batch.Queue(
		`INSERT INTO clusters (id, tenant_id, project_id, name, status)
VALUES ($3, $1, $2, 'cluster', 'pending')`,
		tenantID,
		projectID,
		clusterID,
	)
	batch.Queue(
		`INSERT INTO agents (
    id, tenant_id, project_id, cluster_id, version, protocol_version,
    lifecycle_status, health_status
) VALUES ($4, $1, $2, $3, 'registered', 'v1', 'pending', 'unknown')`,
		tenantID,
		projectID,
		clusterID,
		agentID,
	)
	batch.Queue(
		`INSERT INTO agent_credentials (
    id, tenant_id, project_id, cluster_id, agent_id, serial,
    csr_fingerprint, certificate_pem, expires_at
) VALUES ($5, $1, $2, $3, $4, '42', decode('01', 'hex'), 'certificate', $6)`,
		tenantID,
		projectID,
		clusterID,
		agentID,
		credentialID,
		now.Add(time.Hour),
	)
	if err := pool.SendBatch(ctx, batch).Close(); err != nil {
		t.Fatal(err)
	}

	connectionStore := store.NewAgentConnectionStore(pool)
	identity := store.AgentConnectionIdentity{
		TenantID:  tenantID,
		ProjectID: projectID,
		ClusterID: clusterID,
		AgentID:   agentID,
	}
	err := connectionStore.Activate(ctx, store.ActivateAgentConnectionParams{
		Identity:          identity,
		CertificateSerial: "99",
		AgentVersion:      "development",
		ProtocolVersion:   "v1",
		HealthStatus:      "healthy",
		Now:               now,
	})
	if !errors.Is(err, store.ErrAgentCredentialRejected) {
		t.Fatalf("Activate() error = %v, want credential rejection", err)
	}

	if err := connectionStore.Activate(
		ctx,
		store.ActivateAgentConnectionParams{
			Identity:          identity,
			CertificateSerial: "42",
			AgentVersion:      "development",
			ProtocolVersion:   "v1",
			HealthStatus:      "healthy",
			Now:               now,
		},
	); err != nil {
		t.Fatal(err)
	}
	var lifecycleStatus, healthStatus, clusterStatus string
	var agentLastSeen, clusterLastSeen time.Time
	if err := pool.QueryRow(
		ctx,
		`
SELECT agent.lifecycle_status, agent.health_status, agent.last_seen_at,
       cluster.status, cluster.last_seen_at
FROM agents AS agent
JOIN clusters AS cluster ON cluster.id = agent.cluster_id
WHERE agent.id = $1
`,
		agentID,
	).Scan(
		&lifecycleStatus,
		&healthStatus,
		&agentLastSeen,
		&clusterStatus,
		&clusterLastSeen,
	); err != nil {
		t.Fatal(err)
	}
	if lifecycleStatus != "active" ||
		healthStatus != "healthy" ||
		clusterStatus != "active" ||
		!agentLastSeen.Equal(now) ||
		!clusterLastSeen.Equal(now) {
		t.Fatalf(
			"unexpected activated state: %s %s %s %s %s",
			lifecycleStatus,
			healthStatus,
			clusterStatus,
			agentLastSeen,
			clusterLastSeen,
		)
	}

	heartbeatAt := now.Add(time.Minute)
	if err := connectionStore.RecordHeartbeat(
		ctx,
		store.RecordAgentHeartbeatParams{
			Identity:          identity,
			CertificateSerial: "42",
			HealthStatus:      "degraded",
			Now:               heartbeatAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(
		ctx,
		"SELECT health_status, last_seen_at FROM agents WHERE id = $1",
		agentID,
	).Scan(&healthStatus, &agentLastSeen); err != nil {
		t.Fatal(err)
	}
	if healthStatus != "degraded" || !agentLastSeen.Equal(heartbeatAt) {
		t.Fatalf(
			"unexpected heartbeat state: %s %s",
			healthStatus,
			agentLastSeen,
		)
	}

	renewedExpiresAt := heartbeatAt.Add(24 * time.Hour)
	renewed, err := connectionStore.RenewCredential(
		ctx,
		store.RenewAgentCredentialParams{
			Identity:                 identity,
			CurrentCertificateSerial: "42",
			CSRFingerprint:           bytes.Repeat([]byte{0x02}, 32),
			NewCertificateSerial:     "43",
			CertificatePEM:           "renewed-certificate",
			CertificateExpiresAt:     renewedExpiresAt,
			RequestID:                "renew-agent-certificate-0001",
			Now:                      heartbeatAt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Serial != "43" ||
		renewed.PEM != "renewed-certificate" ||
		!renewed.ExpiresAt.Equal(renewedExpiresAt) {
		t.Fatalf("unexpected renewed credential: %+v", renewed)
	}
	replayed, err := connectionStore.RenewCredential(
		ctx,
		store.RenewAgentCredentialParams{
			Identity:                 identity,
			CurrentCertificateSerial: "42",
			CSRFingerprint:           bytes.Repeat([]byte{0x02}, 32),
			NewCertificateSerial:     "44",
			CertificatePEM:           "different-replay-certificate",
			CertificateExpiresAt:     renewedExpiresAt,
			RequestID:                "renew-agent-certificate-0001",
			Now:                      heartbeatAt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Serial != renewed.Serial ||
		replayed.PEM != renewed.PEM ||
		!replayed.ExpiresAt.Equal(renewed.ExpiresAt) {
		t.Fatalf("renewal replay changed the result: %+v", replayed)
	}
	if err := connectionStore.Activate(
		ctx,
		store.ActivateAgentConnectionParams{
			Identity:          identity,
			CertificateSerial: renewed.Serial,
			AgentVersion:      "development",
			ProtocolVersion:   "v1",
			HealthStatus:      "healthy",
			Now:               heartbeatAt.Add(time.Second),
		},
	); err != nil {
		t.Fatal(err)
	}
	var oldRevokedAt *time.Time
	if err := pool.QueryRow(
		ctx,
		"SELECT revoked_at FROM agent_credentials WHERE serial = '42'",
	).Scan(&oldRevokedAt); err != nil {
		t.Fatal(err)
	}
	if oldRevokedAt == nil {
		t.Fatal("activating the renewed credential did not revoke the old one")
	}
	statuses, total, err := store.NewAgentStatusStore(pool).
		ListProjectAgentCertificates(ctx, store.ListProjectAgentCertificatesParams{
			ProjectID: projectID,
			Page:      pagination.Request{Limit: pagination.DefaultLimit},
		})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("Agent status total = %d, want 1", total)
	}
	if len(statuses) != 1 ||
		statuses[0].CertificateSerial != renewed.Serial ||
		!statuses[0].CertificateExpiresAt.Equal(renewedExpiresAt) {
		t.Fatalf("Agent status did not select the active credential: %+v", statuses)
	}

	if _, err := pool.Exec(
		ctx,
		"UPDATE agent_credentials SET revoked_at = $2 WHERE serial = $1",
		renewed.Serial,
		heartbeatAt.Add(2*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	err = connectionStore.Activate(ctx, store.ActivateAgentConnectionParams{
		Identity:          identity,
		CertificateSerial: renewed.Serial,
		AgentVersion:      "development",
		ProtocolVersion:   "v1",
		HealthStatus:      "healthy",
		Now:               heartbeatAt.Add(time.Second),
	})
	if !errors.Is(err, store.ErrAgentCredentialRejected) {
		t.Fatalf("Activate() accepted a revoked credential: %v", err)
	}
}
