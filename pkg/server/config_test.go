package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigYAML(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "server.yaml")
	content := []byte(`
http:
  address: 127.0.0.1:9000
  read_header_timeout: 5s
  read_timeout: 20s
  write_timeout: 15s
  idle_timeout: 60s
database:
  url: postgres://file-value
  connect_timeout: 4s
shutdown_timeout: 8s
log_level: warn
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.HTTP.Address != "127.0.0.1:9000" {
		t.Fatalf("HTTP address = %q, want YAML value", cfg.HTTP.Address)
	}
	if cfg.Database.URL != "postgres://file-value" {
		t.Fatalf("database URL = %q, want YAML value", cfg.Database.URL)
	}
	if cfg.HTTP.ReadTimeout != 20*time.Second {
		t.Fatalf("read timeout = %s, want YAML value", cfg.HTTP.ReadTimeout)
	}
	if cfg.ShutdownTimeout != 8*time.Second {
		t.Fatalf("shutdown timeout = %s, want YAML value", cfg.ShutdownTimeout)
	}
}

func TestLoadConfigRequiresPath(t *testing.T) {
	t.Parallel()

	_, err := LoadConfig(nil)
	if err == nil {
		t.Fatal("LoadConfig() succeeded without --config")
	}
}

func TestConfigRejectsUnboundedTimeout(t *testing.T) {
	t.Parallel()

	cfg := Config{
		HTTP: HTTPConfig{
			Address:           "127.0.0.1:8080",
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       maxHTTPTimeout + time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		Database: DatabaseConfig{
			URL:            "postgres://example",
			ConnectTimeout: 5 * time.Second,
		},
		ShutdownTimeout: 10 * time.Second,
		LogLevel:        "info",
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted an HTTP timeout above its maximum")
	}
}

func TestLoadConfigRejectsUnknownYAMLField(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "server.yaml")
	content := []byte(`
database:
  url: postgres://example
unknown_field: true
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfig([]string{"--config", path}); err == nil {
		t.Fatal("LoadConfig() accepted an unknown YAML field")
	}
}

func TestLoadConfigRejectsMultipleYAMLDocuments(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "server.yaml")
	content := []byte(`
database:
  url: postgres://example
---
log_level: debug
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfig([]string{"--config", path}); err == nil {
		t.Fatal("LoadConfig() accepted multiple YAML documents")
	}
}
