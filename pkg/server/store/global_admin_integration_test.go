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
	builtinAdminUserID = "74000000-0000-4000-8000-000000000001"
	customAdminUserID  = "74000000-0000-4000-8000-000000000002"
	plainUserID        = "74000000-0000-4000-8000-000000000003"
)

/*
 * The global administrator is the account of last resort.
 *
 * "Global administrator" means the builtin `admin` role bound at global scope —
 * not "holds an equivalent set of permissions". A custom role can be written by
 * anyone with `rbac.manage`, so letting one stand in for the builtin role would
 * make the account of last resort removable by an account somebody else defined.
 */
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

/*
 * The bypass that would make the rule theatre.
 *
 * A custom role holding every permission satisfies the escalation ceiling for
 * `admin`, so without this its holder could bind `admin` to themselves, become a
 * global administrator, and then remove the original.
 */
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
