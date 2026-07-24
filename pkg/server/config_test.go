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
  tls_certificate_file: /run/secrets/server.crt
  tls_private_key_file: /run/secrets/server.key
  read_header_timeout: 5s
  read_timeout: 20s
  write_timeout: 15s
  idle_timeout: 60s
database:
  url: postgres://file-value
  connect_timeout: 4s
  migration_timeout: 90s
auth:
  session_idle_timeout: 45m
  session_absolute_timeout: 12h
  operation_timeout: 12s
  max_concurrent_password_checks: 3
  cookie_secure: false
  login_rate_limit:
    window: 2m
    max_attempts_per_account: 6
    max_attempts_per_source: 24
agent_enrollment:
  signing_ca_certificate_file: /run/secrets/agent-ca.crt
  signing_ca_private_key_file: /run/secrets/agent-ca.key
  certificate_ttl: 48h
  operation_timeout: 9s
  allow_insecure_loopback: false
  rate_limit:
    window: 3m
    max_attempts_per_source: 42
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
	if cfg.HTTP.TLSCertificateFile != "/run/secrets/server.crt" ||
		cfg.HTTP.TLSPrivateKeyFile != "/run/secrets/server.key" {
		t.Fatalf("unexpected HTTP TLS config: %+v", cfg.HTTP)
	}
	if cfg.Database.URL != "postgres://file-value" {
		t.Fatalf("database URL = %q, want YAML value", cfg.Database.URL)
	}
	if cfg.HTTP.ReadTimeout != 20*time.Second {
		t.Fatalf("read timeout = %s, want YAML value", cfg.HTTP.ReadTimeout)
	}
	if cfg.Database.MigrationTimeout != 90*time.Second {
		t.Fatalf("migration timeout = %s, want YAML value", cfg.Database.MigrationTimeout)
	}
	if cfg.Auth.SessionIdleTimeout != 45*time.Minute {
		t.Fatalf("session idle timeout = %s, want YAML value", cfg.Auth.SessionIdleTimeout)
	}
	if cfg.Auth.SessionAbsoluteTimeout != 12*time.Hour {
		t.Fatalf("session absolute timeout = %s, want YAML value", cfg.Auth.SessionAbsoluteTimeout)
	}
	if cfg.Auth.OperationTimeout != 12*time.Second {
		t.Fatalf("authentication operation timeout = %s, want YAML value", cfg.Auth.OperationTimeout)
	}
	if cfg.Auth.MaxConcurrentPasswordChecks != 3 {
		t.Fatalf(
			"maximum concurrent password checks = %d, want YAML value",
			cfg.Auth.MaxConcurrentPasswordChecks,
		)
	}
	if cfg.Auth.CookieSecure {
		t.Fatal("cookie secure = true, want YAML value false")
	}
	if cfg.Auth.LoginRateLimit.MaxAttemptsPerAccount != 6 {
		t.Fatalf(
			"account attempt limit = %d, want YAML value",
			cfg.Auth.LoginRateLimit.MaxAttemptsPerAccount,
		)
	}
	if cfg.AgentEnrollment.SigningCACertificateFile != "/run/secrets/agent-ca.crt" ||
		cfg.AgentEnrollment.SigningCAPrivateKeyFile != "/run/secrets/agent-ca.key" ||
		cfg.AgentEnrollment.CertificateTTL != 48*time.Hour ||
		cfg.AgentEnrollment.OperationTimeout != 9*time.Second ||
		cfg.AgentEnrollment.RateLimit.Window != 3*time.Minute ||
		cfg.AgentEnrollment.RateLimit.MaxAttemptsPerSource != 42 {
		t.Fatalf("unexpected Agent enrollment config: %+v", cfg.AgentEnrollment)
	}
	partialCAConfig := cfg
	partialCAConfig.AgentEnrollment.SigningCAPrivateKeyFile = ""
	if err := partialCAConfig.Validate(); err == nil {
		t.Fatal("Validate() accepted a signing CA certificate without its private key")
	}
	partialTLSConfig := cfg
	partialTLSConfig.HTTP.TLSPrivateKeyFile = ""
	if err := partialTLSConfig.Validate(); err == nil {
		t.Fatal("Validate() accepted an HTTP TLS certificate without its private key")
	}
	insecureExternalConfig := cfg
	insecureExternalConfig.HTTP.Address = "0.0.0.0:9000"
	insecureExternalConfig.AgentEnrollment.AllowInsecureLoopback = true
	if err := insecureExternalConfig.Validate(); err == nil {
		t.Fatal("Validate() allowed insecure Agent enrollment on a non-loopback address")
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
			URL:              "postgres://example",
			ConnectTimeout:   5 * time.Second,
			MigrationTimeout: time.Minute,
		},
		Auth: AuthConfig{
			SessionIdleTimeout:          30 * time.Minute,
			SessionAbsoluteTimeout:      8 * time.Hour,
			OperationTimeout:            10 * time.Second,
			MaxConcurrentPasswordChecks: 4,
			CookieSecure:                true,
			LoginRateLimit: LoginRateLimitConfig{
				Window:                time.Minute,
				MaxAttemptsPerAccount: 5,
				MaxAttemptsPerSource:  20,
			},
		},
		AgentEnrollment: AgentEnrollmentConfig{
			CertificateTTL:   30 * 24 * time.Hour,
			OperationTimeout: 10 * time.Second,
			RateLimit: AgentEnrollmentRateLimitConfig{
				Window:               time.Minute,
				MaxAttemptsPerSource: 30,
			},
		},
		ShutdownTimeout: 10 * time.Second,
		LogLevel:        "info",
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted an HTTP timeout above its maximum")
	}
}

