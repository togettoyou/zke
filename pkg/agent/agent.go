package agent

import (
	"context"
	"log/slog"
)

func Run(ctx context.Context, logger *slog.Logger) error {
	logger.Info("agent started", slog.String("state", "awaiting_protocol_implementation"))
	<-ctx.Done()
	logger.Info("agent stopped")
	return nil
}
