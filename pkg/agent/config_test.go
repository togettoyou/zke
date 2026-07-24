package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigYAML(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "agent.yaml")
	content := []byte(`
server_address: https://server.example.invalid:8443
server_ca_file: /var/run/secrets/zke-server/ca.crt
kubeconfig_file: /home/test/.kube/config
cluster_name: test-cluster
enrollment_token_file: /var/run/secrets/zke-enrollment/token
identity:
  namespace: zke-system
  secret_name: zke-agent-identity
registration:
  timeout: 12s
  retry_initial_interval: 2s
  retry_max_interval: 20s
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
	if cfg.ClusterName != "test-cluster" ||
		cfg.KubeconfigFile != "/home/test/.kube/config" ||
		cfg.IdentityNamespace != "zke-system" ||
		cfg.IdentitySecretName != "zke-agent-identity" ||
		cfg.RegistrationTimeout != 12*time.Second ||
		cfg.RetryInitialInterval != 2*time.Second ||
		cfg.RetryMaxInterval != 20*time.Second {
		t.Fatalf("unexpected Agent registration config: %+v", cfg)
	}
}

func TestConfigRejectsCredentialsInServerAddress(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ServerAddress:        "https://user:password@example.invalid",
		ClusterName:          "test-cluster",
		EnrollmentTokenFile:  "/token",
		IdentityNamespace:    "zke-system",
		IdentitySecretName:   "zke-agent-identity",
		RegistrationTimeout:  10 * time.Second,
		RetryInitialInterval: time.Second,
		RetryMaxInterval:     15 * time.Second,
		LogLevel:             "info",
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
	if err := Run(ctx, Config{}, logger); err != nil {
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
