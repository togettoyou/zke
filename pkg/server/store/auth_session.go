package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const transactionCleanupTimeout = 5 * time.Second

func insertSession(
	ctx context.Context,
	queryer interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	input CreateSessionParams,
) (Session, error) {
	var session Session
	err := queryer.QueryRow(ctx, `
INSERT INTO user_sessions (
    id,
    user_id,
    token_digest,
    csrf_token_digest,
    idle_expires_at,
    expires_at
)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5)
RETURNING
    id::text,
    user_id::text,
    token_digest,
    csrf_token_digest,
    idle_expires_at,
    expires_at,
    last_seen_at,
    revoked_at,
    created_at
`,
		input.UserID,
		input.TokenDigest,
		input.CSRFTokenDigest,
		input.IdleExpiresAt,
		input.ExpiresAt,
	).Scan(
		&session.ID,
		&session.UserID,
		&session.TokenDigest,
		&session.CSRFTokenDigest,
		&session.IdleExpiresAt,
		&session.ExpiresAt,
		&session.LastSeenAt,
		&session.RevokedAt,
		&session.CreatedAt,
	)
	if err != nil {
		return Session{}, fmt.Errorf("create user session: %w", err)
	}
	return session, nil
}

func (store *AuthStore) CompleteLogin(
	ctx context.Context,
	input CompleteLoginParams,
) (Session, error) {
	if strings.TrimSpace(input.UserID) == "" ||
		strings.TrimSpace(input.ExpectedPasswordHash) == "" ||
		input.ExpectedPasswordChangedAt.IsZero() ||
		input.Session.UserID != input.UserID {
		return Session{}, errors.New("login credential version is required")
	}
	if err := validateCreateSession(input.Session); err != nil {
		return Session{}, err
	}
	if strings.TrimSpace(input.RequestID) == "" {
		return Session{}, errors.New("session audit request ID is required")
	}

	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Session{}, fmt.Errorf("begin authenticated session transaction: %w", err)
	}
	defer rollbackTransaction(transaction)

	var lockedUserID string
	err = transaction.QueryRow(ctx, `
SELECT id::text
FROM users
WHERE id = $1
  AND status = 'active'
  AND password_hash = $2
  AND password_changed_at = $3
FOR UPDATE
`,
		input.UserID,
		input.ExpectedPasswordHash,
		input.ExpectedPasswordChangedAt,
	).Scan(&lockedUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrCredentialsChanged
	}
	if err != nil {
		return Session{}, errors.New("lock login credential version")
	}

	if input.ReplacementPasswordHash != "" {
		if _, err := transaction.Exec(ctx, `
UPDATE users
SET
    password_hash = $2,
    updated_at = now()
WHERE id = $1
`, input.UserID, input.ReplacementPasswordHash); err != nil {
			return Session{}, errors.New("upgrade password hash parameters")
		}
	}

	session, err := insertSession(ctx, transaction, input.Session)
	if err != nil {
		return Session{}, err
	}
	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id,
    actor_type,
    actor_user_id,
    scope_type,
    action,
    target_type,
    target_id,
    result,
    request_id
)
VALUES (
    gen_random_uuid(),
    'user',
    $1,
    'global',
    'auth.login',
    'session',
    $2,
    'succeeded',
    $3
)
`, input.UserID, session.ID, input.RequestID); err != nil {
		return Session{}, fmt.Errorf("audit authenticated session creation: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit authenticated session transaction: %w", err)
	}
	return session, nil
}

func validateCreateSession(input CreateSessionParams) error {
	if strings.TrimSpace(input.UserID) == "" {
		return errors.New("session user and token digests are required")
	}
	if len(input.TokenDigest) != sha256.Size {
		return errors.New("session token digest must use SHA-256")
	}
	if len(input.CSRFTokenDigest) != sha256.Size {
		return errors.New("CSRF token digest must use SHA-256")
	}
	if input.IdleExpiresAt.IsZero() || input.ExpiresAt.IsZero() ||
		input.IdleExpiresAt.After(input.ExpiresAt) {
		return errors.New("session expiry is invalid")
	}
	return nil
}

func (store *AuthStore) FindActiveSession(
	ctx context.Context,
	tokenDigest []byte,
	now time.Time,
	idleTimeout time.Duration,
) (AuthenticatedSession, error) {
	if len(tokenDigest) != sha256.Size {
		return AuthenticatedSession{}, ErrSessionNotFound
	}
	if now.IsZero() || idleTimeout <= 0 {
		return AuthenticatedSession{}, errors.New("session activity time and idle timeout are required")
	}
	requestedIdleExpiry := now.Add(idleTimeout)

	var identity AuthenticatedSession
	err := store.pool.QueryRow(ctx, `
