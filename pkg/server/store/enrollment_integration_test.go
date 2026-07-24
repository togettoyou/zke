package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func TestCreateEnrollmentStoresDigestAndAuditAtomically(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

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
	if action != "agent.enrollment.create" ||
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
WHERE action = 'agent.enrollment.create'
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
