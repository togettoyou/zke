package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	"github.com/togettoyou/zke/pkg/server/httpapi"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
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
		enrollment.DefaultTokenTTL,
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
		},
	)
	httpServer := &http.Server{
		Addr:              cfg.HTTP.Address,
		Handler:           handler,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("HTTP server starting", slog.String("address", cfg.HTTP.Address))
		serverErrors <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		logger.Info("server shutdown requested")
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancelShutdown()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("gracefully shut down HTTP server: %w", err)
		}
		logger.Info("server stopped")
		return nil
	}
}
