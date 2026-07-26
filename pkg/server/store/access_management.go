package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (store *AccessManagementStore) ListUsers(
	ctx context.Context,
) ([]ManagedUser, error) {
	rows, err := store.pool.Query(ctx, `
SELECT
    id::text, username_normalized, display_name, status,
    failed_login_count, locked_at, lock_expires_at,
    password_changed_at, created_at, updated_at
FROM users
ORDER BY username_normalized, id
`)
	if err != nil {
		return nil, fmt.Errorf("list managed users: %w", err)
	}
	defer rows.Close()
	var result []ManagedUser
	for rows.Next() {
		item, err := scanManagedUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan managed user: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed users: %w", err)
	}
	return result, nil
}

func (store *AccessManagementStore) GetUser(
	ctx context.Context,
	userID string,
) (ManagedUser, error) {
	item, err := scanManagedUser(store.pool.QueryRow(ctx, `
SELECT
    id::text, username_normalized, display_name, status,
    failed_login_count, locked_at, lock_expires_at,
    password_changed_at, created_at, updated_at
FROM users
WHERE id = $1
`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedUser{}, ErrAccessUserNotFound
	}
	if err != nil {
		return ManagedUser{}, fmt.Errorf("get managed user: %w", err)
	}
	return item, nil
}

func (store *AccessManagementStore) UpdateUser(
	ctx context.Context,
	input UpdateManagedUserParams,
) (ManagedUser, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ManagedUser{}, fmt.Errorf("begin managed user update: %w", err)
	}
	defer rollbackTransaction(transaction)
	item, err := scanManagedUser(transaction.QueryRow(ctx, `
UPDATE users
SET display_name = $2, updated_at = GREATEST(updated_at, $4)
WHERE id = $1
  AND EXISTS (SELECT 1 FROM users WHERE id = $3 AND status = 'active')
RETURNING
    id::text, username_normalized, display_name, status,
    failed_login_count, locked_at, lock_expires_at,
    password_changed_at, created_at, updated_at
`, input.UserID, input.DisplayName, input.ActorUserID, input.Now))
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedUser{}, ErrAccessUserNotFound
	}
	if err != nil {
		return ManagedUser{}, fmt.Errorf("update managed user: %w", err)
	}
	if err := insertGlobalAccessAudit(
		ctx, transaction, input.ActorUserID, "user.update", "user",
		input.UserID, "succeeded", input.RequestID, input.Now,
	); err != nil {
		return ManagedUser{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ManagedUser{}, fmt.Errorf("commit managed user update: %w", err)
	}
	return item, nil
}

func (store *AccessManagementStore) DeleteUser(
	ctx context.Context,
	input DeleteManagedUserParams,
) (ManagedUser, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ManagedUser{}, fmt.Errorf("begin managed user deletion: %w", err)
	}
	defer rollbackTransaction(transaction)
	if err := ensureNotLastGlobalAdmin(ctx, transaction, input.UserID); err != nil {
		return ManagedUser{}, err
	}
	item, err := scanManagedUser(transaction.QueryRow(ctx, `
UPDATE users
SET status = 'disabled', locked_at = NULL, lock_expires_at = NULL,
    updated_at = GREATEST(updated_at, $3)
WHERE id = $1
  AND EXISTS (SELECT 1 FROM users WHERE id = $2 AND status = 'active')
RETURNING
    id::text, username_normalized, display_name, status,
    failed_login_count, locked_at, lock_expires_at,
    password_changed_at, created_at, updated_at
`, input.UserID, input.ActorUserID, input.Now))
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedUser{}, ErrAccessUserNotFound
	}
	if err != nil {
		return ManagedUser{}, fmt.Errorf("disable deleted user: %w", err)
	}
	if err := revokeUserSessions(ctx, transaction, input.UserID, input.Now); err != nil {
		return ManagedUser{}, err
	}
	if err := insertGlobalAccessAudit(
		ctx, transaction, input.ActorUserID, "user.delete", "user",
		input.UserID, "succeeded", input.RequestID, input.Now,
	); err != nil {
		return ManagedUser{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ManagedUser{}, fmt.Errorf("commit managed user deletion: %w", err)
	}
	return item, nil
}

