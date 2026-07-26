package resourcemanagement

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/identifier"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

const maxResourceNameBytes = 253

var (
	ErrInvalidInput        = errors.New("invalid resource management input")
	ErrNotFound            = errors.New("resource not found")
	ErrDenied              = errors.New("resource management denied")
	ErrIdempotencyConflict = errors.New("resource idempotency conflict")
	ErrStateConflict       = errors.New("resource state conflict")
)

type Service struct {
	store         *store.ResourceManagementStore
	authorization *rbac.Service
}

type Tenant struct {
	ID        string
	Name      string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Project struct {
	ID        string
	TenantID  string
	Name      string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Cluster struct {
	ID         string
	TenantID   string
	ProjectID  string
	Name       string
	Status     string
	LastSeenAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type CreateTenantInput struct {
	Name           string
	ActorUserID    string
	RequestID      string
	IdempotencyKey string
	Now            time.Time
}

type CreateTenantResult struct {
	Tenant
	Replayed bool
}

type CreateProjectInput struct {
	TenantID       string
	Name           string
	ActorUserID    string
	RequestID      string
	IdempotencyKey string
	Now            time.Time
}

type CreateProjectResult struct {
	Project
	Replayed bool
}

func NewService(
	resourceStore *store.ResourceManagementStore,
	authorization *rbac.Service,
) *Service {
	return &Service{
		store:         resourceStore,
		authorization: authorization,
	}
}

func (service *Service) ListTenants(
	ctx context.Context,
	userID string,
) ([]Tenant, error) {
	if !validation.IsUUID(userID) {
		return nil, ErrInvalidInput
	}
	visibility, err := service.authorization.ResolveVisibility(
		ctx,
		userID,
		rbac.PermissionClusterRead,
	)
	if err != nil {
		return nil, err
	}
	stored, err := service.store.ListTenants(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Tenant, 0, len(stored))
	for _, item := range stored {
		if !visibility.AllowsTenant(item.ID) {
			continue
		}
		result = append(result, tenantFromStore(item))
	}
	return result, nil
}

func (service *Service) CreateTenant(
	ctx context.Context,
	input CreateTenantInput,
) (CreateTenantResult, error) {
	if !validName(input.Name) ||
		!validation.IsUUID(input.ActorUserID) ||
		strings.TrimSpace(input.RequestID) == "" ||
		!validation.IsIdempotencyKey(input.IdempotencyKey) ||
		input.Now.IsZero() {
		return CreateTenantResult{}, ErrInvalidInput
	}
	id, err := identifier.NewUUID()
	if err != nil {
		return CreateTenantResult{}, err
	}
	created, err := service.store.CreateTenant(ctx, store.CreateTenantParams{
		ID:             id,
		Name:           input.Name,
		ActorUserID:    input.ActorUserID,
		RequestID:      input.RequestID,
		IdempotencyKey: input.IdempotencyKey,
		Now:            input.Now,
	})
	switch {
	case errors.Is(err, store.ErrResourceCreationConflict):
		return CreateTenantResult{}, ErrIdempotencyConflict
	case errors.Is(err, store.ErrResourceCreationNotAllowed):
		return CreateTenantResult{}, ErrDenied
	case err != nil:
		return CreateTenantResult{}, err
	}
	return CreateTenantResult{
		Tenant:   tenantFromStore(created.Tenant),
		Replayed: created.Replayed,
	}, nil
}

func (service *Service) ListProjects(
	ctx context.Context,
	userID string,
	tenantID string,
) ([]Project, error) {
	if !validation.IsUUID(userID) || !validation.IsUUID(tenantID) {
		return nil, ErrInvalidInput
	}
	visibility, err := service.authorization.ResolveVisibility(
		ctx,
		userID,
		rbac.PermissionClusterRead,
	)
	if err != nil {
		return nil, err
	}
	stored, err := service.store.ListTenantProjects(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	result := make([]Project, 0, len(stored))
	for _, item := range stored {
		if !visibility.AllowsProject(item.TenantID, item.ID) {
			continue
		}
		result = append(result, projectFromStore(item))
	}
	return result, nil
}

func (service *Service) CreateProject(
	ctx context.Context,
	input CreateProjectInput,
) (CreateProjectResult, error) {
	if !validation.IsUUID(input.TenantID) ||
		!validName(input.Name) ||
		!validation.IsUUID(input.ActorUserID) ||
		strings.TrimSpace(input.RequestID) == "" ||
		!validation.IsIdempotencyKey(input.IdempotencyKey) ||
		input.Now.IsZero() {
		return CreateProjectResult{}, ErrInvalidInput
	}
	id, err := identifier.NewUUID()
	if err != nil {
		return CreateProjectResult{}, err
	}
	created, err := service.store.CreateProject(ctx, store.CreateProjectParams{
		ID:             id,
		TenantID:       input.TenantID,
		Name:           input.Name,
		ActorUserID:    input.ActorUserID,
		RequestID:      input.RequestID,
		IdempotencyKey: input.IdempotencyKey,
		Now:            input.Now,
	})
	switch {
	case errors.Is(err, store.ErrTenantNotFound):
		return CreateProjectResult{}, ErrNotFound
	case errors.Is(err, store.ErrResourceCreationConflict):
		return CreateProjectResult{}, ErrIdempotencyConflict
	case errors.Is(err, store.ErrResourceStateConflict):
		return CreateProjectResult{}, ErrStateConflict
	case errors.Is(err, store.ErrResourceCreationNotAllowed):
		return CreateProjectResult{}, ErrDenied
	case err != nil:
		return CreateProjectResult{}, err
	}
	return CreateProjectResult{
		Project:  projectFromStore(created.Project),
		Replayed: created.Replayed,
	}, nil
}

func (service *Service) ListClusters(
	ctx context.Context,
	projectID string,
) ([]Cluster, error) {
	if !validation.IsUUID(projectID) {
		return nil, ErrInvalidInput
	}
	stored, err := service.store.ListProjectClusters(ctx, projectID)
	if err != nil {
		return nil, err
	}
	result := make([]Cluster, 0, len(stored))
	for _, item := range stored {
		result = append(result, clusterFromStore(item))
	}
	return result, nil
}

func (service *Service) GetCluster(
	ctx context.Context,
	clusterID string,
) (Cluster, error) {
	if !validation.IsUUID(clusterID) {
		return Cluster{}, ErrInvalidInput
	}
	item, err := service.store.GetCluster(ctx, clusterID)
	if errors.Is(err, store.ErrClusterNotFound) {
		return Cluster{}, ErrNotFound
	}
	if err != nil {
		return Cluster{}, err
	}
	return clusterFromStore(item), nil
}

func validName(value string) bool {
	return strings.TrimSpace(value) == value &&
		len(value) > 0 &&
		len(value) <= maxResourceNameBytes
}

func tenantFromStore(item store.TenantResource) Tenant {
	return Tenant{
		ID:        item.ID,
		Name:      item.Name,
		Status:    item.Status,
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
	}
}

func projectFromStore(item store.ProjectResource) Project {
	return Project{
		ID:        item.ID,
		TenantID:  item.TenantID,
		Name:      item.Name,
		Status:    item.Status,
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
	}
}

func clusterFromStore(item store.ClusterResource) Cluster {
	return Cluster{
		ID:         item.ID,
		TenantID:   item.TenantID,
		ProjectID:  item.ProjectID,
		Name:       item.Name,
		Status:     item.Status,
		LastSeenAt: item.LastSeenAt,
		CreatedAt:  item.CreatedAt,
		UpdatedAt:  item.UpdatedAt,
	}
}
