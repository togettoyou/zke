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

	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	"github.com/togettoyou/zke/pkg/server/httpapi"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
	runContext, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	certificateSigner, err := loadAgentCertificateSigner(cfg.AgentEnrollment)
	if err != nil {
		return err
	}
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

	authenticationService := auth.NewService(
		store.NewAuthStore(database),
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
	handler := httpapi.New(
		logger,
		httpapi.Dependencies{
			ReadinessCheck:    database.Ping,
			AuthService:       authenticationService,
			AuditService:      auditService,
			RBACService:       rbacService,
			EnrollmentService: enrollmentService,
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
	agentConnectionManager, err := agentconn.New(
		agentconn.Config{
			Address:                cfg.HTTP.Address,
			TLSCertificateFile:     cfg.AgentListener.TLSCertificateFile,
			TLSPrivateKeyFile:      cfg.AgentListener.TLSPrivateKeyFile,
			AgentCACertificateFile: cfg.AgentEnrollment.SigningCACertificateFile,
			HandshakeTimeout:       cfg.AgentListener.HandshakeTimeout,
			HeartbeatInterval:      cfg.AgentListener.HeartbeatInterval,
			HeartbeatTimeout:       cfg.AgentListener.HeartbeatTimeout,
			LastSeenWriteInterval:  cfg.AgentListener.LastSeenWriteInterval,
			OperationTimeout:       cfg.AgentListener.OperationTimeout,
		},
		logger,
		store.NewAgentConnectionStore(database),
	)
	if err != nil {
		return err
	}
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
		logger.Info("HTTP server starting", slog.String("address", cfg.HTTP.Address))
		if strings.TrimSpace(cfg.HTTP.TLSCertificateFile) != "" {
			serverErrors <- httpServer.ListenAndServeTLS(
				cfg.HTTP.TLSCertificateFile,
				cfg.HTTP.TLSPrivateKeyFile,
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

func shutdownHTTPServer(server *http.Server, timeout time.Duration) error {
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), timeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("gracefully shut down HTTP server: %w", err)
	}
	return nil
}

func loadAgentCertificateSigner(
	config AgentEnrollmentConfig,
) (*enrollment.CertificateSigner, error) {
	certificatePath := strings.TrimSpace(config.SigningCACertificateFile)
	privateKeyPath := strings.TrimSpace(config.SigningCAPrivateKeyFile)
	if certificatePath == "" && privateKeyPath == "" {
		return nil, nil
	}
	certificatePEM, err := os.ReadFile(certificatePath)
	if err != nil {
		return nil, fmt.Errorf("read Agent signing CA certificate: %w", err)
	}
	privateKeyPEM, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read Agent signing CA private key: %w", err)
	}
	signer, err := enrollment.NewCertificateSigner(
		certificatePEM,
		privateKeyPEM,
		config.CertificateTTL,
	)
	if err != nil {
		return nil, fmt.Errorf("configure Agent certificate signer: %w", err)
	}
	return signer, nil
}