func (store *AccessManagementStore) CreateUser(
	ctx context.Context,
	input CreateManagedUserParams,
) (ManagedUser, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ManagedUser{}, fmt.Errorf("begin managed user creation: %w", err)
	}
	defer rollbackTransaction(transaction)
	item, err := scanManagedUser(transaction.QueryRow(ctx, `
INSERT INTO users (
    id, username_normalized, display_name, password_hash, status,
    failed_login_count, password_changed_at, created_at, updated_at
)
SELECT
    $1, $2, $3, $4, 'active', 0, $6, $6, $6
FROM users
WHERE id = $5 AND status = 'active'
RETURNING
    id::text, username_normalized, display_name, status,
    failed_login_count, locked_at, lock_expires_at,
    password_changed_at, created_at, updated_at
`,
		input.ID,
		input.Username,
		input.DisplayName,
		input.PasswordHash,
		input.ActorUserID,
		input.Now,
	))
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "23505" {
			return ManagedUser{}, ErrAccessUserConflict
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ManagedUser{}, ErrAccessStateConflict
		}
		return ManagedUser{}, fmt.Errorf("insert managed user: %w", err)
	}
	if err := insertGlobalAccessAudit(
		ctx, transaction, input.ActorUserID, "user.create", "user",
		item.ID, "succeeded", input.RequestID, input.Now,
	); err != nil {
		return ManagedUser{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ManagedUser{}, fmt.Errorf("commit managed user creation: %w", err)
	}
	return item, nil
}

func (store *AccessManagementStore) SetUserStatus(
	ctx context.Context,
	input SetManagedUserStatusParams,
) (ManagedUser, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ManagedUser{}, fmt.Errorf("begin managed user status update: %w", err)
	}
	defer rollbackTransaction(transaction)
	var currentStatus string
	if err := transaction.QueryRow(
		ctx,
		"SELECT status FROM users WHERE id = $1 FOR UPDATE",
		input.UserID,
	).Scan(&currentStatus); errors.Is(err, pgx.ErrNoRows) {
		return ManagedUser{}, ErrAccessUserNotFound
	} else if err != nil {
		return ManagedUser{}, fmt.Errorf("lock managed user status: %w", err)
	}
	if currentStatus == "locked" && input.Status == "active" {
		return ManagedUser{}, ErrAccessStateConflict
	}
	if currentStatus == "active" && input.Status == "disabled" {
		if err := ensureNotLastGlobalAdmin(ctx, transaction, input.UserID); err != nil {
			return ManagedUser{}, err
		}
	}
	item, err := scanManagedUser(transaction.QueryRow(ctx, `
UPDATE users
SET
    status = $2,
    failed_login_count = CASE WHEN $2 = 'active' THEN 0 ELSE failed_login_count END,
    locked_at = NULL,
    lock_expires_at = NULL,
    updated_at = GREATEST(updated_at, $3)
WHERE id = $1
RETURNING
    id::text, username_normalized, display_name, status,
    failed_login_count, locked_at, lock_expires_at,
    password_changed_at, created_at, updated_at
`, input.UserID, input.Status, input.Now))
	if err != nil {
		return ManagedUser{}, fmt.Errorf("update managed user status: %w", err)
	}
	if input.Status == "disabled" {
		if err := revokeUserSessions(ctx, transaction, input.UserID, input.Now); err != nil {
			return ManagedUser{}, err
		}
	}
	if err := insertGlobalAccessAudit(
		ctx, transaction, input.ActorUserID, "user.status.update", "user",
		input.UserID, "succeeded", input.RequestID, input.Now,
	); err != nil {
		return ManagedUser{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ManagedUser{}, fmt.Errorf("commit managed user status update: %w", err)
	}
	return item, nil
}

