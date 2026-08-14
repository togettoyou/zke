package store

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

var (
	ErrTenantNotFound             = errors.New("tenant not found")
	ErrClusterNotFound            = errors.New("cluster not found")
	ErrResourceCreationConflict   = errors.New("resource creation idempotency conflict")
	ErrTenantNameConflict         = errors.New("tenant name already exists")
	ErrProjectNameConflict        = errors.New("project name already exists in tenant")
	ErrClusterNameConflict        = errors.New("cluster name already exists in project")
	ErrResourceStateConflict      = errors.New("resource state conflict")
	ErrResourceCreationNotAllowed = errors.New("resource creation not allowed")
)

type ResourceManagementStore struct {
	pool *pgxpool.Pool
}

// ScopeVisibility carries an already-resolved RBAC visibility into a query so
// that the database, not the Server, decides which rows a caller may count and
// page through.
type ScopeVisibility struct {
	// Global grants every tenant and project.
	Global bool
	// TenantIDs are tenants granted in full by a tenant-scoped binding.
	TenantIDs []string
	// ProjectIDs are individually granted projects.
	ProjectIDs []string
	// ProjectTenantIDs are the tenants owning ProjectIDs, which must stay
	// listable so a project-scoped user can still reach their project.
	ProjectTenantIDs []string
}

type ListTenantsParams struct {
	Visibility ScopeVisibility
	Status     string
	Search     string
	Page       pagination.Request
}

type ListTenantProjectsParams struct {
	TenantID   string
	Visibility ScopeVisibility
	Status     string
	Search     string
	Page       pagination.Request
}

type TenantResource struct {
	ID        string
	Name      string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ProjectResource struct {
	ID        string
	TenantID  string
	Name      string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ClusterResource struct {
	ID             string
	TenantID       string
	ProjectID      string
	Name           string
	AgentNamespace string
	Status         string
	LastSeenAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type CreateTenantParams struct {
	ID             string
	Name           string
	ActorUserID    string
	RequestID      string
	IdempotencyKey string
	Now            time.Time
}

type CreateTenantResult struct {
	Tenant   TenantResource
	Replayed bool
}

type CreateProjectParams struct {
	ID             string
	TenantID       string
	Name           string
	ActorUserID    string
	RequestID      string
	IdempotencyKey string
	Now            time.Time
}

type CreateProjectResult struct {
	Project  ProjectResource
	Replayed bool
}

type UpdateTenantParams struct {
	TenantID    string
	Name        string
	Status      string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type DeleteTenantParams struct {
	TenantID    string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type UpdateProjectParams struct {
	ProjectID   string
	Name        string
	Status      string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type DeleteProjectParams struct {
	ProjectID   string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type UpdateClusterParams struct {
	ClusterID   string
	Name        string
	Status      string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type DeleteClusterParams struct {
	ClusterID   string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

func NewResourceManagementStore(pool *pgxpool.Pool) *ResourceManagementStore {
	return &ResourceManagementStore{pool: pool}
}
