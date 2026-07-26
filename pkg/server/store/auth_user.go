package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const initialAdminLockID int64 = 0x5a4b4541555448

func (store *AuthStore) HasUsers(ctx context.Context) (bool, error) {
	var exists bool
	if err := store.pool.QueryRow(
		ctx,
		"SELECT EXISTS (SELECT 1 FROM users)",
	).Scan(&exists); err != nil {
		return false, errors.New("check existing users")
	}
	return exists, nil
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
	defer rollbackTransaction(transaction)

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
    failed_login_count,
    locked_at,
    lock_expires_at,
    password_changed_at,
    created_at,
    updated_at
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
		&user.FailedLoginCount,
		&user.LockedAt,
		&user.LockExpiresAt,
		&user.PasswordChangedAt,
		&user.CreatedAt,
		&user.UpdatedAt,
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
    failed_login_count,
    locked_at,
    lock_expires_at,
    password_changed_at,
    created_at,
    updated_at
FROM users
WHERE username_normalized = $1
`, usernameNormalized).Scan(
		&user.ID,
		&user.UsernameNormalized,
		&user.DisplayName,
		&user.PasswordHash,
		&user.Status,
		&user.FailedLoginCount,
		&user.LockedAt,
		&user.LockExpiresAt,
		&user.PasswordChangedAt,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, errors.New("find user by normalized username")
	}
	return user, nil
}

func (store *AuthStore) FindUserByID(
	ctx context.Context,
	userID string,
) (User, error) {
	var user User
	err := store.pool.QueryRow(ctx, `
SELECT
    id::text,
    username_normalized,
    display_name,
    password_hash,
    status,
    failed_login_count,
    locked_at,
    lock_expires_at,
    password_changed_at,
    created_at,
    updated_at
FROM users
WHERE id = $1
`, userID).Scan(
		&user.ID,
		&user.UsernameNormalized,
		&user.DisplayName,
		&user.PasswordHash,
		&user.Status,
		&user.FailedLoginCount,
		&user.LockedAt,
		&user.LockExpiresAt,
		&user.PasswordChangedAt,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, errors.New("find user by ID")
	}
	return user, nil
}

func (store *AuthStore) ChangeOwnPassword(
	ctx context.Context,
	input ChangeOwnPasswordParams,
) error {
	if strings.TrimSpace(input.UserID) == "" ||
		strings.TrimSpace(input.SessionID) == "" ||
		strings.TrimSpace(input.ExpectedPasswordHash) == "" ||
		input.ExpectedPasswordChangedAt.IsZero() ||
		strings.TrimSpace(input.NewPasswordHash) == "" ||
		strings.TrimSpace(input.RequestID) == "" ||
		input.Now.IsZero() {
		return errors.New("password change fields are required")
	}
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin password change: %w", err)
	}
	defer rollbackTransaction(transaction)

	command, err := transaction.Exec(ctx, `
UPDATE users
SET
    password_hash = $4,
    password_changed_at = $5,
    failed_login_count = 0,
    locked_at = NULL,
    lock_expires_at = NULL,
    updated_at = GREATEST(updated_at, $5)
WHERE id = $1
  AND status = 'active'
  AND password_hash = $2
  AND password_changed_at = $3
  AND EXISTS (
      SELECT 1
      FROM user_sessions
      WHERE id = $6
        AND user_id = $1
        AND revoked_at IS NULL
        AND idle_expires_at > $5
        AND expires_at > $5
  )
`,
		input.UserID,
		input.ExpectedPasswordHash,
		input.ExpectedPasswordChangedAt,
		input.NewPasswordHash,
		input.Now,
		input.SessionID,
	)
	if err != nil {
		return fmt.Errorf("update own password: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrCredentialsChanged
	}
	if _, err := transaction.Exec(ctx, `
UPDATE user_sessions
SET revoked_at = COALESCE(revoked_at, $2)
WHERE user_id = $1
`, input.UserID, input.Now); err != nil {
		return fmt.Errorf("revoke sessions after password change: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id, actor_type, actor_user_id, scope_type, action, target_type,
    target_id, result, request_id, created_at
)
VALUES (
    gen_random_uuid(), 'user', $1, 'global', 'auth.password.change',
    'user', $1, 'succeeded', $2, $3
)
`, input.UserID, input.RequestID, input.Now); err != nil {
		return fmt.Errorf("audit password change: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit password change: %w", err)
	}
	return nil
}
