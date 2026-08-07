package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/togettoyou/zke/pkg/server/auditaction"
)

// Roles as rows.
//
// A role is a named permission set that bindings point at, so every write here
// changes what somebody can already do — without touching their bindings. That
// is why each one is a transaction with an audit row, and why the two builtin
// roles are refused by the same rules rather than by a check the caller has to
// remember.

// roleColumnsSQL is shared by every read so a role has one shape. The binding
// count is a correlated subquery rather than a join with GROUP BY, because the
// rest of the row is already unique per role and grouping would only make the
// zero case harder to get right.
const roleColumnsSQL = `
    roles.id::text,
    roles.name,
    roles.display_name,
    roles.description,
    roles.builtin,
    roles.permissions,
    (
        SELECT count(*)
        FROM role_bindings
        WHERE role_bindings.role = roles.name
    ),
    roles.created_at,
    roles.updated_at
`

// roleFilterSQL matches the Console's case-insensitive search over the fields an
// operator identifies a role by. position() is used rather than LIKE so a term
// containing % or _ needs no escaping.
const roleFilterSQL = `
FROM roles
WHERE ($1 = '' OR ($1 = 'true') = roles.builtin)
  AND (
    $2 = ''
    OR position($2 IN lower(roles.name)) > 0
    OR position($2 IN lower(roles.display_name)) > 0
  )
`

