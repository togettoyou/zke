package migrations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEmbeddedMigrationsAreContiguous(t *testing.T) {
	t.Parallel()

	available, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if len(available) == 0 {
		t.Fatal("no embedded migrations were found")
	}
	// Versions must start at one and increase by one, because a gap would let
	// a Server start against a schema it never fully applied.
	for index, item := range available {
		if item.version != int64(index+1) {
			t.Fatalf(
				"migration %d has version %d, want %d",
				index,
				item.version,
				index+1,
			)
		}
		if item.checksum == "" {
			t.Fatalf("migration %d has an empty checksum", item.version)
		}
	}
}

func TestValidateAppliedRejectsChecksumMismatch(t *testing.T) {
	t.Parallel()

	available, err := load()
	if err != nil {
		t.Fatal(err)
	}
	applied := map[int64]appliedMigration{
		available[0].version: {
			name:     available[0].name,
			checksum: "changed",
		},
	}

	if err := validateApplied(available, applied); err == nil {
		t.Fatal("validateApplied() accepted a changed migration")
	}
}

func TestValidateAppliedRejectsMigrationGap(t *testing.T) {
	t.Parallel()

	available := []migration{
		{version: 1, name: "first", checksum: "first"},
		{version: 2, name: "second", checksum: "second"},
	}
	applied := map[int64]appliedMigration{
		2: {name: "second", checksum: "second"},
	}

	if err := validateApplied(available, applied); err == nil {
		t.Fatal("validateApplied() accepted a non-contiguous migration history")
	}
}

func TestFoundationMigrationDeclaresRequiredContracts(t *testing.T) {
	t.Parallel()

	available, err := load()
	if err != nil {
		t.Fatal(err)
	}

	for _, required := range []string{
		"CONSTRAINT role_bindings_scope_shape",
		"UNIQUE NULLS NOT DISTINCT",
		"CONSTRAINT clusters_project_scope_fk",
		"CONSTRAINT agents_cluster_scope_fk",
		"actor_user_id uuid REFERENCES users (id)",
		"actor_agent_id uuid REFERENCES agents (id)",
		"CONSTRAINT audit_events_actor_shape",
		"token_digest bytea NOT NULL UNIQUE CHECK (octet_length(token_digest) = 32)",
		"csrf_token_digest bytea NOT NULL CHECK (octet_length(csrf_token_digest) = 32)",
		"idempotency_key text NOT NULL",
		"CONSTRAINT enrollments_idempotency_key_format",
		"CONSTRAINT enrollments_cluster_name_format",
		"CONSTRAINT enrollments_creator_idempotency_unique",
		"CREATE INDEX enrollments_active_expiry_idx",
		"CREATE UNIQUE INDEX agent_credentials_agent_csr_unique",
		"CREATE TRIGGER agent_credentials_notify_revocation",
		"CREATE TRIGGER agents_notify_revocation",
		"CREATE TRIGGER clusters_notify_revocation",
		"CREATE INDEX audit_events_scope_time_idx",
		"CREATE TABLE tenant_creation_requests",
		"CREATE TABLE project_creation_requests",
		"UNIQUE (actor_user_id, idempotency_key)",
		"UNIQUE (actor_user_id, tenant_id, idempotency_key)",
		"CREATE TABLE server_pki_state",
		"active_credential_serial text",
		"failed_login_count integer NOT NULL DEFAULT 0",
		"CONSTRAINT users_lock_shape",
		"CREATE INDEX users_status_idx",
	} {
		if !strings.Contains(available[0].sql, required) {
			t.Errorf("foundation migration is missing %q", required)
		}
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	databaseURL := os.Getenv("ZKE_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("ZKE_TEST_DATABASE_URL is required in CI")
		}
		t.Skip("ZKE_TEST_DATABASE_URL is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}

	var randomValue [8]byte
	if _, err := rand.Read(randomValue[:]); err != nil {
		t.Fatal(err)
	}
	schemaName := "zke_migration_test_" + hex.EncodeToString(randomValue[:])
	quotedSchemaName := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchemaName); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		_, _ = adminPool.Exec(ctx, "DROP SCHEMA "+quotedSchemaName+" CASCADE")
		adminPool.Close()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		_, _ = adminPool.Exec(ctx, "DROP SCHEMA "+quotedSchemaName+" CASCADE")
		adminPool.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := adminPool.Exec(context.Background(), "DROP SCHEMA "+quotedSchemaName+" CASCADE"); err != nil {
			t.Errorf("drop integration test schema: %v", err)
		}
		adminPool.Close()
	})

	available, err := load()
	if err != nil {
		t.Fatal(err)
	}
	// Two concurrent Apply calls must together apply each embedded migration
	// exactly once, whatever the current migration count is.
	expectedVersion := int64(len(available))

	type applyResult struct {
		result Result
		err    error
	}
	results := make(chan applyResult, 2)
	for range 2 {
		go func() {
			result, err := Apply(ctx, pool)
			results <- applyResult{result: result, err: err}
		}()
	}

	appliedCount := 0
	for range 2 {
		outcome := <-results
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if outcome.result.CurrentVersion != expectedVersion {
			t.Fatalf(
				"current version = %d, want %d",
				outcome.result.CurrentVersion,
				expectedVersion,
			)
		}
		appliedCount += len(outcome.result.AppliedVersions)
	}
	if appliedCount != len(available) {
		t.Fatalf(
			"concurrent apply changed %d versions, want %d",
			appliedCount,
			len(available),
		)
	}

	idempotentResult, err := Apply(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if idempotentResult.CurrentVersion != expectedVersion {
		t.Fatalf(
			"current version = %d, want %d",
			idempotentResult.CurrentVersion,
			expectedVersion,
		)
	}
	if len(idempotentResult.AppliedVersions) != 0 {
		t.Fatalf("idempotent apply changed versions: %v", idempotentResult.AppliedVersions)
	}

	var tableCount int
	err = pool.QueryRow(ctx, `
SELECT count(*)
FROM information_schema.tables
WHERE table_schema = current_schema()
  AND table_name IN (
      'tenants',
      'projects',
      'users',
      'user_sessions',
      'role_bindings',
      'clusters',
      'agents',
      'agent_credentials',
      'server_pki_state',
      'enrollments',
      'enrollment_attempts',
      'audit_events'
  )`).Scan(&tableCount)
	if err != nil {
		t.Fatal(err)
	}
	if tableCount != 12 {
		t.Fatalf("foundation table count = %d, want 12", tableCount)
	}

	var csrfColumnNullable string
	err = pool.QueryRow(ctx, `
SELECT is_nullable
FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'user_sessions'
  AND column_name = 'csrf_token_digest'
`).Scan(&csrfColumnNullable)
	if err != nil {
		t.Fatal(err)
	}
	if csrfColumnNullable != "NO" {
		t.Fatalf("user_sessions.csrf_token_digest nullable = %q, want NO", csrfColumnNullable)
	}

	testScopeAndAuditConstraints(t, ctx, pool)
	testRequiredIndexes(t, ctx, pool)
}

