package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/togettoyou/zke/pkg/server/auditaction"
)

func (store *AuthStore) RecordLoginAudit(
	ctx context.Context,
	targetUserID *string,
	result string,
	requestID string,
) error {
	if result != "failed" && result != "denied" {
		return errors.New("login audit result must be failed or denied")
	}
	if strings.TrimSpace(requestID) == "" {
		return errors.New("login audit request ID is required")
	}

	var targetID any
	if targetUserID != nil {
		targetID = *targetUserID
	}
	if _, err := store.pool.Exec(ctx, `
INSERT INTO audit_events (
    id,
    actor_type,
    scope_type,
    action,
    target_type,
    target_id,
    result,
    request_id
)
VALUES (
    gen_random_uuid(),
    'system',
    'global',
    $1,
    $2,
    $3,
    $4,
    $5
)
`, auditaction.AuthLogin, auditaction.TargetUser, targetID, result,
		requestID); err != nil {
		return fmt.Errorf("record login audit: %w", err)
	}
	return nil
}

func (store *AuthStore) RecordPasswordChangeAudit(
	ctx context.Context,
	userID string,
	result string,
	requestID string,
	now time.Time,
) error {
	if strings.TrimSpace(userID) == "" ||
		(result != "failed" && result != "denied") ||
		strings.TrimSpace(requestID) == "" ||
		now.IsZero() {
		return errors.New("password change audit fields are invalid")
	}
	if _, err := store.pool.Exec(ctx, `
INSERT INTO audit_events (
    id, actor_type, actor_user_id, scope_type, action, target_type,
    target_id, result, request_id, created_at
)
VALUES (
    gen_random_uuid(), 'user', $1, 'global', $2,
    $3, $1, $4, $5, $6
)
`, userID, auditaction.AuthPasswordChange, auditaction.TargetUser, result,
		requestID, now); err != nil {
		return fmt.Errorf("record password change audit: %w", err)
	}
	return nil
}

