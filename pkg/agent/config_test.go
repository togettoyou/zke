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
enrollment_token_file: /var/run/secrets/zke-enrollment/token
identity:
  namespace: zke-system
  secret_name: zke-agent-identity
registration:
  timeout: 12s
  retry_initial_interval: 2s
  retry_max_interval: 20s
connection:
  server_ca_file: /var/run/secrets/zke-agent-listener/ca.crt
  connect_timeout: 9s
  retry_initial_interval: 3s
  retry_max_interval: 25s
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
	if cfg.ServerCAFile != "/var/run/secrets/zke-server/ca.crt" ||
		cfg.KubeconfigFile != "/home/test/.kube/config" ||
		cfg.EnrollmentTokenFile != "/var/run/secrets/zke-enrollment/token" ||
		cfg.IdentityNamespace != "zke-system" ||
		cfg.IdentitySecretName != "zke-agent-identity" ||
		cfg.RegistrationTimeout != 12*time.Second ||
		cfg.RetryInitialInterval != 2*time.Second ||
		cfg.RetryMaxInterval != 20*time.Second {
		t.Fatalf("unexpected Agent registration config: %+v", cfg)
	}
	if cfg.Connection.ServerCAFile != "/var/run/secrets/zke-agent-listener/ca.crt" ||
		cfg.Connection.ConnectTimeout != 9*time.Second ||
		cfg.Connection.RetryInitialInterval != 3*time.Second ||
		cfg.Connection.RetryMaxInterval != 25*time.Second {
		t.Fatalf("unexpected Agent connection config: %+v", cfg.Connection)
	}
}

func TestConfigRejectsCredentialsInServerAddress(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ServerAddress:        "https://user:password@example.invalid",
		EnrollmentTokenFile:  "/token",
		IdentityNamespace:    "zke-system",
		IdentitySecretName:   "zke-agent-identity",
		RegistrationTimeout:  10 * time.Second,
		RetryInitialInterval: time.Second,
		RetryMaxInterval:     15 * time.Second,
		Connection: ConnectionConfig{
			ServerCAFile:         "/server-ca.crt",
			ConnectTimeout:       10 * time.Second,
			RetryInitialInterval: time.Second,
			RetryMaxInterval:     30 * time.Second,
		},
		LogLevel: "info",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted credentials in the Server address")
	}
}

func TestConfigAllowsHTTPServerAddress(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ServerAddress:        "http://127.0.0.1:8080",
		EnrollmentTokenFile:  "/token",
		IdentityNamespace:    "zke-system",
		IdentitySecretName:   "zke-agent-identity",
		RegistrationTimeout:  10 * time.Second,
		RetryInitialInterval: time.Second,
		RetryMaxInterval:     15 * time.Second,
		Connection: ConnectionConfig{
			ServerCAFile:         "/server-ca.crt",
			ConnectTimeout:       10 * time.Second,
			RetryInitialInterval: time.Second,
			RetryMaxInterval:     30 * time.Second,
		},
		LogLevel: "info",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected HTTP Server address: %v", err)
	}
}

func TestConfigRejectsHTTPServerCA(t *testing.T) {
	t.Parallel()

	cfg := Config{
		ServerAddress:        "http://192.0.2.1:8080",
		ServerCAFile:         "/http-server-ca.crt",
		EnrollmentTokenFile:  "/token",
		IdentityNamespace:    "zke-system",
		IdentitySecretName:   "zke-agent-identity",
		RegistrationTimeout:  10 * time.Second,
		RetryInitialInterval: time.Second,
		RetryMaxInterval:     15 * time.Second,
		Connection: ConnectionConfig{
			ServerCAFile:         "/server-ca.crt",
			ConnectTimeout:       10 * time.Second,
			RetryInitialInterval: time.Second,
			RetryMaxInterval:     30 * time.Second,
		},
		LogLevel: "info",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted an HTTP Server CA for a plaintext address")
	}
}

func TestLoadConfigUsesDeploymentDefaults(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "agent.yaml")
	content := []byte(`
server_address: https://server.example.invalid:8443
connection:
  server_ca_file: /var/run/secrets/zke-agent-listener/ca.crt
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnrollmentTokenFile != defaultEnrollmentTokenFile ||
		cfg.IdentityNamespace != defaultIdentityNamespace ||
		cfg.IdentitySecretName != defaultIdentitySecretName ||
		cfg.RegistrationTimeout != 10*time.Second ||
		cfg.RetryInitialInterval != time.Second ||
		cfg.RetryMaxInterval != 15*time.Second ||
		cfg.Connection.ServerCAFile != "/var/run/secrets/zke-agent-listener/ca.crt" ||
		cfg.Connection.ConnectTimeout != 10*time.Second ||
		cfg.Connection.RetryInitialInterval != time.Second ||
		cfg.Connection.RetryMaxInterval != 30*time.Second ||
		cfg.LogLevel != defaultLogLevel {
		t.Fatalf("unexpected Agent deployment defaults: %+v", cfg)
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
