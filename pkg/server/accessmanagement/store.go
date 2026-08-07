package accessmanagement

import (
	"context"

	"github.com/togettoyou/zke/pkg/server/store"
)

// Store is the persistence surface access management needs. Depending on an
// interface rather than the concrete PostgreSQL store lets the service's
// validation, self-disable guard and last-administrator rules be unit tested
// without a database.
type Store interface {
	ListUsers(ctx context.Context, params store.ListManagedUsersParams) ([]store.ManagedUser, int, error)
	GetUser(ctx context.Context, userID string) (store.ManagedUser, error)
	CreateUser(ctx context.Context, input store.CreateManagedUserParams) (store.ManagedUser, error)
	UpdateUser(ctx context.Context, input store.UpdateManagedUserParams) (store.ManagedUser, error)
	DeleteUser(ctx context.Context, input store.DeleteManagedUserParams) (store.ManagedUser, error)
	SetUserStatus(ctx context.Context, input store.SetManagedUserStatusParams) (store.ManagedUser, error)
	UnlockUser(ctx context.Context, input store.UnlockManagedUserParams) (store.ManagedUser, error)
	ResetUserPassword(ctx context.Context, input store.ResetManagedUserPasswordParams) (store.ManagedUser, error)
	ListRoles(ctx context.Context, params store.ListManagedRolesParams) ([]store.ManagedRole, int, error)
	GetRole(ctx context.Context, roleID string) (store.ManagedRole, error)
	FindRoleByName(ctx context.Context, name string) (store.ManagedRole, error)
	CreateRole(ctx context.Context, input store.CreateManagedRoleParams) (store.ManagedRole, error)
	UpdateRole(ctx context.Context, input store.UpdateManagedRoleParams) (store.ManagedRole, error)
	DeleteRole(ctx context.Context, input store.DeleteManagedRoleParams) (store.ManagedRole, error)
	ListRoleBindings(ctx context.Context, params store.ListManagedRoleBindingsParams) ([]store.ManagedRoleBinding, int, error)
	GetRoleBinding(ctx context.Context, bindingID string) (store.ManagedRoleBinding, error)
	CreateRoleBinding(ctx context.Context, input store.CreateManagedRoleBindingParams) (store.ManagedRoleBinding, bool, error)
	DeleteRoleBinding(ctx context.Context, input store.DeleteManagedRoleBindingParams) (store.ManagedRoleBinding, error)
}
