package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/server/store"
)

const (
	builtinAdminUserID = "74000000-0000-4000-8000-000000000001"
	customAdminUserID  = "74000000-0000-4000-8000-000000000002"
	plainUserID        = "74000000-0000-4000-8000-000000000003"
)

// The global administrator is the account of last resort.
//
// "Global administrator" means the builtin `admin` role bound at global scope —
// not "holds an equivalent set of permissions". A custom role can be written by
// anyone with `rbac.manage`, so letting one stand in for the builtin role would
// make the account of last resort removable by an account somebody else defined.
func globalAdminPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	pool := openIsolatedDatabase(t, ctx, requireAuthTestDatabaseURL(t))
	applyMigrations(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
INSERT INTO users (
    id, username_normalized, display_name, password_hash, status, password_changed_at
) VALUES
    ($1, 'builtin-admin', 'Builtin Admin', 'not-used', 'active', now()),
    ($2, 'custom-admin', 'Custom Admin', 'not-used', 'active', now()),
    ($3, 'plain-user', 'Plain User', 'not-used', 'active', now())
`, builtinAdminUserID, customAdminUserID, plainUserID); err != nil {
		t.Fatal(err)
	}
	// A custom role carrying every permission the platform defines — the
	// strongest thing an operator can build that is still not `admin`.
	if _, err := pool.Exec(ctx, `
INSERT INTO roles (id, name, display_name, builtin, permissions)
SELECT
    '74000000-0000-4000-8000-00000000000a', 'super-operator', '超级运维', false,
    permissions
FROM roles WHERE name = 'admin'
`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES
    ('74000000-0000-4000-8000-00000000000b', $1, 'admin', 'global'),
    ('74000000-0000-4000-8000-00000000000c', $2, 'super-operator', 'global')
`, builtinAdminUserID, customAdminUserID); err != nil {
		t.Fatal(err)
	}
	return pool
}

// The reported bug: the sole global administrator could be deleted by the holder
// of a custom role, because the guard counted "someone who can still recover"
// rather than "another global administrator".
func TestCustomRoleCannotDeleteTheGlobalAdministrator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)

	_, err := accessStore.DeleteUser(ctx, store.DeleteManagedUserParams{
		UserID:      builtinAdminUserID,
		ActorUserID: customAdminUserID,
		RequestID:   "74000000-0000-4000-8000-0000000000f1",
		Now:         time.Now().UTC(),
	})
	// The actor-side refusal, not the count-side one: another administrator does
	// exist by the old definition, and that was exactly the bug.
	if !errors.Is(err, store.ErrGlobalAdminRequired) {
		t.Fatalf("DeleteUser() error = %v, want ErrGlobalAdminRequired", err)
	}

	var exists bool
	if err := pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)", builtinAdminUserID,
	).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("the global administrator was deleted")
	}
}

// Disabling is the same removal by another route, and so is unbinding.
func TestCustomRoleCannotDisableOrUnbindTheGlobalAdministrator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)
	now := time.Now().UTC()

	if _, err := accessStore.SetUserStatus(ctx, store.SetManagedUserStatusParams{
		UserID:      builtinAdminUserID,
		Status:      "disabled",
		ActorUserID: customAdminUserID,
		RequestID:   "74000000-0000-4000-8000-0000000000f2",
		Now:         now,
	}); !errors.Is(err, store.ErrGlobalAdminRequired) {
		t.Fatalf("SetUserStatus() error = %v, want ErrGlobalAdminRequired", err)
	}

	if _, err := accessStore.DeleteRoleBinding(ctx, store.DeleteManagedRoleBindingParams{
		BindingID:   "74000000-0000-4000-8000-00000000000b",
		ActorUserID: customAdminUserID,
		RequestID:   "74000000-0000-4000-8000-0000000000f3",
		Now:         now,
	}); !errors.Is(err, store.ErrGlobalAdminRequired) {
		t.Fatalf("DeleteRoleBinding() error = %v, want ErrGlobalAdminRequired", err)
	}
}

