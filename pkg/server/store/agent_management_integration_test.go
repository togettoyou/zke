package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func TestAgentManagementStoreRevoke(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	const (
		tenantID     = "10000000-0000-4000-8000-000000000001"
		projectID    = "10000000-0000-4000-8000-000000000002"
		clusterID    = "10000000-0000-4000-8000-000000000003"
		agentID      = "10000000-0000-4000-8000-000000000004"
		credentialID = "10000000-0000-4000-8000-000000000005"
		userID       = "10000000-0000-4000-8000-000000000006"
	)
	expiresAt := time.Now().UTC().Add(24 * time.Hour)
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
VALUES ($3, $1, $2, 'cluster', 'active')`,
		tenantID,
		projectID,
		clusterID,
	)
	batch.Queue(
		`INSERT INTO agents (
    id, tenant_id, project_id, cluster_id, version, protocol_version,
    lifecycle_status, health_status, active_credential_serial
) VALUES (
    $4, $1, $2, $3, 'development', 'v1', 'active', 'healthy', '42'
)`,
		tenantID,
		projectID,
		clusterID,
		agentID,
	)
	batch.Queue(
		`INSERT INTO agent_credentials (
    id, tenant_id, project_id, cluster_id, agent_id, serial,
    csr_fingerprint, certificate_pem, expires_at
) VALUES (
    $5, $1, $2, $3, $4, '42', decode('01', 'hex'), 'certificate', $6
)`,
		tenantID,
		projectID,
		clusterID,
		agentID,
		credentialID,
		expiresAt,
	)
	batch.Queue(
		`INSERT INTO users (
    id, username_normalized, display_name, password_hash, status,
    password_changed_at
) VALUES ($1, 'revoker', 'Revoker', 'not-used', 'active', now())`,
		userID,
	)
	if err := pool.SendBatch(ctx, batch).Close(); err != nil {
		t.Fatal(err)
	}

	watchContext, cancelWatch := context.WithCancel(ctx)
	defer cancelWatch()
	ready := make(chan struct{})
	events := make(chan store.AgentConnectionRevocation, 4)
	watchErrors := make(chan error, 1)
	go func() {
		watchErrors <- store.NewAgentConnectionStore(pool).WatchRevocations(
			watchContext,
			ready,
			func(event store.AgentConnectionRevocation) {
				events <- event
			},
		)
	}()
	select {
	case <-ready:
	case err := <-watchErrors:
		t.Fatalf("start revocation watcher: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	revokedAt := time.Now().UTC().Truncate(time.Microsecond)
	managementStore := store.NewAgentManagementStore(pool)
	result, err := managementStore.Revoke(ctx, store.RevokeAgentParams{
		AgentID:     agentID,
		ActorUserID: userID,
		RequestID:   "request-agent-revoke-0001",
		Now:         revokedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentID != agentID ||
		result.AlreadyRevoked ||
		!result.RevokedAt.Equal(revokedAt) {
		t.Fatalf("unexpected revoke result: %+v", result)
	}

	select {
	case event := <-events:
		if event.AgentID != agentID {
			t.Fatalf("revocation event = %+v, want Agent %s", event, agentID)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	var lifecycleStatus, healthStatus string
	var storedRevokedAt time.Time
	if err := pool.QueryRow(ctx, `
SELECT agent.lifecycle_status, agent.health_status, credential.revoked_at
FROM agents AS agent
JOIN agent_credentials AS credential ON credential.agent_id = agent.id
WHERE agent.id = $1
`, agentID).Scan(
		&lifecycleStatus,
		&healthStatus,
		&storedRevokedAt,
	); err != nil {
		t.Fatal(err)
	}
	if lifecycleStatus != "revoked" ||
		healthStatus != "unknown" ||
		!storedRevokedAt.Equal(revokedAt) {
		t.Fatalf(
			"stored revocation = %s/%s/%s",
			lifecycleStatus,
			healthStatus,
			storedRevokedAt,
		)
	}

	repeated, err := managementStore.Revoke(ctx, store.RevokeAgentParams{
		AgentID:     agentID,
		ActorUserID: userID,
		RequestID:   "request-agent-revoke-0002",
		Now:         revokedAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.AlreadyRevoked ||
		!repeated.RevokedAt.Equal(revokedAt) {
		t.Fatalf("unexpected repeated revoke result: %+v", repeated)
	}

	var succeededAudits int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_events
WHERE actor_user_id = $1
  AND actor_agent_id IS NULL
  AND cluster_id = $2
  AND action = 'agent.revoke'
  AND target_type = 'agent'
  AND target_id = $3
  AND result = 'succeeded'
`, userID, clusterID, agentID).Scan(&succeededAudits); err != nil {
		t.Fatal(err)
	}
	if succeededAudits != 2 {
		t.Fatalf("successful revocation audit count = %d, want 2", succeededAudits)
	}

	_, err = managementStore.Revoke(ctx, store.RevokeAgentParams{
		AgentID:     "10000000-0000-4000-8000-000000000099",
		ActorUserID: userID,
		RequestID:   "request-agent-revoke-missing",
		Now:         revokedAt,
	})
	if !errors.Is(err, store.ErrAgentNotFound) {
		t.Fatalf("missing Agent error = %v, want ErrAgentNotFound", err)
	}
}
