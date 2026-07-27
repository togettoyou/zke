package resourcemanagement

import (
	"context"

	"github.com/togettoyou/zke/pkg/server/store"
)

// Store is the persistence surface Tenant, Project and Cluster lifecycle
// management needs.
type Store interface {
	ListTenants(ctx context.Context, params store.ListTenantsParams) ([]store.TenantResource, int, error)
	GetTenant(ctx context.Context, tenantID string) (store.TenantResource, error)
	CreateTenant(ctx context.Context, params store.CreateTenantParams) (store.CreateTenantResult, error)
	UpdateTenant(ctx context.Context, params store.UpdateTenantParams) (store.TenantResource, error)
	DeleteTenant(ctx context.Context, params store.DeleteTenantParams) (store.TenantResource, error)
	ListTenantProjects(ctx context.Context, params store.ListTenantProjectsParams) ([]store.ProjectResource, int, error)
	GetProject(ctx context.Context, projectID string) (store.ProjectResource, error)
	CreateProject(ctx context.Context, params store.CreateProjectParams) (store.CreateProjectResult, error)
	UpdateProject(ctx context.Context, params store.UpdateProjectParams) (store.ProjectResource, error)
	DeleteProject(ctx context.Context, params store.DeleteProjectParams) (store.ProjectResource, error)
	UpdateCluster(ctx context.Context, params store.UpdateClusterParams) (store.ClusterResource, error)
	DeleteCluster(ctx context.Context, params store.DeleteClusterParams) (store.ClusterResource, error)
}
