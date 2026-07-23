package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"zke/pkg/server/httpapi"
	"zke/pkg/server/store"
)

func Run(ctx context.Context, cfg Config, logger *slog.Logger) error {
	databaseContext, cancelDatabase := context.WithTimeout(ctx, cfg.Database.ConnectTimeout)
	database, err := store.Open(databaseContext, cfg.Database.URL)
	cancelDatabase()
	if err != nil {
		return err
	}
	defer database.Close()

	handler := httpapi.New(logger, database.Ping)
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
