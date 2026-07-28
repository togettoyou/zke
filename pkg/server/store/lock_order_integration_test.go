package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func TestAgentEnrollmentLocksScopeBeforeEnrollment(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := openIsolatedDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	tenantID := insertRBACTenant(t, ctx, pool, "Enrollment Lock Tenant")
	projectID := insertRBACProject(t, ctx, pool, tenantID, "Enrollment Lock Project")
	userID := insertRBACUser(t, ctx, pool, "enrollment-lock-user")
	clusterID := "70000000-0000-4000-8000-000000000001"
	if _, err := pool.Exec(ctx, `
INSERT INTO clusters (id, tenant_id, project_id, name, status)
VALUES ($1, $2, $3, 'enrollment-lock-cluster', 'active')
`, clusterID, tenantID, projectID); err != nil {
		t.Fatal(err)
	}

	tokenDigest := sha256.Sum256([]byte("enrollment-lock-token"))
	enrollmentStore := store.NewEnrollmentStore(pool)
	enrollment, err := enrollmentStore.CreateEnrollment(ctx, store.CreateEnrollmentParams{
		ProjectID:       projectID,
		ClusterID:       clusterID,
		ClusterName:     "enrollment-lock-cluster",
		CreatedByUserID: userID,
		TokenDigest:     tokenDigest[:],
		ExpiresAt:       time.Now().UTC().Add(15 * time.Minute),
		RequestID:       "request-create-enrollment-lock",
		IdempotencyKey:  "create-enrollment-lock-0001",
	})
	if err != nil {
		t.Fatal(err)
	}

	clusterBlocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clusterBlocker.Exec(ctx, `
SELECT id FROM clusters WHERE id = $1 FOR UPDATE
`, clusterID); err != nil {
		_ = clusterBlocker.Rollback(context.Background())
		t.Fatal(err)
	}

	beginResult := make(chan error, 1)
	go func() {
		fingerprint := sha256.Sum256([]byte("enrollment-lock-csr"))
		_, beginErr := enrollmentStore.BeginAgentEnrollment(
			ctx,
			store.BeginAgentEnrollmentParams{
				TokenDigest:    tokenDigest[:],
				IdempotencyKey: "consume-enrollment-lock-0001",
				CSRFingerprint: fingerprint[:],
				RequestID:      "request-begin-enrollment-lock",
				Now:            time.Now().UTC(),
			},
		)
		beginResult <- beginErr
	}()

	projectLocked, lockErr := waitForRowLock(
		ctx,
		pool,
		"SELECT id::text FROM projects WHERE id = $1 FOR UPDATE NOWAIT",
		projectID,
	)
	var enrollmentProbeErr error
	if lockErr == nil && projectLocked {
		probe, probeErr := pool.Begin(ctx)
		if probeErr != nil {
			enrollmentProbeErr = probeErr
		} else {
			var ignored string
			enrollmentProbeErr = probe.QueryRow(ctx, `
SELECT id::text FROM enrollments WHERE id = $1 FOR UPDATE NOWAIT
`, enrollment.ID).Scan(&ignored)
			_ = probe.Rollback(context.Background())
		}
	}
	_ = clusterBlocker.Rollback(context.Background())
	beginErr := <-beginResult

	if lockErr != nil {
		t.Fatal(lockErr)
	}
	if !projectLocked {
		t.Fatal("BeginAgentEnrollment did not acquire the Project lock while waiting for Cluster")
	}
	if enrollmentProbeErr != nil {
		t.Fatalf(
			"BeginAgentEnrollment locked Enrollment before Cluster: %v",
			enrollmentProbeErr,
		)
	}
	if beginErr != nil {
		t.Fatalf("BeginAgentEnrollment after releasing Cluster lock: %v", beginErr)
	}
}

func TestProjectDeletionLocksClustersBeforeEnrollments(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := openIsolatedDatabase(t, ctx, databaseURL)
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	tenantID := insertRBACTenant(t, ctx, pool, "Delete Lock Tenant")
	projectID := insertRBACProject(t, ctx, pool, tenantID, "Delete Lock Project")
	userID := insertRBACUser(t, ctx, pool, "delete-lock-user")
	clusterID := "70000000-0000-4000-8000-000000000002"
	if _, err := pool.Exec(ctx, `
INSERT INTO clusters (id, tenant_id, project_id, name, status)
VALUES ($1, $2, $3, 'delete-lock-cluster', 'active')
`, clusterID, tenantID, projectID); err != nil {
		t.Fatal(err)
	}
	tokenDigest := sha256.Sum256([]byte("delete-lock-token"))
	enrollment, err := store.NewEnrollmentStore(pool).CreateEnrollment(
		ctx,
		store.CreateEnrollmentParams{
			ProjectID:       projectID,
			ClusterID:       clusterID,
			ClusterName:     "delete-lock-cluster",
			CreatedByUserID: userID,
			TokenDigest:     tokenDigest[:],
			ExpiresAt:       time.Now().UTC().Add(15 * time.Minute),
			RequestID:       "request-create-delete-lock",
			IdempotencyKey:  "create-delete-lock-0001",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	enrollmentBlocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enrollmentBlocker.Exec(ctx, `
SELECT id FROM enrollments WHERE id = $1 FOR UPDATE
`, enrollment.ID); err != nil {
		_ = enrollmentBlocker.Rollback(context.Background())
		t.Fatal(err)
	}

	deleteResult := make(chan error, 1)
	go func() {
		_, deleteErr := store.NewResourceManagementStore(pool).DeleteProject(
			ctx,
			store.DeleteProjectParams{
				ProjectID:   projectID,
				ActorUserID: userID,
				RequestID:   "request-delete-lock-project",
				Now:         time.Now().UTC(),
			},
		)
		deleteResult <- deleteErr
	}()

	clusterLocked, lockErr := waitForRowLock(
		ctx,
		pool,
		"SELECT id::text FROM clusters WHERE id = $1 FOR UPDATE NOWAIT",
		clusterID,
	)
	_ = enrollmentBlocker.Rollback(context.Background())
	deleteErr := <-deleteResult

	if lockErr != nil {
		t.Fatal(lockErr)
	}
	if !clusterLocked {
		t.Fatal("DeleteProject reached Enrollment before locking its Clusters")
	}
	if deleteErr != nil {
		t.Fatalf("DeleteProject after releasing Enrollment lock: %v", deleteErr)
	}
}

func waitForRowLock(
	ctx context.Context,
	pool *pgxpool.Pool,
	query string,
	argument string,
) (bool, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		locked, err := rowLockUnavailable(ctx, pool, query, argument)
		if err != nil {
			return false, err
		}
		if locked {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
}

func rowLockUnavailable(
	ctx context.Context,
	pool *pgxpool.Pool,
	query string,
	argument string,
) (bool, error) {
	transaction, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()

	var ignored string
	err = transaction.QueryRow(ctx, query, argument).Scan(&ignored)
	if err == nil {
		return false, nil
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "55P03" {
		return true, nil
	}
	return false, fmt.Errorf("probe row lock: %w", err)
}
