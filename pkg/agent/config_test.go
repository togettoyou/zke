package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigYAML(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "agent.yaml")
	content := []byte(`
server_address: https://server.example.invalid:8443
log_level: debug
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerAddress != "https://server.example.invalid:8443" {
		t.Fatalf("server address = %q, want YAML value", cfg.ServerAddress)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("log level = %q, want YAML value", cfg.LogLevel)
	}
}

func TestConfigRejectsCredentialsInServerAddress(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ServerAddress: "https://user:password@example.invalid",
		LogLevel:      "info",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted credentials in the Server address")
	}
}

func TestRunStopsWhenContextIsCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if err := Run(ctx, logger); err != nil {
		t.Fatalf("Run() returned an error: %v", err)
	}
}

func TestLoadConfigRejectsUnknownYAMLField(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "agent.yaml")
	content := []byte(`
server_address: https://example.invalid:8443
unknown_field: true
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfig([]string{"--config", path}); err == nil {
		t.Fatal("LoadConfig() accepted an unknown YAML field")
	}
}
