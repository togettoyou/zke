package store_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

func TestAgentEnrollmentStateMachineIsAtomicAndIdempotent(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	tenantID := insertRBACTenant(t, ctx, pool, "Agent Enrollment Tenant")
	projectID := insertRBACProject(t, ctx, pool, tenantID, "Agent Enrollment Project")
	userID := insertRBACUser(t, ctx, pool, "agent-enrollment-admin")
	enrollmentStore := store.NewEnrollmentStore(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	tokenDigest := sha256.Sum256([]byte("agent-enrollment-token"))
	created, err := enrollmentStore.CreateEnrollment(
		ctx,
		store.CreateEnrollmentParams{
			ProjectID:       projectID,
			ClusterName:     "integration-cluster",
			CreatedByUserID: userID,
			TokenDigest:     tokenDigest[:],
			ExpiresAt:       now.Add(15 * time.Minute),
			RequestID:       "request-create-agent-enrollment",
			IdempotencyKey:  "create-agent-enrollment-0001",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	csrFingerprint := sha256.Sum256([]byte("agent-csr"))
	beginInput := store.BeginAgentEnrollmentParams{
		TokenDigest:    tokenDigest[:],
		IdempotencyKey: "consume-agent-enrollment-0001",
		CSRFingerprint: csrFingerprint[:],
		RequestID:      "request-begin-agent-enrollment",
		Now:            now,
	}
	attempts := make([]store.AgentEnrollmentAttempt, 2)
	beginErrors := make([]error, 2)
	var waitGroup sync.WaitGroup
	for index := range attempts {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			attempts[index], beginErrors[index] = enrollmentStore.BeginAgentEnrollment(
				ctx,
				beginInput,
			)
		}(index)
	}
	waitGroup.Wait()
	for index, beginErr := range beginErrors {
		if beginErr != nil {
			t.Fatalf("concurrent BeginAgentEnrollment() %d error = %v", index, beginErr)
		}
	}
	if attempts[0].ID != attempts[1].ID ||
		attempts[0].Status != store.EnrollmentAttemptPending ||
		attempts[0].EnrollmentID != created.ID ||
		attempts[0].ClusterName != "integration-cluster" ||
		attempts[1].ClusterName != "integration-cluster" {
		t.Fatalf("concurrent attempts are not the same pending attempt: %#v", attempts)
	}

	conflictingFingerprint := sha256.Sum256([]byte("different-agent-csr"))
	_, err = enrollmentStore.BeginAgentEnrollment(
		ctx,
		store.BeginAgentEnrollmentParams{
			TokenDigest:    tokenDigest[:],
			IdempotencyKey: beginInput.IdempotencyKey,
			CSRFingerprint: conflictingFingerprint[:],
			RequestID:      "request-conflicting-agent-enrollment",
			Now:            now,
		},
	)
	if !errors.Is(err, store.ErrEnrollmentAttemptConflict) {
		t.Fatalf("different CSR error = %v, want ErrEnrollmentAttemptConflict", err)
	}

	certificateExpiresAt := now.Add(24 * time.Hour)
	completeInput := store.CompleteAgentEnrollmentParams{
		EnrollmentID:         attempts[0].EnrollmentID,
		AttemptID:            attempts[0].ID,
		IdempotencyKey:       attempts[0].IdempotencyKey,
		CSRFingerprint:       attempts[0].CSRFingerprint,
		ClusterID:            newEnrollmentTestUUID(t),
		AgentID:              newEnrollmentTestUUID(t),
		AgentVersion:         "v0.1.0",
		ProtocolVersion:      "v1",
		CertificateSerial:    "1001",
		CertificatePEM:       "test-public-certificate",
		CertificateExpiresAt: certificateExpiresAt,
		RequestID:            "request-complete-agent-enrollment",
		Now:                  now,
	}
	results := make([]store.AgentEnrollmentResult, 2)
	completeErrors := make([]error, 2)
	for index := range results {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			results[index], completeErrors[index] = enrollmentStore.CompleteAgentEnrollment(
				ctx,
				completeInput,
			)
		}(index)
	}
	waitGroup.Wait()
	for index, completeErr := range completeErrors {
		if completeErr != nil {
			t.Fatalf("concurrent CompleteAgentEnrollment() %d error = %v", index, completeErr)
		}
	}
	if results[0] != results[1] {
		t.Fatalf("concurrent completion results differ: %#v != %#v", results[0], results[1])
	}

	recovered, err := enrollmentStore.BeginAgentEnrollment(
		ctx,
		store.BeginAgentEnrollmentParams{
			TokenDigest:    tokenDigest[:],
			IdempotencyKey: beginInput.IdempotencyKey,
			CSRFingerprint: csrFingerprint[:],
			RequestID:      "request-recover-agent-enrollment",
			Now:            now.Add(time.Minute),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != store.EnrollmentAttemptSucceeded ||
		recovered.Result == nil ||
		*recovered.Result != results[0] {
		t.Fatalf("recovered result = %#v, want %#v", recovered, results[0])
	}

	_, err = enrollmentStore.BeginAgentEnrollment(
		ctx,
		store.BeginAgentEnrollmentParams{
			TokenDigest:    tokenDigest[:],
			IdempotencyKey: "consume-agent-enrollment-0002",
			CSRFingerprint: csrFingerprint[:],
			RequestID:      "request-reuse-consumed-token",
			Now:            now.Add(time.Minute),
		},
	)
	if !errors.Is(err, store.ErrEnrollmentTokenRejected) {
		t.Fatalf("consumed token error = %v, want ErrEnrollmentTokenRejected", err)
	}

	var clusterCount, agentCount, credentialCount, succeededAuditCount int
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM clusters WHERE project_id = $1 AND name = $2",
		projectID,
		"integration-cluster",
	).Scan(&clusterCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM agents WHERE project_id = $1",
		projectID,
	).Scan(&agentCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM agent_credentials WHERE project_id = $1",
		projectID,
	).Scan(&credentialCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_events
WHERE action = 'cluster.enroll'
  AND project_id = $1
  AND result = 'succeeded'
`, projectID).Scan(&succeededAuditCount); err != nil {
		t.Fatal(err)
	}
	if clusterCount != 1 ||
		agentCount != 1 ||
		credentialCount != 1 ||
		succeededAuditCount != 1 {
		t.Fatalf(
			"stored clusters/agents/credentials/audits = %d/%d/%d/%d, want 1/1/1/1",
			clusterCount,
			agentCount,
			credentialCount,
			succeededAuditCount,
		)
	}
	var consumedEnrollmentClusterID string
	if err := pool.QueryRow(ctx, `
SELECT cluster_id::text
FROM enrollments
WHERE id = $1
`, created.ID).Scan(&consumedEnrollmentClusterID); err != nil {
		t.Fatal(err)
	}
	if consumedEnrollmentClusterID != results[0].ClusterID {
		t.Fatalf(
			"consumed Enrollment cluster ID = %q, want %q",
			consumedEnrollmentClusterID,
			results[0].ClusterID,
		)
	}

	if _, err := store.NewAgentManagementStore(pool).Revoke(
		ctx,
		store.RevokeAgentParams{
			ClusterID: results[0].ClusterID, ActorUserID: userID,
			RequestID: "request-revoke-before-reenrollment", Now: now.Add(time.Minute),
		},
	); err != nil {
		t.Fatal(err)
	}
	reenrollmentDigest := sha256.Sum256([]byte("cluster-reenrollment-token"))
	reenrollment, err := enrollmentStore.CreateEnrollment(
		ctx,
		store.CreateEnrollmentParams{
			ProjectID: projectID, ClusterID: results[0].ClusterID,
			ClusterName: "integration-cluster", CreatedByUserID: userID,
			TokenDigest: reenrollmentDigest[:], ExpiresAt: now.Add(20 * time.Minute),
			RequestID:      "request-create-cluster-reenrollment",
			IdempotencyKey: "create-cluster-reenrollment-0001",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	reenrollmentFingerprint := sha256.Sum256([]byte("cluster-reenrollment-csr"))
	if _, err := pool.Exec(
		ctx,
		"UPDATE clusters SET status = 'suspended' WHERE id = $1",
		results[0].ClusterID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := enrollmentStore.BeginAgentEnrollment(
		ctx,
		store.BeginAgentEnrollmentParams{
			TokenDigest:    reenrollmentDigest[:],
			IdempotencyKey: "consume-cluster-reenrollment-0001",
			CSRFingerprint: reenrollmentFingerprint[:],
			RequestID:      "request-begin-suspended-cluster-reenrollment",
			Now:            now.Add(2 * time.Minute),
		},
	); !errors.Is(err, store.ErrEnrollmentTokenRejected) {
		t.Fatalf(
			"BeginAgentEnrollment() for suspended Cluster error = %v, want ErrEnrollmentTokenRejected",
			err,
		)
	}
	if _, err := pool.Exec(
		ctx,
		"UPDATE clusters SET status = 'active' WHERE id = $1",
		results[0].ClusterID,
	); err != nil {
		t.Fatal(err)
	}
	reenrollmentAttempt, err := enrollmentStore.BeginAgentEnrollment(
		ctx,
		store.BeginAgentEnrollmentParams{
			TokenDigest:    reenrollmentDigest[:],
			IdempotencyKey: "consume-cluster-reenrollment-0001",
			CSRFingerprint: reenrollmentFingerprint[:],
			RequestID:      "request-begin-cluster-reenrollment",
			Now:            now.Add(2 * time.Minute),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if reenrollment.ClusterID != results[0].ClusterID ||
		reenrollmentAttempt.ClusterID != results[0].ClusterID {
		t.Fatalf(
			"reenrollment cluster IDs = %q/%q, want %q",
			reenrollment.ClusterID,
			reenrollmentAttempt.ClusterID,
			results[0].ClusterID,
		)
	}
	secondAgentID := newEnrollmentTestUUID(t)
	reenrollmentResult, err := enrollmentStore.CompleteAgentEnrollment(
		ctx,
		store.CompleteAgentEnrollmentParams{
			EnrollmentID: reenrollment.ID, AttemptID: reenrollmentAttempt.ID,
			IdempotencyKey: reenrollmentAttempt.IdempotencyKey,
			CSRFingerprint: reenrollmentAttempt.CSRFingerprint,
			ClusterID:      results[0].ClusterID, AgentID: secondAgentID,
			AgentVersion: "v0.2.0", ProtocolVersion: "v1",
			CertificateSerial: "1002", CertificatePEM: "second-test-certificate",
			CertificateExpiresAt: now.Add(48 * time.Hour),
			RequestID:            "request-complete-cluster-reenrollment",
			Now:                  now.Add(2 * time.Minute),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if reenrollmentResult.ClusterID != results[0].ClusterID ||
		reenrollmentResult.AgentID != secondAgentID {
		t.Fatalf("unexpected reenrollment result: %+v", reenrollmentResult)
	}
	var clustersAfterReenrollment, agentsAfterReenrollment, activeAgents int
	if err := pool.QueryRow(ctx, `
SELECT
    (SELECT count(*) FROM clusters WHERE id = $1),
    (SELECT count(*) FROM agents WHERE cluster_id = $1),
    (
        SELECT count(*) FROM agents
        WHERE cluster_id = $1 AND lifecycle_status <> 'revoked'
    )
`, results[0].ClusterID).Scan(
		&clustersAfterReenrollment,
		&agentsAfterReenrollment,
		&activeAgents,
	); err != nil {
		t.Fatal(err)
	}
	if clustersAfterReenrollment != 1 ||
		agentsAfterReenrollment != 2 ||
		activeAgents != 1 {
		t.Fatalf(
			"reenrollment clusters/agents/active = %d/%d/%d, want 1/2/1",
			clustersAfterReenrollment,
			agentsAfterReenrollment,
			activeAgents,
		)
	}
}

func TestAgentEnrollmentRejectsExpiredTokenAndRollsBackFailedCompletion(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	tenantID := insertRBACTenant(t, ctx, pool, "Agent Enrollment Rollback Tenant")
	projectID := insertRBACProject(t, ctx, pool, tenantID, "Agent Enrollment Rollback Project")
	userID := insertRBACUser(t, ctx, pool, "agent-enrollment-rollback-admin")
	enrollmentStore := store.NewEnrollmentStore(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)

	expiredTokenDigest := sha256.Sum256([]byte("expired-agent-enrollment-token"))
	expired, err := enrollmentStore.CreateEnrollment(
		ctx,
		store.CreateEnrollmentParams{
			ProjectID:       projectID,
			ClusterName:     "expired-cluster",
			CreatedByUserID: userID,
			TokenDigest:     expiredTokenDigest[:],
			ExpiresAt:       now.Add(-time.Minute),
			RequestID:       "request-create-expired-enrollment",
			IdempotencyKey:  "create-expired-enrollment-0001",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	csrFingerprint := sha256.Sum256([]byte("rollback-agent-csr"))
	_, err = enrollmentStore.BeginAgentEnrollment(
		ctx,
		store.BeginAgentEnrollmentParams{
			TokenDigest:    expiredTokenDigest[:],
			IdempotencyKey: "consume-expired-enrollment-0001",
			CSRFingerprint: csrFingerprint[:],
			RequestID:      "request-consume-expired-enrollment",
			Now:            now,
		},
	)
	if !errors.Is(err, store.ErrEnrollmentTokenRejected) {
		t.Fatalf("expired token error = %v, want ErrEnrollmentTokenRejected", err)
	}
	var deniedAuditCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_events
WHERE action = 'cluster.enroll'
  AND target_id = $1
  AND result = 'denied'
`, expired.ID).Scan(&deniedAuditCount); err != nil {
		t.Fatal(err)
	}
	if deniedAuditCount != 1 {
		t.Fatalf("expired token denied audit count = %d, want 1", deniedAuditCount)
	}

	firstResult := createCompletedEnrollmentForSerial(
		t,
		ctx,
		enrollmentStore,
		projectID,
		userID,
		now,
		"first-rollback-token",
		"create-first-rollback-0001",
		"consume-first-rollback-0001",
		"2001",
	)
	secondTokenDigest := sha256.Sum256([]byte("second-rollback-token"))
	secondEnrollment, err := enrollmentStore.CreateEnrollment(
		ctx,
		store.CreateEnrollmentParams{
			ProjectID:       projectID,
			ClusterName:     "second-rollback-cluster",
			CreatedByUserID: userID,
			TokenDigest:     secondTokenDigest[:],
			ExpiresAt:       now.Add(15 * time.Minute),
			RequestID:       "request-create-second-rollback",
			IdempotencyKey:  "create-second-rollback-0001",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondFingerprint := sha256.Sum256([]byte("second-rollback-csr"))
	secondAttempt, err := enrollmentStore.BeginAgentEnrollment(
		ctx,
		store.BeginAgentEnrollmentParams{
			TokenDigest:    secondTokenDigest[:],
			IdempotencyKey: "consume-second-rollback-0001",
			CSRFingerprint: secondFingerprint[:],
			RequestID:      "request-begin-second-rollback",
			Now:            now,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = enrollmentStore.CompleteAgentEnrollment(
		ctx,
		store.CompleteAgentEnrollmentParams{
			EnrollmentID:         secondEnrollment.ID,
			AttemptID:            secondAttempt.ID,
			IdempotencyKey:       secondAttempt.IdempotencyKey,
			CSRFingerprint:       secondAttempt.CSRFingerprint,
			ClusterID:            newEnrollmentTestUUID(t),
			AgentID:              newEnrollmentTestUUID(t),
			AgentVersion:         "v0.1.0",
			ProtocolVersion:      "v1",
			CertificateSerial:    "2001",
			CertificatePEM:       "duplicate-serial-certificate",
			CertificateExpiresAt: now.Add(24 * time.Hour),
			RequestID:            "request-fail-second-rollback",
			Now:                  now,
		},
	)
	if err == nil {
		t.Fatal("duplicate certificate serial unexpectedly completed enrollment")
	}

	var status string
	var consumedAt *time.Time
	if err := pool.QueryRow(ctx, `
SELECT attempt.status, enrollment.consumed_at
FROM enrollment_attempts AS attempt
JOIN enrollments AS enrollment ON enrollment.id = attempt.enrollment_id
WHERE attempt.id = $1
`, secondAttempt.ID).Scan(&status, &consumedAt); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || consumedAt != nil {
		t.Fatalf("failed completion left status/consumed_at = %s/%v, want pending/nil", status, consumedAt)
	}
	var rolledBackClusterCount int
	if err := pool.QueryRow(
		ctx,
		"SELECT count(*) FROM clusters WHERE name = 'second-rollback-cluster'",
	).Scan(&rolledBackClusterCount); err != nil {
		t.Fatal(err)
	}
	if rolledBackClusterCount != 0 {
		t.Fatalf("failed completion retained %d cluster rows, want 0", rolledBackClusterCount)
	}
	if firstResult.ClusterID == "" {
		t.Fatal("first completed enrollment returned no cluster")
	}
}

func createCompletedEnrollmentForSerial(
	t *testing.T,
	ctx context.Context,
	enrollmentStore *store.EnrollmentStore,
	projectID string,
	userID string,
	now time.Time,
	token string,
	createKey string,
	consumeKey string,
	serial string,
) store.AgentEnrollmentResult {
	t.Helper()
	tokenDigest := sha256.Sum256([]byte(token))
	enrollment, err := enrollmentStore.CreateEnrollment(
		ctx,
		store.CreateEnrollmentParams{
			ProjectID:       projectID,
			ClusterName:     "completed-cluster-" + token,
			CreatedByUserID: userID,
			TokenDigest:     tokenDigest[:],
			ExpiresAt:       now.Add(15 * time.Minute),
			RequestID:       "request-" + createKey,
			IdempotencyKey:  createKey,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256([]byte(consumeKey))
	attempt, err := enrollmentStore.BeginAgentEnrollment(
		ctx,
		store.BeginAgentEnrollmentParams{
			TokenDigest:    tokenDigest[:],
			IdempotencyKey: consumeKey,
			CSRFingerprint: fingerprint[:],
			RequestID:      "request-" + consumeKey,
			Now:            now,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := enrollmentStore.CompleteAgentEnrollment(
		ctx,
		store.CompleteAgentEnrollmentParams{
			EnrollmentID:         enrollment.ID,
			AttemptID:            attempt.ID,
			IdempotencyKey:       attempt.IdempotencyKey,
			CSRFingerprint:       attempt.CSRFingerprint,
			ClusterID:            newEnrollmentTestUUID(t),
			AgentID:              newEnrollmentTestUUID(t),
			AgentVersion:         "v0.1.0",
			ProtocolVersion:      "v1",
			CertificateSerial:    serial,
			CertificatePEM:       "certificate-" + serial,
			CertificateExpiresAt: now.Add(24 * time.Hour),
			RequestID:            "request-complete-" + serial,
			Now:                  now,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func newEnrollmentTestUUID(t *testing.T) string {
	t.Helper()
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	var encoded [36]byte
	hex.Encode(encoded[0:8], value[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], value[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], value[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], value[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], value[10:16])
	return string(encoded[:])
}
