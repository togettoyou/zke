package rbac

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

// Permissions the Server only ever checks at global scope.
//
// A RoleBinding may name any of the three scopes, and a role may carry any
// permission, so the two are free to be combined into a grant that can never be
// exercised: every route that checks one of these calls `RequireGlobal`, and a
// Tenant or Project binding never satisfies it. Bound anywhere but globally
// they are inert, and inert is the worst thing a permission can be — the
// operator who bound the builtin `admin` role to a Tenant reads a role that
// holds everything and gets an account that cannot manage a user, write a role,
// or rename the Tenant it was scoped to. Nothing refused it and nothing said so.
//
// Kept here rather than derived from the routes because there is nothing to
// derive it from: the scope lives in the middleware each route is constructed
// with, and Gin's router does not report it. `TestGlobalOnlyPermissionsMatchRoutes`
// states the same list against `routes.go` so the two cannot drift quietly.
//
// `tenant.create` and `tenant.manage` are here for different reasons than the
// rest. Creating a Tenant has no Tenant to be scoped to. Deleting one cascades
// through every Project, Cluster and credential beneath it, which is why it is
// deliberately not something a Tenant's own administrator can do.
var globalOnlyPermissions = map[Permission]struct{}{
	PermissionTenantCreate: {},
	PermissionTenantManage: {},
	PermissionUserRead:     {},
	PermissionUserManage:   {},
	PermissionRBACRead:     {},
	PermissionRBACManage:   {},
}

// GlobalOnly reports whether a permission is only ever enforced at global
// scope, so that a Tenant or Project binding carrying it grants nothing.
func GlobalOnly(permission Permission) bool {
	_, only := globalOnlyPermissions[permission]
	return only
}

var allPermissions = []Permission{
	PermissionTenantCreate,
	PermissionTenantRead,
	PermissionTenantManage,
	PermissionProjectCreate,
	PermissionProjectRead,
	PermissionProjectManage,
	PermissionClusterEnrollmentCreate,
	PermissionClusterEnrollmentRead,
	PermissionClusterEnrollmentRevoke,
	PermissionClusterRead,
	PermissionClusterPodLogsRead,
	PermissionClusterPodExec,
	PermissionClusterEventRead,
	PermissionClusterManage,
	PermissionClusterResourceCreate,
	PermissionClusterResourceUpdate,
	PermissionClusterResourceDelete,
	PermissionClusterRBACRead,
	PermissionClusterRBACManage,
	PermissionClusterSecretRead,
	PermissionClusterSecretManage,
	PermissionClusterConnectionRevoke,
	PermissionUserRead,
	PermissionUserManage,
	PermissionRBACRead,
	PermissionRBACManage,
	PermissionAuditRead,
}

var (
	ErrDenied            = errors.New("permission denied")
	ErrInvalidScope      = errors.New("invalid authorization scope")
	ErrUnknownPermission = errors.New("unknown permission")
)

type Service struct {
	store Store
}

func NewService(rbacStore Store) *Service {
	return &Service{store: rbacStore}
}

func (service *Service) ListCapabilities(
	ctx context.Context,
	userID string,
) ([]Capability, error) {
	if !validation.IsUUID(userID) {
		return nil, ErrDenied
	}
	bindings, err := service.listRoleBindings(ctx, userID)
	if err != nil {
		return nil, err
	}
	result := make([]Capability, 0, len(bindings))
	for _, binding := range bindings {
		// Filtered through allPermissions rather than returned as stored, so a
		// name the Server no longer defines does not reach a client as though
		// it were a capability. That is the same rule the authorization check
		// applies, stated once for the reporting path.
		//
		// Global-only permissions are dropped from a scoped binding for the same
		// reason. A capability is a claim about what the caller can do here, and
		// this endpoint is what a client builds its interface from — reporting
		// `user.manage` on a Tenant binding describes an operation that every
		// route would refuse, which is a worse answer than not mentioning it.
		scoped := binding.ScopeType != string(scopeGlobal)
		permissions := make([]Permission, 0, len(allPermissions))
		for _, permission := range allPermissions {
			if scoped && GlobalOnly(permission) {
				continue
			}
			if bindingGrants(binding.Permissions, permission) {
				permissions = append(permissions, permission)
			}
		}
		sort.Slice(permissions, func(left int, right int) bool {
			return permissions[left] < permissions[right]
		})
		result = append(result, Capability{
			Role:        binding.Role,
			ScopeType:   binding.ScopeType,
			TenantID:    binding.TenantID,
			ProjectID:   binding.ProjectID,
			Permissions: permissions,
		})
	}
	return result, nil
}

func (service *Service) ResolveVisibility(
	ctx context.Context,
	userID string,
	permission Permission,
) (Visibility, error) {
	if err := validateSubjectPermission(userID, permission); err != nil {
		return Visibility{}, err
	}
	bindings, err := service.listRoleBindings(ctx, userID)
	if err != nil {
		return Visibility{}, err
	}
	visibility := Visibility{
		tenantWide:  make(map[string]struct{}),
		projectOnly: make(map[string]string),
	}
	for _, binding := range bindings {
		if !bindingGrants(binding.Permissions, permission) {
			continue
		}
		switch binding.ScopeType {
		case string(scopeGlobal):
			visibility.global = true
		case string(scopeTenant):
			visibility.tenantWide[binding.TenantID] = struct{}{}
		case string(scopeProject):
			visibility.projectOnly[binding.ProjectID] = binding.TenantID
		}
	}
	return visibility, nil
}

func (service *Service) AuthorizeGlobal(
	ctx context.Context,
	userID string,
	permission Permission,
) error {
	return service.authorize(ctx, userID, permission, globalScope())
}

