package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/accessmanagement"
	"github.com/togettoyou/zke/pkg/server/agentinstall"
	"github.com/togettoyou/zke/pkg/server/agentmanagement"
	"github.com/togettoyou/zke/pkg/server/agentstatus"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/resourcemanagement"
)

type ReadinessCheck func(context.Context) error

type Dependencies struct {
	ReadinessCheck            ReadinessCheck
	AuthService               *auth.Service
	AuditService              *audit.Service
	RBACService               *rbac.Service
	EnrollmentService         *enrollment.Service
	AgentInstallationService  *agentinstall.Service
	AgentManagementService    *agentmanagement.Service
	AgentStatusService        *agentstatus.Service
	KubernetesResourceService *kubernetesresource.Service
	ResourceManagementService *resourcemanagement.Service
	AccessManagementService   *accessmanagement.Service
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
	agentInstallation       *agentInstallationHandler
	agentManagement         *agentManagementHandler
	agentStatus             *agentStatusHandler
	kubernetesNode          *kubernetesNodeHandler
	kubernetesNamespace     *kubernetesNamespaceHandler
	kubernetesWorkload      *kubernetesWorkloadHandler
	kubernetesResource      *kubernetesResourceHandler
	resourceManagement      *resourceManagementHandler
	accessManagement        *accessManagementHandler
	auditQuery              *auditQueryHandler
	authMiddleware          *httpmiddleware.Authentication
	authorizationMiddleware *httpmiddleware.Authorization
	requestTimeout          gin.HandlerFunc
	roleBindingCache        gin.HandlerFunc
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
	router.HandleMethodNotAllowed = true
	router.RedirectTrailingSlash = false
	router.Use(
		httpmiddleware.Recovery(logger),
		httpmiddleware.RequestLogger(logger),
		httpmiddleware.CrossOriginProtection(),
	)
	router.NoRoute(func(c *gin.Context) {
		writeError(c, http.StatusNotFound, "not_found", "route not found")
	})
	router.NoMethod(func(c *gin.Context) {
		writeError(
			c,
			http.StatusMethodNotAllowed,
			"method_not_allowed",
			"method not allowed",
		)
	})

	routeHandlers := handlers{
		health: newHealthHandler(logger, dependencies.ReadinessCheck),
		auth: newAuthHandler(
			logger,
			dependencies.AuthService,
			dependencies.RBACService,
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
		agentInstallation: newAgentInstallationHandler(
			logger,
			dependencies.AgentInstallationService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
			config.AgentEnrollment,
		),
		agentManagement: newAgentManagementHandler(
			logger,
			dependencies.AgentManagementService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		agentStatus: newAgentStatusHandler(
			logger,
			dependencies.AgentStatusService,
			dependencies.AuthService,
			dependencies.RBACService,
			config.Authentication.OperationTimeout,
		),
		kubernetesNode: newKubernetesNodeHandler(
			logger,
			dependencies.KubernetesResourceService,
			config.Authentication.OperationTimeout,
		),
		kubernetesNamespace: newKubernetesNamespaceHandler(
			logger,
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesWorkload: newKubernetesWorkloadHandler(
			logger,
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesResource: newKubernetesResourceHandler(
			logger,
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		resourceManagement: newResourceManagementHandler(
			logger,
			dependencies.ResourceManagementService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		accessManagement: newAccessManagementHandler(
			logger,
			dependencies.AccessManagementService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		auditQuery: newAuditQueryHandler(
			logger,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
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
		roleBindingCache: httpmiddleware.RoleBindingCache(),
	}
	registerRoutes(router, routeHandlers)
	return router
}
