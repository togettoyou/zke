package httpapi

import (
	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

// registerRoutes wires every HTTP route.
//
// Authorization is enforced in one of two places, and which one a route uses is
// decided by whether the request names a single scope:
//
//   - Route-level: the path identifies exactly one Tenant, Project or Cluster,
//     so a Require* middleware can authorize it before the handler runs. Every
//     mutation is route-level, and it is the default.
//   - Service-level: the request has no single scope to check against, because
//     the answer itself is the set of resources the caller may see. Listing
//     Tenants, reading one Tenant (a Project-scoped user must still be able to
//     see the Tenant holding their Project), listing a Tenant's Projects, the
//     audit trail and the Cluster event stream fall in this group. These
//     resolve the caller's RBAC visibility and push it into the query, so the
//     reported total matches what the caller may read.
//
// A route with no Require* middleware is therefore not an omission, but it does
// mean its handler must resolve visibility itself.
//
// The role-binding memo is installed alongside the request timeout, so it is
// scoped to one short request. The Cluster event stream deliberately takes
// neither: it is long-lived and re-resolves visibility periodically so that a
// withdrawn RoleBinding ends the stream.
func registerRoutes(router *gin.Engine, handlers handlers) {
	router.GET("/healthz", handlers.health.health)
	router.GET("/readyz", handlers.health.ready)

	apiV1 := router.Group("/api/v1")
	authRoutes := apiV1.Group("/auth")
	authRoutes.POST("/login", handlers.auth.login)

	authenticatedAuthRoutes := authRoutes.Group("")
	authenticatedAuthRoutes.Use(handlers.authMiddleware.RequireAuthentication)
	authenticatedAuthRoutes.GET("/me", handlers.auth.me)
	authenticatedAuthRoutes.POST(
		"/logout",
		handlers.authMiddleware.RequireCSRF,
		handlers.auth.logout,
	)
	authenticatedAuthRoutes.POST(
		"/password",
		handlers.authMiddleware.RequireCSRF,
		handlers.auth.changePassword,
	)

	userRoutes := apiV1.Group("/users")
	userRoutes.Use(
		handlers.requestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireAuthentication,
	)
	userRoutes.GET(
		"",
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionUserRead),
		handlers.accessManagement.listUsers,
	)
	userRoutes.POST(
		"",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionUserManage),
		handlers.accessManagement.createUser,
	)
	userRoutes.GET(
		"/:user_id",
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionUserRead),
		handlers.accessManagement.getUser,
	)
	userRoutes.PUT(
		"/:user_id",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionUserManage),
		handlers.accessManagement.updateUser,
	)
	userRoutes.DELETE(
		"/:user_id",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionUserManage),
		handlers.accessManagement.deleteUser,
	)
	userRoutes.PUT(
		"/:user_id/status",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionUserManage),
		handlers.accessManagement.setUserStatus,
	)
	userRoutes.POST(
		"/:user_id/unlock",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionUserManage),
		handlers.accessManagement.unlockUser,
	)
	userRoutes.POST(
		"/:user_id/password-reset",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionUserManage),
		handlers.accessManagement.resetPassword,
	)

	roleBindingRoutes := apiV1.Group("/role-bindings")
	roleBindingRoutes.Use(
		handlers.requestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireAuthentication,
	)
	roleBindingRoutes.GET(
		"",
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionRBACRead),
		handlers.accessManagement.listRoleBindings,
	)
	roleBindingRoutes.POST(
		"",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionRBACManage),
		handlers.accessManagement.createRoleBinding,
	)
	roleBindingRoutes.DELETE(
		"/:role_binding_id",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionRBACManage),
		handlers.accessManagement.deleteRoleBinding,
	)
	roleBindingRoutes.GET(
		"/:role_binding_id",
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionRBACRead),
		handlers.accessManagement.getRoleBinding,
	)

	auditRoutes := apiV1.Group("/audit-events")
	auditRoutes.Use(
		handlers.requestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireAuthentication,
	)
	auditRoutes.GET("", handlers.auditQuery.list)
	// Static vocabulary rather than a resource, so it sits under the collection
	// it describes and needs only the same authentication.
	auditRoutes.GET("/actions", handlers.auditQuery.listActions)

	apiV1.GET(
		"/events",
		handlers.authMiddleware.RequireAuthentication,
		handlers.agentStatus.events,
	)

	tenantRoutes := apiV1.Group("/tenants")
	tenantRoutes.Use(
		handlers.requestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireAuthentication,
	)
	tenantRoutes.GET("", handlers.resourceManagement.listTenants)
	tenantRoutes.POST(
		"",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(
			rbac.PermissionTenantCreate,
		),
		handlers.resourceManagement.createTenant,
	)
	tenantRoutes.GET(
		"/:tenant_id",
		handlers.resourceManagement.getTenant,
	)
	tenantRoutes.PUT(
		"/:tenant_id",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionTenantManage),
		handlers.resourceManagement.updateTenant,
	)
	tenantRoutes.DELETE(
		"/:tenant_id",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionTenantManage),
		handlers.resourceManagement.deleteTenant,
	)
	tenantRoutes.GET(
		"/:tenant_id/projects",
		handlers.resourceManagement.listProjects,
	)
	tenantRoutes.POST(
		"/:tenant_id/projects",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireTenant(
			rbac.PermissionProjectCreate,
			"tenant_id",
		),
		handlers.resourceManagement.createProject,
	)

	projectRoutes := apiV1.Group("/projects")
	projectRoutes.Use(
		handlers.requestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireAuthentication,
	)
	projectRoutes.POST(
		"/:project_id/cluster-enrollments",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireProject(
			rbac.PermissionClusterEnrollmentCreate,
			"project_id",
		),
		handlers.enrollment.create,
	)
	projectRoutes.GET(
		"/:project_id/cluster-enrollments",
		handlers.authorizationMiddleware.RequireProject(
			rbac.PermissionClusterEnrollmentRead,
			"project_id",
		),
		handlers.enrollment.list,
	)
	projectRoutes.GET(
		"/:project_id/cluster-enrollments/:enrollment_id",
		handlers.authorizationMiddleware.RequireProject(
			rbac.PermissionClusterEnrollmentRead,
			"project_id",
		),
		handlers.enrollment.get,
	)
	projectRoutes.DELETE(
		"/:project_id/cluster-enrollments/:enrollment_id",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireProject(
			rbac.PermissionClusterEnrollmentRevoke,
			"project_id",
		),
		handlers.enrollment.revoke,
	)
	projectRoutes.GET(
		"/:project_id/clusters",
		handlers.authorizationMiddleware.RequireProject(
			rbac.PermissionClusterRead,
			"project_id",
		),
		handlers.agentStatus.list,
	)
	projectRoutes.POST(
		"/:project_id/cluster-installations",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireProject(
			rbac.PermissionClusterEnrollmentCreate,
			"project_id",
		),
		handlers.agentInstallation.create,
	)
	projectRoutes.GET(
		"/:project_id",
		handlers.authorizationMiddleware.RequireProject(
			rbac.PermissionProjectRead,
			"project_id",
		),
		handlers.resourceManagement.getProject,
	)
	projectRoutes.PUT(
		"/:project_id",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireProject(
			rbac.PermissionProjectManage,
			"project_id",
		),
		handlers.resourceManagement.updateProject,
	)
	projectRoutes.DELETE(
		"/:project_id",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireProject(
			rbac.PermissionProjectManage,
			"project_id",
		),
		handlers.resourceManagement.deleteProject,
	)

	clusterRoutes := apiV1.Group("/clusters")
	clusterRoutes.Use(
		handlers.requestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireAuthentication,
	)
	clusterRoutes.GET(
		"/:cluster_id",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.agentStatus.getCluster,
	)
	clusterRoutes.GET(
		"/:cluster_id/nodes",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesNode.list,
	)
	clusterRoutes.GET(
		"/:cluster_id/nodes/:node_name",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesNode.get,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesNamespace.list,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesNamespace.get,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceCreate,
			"cluster_id",
		),
		handlers.kubernetesNamespace.create,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/namespaces/:namespace_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceDelete,
			"cluster_id",
		),
		handlers.kubernetesNamespace.delete,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/pods",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesPod.list,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/pods/:pod_name",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesPod.get,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/namespaces/:namespace_name/pods/:pod_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceDelete,
			"cluster_id",
		),
		handlers.kubernetesPod.delete,
	)
	// Pod logs intentionally use a separate group: clusterRoutes installs the
	// short request timeout, while follow=true is a bounded, revalidated stream
	// whose cancellation must propagate to the target Agent.
	podLogRoutes := apiV1.Group("/clusters")
	podLogRoutes.Use(
		handlers.authMiddleware.RequireAuthentication,
	)
	podLogRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/pods/:pod_name/logs",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterPodLogsRead,
			"cluster_id",
		),
		handlers.kubernetesPodLogs.stream,
	)
	// Kubernetes Events use an independent, bounded SSE route so that the
	// short HTTP operation timeout does not terminate a quiet follow stream.
	eventRoutes := apiV1.Group("/clusters")
	eventRoutes.Use(handlers.authMiddleware.RequireAuthentication)
	eventRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/events",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterEventRead,
			"cluster_id",
		),
		handlers.kubernetesEvents.stream,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesWorkload.list,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceCreate,
			"cluster_id",
		),
		handlers.kubernetesWorkload.create,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesWorkload.get,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name/scale",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceUpdate,
			"cluster_id",
		),
		handlers.kubernetesWorkload.scale,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name/restart",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceUpdate,
			"cluster_id",
		),
		handlers.kubernetesWorkload.restart,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name/suspend",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceUpdate,
			"cluster_id",
		),
		handlers.kubernetesWorkload.suspend,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name/resume",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceUpdate,
			"cluster_id",
		),
		handlers.kubernetesWorkload.resume,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceDelete,
			"cluster_id",
		),
		handlers.kubernetesWorkload.delete,
	)
	clusterRoutes.GET(
		"/:cluster_id/kubernetes/resource-types",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesResource.discover,
	)
	clusterRoutes.GET(
		"/:cluster_id/kubernetes/resources",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesResource.list,
	)
	clusterRoutes.GET(
		"/:cluster_id/kubernetes/resources/:resource_name",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesResource.get,
	)
	clusterRoutes.POST(
		"/:cluster_id/kubernetes/resources",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceCreate,
			"cluster_id",
		),
		handlers.kubernetesResource.create,
	)
	clusterRoutes.PUT(
		"/:cluster_id/kubernetes/resources/:resource_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceUpdate,
			"cluster_id",
		),
		handlers.kubernetesResource.update,
	)
	clusterRoutes.PATCH(
		"/:cluster_id/kubernetes/resources/:resource_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceUpdate,
			"cluster_id",
		),
		handlers.kubernetesResource.patch,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/kubernetes/resources/:resource_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceDelete,
			"cluster_id",
		),
		handlers.kubernetesResource.delete,
	)
	clusterRoutes.PUT(
		"/:cluster_id",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterManage,
			"cluster_id",
		),
		handlers.resourceManagement.updateCluster,
	)
	clusterRoutes.DELETE(
		"/:cluster_id",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterManage,
			"cluster_id",
		),
		handlers.resourceManagement.deleteCluster,
	)
	clusterRoutes.POST(
		"/:cluster_id/connection/revoke",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterConnectionRevoke,
			"cluster_id",
		),
		handlers.agentManagement.revoke,
	)
	clusterRoutes.POST(
		"/:cluster_id/connection/reenroll",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterEnrollmentCreate,
			"cluster_id",
		),
		handlers.enrollment.reenroll,
	)

	agentAPIV1 := router.Group("/agent-api/v1")
	agentAPIV1.POST("/enroll", handlers.agentRegistration.enroll)

	agentInstallV1 := router.Group("/agent-install/v1")
	agentInstallV1.GET("/manifest", handlers.agentInstallation.manifest)
}
