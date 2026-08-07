package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

// applyMigrations prepares a test database the way the Server prepares a real
// one: the schema, and then the roles the Server defines.
//
// Both steps are needed because the schema seeds no roles. `admin` means "every
// permission the Server knows", which is a statement about the permission list
// rather than a list of its own, so it is written by the Server at startup
// instead of being copied into SQL that would go stale. A test that applied only
// the migration would have a database in which no RoleBinding can exist —
// `role_bindings.role` references this table.
//
// Mirroring startup here rather than seeding the two rows by hand keeps the tests
// honest about what a running install actually contains.
func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := store.NewAccessManagementStore(pool).EnsureBuiltinRoles(
		ctx,
		builtinRoleDefinitions(),
		time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
}

func builtinRoleDefinitions() []store.BuiltinRoleDefinition {
	builtin := rbac.BuiltinRoles()
	definitions := make([]store.BuiltinRoleDefinition, 0, len(builtin))
	for _, role := range builtin {
		permissions := make([]string, 0, len(role.Permissions))
		for _, permission := range role.Permissions {
			permissions = append(permissions, string(permission))
		}
		definitions = append(definitions, store.BuiltinRoleDefinition{
			ID:          role.ID,
			Name:        role.Name,
			DisplayName: role.DisplayName,
			Description: role.Description,
			Permissions: permissions,
		})
	}
	return definitions
}
