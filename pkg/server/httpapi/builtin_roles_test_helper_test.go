package httpapi

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
// The schema seeds no roles — `admin` means "every permission the Server knows",
// which only the Server can state — so a test that applied only the migration
// would have a database in which no RoleBinding can exist, and the initial
// administrator could not be bootstrapped.
func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
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
	if err := store.NewAccessManagementStore(pool).EnsureBuiltinRoles(
		ctx,
		definitions,
		time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
}
