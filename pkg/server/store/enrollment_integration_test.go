package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

func TestCreateEnrollmentStoresDigestAndAuditAtomically(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	tenantID := insertRBACTenant(t, ctx, pool, "Enrollment Tenant")
	projectID := insertRBACProject(t, ctx, pool, tenantID, "Enrollment Project")
	userID := insertRBACUser(t, ctx, pool, "enrollment-admin")
	tokenDigest := sha256.Sum256([]byte("enrollment-token"))
	expiresAt := time.Now().UTC().Add(15 * time.Minute)

	enrollmentStore := store.NewEnrollmentStore(pool)
	created, err := enrollmentStore.CreateEnrollment(
		ctx,
		store.CreateEnrollmentParams{
			ProjectID:       projectID,
			ClusterName:     "enrollment-cluster",
			CreatedByUserID: userID,
			TokenDigest:     tokenDigest[:],
			ExpiresAt:       expiresAt,
			RequestID:       "request-create-enrollment",
			IdempotencyKey:  "01234567-89ab-cdef-0123-456789abcdef",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.TenantID != tenantID ||
		created.ProjectID != projectID ||
		created.ClusterName != "enrollment-cluster" {
		t.Fatalf(
			"created scope = %s/%s, want %s/%s",
			created.TenantID,
			created.ProjectID,
			tenantID,
			projectID,
		)
	}

	var storedDigest []byte
	if err := pool.QueryRow(
		ctx,
		"SELECT token_digest FROM enrollments WHERE id = $1",
		created.ID,
	).Scan(&storedDigest); err != nil {
		t.Fatal(err)
	}
	if string(storedDigest) != string(tokenDigest[:]) {
		t.Fatal("stored enrollment token digest is incorrect")
	}

	var action, targetType, targetID, result, requestID string
	if err := pool.QueryRow(ctx, `
SELECT action, target_type, target_id::text, result, request_id
FROM audit_events
WHERE target_id = $1
`, created.ID).Scan(
		&action,
		&targetType,
		&targetID,
		&result,
		&requestID,
	); err != nil {
		t.Fatal(err)
	}
	if action != "cluster.enrollment.create" ||
		targetType != "enrollment" ||
		targetID != created.ID ||
		result != "succeeded" ||
		requestID != "request-create-enrollment" {
		t.Fatalf(
			"unexpected audit: %s %s %s %s %s",
			action,
			targetType,
			targetID,
			result,
			requestID,
		)
	}

	secondTokenDigest := sha256.Sum256([]byte("second-enrollment-token"))
	_, err = enrollmentStore.CreateEnrollment(
		ctx,
		store.CreateEnrollmentParams{
			ProjectID:       projectID,
			ClusterName:     "conflicting-enrollment-cluster",
			CreatedByUserID: userID,
			TokenDigest:     secondTokenDigest[:],
			ExpiresAt:       expiresAt,
			RequestID:       "request-retry-enrollment",
			IdempotencyKey:  "01234567-89ab-cdef-0123-456789abcdef",
		},
	)
	if !errors.Is(err, store.ErrEnrollmentIdempotencyConflict) {
		t.Fatalf(
			"duplicate idempotency key error = %v, want ErrEnrollmentIdempotencyConflict",
			err,
		)
	}
	var enrollmentCount, auditCount int
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM enrollments WHERE project_id = $1",
		projectID,
	).Scan(&enrollmentCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_events
WHERE action = 'cluster.enrollment.create'
  AND project_id = $1
  AND result = 'succeeded'
`, projectID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if enrollmentCount != 1 || auditCount != 1 {
		t.Fatalf(
			"retry created enrollments/audits = %d/%d, want 1/1",
			enrollmentCount,
			auditCount,
		)
	}

	if _, err := pool.Exec(
		ctx,
		"UPDATE projects SET status = 'suspended' WHERE id = $1",
		projectID,
	); err != nil {
		t.Fatal(err)
	}
	_, err = enrollmentStore.CreateEnrollment(
		ctx,
		store.CreateEnrollmentParams{
			ProjectID:       projectID,
			ClusterName:     "suspended-enrollment-cluster",
			CreatedByUserID: userID,
			TokenDigest:     tokenDigest[:],
			ExpiresAt:       expiresAt,
			RequestID:       "request-suspended-project",
			IdempotencyKey:  "fedcba98-7654-3210-fedc-ba9876543210",
		},
	)
	if !errors.Is(err, store.ErrEnrollmentCreationDenied) {
		t.Fatalf("suspended project error = %v, want ErrEnrollmentCreationDenied", err)
	}
}

// Cluster names are unique inside their Project, and the rule is enforced at
// both points a name can be claimed: when the enrollment is issued, so the
// operator hears about it while they are still there, and at the unique index
// when the Agent actually creates the Cluster.
//
// Like Tenants and Projects, suspension keeps a Cluster name reserved. Physical
// deletion is terminal and releases it.
func TestClusterNamesAreUniqueWithinProject(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	tenantID := insertRBACTenant(t, ctx, pool, "Cluster Name Tenant")
	projectID := insertRBACProject(t, ctx, pool, tenantID, "Cluster Name Project")
	otherProjectID := insertRBACProject(t, ctx, pool, tenantID, "Other Project")
	userID := insertRBACUser(t, ctx, pool, "cluster-namer")

	insertCluster := func(id string, project string, name string, status string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
INSERT INTO clusters (id, tenant_id, project_id, name, status)
VALUES ($1, $2, $3, $4, $5)
`, id, tenantID, project, name, status); err != nil {
			t.Fatal(err)
		}
	}
	insertCluster("50000000-0000-4000-8000-000000000001", projectID, "prod-a", "active")

	// The same name in the same project is refused outright.
	if _, err := pool.Exec(ctx, `
INSERT INTO clusters (id, tenant_id, project_id, name, status)
VALUES ($1, $2, $3, 'PROD-A', 'pending')
`, "50000000-0000-4000-8000-000000000002", tenantID, projectID); err == nil {
		t.Fatal("inserting a duplicate cluster name unexpectedly succeeded")
	}
	// A different project is a different namespace.
	insertCluster("50000000-0000-4000-8000-000000000003", otherProjectID, "prod-a", "active")

	enrollmentStore := store.NewEnrollmentStore(pool)
	issue := func(name string, key string) error {
		digest := sha256.Sum256([]byte("token-" + key))
		_, err := enrollmentStore.CreateEnrollment(ctx, store.CreateEnrollmentParams{
			ProjectID:       projectID,
			ClusterName:     name,
			CreatedByUserID: userID,
			TokenDigest:     digest[:],
			ExpiresAt:       time.Now().UTC().Add(15 * time.Minute),
			RequestID:       "request-" + key,
			IdempotencyKey:  key,
		})
		return err
	}

	// Issuing a token for a taken name fails now rather than when the Agent
	// turns up holding it.
	if err := issue("prod-a", "11111111-1111-4111-8111-111111111111"); !errors.Is(
		err,
		store.ErrClusterNameConflict,
	) {
		t.Fatalf("CreateEnrollment() for a taken name error = %v, want ErrClusterNameConflict", err)
	}
	if err := issue("PROD-A", "22222222-2222-4222-8222-222222222222"); !errors.Is(
		err,
		store.ErrClusterNameConflict,
	) {
		t.Fatalf("CreateEnrollment() ignoring case error = %v, want ErrClusterNameConflict", err)
	}
	if err := issue("prod-b", "33333333-3333-4333-8333-333333333333"); err != nil {
		t.Fatalf("CreateEnrollment() for a free name: %v", err)
	}

	// Two outstanding enrollments cannot claim the same name either — otherwise
	// both are issued and whichever Agent arrives second fails against the
	// Cluster name index, far from the operator who chose it.
	if err := issue("prod-b", "55555555-5555-4555-8555-555555555555"); !errors.Is(
		err,
		store.ErrClusterNameConflict,
	) {
		t.Fatalf("second enrollment for the same name error = %v, want ErrClusterNameConflict", err)
	}

	// Suspending the Cluster holds the name, exactly as it does for Tenants and
	// Projects: the Cluster is frozen, not gone.
	if _, err := pool.Exec(
		ctx,
		"UPDATE clusters SET status = 'suspended' WHERE id = $1",
		"50000000-0000-4000-8000-000000000001",
	); err != nil {
		t.Fatal(err)
	}
	if err := issue("prod-a", "44444444-4444-4444-8444-444444444444"); !errors.Is(
		err,
		store.ErrClusterNameConflict,
	) {
		t.Fatalf("CreateEnrollment() while the holder is suspended error = %v, want conflict", err)
	}

	// Deleting it releases the name.
	if _, err := pool.Exec(
		ctx,
		"DELETE FROM clusters WHERE id = $1",
		"50000000-0000-4000-8000-000000000001",
	); err != nil {
		t.Fatal(err)
	}
	if err := issue("prod-a", "66666666-6666-4666-8666-666666666666"); err != nil {
		t.Fatalf("CreateEnrollment() after the holder was deleted: %v", err)
	}
	insertCluster("50000000-0000-4000-8000-000000000004", projectID, "prod-a2", "pending")

	insertCluster("50000000-0000-4000-8000-000000000005", projectID, "prod-c", "active")

	// Renaming onto a live name is refused on the same terms.
	resourceStore := store.NewResourceManagementStore(pool)
	if _, err := resourceStore.UpdateCluster(ctx, store.UpdateClusterParams{
		ClusterID:   "50000000-0000-4000-8000-000000000004",
		Name:        "PROD-C",
		ActorUserID: userID,
		RequestID:   "request-cluster-rename-collision",
		Now:         time.Now().UTC(),
	}); !errors.Is(err, store.ErrClusterNameConflict) {
		t.Fatalf("UpdateCluster() onto a taken name error = %v, want ErrClusterNameConflict", err)
	}

	// An outstanding first-enrollment owns its future Cluster name just as much
	// as an existing Cluster does. A rename must not steal that reservation.
	if _, err := resourceStore.UpdateCluster(ctx, store.UpdateClusterParams{
		ClusterID:   "50000000-0000-4000-8000-000000000005",
		Name:        "PROD-B",
		ActorUserID: userID,
		RequestID:   "request-cluster-rename-enrollment-collision",
		Now:         time.Now().UTC(),
	}); !errors.Is(err, store.ErrClusterNameConflict) {
		t.Fatalf(
			"UpdateCluster() onto an Enrollment name error = %v, want ErrClusterNameConflict",
			err,
		)
	}

	// Renaming onto a name no Cluster holds is allowed.
	if _, err := resourceStore.UpdateCluster(ctx, store.UpdateClusterParams{
		ClusterID:   "50000000-0000-4000-8000-000000000005",
		Name:        "prod-unclaimed",
		ActorUserID: userID,
		RequestID:   "request-cluster-rename-free",
		Now:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpdateCluster() onto a free name: %v", err)
	}
}