func (store *AccessManagementStore) UnlockUser(
	ctx context.Context,
	input UnlockManagedUserParams,
) (ManagedUser, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ManagedUser{}, fmt.Errorf("begin managed user unlock: %w", err)
	}
	defer rollbackTransaction(transaction)
	var status string
	if err := transaction.QueryRow(
		ctx,
		"SELECT status FROM users WHERE id = $1 FOR UPDATE",
		input.UserID,
	).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return ManagedUser{}, ErrAccessUserNotFound
	} else if err != nil {
		return ManagedUser{}, fmt.Errorf("lock managed user for unlock: %w", err)
	}
	if status != "locked" {
		return ManagedUser{}, ErrAccessStateConflict
	}
	item, err := scanManagedUser(transaction.QueryRow(ctx, `
UPDATE users
SET
    status = 'active',
    failed_login_count = 0,
    locked_at = NULL,
    lock_expires_at = NULL,
    updated_at = GREATEST(updated_at, $2)
WHERE id = $1
RETURNING
    id::text, username_normalized, display_name, status,
    failed_login_count, locked_at, lock_expires_at,
    password_changed_at, created_at, updated_at
`, input.UserID, input.Now))
	if err != nil {
		return ManagedUser{}, fmt.Errorf("unlock managed user: %w", err)
	}
	if err := insertGlobalAccessAudit(
		ctx, transaction, input.ActorUserID, "user.unlock", "user",
		input.UserID, "succeeded", input.RequestID, input.Now,
	); err != nil {
		return ManagedUser{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ManagedUser{}, fmt.Errorf("commit managed user unlock: %w", err)
	}
	return item, nil
}

func (store *AccessManagementStore) ResetUserPassword(
	ctx context.Context,
	input ResetManagedUserPasswordParams,
) (ManagedUser, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ManagedUser{}, fmt.Errorf("begin managed password reset: %w", err)
	}
	defer rollbackTransaction(transaction)
	var status string
	if err := transaction.QueryRow(
		ctx,
		"SELECT status FROM users WHERE id = $1 FOR UPDATE",
		input.UserID,
	).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return ManagedUser{}, ErrAccessUserNotFound
	} else if err != nil {
		return ManagedUser{}, fmt.Errorf("lock managed user for password reset: %w", err)
	}
	if status == "disabled" {
		return ManagedUser{}, ErrAccessStateConflict
	}
	item, err := scanManagedUser(transaction.QueryRow(ctx, `
UPDATE users
SET
    password_hash = $2,
    password_changed_at = $3,
    status = 'active',
    failed_login_count = 0,
    locked_at = NULL,
    lock_expires_at = NULL,
    updated_at = GREATEST(updated_at, $3)
WHERE id = $1
RETURNING
    id::text, username_normalized, display_name, status,
    failed_login_count, locked_at, lock_expires_at,
    password_changed_at, created_at, updated_at
`, input.UserID, input.PasswordHash, input.Now))
	if err != nil {
		return ManagedUser{}, fmt.Errorf("reset managed user password: %w", err)
	}
	if err := revokeUserSessions(ctx, transaction, input.UserID, input.Now); err != nil {
		return ManagedUser{}, err
	}
	if err := insertGlobalAccessAudit(
		ctx, transaction, input.ActorUserID, "user.password.reset", "user",
		input.UserID, "succeeded", input.RequestID, input.Now,
	); err != nil {
		return ManagedUser{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ManagedUser{}, fmt.Errorf("commit managed password reset: %w", err)
	}
	return item, nil
}

