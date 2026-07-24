package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const initialAdminLockID int64 = 0x5a4b4541555448
const transactionCleanupTimeout = 5 * time.Second

var (
	ErrInitialAdminExists = errors.New("initial administrator already exists")
	ErrUserNotFound       = errors.New("user not found")
	ErrSessionNotFound    = errors.New("session not found")
)

type AuthStore struct {
	pool *pgxpool.Pool
}

type User struct {
	ID                 string
	UsernameNormalized string
	DisplayName        string
	PasswordHash       string
	Status             string
	PasswordChangedAt  time.Time
}

type InitialAdmin struct {
	UsernameNormalized string
	DisplayName        string
	PasswordHash       string
	RequestID          string
}

type Session struct {
	ID            string
	UserID        string
	TokenDigest   []byte
	IdleExpiresAt time.Time
	ExpiresAt     time.Time
	LastSeenAt    time.Time
	RevokedAt     *time.Time
	CreatedAt     time.Time
}

type CreateSessionParams struct {
	UserID        string
	TokenDigest   []byte
	IdleExpiresAt time.Time
	ExpiresAt     time.Time
}

type AuthenticatedSession struct {
	Session Session
	User    SessionUser
}

type SessionUser struct {
	ID                 string
	UsernameNormalized string
	DisplayName        string
	Status             string
	PasswordChangedAt  time.Time
}

func NewAuthStore(pool *pgxpool.Pool) *AuthStore {
	return &AuthStore{pool: pool}
}

func (store *AuthStore) CreateInitialAdmin(ctx context.Context, input InitialAdmin) (User, error) {
	if strings.TrimSpace(input.UsernameNormalized) == "" ||
		strings.TrimSpace(input.DisplayName) == "" ||
		input.PasswordHash == "" ||
		strings.TrimSpace(input.RequestID) == "" {
		return User{}, errors.New("initial administrator fields are required")
	}

	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return User{}, errors.New("begin initial administrator transaction")
	}
	defer func() {
		rollbackContext, cancelRollback := context.WithTimeout(
			context.Background(),
			transactionCleanupTimeout,
		)
		defer cancelRollback()
		_ = transaction.Rollback(rollbackContext)
	}()

	if _, err := transaction.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", initialAdminLockID); err != nil {
		return User{}, errors.New("lock initial administrator creation")
	}

	var userExists bool
	if err := transaction.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users)").Scan(&userExists); err != nil {
		return User{}, errors.New("check existing users")
	}
	if userExists {
		return User{}, ErrInitialAdminExists
	}

	var user User
	err = transaction.QueryRow(ctx, `
INSERT INTO users (
    id,
    username_normalized,
    display_name,
    password_hash,
    status,
    password_changed_at
)
VALUES (gen_random_uuid(), $1, $2, $3, 'active', now())
RETURNING
    id::text,
    username_normalized,
    display_name,
    password_hash,
    status,
    password_changed_at
`,
		input.UsernameNormalized,
		input.DisplayName,
		input.PasswordHash,
	).Scan(
		&user.ID,
		&user.UsernameNormalized,
		&user.DisplayName,
		&user.PasswordHash,
		&user.Status,
		&user.PasswordChangedAt,
	)
	if err != nil {
		return User{}, fmt.Errorf("insert initial administrator: %w", err)
	}

	if _, err := transaction.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES (gen_random_uuid(), $1, 'admin', 'global')
`, user.ID); err != nil {
		return User{}, fmt.Errorf("grant initial administrator role: %w", err)
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
    'auth.initial_admin.create',
    'user',
    $1,
    'succeeded',
    $2
)
`, user.ID, input.RequestID); err != nil {
		return User{}, fmt.Errorf("audit initial administrator creation: %w", err)
	}

	if err := transaction.Commit(ctx); err != nil {
		return User{}, errors.New("commit initial administrator transaction")
	}
	return user, nil
}

func (store *AuthStore) FindUserByUsername(
	ctx context.Context,
	usernameNormalized string,
) (User, error) {
	var user User
	err := store.pool.QueryRow(ctx, `
SELECT
    id::text,
    username_normalized,
    display_name,
    password_hash,
    status,
    password_changed_at
FROM users
WHERE username_normalized = $1
`, usernameNormalized).Scan(
		&user.ID,
		&user.UsernameNormalized,
		&user.DisplayName,
		&user.PasswordHash,
		&user.Status,
		&user.PasswordChangedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, errors.New("find user by normalized username")
	}
	return user, nil
}

func (store *AuthStore) CreateSession(ctx context.Context, input CreateSessionParams) (Session, error) {
	if strings.TrimSpace(input.UserID) == "" {
		return Session{}, errors.New("session user and token digest are required")
	}
	if len(input.TokenDigest) != sha256.Size {
		return Session{}, errors.New("session token digest must use SHA-256")
	}
	if input.IdleExpiresAt.IsZero() || input.ExpiresAt.IsZero() ||
		input.IdleExpiresAt.After(input.ExpiresAt) {
		return Session{}, errors.New("session expiry is invalid")
	}

	var session Session
	err := store.pool.QueryRow(ctx, `
INSERT INTO user_sessions (
    id,
    user_id,
    token_digest,
    idle_expires_at,
    expires_at
)
VALUES (gen_random_uuid(), $1, $2, $3, $4)
RETURNING
    id::text,
    user_id::text,
    token_digest,
    idle_expires_at,
    expires_at,
    last_seen_at,
    revoked_at,
    created_at
`,
		input.UserID,
		input.TokenDigest,
		input.IdleExpiresAt,
		input.ExpiresAt,
	).Scan(
		&session.ID,
		&session.UserID,
		&session.TokenDigest,
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

func (store *AuthStore) RevokeSession(
	ctx context.Context,
	sessionID string,
	revokedAt time.Time,
) (bool, error) {
	command, err := store.pool.Exec(ctx, `
UPDATE user_sessions
SET revoked_at = $2
WHERE id = $1
  AND revoked_at IS NULL
`, sessionID, revokedAt)
	if err != nil {
		return false, errors.New("revoke user session")
	}
	return command.RowsAffected() == 1, nil
}
