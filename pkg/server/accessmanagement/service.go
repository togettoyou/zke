package accessmanagement

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/identifier"
	"github.com/togettoyou/zke/pkg/shared/pagination"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

const maxDisplayNameBytes = 253

var (
	ErrInvalidInput = errors.New("invalid access management input")
	ErrNotFound     = errors.New("access management target not found")
	ErrConflict     = errors.New("access management conflict")
	ErrLastAdmin    = errors.New("last global administrator")
	// Removing or granting the global administrator role is reserved to the
	// people who already hold it. Separate from ErrLastAdmin: the platform would
	// survive the change, and what is refused is who asked for it.
	ErrGlobalAdminRequired = errors.New("only a global administrator may do this")
	ErrSelfDisable         = errors.New("cannot disable the authenticated user")
	// Deleting yourself and disabling yourself are both refused, but they are
	// not the same refusal: sharing one sentinel meant a rejected deletion was
	// reported to the operator as a rejected disable, describing an operation
	// they had not asked for.
	ErrSelfDelete = errors.New("cannot delete the authenticated user")
)

type Config struct {
	MaxConcurrentPasswordHashes int
}

type Service struct {
	store          Store
	passwordHashes chan struct{}
	// Supplied by WithPermissionAuthority. Role writes refuse to proceed
	// without it, because it is what keeps `rbac.manage` from being a way to
	// grant yourself every other permission.
	permissions PermissionAuthority
}