func (service *Service) AuthorizeTenant(
	ctx context.Context,
	userID string,
	permission Permission,
	tenantID string,
) error {
	return service.authorize(ctx, userID, permission, tenantScope(tenantID))
}

func (service *Service) AuthorizeProject(
	ctx context.Context,
	userID string,
	permission Permission,
	projectID string,
) (ResolvedScope, error) {
	if err := validateSubjectPermission(userID, permission); err != nil {
		return ResolvedScope{}, err
	}
	if !validation.IsUUID(projectID) {
		return ResolvedScope{}, ErrInvalidScope
	}
	tenantID, err := service.store.FindProjectTenant(ctx, projectID)
	if errors.Is(err, store.ErrProjectNotFound) {
		return ResolvedScope{}, ErrDenied
	}
	if err != nil {
		return ResolvedScope{}, err
	}
	resolved := ResolvedScope{TenantID: tenantID, ProjectID: projectID}
	return resolved, service.authorizeValidated(
		ctx, userID, permission, projectScope(tenantID, projectID),
	)
}

func (service *Service) AuthorizeCluster(
	ctx context.Context,
	userID string,
	permission Permission,
	clusterID string,
) (ResolvedScope, error) {
	if err := validateSubjectPermission(userID, permission); err != nil {
		return ResolvedScope{}, err
	}
	if !validation.IsUUID(clusterID) {
		return ResolvedScope{}, ErrInvalidScope
	}
	clusterScope, err := service.store.FindClusterAuthorizationScope(ctx, clusterID)
	if errors.Is(err, store.ErrClusterNotFound) {
		return ResolvedScope{}, ErrDenied
	}
	if err != nil {
		return ResolvedScope{}, err
	}
	resolved := ResolvedScope{
		TenantID:  clusterScope.TenantID,
		ProjectID: clusterScope.ProjectID,
	}
	return resolved, service.authorizeValidated(
		ctx,
		userID,
		permission,
		projectScope(clusterScope.TenantID, clusterScope.ProjectID),
	)
}

func (service *Service) authorize(
	ctx context.Context,
	userID string,
	permission Permission,
	scope scope,
) error {
	if err := validateSubjectPermission(userID, permission); err != nil {
		return err
	}
	if err := scope.validate(); err != nil {
		return err
	}
	return service.authorizeValidated(ctx, userID, permission, scope)
}

func (service *Service) authorizeValidated(
	ctx context.Context,
	userID string,
	permission Permission,
	scope scope,
) error {
	bindings, err := service.listRoleBindings(ctx, userID)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if bindingGrants(binding.Permissions, permission) &&
			bindingApplies(binding, scope) {
			return nil
		}
	}
	return ErrDenied
}

// GlobalPermissions reports the permissions a subject holds globally.
//
// Role management needs this and nothing else needs it: a role is a global
// object, so a permission put into one can be exercised anywhere it is later
// bound. Answering "what does this actor hold everywhere" is therefore the only
// honest ceiling to check a new role against — a permission the actor holds
// only inside one Project would otherwise become a permission they can hand
// themselves at global scope by writing it into a role.
//
// Only global bindings count. A Tenant-scoped grant is deliberately not enough:
// it would let a Tenant administrator mint a role carrying their permissions
// and have someone else bind it globally.
func (service *Service) GlobalPermissions(
	ctx context.Context,
	userID string,
) (map[Permission]struct{}, error) {
	if !validation.IsUUID(userID) {
		return nil, ErrDenied
	}
	bindings, err := service.listRoleBindings(ctx, userID)
	if err != nil {
		return nil, err
	}
	held := make(map[Permission]struct{}, len(allPermissions))
	for _, binding := range bindings {
		if binding.ScopeType != string(scopeGlobal) {
			continue
		}
		for _, permission := range allPermissions {
			if bindingGrants(binding.Permissions, permission) {
				held[permission] = struct{}{}
			}
		}
	}
	return held, nil
}

func validateSubjectPermission(userID string, permission Permission) error {
	if !validation.IsUUID(userID) {
		return ErrDenied
	}
	if !permission.valid() {
		return fmt.Errorf("%w: %q", ErrUnknownPermission, permission)
	}
	return nil
}

// Permissions reports the fixed permission set in a stable order. The slice is
// copied so that a caller cannot reorder the definition for everyone else.
//
// Exported because the permission names also reach the audit trail: an
// authorization denial is recorded under the permission it refused, so the
// published audit vocabulary has to be checked against this list.
func Permissions() []Permission {
	return slices.Clone(allPermissions)
}

// valid is derived from allPermissions rather than repeating it. The two used to
// be separate lists, and a permission added to the constants and to
// allPermissions but forgotten here would have been granted to nobody — `admin`
// grants "every valid permission", so the omission would have read as a denial
// with no error anywhere.
func (permission Permission) valid() bool {
	return slices.Contains(allPermissions, permission)
}

func (scope scope) validate() error {
	switch scope.Type {
	case scopeGlobal:
		if scope.TenantID == "" && scope.ProjectID == "" {
			return nil
		}
	case scopeTenant:
		if validation.IsUUID(scope.TenantID) && scope.ProjectID == "" {
			return nil
		}
	case scopeProject:
		if validation.IsUUID(scope.TenantID) && validation.IsUUID(scope.ProjectID) {
			return nil
		}
	}
	return ErrInvalidScope
}

func bindingApplies(binding store.RoleBinding, scope scope) bool {
	switch binding.ScopeType {
	case string(scopeGlobal):
		return true
	case string(scopeTenant):
		return scope.Type != scopeGlobal &&
			binding.TenantID == scope.TenantID
	case string(scopeProject):
		return scope.Type == scopeProject &&
			binding.TenantID == scope.TenantID &&
			binding.ProjectID == scope.ProjectID
	default:
		return false
	}
}
