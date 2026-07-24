package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const initialAdminLockID int64 = 0x5a4b4541555448

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
