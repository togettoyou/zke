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
	// The actor holds `admin` globally, which is what reaching these APIs
	// requires: every route here is gated on `rbac.manage` at global scope, and
	// UpdateRole now refuses an edit that would cost the actor that permission.
	// An actor with no bindings was modelling a caller the router never admits.
	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ('73000000-0000-4000-8000-000000000005', $1, 'admin', 'global')
`, roleActorID); err != nil {
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

// Editing a role you hold is allowed; editing away your own way back in is not.
//
// The reasoning is ErrSelfUnbind's, reached by the other route: deleting the
// binding that grants your access is refused because you are the one person who
// then cannot undo it, and editing a permission out of the role behind that
// binding ends in exactly the same place.
//
// Every permission, not the ones that gate this API. The escalation ceiling only
// lets a role carry what its author holds globally, so a permission removed from
// its last global source cannot be written back by the person who removed it —
// `tenant.read` is as unrecoverable as `rbac.manage`, which is why the cases
// below include one of each.
func TestUpdateRoleRefusesRemovingTheActorsOwnRoleManagement(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		remaining []string
	}{
		{name: "without rbac.manage", remaining: []string{"rbac.read", "tenant.read"}},
		{name: "without rbac.read", remaining: []string{"rbac.manage", "tenant.read"}},
		// The case that showed the rule had been drawn in the wrong place: this
		// one leaves role management entirely intact and is still a one-way door.
		{name: "without an unrelated permission", remaining: []string{"rbac.read", "rbac.manage"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			pool := roleTestPool(t, ctx)
			accessStore := store.NewAccessManagementStore(pool)
			now := time.Now().UTC()

			// A custom role, and the actor holding only it: the fixture's builtin
			// binding is removed so this role is the actor's single global source
			// of everything in it.
			if _, err := accessStore.CreateRole(ctx, store.CreateManagedRoleParams{
				ID:          customRoleID,
				Name:        "access-operator",
				DisplayName: "权限运维",
				Permissions: []string{"rbac.read", "rbac.manage", "tenant.read"},
				ActorUserID: roleActorID,
				RequestID:   "73000000-0000-4000-8000-00000000000f",
				Now:         now,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ($1, $2, 'access-operator', 'global')
`, roleBindID, roleActorID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx,
				"DELETE FROM role_bindings WHERE subject_id = $1 AND role = 'admin'",
				roleActorID,
			); err != nil {
				t.Fatal(err)
			}

			_, err := accessStore.UpdateRole(ctx, store.UpdateManagedRoleParams{
				RoleID:      customRoleID,
				DisplayName: "权限运维",
				Permissions: testCase.remaining,
				ActorUserID: roleActorID,
				RequestID:   "73000000-0000-4000-8000-000000000010",
				Now:         now,
			})
			if !errors.Is(err, store.ErrSelfLockout) {
				t.Fatalf("UpdateRole() error = %v, want ErrSelfLockout", err)
			}
			// The refusal names what would be lost, so the operator knows which
			// checkbox to put back rather than bisecting the list.
			var detailed interface{ Detail() string }
			if !errors.As(err, &detailed) || detailed.Detail() == "" {
				t.Fatalf("refusal did not name the permissions: %v", err)
			}
			// Refused means rolled back, not partially applied.
			role, err := accessStore.GetRole(ctx, customRoleID)
			if err != nil {
				t.Fatal(err)
			}
			if len(role.Permissions) != 3 {
				t.Fatalf("permissions after the refused edit = %v, want all kept", role.Permissions)
			}

			// Editing a role you hold is not what is refused — losing something
			// by it is. The same role, same permissions, new description saves.
			if _, err := accessStore.UpdateRole(ctx, store.UpdateManagedRoleParams{
				RoleID:      customRoleID,
				DisplayName: "权限运维（改名）",
				Description: "同一套权限，只改说明",
				Permissions: []string{"rbac.read", "rbac.manage", "tenant.read"},
				ActorUserID: roleActorID,
				RequestID:   "73000000-0000-4000-8000-000000000011",
				Now:         now,
			}); err != nil {
				t.Fatalf("UpdateRole(same permissions) error = %v, want nil", err)
			}
		})
	}
}

