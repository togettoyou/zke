package rbac

import (
	"context"
	"errors"
	"fmt"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

var (
	ErrDenied            = errors.New("permission denied")
	ErrInvalidScope      = errors.New("invalid authorization scope")
	ErrUnknownPermission = errors.New("unknown permission")
)

type Service struct {
	store *store.RBACStore
}

func NewService(rbacStore *store.RBACStore) *Service {
	return &Service{store: rbacStore}
}

func (service *Service) ResolveVisibility(
	ctx context.Context,
	userID string,
	permission Permission,
) (Visibility, error) {
	if err := validateSubjectPermission(userID, permission); err != nil {
		return Visibility{}, err
	}
	bindings, err := service.store.ListRoleBindings(ctx, userID)
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
) error {
	if err := validateSubjectPermission(userID, permission); err != nil {
		return err
	}
	if !validation.IsUUID(projectID) {
		return ErrInvalidScope
	}
	tenantID, err := service.store.FindProjectTenant(ctx, projectID)
	if errors.Is(err, store.ErrProjectNotFound) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	return service.authorizeValidated(
		ctx, userID, permission, projectScope(tenantID, projectID),
	)
}

func (service *Service) AuthorizeAgent(
	ctx context.Context,
	userID string,
	permission Permission,
	agentID string,
) (store.AgentAuthorizationScope, error) {
	if err := validateSubjectPermission(userID, permission); err != nil {
		return store.AgentAuthorizationScope{}, err
	}
	if !validation.IsUUID(agentID) {
		return store.AgentAuthorizationScope{}, ErrInvalidScope
	}
	agentScope, err := service.store.FindAgentAuthorizationScope(ctx, agentID)
	if errors.Is(err, store.ErrAgentNotFound) {
		return store.AgentAuthorizationScope{}, ErrDenied
	}
	if err != nil {
		return store.AgentAuthorizationScope{}, err
	}
	err = service.authorizeValidated(
		ctx,
		userID,
		permission,
		projectScope(agentScope.TenantID, agentScope.ProjectID),
	)
	return agentScope, err
}

func (service *Service) AuthorizeCluster(
	ctx context.Context,
	userID string,
	permission Permission,
	clusterID string,
) (store.ClusterAuthorizationScope, error) {
	if err := validateSubjectPermission(userID, permission); err != nil {
		return store.ClusterAuthorizationScope{}, err
	}
	if !validation.IsUUID(clusterID) {
		return store.ClusterAuthorizationScope{}, ErrInvalidScope
	}
	clusterScope, err := service.store.FindClusterAuthorizationScope(ctx, clusterID)
	if errors.Is(err, store.ErrClusterNotFound) {
		return store.ClusterAuthorizationScope{}, ErrDenied
	}
	if err != nil {
		return store.ClusterAuthorizationScope{}, err
	}
	err = service.authorizeValidated(
		ctx,
		userID,
		permission,
		projectScope(clusterScope.TenantID, clusterScope.ProjectID),
	)
	return clusterScope, err
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
	bindings, err := service.store.ListRoleBindings(ctx, userID)
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

func (permission Permission) valid() bool {
	switch permission {
	case PermissionTenantCreate,
		PermissionProjectCreate,
		PermissionAgentEnrollmentCreate,
		PermissionClusterRead,
		PermissionAgentRead,
		PermissionAgentRevoke,
		PermissionUserRead,
		PermissionUserManage,
		PermissionRBACRead,
		PermissionRBACManage,
		PermissionAuditRead:
		return true
	default:
		return false
	}
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

func roleGrants(role string, permission Permission) bool {
	switch role {
	case "admin":
		return true
	case "viewer":
		return permission == PermissionClusterRead ||
			permission == PermissionAgentRead
	default:
		return false
	}
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
