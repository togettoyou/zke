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
	apiV1.GET("/setup", handlers.setup.status)
	apiV1.POST("/setup", handlers.setup.initialize)
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

	platformRoutes := apiV1.Group("/platform")
	platformRoutes.Use(
		handlers.requestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireAuthentication,
		handlers.authorizationMiddleware.RequireGlobalAdministrator,
	)
	platformRoutes.GET("/settings", handlers.platformSettings.get)
	platformRoutes.PUT("/settings", handlers.authMiddleware.RequireCSRF, handlers.platformSettings.updateSettings)
	platformRoutes.POST("/agent-endpoint-profiles", handlers.authMiddleware.RequireCSRF, handlers.platformSettings.createProfile)
	platformRoutes.PUT("/agent-endpoint-profiles/:profile_id", handlers.authMiddleware.RequireCSRF, handlers.platformSettings.updateProfile)
	platformRoutes.DELETE("/agent-endpoint-profiles/:profile_id", handlers.authMiddleware.RequireCSRF, handlers.platformSettings.deleteProfile)
	// The AI model endpoint is platform configuration like the rest of this
	// group, and is authorized the same way. It carries its own revision
	// because it is saved on its own, so it gets its own routes rather than
	// widening the settings body.
	platformRoutes.GET("/ai-model", handlers.aiModelSettings.get)
	platformRoutes.PUT("/ai-model", handlers.authMiddleware.RequireCSRF, handlers.aiModelSettings.update)
	platformRoutes.PATCH("/ai-model/enabled", handlers.authMiddleware.RequireCSRF, handlers.aiModelSettings.setEnabled)

	// The chart catalogue.
	//
	// It is its own group rather than part of /platform because it answers to
	// permissions rather than to the global administrator role: browsing what
	// may be installed is a read an operator needs, and the operator installing
	// a chart is rarely the administrator who added the repository it came
	// from. Managing the catalogue keeps the stronger of the two permissions.
	//
	// Fetching a chart or an index makes this Server issue a request to an
	// address stored in the catalogue. That is why there is no route taking a
	// URL: the address is always one an administrator holding
	// `helm.repository.manage` put there.
	helmCatalogueRoutes := apiV1.Group("/helm")
	helmCatalogueRoutes.Use(
		handlers.requestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireAuthentication,
	)
	helmCatalogueRoutes.GET(
		"/repositories",
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionHelmRepositoryRead),
		handlers.helmRepository.list,
	)
	helmCatalogueRoutes.GET(
		"/repositories/:repository_id",
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionHelmRepositoryRead),
		handlers.helmRepository.get,
	)
	helmCatalogueRoutes.GET(
		"/repositories/:repository_id/charts",
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionHelmRepositoryRead),
		handlers.helmRepository.charts,
	)
	// Re-reading the index is a deliberate action, not a page refresh, so it is
	// a POST — but it answers to the read permission, because the request it
	// makes upstream is the same one the cache's own expiry would have made.
	//
	// A path of its own rather than a name under `/charts`: everything under
	// there is a chart name, and a chart really called `refresh` would sit in
	// the same place.
	helmCatalogueRoutes.POST(
		"/repositories/:repository_id/index-refresh",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionHelmRepositoryRead),
		handlers.helmRepository.refreshCharts,
	)
	helmCatalogueRoutes.GET(
		"/repositories/:repository_id/charts/:chart_name",
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionHelmRepositoryRead),
		handlers.helmRepository.chart,
	)
	helmCatalogueRoutes.GET(
		"/repositories/:repository_id/charts/:chart_name/versions",
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionHelmRepositoryRead),
		handlers.helmRepository.chartVersions,
	)
	// What the chart archive holds. Separate from the chart detail because the
	// detail is what an operator reads to decide, and most of them decide
	// without ever opening the tree.
	helmCatalogueRoutes.GET(
		"/repositories/:repository_id/charts/:chart_name/files",
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionHelmRepositoryRead),
		handlers.helmRepository.chartFiles,
	)
	// One file out of the chart archive. The listing above says what is in it;
	// this is how one of them is read, and it answers to the same permission
	// because it is the same document by another route.
	//
	// The path travels as a query parameter rather than in the URL: it is
	// matched against the archive's own member names, and a path segment would
	// invite reading it as one.
	helmCatalogueRoutes.GET(
		"/repositories/:repository_id/charts/:chart_name/file",
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionHelmRepositoryRead),
		handlers.helmRepository.chartFile,
	)
	helmCatalogueRoutes.POST(
		"/repositories",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionHelmRepositoryManage),
		handlers.helmRepository.create,
	)
	helmCatalogueRoutes.PUT(
		"/repositories/:repository_id",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionHelmRepositoryManage),
		handlers.helmRepository.update,
	)
	helmCatalogueRoutes.DELETE(
		"/repositories/:repository_id",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionHelmRepositoryManage),
		handlers.helmRepository.delete,
	)

	// The connectivity test is authorized exactly like the rest of the platform
	// group but cannot share its request budget: it waits on somebody else's
	// inference service for as long as the operator configured. Its own group
	// carries the longer timeout, the same way a manifest's does.
	aiModelTestRoutes := apiV1.Group("/platform")
	aiModelTestRoutes.Use(
		handlers.aiModelTestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireAuthentication,
		handlers.authorizationMiddleware.RequireGlobalAdministrator,
	)
	aiModelTestRoutes.POST("/ai-model/test", handlers.authMiddleware.RequireCSRF, handlers.aiModelSettings.test)

	// AIOps follows the current Project and every session has one fixed target
	// Cluster, so authorization is resolved by the runtime after loading the
	// session. The SSE route deliberately has no request timeout; it reauthenticates and
	// reauthorizes throughout its bounded lifetime.
	aiRoutes := apiV1.Group("/ai")
	aiRoutes.Use(
		handlers.authMiddleware.RequireAuthentication,
	)
	aiRoutes.GET("/tools", handlers.aiRuntime.tools)
	aiRoutes.GET("/sessions", handlers.aiRuntime.listSessions)
	aiRoutes.POST("/sessions", handlers.authMiddleware.RequireCSRF, handlers.aiRuntime.createSession)
	aiRoutes.GET("/sessions/:session_id", handlers.aiRuntime.getSession)
	aiRoutes.PATCH("/sessions/:session_id", handlers.authMiddleware.RequireCSRF, handlers.aiRuntime.updateSession)
	aiRoutes.DELETE("/sessions/:session_id", handlers.authMiddleware.RequireCSRF, handlers.aiRuntime.deleteSession)
	aiRoutes.POST("/sessions/:session_id/turns", handlers.authMiddleware.RequireCSRF, handlers.aiRuntime.startTurn)
	aiRoutes.DELETE("/sessions/:session_id/turns/current", handlers.authMiddleware.RequireCSRF, handlers.aiRuntime.cancelTurn)
	aiRoutes.POST("/sessions/:session_id/approvals", handlers.authMiddleware.RequireCSRF, handlers.aiRuntime.decideApproval)
	aiRoutes.GET("/sessions/:session_id/trajectory", handlers.aiRuntime.trajectory)
	aiRoutes.GET("/sessions/:session_id/context", handlers.aiRuntime.contextUsage)
	aiRoutes.GET("/sessions/:session_id/events", handlers.aiRuntime.events)
	aiRoutes.GET("/sessions/:session_id/attachments", handlers.aiRuntime.listAttachments)
	aiRoutes.POST("/sessions/:session_id/attachments", handlers.authMiddleware.RequireCSRF, handlers.aiRuntime.createAttachment)
	aiRoutes.DELETE("/sessions/:session_id/attachments/:attachment_id", handlers.authMiddleware.RequireCSRF, handlers.aiRuntime.deleteAttachment)
	aiRoutes.GET("/sessions/:session_id/export", handlers.aiRuntime.exportSession)
	authenticatedAuthRoutes.POST(
		"/password",
		handlers.requestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(
			rbac.PermissionUserPasswordChange,
		),
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

	// Roles and bindings share the two `rbac.*` permissions: a role is what a
	// binding hands out, and separating who may define one from who may grant it
	// would leave each half unable to finish the job the other started.
	roleRoutes := apiV1.Group("/roles")
	roleRoutes.Use(
		handlers.requestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireAuthentication,
	)
	roleRoutes.GET(
		"",
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionRBACRead),
		handlers.accessManagement.listRoles,
	)
	roleRoutes.POST(
		"",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionRBACManage),
		handlers.accessManagement.createRole,
	)
	roleRoutes.GET(
		"/:role_id",
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionRBACRead),
		handlers.accessManagement.getRole,
	)
	roleRoutes.PUT(
		"/:role_id",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionRBACManage),
		handlers.accessManagement.updateRole,
	)
	roleRoutes.DELETE(
		"/:role_id",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionRBACManage),
		handlers.accessManagement.deleteRole,
	)

	// The permission vocabulary, under the read permission that the role editor
	// already requires. It reports the caller's own ceiling alongside each name,
	// so it is not a public dictionary and does not belong outside authentication.
	permissionRoutes := apiV1.Group("/permissions")
	permissionRoutes.Use(
		handlers.requestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireAuthentication,
	)
	permissionRoutes.GET(
		"",
		handlers.authorizationMiddleware.RequireGlobal(rbac.PermissionRBACRead),
		handlers.accessManagement.listPermissions,
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

	// Metrics are a multi-cluster application: there is no cluster_id in the
	// path, so no RequireCluster gate here. The query service resolves the
	// caller's visibility for cluster.metrics.read and builds the scope filter
	// from it, which keeps that boundary in one place instead of splitting it
	// between middleware and the query.
	observabilityRoutes := apiV1.Group("/observability")
	observabilityRoutes.Use(
		handlers.requestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireAuthentication,
	)
	observabilityRoutes.GET(
		"/metrics/queries",
		handlers.observabilityMetrics.catalog,
	)
	observabilityRoutes.GET(
		"/metrics/query",
		handlers.observabilityMetrics.query,
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
		"/:project_id/agent-endpoint-profiles",
		handlers.authorizationMiddleware.RequireProject(
			rbac.PermissionClusterEnrollmentCreate,
			"project_id",
		),
		handlers.platformSettings.listReady,
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
		handlers.authorizationMiddleware.RequireProtectedNamespaceAccess("cluster_id"),
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
		"/:cluster_id/overview",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.clusterOverview.get,
	)
	// One permission covers installing and removing the collector; reading
	// metrics is a different one, so an operator who may look at charts cannot
	// change what runs in a Cluster.
	clusterRoutes.GET(
		"/:cluster_id/metrics-collector",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterMetricsManage,
			"cluster_id",
		),
		handlers.metricsCollector.status,
	)
	clusterRoutes.POST(
		"/:cluster_id/metrics-collector",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterMetricsManage,
			"cluster_id",
		),
		handlers.metricsCollector.install,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/metrics-collector",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterMetricsManage,
			"cluster_id",
		),
		handlers.metricsCollector.uninstall,
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
		"/:cluster_id/metrics/nodes",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesMetrics.nodes,
	)
	clusterRoutes.GET(
		"/:cluster_id/nodes/:node_name",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesNode.get,
	)
	clusterRoutes.POST(
		"/:cluster_id/nodes/:node_name/drain",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterNodeDrain,
			"cluster_id",
		),
		handlers.authorizationMiddleware.ResolveClusterProtectedNamespaceGrant("cluster_id"),
		handlers.kubernetesNode.drain,
	)
	clusterRoutes.GET(
		"/:cluster_id/nodes/:node_name/describe",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterEventRead,
			"cluster_id",
		),
		handlers.kubernetesDescribe.node,
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
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/metrics/pods",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesMetrics.pods,
	)
	// Namespace writes use their own permission family. Reading remains on
	// `cluster.read`; ordinary, Kubernetes system, and Agent Namespace mutations
	// are selected from the concrete target by the authorization middleware.
	clusterRoutes.POST(
		"/:cluster_id/namespaces",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterNamespaceManage,
			"cluster_id",
		),
		handlers.kubernetesNamespace.create,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/namespaces/:namespace_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterNamespaceManage,
			"cluster_id",
		),
		handlers.kubernetesNamespace.delete,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/pods/:pod_name/terminal-sessions",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterPodExec,
			"cluster_id",
		),
		handlers.kubernetesPodExec.create,
	)
	// Creating a standalone terminal may include the image's first pull, so it
	// has the Agent Resource request budget rather than the short budget shared
	// by ordinary Cluster API calls.
	terminalCreateRoutes := apiV1.Group("/clusters")
	terminalCreateRoutes.Use(
		handlers.terminalRequestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireAuthentication,
	)
	terminalCreateRoutes.POST(
		"/:cluster_id/terminal-sessions",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterTerminalExec,
			"cluster_id",
		),
		handlers.clusterTerminal.create,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/pods/:pod_name/terminal-recordings",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterPodTerminalRecordingRead,
			"cluster_id",
		),
		handlers.kubernetesPodExec.listRecordings,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/pods/:pod_name/terminal-recordings/:recording_id",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterPodTerminalRecordingRead,
			"cluster_id",
		),
		handlers.kubernetesPodExec.getRecording,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/pods/:pod_name/access-sessions",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterPodPortForward,
			"cluster_id",
		),
		handlers.kubernetesPodAccess.create,
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
	// Describe joins an object with the Events that name it, so it answers to
	// both permissions rather than to the weaker of the two. `cluster.read`
	// alone would turn describe into a way to read a Namespace's Events without
	// `cluster.event.read`, which is the separation that permission exists for;
	// the Events are not an incidental part of the response but the half an
	// operator opened it for. Both checks are route-level and each records its
	// own denial, so a refusal says which permission was missing.
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/pods/:pod_name/describe",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterEventRead,
			"cluster_id",
		),
		handlers.kubernetesDescribe.pod,
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
	// Eviction takes the Pod away exactly as the delete above does, so it
	// answers to the same permission — and to the same protected-Namespace
	// substitution, because the Namespace is in the path. What it adds is the
	// Cluster's own veto: a PodDisruptionBudget can refuse.
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/pods/:pod_name/eviction",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceDelete,
			"cluster_id",
		),
		handlers.kubernetesPod.evict,
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
	// Pod terminal WebSockets have their own group so the short request timeout
	// does not terminate an active, bounded and periodically revalidated shell.
	podExecRoutes := apiV1.Group("/clusters")
	podExecRoutes.Use(
		handlers.authMiddleware.RequireAuthentication,
		handlers.authorizationMiddleware.RequireProtectedNamespaceAccess("cluster_id"),
	)
	podExecRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/pods/:pod_name/terminal-sessions/:session_id",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterPodExec,
			"cluster_id",
		),
		handlers.kubernetesPodExec.connect,
	)
	podExecRoutes.GET(
		"/:cluster_id/terminal-sessions/:session_id",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterTerminalExec,
			"cluster_id",
		),
		handlers.clusterTerminal.connect,
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
	// The Cluster-wide event centre. Same permission as the Namespace route
	// above: `cluster.event.read` is granted per Cluster, never per Namespace,
	// so this reaches nothing a holder could not already read one Namespace at
	// a time.
	eventRoutes.GET(
		"/:cluster_id/events",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterEventRead,
			"cluster_id",
		),
		handlers.kubernetesEvents.clusterStream,
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
	clusterRoutes.PUT(
		"/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceUpdate,
			"cluster_id",
		),
		handlers.kubernetesWorkload.update,
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
	// Running a CronJob now creates a Job; nothing about the CronJob changes.
	// So it takes the create permission, not the update one the other CronJob
	// actions beside it take.
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name/trigger",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceCreate,
			"cluster_id",
		),
		handlers.kubernetesWorkload.trigger,
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
	// The workload's own describe reads more than the workload: the objects it
	// owns and their Events are where a rollout that will not come up actually
	// fails. Same two permissions as the other describes, for the same reason.
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name/describe",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterEventRead,
			"cluster_id",
		),
		handlers.kubernetesDescribe.workload,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name/revisions",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesWorkload.revisions,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name/rollback",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceUpdate,
			"cluster_id",
		),
		handlers.kubernetesWorkload.rollback,
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
		"/:cluster_id/namespaces/:namespace_name/networking/:network_resource",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesNetworking.list,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/networking/:network_resource",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceCreate,
			"cluster_id",
		),
		handlers.kubernetesNetworking.create,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/networking/:network_resource/:network_name",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesNetworking.get,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/networking/:network_resource/:network_name/describe",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterEventRead,
			"cluster_id",
		),
		handlers.kubernetesDescribe.networkingResource,
	)
	clusterRoutes.PUT(
		"/:cluster_id/namespaces/:namespace_name/networking/:network_resource/:network_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceUpdate,
			"cluster_id",
		),
		handlers.kubernetesNetworking.update,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/namespaces/:namespace_name/networking/:network_resource/:network_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceDelete,
			"cluster_id",
		),
		handlers.kubernetesNetworking.delete,
	)
	clusterRoutes.GET(
		"/:cluster_id/storage/:storage_resource",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesStorage.list,
	)
	clusterRoutes.POST(
		"/:cluster_id/storage/:storage_resource",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceCreate, "cluster_id"),
		handlers.kubernetesStorage.create,
	)
	clusterRoutes.GET(
		"/:cluster_id/storage/:storage_resource/:storage_name",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesStorage.get,
	)
	clusterRoutes.PUT(
		"/:cluster_id/storage/:storage_resource/:storage_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceUpdate, "cluster_id"),
		handlers.kubernetesStorage.update,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/storage/:storage_resource/:storage_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceDelete, "cluster_id"),
		handlers.kubernetesStorage.delete,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/storage/:storage_resource",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesStorage.list,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/storage/:storage_resource",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceCreate, "cluster_id"),
		handlers.kubernetesStorage.create,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/storage/:storage_resource/:storage_name",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesStorage.get,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/storage/:storage_resource/:storage_name/describe",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterEventRead,
			"cluster_id",
		),
		handlers.kubernetesDescribe.persistentVolumeClaim,
	)
	clusterRoutes.PUT(
		"/:cluster_id/namespaces/:namespace_name/storage/:storage_resource/:storage_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceUpdate, "cluster_id"),
		handlers.kubernetesStorage.update,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/namespaces/:namespace_name/storage/:storage_resource/:storage_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceDelete, "cluster_id"),
		handlers.kubernetesStorage.delete,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/horizontalpodautoscalers",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesHPA.list,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/horizontalpodautoscalers",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceCreate, "cluster_id"),
		handlers.kubernetesHPA.create,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/horizontalpodautoscalers/:hpa_name",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesHPA.get,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/horizontalpodautoscalers/:hpa_name/metrics-trend",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesHPA.metricTrend,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/horizontalpodautoscalers/:hpa_name/describe",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterEventRead,
			"cluster_id",
		),
		handlers.kubernetesDescribe.horizontalPodAutoscaler,
	)
	clusterRoutes.PUT(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/horizontalpodautoscalers/:hpa_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceUpdate, "cluster_id"),
		handlers.kubernetesHPA.update,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/horizontalpodautoscalers/:hpa_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceDelete, "cluster_id"),
		handlers.kubernetesHPA.delete,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/verticalpodautoscalers",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesHPA.listVPA,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/verticalpodautoscalers",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceCreate, "cluster_id"),
		handlers.kubernetesHPA.createVPA,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/verticalpodautoscalers/:vpa_name",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesHPA.getVPA,
	)
	clusterRoutes.PUT(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/verticalpodautoscalers/:vpa_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceUpdate, "cluster_id"),
		handlers.kubernetesHPA.updateVPA,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/verticalpodautoscalers/:vpa_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceDelete, "cluster_id"),
		handlers.kubernetesHPA.deleteVPA,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/scaledobjects",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesHPA.listKEDA,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/scaledobjects",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceCreate, "cluster_id"),
		handlers.kubernetesHPA.createKEDA,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/scaledobjects/:scaled_object_name",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesHPA.getKEDA,
	)
	clusterRoutes.PUT(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/scaledobjects/:scaled_object_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceUpdate, "cluster_id"),
		handlers.kubernetesHPA.updateKEDA,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/namespaces/:namespace_name/autoscaling/scaledobjects/:scaled_object_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceDelete, "cluster_id"),
		handlers.kubernetesHPA.deleteKEDA,
	)
	clusterRoutes.GET(
		"/:cluster_id/policies/:policy_resource",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesPolicy.list,
	)
	clusterRoutes.POST(
		"/:cluster_id/policies/:policy_resource",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceCreate, "cluster_id"),
		handlers.kubernetesPolicy.create,
	)
	clusterRoutes.GET(
		"/:cluster_id/policies/:policy_resource/:policy_name",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesPolicy.get,
	)
	clusterRoutes.PUT(
		"/:cluster_id/policies/:policy_resource/:policy_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceUpdate, "cluster_id"),
		handlers.kubernetesPolicy.update,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/policies/:policy_resource/:policy_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceDelete, "cluster_id"),
		handlers.kubernetesPolicy.delete,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/policies/:policy_resource",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesPolicy.list,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/policies/:policy_resource",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceCreate, "cluster_id"),
		handlers.kubernetesPolicy.create,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/policies/:policy_resource/:policy_name",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.kubernetesPolicy.get,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/policies/:policy_resource/:policy_name/describe",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterEventRead,
			"cluster_id",
		),
		handlers.kubernetesDescribe.policy,
	)
	clusterRoutes.PUT(
		"/:cluster_id/namespaces/:namespace_name/policies/:policy_resource/:policy_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceUpdate, "cluster_id"),
		handlers.kubernetesPolicy.update,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/namespaces/:namespace_name/policies/:policy_resource/:policy_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceDelete, "cluster_id"),
		handlers.kubernetesPolicy.delete,
	)
	clusterRoutes.GET(
		"/:cluster_id/authorization/:authorization_resource",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRBACRead, "cluster_id"),
		handlers.kubernetesAuthorization.list,
	)
	clusterRoutes.POST(
		"/:cluster_id/authorization/:authorization_resource",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRBACManage, "cluster_id"),
		handlers.authorizationMiddleware.ResolveClusterSecretGrant("cluster_id"),
		handlers.kubernetesAuthorization.create,
	)
	clusterRoutes.GET(
		"/:cluster_id/authorization/:authorization_resource/:authorization_name",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRBACRead, "cluster_id"),
		handlers.kubernetesAuthorization.get,
	)
	clusterRoutes.PUT(
		"/:cluster_id/authorization/:authorization_resource/:authorization_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRBACManage, "cluster_id"),
		handlers.authorizationMiddleware.ResolveClusterSecretGrant("cluster_id"),
		handlers.kubernetesAuthorization.update,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/authorization/:authorization_resource/:authorization_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRBACManage, "cluster_id"),
		handlers.kubernetesAuthorization.delete,
	)
	// YAML for the same five families, under the same two permissions. The
	// generic Resource and YAML endpoints still refuse them: reaching a Role
	// through `cluster.resource.update` is exactly what that refusal is for.
	clusterRoutes.GET(
		"/:cluster_id/authorization/:authorization_resource/:authorization_name/yaml",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRBACRead, "cluster_id"),
		handlers.kubernetesAuthorizationYAML.get,
	)
	clusterRoutes.PUT(
		"/:cluster_id/authorization/:authorization_resource/:authorization_name/yaml",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRBACManage, "cluster_id"),
		handlers.authorizationMiddleware.ResolveClusterSecretGrant("cluster_id"),
		handlers.kubernetesAuthorizationYAML.update,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/authorization/:authorization_resource",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRBACRead, "cluster_id"),
		handlers.kubernetesAuthorization.list,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/authorization/:authorization_resource",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRBACManage, "cluster_id"),
		handlers.authorizationMiddleware.ResolveClusterSecretGrant("cluster_id"),
		handlers.kubernetesAuthorization.create,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/authorization/:authorization_resource/:authorization_name",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRBACRead, "cluster_id"),
		handlers.kubernetesAuthorization.get,
	)
	clusterRoutes.PUT(
		"/:cluster_id/namespaces/:namespace_name/authorization/:authorization_resource/:authorization_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRBACManage, "cluster_id"),
		handlers.authorizationMiddleware.ResolveClusterSecretGrant("cluster_id"),
		handlers.kubernetesAuthorization.update,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/namespaces/:namespace_name/authorization/:authorization_resource/:authorization_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRBACManage, "cluster_id"),
		handlers.kubernetesAuthorization.delete,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/authorization/:authorization_resource/:authorization_name/yaml",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRBACRead, "cluster_id"),
		handlers.kubernetesAuthorizationYAML.get,
	)
	clusterRoutes.PUT(
		"/:cluster_id/namespaces/:namespace_name/authorization/:authorization_resource/:authorization_name/yaml",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRBACManage, "cluster_id"),
		handlers.authorizationMiddleware.ResolveClusterSecretGrant("cluster_id"),
		handlers.kubernetesAuthorizationYAML.update,
	)
	// Secrets use their own permissions rather than the general cluster ones:
	// reading configuration and reading credentials are different asks, and a
	// role that may do the first must not silently be able to do the second.
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/secrets",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterSecretRead,
			"cluster_id",
		),
		handlers.kubernetesSecret.list,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/secrets",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterSecretManage,
			"cluster_id",
		),
		handlers.kubernetesSecret.create,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/secrets/:secret_name",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterSecretRead,
			"cluster_id",
		),
		handlers.kubernetesSecret.get,
	)
	clusterRoutes.PUT(
		"/:cluster_id/namespaces/:namespace_name/secrets/:secret_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterSecretManage,
			"cluster_id",
		),
		handlers.kubernetesSecret.update,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/namespaces/:namespace_name/secrets/:secret_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterSecretManage,
			"cluster_id",
		),
		handlers.kubernetesSecret.delete,
	)
	// The Secret's YAML, under the Secret permissions rather than the resource
	// ones. The generic YAML endpoint's refusal of Secrets is unchanged.
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/secrets/:secret_name/yaml",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterSecretRead,
			"cluster_id",
		),
		handlers.kubernetesSecretYAML.get,
	)
	clusterRoutes.PUT(
		"/:cluster_id/namespaces/:namespace_name/secrets/:secret_name/yaml",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterSecretManage,
			"cluster_id",
		),
		handlers.kubernetesSecretYAML.update,
	)
	// Helm releases are Secrets of type `helm.sh/release.v1`, so they answer to
	// the Secret permission and not to `cluster.read` alone: reading a release
	// hands back the values the chart was installed with. Both permissions are
	// required — `cluster.read` because this is a Cluster query like any other,
	// `cluster.secret.read` because of what it returns — and the
	// protected-Namespace gate covers these routes for the same reason it covers
	// `/secrets`. The write routes are further down and require considerably
	// more than these two.
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/helm-releases",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterSecretRead,
			"cluster_id",
		),
		handlers.kubernetesHelmRelease.list,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/helm-releases/:release_name",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterSecretRead,
			"cluster_id",
		),
		handlers.kubernetesHelmRelease.get,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/helm-releases/:release_name/revisions",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterSecretRead,
			"cluster_id",
		),
		handlers.kubernetesHelmRelease.revisions,
	)
	// Helm release writes.
	//
	// The permission stack is longer than anywhere else on this Server, and
	// each of its parts answers a different question about the same request.
	//
	//   - `cluster.read`, because this addresses a Cluster like any other
	//     route here.
	//   - `cluster.helm.manage`, because changing a release is its own
	//     capability: one request renders a chart and writes every object an
	//     application owns, which is not a thing any single-object permission
	//     was granted for.
	//   - the object permissions the operation actually spends. An install or
	//     an upgrade creates and replaces objects; an uninstall deletes them.
	//     Holding the Helm permission does not conjure the power to write
	//     objects, and this is where that is checked.
	//   - `cluster.secret.manage`, because Helm's release storage *is* a
	//     Secret and the values it holds are its content. A role that may not
	//     write Secrets may not write releases.
	//
	// The protected-Namespace gate on this group covers these routes too — a
	// release installed into `kube-system` or the Agent's own Namespace needs
	// the same additional grant a Secret written there does.
	//
	// One thing is deliberately *not* here: a chart that renders objects no
	// Namespace contains needs authorization over the whole Cluster, and which
	// objects those are is only known once the chart has been rendered. The
	// handler resolves `cluster.manage` and tells the Agent, which refuses the
	// rendered manifest by name if the answer was no.
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/helm-releases",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterHelmManage, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceCreate, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceUpdate, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterSecretManage, "cluster_id"),
		handlers.helmReleaseWrite.install,
	)
	clusterRoutes.PUT(
		"/:cluster_id/namespaces/:namespace_name/helm-releases/:release_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterHelmManage, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceCreate, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceUpdate, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterSecretManage, "cluster_id"),
		handlers.helmReleaseWrite.upgrade,
	)
	// A rollback replays a revision Helm already stored, which means writing
	// the objects that revision described — creating what is missing and
	// replacing what changed. It is an upgrade to an older shape, so it
	// answers to the same permissions.
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/helm-releases/:release_name/rollback",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterHelmManage, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceCreate, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceUpdate, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterSecretManage, "cluster_id"),
		handlers.helmReleaseWrite.rollback,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/namespaces/:namespace_name/helm-releases/:release_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterHelmManage, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterResourceDelete, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterSecretManage, "cluster_id"),
		handlers.helmReleaseWrite.uninstall,
	)
	// The account of a release change that is still happening.
	//
	// Each of the four routes above answers 202 and leaves the operation
	// running, because a release change takes as long as the rollout it waits
	// for; these are where its progress, its log and finally its outcome are
	// read. `cluster.helm.manage` rather than the read permissions next door:
	// this is not a way to read what is installed, it is the other end of a
	// write, and an operator whose permission to change releases was withdrawn
	// mid-deployment stops being able to watch one too.
	//
	// The record itself is restricted to the operator who started it, which is
	// what makes it safe to leave these two unaudited — see helm_operation.go.
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/helm-operations",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterHelmManage, "cluster_id"),
		handlers.helmOperation.list,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/helm-operations/:operation_id",
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterRead, "cluster_id"),
		handlers.authorizationMiddleware.RequireCluster(rbac.PermissionClusterHelmManage, "cluster_id"),
		handlers.helmOperation.get,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/configmaps",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesConfigMap.list,
	)
	clusterRoutes.POST(
		"/:cluster_id/namespaces/:namespace_name/configmaps",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceCreate,
			"cluster_id",
		),
		handlers.kubernetesConfigMap.create,
	)
	clusterRoutes.GET(
		"/:cluster_id/namespaces/:namespace_name/configmaps/:config_map_name",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesConfigMap.get,
	)
	clusterRoutes.PUT(
		"/:cluster_id/namespaces/:namespace_name/configmaps/:config_map_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceUpdate,
			"cluster_id",
		),
		handlers.kubernetesConfigMap.update,
	)
	clusterRoutes.DELETE(
		"/:cluster_id/namespaces/:namespace_name/configmaps/:config_map_name",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceDelete,
			"cluster_id",
		),
		handlers.kubernetesConfigMap.delete,
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
	clusterRoutes.GET(
		"/:cluster_id/kubernetes/resources/:resource_name/yaml",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.kubernetesYAML.get,
	)
	clusterRoutes.GET(
		"/:cluster_id/kubernetes/resources/:resource_name/describe",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterEventRead,
			"cluster_id",
		),
		handlers.kubernetesDescribe.resource,
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
	clusterRoutes.PUT(
		"/:cluster_id/kubernetes/resources/:resource_name/yaml",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterResourceUpdate,
			"cluster_id",
		),
		handlers.kubernetesYAML.update,
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
	// Manifests use a group of their own for two reasons.
	//
	// The timeout: clusterRoutes installs the ten seconds that bound a
	// single-object write, and a manifest is a bounded number of them in
	// sequence, so that budget would refuse files that were going to succeed.
	//
	// The authorization: every other write in the table names one object of one
	// family in its URL, so `RequireCluster` decides it completely. A manifest
	// carries objects of every family at once, and the families answer to
	// different permissions on purpose — `cluster.secret.manage`,
	// `cluster.rbac.manage`, `cluster.namespace.manage` — so no single route-level
	// permission can decide one. `cluster.read` is the floor: it establishes that
	// the caller may see this Cluster at all, and produces the ordinary denial
	// record for one who may not. What each document actually requires is then
	// resolved by ResolveClusterManifestGrant and checked per document, before
	// anything is written, with the whole request refused if any document is not
	// covered. That is a stricter check than a route-level one, not a weaker one:
	// a manifest holding a Secret is refused for a caller lacking
	// `cluster.secret.manage` no matter what else they hold.
	manifestRoutes := apiV1.Group("/clusters")
	manifestRoutes.Use(
		handlers.manifestRequestTimeout,
		handlers.roleBindingCache,
		handlers.authMiddleware.RequireAuthentication,
	)
	manifestRoutes.POST(
		"/:cluster_id/kubernetes/manifests/apply",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.authorizationMiddleware.ResolveClusterManifestGrant("cluster_id"),
		handlers.kubernetesManifest.apply,
	)
	manifestRoutes.POST(
		"/:cluster_id/kubernetes/manifests/delete",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.authorizationMiddleware.ResolveClusterManifestGrant("cluster_id"),
		handlers.kubernetesManifest.delete,
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
