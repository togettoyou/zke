package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/togettoyou/zke/pkg/server/accessmanagement"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/agentinstall"
	"github.com/togettoyou/zke/pkg/server/agentmanagement"
	"github.com/togettoyou/zke/pkg/server/agentstatus"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/clusteroverview"
	"github.com/togettoyou/zke/pkg/server/clusterterminal"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	"github.com/togettoyou/zke/pkg/server/httpapi"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/metricscollector"
	"github.com/togettoyou/zke/pkg/server/metricsingest"
	"github.com/togettoyou/zke/pkg/server/metricsquery"
	"github.com/togettoyou/zke/pkg/server/pki"
	"github.com/togettoyou/zke/pkg/server/platformsettings"
	"github.com/togettoyou/zke/pkg/server/podaccess"
	"github.com/togettoyou/zke/pkg/server/podexec"
	"github.com/togettoyou/zke/pkg/server/podlogs"
	"github.com/togettoyou/zke/pkg/server/podportforward"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/resourcemanagement"
	"github.com/togettoyou/zke/pkg/server/resourcewatch"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

// Most documents one manifest request may carry.
//
// Every document is a separate round trip to the Cluster's Agent, and they run
// in sequence, so this is what keeps one operator's file from occupying that
// Cluster's resource stream for as long as the file is long. It is generous
// enough for the manifests operators actually paste — a Helm-rendered chart of a
// single application sits well inside it — and a file larger than this is one
// that belongs in a pipeline rather than in a console form.
const maxManifestDocuments = 64

const (
	loopbackEndpointProfileID     = "00000000-0000-0000-0000-000000000010"
	desktopEndpointProfileID      = "00000000-0000-0000-0000-000000000011"
	deploymentDefaultEndpointID   = "00000000-0000-0000-0000-000000000012"
	deploymentDefaultEndpointName = "部署配置默认端点"
)