type User struct {
	ID                string
	Username          string
	DisplayName       string
	Status            string
	FailedLoginCount  int
	LockedAt          *time.Time
	LockExpiresAt     *time.Time
	PasswordChangedAt time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ListUsersInput selects one page of managed users.
type ListUsersInput struct {
	Status string
	Search string
	// Reads carry a clock for the same reason writes do: the lock state they
	// report is relative to a moment, and the caller owns which one.
	Now  time.Time
	Page pagination.Request
}

// UserPage is one page of managed users plus where it sits in the full set.
type UserPage struct {
	Users []User
	Page  pagination.Result
}

// ListRoleBindingsInput selects one page of role bindings.
type ListRoleBindingsInput struct {
	Role      string
	ScopeType string
	Search    string
	Page      pagination.Request
}

// RoleBindingPage is one page of role bindings plus its position.
type RoleBindingPage struct {
	RoleBindings []RoleBinding
	Page         pagination.Result
}

type CreateUserInput struct {
	Username    string
	DisplayName string
	Password    []byte
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type SetUserStatusInput struct {
	UserID      string
	Status      string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type UpdateUserInput struct {
	UserID      string
	DisplayName string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type DeleteUserInput struct {
	UserID      string
	Confirm     bool
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type ConfirmUserActionInput struct {
	UserID      string
	Confirm     bool
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type ResetPasswordInput struct {
	UserID      string
	Password    []byte
	Confirm     bool
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type RoleBinding struct {
	ID        string
	SubjectID string
	Role      string
	ScopeType string
	TenantID  string
	ProjectID string
	CreatedAt time.Time
	// The subject's own names, resolved alongside the binding. Empty when the
	// subject row is gone; the binding is still reported so it can be removed.
	SubjectUsername    string
	SubjectDisplayName string
}

type CreateRoleBindingInput struct {
	SubjectID   string
	Role        string
	ScopeType   string
	TenantID    string
	ProjectID   string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type CreateRoleBindingResult struct {
	RoleBinding
	Replayed bool
}

type DeleteRoleBindingInput struct {
	BindingID   string
	Confirm     bool
	ActorUserID string
	RequestID   string
	Now         time.Time
}

func NewService(
	accessStore Store,
	config Config,
) *Service {
	maxHashes := max(1, config.MaxConcurrentPasswordHashes)
	return &Service{
		store:          accessStore,
		passwordHashes: make(chan struct{}, maxHashes),
	}
}

// ListUsers returns one page of managed users. Filtering, searching and
// paging all happen in the database so that the reported total describes the
// whole filtered set rather than whatever fit in memory.
func (service *Service) ListUsers(
	ctx context.Context,
	input ListUsersInput,
) (UserPage, error) {
	if input.Page.Validate() != nil ||
		!allowedValue(input.Status, "active", "locked", "disabled") {
		return UserPage{}, ErrInvalidInput
	}
	stored, total, err := service.store.ListUsers(ctx, store.ListManagedUsersParams{
		Status: input.Status,
		Search: normalizeSearch(input.Search),
		Now:    input.Now,
		Page:   input.Page,
	})
	if err != nil {
		return UserPage{}, err
	}
	result := make([]User, 0, len(stored))
	for _, item := range stored {
		result = append(result, userFromStore(item, input.Now))
	}
	return UserPage{
		Users: result,
		Page:  pagination.NewResult(input.Page, total, len(result)),
	}, nil
}

func (service *Service) GetUser(
	ctx context.Context,
	userID string,
	now time.Time,
) (User, error) {
	if !validation.IsUUID(userID) {
		return User{}, ErrInvalidInput
	}
	item, err := service.store.GetUser(ctx, userID)
	if errors.Is(err, store.ErrAccessUserNotFound) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return userFromStore(item, now), nil
}

func (service *Service) CreateUser(
	ctx context.Context,
	input CreateUserInput,
) (User, error) {
	username, err := auth.NormalizeUsername(input.Username)
	if err != nil ||
		!validDisplayName(input.DisplayName) ||
		!validActorRequest(input.ActorUserID, input.RequestID, input.Now) {
		return User{}, ErrInvalidInput
	}
	passwordHash, err := service.hashPassword(ctx, input.Password)
	if err != nil {
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return User{}, err
		}
		return User{}, ErrInvalidInput
	}
	id, err := identifier.NewUUID()
	if err != nil {
		return User{}, err
	}
	item, err := service.store.CreateUser(ctx, store.CreateManagedUserParams{
		ID:           id,
		Username:     username,
		DisplayName:  input.DisplayName,
		PasswordHash: passwordHash,
		ActorUserID:  input.ActorUserID,
		RequestID:    input.RequestID,
		Now:          input.Now,
	})
	switch {
	case errors.Is(err, store.ErrAccessUserConflict):
		return User{}, ErrConflict
	case errors.Is(err, store.ErrAccessStateConflict):
		return User{}, ErrConflict
	case err != nil:
		return User{}, err
	default:
		return userFromStore(item, input.Now), nil
	}
}

func (service *Service) UpdateUser(
	ctx context.Context,
	input UpdateUserInput,
) (User, error) {
	if !validation.IsUUID(input.UserID) ||
		!validDisplayName(input.DisplayName) ||
		!validActorRequest(input.ActorUserID, input.RequestID, input.Now) {
		return User{}, ErrInvalidInput
	}
	item, err := service.store.UpdateUser(ctx, store.UpdateManagedUserParams{
		UserID: input.UserID, DisplayName: input.DisplayName,
		ActorUserID: input.ActorUserID, RequestID: input.RequestID, Now: input.Now,
	})
	return mapUserMutation(item, input.Now, err)
}

func (service *Service) DeleteUser(
	ctx context.Context,
	input DeleteUserInput,
) (User, error) {
	if !input.Confirm || !validation.IsUUID(input.UserID) ||
		!validActorRequest(input.ActorUserID, input.RequestID, input.Now) {
		return User{}, ErrInvalidInput
	}
	if input.UserID == input.ActorUserID {
		return User{}, ErrSelfDelete
	}
	item, err := service.store.DeleteUser(ctx, store.DeleteManagedUserParams{
		UserID: input.UserID, ActorUserID: input.ActorUserID,
		RequestID: input.RequestID, Now: input.Now,
	})
	return mapUserMutation(item, input.Now, err)
}

func (service *Service) SetUserStatus(
	ctx context.Context,
	input SetUserStatusInput,
) (User, error) {
	if !validation.IsUUID(input.UserID) ||
		(input.Status != "active" && input.Status != "disabled") ||
		!validActorRequest(input.ActorUserID, input.RequestID, input.Now) {
		return User{}, ErrInvalidInput
	}
	if input.UserID == input.ActorUserID && input.Status == "disabled" {
		return User{}, ErrSelfDisable
	}
	item, err := service.store.SetUserStatus(ctx, store.SetManagedUserStatusParams{
		UserID:      input.UserID,
		Status:      input.Status,
		ActorUserID: input.ActorUserID,
		RequestID:   input.RequestID,
		Now:         input.Now,
	})
	return mapUserMutation(item, input.Now, err)
}

func (service *Service) UnlockUser(
	ctx context.Context,
	input ConfirmUserActionInput,
) (User, error) {
	if !input.Confirm ||
		!validation.IsUUID(input.UserID) ||
		!validActorRequest(input.ActorUserID, input.RequestID, input.Now) {
		return User{}, ErrInvalidInput
	}
	item, err := service.store.UnlockUser(ctx, store.UnlockManagedUserParams{
		UserID:      input.UserID,
		ActorUserID: input.ActorUserID,
		RequestID:   input.RequestID,
		Now:         input.Now,
	})
	return mapUserMutation(item, input.Now, err)
}

func (service *Service) ResetUserPassword(
	ctx context.Context,
	input ResetPasswordInput,
) (User, error) {
	if !input.Confirm ||
		!validation.IsUUID(input.UserID) ||
		!validActorRequest(input.ActorUserID, input.RequestID, input.Now) {
		return User{}, ErrInvalidInput
	}
	passwordHash, err := service.hashPassword(ctx, input.Password)
	if err != nil {
		if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			return User{}, err
		}
		return User{}, ErrInvalidInput
	}
	item, err := service.store.ResetUserPassword(
		ctx,
		store.ResetManagedUserPasswordParams{
			UserID:       input.UserID,
			PasswordHash: passwordHash,
			ActorUserID:  input.ActorUserID,
			RequestID:    input.RequestID,
			Now:          input.Now,
		},
	)
	return mapUserMutation(item, input.Now, err)
}

// ListRoleBindings returns one page of role bindings.
func (service *Service) ListRoleBindings(
	ctx context.Context,
	input ListRoleBindingsInput,
) (RoleBindingPage, error) {
	// The role filter is no longer checked against a list of names: roles are
	// data, so an unknown one is a filter that matches nothing rather than a
	// malformed request. Its shape is still checked, because a value that could
	// never be a role name is a client bug worth reporting.
	if input.Page.Validate() != nil ||
		(input.Role != "" && !validRoleName(input.Role)) ||
		!allowedValue(input.ScopeType, "global", "tenant", "project") {
		return RoleBindingPage{}, ErrInvalidInput
	}
	stored, total, err := service.store.ListRoleBindings(
		ctx,
		store.ListManagedRoleBindingsParams{
			Role:      input.Role,
			ScopeType: input.ScopeType,
			Search:    normalizeSearch(input.Search),
			Page:      input.Page,
		},
	)
	if err != nil {
		return RoleBindingPage{}, err
	}
	result := make([]RoleBinding, 0, len(stored))
	for _, item := range stored {
		result = append(result, roleBindingFromStore(item))
	}
	return RoleBindingPage{
		RoleBindings: result,
		Page:         pagination.NewResult(input.Page, total, len(result)),
	}, nil
}

func (service *Service) GetRoleBinding(
	ctx context.Context,
	bindingID string,
) (RoleBinding, error) {
	if !validation.IsUUID(bindingID) {
		return RoleBinding{}, ErrInvalidInput
	}
	item, err := service.store.GetRoleBinding(ctx, bindingID)
	if errors.Is(err, store.ErrRoleBindingNotFound) {
		return RoleBinding{}, ErrNotFound
	}
	if err != nil {
		return RoleBinding{}, err
	}
	return roleBindingFromStore(item), nil
}

func (service *Service) CreateRoleBinding(
	ctx context.Context,
	input CreateRoleBindingInput,
) (CreateRoleBindingResult, error) {
	if !validRoleScope(
		input.Role, input.ScopeType, input.TenantID, input.ProjectID,
	) || !validation.IsUUID(input.SubjectID) ||
		!validActorRequest(input.ActorUserID, input.RequestID, input.Now) {
		return CreateRoleBindingResult{}, ErrInvalidInput
	}
	/*
	 * Binding is subject to the same ceiling as authoring.
	 *
	 * Checking only the create-a-role path would leave the shorter way round
	 * wide open: `admin` already exists and already carries everything, so
	 * anyone holding `rbac.manage` could bind it to themselves and be done. The
	 * question is not who wrote the permission set but who is about to hand it
	 * out, and the answer has to be the same either way.
	 */
	role, err := service.store.FindRoleByName(ctx, input.Role)
	if errors.Is(err, store.ErrRoleNotFound) {
		return CreateRoleBindingResult{}, ErrNotFound
	}
	if err != nil {
		return CreateRoleBindingResult{}, err
	}
	if err := service.ensureWithinActorCeiling(
		ctx, input.ActorUserID, role.Permissions,
	); err != nil {
		return CreateRoleBindingResult{}, err
	}
	id, err := identifier.NewUUID()
	if err != nil {
		return CreateRoleBindingResult{}, err
	}
	item, replayed, err := service.store.CreateRoleBinding(
		ctx,
		store.CreateManagedRoleBindingParams{
			ID:          id,
			SubjectID:   input.SubjectID,
			Role:        input.Role,
			ScopeType:   input.ScopeType,
			TenantID:    input.TenantID,
			ProjectID:   input.ProjectID,
			ActorUserID: input.ActorUserID,
			RequestID:   input.RequestID,
			Now:         input.Now,
		},
	)
	switch {
	case errors.Is(err, store.ErrAccessUserNotFound):
		return CreateRoleBindingResult{}, ErrNotFound
	case errors.Is(err, store.ErrGlobalAdminRequired):
		return CreateRoleBindingResult{}, ErrGlobalAdminRequired
	case errors.Is(err, store.ErrRoleBindingConflict),
		errors.Is(err, store.ErrAccessStateConflict):
		return CreateRoleBindingResult{}, ErrConflict
	case err != nil:
		return CreateRoleBindingResult{}, err
	default:
		return CreateRoleBindingResult{
			RoleBinding: roleBindingFromStore(item),
			Replayed:    replayed,
		}, nil
	}
}

func (service *Service) DeleteRoleBinding(
	ctx context.Context,
	input DeleteRoleBindingInput,
) (RoleBinding, error) {
	if !input.Confirm ||
		!validation.IsUUID(input.BindingID) ||
		!validActorRequest(input.ActorUserID, input.RequestID, input.Now) {
		return RoleBinding{}, ErrInvalidInput
	}
	item, err := service.store.DeleteRoleBinding(
		ctx,
		store.DeleteManagedRoleBindingParams{
			BindingID:   input.BindingID,
			ActorUserID: input.ActorUserID,
			RequestID:   input.RequestID,
			Now:         input.Now,
		},
	)
	switch {
	case errors.Is(err, store.ErrRoleBindingNotFound):
		return RoleBinding{}, ErrNotFound
	case errors.Is(err, store.ErrLastGlobalAdmin):
		return RoleBinding{}, ErrLastAdmin
	case errors.Is(err, store.ErrGlobalAdminRequired):
		return RoleBinding{}, ErrGlobalAdminRequired
	case err != nil:
		return RoleBinding{}, err
	default:
		return roleBindingFromStore(item), nil
	}
}

func (service *Service) hashPassword(
	ctx context.Context,
	password []byte,
) (string, error) {
	select {
	case service.passwordHashes <- struct{}{}:
		defer func() { <-service.passwordHashes }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	result, err := auth.HashPassword(password, auth.DefaultPasswordParams())
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return result, nil
}

// allowedValue reports whether an optional enum filter is empty or known.
// Rejecting unknown values in the service keeps the check on the path every
// caller takes, rather than relying on each HTTP handler to remember it.
func allowedValue(value string, candidates ...string) bool {
	if value == "" {
		return true
	}
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}

// normalizeSearch lowercases the term so the store can compare it against
// already-lowercased columns.
func normalizeSearch(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validDisplayName(value string) bool {
	return strings.TrimSpace(value) == value &&
		len(value) > 0 &&
		len(value) <= maxDisplayNameBytes
}

func validActorRequest(actorUserID string, requestID string, now time.Time) bool {
	return validation.IsUUID(actorUserID) &&
		strings.TrimSpace(requestID) != "" &&
		!now.IsZero()
}

func validRoleScope(
	role string,
	scopeType string,
	tenantID string,
	projectID string,
) bool {
	// Only the shape is checked here. Whether the role exists is a question for
	// the store, which answers it and returns the permission set the ceiling
	// check needs — asking twice would be two answers about a role that can
	// change between them.
	if !validRoleName(role) {
		return false
	}
	switch scopeType {
	case "global":
		return tenantID == "" && projectID == ""
	case "tenant":
		return validation.IsUUID(tenantID) && projectID == ""
	case "project":
		return validation.IsUUID(tenantID) &&
			validation.IsUUID(projectID)
	default:
		return false
	}
}

/*
 * Reports the effective lock state, not the stored one.
 *
 * A lock is expired lazily: the row keeps `status = 'locked'` and its elapsed
 * `lock_expires_at` until the next login attempt rewrites them. Passing that
 * through tells every reader the account cannot sign in during a window where
 * `auth.Service.Login` would in fact admit it — the API would be describing a
 * state the rest of the Server no longer believes in, and a Console rendering it
 * faithfully ends up printing "锁定至 7 秒前".
 *
 * The condition mirrors `lockActive` in the login path exactly, including the
 * nil case: a `locked` row with no expiry is not holding anyone out, so it is
 * not reported as holding anyone out.
 *
 * The failure counter is deliberately left alone. It is cleared by a successful
 * login, and until then it is the only visible evidence of what happened.
 */
func userFromStore(item store.ManagedUser, now time.Time) User {
	status := item.Status
	lockedAt := item.LockedAt
	lockExpiresAt := item.LockExpiresAt
	if status == "locked" && (lockExpiresAt == nil || !lockExpiresAt.After(now)) {
		status = "active"
		lockedAt = nil
		lockExpiresAt = nil
	}
	return User{
		ID:                item.ID,
		Username:          item.Username,
		DisplayName:       item.DisplayName,
		Status:            status,
		FailedLoginCount:  item.FailedLoginCount,
		LockedAt:          lockedAt,
		LockExpiresAt:     lockExpiresAt,
		PasswordChangedAt: item.PasswordChangedAt,
		CreatedAt:         item.CreatedAt,
		UpdatedAt:         item.UpdatedAt,
	}
}

func roleBindingFromStore(item store.ManagedRoleBinding) RoleBinding {
	return RoleBinding{
		ID:                 item.ID,
		SubjectID:          item.SubjectID,
		Role:               item.Role,
		ScopeType:          item.ScopeType,
		TenantID:           item.TenantID,
		ProjectID:          item.ProjectID,
		CreatedAt:          item.CreatedAt,
		SubjectUsername:    item.SubjectUsername,
		SubjectDisplayName: item.SubjectDisplayName,
	}
}

func mapUserMutation(item store.ManagedUser, now time.Time, err error) (User, error) {
	switch {
	case errors.Is(err, store.ErrAccessUserNotFound):
		return User{}, ErrNotFound
	case errors.Is(err, store.ErrAccessStateConflict):
		return User{}, ErrConflict
	case errors.Is(err, store.ErrLastGlobalAdmin):
		return User{}, ErrLastAdmin
	case errors.Is(err, store.ErrGlobalAdminRequired):
		return User{}, ErrGlobalAdminRequired
	case err != nil:
		return User{}, err
	default:
		return userFromStore(item, now), nil
	}
}