func (store *AccessManagementStore) ListRoleBindings(
	ctx context.Context,
) ([]ManagedRoleBinding, error) {
	rows, err := store.pool.Query(ctx, `
SELECT
    id::text, subject_id::text, role, scope_type,
    COALESCE(tenant_id::text, ''), COALESCE(project_id::text, ''),
    created_at
FROM role_bindings
ORDER BY created_at, id
`)
	if err != nil {
		return nil, fmt.Errorf("list managed role bindings: %w", err)
	}
	defer rows.Close()
	var result []ManagedRoleBinding
	for rows.Next() {
		item, err := scanManagedRoleBinding(rows)
		if err != nil {
			return nil, fmt.Errorf("scan managed role binding: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed role bindings: %w", err)
	}
	return result, nil
}

func (store *AccessManagementStore) GetRoleBinding(
	ctx context.Context,
	bindingID string,
) (ManagedRoleBinding, error) {
	item, err := scanManagedRoleBinding(store.pool.QueryRow(ctx, `
SELECT
    id::text, subject_id::text, role, scope_type,
    COALESCE(tenant_id::text, ''), COALESCE(project_id::text, ''),
    created_at
FROM role_bindings
WHERE id = $1
`, bindingID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedRoleBinding{}, ErrRoleBindingNotFound
	}
	if err != nil {
		return ManagedRoleBinding{}, fmt.Errorf("get managed role binding: %w", err)
	}
	return item, nil
}

func (store *AccessManagementStore) CreateRoleBinding(
	ctx context.Context,
	input CreateManagedRoleBindingParams,
) (ManagedRoleBinding, bool, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ManagedRoleBinding{}, false, fmt.Errorf("begin role binding creation: %w", err)
	}
	defer rollbackTransaction(transaction)
	item, err := scanManagedRoleBinding(transaction.QueryRow(ctx, `
INSERT INTO role_bindings (
    id, subject_id, role, scope_type, tenant_id, project_id, created_at
)
SELECT
    $1, users.id, $3, $4, NULLIF($5, '')::uuid,
    NULLIF($6, '')::uuid, $7
FROM users
WHERE users.id = $2
ON CONFLICT (subject_id, role, scope_type, tenant_id, project_id)
    DO NOTHING
RETURNING
    id::text, subject_id::text, role, scope_type,
    COALESCE(tenant_id::text, ''), COALESCE(project_id::text, ''),
    created_at
`,
		input.ID,
		input.SubjectID,
		input.Role,
		input.ScopeType,
		input.TenantID,
		input.ProjectID,
		input.Now,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if queryErr := transaction.QueryRow(
			ctx,
			"SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)",
			input.SubjectID,
		).Scan(&exists); queryErr != nil {
			return ManagedRoleBinding{}, false, fmt.Errorf("check role binding subject: %w", queryErr)
		}
		if !exists {
			return ManagedRoleBinding{}, false, ErrAccessUserNotFound
		}
		item, err = scanManagedRoleBinding(transaction.QueryRow(ctx, `
SELECT
    id::text, subject_id::text, role, scope_type,
    COALESCE(tenant_id::text, ''), COALESCE(project_id::text, ''),
    created_at
FROM role_bindings
WHERE subject_id = $1
  AND role = $2
  AND scope_type = $3
  AND tenant_id IS NOT DISTINCT FROM NULLIF($4, '')::uuid
  AND project_id IS NOT DISTINCT FROM NULLIF($5, '')::uuid
`, input.SubjectID, input.Role, input.ScopeType, input.TenantID, input.ProjectID))
		if err != nil {
			return ManagedRoleBinding{}, false, ErrRoleBindingConflict
		}
		return item, true, nil
	}
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) &&
			(databaseError.Code == "23503" || databaseError.Code == "23514") {
			return ManagedRoleBinding{}, false, ErrAccessStateConflict
		}
		return ManagedRoleBinding{}, false, fmt.Errorf("insert role binding: %w", err)
	}
	if err := insertRoleBindingAudit(
		ctx, transaction, input.ActorUserID, "role_binding.create",
		item, "succeeded", input.RequestID, input.Now,
	); err != nil {
		return ManagedRoleBinding{}, false, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ManagedRoleBinding{}, false, fmt.Errorf("commit role binding creation: %w", err)
	}
	return item, false, nil
}

