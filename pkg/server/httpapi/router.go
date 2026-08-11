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
	"github.com/togettoyou/zke/pkg/server/clusteroverview"
	"github.com/togettoyou/zke/pkg/server/clusterterminal"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesdescribe"
	"github.com/togettoyou/zke/pkg/server/kubernetesmanifest"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/kubernetesyaml"
	"github.com/togettoyou/zke/pkg/server/podaccess"
	"github.com/togettoyou/zke/pkg/server/podexec"
	"github.com/togettoyou/zke/pkg/server/podlogs"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/resourcemanagement"
	"github.com/togettoyou/zke/pkg/server/resourcewatch"
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
	ClusterOverviewService    *clusteroverview.Service
	KubernetesResourceService *kubernetesresource.Service
	PodLogsService            *podlogs.Service
	PodExecService            *podexec.Service
	ClusterTerminalService    *clusterterminal.Service
	PodAccessService          *podaccess.Service
	ResourceWatchService      *resourcewatch.Service
	ResourceManagementService *resourcemanagement.Service
	AccessManagementService   *accessmanagement.Service
}

type Config struct {
	ConsoleDirectory   string
	AgentNamespace     string
	Authentication     AuthenticationConfig
	AgentEnrollment    AgentEnrollmentHTTPConfig
	ClusterTerminal    ClusterTerminalHTTPConfig
	PodLogs            PodLogsHTTPConfig
	PodExec            PodExecHTTPConfig
	KubernetesEvents   KubernetesEventsHTTPConfig
	KubernetesManifest KubernetesManifestHTTPConfig
}