func (store *AuthStore) RecordLoginFailure(
	ctx context.Context,
	input RecordLoginFailureParams,
) error {
	if strings.TrimSpace(input.RequestID) == "" ||
		input.Now.IsZero() ||
		input.MaxFailures <= 0 ||
		input.LockDuration <= 0 {
		return errors.New("login failure fields are invalid")
	}
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin login failure transaction: %w", err)
	}
	defer rollbackTransaction(transaction)

	var targetID any
	if input.UserID != nil {
		targetID = *input.UserID
		var status string
		var newlyLocked bool
		var lockWithheld bool
		// `lockable` is the last-global-administrator carve-out.
		//
		// Account lockout is a denial-of-service primitive pointed at a known
		// username: five wrong passwords take the account off the air for the
		// lock duration, revoke its live sessions, and refuse even the correct
		// password until it expires. For every other account that is the right
		// trade. For the only remaining global administrator it is not — there
		// is no second administrator to call `POST /users/{id}/unlock`, the
		// initial-admin bootstrap only runs against an empty users table, and
		// the deployment is left with no way back in short of the database.
		//
		// The repository already holds the matching invariant on the other side:
		// the last active global administrator cannot be deleted or unbound
		// (`ensureNotLastGlobalAdmin`). Locking one out costs exactly what
		// removing one costs, so it is refused on the same grounds and asks the
		// same question: who holds the builtin `admin` role at global scope.
		//
		// Evaluated as a CTE inside the same statement, so the decision and the
		// write cannot be separated by a concurrent binding change. No advisory
		// lock: this runs on every failed login, which is precisely when an
		// attack is in progress, and serialising the whole failure path behind
		// one lock would hand over a second denial of service.
		//
		// What is NOT withheld: the failure still counts, the audit event is
		// still written, and the per-account rate limit still admits at most
		// `max_attempts_per_account` tries per window. The account stays
		// reachable and stays throttled.
		err = transaction.QueryRow(ctx, `
WITH administrators AS (
    SELECT DISTINCT users.id
    FROM users
    JOIN role_bindings ON role_bindings.subject_id = users.id
    WHERE users.status = 'active'
      AND role_bindings.scope_type = 'global'
      AND role_bindings.role = $5
), lockable AS (
    SELECT (
        NOT EXISTS (SELECT 1 FROM administrators WHERE id = $1)
        OR (SELECT count(*) FROM administrators) > 1
    ) AS allowed
)
UPDATE users
SET
    failed_login_count = CASE
        WHEN status = 'locked' AND lock_expires_at > $2
            THEN failed_login_count
        WHEN status = 'disabled'
            THEN failed_login_count
        WHEN status = 'locked' AND lock_expires_at <= $2
            THEN 1
        ELSE failed_login_count + 1
    END,
    status = CASE
        WHEN status = 'disabled' THEN status
        WHEN status = 'locked' AND lock_expires_at > $2 THEN status
        WHEN status = 'locked' AND lock_expires_at <= $2 AND 1 < $3
            THEN 'active'
        WHEN failed_login_count + 1 >= $3 AND (SELECT allowed FROM lockable)
            THEN 'locked'
        ELSE 'active'
    END,
    locked_at = CASE
        WHEN status = 'disabled' THEN NULL
        WHEN status = 'locked' AND lock_expires_at > $2 THEN locked_at
        WHEN status = 'locked' AND lock_expires_at <= $2 AND 1 < $3
            THEN NULL
        WHEN failed_login_count + 1 >= $3 AND (SELECT allowed FROM lockable)
            THEN $2
        ELSE NULL
    END,
    lock_expires_at = CASE
        WHEN status = 'disabled' THEN NULL
        WHEN status = 'locked' AND lock_expires_at > $2 THEN lock_expires_at
        WHEN status = 'locked' AND lock_expires_at <= $2 AND 1 < $3
            THEN NULL
        WHEN failed_login_count + 1 >= $3 AND (SELECT allowed FROM lockable)
            THEN $2 + $4::interval
        ELSE NULL
    END,
    updated_at = GREATEST(updated_at, $2)
WHERE id = $1
RETURNING
    status,
    status = 'locked' AND locked_at = $2,
    failed_login_count = $3 AND NOT (SELECT allowed FROM lockable)
`,
			*input.UserID,
			input.Now,
			input.MaxFailures,
			input.LockDuration,
			globalAdminRoleName,
		).Scan(&status, &newlyLocked, &lockWithheld)
		if errors.Is(err, pgx.ErrNoRows) {
			targetID = nil
		} else if err != nil {
			return fmt.Errorf("record persistent login failure: %w", err)
		}
		if status == "locked" {
			if _, err := transaction.Exec(ctx, `
UPDATE user_sessions
SET revoked_at = COALESCE(revoked_at, $2)
WHERE user_id = $1
`, *input.UserID, input.Now); err != nil {
				return fmt.Errorf("revoke locked user sessions: %w", err)
			}
		}
		if newlyLocked {
			if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id, actor_type, scope_type, action, target_type, target_id,
    result, request_id, created_at
)
VALUES (
    gen_random_uuid(), 'system', 'global', $1,
    $2, $3, 'succeeded', $4, $5
)
`, auditaction.AuthAccountLock, auditaction.TargetUser, *input.UserID,
				input.RequestID, input.Now); err != nil {
				return fmt.Errorf("audit persistent account lock: %w", err)
			}
		}
		// A control deliberately not applied has to leave a trace, or the only
		// evidence that the last administrator is under sustained attack is a
		// counter on a screen nobody is looking at.
		//
		// Written once per episode, on the attempt that crosses the threshold:
		// the counter keeps climbing past it and only a successful login clears
		// it, so the condition cannot hold twice without the account recovering
		// in between. Auditing every attempt past the threshold would let the
		// attacker choose how fast the audit table grows.
		if lockWithheld {
			if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id, actor_type, scope_type, action, target_type, target_id,
    result, request_id, created_at
)
VALUES (
    gen_random_uuid(), 'system', 'global', $1,
    $2, $3, 'succeeded', $4, $5
)
`, auditaction.AuthAccountLockWithheld, auditaction.TargetUser, *input.UserID,
				input.RequestID, input.Now); err != nil {
				return fmt.Errorf("audit withheld account lock: %w", err)
			}
		}
	}
	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id, actor_type, scope_type, action, target_type, target_id,
    result, request_id, created_at
)
VALUES (
    gen_random_uuid(), 'system', 'global', $1, $2,
    $3, 'failed', $4, $5
)
`, auditaction.AuthLogin, auditaction.TargetUser, targetID, input.RequestID,
		input.Now); err != nil {
		return fmt.Errorf("audit login failure: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit login failure transaction: %w", err)
	}
	return nil
}
