package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
)

func NewLogger(level string) (*slog.Logger, error) {
	var parsed slog.Level
	switch strings.ToLower(level) {
	case "debug":
		parsed = slog.LevelDebug
	case "info":
		parsed = slog.LevelInfo
	case "warn":
		parsed = slog.LevelWarn
	case "error":
		parsed = slog.LevelError
	default:
		return nil, errors.New("log level must be one of debug, info, warn, or error")
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parsed})
	return slog.New(handler).With("component", "zke-agent"), nil
}

func Run(ctx context.Context, logger *slog.Logger) error {
	logger.Info("agent started", slog.String("state", "awaiting_protocol_implementation"))
	<-ctx.Done()
	logger.Info("agent stopped")
	return nil
}
