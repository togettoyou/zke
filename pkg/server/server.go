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

	corev1 "k8s.io/api/core/v1"

	"github.com/togettoyou/zke/pkg/server/accessmanagement"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/agentinstall"
	"github.com/togettoyou/zke/pkg/server/agentmanagement"
	"github.com/togettoyou/zke/pkg/server/agentstatus"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	"github.com/togettoyou/zke/pkg/server/httpapi"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/pki"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/resourcemanagement"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
	runContext, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

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
	agentListenerCACertificateFile :=
		cfg.AgentPKI.External.AgentListenerCA.CertificateFile
	if cfg.AgentPKI.Mode == "managed" {
		managed := cfg.AgentPKI.Managed
		managedFiles, err := pki.Ensure(
			ctx,
			database,
			pki.Config{
				Directory:                managed.Directory,
				AutoGenerate:             managed.AutoGenerate,
				AgentClientCAValidity:    managed.AgentClientCAValidity,
				AgentListenerCAValidity:  managed.AgentListenerCAValidity,
				AgentListenerValidity:    managed.AgentListenerValidity,
				AgentListenerRenewBefore: managed.AgentListenerRenewBefore,
				ListenerDNSNames:         managed.ListenerSANs.DNSNames,
				ListenerIPAddresses:      managed.ListenerSANs.IPAddresses,
			},
			time.Now().UTC(),
		)
		if err != nil {
			return fmt.Errorf("prepare managed Server PKI: %w", err)
		}
		cfg.AgentIdentity.CACertificateFile =
			managedFiles.AgentClientCACertificate
		cfg.AgentIdentity.CAPrivateKeyFile =
			managedFiles.AgentClientCAPrivateKey
		cfg.AgentListener.TLS.CertificateFile =
			managedFiles.AgentListenerCertificate
		cfg.AgentListener.TLS.PrivateKeyFile =
			managedFiles.AgentListenerPrivateKey
		agentListenerCACertificateFile =
			managedFiles.AgentListenerCACertificate
		logServerPKIExpiry(logger, managedFiles.State, cfg.CertificateMonitor.WarningBefore)
	}
	authStore := store.NewAuthStore(database)
	adminContext, cancelAdmin := context.WithTimeout(
		ctx,
		cfg.Auth.OperationTimeout,
	)
	err = bootstrapInitialAdmin(
		adminContext,
		authStore,
		cfg.Auth.InitialAdmin,
		logger,
	)
	cancelAdmin()
	if err != nil {
		return fmt.Errorf("bootstrap initial administrator: %w", err)
	}
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
			TokenTTL:          enrollment.DefaultTokenTTL,
			CertificateSigner: certificateSigner,
		},
	)
	var listenerCACertificate []byte
	var registrationCACertificate []byte
	if cfg.AgentInstall.Enabled {
		listenerCACertificate, err = pki.CertificatePEM(
			agentListenerCACertificateFile,
		)
		if err != nil {
			return fmt.Errorf("read Agent installation Listener CA: %w", err)
		}
		if strings.TrimSpace(cfg.AgentInstall.RegistrationCACertificateFile) != "" {
			registrationCACertificate, err = pki.CertificatePEM(
				cfg.AgentInstall.RegistrationCACertificateFile,
			)
			if err != nil {
				return fmt.Errorf("read Agent installation registration CA: %w", err)
			}
		}
	}
	agentInstallationService := agentinstall.NewService(
		enrollmentService,
		agentinstall.Config{
			Enabled:                      cfg.AgentInstall.Enabled,
			PublicHTTPURL:                cfg.AgentInstall.PublicHTTPURL,
			PublicQUICAddress:            cfg.AgentInstall.PublicQUICAddress,
			Image:                        cfg.AgentInstall.Image,
			Namespace:                    cfg.AgentInstall.Namespace,
			ImagePullPolicy:              agentInstallPullPolicy(cfg.AgentInstall.ImagePullPolicy),
			ListenerCACertificatePEM:     listenerCACertificate,
			RegistrationCACertificatePEM: registrationCACertificate,
		},
	)
	agentConnectionStore := store.NewAgentConnectionStore(database)
	agentConnectionManager, err := agentconn.New(
		agentconn.Config{
			Address:                  cfg.AgentListener.Address,
			TLSCertificateFile:       cfg.AgentListener.TLS.CertificateFile,
			TLSPrivateKeyFile:        cfg.AgentListener.TLS.PrivateKeyFile,
			ClientCACertificateFile:  cfg.AgentIdentity.CACertificateFile,
			HandshakeTimeout:         cfg.AgentListener.HandshakeTimeout,
			HeartbeatInterval:        cfg.AgentListener.HeartbeatInterval,
			HeartbeatTimeout:         cfg.AgentListener.HeartbeatTimeout,
			LastSeenWriteInterval:    cfg.AgentListener.LastSeenWriteInterval,
			OperationTimeout:         cfg.AgentListener.OperationTimeout,
			WriteTimeout:             cfg.AgentListener.WriteTimeout,
			MaxConcurrentAgents:      cfg.AgentListener.MaxConcurrentAgents,
			MaxIncomingStreams:       cfg.AgentListener.MaxIncomingStreams,
			MaxRememberedDisconnects: cfg.AgentListener.MaxRememberedDisconnects,
			ResourceRequestTimeout:   cfg.AgentListener.ResourceRequestTimeout,
			ConnectionDrainTimeout:   cfg.AgentListener.ConnectionDrainTimeout,
			MaxResourceBodyBytes:     cfg.AgentListener.MaxResourceBodyBytes,
			MaxResourceStreams:       cfg.AgentListener.MaxResourceStreams,
			MaxResourceRequests:      cfg.AgentListener.MaxResourceRequests,
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
	agentManagementService := agentmanagement.NewService(
		store.NewAgentManagementStore(database),
		agentConnectionManager,
	)
	agentStatusStore := store.NewAgentStatusStore(database)
	agentStatusService := agentstatus.NewService(
		agentStatusStore,
		agentConnectionManager,
		agentConnectionManager,
		cfg.CertificateMonitor.WarningBefore,
	)
	kubernetesResourceService := kubernetesresource.NewService(
		agentConnectionManager,
		kubernetesresource.Config{
			MaxBufferedResponseBytes: int64(
				cfg.AgentListener.MaxBufferedResourceBytes,
			),
		},
	)
	resourceManagementService := resourcemanagement.NewService(
		store.NewResourceManagementStore(database),
		rbacService,
	)
	accessManagementService := accessmanagement.NewService(
		store.NewAccessManagementStore(database),
		accessmanagement.Config{
			MaxConcurrentPasswordHashes: cfg.Auth.MaxConcurrentPasswordChecks,
		},
	)
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
			KubernetesResourceService: kubernetesResourceService,
			ResourceManagementService: resourceManagementService,
			AccessManagementService:   accessManagementService,
		},
		httpapi.Config{
			Authentication: httpapi.AuthenticationConfig{
				CookieSecure:          cfg.Auth.CookieSecure,
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
			cfg.CertificateMonitor,
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

	serverErrors := make(chan error, 1)
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
	// Wait before returning: the deferred database.Close runs on return, and
	// Agent heartbeats or the certificate monitor may still be mid-query.
	backgroundTasks.Wait()
	if runError == nil && shutdownError == nil {
		logger.Info("server stopped")
	}
	return errors.Join(runError, shutdownError)
}

func agentInstallPullPolicy(value string) corev1.PullPolicy {
	return corev1.PullPolicy(value)
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
	config CertificateMonitorConfig,
) {
	check := func() {
		now := time.Now().UTC()
		operationContext, cancel := context.WithTimeout(ctx, min(config.CheckInterval, time.Minute))
		defer cancel()
		certificates, err := agentStore.ListExpiringAgentCertificates(
			operationContext,
			now.Add(config.WarningBefore),
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