func (store *AccessManagementStore) DeleteRoleBinding(
	ctx context.Context,
	input DeleteManagedRoleBindingParams,
) (ManagedRoleBinding, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ManagedRoleBinding{}, fmt.Errorf("begin role binding deletion: %w", err)
	}
	defer rollbackTransaction(transaction)
	item, err := scanManagedRoleBinding(transaction.QueryRow(ctx, `
SELECT
    id::text, subject_id::text, role, scope_type,
    COALESCE(tenant_id::text, ''), COALESCE(project_id::text, ''),
    created_at
FROM role_bindings
WHERE id = $1
FOR UPDATE
`, input.BindingID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedRoleBinding{}, ErrRoleBindingNotFound
	}
	if err != nil {
		return ManagedRoleBinding{}, fmt.Errorf("lock role binding deletion: %w", err)
	}
	if item.Role == "admin" && item.ScopeType == "global" {
		if err := ensureNotLastGlobalAdmin(ctx, transaction, item.SubjectID); err != nil {
			return ManagedRoleBinding{}, err
		}
	}
	if _, err := transaction.Exec(
		ctx,
		"DELETE FROM role_bindings WHERE id = $1",
		input.BindingID,
	); err != nil {
		return ManagedRoleBinding{}, fmt.Errorf("delete role binding: %w", err)
	}
	if err := insertRoleBindingAudit(
		ctx, transaction, input.ActorUserID, "role_binding.delete",
		item, "succeeded", input.RequestID, input.Now,
	); err != nil {
		return ManagedRoleBinding{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ManagedRoleBinding{}, fmt.Errorf("commit role binding deletion: %w", err)
	}
	return item, nil
}

type rowScannerAccess interface {
	Scan(...any) error
}

func scanManagedUser(row rowScannerAccess) (ManagedUser, error) {
	var item ManagedUser
	err := row.Scan(
		&item.ID,
		&item.Username,
		&item.DisplayName,
		&item.Status,
		&item.FailedLoginCount,
		&item.LockedAt,
		&item.LockExpiresAt,
		&item.PasswordChangedAt,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	return item, err
}

func scanManagedRoleBinding(row rowScannerAccess) (ManagedRoleBinding, error) {
	var item ManagedRoleBinding
	err := row.Scan(
		&item.ID,
		&item.SubjectID,
		&item.Role,
		&item.ScopeType,
		&item.TenantID,
		&item.ProjectID,
		&item.CreatedAt,
	)
	return item, err
}

func ensureNotLastGlobalAdmin(
	ctx context.Context,
	transaction pgx.Tx,
	userID string,
) error {
	if _, err := transaction.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1)",
		int64(0x5a4b4541444d494e),
	); err != nil {
		return fmt.Errorf("lock global administrator invariant: %w", err)
	}
	var isGlobalAdmin bool
	if err := transaction.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM role_bindings
    JOIN users ON users.id = role_bindings.subject_id
    WHERE role_bindings.subject_id = $1
      AND role_bindings.role = 'admin'
      AND role_bindings.scope_type = 'global'
      AND users.status = 'active'
)
`, userID).Scan(&isGlobalAdmin); err != nil {
		return fmt.Errorf("check global administrator binding: %w", err)
	}
	if !isGlobalAdmin {
		return nil
	}
	var activeGlobalAdministrators int
	if err := transaction.QueryRow(ctx, `
SELECT count(DISTINCT users.id)
FROM users
JOIN role_bindings ON role_bindings.subject_id = users.id
WHERE users.status = 'active'
  AND role_bindings.role = 'admin'
  AND role_bindings.scope_type = 'global'
`).Scan(&activeGlobalAdministrators); err != nil {
		return fmt.Errorf("count active global administrators: %w", err)
	}
	if activeGlobalAdministrators <= 1 {
		return ErrLastGlobalAdmin
	}
	return nil
}

func revokeUserSessions(
	ctx context.Context,
	transaction pgx.Tx,
	userID string,
	now time.Time,
) error {
	if _, err := transaction.Exec(ctx, `
UPDATE user_sessions
SET revoked_at = COALESCE(revoked_at, $2)
WHERE user_id = $1
`, userID, now); err != nil {
		return fmt.Errorf("revoke managed user sessions: %w", err)
	}
	return nil
}

func insertGlobalAccessAudit(
	ctx context.Context,
	transaction pgx.Tx,
	actorUserID string,
	action string,
	targetType string,
	targetID string,
	result string,
	requestID string,
	now time.Time,
) error {
	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id, actor_type, actor_user_id, scope_type, action, target_type,
    target_id, result, request_id, created_at
)
VALUES (
    gen_random_uuid(), 'user', $1, 'global', $2, $3,
    $4, $5, $6, $7
)
`, actorUserID, action, targetType, targetID, result, requestID, now); err != nil {
		return fmt.Errorf("record access management audit: %w", err)
	}
	return nil
}

func insertRoleBindingAudit(
	ctx context.Context,
	transaction pgx.Tx,
	actorUserID string,
	action string,
	binding ManagedRoleBinding,
	result string,
	requestID string,
	now time.Time,
) error {
	scopeType := binding.ScopeType
	if !strings.Contains(" global tenant project ", " "+scopeType+" ") {
		return errors.New("role binding audit scope is invalid")
	}
	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id, actor_type, actor_user_id, scope_type, tenant_id, project_id,
    action, target_type, target_id, result, request_id, created_at
)
VALUES (
    gen_random_uuid(), 'user', $1, $2, NULLIF($3, '')::uuid,
    NULLIF($4, '')::uuid, $5, 'role_binding', $6, $7, $8, $9
)
`,
		actorUserID,
		binding.ScopeType,
		binding.TenantID,
		binding.ProjectID,
		action,
		binding.ID,
		result,
		requestID,
		now,
	); err != nil {
		return fmt.Errorf("record role binding audit: %w", err)
	}
	return nil
}
