package accessmanagement

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/rbac"
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
	ErrSelfDisable  = errors.New("cannot disable the authenticated user")
)

type Config struct {
	MaxConcurrentPasswordHashes int
}

type Service struct {
	store          Store
	passwordHashes chan struct{}
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
	Page   pagination.Request
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
		Page:   input.Page,
	})
	if err != nil {
		return UserPage{}, err
	}
	result := make([]User, 0, len(stored))
	for _, item := range stored {
		result = append(result, userFromStore(item))
	}
	return UserPage{
		Users: result,
		Page:  pagination.NewResult(input.Page, total, len(result)),
	}, nil
}

func (service *Service) GetUser(
	ctx context.Context,
	userID string,
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
	return userFromStore(item), nil
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
		return userFromStore(item), nil
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
	return mapUserMutation(item, err)
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
		return User{}, ErrSelfDisable
	}
	item, err := service.store.DeleteUser(ctx, store.DeleteManagedUserParams{
		UserID: input.UserID, ActorUserID: input.ActorUserID,
		RequestID: input.RequestID, Now: input.Now,
	})
	return mapUserMutation(item, err)
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
	return mapUserMutation(item, err)
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
	return mapUserMutation(item, err)
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
	return mapUserMutation(item, err)
}

// ListRoleBindings returns one page of role bindings.
func (service *Service) ListRoleBindings(
	ctx context.Context,
	input ListRoleBindingsInput,
) (RoleBindingPage, error) {
	if input.Page.Validate() != nil ||
		!allowedValue(input.Role, "admin", "viewer") ||
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
	// The accepted roles come from the authorization package rather than a
	// second list here, so a role can never be granted before authorization
	// knows what it permits.
	if !rbac.RoleExists(role) {
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

func userFromStore(item store.ManagedUser) User {
	return User{
		ID:                item.ID,
		Username:          item.Username,
		DisplayName:       item.DisplayName,
		Status:            item.Status,
		FailedLoginCount:  item.FailedLoginCount,
		LockedAt:          item.LockedAt,
		LockExpiresAt:     item.LockExpiresAt,
		PasswordChangedAt: item.PasswordChangedAt,
		CreatedAt:         item.CreatedAt,
		UpdatedAt:         item.UpdatedAt,
	}
}

func roleBindingFromStore(item store.ManagedRoleBinding) RoleBinding {
	return RoleBinding{
		ID:        item.ID,
		SubjectID: item.SubjectID,
		Role:      item.Role,
		ScopeType: item.ScopeType,
		TenantID:  item.TenantID,
		ProjectID: item.ProjectID,
		CreatedAt: item.CreatedAt,
	}
}

func mapUserMutation(item store.ManagedUser, err error) (User, error) {
	switch {
	case errors.Is(err, store.ErrAccessUserNotFound):
		return User{}, ErrNotFound
	case errors.Is(err, store.ErrAccessStateConflict):
		return User{}, ErrConflict
	case errors.Is(err, store.ErrLastGlobalAdmin):
		return User{}, ErrLastAdmin
	case err != nil:
		return User{}, err
	default:
		return userFromStore(item), nil
	}
}
