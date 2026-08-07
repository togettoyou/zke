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
	// Reported when the actor is not a global administrator and the operation is
	// one only a global administrator may perform. Distinct from
	// ErrLastGlobalAdmin: the platform would survive the change, and the refusal
	// is about who is asking rather than about how many would be left.
	ErrGlobalAdminRequired = errors.New("only a global administrator may do this")
	// Reported instead of letting the actor delete a binding that grants the
	// actor's own access. Not a permission question — the actor holds
	// `rbac.manage` or the request would not have reached here — but the one
	// deletion whose result is that the caller can no longer undo it.
	ErrSelfUnbind   = errors.New("cannot delete the authenticated user's own role binding")
	ErrRoleNotFound = errors.New("role not found")
	ErrRoleConflict = errors.New("role name already exists")
	ErrRoleBuiltin  = errors.New("builtin role cannot be changed")
	// Reported instead of removing a role somebody still holds. The database
	// refuses it too — `role_bindings.role` is a foreign key — and this is that
	// refusal named, so the API can say which of the two conflicts happened.
	ErrRoleInUse = errors.New("role is still bound to a subject")
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
	// Resolves the status filter against the effective lock state rather than
	// the lazily-expired stored one.
	Now  time.Time
	Page pagination.Request
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

// ListManagedRolesParams filters and pages the role list.
type ListManagedRolesParams struct {
	// "" for every role, "true"/"false" to select only builtin or only custom
	// ones. A string rather than a *bool so the HTTP query parameter maps onto
	// it without a second nil-vs-false distinction to keep straight.
	Builtin string
	Search  string
	Page    pagination.Request
}

type ManagedRole struct {
	ID          string
	Name        string
	DisplayName string
	Description string
	Builtin     bool
	Permissions []string
	// How many bindings name this role. Resolved by the same query that reads
	// the role, because it is the number that decides whether deleting it is
	// possible and the Console should not have to ask separately.
	BindingCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type CreateManagedRoleParams struct {
	ID          string
	Name        string
	DisplayName string
	Description string
	Permissions []string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

// UpdateManagedRoleParams replaces a role's editable fields. Permissions are a
// complete replacement rather than a delta: an update carrying a subset would
// be indistinguishable from one meaning to remove the rest.
type UpdateManagedRoleParams struct {
	RoleID      string
	DisplayName string
	Description string
	Permissions []string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type DeleteManagedRoleParams struct {
	RoleID      string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type ManagedRoleBinding struct {
	ID        string
	SubjectID string
	Role      string
	ScopeType string
	TenantID  string
	ProjectID string
	CreatedAt time.Time
	// Resolved from the subject row by the same query that reads the binding.
	// Empty when the subject no longer exists, which a LEFT JOIN allows so that
	// an orphaned binding is still listed and can still be removed.
	SubjectUsername    string
	SubjectDisplayName string
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