// The bypass that would make the rule theatre.
//
// A custom role holding every permission satisfies the escalation ceiling for
// `admin`, so without this its holder could bind `admin` to themselves, become a
// global administrator, and then remove the original.
func TestOnlyAGlobalAdministratorMayGrantTheAdminRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)

	_, _, err := accessStore.CreateRoleBinding(ctx, store.CreateManagedRoleBindingParams{
		ID:          "74000000-0000-4000-8000-0000000000d1",
		SubjectID:   customAdminUserID,
		Role:        "admin",
		ScopeType:   "global",
		ActorUserID: customAdminUserID,
		RequestID:   "74000000-0000-4000-8000-0000000000f4",
		Now:         time.Now().UTC(),
	})
	if !errors.Is(err, store.ErrGlobalAdminRequired) {
		t.Fatalf("CreateRoleBinding() error = %v, want ErrGlobalAdminRequired", err)
	}
}

// A second global administrator makes all of it allowed — the rule is about who
// is asking, not a blanket freeze on the account.
func TestAnotherGlobalAdministratorMayRemoveOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)

	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ('74000000-0000-4000-8000-0000000000e1', $1, 'admin', 'global')
`, customAdminUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := accessStore.DeleteUser(ctx, store.DeleteManagedUserParams{
		UserID:      builtinAdminUserID,
		ActorUserID: customAdminUserID,
		RequestID:   "74000000-0000-4000-8000-0000000000f5",
		Now:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("DeleteUser() error = %v, want nil", err)
	}
}

// The reported bug: every guard watched role bindings, and none watched the
// password. Resetting the administrator's password and signing in as them
// arrives with every permission there is, holding no binding for the binding
// guards to refuse.
func TestCustomRoleCannotResetTheGlobalAdministratorPassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)

	_, err := accessStore.ResetUserPassword(ctx, store.ResetManagedUserPasswordParams{
		UserID:       builtinAdminUserID,
		PasswordHash: "seized",
		ActorUserID:  customAdminUserID,
		RequestID:    "74000000-0000-4000-8000-0000000000f7",
		Now:          time.Now().UTC(),
	})
	if !errors.Is(err, store.ErrGlobalAdminRequired) {
		t.Fatalf("ResetUserPassword() error = %v, want ErrGlobalAdminRequired", err)
	}

	var hash string
	if err := pool.QueryRow(ctx,
		"SELECT password_hash FROM users WHERE id = $1", builtinAdminUserID,
	).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != "not-used" {
		t.Fatalf("the global administrator's password was reset to %q", hash)
	}
}

// The rule is about the account, not about a list of dangerous fields.
//
// Renaming a global administrator gives the caller nothing — which is exactly
// why it was the operation left open, and exactly why it belongs here: a rule
// stated as "may not take over the account" invites each new operation to be
// judged on its own harm, and the judgement is made by whoever is adding that
// operation. Stated as "may not act on the account", there is nothing to judge.
func TestCustomRoleCannotRenameTheGlobalAdministrator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)

	_, err := accessStore.UpdateUser(ctx, store.UpdateManagedUserParams{
		UserID:      builtinAdminUserID,
		DisplayName: "renamed by an outsider",
		ActorUserID: customAdminUserID,
		RequestID:   "74000000-0000-4000-8000-0000000000fa",
		Now:         time.Now().UTC(),
	})
	if !errors.Is(err, store.ErrGlobalAdminRequired) {
		t.Fatalf("UpdateUser() error = %v, want ErrGlobalAdminRequired", err)
	}

	var displayName string
	if err := pool.QueryRow(ctx,
		"SELECT display_name FROM users WHERE id = $1", builtinAdminUserID,
	).Scan(&displayName); err != nil {
		t.Fatal(err)
	}
	if displayName == "renamed by an outsider" {
		t.Fatal("the global administrator was renamed from outside the group")
	}
}

// And the two halves that must keep working: another administrator may rename
// one, and an ordinary account is not covered by the guard at all.
func TestRenamingIsAllowedInsideTheGroupAndForOrdinaryAccounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)
	now := time.Now().UTC()

	if _, err := accessStore.UpdateUser(ctx, store.UpdateManagedUserParams{
		UserID:      builtinAdminUserID,
		DisplayName: "renamed by an administrator",
		ActorUserID: builtinAdminUserID,
		RequestID:   "74000000-0000-4000-8000-0000000000fb",
		Now:         now,
	}); err != nil {
		t.Fatalf("UpdateUser(self) error = %v, want nil", err)
	}

	if _, err := accessStore.UpdateUser(ctx, store.UpdateManagedUserParams{
		UserID:      plainUserID,
		DisplayName: "renamed by the helpdesk",
		ActorUserID: customAdminUserID,
		RequestID:   "74000000-0000-4000-8000-0000000000fc",
		Now:         now,
	}); err != nil {
		t.Fatalf("UpdateUser(plain user) error = %v, want nil", err)
	}
}

// The sole administrator may still reset their own password.
//
// Worth its own case because the neighbouring rule counts administrators and
// this one must not: reusing it would have left the only administrator unable
// to change the password of the only account that can still get in.
func TestAGlobalAdministratorMayResetPasswords(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)
	now := time.Now().UTC()

	if _, err := accessStore.ResetUserPassword(ctx, store.ResetManagedUserPasswordParams{
		UserID:       builtinAdminUserID,
		PasswordHash: "rotated",
		ActorUserID:  builtinAdminUserID,
		RequestID:    "74000000-0000-4000-8000-0000000000f8",
		Now:          now,
	}); err != nil {
		t.Fatalf("ResetUserPassword(self) error = %v, want nil", err)
	}

	// And an ordinary account is not covered by the guard at all, so the
	// helpdesk case a non-administrator is meant to serve still works.
	if _, err := accessStore.ResetUserPassword(ctx, store.ResetManagedUserPasswordParams{
		UserID:       plainUserID,
		PasswordHash: "issued",
		ActorUserID:  customAdminUserID,
		RequestID:    "74000000-0000-4000-8000-0000000000f9",
		Now:          now,
	}); err != nil {
		t.Fatalf("ResetUserPassword(plain user) error = %v, want nil", err)
	}
}

// Deleting a binding revokes whatever its role grants, so it answers to the
// same rule as editing that role: you may only take away what you hold.
//
// The custom role here carries every permission — the platform allows that, and
// the administrator rules do not cover it because it is not `admin`. Before this
// check, an account with role management and nothing else could delete the
// binding and strip its holder of authority it could never obtain, which is the
// role-edit refusal reached by a shorter route.
func TestRevokingABindingRequiresHoldingWhatItGrants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)

	// The plain user is given the all-powerful custom role, and the actor is left
	// holding only role management.
	if _, err := pool.Exec(ctx, `
