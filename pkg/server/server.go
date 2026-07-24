package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/agentinstall"
	"github.com/togettoyou/zke/pkg/server/agentstatus"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	"github.com/togettoyou/zke/pkg/server/httpapi"
	"github.com/togettoyou/zke/pkg/server/pki"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
	runContext, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	databaseContext, cancelDatabase := context.WithTimeout(ctx, cfg.Database.ConnectTimeout)
	database, err := store.Open(databaseContext, cfg.Database.URL)
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
		cfg.AgentPKI.ExternalAgentListenerCACertificate
	if cfg.AgentPKI.Mode == "managed" {
		managedFiles, err := pki.Ensure(
			ctx,
			database,
			pki.Config{
				Directory:                cfg.AgentPKI.Directory,
				AutoGenerate:             cfg.AgentPKI.AutoGenerate,
				AgentClientCAValidity:    cfg.AgentPKI.AgentClientCAValidity,
				AgentListenerCAValidity:  cfg.AgentPKI.AgentListenerCAValidity,
				AgentListenerValidity:    cfg.AgentPKI.AgentListenerValidity,
				AgentListenerRenewBefore: cfg.AgentPKI.AgentListenerRenewBefore,
				ListenerDNSNames:         cfg.AgentPKI.AgentListenerDNSNames,
				ListenerIPAddresses:      cfg.AgentPKI.AgentListenerIPAddresses,
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
		},
	)
	rbacService := rbac.NewService(store.NewRBACStore(database))
	auditService := audit.NewService(store.NewAuditStore(database))
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
	agentStatusStore := store.NewAgentStatusStore(database)
	agentStatusService := agentstatus.NewService(
		agentStatusStore,
		cfg.CertificateMonitor.WarningBefore,
	)
	handler := httpapi.New(
		logger,
		httpapi.Dependencies{
			ReadinessCheck:           database.Ping,
			AuthService:              authenticationService,
			AuditService:             auditService,
			RBACService:              rbacService,
			EnrollmentService:        enrollmentService,
			AgentInstallationService: agentInstallationService,
			AgentStatusService:       agentStatusService,
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
	agentConnectionStore := store.NewAgentConnectionStore(database)
	agentConnectionManager, err := agentconn.New(
		agentconn.Config{
			Address:                 cfg.AgentListener.Address,
			TLSCertificateFile:      cfg.AgentListener.TLS.CertificateFile,
			TLSPrivateKeyFile:       cfg.AgentListener.TLS.PrivateKeyFile,
			ClientCACertificateFile: cfg.AgentIdentity.CACertificateFile,
			HandshakeTimeout:        cfg.AgentListener.HandshakeTimeout,
			HeartbeatInterval:       cfg.AgentListener.HeartbeatInterval,
			HeartbeatTimeout:        cfg.AgentListener.HeartbeatTimeout,
			LastSeenWriteInterval:   cfg.AgentListener.LastSeenWriteInterval,
			OperationTimeout:        cfg.AgentListener.OperationTimeout,
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
	go monitorAgentCertificates(
		runContext,
		logger,
		agentStatusStore,
		cfg.CertificateMonitor,
	)
	httpServer := &http.Server{
		Addr:              cfg.HTTP.Address,
		Handler:           handler,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	serverErrors := make(chan error, 1)
	go func() {
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
	go func() {
		agentErrors <- agentConnectionManager.Run(runContext)
	}()

	select {
	case err := <-serverErrors:
		cancelRun()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case err := <-agentErrors:
		cancelRun()
		if shutdownErr := shutdownHTTPServer(httpServer, cfg.ShutdownTimeout); shutdownErr != nil {
			return errors.Join(err, shutdownErr)
		}
		if err == nil {
			return errors.New("Agent QUIC listener stopped unexpectedly")
		}
		return err
	case <-ctx.Done():
		logger.Info("server shutdown requested")
		cancelRun()
		if err := shutdownHTTPServer(httpServer, cfg.ShutdownTimeout); err != nil {
			return err
		}
		logger.Info("server stopped")
		return nil
	}
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
