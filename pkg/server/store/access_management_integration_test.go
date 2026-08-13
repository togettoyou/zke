package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/togettoyou/zke/pkg/server/store"
)

func TestDeleteUserPhysicallyRemovesAccessStateAndKeepsHistory(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	const (
		actorID          = "72000000-0000-4000-8000-000000000001"
		userID           = "72000000-0000-4000-8000-000000000002"
		bindingID        = "72000000-0000-4000-8000-000000000003"
		sessionID        = "72000000-0000-4000-8000-000000000004"
		tenantID         = "72000000-0000-4000-8000-000000000005"
		projectID        = "72000000-0000-4000-8000-000000000006"
		enrollmentID     = "72000000-0000-4000-8000-000000000007"
		tenantRequestID  = "72000000-0000-4000-8000-000000000008"
		projectRequestID = "72000000-0000-4000-8000-000000000009"
	)
	now := time.Now().UTC().Truncate(time.Microsecond)
	batch := &pgx.Batch{}
	batch.Queue(`
INSERT INTO users (
    id, username_normalized, display_name, password_hash, status,
    password_changed_at
) VALUES
    ($1, 'deletion-actor', 'Deletion Actor', 'not-used', 'active', $3),
    ($2, 'physical-delete-user', 'Physical Delete User', 'not-used', 'active', $3)
`, actorID, userID, now)
	batch.Queue(`
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ($1, $2, 'viewer', 'global')
`, bindingID, userID)
	batch.Queue(`
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ('72000000-0000-4000-8000-00000000000a', $1, 'admin', 'global')
`, actorID)
	batch.Queue(`
INSERT INTO user_sessions (
    id, user_id, token_digest, csrf_token_digest, idle_expires_at, expires_at
) VALUES (
    $1, $2, decode(repeat('11', 32), 'hex'),
    decode(repeat('22', 32), 'hex'), $3, $4
)
`, sessionID, userID, now.Add(time.Hour), now.Add(2*time.Hour))
	batch.Queue(
		"INSERT INTO tenants (id, name, status) VALUES ($1, 'Deletion History Tenant', 'active')",
		tenantID,
	)
	batch.Queue(`
INSERT INTO projects (id, tenant_id, name, status)
VALUES ($1, $2, 'Deletion History Project', 'active')
`, projectID, tenantID)
	batch.Queue(`
INSERT INTO enrollments (
    id, tenant_id, project_id, cluster_name, token_digest,
    created_by_user_id, idempotency_key, expires_at
) VALUES (
    $1, $2, $3, 'history-cluster', decode(repeat('33', 32), 'hex'),
    $4, 'history-enrollment-key', $5
)
`, enrollmentID, tenantID, projectID, userID, now.Add(time.Hour))
	batch.Queue(`
INSERT INTO tenant_creation_requests (
    id, actor_user_id, idempotency_key, requested_name, tenant_id
) VALUES ($1, $2, 'tenant-history-key', 'Deletion History Tenant', $3)
`, tenantRequestID, userID, tenantID)
	batch.Queue(`
INSERT INTO project_creation_requests (
    id, actor_user_id, tenant_id, idempotency_key, requested_name, project_id
) VALUES (
    $1, $2, $3, 'project-history-key', 'Deletion History Project', $4
)
`, projectRequestID, userID, tenantID, projectID)
	if err := pool.SendBatch(ctx, batch).Close(); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.NewAccessManagementStore(pool).DeleteUser(
		ctx,
		store.DeleteManagedUserParams{
			UserID:      userID,
			ActorUserID: actorID,
			RequestID:   "request-physical-user-delete",
			Now:         now,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.ID != userID ||
		deleted.Username != "physical-delete-user" ||
		deleted.Status != "active" {
		t.Fatalf("deleted user snapshot = %+v", deleted)
	}

	var users, sessions, bindings int
	var enrollments, tenantRequests, projectRequests int
	if err := pool.QueryRow(ctx, `
SELECT
    (SELECT count(*) FROM users WHERE id = $1),
    (SELECT count(*) FROM user_sessions WHERE user_id = $1),
    (SELECT count(*) FROM role_bindings WHERE subject_id = $1),
    (SELECT count(*) FROM enrollments WHERE created_by_user_id = $1),
    (SELECT count(*) FROM tenant_creation_requests WHERE actor_user_id = $1),
    (SELECT count(*) FROM project_creation_requests WHERE actor_user_id = $1)
`, userID).Scan(
		&users,
		&sessions,
		&bindings,
		&enrollments,
		&tenantRequests,
		&projectRequests,
	); err != nil {
		t.Fatal(err)
	}
	if users != 0 || sessions != 0 || bindings != 0 ||
		enrollments != 1 || tenantRequests != 1 || projectRequests != 1 {
		t.Fatalf(
			"deleted user rows user/session/binding/history = %d/%d/%d/%d/%d/%d",
			users,
			sessions,
			bindings,
			enrollments,
			tenantRequests,
			projectRequests,
		)
	}

	var actorName, targetName string
	if err := pool.QueryRow(ctx, `
SELECT actor_user_name, target_name
FROM audit_events
WHERE request_id = 'request-physical-user-delete'
`).Scan(&actorName, &targetName); err != nil {
		t.Fatal(err)
	}
	if actorName != "deletion-actor" || targetName != "physical-delete-user" {
		t.Fatalf("user deletion audit names = %q/%q", actorName, targetName)
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO users (
    id, username_normalized, display_name, password_hash, status,
    password_changed_at
) VALUES (
    '72000000-0000-4000-8000-000000000010',
    'physical-delete-user',
    'Recreated User',
    'not-used',
    'active',
    $1
)
`, now); err != nil {
		t.Fatalf("reuse physically deleted username: %v", err)
	}
}
