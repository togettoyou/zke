package rbac

import "slices"

// Role names the fixed authorization roles Phase 1 ships.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// roleMatrix is the single definition of what each role grants. A nil
// permission set means the role grants every fixed permission.
//
// Access management validates submitted roles against this table rather than
// repeating the role names, so the management API cannot accept a role that
// authorization does not yet know how to evaluate.
var roleMatrix = map[string][]Permission{
	RoleAdmin: nil,
	RoleViewer: {
		PermissionTenantRead,
		PermissionProjectRead,
		PermissionClusterRead,
	},
}

// Roles reports the fixed role names in a stable order.
func Roles() []string {
	return []string{RoleAdmin, RoleViewer}
}

// RoleExists reports whether role is one of the fixed roles.
func RoleExists(role string) bool {
	_, exists := roleMatrix[role]
	return exists
}

func roleGrants(role string, permission Permission) bool {
	granted, exists := roleMatrix[role]
	if !exists {
		return false
	}
	if granted == nil {
		return permission.valid()
	}
	return slices.Contains(granted, permission)
}
