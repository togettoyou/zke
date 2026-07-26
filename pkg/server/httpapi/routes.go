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
