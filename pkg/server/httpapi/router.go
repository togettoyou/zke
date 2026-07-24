package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

type ReadinessCheck func(context.Context) error

type Dependencies struct {
	ReadinessCheck    ReadinessCheck
	AuthService       *auth.Service
	AuditService      *audit.Service
	RBACService       *rbac.Service
	EnrollmentService *enrollment.Service
}

type Config struct {
	Authentication  AuthenticationConfig
	AgentEnrollment AgentEnrollmentHTTPConfig
}

type handlers struct {
	health                  *healthHandler
	auth                    *authHandler
	enrollment              *enrollmentHandler
	agentRegistration       *agentRegistrationHandler
	authMiddleware          *httpmiddleware.Authentication
	authorizationMiddleware *httpmiddleware.Authorization
	requestTimeout          gin.HandlerFunc
}

var configureGinMode sync.Once

func New(
	logger *slog.Logger,
	dependencies Dependencies,
	config Config,
) http.Handler {
	configureGinMode.Do(func() {
		gin.SetMode(gin.ReleaseMode)
	})

	router := gin.New()
	router.Use(
		httpmiddleware.Recovery(logger),
		httpmiddleware.RequestLogger(logger),
		httpmiddleware.CrossOriginProtection(),
	)

	routeHandlers := handlers{
		health: newHealthHandler(logger, dependencies.ReadinessCheck),
		auth: newAuthHandler(
			logger,
			dependencies.AuthService,
			config.Authentication,
		),
		enrollment: newEnrollmentHandler(
			logger,
			dependencies.EnrollmentService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		agentRegistration: newAgentRegistrationHandler(
			logger,
			dependencies.EnrollmentService,
			config.AgentEnrollment,
		),
		authMiddleware: httpmiddleware.NewAuthentication(
			logger,
			dependencies.AuthService,
			httpmiddleware.AuthenticationConfig{
				CookieSecure:     config.Authentication.CookieSecure,
				OperationTimeout: config.Authentication.OperationTimeout,
			},
		),
		authorizationMiddleware: httpmiddleware.NewAuthorization(
			logger,
			dependencies.RBACService,
			dependencies.AuditService,
			httpmiddleware.AuthorizationConfig{
				OperationTimeout: config.Authentication.OperationTimeout,
			},
		),
		requestTimeout: httpmiddleware.RequestTimeout(
			config.Authentication.OperationTimeout,
		),
	}
	registerRoutes(router, routeHandlers)
	return router
}
