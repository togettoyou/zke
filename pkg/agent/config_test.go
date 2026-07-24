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
kubeconfig_file: /home/test/.kube/config
identity:
  namespace: zke-system
  secret_name: zke-agent-identity
  renew_before: 120h
registration:
  server_url: https://api.example.invalid:8443
  ca_certificate_file: /var/run/secrets/zke-server/ca.crt
  timeout: 12s
  retry_initial_interval: 2s
  retry_max_interval: 20s
connection:
  server_address: agent.example.invalid:9443
  ca_certificate_file: /var/run/secrets/zke-agent-listener/ca.crt
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
	if cfg.Registration.ServerURL != "https://api.example.invalid:8443" ||
		cfg.Registration.CACertificateFile != "/var/run/secrets/zke-server/ca.crt" ||
		cfg.Registration.Timeout != 12*time.Second ||
		cfg.Registration.RetryInitialInterval != 2*time.Second ||
		cfg.Registration.RetryMaxInterval != 20*time.Second {
		t.Fatalf(
			"unexpected Agent registration config: %+v",
			cfg.Registration,
		)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("log level = %q, want YAML value", cfg.LogLevel)
	}
	if cfg.KubeconfigFile != "/home/test/.kube/config" ||
		cfg.IdentityNamespace != "zke-system" ||
		cfg.IdentitySecretName != "zke-agent-identity" ||
		cfg.CertificateRenewBefore != 120*time.Hour {
		t.Fatalf("unexpected Agent config: %+v", cfg)
	}
	if cfg.Connection.ServerAddress != "agent.example.invalid:9443" ||
		cfg.Connection.CACertificateFile != "/var/run/secrets/zke-agent-listener/ca.crt" ||
		cfg.Connection.ConnectTimeout != 9*time.Second ||
		cfg.Connection.RetryInitialInterval != 3*time.Second ||
		cfg.Connection.RetryMaxInterval != 25*time.Second {
		t.Fatalf("unexpected Agent connection config: %+v", cfg.Connection)
	}
}

func TestConfigRejectsCredentialsInServerAddress(t *testing.T) {
	t.Parallel()

	cfg := validAgentConfig()
	cfg.Registration.ServerURL = "https://user:password@example.invalid"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted credentials in the registration Server URL")
	}
}

func TestConfigAllowsHTTPServerAddress(t *testing.T) {
	t.Parallel()

	cfg := validAgentConfig()
	cfg.Registration.ServerURL = "http://127.0.0.1:8080"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected HTTP registration Server URL: %v", err)
	}
}

func TestConfigRejectsHTTPServerCA(t *testing.T) {
	t.Parallel()

	cfg := validAgentConfig()
	cfg.Registration.ServerURL = "http://192.0.2.1:8080"
	cfg.Registration.CACertificateFile = "/http-server-ca.crt"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a registration Server CA for HTTP")
	}
}

func TestConfigRejectsRemoteHTTPRegistrationServer(t *testing.T) {
	t.Parallel()

	cfg := validAgentConfig()
	cfg.Registration.ServerURL = "http://192.0.2.1:8080"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a remote plaintext registration Server URL")
	}
}

func TestConfigRequiresDedicatedConnectionAddress(t *testing.T) {
	t.Parallel()

	cfg := validAgentConfig()
	cfg.Connection.ServerAddress = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a missing connection Server address")
	}
	cfg.Connection.ServerAddress = "https://agent.example.invalid:8443"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a URL as the QUIC connection address")
	}
}

func TestLoadConfigUsesDeploymentDefaults(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "agent.yaml")
	content := []byte(`
registration:
  server_url: https://api.example.invalid:8443
connection:
  server_address: agent.example.invalid:9443
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IdentityNamespace != defaultIdentityNamespace ||
		cfg.IdentitySecretName != defaultIdentitySecretName ||
		cfg.CertificateRenewBefore != 7*24*time.Hour ||
		cfg.Registration.Timeout != 10*time.Second ||
		cfg.Registration.RetryInitialInterval != time.Second ||
		cfg.Registration.RetryMaxInterval != 15*time.Second ||
		cfg.Connection.ServerAddress != "agent.example.invalid:9443" ||
		cfg.Connection.CACertificateFile != "" ||
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
registration:
  server_url: https://api.example.invalid:8443
connection:
  server_address: agent.example.invalid:9443
unknown_field: true
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfig([]string{"--config", path}); err == nil {
		t.Fatal("LoadConfig() accepted an unknown YAML field")
	}
}

func TestRepositoryAgentConfigLoads(t *testing.T) {
	t.Parallel()

	if _, err := LoadConfig([]string{
		"--config",
		filepath.Join("..", "..", "configs", "zke-agent.yaml"),
	}); err != nil {
		t.Fatal(err)
	}
}

func validAgentConfig() Config {
	return Config{
		IdentityNamespace:      "zke-system",
		IdentitySecretName:     "zke-agent-identity",
		CertificateRenewBefore: 7 * 24 * time.Hour,
		Registration: RegistrationConfig{
			ServerURL:            "https://api.example.invalid:8443",
			Timeout:              10 * time.Second,
			RetryInitialInterval: time.Second,
			RetryMaxInterval:     15 * time.Second,
		},
		Connection: ConnectionConfig{
			ServerAddress:        "agent.example.invalid:9443",
			CACertificateFile:    "/server-ca.crt",
			ConnectTimeout:       10 * time.Second,
			RetryInitialInterval: time.Second,
			RetryMaxInterval:     30 * time.Second,
		},
		LogLevel: "info",
	}
}
