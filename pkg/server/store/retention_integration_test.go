package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/togettoyou/zke/pkg/server/store"
)

// TestSweepReclaimsOnlyFinishedRows covers the half of retention that is easy to
// get wrong. That a sweep deletes what has ended is the obvious half; that it
// leaves everything else alone is the half a bug hides in, so each table is
// seeded with a finished row and a live one and both outcomes are asserted.
func TestSweepReclaimsOnlyFinishedRows(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	now := time.Now().UTC().Truncate(time.Microsecond)
	policy := store.RetentionPolicy{
		Sessions:    24 * time.Hour,
		Enrollments: 48 * time.Hour,
		Credentials: 48 * time.Hour,
	}
	// Comfortably past every grace above, so "finished" is unambiguous.
	longAgo := now.Add(-30 * 24 * time.Hour)
	seedRetentionFixture(t, ctx, pool, now, longAgo)

	result, err := store.NewRetentionStore(pool).Sweep(ctx, policy, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct {
		name   string
		actual int64
		want   int64
	}{
		{"user sessions", result.Sessions, 2},
		{"enrollments", result.Enrollments, 1},
		{"enrollment attempts", result.EnrollmentAttempts, 1},
		{"Agent credentials", result.Credentials, 1},
	} {
		if expected.actual != expected.want {
			t.Errorf("swept %d %s, want %d", expected.actual, expected.name, expected.want)
		}
	}

	for _, survivor := range []struct {
		name  string
		query string
	}{
		// The live session is still within its absolute expiry.
		{"live session", `SELECT count(*) FROM user_sessions
			WHERE id = '00000000-0000-0000-0000-0000000000a3'`},
		// An outstanding enrollment has neither been used nor lapsed.
		{"outstanding enrollment", `SELECT count(*) FROM enrollments
			WHERE id = '00000000-0000-0000-0000-0000000000b2'`},
		// The credential the Agent is connected with is revoked and long
		// expired, and must still survive: deleting it would clear the pointer
		// naming it.
		{"active credential", `SELECT count(*) FROM agent_credentials
			WHERE serial = 'active-serial'`},
	} {
		var count int
		if err := pool.QueryRow(ctx, survivor.query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("%s was swept away", survivor.name)
		}
	}

	var activeSerial *string
	if err := pool.QueryRow(ctx,
		`SELECT active_credential_serial FROM agents
		 WHERE id = '00000000-0000-0000-0000-0000000000c1'`,
	).Scan(&activeSerial); err != nil {
		t.Fatal(err)
	}
	if activeSerial == nil || *activeSerial != "active-serial" {
		t.Errorf("Agent lost its active credential serial: %v", activeSerial)
	}
}

func seedRetentionFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	now time.Time,
	longAgo time.Time,
) {
	t.Helper()
	statements := []struct {
		sql       string
		arguments []any
	}{
		{`INSERT INTO tenants (id, name, status)
		  VALUES ('00000000-0000-0000-0000-0000000000f1', 'retention', 'active')`, nil},
		{`INSERT INTO projects (id, tenant_id, name, status)
		  VALUES ('00000000-0000-0000-0000-0000000000f2',
		          '00000000-0000-0000-0000-0000000000f1', 'retention', 'active')`, nil},
		{`INSERT INTO clusters (id, tenant_id, project_id, name, status)
		  VALUES ('00000000-0000-0000-0000-0000000000f3',
		          '00000000-0000-0000-0000-0000000000f1',
		          '00000000-0000-0000-0000-0000000000f2', 'retention', 'active')`, nil},
		{`INSERT INTO users (id, username_normalized, display_name, password_hash,
		                     status, password_changed_at)
		  VALUES ('00000000-0000-0000-0000-0000000000f4', 'retention', 'Retention',
		          'not-used', 'active', $1)`, []any{now}},

		// Two finished sessions -- one expired, one revoked -- and one live.
		{`INSERT INTO user_sessions (id, user_id, token_digest, csrf_token_digest,
		                             idle_expires_at, expires_at)
		  VALUES ('00000000-0000-0000-0000-0000000000a1',
		          '00000000-0000-0000-0000-0000000000f4',
		          decode(repeat('a1', 32), 'hex'), decode(repeat('b1', 32), 'hex'),
		          $1, $1)`, []any{longAgo}},
		{`INSERT INTO user_sessions (id, user_id, token_digest, csrf_token_digest,
		                             idle_expires_at, expires_at, revoked_at)
		  VALUES ('00000000-0000-0000-0000-0000000000a2',
		          '00000000-0000-0000-0000-0000000000f4',
		          decode(repeat('a2', 32), 'hex'), decode(repeat('b2', 32), 'hex'),
		          $1, $1, $2)`, []any{now.Add(24 * time.Hour), longAgo}},
		{`INSERT INTO user_sessions (id, user_id, token_digest, csrf_token_digest,
		                             idle_expires_at, expires_at)
		  VALUES ('00000000-0000-0000-0000-0000000000a3',
		          '00000000-0000-0000-0000-0000000000f4',
		          decode(repeat('a3', 32), 'hex'), decode(repeat('b3', 32), 'hex'),
		          $1, $1)`, []any{now.Add(24 * time.Hour)}},

		// One consumed enrollment carrying an attempt, and one still outstanding.
		{`INSERT INTO enrollments (id, tenant_id, project_id, cluster_name,
		      token_digest, created_by_user_id, idempotency_key, expires_at,
		      endpoint_profile_id, endpoint_profile_revision, registration_url,
		      quic_address, registration_ca_certificate_pem, agent_workload,
		      agent_namespace, consumed_at)
		  VALUES ('00000000-0000-0000-0000-0000000000b1',
		          '00000000-0000-0000-0000-0000000000f1',
		          '00000000-0000-0000-0000-0000000000f2', 'consumed',
		          decode(repeat('c1', 16), 'hex'),
		          '00000000-0000-0000-0000-0000000000f4', 'idempotency-key-0001',
		          $1, '00000000-0000-0000-0000-000000000010', 1, 'http://localhost',
		          'localhost:8443', '',
		          '{"image": "image", "image_pull_policy": "IfNotPresent", "cpu_request": "", "memory_request": "", "cpu_limit": "", "memory_limit": ""}',
		          'zke-system', $1)`,
			[]any{longAgo}},
		{`INSERT INTO enrollment_attempts (id, enrollment_id, idempotency_key,
		      csr_fingerprint, status)
		  VALUES ('00000000-0000-0000-0000-0000000000b3',
		          '00000000-0000-0000-0000-0000000000b1', 'attempt-key',
		          decode('01', 'hex'), 'succeeded')`, nil},
		{`INSERT INTO enrollments (id, tenant_id, project_id, cluster_name,
		      token_digest, created_by_user_id, idempotency_key, expires_at,
		      endpoint_profile_id, endpoint_profile_revision, registration_url,
		      quic_address, registration_ca_certificate_pem, agent_workload,
		      agent_namespace)
		  VALUES ('00000000-0000-0000-0000-0000000000b2',
		          '00000000-0000-0000-0000-0000000000f1',
		          '00000000-0000-0000-0000-0000000000f2', 'outstanding',
		          decode(repeat('c2', 16), 'hex'),
		          '00000000-0000-0000-0000-0000000000f4', 'idempotency-key-0002',
		          $1, '00000000-0000-0000-0000-000000000010', 1, 'http://localhost',
		          'localhost:8443', '',
		          '{"image": "image", "image_pull_policy": "IfNotPresent", "cpu_request": "", "memory_request": "", "cpu_limit": "", "memory_limit": ""}',
		          'zke-system')`,
			[]any{now.Add(24 * time.Hour)}},

		// An Agent holding one superseded credential and one it is connected
		// with. Both are revoked and long expired; only the unreferenced one
		// may go.
		{`INSERT INTO agents (id, tenant_id, project_id, cluster_id, version,
		      protocol_version, lifecycle_status, health_status)
		  VALUES ('00000000-0000-0000-0000-0000000000c1',
		          '00000000-0000-0000-0000-0000000000f1',
		          '00000000-0000-0000-0000-0000000000f2',
		          '00000000-0000-0000-0000-0000000000f3',
		          'development', 'v1', 'active', 'healthy')`, nil},
		{`INSERT INTO agent_credentials (id, tenant_id, project_id, cluster_id,
		      agent_id, serial, csr_fingerprint, certificate_pem, expires_at,
		      revoked_at)
		  VALUES ('00000000-0000-0000-0000-0000000000c2',
		          '00000000-0000-0000-0000-0000000000f1',
		          '00000000-0000-0000-0000-0000000000f2',
		          '00000000-0000-0000-0000-0000000000f3',
		          '00000000-0000-0000-0000-0000000000c1', 'superseded-serial',
		          decode('02', 'hex'), 'certificate', $1, $1)`, []any{longAgo}},
		{`INSERT INTO agent_credentials (id, tenant_id, project_id, cluster_id,
		      agent_id, serial, csr_fingerprint, certificate_pem, expires_at,
		      revoked_at)
		  VALUES ('00000000-0000-0000-0000-0000000000c3',
		          '00000000-0000-0000-0000-0000000000f1',
		          '00000000-0000-0000-0000-0000000000f2',
		          '00000000-0000-0000-0000-0000000000f3',
		          '00000000-0000-0000-0000-0000000000c1', 'active-serial',
		          decode('03', 'hex'), 'certificate', $1, $1)`, []any{longAgo}},
		{`UPDATE agents SET active_credential_serial = 'active-serial'
		  WHERE id = '00000000-0000-0000-0000-0000000000c1'`, nil},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.sql, statement.arguments...); err != nil {
			t.Fatalf("seed retention fixture: %v", err)
		}
	}
}
