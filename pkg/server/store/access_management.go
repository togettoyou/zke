package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/togettoyou/zke/pkg/server/auditaction"
)

// userFilterSQL matches the Console's case-insensitive substring search over
// the identifiers an operator can see. position() is used rather than LIKE so
// that a search term containing % or _ needs no escaping.
//
// A lock is expired lazily: the row keeps `status = 'locked'` and its elapsed
// `lock_expires_at` until the next login attempt rewrites them. Filtering on the
// stored column would therefore answer "which accounts are locked" with accounts
// the login path already admits, so the filter compares against the effective
// status instead — the same condition `auth.Service.Login` evaluates.
const userFilterSQL = `
FROM users
WHERE (
    $1 = ''
    OR $1 = CASE
        WHEN status = 'locked' AND (lock_expires_at IS NULL OR lock_expires_at <= $3)
            THEN 'active'
        ELSE status
    END
  )
  AND (
    $2 = ''
    OR position($2 IN username_normalized) > 0
    OR position($2 IN lower(display_name)) > 0
    OR position($2 IN id::text) > 0
  )
`

func (store *AccessManagementStore) ListUsers(
	ctx context.Context,
	params ListManagedUsersParams,
) ([]ManagedUser, int, error) {
	return queryPage(
		ctx,
		store.pool,
		"SELECT count(*) "+userFilterSQL,
		`
SELECT
    id::text, username_normalized, display_name, status,
    failed_login_count, locked_at, lock_expires_at,
    password_changed_at, created_at, updated_at
`+userFilterSQL+`
ORDER BY username_normalized, id
LIMIT $4 OFFSET $5
`,
		[]any{params.Status, params.Search, params.Now},
		params.Page,
		func(rows pgx.Rows) (ManagedUser, error) {
			return scanManagedUser(rows)
		},
		"managed users",
	)
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
		ctx, transaction, input.ActorUserID, auditaction.UserUpdate, auditaction.TargetUser,
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
	item, err := scanManagedUser(transaction.QueryRow(ctx, `
SELECT
    id::text, username_normalized, display_name, status,
    failed_login_count, locked_at, lock_expires_at,
    password_changed_at, created_at, updated_at
FROM users
WHERE id = $1
  AND EXISTS (SELECT 1 FROM users WHERE id = $2 AND status = 'active')
FOR UPDATE
`, input.UserID, input.ActorUserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedUser{}, ErrAccessUserNotFound
	}
	if err != nil {
		return ManagedUser{}, fmt.Errorf("lock managed user deletion: %w", err)
	}
	// DeleteRoleBinding locks a binding row before checking the last-admin
	// invariant. Lock this user's bindings in the same order so concurrent
	// user and binding deletion cannot deadlock.
	rows, err := transaction.Query(ctx, `
SELECT id
FROM role_bindings
WHERE subject_id = $1
ORDER BY id
FOR UPDATE
`, input.UserID)
	if err != nil {
		return ManagedUser{}, fmt.Errorf("lock managed user role bindings: %w", err)
	}
	for rows.Next() {
		var bindingID string
		if err := rows.Scan(&bindingID); err != nil {
			rows.Close()
			return ManagedUser{}, fmt.Errorf("scan managed user role binding lock: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ManagedUser{}, fmt.Errorf("iterate managed user role binding locks: %w", err)
	}
	rows.Close()
	if err := ensureNotLastGlobalAdmin(ctx, transaction, input.ActorUserID, input.UserID); err != nil {
		return ManagedUser{}, err
	}
	// Audit before removing the target so fill_audit_event_names can snapshot
	// the username. Audit rows have no user foreign key and intentionally
	// outlive both the target and the actor they describe.
	if err := insertGlobalAccessAudit(
		ctx, transaction, input.ActorUserID, auditaction.UserDelete, auditaction.TargetUser,
		input.UserID, "succeeded", input.RequestID, input.Now,
	); err != nil {
		return ManagedUser{}, err
	}
	for _, statement := range []struct {
		sql         string
		description string
	}{
		{"DELETE FROM user_sessions WHERE user_id = $1", "managed user sessions"},
		{"DELETE FROM role_bindings WHERE subject_id = $1", "managed user role bindings"},
		{"DELETE FROM users WHERE id = $1", "managed user"},
	} {
		if _, err := transaction.Exec(ctx, statement.sql, input.UserID); err != nil {
			return ManagedUser{}, fmt.Errorf("delete %s: %w", statement.description, err)
		}
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
		ctx, transaction, input.ActorUserID, auditaction.UserCreate, auditaction.TargetUser,
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
		if err := ensureNotLastGlobalAdmin(ctx, transaction, input.ActorUserID, input.UserID); err != nil {
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
		ctx, transaction, input.ActorUserID, auditaction.UserStatusUpdate, auditaction.TargetUser,
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
	// Unlocking is a recovery action, and on an administrator's account it is
	// also the way to keep guessing: lockout is what ends a run of password
	// attempts, and clearing it from outside the group hands that run back. The
	// per-account rate limit still applies either way; this is the barrier it
	// sits behind, not a replacement for it.
	if err := ensureGlobalAdminTargetAllowed(
		ctx, transaction, input.ActorUserID, input.UserID,
	); err != nil {
		return ManagedUser{}, err
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
		ctx, transaction, input.ActorUserID, auditaction.UserUnlock, auditaction.TargetUser,
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
	// Before the state check, not after: who may act on this account is settled
	// ahead of what state the account happens to be in.
	if err := ensureGlobalAdminTargetAllowed(
		ctx, transaction, input.ActorUserID, input.UserID,
	); err != nil {
		return ManagedUser{}, err
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
		ctx, transaction, input.ActorUserID, auditaction.UserPasswordReset, auditaction.TargetUser,
		input.UserID, "succeeded", input.RequestID, input.Now,
	); err != nil {
		return ManagedUser{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ManagedUser{}, fmt.Errorf("commit managed password reset: %w", err)
	}
	return item, nil
}

// roleBindingColumnsSQL and roleBindingSourceSQL keep every read of a binding
// carrying the subject it is about. A binding stores a user id, which is the
// right thing to store and the wrong thing to read on its own: resolving the
// name is a join the database does once per page, and any caller left to do it
// for itself has to page the whole user table and still gives up past the page
// limit.
const roleBindingColumnsSQL = `
    role_bindings.id::text, role_bindings.subject_id::text,
    role_bindings.role, role_bindings.scope_type,
    COALESCE(role_bindings.tenant_id::text, ''),
    COALESCE(role_bindings.project_id::text, ''),
    role_bindings.created_at,
    COALESCE(subjects.username_normalized, ''),
    COALESCE(subjects.display_name, '')
`

// LEFT JOIN, not INNER: a binding whose subject row has gone must still be
// listed, so that it can be seen and removed.
const roleBindingSourceSQL = `
FROM role_bindings
LEFT JOIN users AS subjects ON subjects.id = role_bindings.subject_id
`

const roleBindingFilterSQL = roleBindingSourceSQL + `
WHERE ($1 = '' OR role_bindings.role = $1)
  AND ($2 = '' OR role_bindings.scope_type = $2)
  AND (
    $3 = ''
    OR position($3 IN role_bindings.id::text) > 0
    OR position($3 IN role_bindings.subject_id::text) > 0
    OR position($3 IN COALESCE(role_bindings.tenant_id::text, '')) > 0
    OR position($3 IN COALESCE(role_bindings.project_id::text, '')) > 0
    OR position($3 IN subjects.username_normalized) > 0
    OR position($3 IN lower(subjects.display_name)) > 0
  )
`

func (store *AccessManagementStore) ListRoleBindings(
	ctx context.Context,
	params ListManagedRoleBindingsParams,
) ([]ManagedRoleBinding, int, error) {
	return queryPage(
		ctx,
		store.pool,
		"SELECT count(*) "+roleBindingFilterSQL,
		"SELECT"+roleBindingColumnsSQL+roleBindingFilterSQL+`
ORDER BY role_bindings.created_at, role_bindings.id
LIMIT $4 OFFSET $5
`,
		[]any{params.Role, params.ScopeType, params.Search},
		params.Page,
		func(rows pgx.Rows) (ManagedRoleBinding, error) {
			return scanManagedRoleBinding(rows)
		},
		"managed role bindings",
	)
}

func (store *AccessManagementStore) GetRoleBinding(
	ctx context.Context,
	bindingID string,
) (ManagedRoleBinding, error) {
	item, err := scanManagedRoleBinding(store.pool.QueryRow(ctx,
		"SELECT"+roleBindingColumnsSQL+roleBindingSourceSQL+`
WHERE role_bindings.id = $1
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
	// Membership of the group that guards the account of last resort is granted
	// from inside it. Checked before the insert, in the same transaction, so a
	// concurrent removal of the actor's own binding cannot slip between them.
	if err := ensureGlobalAdminGrantAllowed(
		ctx, transaction, input.ActorUserID, input.Role, input.ScopeType,
	); err != nil {
		return ManagedRoleBinding{}, false, err
	}
	// Wrapped in a CTE because RETURNING can only see the inserted row, and the
	// created binding has to come back in the same shape as a listed one. The CTE
	// is named `created` rather than `role_bindings` so that it cannot be read as
	// shadowing the table its own INSERT targets.
	item, err := scanManagedRoleBinding(transaction.QueryRow(ctx, `
WITH created AS (
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
        id, subject_id, role, scope_type, tenant_id, project_id, created_at
)
SELECT
    created.id::text, created.subject_id::text,
    created.role, created.scope_type,
    COALESCE(created.tenant_id::text, ''),
    COALESCE(created.project_id::text, ''),
    created.created_at,
    COALESCE(subjects.username_normalized, ''),
    COALESCE(subjects.display_name, '')
FROM created
LEFT JOIN users AS subjects ON subjects.id = created.subject_id
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
		item, err = scanManagedRoleBinding(transaction.QueryRow(ctx,
			"SELECT"+roleBindingColumnsSQL+roleBindingSourceSQL+`
WHERE role_bindings.subject_id = $1
  AND role_bindings.role = $2
  AND role_bindings.scope_type = $3
  AND role_bindings.tenant_id IS NOT DISTINCT FROM NULLIF($4, '')::uuid
  AND role_bindings.project_id IS NOT DISTINCT FROM NULLIF($5, '')::uuid
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
		ctx, transaction, input.ActorUserID, auditaction.RoleBindingCreate,
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
	// `FOR UPDATE OF role_bindings`, not a bare `FOR UPDATE`: only the binding
	// row is being changed, and PostgreSQL refuses to lock the nullable side of
	// an outer join at all.
	item, err := scanManagedRoleBinding(transaction.QueryRow(ctx,
		"SELECT"+roleBindingColumnsSQL+roleBindingSourceSQL+`
WHERE role_bindings.id = $1
FOR UPDATE OF role_bindings
`, input.BindingID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedRoleBinding{}, ErrRoleBindingNotFound
	}
	if err != nil {
		return ManagedRoleBinding{}, fmt.Errorf("lock role binding deletion: %w", err)
	}
	// Checked before the administrator rule, because it is the more specific
	// account of what happened: an administrator unbinding themselves is told
	// they cannot unbind themselves, rather than that one administrator must
	// remain.
	if item.SubjectID == input.ActorUserID {
		return ManagedRoleBinding{}, ErrSelfUnbind
	}
	if item.Role == "admin" && item.ScopeType == "global" {
		if err := ensureNotLastGlobalAdmin(ctx, transaction, input.ActorUserID, item.SubjectID); err != nil {
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
		ctx, transaction, input.ActorUserID, auditaction.RoleBindingDelete,
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
		&item.SubjectUsername,
		&item.SubjectDisplayName,
	)
	return item, err
}

// Who counts as a global administrator.
//
// The builtin `admin` role, bound at global scope, on an active account. Not
// "holds an equivalent set of permissions": a custom role is written by whoever
// holds `rbac.manage`, so treating one as equivalent would make the account of
// last resort removable by an account somebody else defined — and a custom role
// carrying all 27 permissions is exactly what an attacker who reached
// `rbac.manage` would build.
//
// The role name is a literal because the store sits below the authorization
// package and cannot import it. `TestGlobalAdminRoleMatchesAuthorization` in the
// rbac package fails if the two ever disagree.
//
// Nothing is lost by keying on the role rather than on permissions: `admin` is
// builtin, uneditable, and reconciled from code at every startup, so an install
// that has one has every permission the Server defines in reach.
const globalAdminRoleName = "admin"

// GlobalAdminRoleName reports the role this invariant protects, so the
// authorization package can assert it still exists.
func GlobalAdminRoleName() string {
	return globalAdminRoleName
}

// globalAdministratorsSQL selects the active accounts holding the builtin role
// at global scope.
const globalAdministratorsSQL = `
SELECT DISTINCT users.id
FROM users
JOIN role_bindings ON role_bindings.subject_id = users.id
WHERE users.status = 'active'
  AND role_bindings.scope_type = 'global'
  AND role_bindings.role = $1
`

// Refuses removing a global administrator, unless another one is asking and
// another one remains.
//
// Two rules, and both are needed:
//
//   - one global administrator must always exist, or the install has no way back
//     in short of the database;
//   - only a global administrator may remove one, so holding a custom role — even
//     one carrying every permission there is — is not enough.
//
// The second rule is the one that matters against a compromised account. Without
// it, reaching any account with `user.manage` and `rbac.manage` was enough to
// delete the real administrator and be the only one left; the count said two, so
// the removal looked safe. It was safe for the platform and not for its owner.
//
// The advisory lock serialises this against a concurrent check, so two
// transactions each removing a different administrator cannot both observe two
// and both proceed.
func ensureNotLastGlobalAdmin(
	ctx context.Context,
	transaction pgx.Tx,
	actorUserID string,
	userID string,
) error {
	facts, err := readGlobalAdministrators(ctx, transaction, actorUserID, userID)
	if err != nil {
		return err
	}
	if !facts.targetIsAdmin {
		return nil
	}
	if !facts.actorIsAdmin {
		return ErrGlobalAdminRequired
	}
	// Only an administrator who is currently counted can be the last one. A
	// locked or disabled administrator is already not holding the install up, so
	// removing them takes nothing away from the count and the second rule has
	// nothing to refuse.
	if facts.targetIsActiveAdmin && facts.administrators <= 1 {
		return ErrLastGlobalAdmin
	}
	return nil
}

// Refuses taking over a global administrator's account from outside the group.
//
// The membership half of the rule above, without the count. Seizing an account
// is not removing it, so "one must always remain" has nothing to say here —
// but "only a global administrator may touch one" has everything to say, and
// this is the operation that most needed it and least had it.
//
// A password reset hands the account to whoever asked for it. Every guard on
// becoming a global administrator watches role bindings: `admin` may only be
// granted by an administrator, an administrator may only be unbound by another
// one, and no role may carry more than its author holds. None of them is
// looking at the password. `user.manage` alone was therefore the whole ladder —
// reset the administrator's password, sign in as them, and arrive with every
// permission there is, holding no binding that any of those checks would ever
// see.
//
// Sessions are revoked by the reset itself, so the real administrator is signed
// out at the same moment. Discovering the takeover means noticing an audit row
// among the ordinary helpdesk traffic that looks exactly like a password reset,
// because that is what it is.
func ensureGlobalAdminTargetAllowed(
	ctx context.Context,
	transaction pgx.Tx,
	actorUserID string,
	userID string,
) error {
	facts, err := readGlobalAdministrators(ctx, transaction, actorUserID, userID)
	if err != nil {
		return err
	}
	if !facts.targetIsAdmin || facts.actorIsAdmin {
		return nil
	}
	return ErrGlobalAdminRequired
}

// What both guards need to know, read once under the invariant lock.
//
// Two questions, not one, and the difference is `users.status`.
//
// "How many are left" is about who can still sign in, so it counts active
// accounts only — a locked or disabled administrator is not holding the install
// up. "Is this an administrator's account" is not: the binding is what makes the
// account worth seizing, and it survives the account being locked. Asking the
// active-only question on the target side left a hole shaped exactly like the
// lockout: five wrong passwords take an administrator off `active`, and every
// guard keyed on that set then agreed the account was an ordinary one.
type globalAdministratorFacts struct {
	// Holds the builtin role at global scope, whatever state the account is in.
	targetIsAdmin bool
	// ... and is active, so counts towards the administrators below.
	targetIsActiveAdmin bool
	actorIsAdmin        bool
	administrators      int
}

func readGlobalAdministrators(
	ctx context.Context,
	transaction pgx.Tx,
	actorUserID string,
	userID string,
) (globalAdministratorFacts, error) {
	if err := lockGlobalAdministratorInvariant(ctx, transaction); err != nil {
		return globalAdministratorFacts{}, err
	}
	var facts globalAdministratorFacts
	if err := transaction.QueryRow(ctx, `
WITH administrators AS (`+globalAdministratorsSQL+`)
SELECT
    EXISTS (
        SELECT 1 FROM role_bindings
        WHERE role_bindings.subject_id = $2
          AND role_bindings.scope_type = 'global'
          AND role_bindings.role = $1
    ),
    EXISTS (SELECT 1 FROM administrators WHERE id = $2),
    EXISTS (SELECT 1 FROM administrators WHERE id = $3),
    (SELECT count(*) FROM administrators)
`,
		globalAdminRoleName,
		userID,
		actorUserID,
	).Scan(
		&facts.targetIsAdmin,
		&facts.targetIsActiveAdmin,
		&facts.actorIsAdmin,
		&facts.administrators,
	); err != nil {
		return globalAdministratorFacts{}, fmt.Errorf(
			"check global administrator invariant: %w", err,
		)
	}
	return facts, nil
}

// Refuses granting the builtin role to anyone but by a global administrator.
//
// Without this the rule above is theatre: a custom role carrying all 27
// permissions satisfies the escalation ceiling for `admin`, so its holder could
// bind `admin` to themselves, become a global administrator by their own hand,
// and then remove the original. Membership of the group that guards the account
// of last resort has to be granted from inside it.
func ensureGlobalAdminGrantAllowed(
	ctx context.Context,
	transaction pgx.Tx,
	actorUserID string,
	role string,
	scopeType string,
) error {
	if role != globalAdminRoleName || scopeType != "global" {
		return nil
	}
	if err := lockGlobalAdministratorInvariant(ctx, transaction); err != nil {
		return err
	}
	var actorIsAdmin bool
	if err := transaction.QueryRow(ctx,
		"WITH administrators AS ("+globalAdministratorsSQL+`)
SELECT EXISTS (SELECT 1 FROM administrators WHERE id = $2)
`,
		globalAdminRoleName,
		actorUserID,
	).Scan(&actorIsAdmin); err != nil {
		return fmt.Errorf("check global administrator grant: %w", err)
	}
	if !actorIsAdmin {
		return ErrGlobalAdminRequired
	}
	return nil
}

func lockGlobalAdministratorInvariant(
	ctx context.Context,
	transaction pgx.Tx,
) error {
	if _, err := transaction.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1)",
		int64(0x5a4b4541444d494e),
	); err != nil {
		return fmt.Errorf("lock global administrator invariant: %w", err)
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
    NULLIF($4, '')::uuid, $5, $6, $7, $8, $9, $10
)
`,
		actorUserID,
		binding.ScopeType,
		binding.TenantID,
		binding.ProjectID,
		action,
		auditaction.TargetRoleBinding,
		binding.ID,
		result,
		requestID,
		now,
	); err != nil {
		return fmt.Errorf("record role binding audit: %w", err)
	}
	return nil
}
