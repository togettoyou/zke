package accessmanagement

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/identifier"
	"github.com/togettoyou/zke/pkg/shared/pagination"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

const (
	maxRoleDescriptionBytes = 1024
	maxRoleNameBytes        = 63
)

var (
	// ErrRoleBuiltin reports an edit to a role the Server defines. It is not a
	// permission error: the caller may hold every permission there is, and these
	// two roles are still not theirs to change.
	ErrRoleBuiltin = errors.New("builtin role cannot be changed")
	// ErrRoleInUse reports a deletion refused because somebody still holds the
	// role. Unbinding first is deliberate: it makes losing access an act with a
	// subject rather than a side effect of tidying up a list of roles.
	ErrRoleInUse = errors.New("role is still bound to a subject")
	// ErrPermissionEscalation reports a role carrying more than its author does.
	ErrPermissionEscalation = errors.New("role grants permissions the actor does not hold")
)

// roleNamePattern matches the stable identifier a binding stores. Lowercase
// because the name appears in audit rows and URLs where case would be a way for
// two roles to look like one.
var roleNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type Role struct {
	ID           string
	Name         string
	DisplayName  string
	Description  string
	Builtin      bool
	Permissions  []string
	BindingCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type ListRolesInput struct {
	Builtin string
	Search  string
	Page    pagination.Request
}

type RolePage struct {
	Roles []Role
	Page  pagination.Result
}

type CreateRoleInput struct {
	Name        string
	DisplayName string
	Description string
	Permissions []string
	Confirm     bool
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type UpdateRoleInput struct {
	RoleID      string
	DisplayName string
	Description string
	Permissions []string
	Confirm     bool
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type DeleteRoleInput struct {
	RoleID      string
	Confirm     bool
	ActorUserID string
	RequestID   string
	Now         time.Time
}

// PermissionAuthority answers what the actor holds. It is the authorization
// service, narrowed to the one question role management asks, so that this
// package depends on the check rather than on the whole of authorization.
type PermissionAuthority interface {
	GlobalPermissions(ctx context.Context, userID string) (map[rbac.Permission]struct{}, error)
}

// WithPermissionAuthority supplies the actor's permission ceiling. Role
// management refuses every write without it rather than skipping the check: a
// missing dependency must not read as an absent restriction.
func (service *Service) WithPermissionAuthority(
	authority PermissionAuthority,
) *Service {
	service.permissions = authority
	return service
}

func (service *Service) ListRoles(
	ctx context.Context,
	input ListRolesInput,
) (RolePage, error) {
	if input.Page.Validate() != nil ||
		!allowedValue(input.Builtin, "true", "false") {
		return RolePage{}, ErrInvalidInput
	}
	stored, total, err := service.store.ListRoles(ctx, store.ListManagedRolesParams{
		Builtin: input.Builtin,
		Search:  strings.ToLower(strings.TrimSpace(input.Search)),
		Page:    input.Page,
	})
	if err != nil {
		return RolePage{}, err
	}
	result := make([]Role, 0, len(stored))
	for _, item := range stored {
		result = append(result, roleFromStore(item))
	}
	return RolePage{
		Roles: result,
		Page:  pagination.NewResult(input.Page, total, len(result)),
	}, nil
}

func (service *Service) GetRole(
	ctx context.Context,
	roleID string,
) (Role, error) {
	if !validation.IsUUID(roleID) {
		return Role{}, ErrInvalidInput
	}
	item, err := service.store.GetRole(ctx, roleID)
	if errors.Is(err, store.ErrRoleNotFound) {
		return Role{}, ErrNotFound
	}
	if err != nil {
		return Role{}, err
	}
	return roleFromStore(item), nil
}

func (service *Service) CreateRole(
	ctx context.Context,
	input CreateRoleInput,
) (Role, error) {
	permissions, err := normalizedPermissions(input.Permissions)
	if err != nil {
		return Role{}, err
	}
	if !validRoleName(input.Name) ||
		rbac.IsBuiltinRole(input.Name) ||
		!validRoleText(input.DisplayName, maxDisplayNameBytes, true) ||
		!validRoleText(input.Description, maxRoleDescriptionBytes, false) ||
		!input.Confirm ||
		!validActorRequest(input.ActorUserID, input.RequestID, input.Now) {
		return Role{}, ErrInvalidInput
	}
	if err := service.ensureWithinActorCeiling(
		ctx, input.ActorUserID, permissions,
	); err != nil {
		return Role{}, err
	}
	id, err := identifier.NewUUID()
	if err != nil {
		return Role{}, err
	}
	item, err := service.store.CreateRole(ctx, store.CreateManagedRoleParams{
		ID:          id,
		Name:        input.Name,
		DisplayName: input.DisplayName,
		Description: input.Description,
		Permissions: permissions,
		ActorUserID: input.ActorUserID,
		RequestID:   input.RequestID,
		Now:         input.Now,
	})
	if err != nil {
		return Role{}, roleWriteError(err)
	}
	return roleFromStore(item), nil
}

func (service *Service) UpdateRole(
	ctx context.Context,
	input UpdateRoleInput,
) (Role, error) {
	permissions, err := normalizedPermissions(input.Permissions)
	if err != nil {
		return Role{}, err
	}
	if !validation.IsUUID(input.RoleID) ||
		!validRoleText(input.DisplayName, maxDisplayNameBytes, true) ||
		!validRoleText(input.Description, maxRoleDescriptionBytes, false) ||
		!input.Confirm ||
		!validActorRequest(input.ActorUserID, input.RequestID, input.Now) {
		return Role{}, ErrInvalidInput
	}
	if err := service.ensureWithinActorCeiling(
		ctx, input.ActorUserID, permissions,
	); err != nil {
		return Role{}, err
	}
	item, err := service.store.UpdateRole(ctx, store.UpdateManagedRoleParams{
		RoleID:      input.RoleID,
		DisplayName: input.DisplayName,
		Description: input.Description,
		Permissions: permissions,
		ActorUserID: input.ActorUserID,
		RequestID:   input.RequestID,
		Now:         input.Now,
	})
	if err != nil {
		return Role{}, roleWriteError(err)
	}
	return roleFromStore(item), nil
}

func (service *Service) DeleteRole(
	ctx context.Context,
	input DeleteRoleInput,
) (Role, error) {
	if !validation.IsUUID(input.RoleID) ||
		!input.Confirm ||
		!validActorRequest(input.ActorUserID, input.RequestID, input.Now) {
		return Role{}, ErrInvalidInput
	}
	item, err := service.store.DeleteRole(ctx, store.DeleteManagedRoleParams{
		RoleID:      input.RoleID,
		ActorUserID: input.ActorUserID,
		RequestID:   input.RequestID,
		Now:         input.Now,
	})
	if err != nil {
		return Role{}, roleWriteError(err)
	}
	return roleFromStore(item), nil
}

/*
 * The ceiling a role may not exceed.
 *
 * Without this, `rbac.manage` would be the only permission anyone needs: hold
 * it, write a role carrying `cluster.secret.read`, bind it to yourself, and the
 * separation between reading configuration and reading credentials is gone.
 * Every permission the platform defines would collapse into one.
 *
 * The ceiling is what the actor holds *globally*, not what they hold where they
 * happen to be working. A role is a global object — it can be bound anywhere
 * later — so a permission the actor only holds inside one Project would
 * otherwise become one they can exercise everywhere by routing it through a
 * role. Kubernetes solves the same problem the same way, and calls the way
 * around it `escalate`; ZKE has no equivalent, which is deliberate.
 *
 * A missing authority is a refusal, not a bypass: this is the only thing
 * standing between `rbac.manage` and every other permission.
 */
func (service *Service) ensureWithinActorCeiling(
	ctx context.Context,
	actorUserID string,
	permissions []string,
) error {
	if service.permissions == nil {
		return errors.New("role management has no permission authority configured")
	}
	held, err := service.permissions.GlobalPermissions(ctx, actorUserID)
	if err != nil {
		return err
	}
	exceeded := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		if _, granted := held[rbac.Permission(permission)]; !granted {
			exceeded = append(exceeded, permission)
		}
	}
	if len(exceeded) == 0 {
		return nil
	}
	// The refused permissions are named because the alternative is an operator
	// re-submitting the same role one permission at a time to find out which.
	// Naming them reveals nothing: the caller already sent this list.
	return &escalationError{permissions: exceeded}
}

// escalationError reports which permissions exceeded the actor's ceiling.
type escalationError struct {
	permissions []string
}

func (err *escalationError) Error() string {
	return ErrPermissionEscalation.Error() + ": " + strings.Join(err.permissions, ", ")
}

func (err *escalationError) Is(target error) bool {
	return target == ErrPermissionEscalation
}

// Detail is what the HTTP layer returns in place of the fixed message. It
// describes what the caller sent and nothing about the Server.
func (err *escalationError) Detail() string {
	return "角色包含调用者未持有的权限：" + strings.Join(err.permissions, "、")
}

/*
 * Validates the submitted permission names and puts them in a stable order.
 *
 * Unknown names are refused rather than dropped. A role stored with a permission
 * the Server does not define would grant nothing, and silently accepting one is
 * how an operator ends up with a role that reads correctly in the Console and
 * denies everything in practice.
 */
func normalizedPermissions(permissions []string) ([]string, error) {
	if len(permissions) == 0 || len(permissions) > len(rbac.Permissions()) {
		return nil, ErrInvalidInput
	}
	known := rbac.Permissions()
	seen := make(map[string]struct{}, len(permissions))
	result := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		if !slices.Contains(known, rbac.Permission(permission)) {
			return nil, ErrInvalidInput
		}
		if _, duplicate := seen[permission]; duplicate {
			continue
		}
		seen[permission] = struct{}{}
		result = append(result, permission)
	}
	sort.Strings(result)
	return result, nil
}

func validRoleName(name string) bool {
	return len(name) <= maxRoleNameBytes && roleNamePattern.MatchString(name)
}

func validRoleText(value string, maximum int, required bool) bool {
	if strings.TrimSpace(value) != value || len(value) > maximum {
		return false
	}
	return !required || value != ""
}

func roleWriteError(err error) error {
	switch {
	case errors.Is(err, store.ErrRoleNotFound):
		return ErrNotFound
	case errors.Is(err, store.ErrRoleConflict):
		return ErrConflict
	case errors.Is(err, store.ErrRoleBuiltin):
		return ErrRoleBuiltin
	case errors.Is(err, store.ErrRoleInUse):
		return ErrRoleInUse
	case errors.Is(err, store.ErrLastGlobalAdmin):
		return ErrLastAdmin
	case errors.Is(err, store.ErrAccessStateConflict):
		return ErrConflict
	default:
		return err
	}
}

func roleFromStore(item store.ManagedRole) Role {
	return Role{
		ID:           item.ID,
		Name:         item.Name,
		DisplayName:  item.DisplayName,
		Description:  item.Description,
		Builtin:      item.Builtin,
		Permissions:  slices.Clone(item.Permissions),
		BindingCount: item.BindingCount,
		CreatedAt:    item.CreatedAt,
		UpdatedAt:    item.UpdatedAt,
	}
}