// A role edit may only move permissions the actor holds — in either direction.
//
// The reported bug was the removal half. The ceiling checked the submitted set,
// so keeping a permission the editor did not hold was refused as escalation
// while removing it went through: an account with `rbac.manage` and little else
// could strip every custom role of authority it could never obtain, and that was
// also the *only* edit of such a role it could save. Renaming one required
// stripping it.
func TestUpdateRoleChecksBothDirectionsAgainstTheActor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := roleTestPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)
	now := time.Now().UTC()

	// The role carries a permission the actor will not hold; the actor is left
	// with role management and nothing else.
	if _, err := accessStore.CreateRole(ctx, store.CreateManagedRoleParams{
		ID:          customRoleID,
		Name:        "tenant-operator",
		DisplayName: "租户运维",
		Permissions: []string{"tenant.create", "tenant.read"},
		ActorUserID: roleActorID,
		RequestID:   "73000000-0000-4000-8000-000000000014",
		Now:         now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := accessStore.CreateRole(ctx, store.CreateManagedRoleParams{
		ID:          "73000000-0000-4000-8000-000000000006",
		Name:        "access-only",
		DisplayName: "仅权限管理",
		Permissions: []string{"rbac.read", "rbac.manage"},
		ActorUserID: roleActorID,
		RequestID:   "73000000-0000-4000-8000-000000000015",
		Now:         now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ($1, $2, 'access-only', 'global')
`, roleBindID, roleActorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"DELETE FROM role_bindings WHERE subject_id = $1 AND role = 'admin'", roleActorID,
	); err != nil {
		t.Fatal(err)
	}

	// Removing what the actor does not hold: the reported bug.
	_, err := accessStore.UpdateRole(ctx, store.UpdateManagedRoleParams{
		RoleID:      customRoleID,
		DisplayName: "租户运维",
		Permissions: []string{"tenant.read"},
		ActorUserID: roleActorID,
		RequestID:   "73000000-0000-4000-8000-000000000016",
		Now:         now,
	})
	if !errors.Is(err, store.ErrRoleRevokeForbidden) {
		t.Fatalf("UpdateRole(removing) error = %v, want ErrRoleRevokeForbidden", err)
	}

	// Adding what the actor does not hold: the half that was already refused.
	_, err = accessStore.UpdateRole(ctx, store.UpdateManagedRoleParams{
		RoleID:      customRoleID,
		DisplayName: "租户运维",
		Permissions: []string{"tenant.create", "tenant.read", "tenant.manage"},
		ActorUserID: roleActorID,
		RequestID:   "73000000-0000-4000-8000-000000000017",
		Now:         now,
	})
	if !errors.Is(err, store.ErrRoleGrantForbidden) {
		t.Fatalf("UpdateRole(adding) error = %v, want ErrRoleGrantForbidden", err)
	}

	// Leaving them alone is allowed, and this is the edit the old rule made
	// impossible: the description changes while the permission the actor cannot
	// touch stays exactly where it was.
	updated, err := accessStore.UpdateRole(ctx, store.UpdateManagedRoleParams{
		RoleID:      customRoleID,
		DisplayName: "租户运维",
		Description: "只改说明，权限原样保留",
		Permissions: []string{"tenant.create", "tenant.read"},
		ActorUserID: roleActorID,
		RequestID:   "73000000-0000-4000-8000-000000000018",
		Now:         now,
	})
	if err != nil {
		t.Fatalf("UpdateRole(unchanged permissions) error = %v, want nil", err)
	}
	if len(updated.Permissions) != 2 {
		t.Fatalf("permissions after the description edit = %v", updated.Permissions)
	}
}

// A second global source of the permissions makes the same edit fine.
//
// The check asks what the commit would leave, not whether this role happens to
// carry them — an actor who also holds `admin` can narrow a custom
// role freely, and a rule that could not tell the two apart would block
// ordinary work.
func TestUpdateRoleAllowsRemovingPermissionsTheActorHoldsElsewhere(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := roleTestPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)
	now := time.Now().UTC()

	if _, err := accessStore.CreateRole(ctx, store.CreateManagedRoleParams{
		ID:          customRoleID,
		Name:        "access-operator",
		DisplayName: "权限运维",
		Permissions: []string{"rbac.read", "rbac.manage"},
		ActorUserID: roleActorID,
		RequestID:   "73000000-0000-4000-8000-000000000012",
		Now:         now,
	}); err != nil {
		t.Fatal(err)
	}
	// Held in addition to the fixture's `admin` binding, which is not removed.
	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ($1, $2, 'access-operator', 'global')
`, roleBindID, roleActorID); err != nil {
		t.Fatal(err)
	}

	if _, err := accessStore.UpdateRole(ctx, store.UpdateManagedRoleParams{
		RoleID:      customRoleID,
		DisplayName: "权限运维",
		Permissions: []string{"cluster.read"},
		ActorUserID: roleActorID,
		RequestID:   "73000000-0000-4000-8000-000000000013",
		Now:         now,
	}); err != nil {
		t.Fatalf("UpdateRole() error = %v, want nil", err)
	}
}
