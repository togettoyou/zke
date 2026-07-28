package resourcemanagement

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/identifier"
	"github.com/togettoyou/zke/pkg/shared/pagination"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

const maxResourceNameBytes = 253

var (
	ErrInvalidInput        = errors.New("invalid resource management input")
	ErrNotFound            = errors.New("resource not found")
	ErrDenied              = errors.New("resource management denied")
	ErrIdempotencyConflict = errors.New("resource idempotency conflict")
	ErrStateConflict       = errors.New("resource state conflict")
	// Name collisions, one per resource, because the scope of the rule and the
	// advice that follows from it differ: a Tenant name is unique globally, a
	// Project name is unique inside its Tenant, and a Cluster name is unique
	// inside its Project. Suspended resources keep their names. All three ignore
	// case.
	ErrTenantNameConflict  = errors.New("tenant name already exists")
	ErrProjectNameConflict = errors.New("project name already exists in tenant")
	ErrClusterNameConflict = errors.New("cluster name already exists in project")
)

type Service struct {
	store         Store
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

// ListTenantsInput selects one page of visible tenants.
type ListTenantsInput struct {
	UserID string
	Status string
	Search string
	Page   pagination.Request
}

// TenantPage is one page of tenants plus its position in the visible set.
type TenantPage struct {
	Tenants []Tenant
	Page    pagination.Result
}

// ListProjectsInput selects one page of visible projects inside a tenant.
type ListProjectsInput struct {
	UserID   string
	TenantID string
	Status   string
	Search   string
	Page     pagination.Request
}

// ProjectPage is one page of projects plus its position.
type ProjectPage struct {
	Projects []Project
	Page     pagination.Result
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
	Status      string
	Confirm     bool
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
	resourceStore Store,
	authorization *rbac.Service,
) *Service {
	return &Service{
		store:         resourceStore,
		authorization: authorization,
	}
}

// ListTenants returns one page of the tenants the caller may read. The
// resolved RBAC visibility is pushed into the query, so the total reflects
// exactly what this user is allowed to see.
func (service *Service) ListTenants(
	ctx context.Context,
	input ListTenantsInput,
) (TenantPage, error) {
	if !validation.IsUUID(input.UserID) ||
		input.Page.Validate() != nil ||
		!allowedValue(input.Status, "active", "suspended") {
		return TenantPage{}, ErrInvalidInput
	}
	visibility, err := service.authorization.ResolveVisibility(
		ctx,
		input.UserID,
		rbac.PermissionTenantRead,
	)
	if err != nil {
		return TenantPage{}, err
	}
	stored, total, err := service.store.ListTenants(ctx, store.ListTenantsParams{
		Visibility: scopeVisibility(visibility),
		Status:     input.Status,
		Search:     normalizeSearch(input.Search),
		Page:       input.Page,
	})
	if err != nil {
		return TenantPage{}, err
	}
	result := make([]Tenant, 0, len(stored))
	for _, item := range stored {
		result = append(result, tenantFromStore(item))
	}
	return TenantPage{
		Tenants: result,
		Page:    pagination.NewResult(input.Page, total, len(result)),
	}, nil
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
	case errors.Is(err, store.ErrTenantNameConflict):
		return CreateTenantResult{}, ErrTenantNameConflict
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

// ListProjects returns one page of the projects the caller may read inside a
// tenant.
func (service *Service) ListProjects(
	ctx context.Context,
	input ListProjectsInput,
) (ProjectPage, error) {
	if !validation.IsUUID(input.UserID) ||
		!validation.IsUUID(input.TenantID) ||
		input.Page.Validate() != nil ||
		!allowedValue(input.Status, "active", "suspended") {
		return ProjectPage{}, ErrInvalidInput
	}
	visibility, err := service.authorization.ResolveVisibility(
		ctx,
		input.UserID,
		rbac.PermissionProjectRead,
	)
	if err != nil {
		return ProjectPage{}, err
	}
	stored, total, err := service.store.ListTenantProjects(
		ctx,
		store.ListTenantProjectsParams{
			TenantID:   input.TenantID,
			Visibility: scopeVisibility(visibility),
			Status:     input.Status,
			Search:     normalizeSearch(input.Search),
			Page:       input.Page,
		},
	)
	if err != nil {
		return ProjectPage{}, err
	}
	result := make([]Project, 0, len(stored))
	for _, item := range stored {
		result = append(result, projectFromStore(item))
	}
	return ProjectPage{
		Projects: result,
		Page:     pagination.NewResult(input.Page, total, len(result)),
	}, nil
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
	case errors.Is(err, store.ErrProjectNameConflict):
		return CreateProjectResult{}, ErrProjectNameConflict
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

func (service *Service) UpdateCluster(
	ctx context.Context,
	input UpdateClusterInput,
) (Cluster, error) {
	// Only the two states an operator may ask for. `pending` and `active` are
	// reported by the Agent connection, never set from here.
	if !validation.IsUUID(input.ClusterID) ||
		!validName(input.Name) ||
		(input.Status != "active" && input.Status != "suspended") ||
		(input.Status == "suspended" && !input.Confirm) ||
		!validMutationActor(input.ActorUserID, input.RequestID, input.Now) {
		return Cluster{}, ErrInvalidInput
	}
	item, err := service.store.UpdateCluster(ctx, store.UpdateClusterParams{
		ClusterID:   input.ClusterID,
		Name:        input.Name,
		Status:      input.Status,
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

// scopeVisibility translates a resolved RBAC visibility into the row filter
// the store applies, keeping the authorization decision in one place.
func scopeVisibility(visibility rbac.Visibility) store.ScopeVisibility {
	return store.ScopeVisibility{
		Global:           visibility.IsGlobal(),
		TenantIDs:        visibility.TenantIDs(),
		ProjectIDs:       visibility.ProjectIDs(),
		ProjectTenantIDs: visibility.ProjectTenantIDs(),
	}
}

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

func normalizeSearch(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
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
	case errors.Is(err, store.ErrTenantNameConflict):
		return Tenant{}, ErrTenantNameConflict
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
	case errors.Is(err, store.ErrProjectNameConflict):
		return Project{}, ErrProjectNameConflict
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
	case errors.Is(err, store.ErrClusterNameConflict):
		return Cluster{}, ErrClusterNameConflict
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
