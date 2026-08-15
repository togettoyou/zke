package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/togettoyou/zke/pkg/server"
	"github.com/togettoyou/zke/pkg/shared/buildinfo"
	"github.com/togettoyou/zke/pkg/shared/logging"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		logger := slog.New(slog.NewTextHandler(os.Stderr, nil)).With(
			slog.String("component", "zke-server"),
		)
		logger.Error("process exited", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := server.LoadConfig(args)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger, err := logging.New(cfg.LogLevel, "zke-server")
	if err != nil {
		return fmt.Errorf("configure logging: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("server configuration loaded",
		slog.String("version", buildinfo.Version()),
	)

	return server.Run(ctx, cfg, logger)
}
