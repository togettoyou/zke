package logging

import (
	"context"
	"log/slog"
	"testing"
)

func TestNewConfiguresLevel(t *testing.T) {
	t.Parallel()

	logger, err := New("info", "test-component")
	if err != nil {
		t.Fatal(err)
	}
	if logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug logging is enabled at info level")
	}
	if !logger.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("info logging is disabled at info level")
	}
}

func TestNewRejectsInvalidLevel(t *testing.T) {
	t.Parallel()

	if _, err := New("verbose", "test-component"); err == nil {
		t.Fatal("New() accepted an invalid log level")
	}
}

func TestNewRejectsEmptyComponent(t *testing.T) {
	t.Parallel()

	if _, err := New("info", " "); err == nil {
		t.Fatal("New() accepted an empty component")
	}
}