INSERT INTO roles (id, name, display_name, builtin, permissions)
VALUES (
    '74000000-0000-4000-8000-00000000000d', 'access-only', '仅权限管理', false,
    ARRAY['rbac.read', 'rbac.manage']
)
`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES
    ('74000000-0000-4000-8000-00000000000e', $1, 'super-operator', 'global'),
    ('74000000-0000-4000-8000-00000000000f', $2, 'access-only', 'global')
`, plainUserID, customAdminUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"DELETE FROM role_bindings WHERE subject_id = $1 AND role = 'super-operator'",
		customAdminUserID,
	); err != nil {
		t.Fatal(err)
	}

	_, err := accessStore.DeleteRoleBinding(ctx, store.DeleteManagedRoleBindingParams{
		BindingID:   "74000000-0000-4000-8000-00000000000e",
		ActorUserID: customAdminUserID,
		RequestID:   "74000000-0000-4000-8000-000000000101",
		Now:         time.Now().UTC(),
	})
	if !errors.Is(err, store.ErrRoleRevokeForbidden) {
		t.Fatalf("DeleteRoleBinding() error = %v, want ErrRoleRevokeForbidden", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM role_bindings WHERE id = $1",
		"74000000-0000-4000-8000-00000000000e",
	).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatal("the refused deletion removed the binding anyway")
	}

	// The global administrator holds everything, so the same deletion is theirs
	// to make — the rule is about who is asking, not a freeze on the binding.
	if _, err := accessStore.DeleteRoleBinding(ctx, store.DeleteManagedRoleBindingParams{
		BindingID:   "74000000-0000-4000-8000-00000000000e",
		ActorUserID: builtinAdminUserID,
		RequestID:   "74000000-0000-4000-8000-000000000102",
		Now:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("DeleteRoleBinding(global administrator) error = %v, want nil", err)
	}
}

