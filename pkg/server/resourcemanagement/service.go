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

type UpdateTenantInput struct {
	TenantID    string
	Name        string
	Status      string
	Confirm     bool
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type DeleteTenantInput struct {
	TenantID    string
	Confirm     bool
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type UpdateProjectInput struct {
	ProjectID   string
	Name        string
	Status      string
	Confirm     bool
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type DeleteProjectInput struct {
	ProjectID   string
	Confirm     bool
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type UpdateClusterInput struct {
	ClusterID   string
	Name        string
	ActorUserID string
	RequestID   string
	Now         time.Time
}

type DeleteClusterInput struct {
	ClusterID   string
	Confirm     bool
	ActorUserID string
	RequestID   string
	Now         time.Time
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
		rbac.PermissionTenantRead,
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

func (service *Service) GetTenant(
	ctx context.Context,
	userID string,
	tenantID string,
) (Tenant, error) {
	if !validation.IsUUID(userID) || !validation.IsUUID(tenantID) {
		return Tenant{}, ErrInvalidInput
	}
	visibility, err := service.authorization.ResolveVisibility(
		ctx, userID, rbac.PermissionTenantRead,
	)
	if err != nil {
		return Tenant{}, err
	}
	if !visibility.AllowsTenant(tenantID) {
		return Tenant{}, ErrDenied
	}
	item, err := service.store.GetTenant(ctx, tenantID)
	if errors.Is(err, store.ErrTenantNotFound) {
		return Tenant{}, ErrNotFound
	}
	if err != nil {
		return Tenant{}, err
	}
	return tenantFromStore(item), nil
}

func (service *Service) UpdateTenant(
	ctx context.Context,
	input UpdateTenantInput,
) (Tenant, error) {
	if !validation.IsUUID(input.TenantID) ||
		!validName(input.Name) ||
		(input.Status != "active" && input.Status != "suspended") ||
		(input.Status == "suspended" && !input.Confirm) ||
		!validMutationActor(input.ActorUserID, input.RequestID, input.Now) {
		return Tenant{}, ErrInvalidInput
	}
	item, err := service.store.UpdateTenant(ctx, store.UpdateTenantParams{
		TenantID:    input.TenantID,
		Name:        input.Name,
		Status:      input.Status,
		ActorUserID: input.ActorUserID,
		RequestID:   input.RequestID,
		Now:         input.Now,
	})
	return mapTenantMutation(item, err)
}

func (service *Service) DeleteTenant(
	ctx context.Context,
	input DeleteTenantInput,
) (Tenant, error) {
	if !input.Confirm || !validation.IsUUID(input.TenantID) ||
		!validMutationActor(input.ActorUserID, input.RequestID, input.Now) {
		return Tenant{}, ErrInvalidInput
	}
	item, err := service.store.DeleteTenant(ctx, store.DeleteTenantParams{
		TenantID:    input.TenantID,
		ActorUserID: input.ActorUserID,
		RequestID:   input.RequestID,
		Now:         input.Now,
	})
	return mapTenantMutation(item, err)
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
		rbac.PermissionProjectRead,
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

func (service *Service) GetProject(
	ctx context.Context,
	projectID string,
) (Project, error) {
	if !validation.IsUUID(projectID) {
		return Project{}, ErrInvalidInput
	}
	item, err := service.store.GetProject(ctx, projectID)
	if errors.Is(err, store.ErrProjectNotFound) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}
	return projectFromStore(item), nil
}

func (service *Service) UpdateProject(
	ctx context.Context,
	input UpdateProjectInput,
) (Project, error) {
	if !validation.IsUUID(input.ProjectID) ||
		!validName(input.Name) ||
		(input.Status != "active" && input.Status != "suspended") ||
		(input.Status == "suspended" && !input.Confirm) ||
		!validMutationActor(input.ActorUserID, input.RequestID, input.Now) {
		return Project{}, ErrInvalidInput
	}
	item, err := service.store.UpdateProject(ctx, store.UpdateProjectParams{
		ProjectID:   input.ProjectID,
		Name:        input.Name,
		Status:      input.Status,
		ActorUserID: input.ActorUserID,
		RequestID:   input.RequestID,
		Now:         input.Now,
	})
	return mapProjectMutation(item, err)
}

func (service *Service) DeleteProject(
	ctx context.Context,
	input DeleteProjectInput,
) (Project, error) {
	if !input.Confirm || !validation.IsUUID(input.ProjectID) ||
		!validMutationActor(input.ActorUserID, input.RequestID, input.Now) {
		return Project{}, ErrInvalidInput
	}
	item, err := service.store.DeleteProject(ctx, store.DeleteProjectParams{
		ProjectID:   input.ProjectID,
		ActorUserID: input.ActorUserID,
		RequestID:   input.RequestID,
		Now:         input.Now,
	})
	return mapProjectMutation(item, err)
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

func (service *Service) UpdateCluster(
	ctx context.Context,
	input UpdateClusterInput,
) (Cluster, error) {
	if !validation.IsUUID(input.ClusterID) ||
		!validName(input.Name) ||
		!validMutationActor(input.ActorUserID, input.RequestID, input.Now) {
		return Cluster{}, ErrInvalidInput
	}
	item, err := service.store.UpdateCluster(ctx, store.UpdateClusterParams{
		ClusterID:   input.ClusterID,
		Name:        input.Name,
		ActorUserID: input.ActorUserID,
		RequestID:   input.RequestID,
		Now:         input.Now,
	})
	return mapClusterMutation(item, err)
}

func (service *Service) DeleteCluster(
	ctx context.Context,
	input DeleteClusterInput,
) (Cluster, error) {
	if !input.Confirm || !validation.IsUUID(input.ClusterID) ||
		!validMutationActor(input.ActorUserID, input.RequestID, input.Now) {
		return Cluster{}, ErrInvalidInput
	}
	item, err := service.store.DeleteCluster(ctx, store.DeleteClusterParams{
		ClusterID:   input.ClusterID,
		ActorUserID: input.ActorUserID,
		RequestID:   input.RequestID,
		Now:         input.Now,
	})
	return mapClusterMutation(item, err)
}

func validName(value string) bool {
	return strings.TrimSpace(value) == value &&
		len(value) > 0 &&
		len(value) <= maxResourceNameBytes
}

func validMutationActor(actorUserID string, requestID string, now time.Time) bool {
	return validation.IsUUID(actorUserID) &&
		strings.TrimSpace(requestID) != "" &&
		!now.IsZero()
}

func mapTenantMutation(item store.TenantResource, err error) (Tenant, error) {
	switch {
	case errors.Is(err, store.ErrTenantNotFound):
		return Tenant{}, ErrNotFound
	case errors.Is(err, store.ErrResourceStateConflict),
		errors.Is(err, store.ErrResourceCreationNotAllowed):
		return Tenant{}, ErrStateConflict
	case err != nil:
		return Tenant{}, err
	default:
		return tenantFromStore(item), nil
	}
}

func mapProjectMutation(item store.ProjectResource, err error) (Project, error) {
	switch {
	case errors.Is(err, store.ErrProjectNotFound):
		return Project{}, ErrNotFound
	case errors.Is(err, store.ErrResourceStateConflict),
		errors.Is(err, store.ErrResourceCreationNotAllowed):
		return Project{}, ErrStateConflict
	case err != nil:
		return Project{}, err
	default:
		return projectFromStore(item), nil
	}
}

func mapClusterMutation(item store.ClusterResource, err error) (Cluster, error) {
	switch {
	case errors.Is(err, store.ErrClusterNotFound):
		return Cluster{}, ErrNotFound
	case errors.Is(err, store.ErrResourceStateConflict),
		errors.Is(err, store.ErrResourceCreationNotAllowed):
		return Cluster{}, ErrStateConflict
	case err != nil:
		return Cluster{}, err
	default:
		return clusterFromStore(item), nil
	}
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