type handlers struct {
	health                  *healthHandler
	auth                    *authHandler
	enrollment              *enrollmentHandler
	agentRegistration       *agentRegistrationHandler
	agentInstallation       *agentInstallationHandler
	agentManagement         *agentManagementHandler
	agentStatus             *agentStatusHandler
	clusterOverview         *clusterOverviewHandler
	kubernetesNode          *kubernetesNodeHandler
	kubernetesMetrics       *kubernetesMetricsHandler
	kubernetesNamespace     *kubernetesNamespaceHandler
	kubernetesPod           *kubernetesPodHandler
	kubernetesPodLogs       *kubernetesPodLogsHandler
	kubernetesPodExec       *kubernetesPodExecHandler
	clusterTerminal         *clusterTerminalHandler
	kubernetesPodAccess     *kubernetesPodAccessHandler
	kubernetesEvents        *kubernetesEventsHandler
	kubernetesWorkload      *kubernetesWorkloadHandler
	kubernetesNetworking    *kubernetesNetworkingHandler
	kubernetesStorage       *kubernetesStorageHandler
	kubernetesHPA           *kubernetesHorizontalPodAutoscalerHandler
	kubernetesPolicy        *kubernetesPolicyHandler
	kubernetesAuthorization *kubernetesAuthorizationHandler
	kubernetesConfigMap     *kubernetesConfigMapHandler
	kubernetesSecret        *kubernetesSecretHandler
	kubernetesResource      *kubernetesResourceHandler
	kubernetesYAML          *kubernetesYAMLHandler
	kubernetesDescribe      *kubernetesDescribeHandler
	// The families whose YAML is reached through their own permissions, each
	// over a service carrying that family's rules.
	kubernetesAuthorizationYAML *kubernetesAuthorizationYAMLHandler
	kubernetesSecretYAML        *kubernetesSecretYAMLHandler
	// Whole manifests, whose documents may belong to any of the families above
	// and are therefore decided one at a time rather than by the route.
	kubernetesManifest      *kubernetesManifestHandler
	resourceManagement      *resourceManagementHandler
	accessManagement        *accessManagementHandler
	auditQuery              *auditQueryHandler
	authMiddleware          *httpmiddleware.Authentication
	authorizationMiddleware *httpmiddleware.Authorization
	requestTimeout          gin.HandlerFunc
	terminalRequestTimeout  gin.HandlerFunc
	// A manifest is a bounded number of single-object writes in sequence, so it
	// cannot run under the budget that bounds one of them.
	manifestRequestTimeout gin.HandlerFunc
	roleBindingCache       gin.HandlerFunc
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
	console := newConsoleHandler(config.ConsoleDirectory)
	router.NoRoute(func(c *gin.Context) {
		if console.serve(c) {
			return
		}
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

	var yamlService kubernetesYAMLService
	var authorizationYAMLService kubernetesYAMLService
	var secretYAMLService kubernetesYAMLService
	var describeService kubernetesDescribeService
	if dependencies.KubernetesResourceService != nil {
		// A Server with no Resource Watch service still describes: that service
		// reports its own absence, and describe carries the object half of the
		// answer with the Event section marked unavailable.
		describeService = kubernetesdescribe.NewService(
			dependencies.KubernetesResourceService,
			dependencies.ResourceWatchService,
			kubernetesdescribe.Config{},
		)
		yamlService = kubernetesyaml.NewService(
			dependencies.KubernetesResourceService,
			nil,
		)
		// Same accessor, different rules: the authorization families are not
		// refused at the resource layer, so what keeps a YAML edit from writing
		// what the typed API would not is this guard.
		authorizationYAMLService = kubernetesyaml.NewService(
			dependencies.KubernetesResourceService,
			kubernetesresource.AuthorizationManifestGuard,
		)
		// A different accessor, not just different rules: the resource layer
		// refuses a Secret to everything except the Secret service's own access,
		// and this is that access rather than a way around it.
		secretYAMLService = kubernetesyaml.NewService(
			kubernetesresource.NewSecretYAMLAccess(
				dependencies.KubernetesResourceService,
			),
			kubernetesresource.SecretManifestGuard,
		)
	}
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
		clusterOverview: newClusterOverviewHandler(
			logger,
			dependencies.ClusterOverviewService,
			config.Authentication.OperationTimeout,
		),
		kubernetesNode: newKubernetesNodeHandler(
			logger,
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesMetrics: newKubernetesMetricsHandler(
			logger,
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesNamespace: newKubernetesNamespaceHandler(
			logger,
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesPod: newKubernetesPodHandler(
			logger,
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesPodLogs: newKubernetesPodLogsHandler(
			logger,
			dependencies.PodLogsService,
			dependencies.AuthService,
			dependencies.RBACService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
			config.PodLogs,
		),
		kubernetesPodExec: newKubernetesPodExecHandler(
			logger,
			dependencies.PodExecService,
			dependencies.AuthService,
			dependencies.RBACService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
			config.PodExec,
		),
		kubernetesPodAccess: newKubernetesPodAccessHandler(
			logger,
			dependencies.PodAccessService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesEvents: newKubernetesEventsHandler(
			logger,
			dependencies.ResourceWatchService,
			dependencies.AuthService,
			dependencies.RBACService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
			config.KubernetesEvents,
		),
		kubernetesWorkload: newKubernetesWorkloadHandler(
			logger,
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesNetworking: newKubernetesNetworkingHandler(
			logger,
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesStorage: newKubernetesStorageHandler(
			logger,
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesHPA: newKubernetesHorizontalPodAutoscalerHandler(
			logger,
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesPolicy: newKubernetesPolicyHandler(
			logger,
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesAuthorization: newKubernetesAuthorizationHandler(
			logger,
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesSecret: newKubernetesSecretHandler(
			logger,
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesConfigMap: newKubernetesConfigMapHandler(
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
		kubernetesYAML: newKubernetesYAMLHandler(
			logger,
			yamlService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		// Describe holds no accessor of its own either: it reads the object
		// through the resource service and the Events through the same Resource
		// Watch the Event stream uses, so it can reach nothing those two cannot.
		kubernetesDescribe: newKubernetesDescribeHandler(
			logger,
			describeService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesAuthorizationYAML: newKubernetesAuthorizationYAMLHandler(
			logger,
			authorizationYAMLService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		kubernetesSecretYAML: newKubernetesSecretYAMLHandler(
			logger,
			secretYAMLService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
		),
		// The manifest service holds no resource accessor of its own: the access
		// is built per request from the caller's resolved grant, because which
		// families a manifest may touch is the caller's question and not the
		// Server's.
		kubernetesManifest: newKubernetesManifestHandler(
			logger,
			kubernetesmanifest.NewService(kubernetesmanifest.Config{
				MaxDocuments: config.KubernetesManifest.MaxDocuments,
				FieldManager: manifestFieldManager,
			}),
			dependencies.KubernetesResourceService,
			dependencies.AuditService,
			config.Authentication.OperationTimeout,
			config.KubernetesManifest,
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
			dependencies.RBACService,
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
				AgentNamespace:   config.AgentNamespace,
			},
		),
		requestTimeout: httpmiddleware.RequestTimeout(
			config.Authentication.OperationTimeout,
		),
		manifestRequestTimeout: httpmiddleware.RequestTimeout(
			config.KubernetesManifest.RequestTimeout,
		),
		roleBindingCache: httpmiddleware.RoleBindingCache(),
	}
	routeHandlers.clusterTerminal = newClusterTerminalHandler(
		logger, dependencies.ClusterTerminalService, routeHandlers.kubernetesPodExec,
		dependencies.RBACService, dependencies.AuditService, config.Authentication.OperationTimeout,
		config.ClusterTerminal,
	)
	terminalCreationTimeout := config.ClusterTerminal.CreationTimeout
	if terminalCreationTimeout <= 0 {
		terminalCreationTimeout = config.Authentication.OperationTimeout
	}
	routeHandlers.terminalRequestTimeout = httpmiddleware.RequestTimeout(terminalCreationTimeout)
	registerRoutes(router, routeHandlers)
	return router
}
