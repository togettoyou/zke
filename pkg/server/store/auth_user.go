package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/togettoyou/zke/pkg/server/auditaction"
)

const globalAdministratorSetupLockID int64 = 0x5a4b4541555448

func (store *AuthStore) HasGlobalAdministrator(ctx context.Context) (bool, error) {
	var exists bool
	if err := store.pool.QueryRow(
		ctx,
		`SELECT EXISTS (
    SELECT 1
    FROM role_bindings
    WHERE role = 'admin' AND scope_type = 'global'
)`,
	).Scan(&exists); err != nil {
		return false, errors.New("check global administrator")
	}
	return exists, nil
}

func (store *AuthStore) CreateFirstGlobalAdministrator(ctx context.Context, input FirstGlobalAdministrator) (User, error) {
	if strings.TrimSpace(input.UsernameNormalized) == "" ||
		strings.TrimSpace(input.DisplayName) == "" ||
		input.PasswordHash == "" ||
		strings.TrimSpace(input.RequestID) == "" {
		return User{}, errors.New("global administrator setup fields are required")
	}

	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return User{}, errors.New("begin global administrator setup transaction")
	}
	defer rollbackTransaction(transaction)

	if _, err := transaction.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", globalAdministratorSetupLockID); err != nil {
		return User{}, errors.New("lock global administrator setup")
	}

	var administratorExists bool
	if err := transaction.QueryRow(ctx, `SELECT EXISTS (
    SELECT 1
    FROM role_bindings
    WHERE role = 'admin' AND scope_type = 'global'
)`).Scan(&administratorExists); err != nil {
		return User{}, errors.New("check global administrator")
	}
	if administratorExists {
		return User{}, ErrGlobalAdministratorExists
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
ON CONFLICT (username_normalized) DO NOTHING
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
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrGlobalAdministratorUsernameUnavailable
	}
	if err != nil {
		return User{}, fmt.Errorf("insert global administrator: %w", err)
	}

	if _, err := transaction.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES (gen_random_uuid(), $1, 'admin', 'global')
`, user.ID); err != nil {
		return User{}, fmt.Errorf("grant global administrator role: %w", err)
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
    $2,
    $3,
    $1,
    'succeeded',
    $4
)
`, user.ID, auditaction.AuthAdministratorSetup, auditaction.TargetUser,
		input.RequestID); err != nil {
		return User{}, fmt.Errorf("audit global administrator setup: %w", err)
	}

	if err := transaction.Commit(ctx); err != nil {
		return User{}, errors.New("commit global administrator setup transaction")
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
    gen_random_uuid(), 'user', $1, 'global', $2,
    $3, $1, 'succeeded', $4, $5
)
`, input.UserID, auditaction.AuthPasswordChange, auditaction.TargetUser,
		input.RequestID, input.Now); err != nil {
		return fmt.Errorf("audit password change: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit password change: %w", err)
	}
	return nil
}
