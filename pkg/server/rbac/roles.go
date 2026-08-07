package rbac

import "slices"

// Role names the two roles the Server defines for itself.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// BuiltinRole is a role the Server owns rather than an operator. It exists as a
// row like any other — `role_bindings.role` references one table, not two — but
// its permission set comes from here, and the API refuses to edit or delete it.
type BuiltinRole struct {
	// Fixed, and the same in every install. Nothing depends on the value —
	// reconciliation addresses these rows by name — but keeping it stable means
	// "the admin role" refers to one thing across installs, in a support
	// conversation and in an audit record alike.
	ID          string
	Name        string
	DisplayName string
	Description string
	Permissions []Permission
}

// Identifiers of the builtin rows. The schema does not seed them; this is the
// only place they are written, and startup reconciliation is what puts them in
// the database.
const (
	builtinAdminRoleID  = "00000000-0000-4000-8000-000000000001"
	builtinViewerRoleID = "00000000-0000-4000-8000-000000000002"
)

// BuiltinRoles reports the roles the Server ships, and why they stay in code.
//
// `admin` is defined as "every permission there is", which is a statement about
// the permission list rather than a list of its own. Materialising it into the
// database once — in a migration, say — would mean a permission added to the
// Server later reached nobody: `admin` would keep the set it was seeded with,
// and the omission would read as a denial with no error anywhere. Deriving it
// here and reconciling at startup keeps the definition true by construction.
//
// `viewer` is a fixed list because it is a claim about what "read-only" means,
// and a new permission is not automatically part of it. A new read permission
// belongs in this list only when someone decides it does.
func BuiltinRoles() []BuiltinRole {
	return []BuiltinRole{
		{
			ID:          builtinAdminRoleID,
			Name:        RoleAdmin,
			DisplayName: "管理员",
			Description: "平台内置角色，持有全部权限。权限集由 Server 在启动时对账，不可编辑。",
			Permissions: Permissions(),
		},
		{
			ID:          builtinViewerRoleID,
			Name:        RoleViewer,
			DisplayName: "只读用户",
			Description: "平台内置角色，只能查看租户、项目与集群，不能执行任何变更。",
			Permissions: []Permission{
				PermissionTenantRead,
				PermissionProjectRead,
				PermissionClusterRead,
			},
		},
	}
}

// IsBuiltinRole reports whether name is one of the roles the Server defines.
// Role management uses it to refuse edits and deletions rather than keeping a
// second list of protected names.
func IsBuiltinRole(name string) bool {
	return slices.ContainsFunc(BuiltinRoles(), func(role BuiltinRole) bool {
		return role.Name == name
	})
}

// Whether a binding's role carries a permission.
//
// The permission set arrives with the binding, read from the roles table by the
// same query, so this is a membership test rather than a lookup. A stored name
// the Server no longer defines simply never matches: removing a permission from
// the vocabulary withdraws it everywhere without a migration, which is the
// behaviour to want — the alternative is a role that keeps granting something
// the Server can no longer check.
func bindingGrants(permissions []string, permission Permission) bool {
	return slices.Contains(permissions, string(permission))
}