func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
	runContext, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	if err := validateConsoleDirectory(cfg.HTTP.ConsoleDirectory); err != nil {
		return err
	}

	databaseContext, cancelDatabase := context.WithTimeout(ctx, cfg.Database.ConnectTimeout)
	database, err := store.Open(databaseContext, store.PoolConfig{
		URL:             cfg.Database.URL,
		MaxConnections:  cfg.Database.MaxConnections,
		MinConnections:  cfg.Database.MinConnections,
		MaxConnLifetime: cfg.Database.MaxConnLifetime,
		MaxConnIdleTime: cfg.Database.MaxConnIdleTime,
	})
	cancelDatabase()
	if err != nil {
		return err
	}
	defer database.Close()

	migrationContext, cancelMigration := context.WithTimeout(ctx, cfg.Database.MigrationTimeout)
	migrationResult, err := migrations.Apply(migrationContext, database)
	cancelMigration()
	if err != nil {
		return fmt.Errorf("migrate PostgreSQL database: %w", err)
	}
	logger.Info("database schema ready",
		slog.Int64("current_version", migrationResult.CurrentVersion),
		slog.Int("applied_count", len(migrationResult.AppliedVersions)),
	)
	platformSettingsStore := store.NewPlatformSettingsStore(database)
	publicHTTPURL, publicQUICAddress := cfg.AgentInstall.EffectiveEndpoint()
	defaultEndpoint, err := platformSettingsStore.ReconcileDefaultEndpoint(ctx, store.ReconcileDefaultEndpointParams{
		ReservedID: deploymentDefaultEndpointID, ReservedName: deploymentDefaultEndpointName,
		PresetProfileIDs: []string{loopbackEndpointProfileID, desktopEndpointProfileID},
		RegistrationURL:  publicHTTPURL, QUICAddress: publicQUICAddress, Now: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("reconcile platform default Agent endpoint: %w", err)
	}
	logger.Info("platform default Agent endpoint ready",
		slog.String("profile_id", defaultEndpoint.ID),
		slog.String("registration_url", defaultEndpoint.RegistrationURL),
		slog.String("quic_address", defaultEndpoint.QUICAddress),
	)
	listenerDNSNames, listenerIPAddresses, err := platformsettings.DesiredListenerSANs(ctx, platformSettingsStore)
	if err != nil {
		return fmt.Errorf("load Agent endpoint SANs: %w", err)
	}
	pkiSettings := cfg.AgentPKI
	managedPKIConfig := pki.Config{
		Directory:                pkiSettings.Directory,
		AgentClientCAValidity:    pkiSettings.ClientCAValidity,
		AgentListenerCAValidity:  pkiSettings.ListenerCAValidity,
		AgentListenerValidity:    pkiSettings.ListenerValidity,
		AgentListenerRenewBefore: pkiSettings.ListenerRenewBefore,
		ListenerDNSNames:         listenerDNSNames, ListenerIPAddresses: listenerIPAddresses,
	}
	managedFiles, err := pki.Ensure(
		ctx,
		database,
		managedPKIConfig,
		time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("prepare managed Server PKI: %w", err)
	}
	cfg.AgentIdentity.CACertificateFile = managedFiles.AgentClientCACertificate
	cfg.AgentIdentity.CAPrivateKeyFile = managedFiles.AgentClientCAPrivateKey
	cfg.AgentListener.TLS.CertificateFile = managedFiles.AgentListenerCertificate
	cfg.AgentListener.TLS.PrivateKeyFile = managedFiles.AgentListenerPrivateKey
	listenerCertificateReloader, err := agentconn.NewTLSCertificateReloader(
		managedFiles.AgentListenerCertificate,
		managedFiles.AgentListenerPrivateKey,
	)
	if err != nil {
		return err
	}
	logServerPKIExpiry(logger, managedFiles.State, cfg.AgentPKI.Monitor.WarnBefore)
	platformSettingsService := platformsettings.NewService(
		platformSettingsStore,
		managedFiles.AgentListenerCertificate,
		func(ctx context.Context, dnsNames, ipAddresses []string, now time.Time) error {
			reconcileConfig := managedPKIConfig
			reconcileConfig.ListenerDNSNames = dnsNames
			reconcileConfig.ListenerIPAddresses = ipAddresses
			if _, err := pki.Ensure(ctx, database, reconcileConfig, now); err != nil {
				return fmt.Errorf("reconcile Agent Listener certificate: %w", err)
			}
			if err := listenerCertificateReloader.Reload(); err != nil {
				return fmt.Errorf("activate Agent Listener certificate: %w", err)
			}
			return nil
		},
	)
	// Before anything can authorize. `admin` means "every permission the Server defines", so
	// a release that adds one widens that row here — leaving it to the migration
	// that created the row would mean the new permission reached nobody, and a
	// permission granted to nobody reads exactly like a denial.
	roleContext, cancelRoles := context.WithTimeout(
		ctx,
		cfg.Auth.OperationTimeout,
	)
	err = reconcileBuiltinRoles(
		roleContext,
		store.NewAccessManagementStore(database),
		time.Now().UTC(),
	)
	cancelRoles()
	if err != nil {
		return fmt.Errorf("reconcile builtin roles: %w", err)
	}
	authStore := store.NewAuthStore(database)
	certificateSigner, err := loadAgentCertificateSigner(cfg.AgentIdentity)
	if err != nil {
		return err
	}

	authenticationService := auth.NewService(
		authStore,
		auth.ServiceConfig{
			SessionIdleTimeout:          cfg.Auth.SessionIdleTimeout,
			SessionAbsoluteTimeout:      cfg.Auth.SessionAbsoluteTimeout,
			MaxConcurrentPasswordChecks: cfg.Auth.MaxConcurrentPasswordChecks,
			MaxFailedLoginAttempts:      cfg.Auth.AccountLockout.MaxFailedAttempts,
			AccountLockDuration:         cfg.Auth.AccountLockout.Duration,
		},
	)
	rbacService := rbac.NewService(store.NewRBACStore(database))
	auditService := audit.NewService(store.NewAuditStore(database), rbacService)
	enrollmentService := enrollment.NewService(
		store.NewEnrollmentStore(database),
		enrollment.ServiceConfig{
			TokenTTL:              enrollment.DefaultTokenTTL,
			CertificateSigner:     certificateSigner,
			ConfigurationResolver: platformSettingsService,
		},
	)
	listenerCACertificate, err := pki.CertificatePEM(managedFiles.AgentListenerCACertificate)
	if err != nil {
		return fmt.Errorf("read Agent installation Listener CA: %w", err)
	}
	agentInstallationService := agentinstall.NewService(
		enrollmentService,
		agentinstall.Config{
			ListenerCACertificatePEM: listenerCACertificate,
		},
	)
	// The ingest gateway comes first because both the read side and the
	// collector status report what it knows: a Cluster the Server is refusing
	// must be able to say so on the chart and next to the collector, not only in
	// the Server's own log.
	metricsGateway, err := newMetricsGateway(cfg, logger)
	if err != nil {
		return err
	}
	metricsQueryService, err := newMetricsQueryService(
		cfg,
		rbacService,
		store.NewMetricsScopeStore(database),
		metricsGateway,
		logger,
	)
	if err != nil {
		return err
	}
	agentConnectionStore := store.NewAgentConnectionStore(database)
	// A typed nil pointer in an interface is not nil, and the connection manager
	// reads a nil sink as "this Server stores no metrics", so the conversion has
	// to be explicit.
	var metricsSink agentconn.MetricsSink
	if metricsGateway != nil {
		metricsSink = metricsGateway
	}
	agentConnectionManager, err := agentconn.New(
		agentconn.Config{
			Address:                      cfg.AgentListener.Address,
			TLSCertificateFile:           cfg.AgentListener.TLS.CertificateFile,
			TLSPrivateKeyFile:            cfg.AgentListener.TLS.PrivateKeyFile,
			TLSCertificateReloader:       listenerCertificateReloader,
			ClientCACertificateFile:      cfg.AgentIdentity.CACertificateFile,
			HandshakeTimeout:             cfg.AgentListener.HandshakeTimeout,
			HeartbeatInterval:            cfg.AgentListener.HeartbeatInterval,
			HeartbeatTimeout:             cfg.AgentListener.HeartbeatTimeout,
			LastSeenWriteInterval:        cfg.AgentListener.LastSeenWriteInterval,
			OperationTimeout:             cfg.AgentListener.OperationTimeout,
			WriteTimeout:                 cfg.AgentListener.WriteTimeout,
			MaxConcurrentAgents:          cfg.AgentListener.MaxConcurrentAgents,
			MaxIncomingStreams:           cfg.AgentListener.MaxIncomingStreams,
			MaxRememberedDisconnects:     cfg.AgentListener.MaxRememberedDisconnects,
			ResourceRequestTimeout:       cfg.AgentListener.ResourceRequestTimeout,
			ConnectionDrainTimeout:       cfg.AgentListener.ConnectionDrainTimeout,
			MaxResourceBodyBytes:         cfg.AgentListener.MaxResourceBodyBytes,
			MaxResourceStreams:           cfg.AgentListener.MaxResourceStreams,
			MaxResourceRequests:          cfg.AgentListener.MaxResourceRequests,
			PodLogsRequestTimeout:        cfg.AgentListener.PodLogsRequestTimeout,
			MaxPodLogBytes:               cfg.AgentListener.MaxPodLogBytes,
			MaxPodLogsStreams:            cfg.AgentListener.MaxPodLogsStreams,
			MaxPodLogsRequests:           cfg.AgentListener.MaxPodLogsRequests,
			PodExecRequestTimeout:        cfg.AgentListener.PodExecRequestTimeout,
			MaxPodExecInputBytes:         cfg.AgentListener.MaxPodExecInputBytes,
			MaxPodExecOutputBytes:        cfg.AgentListener.MaxPodExecOutputBytes,
			MaxPodExecStreams:            cfg.AgentListener.MaxPodExecStreams,
			MaxPodExecRequests:           cfg.AgentListener.MaxPodExecRequests,
			PodPortForwardRequestTimeout: cfg.PodAccess.SessionTTL,
			MaxPodPortForwardClientBytes: cfg.PodAccess.MaxClientBytes,
			MaxPodPortForwardPodBytes:    cfg.PodAccess.MaxPodBytes,
			MaxPodPortForwardStreams:     cfg.PodAccess.MaxConnectionsPerAgent,
			MaxPodPortForwardRequests:    cfg.PodAccess.MaxConnections,
			ResourceWatchRequestTimeout:  cfg.AgentListener.ResourceWatchRequestTimeout,
			MaxResourceWatchStreams:      cfg.AgentListener.MaxResourceWatchStreams,
			MaxResourceWatchRequests:     cfg.AgentListener.MaxResourceWatchRequests,
			MetricsIngestTimeout:         cfg.Observability.Metrics.IngestSessionTimeout,
			MaxMetricsBatchBytes:         cfg.Observability.Metrics.MaxBatchBytes,
			MaxMetricsIngestStreams:      cfg.Observability.Metrics.MaxIngestStreams,
			MetricsSink:                  metricsSink,
		},
		logger,
		agentConnectionStore,
		enrollment.NewCertificateRenewalService(
			agentConnectionStore,
			certificateSigner,
		),
	)
	if err != nil {
		return err
	}
	metricsCollectorService, err := newMetricsCollectorService(
		cfg,
		agentConnectionManager,
		platformCollectorSettings{service: platformSettingsService},
		metricsGateway,
	)
	if err != nil {
		return err
	}
	agentManagementService := agentmanagement.NewService(
		store.NewAgentManagementStore(database),
		agentConnectionManager,
	)
	agentStatusStore := store.NewAgentStatusStore(database)
	agentStatusService := agentstatus.NewService(
		agentStatusStore,
		agentConnectionManager,
		agentConnectionManager,
		cfg.AgentPKI.Monitor.WarnBefore,
	)
	kubernetesResourceService := kubernetesresource.NewService(
		agentConnectionManager,
		kubernetesresource.Config{
			MaxBufferedResponseBytes: int64(
				cfg.AgentListener.MaxBufferedResourceBytes,
			),
		},
	)
	clusterOverviewService := clusteroverview.NewService(
		kubernetesResourceService,
	)
	podLogsService := podlogs.NewService(
		agentConnectionManager,
		podlogs.Config{MaxBytes: cfg.AgentListener.MaxPodLogBytes},
	)
	podExecService := podexec.NewService(
		agentConnectionManager,
		podexec.NewPostgresRecordingStore(database),
		podexec.Config{
			SessionTTL:     cfg.AgentListener.PodExecSessionTTL,
			MaxPending:     cfg.AgentListener.MaxPendingPodExecSessions,
			MaxInputBytes:  cfg.AgentListener.MaxPodExecInputBytes,
			MaxOutputBytes: cfg.AgentListener.MaxPodExecOutputBytes,
		},
	)
	clusterTerminalService := clusterterminal.NewService(
		agentConnectionManager,
		podExecService,
		clusterterminal.Config{
			ResolveRuntime: func(ctx context.Context, clusterID string) (clusterterminal.RuntimeConfig, error) {
				settings, _, err := platformSettingsService.Get(ctx)
				if err != nil {
					return clusterterminal.RuntimeConfig{}, err
				}
				clusterScope, err := rbacService.ResolveClusterScope(ctx, clusterID)
				if err != nil {
					return clusterterminal.RuntimeConfig{}, err
				}
				return clusterterminal.RuntimeConfig{
					Image: settings.ClusterTerminalImage, ImagePullPolicy: settings.ClusterTerminalImagePullPolicy,
					Namespace: clusterScope.AgentNamespace, TTL: settings.ClusterTerminalSessionTTL,
				}, nil
			},
		},
	)
	podPortForwardService := podportforward.NewService(agentConnectionManager)
	var podAccessService *podaccess.Service
	if cfg.PodAccess.Enabled {
		podAccessService, err = podaccess.NewService(
			runContext,
			logger,
			authenticationService,
			rbacService,
			auditService,
			podPortForwardService,
			podaccess.Config{
				Enabled:                  true,
				ExternalURL:              cfg.PodAccess.ExternalURL,
				ActivationTTL:            cfg.PodAccess.ActivationTTL,
				MaxSessionTTL:            cfg.PodAccess.SessionTTL,
				RevalidateInterval:       cfg.PodAccess.RevalidateInterval,
				OperationTimeout:         cfg.Auth.OperationTimeout,
				IdleConnectionTimeout:    cfg.PodAccess.IdleTimeout,
				MaxPending:               cfg.PodAccess.MaxPendingSessions,
				MaxActive:                cfg.PodAccess.MaxActiveSessions,
				MaxConnections:           cfg.PodAccess.MaxConnections,
				MaxConnectionsPerSession: cfg.PodAccess.MaxConnectionsPerSession,
				MaxClientBytes:           cfg.PodAccess.MaxClientBytes,
				MaxPodBytes:              cfg.PodAccess.MaxPodBytes,
			},
		)
		if err != nil {
			return fmt.Errorf("configure Pod Access service: %w", err)
		}
	}
	resourceWatchService := resourcewatch.NewService(agentConnectionManager)
	resourceManagementService := resourcemanagement.NewService(
		store.NewResourceManagementStore(database),
		rbacService,
	)
	// The authority is what stops `rbac.manage` from being a way to grant
	// yourself every other permission, so it is supplied at construction and
	// role management refuses to write without it.
	accessManagementService := accessmanagement.NewService(
		store.NewAccessManagementStore(database),
		accessmanagement.Config{
			MaxConcurrentPasswordHashes: cfg.Auth.MaxConcurrentPasswordChecks,
		},
	).WithPermissionAuthority(rbacService)
	defaultEndpointProfileID, readyEndpointProfiles, err := platformSettingsService.ListReadyProfiles(ctx)
	if err != nil {
		return fmt.Errorf("resolve default Agent endpoint: %w", err)
	}
	var defaultRegistrationURL string
	for _, profile := range readyEndpointProfiles {
		if profile.ID == defaultEndpointProfileID {
			defaultRegistrationURL = profile.RegistrationURL
			break
		}
	}
	if defaultRegistrationURL == "" {
		return fmt.Errorf("resolve default Agent endpoint: default profile is not ready")
	}
	handler := httpapi.New(
		logger,
		httpapi.Dependencies{
			ReadinessCheck:            database.Ping,
			AuthService:               authenticationService,
			AuditService:              auditService,
			RBACService:               rbacService,
			EnrollmentService:         enrollmentService,
			AgentInstallationService:  agentInstallationService,
			AgentManagementService:    agentManagementService,
			AgentStatusService:        agentStatusService,
			ClusterOverviewService:    clusterOverviewService,
			MetricsCollectorService:   metricsCollectorService,
			MetricsQueryService:       metricsQueryService,
			KubernetesResourceService: kubernetesResourceService,
			PodLogsService:            podLogsService,
			PodExecService:            podExecService,
			ClusterTerminalService:    clusterTerminalService,
			PodAccessService:          podAccessService,
			ResourceWatchService:      resourceWatchService,
			ResourceManagementService: resourceManagementService,
			AccessManagementService:   accessManagementService,
			PlatformSettingsService:   platformSettingsService,
		},
		httpapi.Config{
			ConsoleDirectory: cfg.HTTP.ConsoleDirectory,
			Authentication: httpapi.AuthenticationConfig{
				OperationTimeout:      cfg.Auth.OperationTimeout,
				LoginRateLimitWindow:  cfg.Auth.LoginRateLimit.Window,
				MaxAttemptsPerAccount: cfg.Auth.LoginRateLimit.MaxAttemptsPerAccount,
				MaxAttemptsPerSource:  cfg.Auth.LoginRateLimit.MaxAttemptsPerSource,
			},
			AgentEnrollment: httpapi.AgentEnrollmentHTTPConfig{
				OperationTimeout:     cfg.AgentEnrollment.OperationTimeout,
				RateLimitWindow:      cfg.AgentEnrollment.RateLimit.Window,
				MaxAttemptsPerSource: cfg.AgentEnrollment.RateLimit.MaxAttemptsPerSource,
			},
			ClusterTerminal: httpapi.ClusterTerminalHTTPConfig{
				CreationTimeout: cfg.AgentListener.ResourceRequestTimeout,
			},
			PodLogs: httpapi.PodLogsHTTPConfig{
				SnapshotTimeout:       cfg.Auth.OperationTimeout,
				MaximumFollowDuration: cfg.AgentListener.PodLogsRequestTimeout,
				WriteTimeout:          cfg.AgentListener.WriteTimeout,
			},
			PodExec: httpapi.PodExecHTTPConfig{
				MaximumDuration: cfg.AgentListener.PodExecRequestTimeout,
				WriteTimeout:    cfg.AgentListener.WriteTimeout,
				PublicHTTPURL:   defaultRegistrationURL,
			},
			KubernetesEvents: httpapi.KubernetesEventsHTTPConfig{
				SnapshotTimeout:       cfg.Auth.OperationTimeout,
				MaximumFollowDuration: cfg.AgentListener.ResourceWatchRequestTimeout,
				WriteTimeout:          cfg.AgentListener.WriteTimeout,
			},
			// A manifest runs its documents one at a time, so it is bounded by
			// the Agent's own resource request timeout rather than by the budget
			// for a single write, and by a document count that keeps the total
			// finite.
			KubernetesManifest: httpapi.KubernetesManifestHTTPConfig{
				RequestTimeout: cfg.AgentListener.ResourceRequestTimeout,
				MaxDocuments:   maxManifestDocuments,
			},
		},
	)
	// Background tasks are tracked so that the deferred database.Close below
	// only runs once nothing can still issue queries against the pool.
	var backgroundTasks sync.WaitGroup
	backgroundTasks.Add(1)
	go func() {
		defer backgroundTasks.Done()
		monitorAgentCertificates(
			runContext,
			logger,
			agentStatusStore,
			cfg.AgentPKI.Monitor,
		)
	}()
	backgroundTasks.Add(1)
	go func() {
		defer backgroundTasks.Done()
		sweepRetainedRecords(
			runContext,
			logger,
			store.NewRetentionStore(database),
			cfg.Retention,
		)
	}()
	httpServer := &http.Server{
		Addr:              cfg.HTTP.Address,
		Handler:           handler,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		BaseContext: func(net.Listener) context.Context {
			return runContext
		},
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	var podAccessServer *http.Server
	if podAccessService != nil {
		podAccessServer = &http.Server{
			Addr:              cfg.PodAccess.Address,
			Handler:           podAccessService,
			ReadHeaderTimeout: cfg.PodAccess.ReadHeaderTimeout,
			IdleTimeout:       cfg.PodAccess.IdleTimeout,
			MaxHeaderBytes:    64 * 1024,
			BaseContext: func(net.Listener) context.Context {
				return runContext
			},
			TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		}
	}

	serverErrors := make(chan error, 2)
	backgroundTasks.Add(1)
	go func() {
		defer backgroundTasks.Done()
		tlsEnabled := strings.TrimSpace(cfg.HTTP.TLS.CertificateFile) != ""
		scheme := "http"
		if tlsEnabled {
			scheme = "https"
		}
		logger.Info(
			"HTTP server starting",
			slog.String("address", cfg.HTTP.Address),
			slog.String("scheme", scheme),
		)
		if tlsEnabled {
			serverErrors <- httpServer.ListenAndServeTLS(
				cfg.HTTP.TLS.CertificateFile,
				cfg.HTTP.TLS.PrivateKeyFile,
			)
			return
		}
		serverErrors <- httpServer.ListenAndServe()
	}()
	if podAccessServer != nil {
		backgroundTasks.Add(1)
		go func() {
			defer backgroundTasks.Done()
			tlsEnabled := strings.TrimSpace(cfg.PodAccess.TLS.CertificateFile) != ""
			scheme := "http"
			if tlsEnabled {
				scheme = "https"
			}
			logger.Info("Pod Access server starting",
				slog.String("address", cfg.PodAccess.Address),
				slog.String("external_url", cfg.PodAccess.ExternalURL),
				slog.String("scheme", scheme),
			)
			var serveErr error
			if tlsEnabled {
				serveErr = podAccessServer.ListenAndServeTLS(
					cfg.PodAccess.TLS.CertificateFile,
					cfg.PodAccess.TLS.PrivateKeyFile,
				)
			} else {
				serveErr = podAccessServer.ListenAndServe()
			}
			if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				serverErrors <- fmt.Errorf("serve Pod Access HTTP: %w", serveErr)
				return
			}
			serverErrors <- serveErr
		}()
	}
	agentErrors := make(chan error, 1)
	backgroundTasks.Add(1)
	go func() {
		defer backgroundTasks.Done()
		agentErrors <- agentConnectionManager.Run(runContext)
	}()

	var runError error
	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			runError = fmt.Errorf("serve HTTP: %w", err)
		}
	case err := <-agentErrors:
		runError = err
		if err == nil {
			runError = errors.New("Agent QUIC listener stopped unexpectedly")
		}
	case <-ctx.Done():
		logger.Info("server shutdown requested")
	}

	cancelRun()
	shutdownError := shutdownHTTPServer(httpServer, cfg.ShutdownTimeout)
	if podAccessServer != nil {
		shutdownError = errors.Join(shutdownError, shutdownHTTPServer(podAccessServer, cfg.ShutdownTimeout))
	}
	// Wait before returning: the deferred database.Close runs on return, and
	// Agent heartbeats or the certificate monitor may still be mid-query.
	backgroundTasks.Wait()
	if runError == nil && shutdownError == nil {
		logger.Info("server stopped")
	}
	return errors.Join(runError, shutdownError)
}