// A binding is deleted by its id, so nothing in the request says whose access
// is about to be removed. Removing your own is the one deletion here that takes
// away the permission that authorized it.
func TestNobodyMayDeleteTheirOwnRoleBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)

	if _, err := accessStore.DeleteRoleBinding(ctx, store.DeleteManagedRoleBindingParams{
		BindingID:   "74000000-0000-4000-8000-00000000000c",
		ActorUserID: customAdminUserID,
		RequestID:   "74000000-0000-4000-8000-0000000000fa",
		Now:         time.Now().UTC(),
	}); !errors.Is(err, store.ErrSelfUnbind) {
		t.Fatalf("DeleteRoleBinding(own) error = %v, want ErrSelfUnbind", err)
	}

	// The sole administrator's own binding is refused by the rule above it,
	// not by this one. Both would refuse, and the order decides which fact the
	// operator is told: "have another administrator do it" is what the self rule
	// says, and at this point there is no other administrator.
	if _, err := accessStore.DeleteRoleBinding(ctx, store.DeleteManagedRoleBindingParams{
		BindingID:   "74000000-0000-4000-8000-00000000000b",
		ActorUserID: builtinAdminUserID,
		RequestID:   "74000000-0000-4000-8000-0000000000fb",
		Now:         time.Now().UTC(),
	}); !errors.Is(err, store.ErrLastGlobalAdmin) {
		t.Fatalf("DeleteRoleBinding(sole admin, own) error = %v, want ErrLastGlobalAdmin", err)
	}

	// With a second administrator the count rule has nothing to say, and the
	// self rule becomes both applicable and accurate.
	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ('74000000-0000-4000-8000-0000000000e3', $1, 'admin', 'global')
`, customAdminUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := accessStore.DeleteRoleBinding(ctx, store.DeleteManagedRoleBindingParams{
		BindingID:   "74000000-0000-4000-8000-00000000000b",
		ActorUserID: builtinAdminUserID,
		RequestID:   "74000000-0000-4000-8000-000000000101",
		Now:         time.Now().UTC(),
	}); !errors.Is(err, store.ErrSelfUnbind) {
		t.Fatalf("DeleteRoleBinding(one of two admins, own) error = %v, want ErrSelfUnbind", err)
	}

	var bindings int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM role_bindings WHERE id IN ($1, $2)",
		"74000000-0000-4000-8000-00000000000b",
		"74000000-0000-4000-8000-00000000000c",
	).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 2 {
		t.Fatalf("role bindings remaining = %d, want 2", bindings)
	}
}

// Lockout must not turn an administrator into an ordinary account.
//
// "Global administrator" counts active accounts, which is right for "how many
// are left" and wrong for "whose account is this": five wrong passwords take the
// account off `active`, and a guard keyed on that set then waves through the
// takeover it exists to refuse.
func TestALockedGlobalAdministratorIsStillProtected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)
	now := time.Now().UTC()

	if _, err := pool.Exec(ctx, `
