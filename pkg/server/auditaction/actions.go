// Package auditaction is the single definition of the audit action vocabulary.
//
// It is a leaf package on purpose. The names are written by the store, quoted by
// the HTTP handlers and offered to the Console as a filter; anything that has to
// agree on them can import this without pulling in the rest of the server, and
// without the import cycle that would follow from putting them in the audit
// service, which itself depends on the store.
//
// Two vocabularies are easy to confuse and must not be merged. A *permission*
// (`pkg/server/rbac`) says what an operator may do; an *action* says what
// happened. They overlap in three names and diverge everywhere else — one
// `user.manage` permission produces six distinct user actions, and `auth.login`
// corresponds to no permission at all.
package auditaction

import "slices"

// Actions written by the Server. Keep this list and the literals that produce
// them in step: several are embedded in the store's SQL rather than passed as
// Go values, so the compiler cannot check them. `TestPhase1BackendEndToEnd`
// asserts that every action an exercised flow actually writes is listed here.
const (
	AuthLogin               = "auth.login"
	AuthLogout              = "auth.logout"
	AuthPasswordChange      = "auth.password.change"
	AuthAccountLock         = "auth.account.lock"
	AuthAccountLockWithheld = "auth.account.lock_withheld"
	AuthAccountAutoUnlock   = "auth.account.auto_unlock"
	AuthInitialAdminCreate  = "auth.initial_admin.create"

	UserCreate        = "user.create"
	UserUpdate        = "user.update"
	UserDelete        = "user.delete"
	UserStatusUpdate  = "user.status.update"
	UserUnlock        = "user.unlock"
	UserPasswordReset = "user.password.reset"

	RoleBindingCreate = "role_binding.create"
	RoleBindingDelete = "role_binding.delete"

	TenantCreate = "tenant.create"
	TenantUpdate = "tenant.update"
	TenantDelete = "tenant.delete"

	ProjectCreate = "project.create"
	ProjectUpdate = "project.update"
	ProjectDelete = "project.delete"

	ClusterEnroll             = "cluster.enroll"
	ClusterUpdate             = "cluster.update"
	ClusterDelete             = "cluster.delete"
	ClusterEnrollmentCreate   = "cluster.enrollment.create"
	ClusterEnrollmentRevoke   = "cluster.enrollment.revoke"
	ClusterConnectionRevoke   = "cluster.connection.revoke"
	ClusterConnectionReenroll = "cluster.connection.reenroll"
)

// Group names the family an action belongs to. It is declared here rather than
// derived from the name's first segment because the segments are not a reliable
// tree: `cluster.enrollment.create` and `cluster.delete` belong to the same
// family at different depths, and a client splitting on dots would have to guess
// which prefix is the group.
const (
	GroupAuth        = "auth"
	GroupUser        = "user"
	GroupRoleBinding = "role_binding"
	GroupTenant      = "tenant"
	GroupProject     = "project"
	GroupCluster     = "cluster"
)

// Action is one entry of the vocabulary.
type Action struct {
	Name  string
	Group string
}

// Ordered by group, then by the order an operator meets them: create before
// update before delete, so the list reads as a lifecycle rather than as an
// alphabetisation of it.
var actions = []Action{
	{AuthLogin, GroupAuth},
	{AuthLogout, GroupAuth},
	{AuthPasswordChange, GroupAuth},
	{AuthAccountLock, GroupAuth},
	{AuthAccountLockWithheld, GroupAuth},
	{AuthAccountAutoUnlock, GroupAuth},
	{AuthInitialAdminCreate, GroupAuth},

	{UserCreate, GroupUser},
	{UserUpdate, GroupUser},
	{UserStatusUpdate, GroupUser},
	{UserUnlock, GroupUser},
	{UserPasswordReset, GroupUser},
	{UserDelete, GroupUser},

	{RoleBindingCreate, GroupRoleBinding},
	{RoleBindingDelete, GroupRoleBinding},

	{TenantCreate, GroupTenant},
	{TenantUpdate, GroupTenant},
	{TenantDelete, GroupTenant},

	{ProjectCreate, GroupProject},
	{ProjectUpdate, GroupProject},
	{ProjectDelete, GroupProject},

	{ClusterEnroll, GroupCluster},
	{ClusterUpdate, GroupCluster},
	{ClusterDelete, GroupCluster},
	{ClusterEnrollmentCreate, GroupCluster},
	{ClusterEnrollmentRevoke, GroupCluster},
	{ClusterConnectionRevoke, GroupCluster},
	{ClusterConnectionReenroll, GroupCluster},
}

// All reports the vocabulary in presentation order. The slice is copied so that
// a caller cannot reorder the definition for everyone else.
func All() []Action {
	return slices.Clone(actions)
}

// Known reports whether name is part of the vocabulary.
func Known(name string) bool {
	return slices.ContainsFunc(actions, func(action Action) bool {
		return action.Name == name
	})
}
