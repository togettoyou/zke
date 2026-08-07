package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/server/store"
)

const (
	roleActorID  = "73000000-0000-4000-8000-000000000001"
	roleSubject  = "73000000-0000-4000-8000-000000000002"
	customRoleID = "73000000-0000-4000-8000-000000000003"
	roleBindID   = "73000000-0000-4000-8000-000000000004"
)

func roleTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	pool := openIsolatedDatabase(t, ctx, requireAuthTestDatabaseURL(t))
	applyMigrations(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
INSERT INTO users (
    id, username_normalized, display_name, password_hash, status,
    password_changed_at
) VALUES
    ($1, 'role-actor', 'Role Actor', 'not-used', 'active', now()),
    ($2, 'role-subject', 'Role Subject', 'not-used', 'active', now())
`, roleActorID, roleSubject); err != nil {
		t.Fatal(err)
	}
	return pool
}

// The migration seeds the two builtin roles, and role_bindings references them.
// Both halves matter: without the rows, the initial administrator cannot be
// bound at all; without the foreign key, a binding could name a role that does
// not exist and authorization would silently grant nothing.
func TestBuiltinRolesAreSeededAndReferenced(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := roleTestPool(t, ctx)

	var permissions []string
	var builtin bool
	if err := pool.QueryRow(ctx,
		"SELECT permissions, builtin FROM roles WHERE name = 'admin'",
	).Scan(&permissions, &builtin); err != nil {
		t.Fatal(err)
	}
	if !builtin || len(permissions) == 0 {
		t.Fatalf("admin role builtin = %v with %d permissions", builtin, len(permissions))
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ($1, $2, 'does-not-exist', 'global')
`, roleBindID, roleSubject); err == nil {
		t.Fatal("a binding naming an unknown role was accepted")
	}
}

// EnsureBuiltinRoles has to widen `admin` when the Server grows a permission,
// otherwise the new permission is granted to nobody — which reads exactly like a
// denial and reports nothing.
func TestEnsureBuiltinRolesRefreshesTheAdminPermissionSet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := roleTestPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)

	if _, err := pool.Exec(ctx,
		"UPDATE roles SET permissions = ARRAY['cluster.read'] WHERE name = 'admin'",
	); err != nil {
		t.Fatal(err)
	}
	if err := accessStore.EnsureBuiltinRoles(ctx, []store.BuiltinRoleDefinition{{
		ID:          "00000000-0000-4000-8000-000000000001",
		Name:        "admin",
		DisplayName: "管理员",
		Description: "全部权限",
		Permissions: []string{"cluster.read", "rbac.manage", "user.manage"},
	}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	var permissions []string
	if err := pool.QueryRow(ctx,
		"SELECT permissions FROM roles WHERE name = 'admin'",
	).Scan(&permissions); err != nil {
		t.Fatal(err)
	}
	if len(permissions) != 3 {
		t.Fatalf("admin permissions = %v, want the reconciled set", permissions)
	}
}

func TestDeleteRoleRefusesABoundRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := roleTestPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)
	now := time.Now().UTC()

	if _, err := accessStore.CreateRole(ctx, store.CreateManagedRoleParams{
		ID:          customRoleID,
		Name:        "release-engineer",
		DisplayName: "发布工程师",
		Permissions: []string{"cluster.read"},
		ActorUserID: roleActorID,
		RequestID:   "73000000-0000-4000-8000-00000000000a",
		Now:         now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ($1, $2, 'release-engineer', 'global')
`, roleBindID, roleSubject); err != nil {
		t.Fatal(err)
	}

	_, err := accessStore.DeleteRole(ctx, store.DeleteManagedRoleParams{
		RoleID:      customRoleID,
		ActorUserID: roleActorID,
		RequestID:   "73000000-0000-4000-8000-00000000000b",
		Now:         now,
	})
	if !errors.Is(err, store.ErrRoleInUse) {
		t.Fatalf("DeleteRole() error = %v, want ErrRoleInUse", err)
	}

	// The role is still there, and so is the binding that stopped the deletion.
	role, err := accessStore.GetRole(ctx, customRoleID)
	if err != nil || role.BindingCount != 1 {
		t.Fatalf("role after refused deletion = %+v, err = %v", role, err)
	}
}

func TestUpdateRoleRefusesTheBuiltinRoles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := roleTestPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)
	now := time.Now().UTC()

	var adminRoleID string
	if err := pool.QueryRow(ctx,
		"SELECT id::text FROM roles WHERE name = 'admin'",
	).Scan(&adminRoleID); err != nil {
		t.Fatal(err)
	}
	if _, err := accessStore.UpdateRole(ctx, store.UpdateManagedRoleParams{
		RoleID:      adminRoleID,
		DisplayName: "改名",
		Permissions: []string{"cluster.read"},
		ActorUserID: roleActorID,
		RequestID:   "73000000-0000-4000-8000-00000000000c",
		Now:         now,
	}); !errors.Is(err, store.ErrRoleBuiltin) {
		t.Fatalf("UpdateRole(builtin) error = %v, want ErrRoleBuiltin", err)
	}
}

// Narrowing a custom role cannot remove the account of last resort.
//
// A global administrator holds the builtin `admin` role, which this statement
// cannot reach: `admin` is builtin, so the update excludes it, and its permission
// set is reconciled from code at every startup. An earlier version of this guard
// counted "accounts holding the recovery permissions", which made a custom role
// able to stand in for `admin` — and that was the same mistake that let a custom
// role delete the real administrator.
func TestUpdateRoleMayNarrowARoleItsHoldersDependOn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := roleTestPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)
	now := time.Now().UTC()

	if _, err := accessStore.CreateRole(ctx, store.CreateManagedRoleParams{
		ID:          customRoleID,
		Name:        "platform-admin",
		DisplayName: "平台管理员",
		Permissions: []string{"user.manage", "rbac.manage"},
		ActorUserID: roleActorID,
		RequestID:   "73000000-0000-4000-8000-00000000000d",
		Now:         now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ($1, $2, 'platform-admin', 'global')
`, roleBindID, roleSubject); err != nil {
		t.Fatal(err)
	}

	if _, err := accessStore.UpdateRole(ctx, store.UpdateManagedRoleParams{
		RoleID:      customRoleID,
		DisplayName: "平台管理员",
		Permissions: []string{"cluster.read"},
		ActorUserID: roleActorID,
		RequestID:   "73000000-0000-4000-8000-00000000000e",
		Now:         now,
	}); err != nil {
		t.Fatalf("UpdateRole() error = %v, want nil", err)
	}
	role, err := accessStore.GetRole(ctx, customRoleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(role.Permissions) != 1 {
		t.Fatalf("permissions after edit = %v", role.Permissions)
	}
}