UPDATE users
SET status = 'locked', locked_at = now(), lock_expires_at = now() + interval '15 minutes'
WHERE id = $1
`, builtinAdminUserID); err != nil {
		t.Fatal(err)
	}

	if _, err := accessStore.ResetUserPassword(ctx, store.ResetManagedUserPasswordParams{
		UserID:       builtinAdminUserID,
		PasswordHash: "seized",
		ActorUserID:  customAdminUserID,
		RequestID:    "74000000-0000-4000-8000-0000000000fc",
		Now:          now,
	}); !errors.Is(err, store.ErrGlobalAdminRequired) {
		t.Fatalf("ResetUserPassword(locked admin) error = %v, want ErrGlobalAdminRequired", err)
	}

	// Unlocking is refused on the same grounds: it is what ends a run of
	// password attempts, so handing it to a non-administrator hands back the run.
	if _, err := accessStore.UnlockUser(ctx, store.UnlockManagedUserParams{
		UserID:      builtinAdminUserID,
		ActorUserID: customAdminUserID,
		RequestID:   "74000000-0000-4000-8000-0000000000fd",
		Now:         now,
	}); !errors.Is(err, store.ErrGlobalAdminRequired) {
		t.Fatalf("UnlockUser(locked admin) error = %v, want ErrGlobalAdminRequired", err)
	}

	if _, err := accessStore.DeleteUser(ctx, store.DeleteManagedUserParams{
		UserID:      builtinAdminUserID,
		ActorUserID: customAdminUserID,
		RequestID:   "74000000-0000-4000-8000-0000000000fe",
		Now:         now,
	}); !errors.Is(err, store.ErrGlobalAdminRequired) {
		t.Fatalf("DeleteUser(locked admin) error = %v, want ErrGlobalAdminRequired", err)
	}
}

// The count rule still has to let go of an administrator who is not holding the
// install up. A locked one is not in the active set, so removing them costs the
// count nothing and the last-administrator refusal must not fire.
func TestAGlobalAdministratorMayRemoveALockedOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)

	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ('74000000-0000-4000-8000-0000000000e2', $1, 'admin', 'global')
`, customAdminUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE users
SET status = 'locked', locked_at = now(), lock_expires_at = now() + interval '15 minutes'
WHERE id = $1
`, builtinAdminUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := accessStore.DeleteUser(ctx, store.DeleteManagedUserParams{
		UserID:      builtinAdminUserID,
		ActorUserID: customAdminUserID,
		RequestID:   "74000000-0000-4000-8000-0000000000ff",
		Now:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("DeleteUser(locked admin, by admin) error = %v, want nil", err)
	}
}

// Nothing here restricts ordinary accounts: the guard is about global
// administrators, not about every user a non-administrator might touch.
func TestNonAdministratorsAreNotProtectedByTheAdminGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)

	if _, err := accessStore.DeleteUser(ctx, store.DeleteManagedUserParams{
		UserID:      plainUserID,
		ActorUserID: customAdminUserID,
		RequestID:   "74000000-0000-4000-8000-0000000000f6",
		Now:         time.Now().UTC(),
	}); err != nil {
		t.Fatalf("DeleteUser(plain user) error = %v, want nil", err)
	}
}

func TestUserAdministrationCannotBypassThePermissionCeiling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	accessStore := store.NewAccessManagementStore(pool)
	now := time.Now().UTC()

	// The actor keeps user.manage but no longer holds the target's
	// super-operator permissions. User lifecycle endpoints must not become a
	// shorter way to seize or revoke that stronger identity.
	if _, err := pool.Exec(ctx, `
