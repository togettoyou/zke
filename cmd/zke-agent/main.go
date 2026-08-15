package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/togettoyou/zke/pkg/agent"
	"github.com/togettoyou/zke/pkg/shared/buildinfo"
	"github.com/togettoyou/zke/pkg/shared/logging"
)

func main() {
	if err := run(); err != nil {
		logger := slog.New(slog.NewTextHandler(os.Stderr, nil)).With(
			slog.String("component", "zke-agent"),
		)
		logger.Error("process exited", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := agent.LoadConfig(os.Args[1:])
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger, err := logging.New(cfg.LogLevel, "zke-agent")
	if err != nil {
		return fmt.Errorf("configure logging: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("agent configuration loaded",
		slog.String("version", buildinfo.Version()),
		slog.String("registration_server", cfg.Registration.ServerURL),
		slog.String("connection_server", cfg.Connection.ServerAddress),
	)

	return agent.Run(ctx, cfg, logger)
}
