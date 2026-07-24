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
}
