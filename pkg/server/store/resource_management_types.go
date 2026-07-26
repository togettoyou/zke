package store

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrTenantNotFound             = errors.New("tenant not found")
	ErrClusterNotFound            = errors.New("cluster not found")
	ErrResourceCreationConflict   = errors.New("resource creation idempotency conflict")
	ErrResourceStateConflict      = errors.New("resource state conflict")
	ErrResourceCreationNotAllowed = errors.New("resource creation not allowed")
)

type ResourceManagementStore struct {
	pool *pgxpool.Pool
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
	ID         string
	TenantID   string
	ProjectID  string
	Name       string
	Status     string
	LastSeenAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
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
