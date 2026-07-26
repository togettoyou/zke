package httpapi

import (
	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

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

	userRoutes := apiV1.Group("/users")
	userRoutes.Use(
		handlers.requestTimeout,
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

	auditRoutes := apiV1.Group("/audit-events")
	auditRoutes.Use(
		handlers.requestTimeout,
		handlers.authMiddleware.RequireAuthentication,
	)
	auditRoutes.GET("", handlers.auditQuery.list)

	apiV1.GET(
		"/events",
		handlers.authMiddleware.RequireAuthentication,
		handlers.agentStatus.events,
	)

	tenantRoutes := apiV1.Group("/tenants")
	tenantRoutes.Use(
		handlers.requestTimeout,
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
		handlers.authMiddleware.RequireAuthentication,
	)
	projectRoutes.POST(
		"/:project_id/agent-enrollments",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireProject(
			rbac.PermissionAgentEnrollmentCreate,
			"project_id",
		),
		handlers.enrollment.create,
	)
	projectRoutes.GET(
		"/:project_id/agents",
		handlers.authorizationMiddleware.RequireProject(
			rbac.PermissionAgentRead,
			"project_id",
		),
		handlers.agentStatus.list,
	)
	projectRoutes.POST(
		"/:project_id/agent-installations",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireProject(
			rbac.PermissionAgentEnrollmentCreate,
			"project_id",
		),
		handlers.agentInstallation.create,
	)
	projectRoutes.GET(
		"/:project_id/clusters",
		handlers.authorizationMiddleware.RequireProject(
			rbac.PermissionClusterRead,
			"project_id",
		),
		handlers.resourceManagement.listClusters,
	)

	clusterRoutes := apiV1.Group("/clusters")
	clusterRoutes.Use(
		handlers.requestTimeout,
		handlers.authMiddleware.RequireAuthentication,
	)
	clusterRoutes.GET(
		"/:cluster_id",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionClusterRead,
			"cluster_id",
		),
		handlers.resourceManagement.getCluster,
	)
	clusterRoutes.GET(
		"/:cluster_id/agent",
		handlers.authorizationMiddleware.RequireCluster(
			rbac.PermissionAgentRead,
			"cluster_id",
		),
		handlers.agentStatus.getCluster,
	)

	agentRoutes := apiV1.Group("/agents")
	agentRoutes.Use(
		handlers.requestTimeout,
		handlers.authMiddleware.RequireAuthentication,
	)
	agentRoutes.POST(
		"/:agent_id/revoke",
		handlers.authMiddleware.RequireCSRF,
		handlers.authorizationMiddleware.RequireAgent(
			rbac.PermissionAgentRevoke,
			"agent_id",
		),
		handlers.agentManagement.revoke,
	)

	agentAPIV1 := router.Group("/agent-api/v1")
	agentAPIV1.POST("/enroll", handlers.agentRegistration.enroll)

	agentInstallV1 := router.Group("/agent-install/v1")
	agentInstallV1.GET("/manifest", handlers.agentInstallation.manifest)
}
