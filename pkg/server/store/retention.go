package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RetentionStore reclaims rows whose purpose has ended.
//
// Four tables in this schema grow with use and nothing ever shrank them: a row
// per login, a row per enrollment and its attempts, a row per issued
// credential. Each already carries the timestamp that says when it stopped
// mattering, and each already has the index to find those rows -- the sweeps
// below are the readers those indexes were built for.
//
// `audit_events` is deliberately absent. It is the one table here whose value
// is precisely that it outlives its subjects, and how long an operator must
// keep it is a compliance question with no defensible default. Deleting it on a
// timer this package chose would be the wrong kind of decision to make quietly.
type RetentionStore struct {
	pool *pgxpool.Pool
}

func NewRetentionStore(pool *pgxpool.Pool) *RetentionStore {
	return &RetentionStore{pool: pool}
}

// RetentionPolicy is how long a finished row is kept past the moment it
// finished. The grace is not politeness: a session that expired seconds ago may
// still be the subject of a support question, and a credential deleted the
// instant it is superseded takes its serial out of the Agent row that names it.
type RetentionPolicy struct {
	Sessions    time.Duration
	Enrollments time.Duration
	Credentials time.Duration
}

// RetentionResult reports what a sweep reclaimed, per table, for the log line.
type RetentionResult struct {
	Sessions           int64
	Enrollments        int64
	EnrollmentAttempts int64
	Credentials        int64
}

// Sweep deletes what each policy says is finished, and reports how much went.
//
// Every statement is independent and bounded by its own timestamp predicate, so
// a sweep that fails partway leaves a consistent database and the next one
// picks up the rest. They run in one transaction only where a table references
// another -- enrollment attempts before the enrollments they belong to.
func (store *RetentionStore) Sweep(
	ctx context.Context,
	policy RetentionPolicy,
	now time.Time,
) (RetentionResult, error) {
	result := RetentionResult{}

	// A session is finished when it has expired outright or been revoked.
	// Idle expiry is not a deletion trigger: an idle session is refused at
	// authentication but its absolute expiry is still ahead, and the row is
	// what makes that refusal explicable.
	sessions, err := store.pool.Exec(ctx, `
DELETE FROM user_sessions
WHERE (expires_at <= $1)
   OR (revoked_at IS NOT NULL AND revoked_at <= $1)`,
		now.Add(-policy.Sessions))
	if err != nil {
		return result, fmt.Errorf("sweep expired user sessions: %w", err)
	}
	result.Sessions = sessions.RowsAffected()

	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin enrollment retention sweep: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// An enrollment is finished when it was consumed, revoked, or lapsed
	// unused. Attempts go first because they reference it.
	enrollmentCutoff := now.Add(-policy.Enrollments)
	const finishedEnrollments = `
SELECT id
FROM enrollments
WHERE (consumed_at IS NOT NULL AND consumed_at <= $1)
   OR (revoked_at IS NOT NULL AND revoked_at <= $1)
   OR (consumed_at IS NULL AND revoked_at IS NULL AND expires_at <= $1)`

	attempts, err := transaction.Exec(ctx, `
DELETE FROM enrollment_attempts
WHERE enrollment_id IN (`+finishedEnrollments+`)`, enrollmentCutoff)
	if err != nil {
		return result, fmt.Errorf("sweep finished enrollment attempts: %w", err)
	}
	result.EnrollmentAttempts = attempts.RowsAffected()

	enrollments, err := transaction.Exec(ctx,
		`DELETE FROM enrollments WHERE id IN (`+finishedEnrollments+`)`,
		enrollmentCutoff)
	if err != nil {
		return result, fmt.Errorf("sweep finished enrollments: %w", err)
	}
	result.Enrollments = enrollments.RowsAffected()

	if err := transaction.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit enrollment retention sweep: %w", err)
	}

	// A credential is finished when it was revoked or has expired -- but never
	// while an Agent still names it as its active one. The foreign key would
	// clear that pointer rather than refuse the delete, which is right for
	// Cluster deletion and wrong here: it would erase the Server's record of
	// which credential a live connection is using.
	credentials, err := store.pool.Exec(ctx, `
DELETE FROM agent_credentials AS credential
WHERE ((credential.revoked_at IS NOT NULL AND credential.revoked_at <= $1)
       OR credential.expires_at <= $1)
  AND NOT EXISTS (
      SELECT 1 FROM agents
      WHERE agents.active_credential_serial = credential.serial
  )`, now.Add(-policy.Credentials))
	if err != nil {
		return result, fmt.Errorf("sweep superseded Agent credentials: %w", err)
	}
	result.Credentials = credentials.RowsAffected()

	return result, nil
}