UPDATE user_sessions AS session
SET
    last_seen_at = GREATEST(session.last_seen_at, $2),
    idle_expires_at = LEAST(
        session.expires_at,
        GREATEST(session.idle_expires_at, $3)
    )
FROM users
WHERE session.token_digest = $1
  AND users.id = session.user_id
  AND session.revoked_at IS NULL
  AND session.idle_expires_at > $2
  AND session.expires_at > $2
  AND session.created_at >= users.password_changed_at
  AND users.status = 'active'
RETURNING
    session.id::text,
    session.user_id::text,
    session.token_digest,
    session.csrf_token_digest,
    session.idle_expires_at,
    session.expires_at,
    session.last_seen_at,
    session.revoked_at,
    session.created_at,
    users.id::text,
    users.username_normalized,
    users.display_name,
    users.status,
    users.password_changed_at
`, tokenDigest, now, requestedIdleExpiry).Scan(
		&identity.Session.ID,
		&identity.Session.UserID,
		&identity.Session.TokenDigest,
		&identity.Session.CSRFTokenDigest,
		&identity.Session.IdleExpiresAt,
		&identity.Session.ExpiresAt,
		&identity.Session.LastSeenAt,
		&identity.Session.RevokedAt,
		&identity.Session.CreatedAt,
		&identity.User.ID,
		&identity.User.UsernameNormalized,
		&identity.User.DisplayName,
		&identity.User.Status,
		&identity.User.PasswordChangedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthenticatedSession{}, ErrSessionNotFound
	}
	if err != nil {
		return AuthenticatedSession{}, errors.New("find active user session")
	}
	return identity, nil
}

func (store *AuthStore) RevokeAuthenticatedSession(
	ctx context.Context,
	sessionID string,
	userID string,
	revokedAt time.Time,
	requestID string,
) error {
	if strings.TrimSpace(sessionID) == "" ||
		strings.TrimSpace(userID) == "" ||
		revokedAt.IsZero() ||
		strings.TrimSpace(requestID) == "" {
		return errors.New("authenticated session revocation fields are required")
	}

	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin authenticated session revocation transaction: %w", err)
	}
	defer rollbackTransaction(transaction)

	command, err := transaction.Exec(ctx, `
UPDATE user_sessions
SET revoked_at = $3
WHERE id = $1
  AND user_id = $2
  AND revoked_at IS NULL
`, sessionID, userID, revokedAt)
	if err != nil {
		return errors.New("revoke authenticated session")
	}
	if command.RowsAffected() != 1 {
		return ErrSessionNotFound
	}
	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id,
    actor_type,
    actor_user_id,
    scope_type,
    action,
    target_type,
    target_id,
    result,
    request_id
)
VALUES (
    gen_random_uuid(),
    'user',
    $1,
    'global',
    'auth.logout',
    'session',
    $2,
    'succeeded',
    $3
)
`, userID, sessionID, requestID); err != nil {
		return fmt.Errorf("audit authenticated session revocation: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit authenticated session revocation transaction: %w", err)
	}
	return nil
}

func rollbackTransaction(transaction pgx.Tx) {
	rollbackContext, cancelRollback := context.WithTimeout(
		context.Background(),
		transactionCleanupTimeout,
	)
	defer cancelRollback()
	_ = transaction.Rollback(rollbackContext)
}
