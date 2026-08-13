package server

import (
	"context"
	"time"

	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
)

// builtinRoleStore is the one thing reconciliation needs. Narrowing it to a
// single method keeps the startup step testable without a database and makes
// the write it performs obvious from the signature.
type builtinRoleStore interface {
	EnsureBuiltinRoles(
		ctx context.Context,
		roles []store.BuiltinRoleDefinition,
		now time.Time,
	) error
}

// Brings the roles the Server defines into line with the database.
//
// Roles are rows, and these two are the rows the Server owns. The schema seeds
// nothing, so this is what puts them there: `admin` is defined as every
// permission the Server knows, and writing that into a migration would fix the
// set at the moment the migration was written. The permission added afterwards
// would be granted to nobody — indistinguishable, from the outside, from a
// permission that denies everyone.
//
// Runs before the setup API can create the first administrator, because that
// transaction binds a user to `admin` and the foreign key requires the row.
func reconcileBuiltinRoles(
	ctx context.Context,
	roleStore builtinRoleStore,
	now time.Time,
) error {
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
	return roleStore.EnsureBuiltinRoles(ctx, definitions, now)
}
