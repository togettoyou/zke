package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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
    'auth.login',
    'user',
    $1,
    $2,
    $3
)
`, targetID, result, requestID); err != nil {
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
    gen_random_uuid(), 'user', $1, 'global', 'auth.password.change',
    'user', $1, $2, $3, $4
)
`, userID, result, requestID, now); err != nil {
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
		err = transaction.QueryRow(ctx, `
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
        WHEN failed_login_count + 1 >= $3 THEN 'locked'
        ELSE 'active'
    END,
    locked_at = CASE
        WHEN status = 'disabled' THEN NULL
        WHEN status = 'locked' AND lock_expires_at > $2 THEN locked_at
        WHEN status = 'locked' AND lock_expires_at <= $2 AND 1 < $3
            THEN NULL
        WHEN failed_login_count + 1 >= $3 THEN $2
        ELSE NULL
    END,
    lock_expires_at = CASE
        WHEN status = 'disabled' THEN NULL
        WHEN status = 'locked' AND lock_expires_at > $2 THEN lock_expires_at
        WHEN status = 'locked' AND lock_expires_at <= $2 AND 1 < $3
            THEN NULL
        WHEN failed_login_count + 1 >= $3 THEN $2 + $4::interval
        ELSE NULL
    END,
    updated_at = GREATEST(updated_at, $2)
WHERE id = $1
RETURNING status, status = 'locked' AND locked_at = $2
`,
			*input.UserID,
			input.Now,
			input.MaxFailures,
			input.LockDuration,
		).Scan(&status, &newlyLocked)
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
    gen_random_uuid(), 'system', 'global', 'auth.account.lock',
    'user', $1, 'succeeded', $2, $3
)
`, *input.UserID, input.RequestID, input.Now); err != nil {
				return fmt.Errorf("audit persistent account lock: %w", err)
			}
		}
	}
	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id, actor_type, scope_type, action, target_type, target_id,
    result, request_id, created_at
)
VALUES (
    gen_random_uuid(), 'system', 'global', 'auth.login', 'user',
    $1, 'failed', $2, $3
)
`, targetID, input.RequestID, input.Now); err != nil {
		return fmt.Errorf("audit login failure: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit login failure transaction: %w", err)
	}
	return nil
}