func testScopeAndAuditConstraints(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	const (
		tenantA  = "00000000-0000-0000-0000-000000000001"
		tenantB  = "00000000-0000-0000-0000-000000000002"
		projectA = "10000000-0000-0000-0000-000000000001"
		userA    = "20000000-0000-0000-0000-000000000001"
	)

	_, err := pool.Exec(ctx, `
INSERT INTO tenants (id, name, status)
VALUES ($1, 'tenant-a', 'active'), ($2, 'tenant-b', 'active')
`, tenantA, tenantB)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
INSERT INTO projects (id, tenant_id, name, status)
VALUES ($1, $2, 'project-a', 'active')
`, projectA, tenantA)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
INSERT INTO users (
    id, username_normalized, display_name, password_hash, status, password_changed_at
)
VALUES ($1, 'review-user', 'Review User', 'argon2id-placeholder', 'active', now())
`, userA)
	if err != nil {
		t.Fatal(err)
	}

	_, err = pool.Exec(ctx, `
INSERT INTO clusters (id, tenant_id, project_id, name, status)
VALUES ('30000000-0000-0000-0000-000000000001', $1, $2, 'wrong-scope', 'pending')
`, tenantB, projectA)
	requirePostgreSQLCode(t, err, "23503")

	_, err = pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type, tenant_id)
VALUES ('50000000-0000-0000-0000-000000000001', $1, 'admin', 'global', $2)
`, userA, tenantA)
	requirePostgreSQLCode(t, err, "23514")

	_, err = pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ('50000000-0000-0000-0000-000000000002', $1, 'admin', 'global')
`, userA)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ('50000000-0000-0000-0000-000000000003', $1, 'admin', 'global')
`, userA)
	requirePostgreSQLCode(t, err, "23505")

	_, err = pool.Exec(ctx, `
INSERT INTO audit_events (
    id, actor_type, scope_type, action, target_type, result, request_id
)
VALUES (
    '60000000-0000-0000-0000-000000000001',
    'user',
    'global',
    'review.invalid_actor',
    'test',
    'denied',
    'request-1'
)
`)
	requirePostgreSQLCode(t, err, "23514")

	_, err = pool.Exec(ctx, `
INSERT INTO audit_events (
    id,
    actor_type,
    actor_user_id,
    scope_type,
    action,
    target_type,
    result,
    request_id
)
VALUES (
    '60000000-0000-0000-0000-000000000002',
    'user',
    '20000000-0000-0000-0000-000000000099',
    'global',
    'review.unknown_actor',
    'test',
    'denied',
    'request-2'
)
`)
	requirePostgreSQLCode(t, err, "23503")
}

func testRequiredIndexes(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	rows, err := pool.Query(ctx, `
SELECT indexname
FROM pg_indexes
WHERE schemaname = current_schema()
`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	found := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	for _, required := range []string{
		"role_bindings_scope_idx",
		"clusters_project_scope_idx",
		"agents_project_scope_idx",
		"enrollments_active_expiry_idx",
		"audit_events_scope_time_idx",
		"audit_events_request_id_idx",
	} {
		if !found[required] {
			t.Errorf("required PostgreSQL index %q is missing", required)
		}
	}
}

func requirePostgreSQLCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("PostgreSQL operation succeeded, want SQLSTATE %s", code)
	}

	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		t.Fatalf("error type = %T, want *pgconn.PgError: %v", err, err)
	}
	if postgresError.Code != code {
		t.Fatalf("PostgreSQL SQLSTATE = %s, want %s: %v", postgresError.Code, code, err)
	}
}