func TestConfigRejectsSessionIdleAboveAbsoluteTimeout(t *testing.T) {
	t.Parallel()

	cfg := Config{
		HTTP: HTTPConfig{
			Address:           "127.0.0.1:8080",
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		Database: DatabaseConfig{
			URL:              "postgres://example",
			ConnectTimeout:   5 * time.Second,
			MigrationTimeout: time.Minute,
		},
		Auth: AuthConfig{
			SessionIdleTimeout:          9 * time.Hour,
			SessionAbsoluteTimeout:      8 * time.Hour,
			OperationTimeout:            10 * time.Second,
			MaxConcurrentPasswordChecks: 4,
			CookieSecure:                true,
			LoginRateLimit: LoginRateLimitConfig{
				Window:                time.Minute,
				MaxAttemptsPerAccount: 5,
				MaxAttemptsPerSource:  20,
			},
		},
		AgentEnrollment: AgentEnrollmentConfig{
			CertificateTTL:   30 * 24 * time.Hour,
			OperationTimeout: 10 * time.Second,
			RateLimit: AgentEnrollmentRateLimitConfig{
				Window:               time.Minute,
				MaxAttemptsPerSource: 30,
			},
		},
		ShutdownTimeout: 10 * time.Second,
		LogLevel:        "info",
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted an idle timeout above the absolute timeout")
	}
}

func TestConfigRejectsOperationTimeoutAtOrAboveWriteTimeout(t *testing.T) {
	t.Parallel()

	cfg := Config{
		HTTP: HTTPConfig{
			Address:           "127.0.0.1:8080",
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		Database: DatabaseConfig{
			URL:              "postgres://example",
			ConnectTimeout:   5 * time.Second,
			MigrationTimeout: time.Minute,
		},
		Auth: AuthConfig{
			SessionIdleTimeout:          30 * time.Minute,
			SessionAbsoluteTimeout:      8 * time.Hour,
			OperationTimeout:            10 * time.Second,
			MaxConcurrentPasswordChecks: 4,
			CookieSecure:                true,
			LoginRateLimit: LoginRateLimitConfig{
				Window:                time.Minute,
				MaxAttemptsPerAccount: 5,
				MaxAttemptsPerSource:  20,
			},
		},
		AgentEnrollment: AgentEnrollmentConfig{
			CertificateTTL:   30 * 24 * time.Hour,
			OperationTimeout: 9 * time.Second,
			RateLimit: AgentEnrollmentRateLimitConfig{
				Window:               time.Minute,
				MaxAttemptsPerSource: 30,
			},
		},
		ShutdownTimeout: 10 * time.Second,
		LogLevel:        "info",
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted an operation timeout at the HTTP write timeout")
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
