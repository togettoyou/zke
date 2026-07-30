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
	PermissionClusterManage,
	PermissionClusterResourceCreate,
	PermissionClusterResourceUpdate,
	PermissionClusterResourceDelete,
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
		permissions := make([]Permission, 0, len(allPermissions))
		for _, permission := range allPermissions {
			if roleGrants(binding.Role, permission) {
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
		if !roleGrants(binding.Role, permission) {
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
		if roleGrants(binding.Role, permission) &&
			bindingApplies(binding, scope) {
			return nil
		}
	}
	return ErrDenied
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