INSERT INTO roles (id, name, display_name, builtin, permissions)
VALUES (
    '74000000-0000-4000-8000-000000000110', 'user-helpdesk', '用户支持', false,
    ARRAY['user.manage']
)
`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES
    ('74000000-0000-4000-8000-000000000111', $1, 'user-helpdesk', 'global'),
    ('74000000-0000-4000-8000-000000000112', $2, 'super-operator', 'global')
`, customAdminUserID, plainUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"DELETE FROM role_bindings WHERE subject_id = $1 AND role = 'super-operator'",
		customAdminUserID,
	); err != nil {
		t.Fatal(err)
	}

	assertRefused := func(operation string, err error) {
		t.Helper()
		if !errors.Is(err, store.ErrTargetAuthorityExceeded) {
			t.Fatalf("%s error = %v, want ErrTargetAuthorityExceeded", operation, err)
		}
	}

	_, err := accessStore.ResetUserPassword(ctx, store.ResetManagedUserPasswordParams{
		UserID: plainUserID, PasswordHash: "seized", ActorUserID: customAdminUserID,
		RequestID: "74000000-0000-4000-8000-000000000113", Now: now,
	})
	assertRefused("ResetUserPassword", err)

	_, err = accessStore.SetUserStatus(ctx, store.SetManagedUserStatusParams{
		UserID: plainUserID, Status: "disabled", ActorUserID: customAdminUserID,
		RequestID: "74000000-0000-4000-8000-000000000114", Now: now,
	})
	assertRefused("SetUserStatus(disable)", err)

	_, err = accessStore.DeleteUser(ctx, store.DeleteManagedUserParams{
		UserID: plainUserID, ActorUserID: customAdminUserID,
		RequestID: "74000000-0000-4000-8000-000000000115", Now: now,
	})
	assertRefused("DeleteUser", err)

	if _, err := pool.Exec(ctx, `
UPDATE users
SET status = 'locked', locked_at = $2::timestamptz,
    lock_expires_at = $2::timestamptz + interval '15 minutes'
WHERE id = $1
`, plainUserID, now); err != nil {
		t.Fatal(err)
	}
	_, err = accessStore.UnlockUser(ctx, store.UnlockManagedUserParams{
		UserID: plainUserID, ActorUserID: customAdminUserID,
		RequestID: "74000000-0000-4000-8000-000000000116", Now: now,
	})
	assertRefused("UnlockUser", err)

	if _, err := pool.Exec(ctx, `
UPDATE users
SET status = 'disabled', locked_at = NULL, lock_expires_at = NULL
WHERE id = $1
`, plainUserID); err != nil {
		t.Fatal(err)
	}
	_, err = accessStore.SetUserStatus(ctx, store.SetManagedUserStatusParams{
		UserID: plainUserID, Status: "active", ActorUserID: customAdminUserID,
		RequestID: "74000000-0000-4000-8000-000000000117", Now: now,
	})
	assertRefused("SetUserStatus(enable)", err)

	var passwordHash string
	var status string
	if err := pool.QueryRow(ctx,
		"SELECT password_hash, status FROM users WHERE id = $1", plainUserID,
	).Scan(&passwordHash, &status); err != nil {
		t.Fatal(err)
	}
	if passwordHash != "not-used" || status != "disabled" {
		t.Fatalf("refused operations changed target: password_hash=%q status=%q", passwordHash, status)
	}
}

func TestConcurrentLoginFailuresPreserveAnActiveGlobalAdministrator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := globalAdminPool(t, ctx)
	authStore := store.NewAuthStore(pool)
	now := time.Now().UTC()

	if _, err := pool.Exec(ctx, `
INSERT INTO role_bindings (id, subject_id, role, scope_type)
VALUES ('74000000-0000-4000-8000-000000000120', $1, 'admin', 'global')
`, customAdminUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE users SET failed_login_count = 4 WHERE id IN ($1, $2)",
		customAdminUserID, builtinAdminUserID,
	); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	errorsByUser := make(chan error, 2)
	for index, userID := range []string{builtinAdminUserID, customAdminUserID} {
		wait.Add(1)
		go func(index int, userID string) {
			defer wait.Done()
			errorsByUser <- authStore.RecordLoginFailure(ctx, store.RecordLoginFailureParams{
				UserID:       &userID,
				RequestID:    fmt.Sprintf("74000000-0000-4000-8000-%012d", 121+index),
				Now:          now,
				MaxFailures:  5,
				LockDuration: 15 * time.Minute,
			})
		}(index, userID)
	}
	wait.Wait()
	close(errorsByUser)
	for err := range errorsByUser {
		if err != nil {
			t.Fatal(err)
		}
	}

	var activeAdministrators int
	if err := pool.QueryRow(ctx, `
SELECT count(DISTINCT users.id)
FROM users
JOIN role_bindings ON role_bindings.subject_id = users.id
WHERE users.status = 'active'
  AND role_bindings.role = 'admin'
  AND role_bindings.scope_type = 'global'
`).Scan(&activeAdministrators); err != nil {
		t.Fatal(err)
	}
	if activeAdministrators != 1 {
		t.Fatalf("active global administrators = %d, want 1", activeAdministrators)
	}
}