// EnsureBuiltinRoles brings the builtin roles into line with the Server's own
// definition.
//
// This is the only thing that writes them. The schema creates an empty `roles`
// table and seeds nothing: `admin` means "every permission the Server knows",
// which is a statement about the permission list rather than a list of its own,
// and a copy of it in SQL would go stale the first time a permission was added.
// So the rows are inserted here, at startup, before anything can authorize
// against them and before the initial administrator is bound to one.
//
// Only builtin rows are touched, and only by name. An operator's role is never
// rewritten from here, and a name collision cannot happen because the API
// refuses to create a role called `admin` or `viewer`.
func (store *AccessManagementStore) EnsureBuiltinRoles(
	ctx context.Context,
	roles []BuiltinRoleDefinition,
	now time.Time,
) error {
	if len(roles) == 0 {
		return errors.New("builtin role definitions are required")
	}
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin builtin role reconciliation: %w", err)
	}
	defer rollbackTransaction(transaction)
	for _, role := range roles {
		if len(role.Permissions) == 0 {
			return fmt.Errorf("builtin role %q has no permissions", role.Name)
		}
		// Upsert rather than insert: a fresh database needs the row created, and
		// every later start needs the permission set refreshed in case the
		// Server's own definition has widened since.
		if _, err := transaction.Exec(ctx, `
INSERT INTO roles (
    id, name, display_name, description, builtin, permissions,
    created_at, updated_at
)
VALUES ($1, $2, $3, $4, true, $5, $6, $6)
ON CONFLICT (name) DO UPDATE SET
    display_name = EXCLUDED.display_name,
    description = EXCLUDED.description,
    builtin = true,
    permissions = EXCLUDED.permissions,
    updated_at = CASE
        WHEN roles.display_name IS DISTINCT FROM EXCLUDED.display_name
            OR roles.description IS DISTINCT FROM EXCLUDED.description
            OR roles.permissions IS DISTINCT FROM EXCLUDED.permissions
            OR NOT roles.builtin
        THEN EXCLUDED.updated_at
        ELSE roles.updated_at
    END
`,
			role.ID,
			role.Name,
			role.DisplayName,
			role.Description,
			role.Permissions,
			now,
		); err != nil {
			return fmt.Errorf("reconcile builtin role %q: %w", role.Name, err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit builtin role reconciliation: %w", err)
	}
	return nil
}

// BuiltinRoleDefinition is one row EnsureBuiltinRoles maintains. It repeats the
// authorization package's type rather than importing it, because the store
// depends on nothing above it.
type BuiltinRoleDefinition struct {
	ID          string
	Name        string
	DisplayName string
	Description string
	Permissions []string
}

func (store *AccessManagementStore) ListRoles(
	ctx context.Context,
	params ListManagedRolesParams,
) ([]ManagedRole, int, error) {
	return queryPage(
		ctx,
		store.pool,
		"SELECT count(*) "+roleFilterSQL,
		"SELECT"+roleColumnsSQL+roleFilterSQL+`
ORDER BY roles.builtin DESC, lower(roles.display_name), roles.id
LIMIT $3 OFFSET $4
`,
		[]any{params.Builtin, params.Search},
		params.Page,
		func(rows pgx.Rows) (ManagedRole, error) {
			return scanManagedRole(rows)
		},
		"roles",
	)
}

func (store *AccessManagementStore) GetRole(
	ctx context.Context,
	roleID string,
) (ManagedRole, error) {
	item, err := scanManagedRole(store.pool.QueryRow(ctx,
		"SELECT"+roleColumnsSQL+"FROM roles WHERE roles.id = $1", roleID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedRole{}, ErrRoleNotFound
	}
	if err != nil {
		return ManagedRole{}, fmt.Errorf("get role: %w", err)
	}
	return item, nil
}

// FindRoleByName answers whether a role exists and what it permits. Binding
// creation uses it so that naming a role that does not exist is reported as a
// missing target rather than as the foreign key violation it would otherwise
// become.
func (store *AccessManagementStore) FindRoleByName(
	ctx context.Context,
	name string,
) (ManagedRole, error) {
	item, err := scanManagedRole(store.pool.QueryRow(ctx,
		"SELECT"+roleColumnsSQL+"FROM roles WHERE roles.name = $1", name,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedRole{}, ErrRoleNotFound
	}
	if err != nil {
		return ManagedRole{}, fmt.Errorf("find role by name: %w", err)
	}
	return item, nil
}

func (store *AccessManagementStore) CreateRole(
	ctx context.Context,
	input CreateManagedRoleParams,
) (ManagedRole, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ManagedRole{}, fmt.Errorf("begin role creation: %w", err)
	}
	defer rollbackTransaction(transaction)
	item, err := scanManagedRole(transaction.QueryRow(ctx, `
WITH created AS (
    INSERT INTO roles (
        id, name, display_name, description, builtin, permissions,
        created_at, updated_at
    )
    VALUES ($1, $2, $3, $4, false, $5, $6, $6)
    RETURNING *
)
SELECT`+roleColumnsSQL+`FROM created AS roles
`,
		input.ID,
		input.Name,
		input.DisplayName,
		input.Description,
		input.Permissions,
		input.Now,
	))
	if err != nil {
		return ManagedRole{}, createRoleError(err)
	}
	if err := insertRoleAudit(
		ctx, transaction, input.ActorUserID, auditaction.RoleCreate,
		item, "succeeded", input.RequestID, input.Now,
	); err != nil {
		return ManagedRole{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ManagedRole{}, fmt.Errorf("commit role creation: %w", err)
	}
	return item, nil
}

// UpdateRole replaces a role's editable fields.
//
// `name` is not among them. It is what bindings store and what audit rows
// already recorded, so rewriting it would change what a historical record
// appears to say about a role that no longer has that name. The display name is
// the one an operator renames.
//
// The builtin guard is in the statement rather than in a read before it: a
// separate check would leave a window in which the row could be reconciled
// between the two, and the API would report a success the database refused.
func (store *AccessManagementStore) UpdateRole(
	ctx context.Context,
	input UpdateManagedRoleParams,
) (ManagedRole, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ManagedRole{}, fmt.Errorf("begin role update: %w", err)
	}
	defer rollbackTransaction(transaction)
	item, err := scanManagedRole(transaction.QueryRow(ctx, `
WITH updated AS (
    UPDATE roles
    SET display_name = $2,
        description = $3,
        permissions = $4,
        updated_at = $5
    WHERE id = $1
      AND NOT builtin
    RETURNING *
)
SELECT`+roleColumnsSQL+`FROM updated AS roles
`,
		input.RoleID,
		input.DisplayName,
		input.Description,
		input.Permissions,
		input.Now,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedRole{}, store.missingOrBuiltinRole(ctx, transaction, input.RoleID)
	}
	if err != nil {
		return ManagedRole{}, fmt.Errorf("update role: %w", err)
	}
	// No last-administrator check here. A global administrator is someone
	// holding the builtin `admin` role, and this statement cannot touch that:
	// `NOT builtin` excludes it, and its permission set is reconciled from code
	// at every startup. Narrowing a custom role can take a lot away from a lot
	// of people, but it cannot remove the account of last resort.
	if err := insertRoleAudit(
		ctx, transaction, input.ActorUserID, auditaction.RoleUpdate,
		item, "succeeded", input.RequestID, input.Now,
	); err != nil {
		return ManagedRole{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return ManagedRole{}, fmt.Errorf("commit role update: %w", err)
	}
	return item, nil
}

// DeleteRole removes a role nobody holds.
//
// The binding count is read inside the transaction and the foreign key stands
// behind it: between the two, a role that gains a binding while it is being
// deleted is refused rather than deleted out from under whoever holds it. The
// count is what produces the useful error; the constraint is what makes the
// answer true.
func (store *AccessManagementStore) DeleteRole(
	ctx context.Context,
	input DeleteManagedRoleParams,
) (ManagedRole, error) {
	transaction, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ManagedRole{}, fmt.Errorf("begin role deletion: %w", err)
	}
	defer rollbackTransaction(transaction)
	item, err := scanManagedRole(transaction.QueryRow(ctx,
		"SELECT"+roleColumnsSQL+"FROM roles WHERE roles.id = $1 FOR UPDATE OF roles",
		input.RoleID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return ManagedRole{}, ErrRoleNotFound
	}
	if err != nil {
		return ManagedRole{}, fmt.Errorf("read role for deletion: %w", err)
	}
	if item.Builtin {
		return ManagedRole{}, ErrRoleBuiltin
	}
	if item.BindingCount > 0 {
		return ManagedRole{}, ErrRoleInUse
	}
	// Audited before the row goes, so the record is written while the role it
	// names still exists and the whole thing lands or none of it does.
	if err := insertRoleAudit(
		ctx, transaction, input.ActorUserID, auditaction.RoleDelete,
		item, "succeeded", input.RequestID, input.Now,
	); err != nil {
		return ManagedRole{}, err
	}
	if _, err := transaction.Exec(ctx,
		"DELETE FROM roles WHERE id = $1 AND NOT builtin", input.RoleID,
	); err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "23503" {
			return ManagedRole{}, ErrRoleInUse
		}
		return ManagedRole{}, fmt.Errorf("delete role: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return ManagedRole{}, fmt.Errorf("commit role deletion: %w", err)
	}
	return item, nil
}

// missingOrBuiltinRole tells the two reasons an update matched nothing apart.
// They are different answers — one is a 404 and the other a refusal about a row
// that exists — and the UPDATE cannot report which without a second look.
func (store *AccessManagementStore) missingOrBuiltinRole(
	ctx context.Context,
	transaction pgx.Tx,
	roleID string,
) error {
	var builtin bool
	err := transaction.QueryRow(
		ctx, "SELECT builtin FROM roles WHERE id = $1", roleID,
	).Scan(&builtin)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRoleNotFound
	}
	if err != nil {
		return fmt.Errorf("classify role update failure: %w", err)
	}
	if builtin {
		return ErrRoleBuiltin
	}
	// The row exists and is editable, so the UPDATE matching nothing means it
	// was removed between the two statements.
	return ErrRoleNotFound
}

func createRoleError(err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		switch databaseError.Code {
		case "23505":
			return ErrRoleConflict
		case "23514":
			return ErrAccessStateConflict
		}
	}
	return fmt.Errorf("insert role: %w", err)
}

func insertRoleAudit(
	ctx context.Context,
	transaction pgx.Tx,
	actorUserID string,
	action string,
	role ManagedRole,
	result string,
	requestID string,
	now time.Time,
) error {
	// Global scope, always: a role is not owned by a Tenant or a Project, and
	// filing the record under one would hide a change that reaches every scope
	// the role is bound in.
	if _, err := transaction.Exec(ctx, `
INSERT INTO audit_events (
    id, actor_type, actor_user_id, scope_type,
    action, target_type, target_id, target_name, result, request_id, created_at
)
VALUES (
    gen_random_uuid(), 'user', $1, 'global',
    $2, $3, $4, $5, $6, $7, $8
)
`,
		actorUserID,
		action,
		auditaction.TargetRole,
		role.ID,
		role.Name,
		result,
		requestID,
		now,
	); err != nil {
		return fmt.Errorf("record role audit: %w", err)
	}
	return nil
}

type roleRowScanner interface {
	Scan(destination ...any) error
}

func scanManagedRole(row roleRowScanner) (ManagedRole, error) {
	var item ManagedRole
	if err := row.Scan(
		&item.ID,
		&item.Name,
		&item.DisplayName,
		&item.Description,
		&item.Builtin,
		&item.Permissions,
		&item.BindingCount,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return ManagedRole{}, err
	}
	return item, nil
}
