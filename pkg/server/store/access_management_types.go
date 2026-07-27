package store

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

var (
	ErrAccessUserNotFound  = errors.New("managed user not found")
	ErrAccessUserConflict  = errors.New("managed user conflict")
	ErrAccessStateConflict = errors.New("managed access state conflict")
	ErrRoleBindingNotFound = errors.New("role binding not found")
	ErrRoleBindingConflict = errors.New("role binding conflict")
	ErrLastGlobalAdmin     = errors.New("last active global administrator")
)

type AccessManagementStore struct {
	pool *pgxpool.Pool
}

// ListManagedUsersParams filters and pages the managed user list. Search must
// already be lowercased by the caller so the comparison stays index-friendly
// and matches the Console's case-insensitive behaviour.
type ListManagedUsersParams struct {
	Status string
	Search string
	Page   pagination.Request
}

// ListManagedRoleBindingsParams filters and pages the role binding list.
type ListManagedRoleBindingsParams struct {
	Role      string
	ScopeType string
	Search    string
	Page      pagination.Request
}

type ManagedUser struct {
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

type CreateManagedUserParams struct {
	ID           string
	Username     string
	DisplayName  string
	PasswordHash string
	ActorUserID  string
	RequestID    string
	Now          time.Time
}

type SetManagedUserStatusParams struct {
	UserID      string
	Status      string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type UpdateManagedUserParams struct {
	UserID      string
	DisplayName string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type DeleteManagedUserParams struct {
	UserID      string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type UnlockManagedUserParams struct {
	UserID      string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type ResetManagedUserPasswordParams struct {
	UserID       string
	PasswordHash string
	ActorUserID  string
	RequestID    string
	Now          time.Time
}

type ManagedRoleBinding struct {
	ID        string
	SubjectID string
	Role      string
	ScopeType string
	TenantID  string
	ProjectID string
	CreatedAt time.Time
}

type CreateManagedRoleBindingParams struct {
	ID          string
	SubjectID   string
	Role        string
	ScopeType   string
	TenantID    string
	ProjectID   string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type DeleteManagedRoleBindingParams struct {
	BindingID   string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

func NewAccessManagementStore(pool *pgxpool.Pool) *AccessManagementStore {
	return &AccessManagementStore{pool: pool}
}