func logServerPKIExpiry(
	logger *slog.Logger,
	state store.ServerPKIState,
	warningBefore time.Duration,
) {
	now := time.Now().UTC()
	for _, item := range []struct {
		name      string
		expiresAt time.Time
	}{
		{"agent_client_ca", state.AgentClientCAExpiresAt},
		{"agent_listener_ca", state.AgentListenerCAExpiresAt},
		{"agent_listener_certificate", state.AgentListenerCertificateExpiresAt},
	} {
		remaining := item.expiresAt.Sub(now)
		if remaining <= warningBefore {
			logger.Warn(
				"Server PKI certificate is approaching expiry",
				slog.String("certificate", item.name),
				slog.Time("expires_at", item.expiresAt),
				slog.Duration("remaining", remaining),
			)
		}
	}
}

func monitorAgentCertificates(
	ctx context.Context,
	logger *slog.Logger,
	agentStore *store.AgentStatusStore,
	config AgentPKIMonitorConfig,
) {
	check := func() {
		now := time.Now().UTC()
		operationContext, cancel := context.WithTimeout(ctx, min(config.CheckInterval, time.Minute))
		defer cancel()
		certificates, err := agentStore.ListExpiringAgentCertificates(
			operationContext,
			now.Add(config.WarnBefore),
		)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("check expiring Agent certificates", slog.String("error", err.Error()))
			}
			return
		}
		for _, certificate := range certificates {
			logger.Warn(
				"Agent certificate is approaching expiry",
				slog.String("tenant_id", certificate.TenantID),
				slog.String("project_id", certificate.ProjectID),
				slog.String("cluster_id", certificate.ClusterID),
				slog.String("agent_id", certificate.AgentID),
				slog.Time("expires_at", certificate.CertificateExpiresAt),
				slog.Duration("remaining", certificate.CertificateExpiresAt.Sub(now)),
			)
		}
	}
	check()
	ticker := time.NewTicker(config.CheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

// sweepRetainedRecords reclaims finished rows on an interval.
//
// It runs once at startup rather than waiting out the first interval: a Server
// that is restarted more often than the interval would otherwise never sweep at
// all, which is the deployment most likely to be accumulating rows.
//
// A failing sweep is logged and the loop continues. Nothing downstream depends
// on it having run -- the rows it removes are already unusable -- so a database
// that is briefly unavailable should not end the task for the process lifetime.
func sweepRetainedRecords(
	ctx context.Context,
	logger *slog.Logger,
	retentionStore *store.RetentionStore,
	config RetentionConfig,
) {
	sweep := func() {
		operationContext, cancel := context.WithTimeout(ctx, min(config.SweepInterval, time.Minute))
		defer cancel()
		result, err := retentionStore.Sweep(
			operationContext,
			store.RetentionPolicy{
				Sessions:    config.Sessions,
				Enrollments: config.Enrollments,
				Credentials: config.Credentials,
			},
			time.Now().UTC(),
		)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("sweep retained records", slog.String("error", err.Error()))
			}
			return
		}
		total := result.Sessions + result.Enrollments +
			result.EnrollmentAttempts + result.Credentials
		if total == 0 {
			return
		}
		logger.Info(
			"swept retained records",
			slog.Int64("user_sessions", result.Sessions),
			slog.Int64("enrollments", result.Enrollments),
			slog.Int64("enrollment_attempts", result.EnrollmentAttempts),
			slog.Int64("agent_credentials", result.Credentials),
		)
	}
	sweep()
	ticker := time.NewTicker(config.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

// platformCollectorSettings reads the collector's image and resource budget at
// the moment they are needed rather than at startup: they are platform
// settings, so an operator can change them and the next install must use the
// new values without a restart.
type platformCollectorSettings struct {
	service *platformsettings.Service
}

func (source platformCollectorSettings) CollectorSettings(
	ctx context.Context,
) (metricscollector.CollectorSettings, error) {
	settings, _, err := source.service.Get(ctx)
	if err != nil {
		return metricscollector.CollectorSettings{}, err
	}
	return metricscollector.CollectorSettings{
		Collector: metricscollector.ComponentSettings{
			Image:           settings.MetricsCollectorImage,
			ImagePullPolicy: settings.MetricsCollectorImagePullPolicy,
			CPURequest:      settings.MetricsCollectorCPURequest,
			MemoryRequest:   settings.MetricsCollectorMemoryRequest,
			CPULimit:        settings.MetricsCollectorCPULimit,
			MemoryLimit:     settings.MetricsCollectorMemoryLimit,
		},
		KubeStateMetrics: metricscollector.ComponentSettings{
			Image:           settings.KubeStateMetricsImage,
			ImagePullPolicy: settings.KubeStateMetricsImagePullPolicy,
			CPURequest:      settings.KubeStateMetricsCPURequest,
			MemoryRequest:   settings.KubeStateMetricsMemoryRequest,
			CPULimit:        settings.KubeStateMetricsCPULimit,
			MemoryLimit:     settings.KubeStateMetricsMemoryLimit,
		},
		NodeExporter: metricscollector.ComponentSettings{
			Image:           settings.NodeExporterImage,
			ImagePullPolicy: settings.NodeExporterImagePullPolicy,
			CPURequest:      settings.NodeExporterCPURequest,
			MemoryRequest:   settings.NodeExporterMemoryRequest,
			CPULimit:        settings.NodeExporterCPULimit,
			MemoryLimit:     settings.NodeExporterMemoryLimit,
		},
	}, nil
}

// newMetricsCollectorService builds the install/uninstall path. It is nil when
// metrics are disabled, and the handler reports that plainly rather than the
// routes disappearing from a Console built against this Server.
func newMetricsCollectorService(
	cfg Config,
	agents metricscollector.AgentAccess,
	settings metricscollector.SettingsSource,
	gateway *metricsingest.Gateway,
) (*metricscollector.Service, error) {
	metrics := cfg.Observability.Metrics
	if !metrics.Enabled {
		return nil, nil
	}
	var budget metricscollector.IngestBudget
	if gateway != nil {
		budget = gateway
	}
	return metricscollector.NewService(
		metricscollector.Config{
			ScrapeInterval:     metrics.ScrapeInterval,
			BufferSize:         metrics.CollectorBufferSize,
			KubeletMetricsPort: metrics.KubeletMetricsPort,
		},
		agents,
		settings,
		budget,
	)
}

// newMetricsQueryService builds the read side. Like the sink it is nil when
// metrics are disabled, and the handler reports that plainly rather than the
// route disappearing from a Console built against this Server.
func newMetricsQueryService(
	cfg Config,
	rbacService *rbac.Service,
	clusters *store.MetricsScopeStore,
	gateway *metricsingest.Gateway,
	logger *slog.Logger,
) (*metricsquery.Service, error) {
	metrics := cfg.Observability.Metrics
	if !metrics.Enabled {
		return nil, nil
	}
	var budget metricsquery.IngestBudget
	if gateway != nil {
		budget = gateway
	}
	return metricsquery.NewService(
		metricsquery.Config{
			QueryURL:     metrics.StorageQueryURL,
			QueryTimeout: metrics.StorageQueryTimeout,
			MaxClusters:  metrics.MaxQueryClusters,
			MaxPoints:    metrics.MaxQueryPoints,
			MaxSeries:    metrics.MaxQuerySeries,
			MaxRange:     metrics.MaxQueryRange,
			MinStep:      metrics.MinQueryStep,
			Budget:       budget,
		},
		metricsquery.RBACVisibility{Service: rbacService},
		clusters,
		logger,
	)
}

// newMetricsGateway builds the ingest gateway when metrics are enabled. A nil
// gateway is a meaningful value: the Agent connection manager then never
// advertises the ingest capability, so a collecting Agent learns at handshake
// time that this Server stores no metrics.
func newMetricsGateway(
	cfg Config,
	logger *slog.Logger,
) (*metricsingest.Gateway, error) {
	metrics := cfg.Observability.Metrics
	if !metrics.Enabled {
		return nil, nil
	}
	gateway, err := metricsingest.New(
		metricsingest.Config{
			WriteURL:             metrics.StorageWriteURL,
			WriteTimeout:         metrics.StorageWriteTimeout,
			MaxDecompressedBytes: metrics.MaxDecompressedBytes,
			Limits: metricsingest.Limits{
				MaxSeriesPerBatch:  metrics.MaxSeriesPerBatch,
				MaxSamplesPerBatch: metrics.MaxSamplesPerBatch,
				MaxLabelsPerSeries: metrics.MaxLabelsPerSeries,
				MaxLabelNameBytes:  metrics.MaxLabelNameBytes,
				MaxLabelValueBytes: metrics.MaxLabelValueBytes,
				MaxSampleAge:       metrics.MaxSampleAge,
				MaxSampleFuture:    metrics.MaxSampleFuture,
			},
			ClusterLimits: metricsingest.ClusterLimits{
				MaxSamplesPerSecond: metrics.MaxSamplesPerSecondPerCluster,
				SampleBurstWindow:   metrics.SampleBurstWindow,
				MaxActiveSeries:     metrics.MaxActiveSeriesPerCluster,
				ActiveSeriesWindow:  metrics.ActiveSeriesWindow,
			},
		},
		logger,
	)
	if err != nil {
		return nil, err
	}
	logger.Info(
		"metrics ingest enabled",
		slog.String("storage_write_url", metrics.StorageWriteURL),
	)
	return gateway, nil
}

func shutdownHTTPServer(server *http.Server, timeout time.Duration) error {
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), timeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("gracefully shut down HTTP server: %w", err)
	}
	return nil
}

func loadAgentCertificateSigner(
	config AgentIdentityConfig,
) (*enrollment.CertificateSigner, error) {
	certificatePath := strings.TrimSpace(config.CACertificateFile)
	privateKeyPath := strings.TrimSpace(config.CAPrivateKeyFile)
	if certificatePath == "" && privateKeyPath == "" {
		return nil, nil
	}
	certificatePEM, err := os.ReadFile(certificatePath)
	if err != nil {
		return nil, fmt.Errorf("read Agent identity CA certificate: %w", err)
	}
	privateKeyPEM, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read Agent identity CA private key: %w", err)
	}
	signer, err := enrollment.NewCertificateSigner(
		certificatePEM,
		privateKeyPEM,
		config.CertificateTTL,
	)
	if err != nil {
		return nil, fmt.Errorf("configure Agent identity certificate signer: %w", err)
	}
	return signer, nil
}
