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

	agentAPIV1 := router.Group("/agent-api/v1")
	agentAPIV1.POST("/enroll", handlers.agentRegistration.enroll)

	agentInstallV1 := router.Group("/agent-install/v1")
	agentInstallV1.GET("/manifest", handlers.agentInstallation.manifest)
}
