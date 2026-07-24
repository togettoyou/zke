package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"zke/pkg/server"
	"zke/pkg/shared/logging"
)

func main() {
	if err := run(); err != nil {
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With(
			slog.String("component", "zke-server"),
		)
		logger.Error("process exited", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := server.LoadConfig(os.Args[1:])
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger, err := logging.New(cfg.LogLevel, "zke-server")
	if err != nil {
		return fmt.Errorf("configure logging: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return server.Run(ctx, cfg, logger)
}
