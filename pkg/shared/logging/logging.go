package logging

import (
	"errors"
	"log/slog"
	"os"
	"strings"
)

func New(level, component string) (*slog.Logger, error) {
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

	component = strings.TrimSpace(component)
	if component == "" {
		return nil, errors.New("logger component is required")
	}

	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parsed})
	return slog.New(handler).With(slog.String("component", component)), nil
}
