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
  tls:
    certificate_file: /run/secrets/http.crt
    private_key_file: /run/secrets/http.key
  read_header_timeout: 5s
  read_timeout: 20s
  write_timeout: 15s
  idle_timeout: 60s
pod_access:
  enabled: true
  address: 127.0.0.1:10443
  external_url: https://access.example.com:10443
  tls:
    certificate_file: /run/secrets/pod-access.crt
    private_key_file: /run/secrets/pod-access.key
  read_header_timeout: 4s
  idle_timeout: 50s
  activation_ttl: 25s
  session_ttl: 30m
  revalidate_interval: 10s
  max_pending_sessions: 500
  max_active_sessions: 100
  max_connections: 128
  max_connections_per_session: 3
  max_client_bytes: 4194304
  max_pod_bytes: 8388608
database:
  url: postgres://file-value
  connect_timeout: 4s
  migration_timeout: 90s
auth:
  session_idle_timeout: 45m
  session_absolute_timeout: 12h
  operation_timeout: 12s
  max_concurrent_password_checks: 3
  cookie_secure: true
  login_rate_limit:
    window: 2m
    max_attempts_per_account: 6
    max_attempts_per_source: 24
  account_lockout:
    max_failed_attempts: 7
    duration: 20m
  initial_admin:
    enabled: true
    username: admin
    display_name: ZKE Administrator
    password_file: /run/secrets/zke-admin/password
    auto_generate_password: false
agent_pki:
  mode: external
  agent_client_certificate_validity: 48h
  external:
    agent_client_ca:
      certificate_file: /run/secrets/agent-client-ca.crt
      private_key_file: /run/secrets/agent-client-ca.key
    agent_listener_ca:
      certificate_file: /run/secrets/agent-listener-ca.crt
    agent_listener:
      certificate_file: /run/secrets/agent-listener.crt
      private_key_file: /run/secrets/agent-listener.key
agent_enrollment:
  operation_timeout: 9s
  rate_limit:
    window: 3m
    max_attempts_per_source: 42
agent_listener:
  address: 127.0.0.1:9443
  handshake_timeout: 8s
  heartbeat_interval: 12s
  heartbeat_timeout: 36s
  last_seen_write_interval: 2m
  operation_timeout: 7s
  resource_request_timeout: 90s
  connection_drain_timeout: 12s
  max_resource_body_bytes: 16777216
  max_buffered_resource_response_bytes: 134217728
  max_resource_streams_per_agent: 32
  max_concurrent_resource_requests: 2048
  pod_logs_request_timeout: 20m
  max_pod_log_bytes: 8388608
  max_pod_logs_streams_per_agent: 12
  max_concurrent_pod_logs_requests: 512
  pod_exec_request_timeout: 12m
  max_pod_exec_input_bytes: 4194304
  max_pod_exec_output_bytes: 12582912
  max_pod_exec_streams_per_agent: 6
  max_concurrent_pod_exec_requests: 256
  pod_exec_session_ttl: 25s
  max_pending_pod_exec_sessions: 768
  pod_port_forward_request_timeout: 31m
  pod_port_forward_maximum_duration: 10m
  max_pod_port_forward_client_bytes: 5242880
  max_pod_port_forward_pod_bytes: 9437184
  pod_port_forward_session_client_bytes: 3145728
  pod_port_forward_session_pod_bytes: 7340032
  max_pod_port_forward_streams_per_agent: 5
  max_concurrent_pod_port_forward_requests: 192
  pod_port_forward_session_ttl: 20s
  max_pending_pod_port_forward_sessions: 640
  resource_watch_request_timeout: 25m
  max_resource_watch_streams_per_agent: 10
  max_concurrent_resource_watch_requests: 600
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
	if cfg.HTTP.TLS.CertificateFile != "/run/secrets/http.crt" ||
		cfg.HTTP.TLS.PrivateKeyFile != "/run/secrets/http.key" {
		t.Fatalf("unexpected HTTP TLS config: %+v", cfg.HTTP.TLS)
	}
	if cfg.Database.URL != "postgres://file-value" {
		t.Fatalf("database URL = %q, want YAML value", cfg.Database.URL)
	}
	if cfg.HTTP.ReadTimeout != 20*time.Second {
		t.Fatalf("read timeout = %s, want YAML value", cfg.HTTP.ReadTimeout)
	}
	if !cfg.PodAccess.Enabled || cfg.PodAccess.Address != "127.0.0.1:10443" ||
		cfg.PodAccess.ExternalURL != "https://access.example.com:10443" ||
		cfg.PodAccess.TLS.CertificateFile != "/run/secrets/pod-access.crt" ||
		cfg.PodAccess.TLS.PrivateKeyFile != "/run/secrets/pod-access.key" ||
		cfg.PodAccess.ReadHeaderTimeout != 4*time.Second || cfg.PodAccess.IdleTimeout != 50*time.Second ||
		cfg.PodAccess.ActivationTTL != 25*time.Second || cfg.PodAccess.SessionTTL != 30*time.Minute ||
		cfg.PodAccess.RevalidateInterval != 10*time.Second || cfg.PodAccess.MaxPendingSessions != 500 ||
		cfg.PodAccess.MaxActiveSessions != 100 || cfg.PodAccess.MaxConnections != 128 ||
		cfg.PodAccess.MaxConnectionsPerSession != 3 || cfg.PodAccess.MaxClientBytes != 4*1024*1024 ||
		cfg.PodAccess.MaxPodBytes != 8*1024*1024 {
		t.Fatalf("unexpected Pod Access config: %+v", cfg.PodAccess)
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
	if !cfg.Auth.CookieSecure {
		t.Fatal("cookie secure = false, want YAML value true")
	}
	if cfg.Auth.LoginRateLimit.MaxAttemptsPerAccount != 6 {
		t.Fatalf(
			"account attempt limit = %d, want YAML value",
			cfg.Auth.LoginRateLimit.MaxAttemptsPerAccount,
		)
	}
	if cfg.Auth.AccountLockout.MaxFailedAttempts != 7 ||
		cfg.Auth.AccountLockout.Duration != 20*time.Minute {
		t.Fatalf("unexpected account lockout config: %+v", cfg.Auth.AccountLockout)
	}
	if !cfg.Auth.InitialAdmin.Enabled ||
		cfg.Auth.InitialAdmin.Username != "admin" ||
		cfg.Auth.InitialAdmin.DisplayName != "ZKE Administrator" ||
		cfg.Auth.InitialAdmin.PasswordFile !=
			"/run/secrets/zke-admin/password" ||
		cfg.Auth.InitialAdmin.AutoGeneratePassword {
		t.Fatalf(
			"unexpected initial administrator config: %+v",
			cfg.Auth.InitialAdmin,
		)
	}
	if cfg.AgentIdentity.CACertificateFile != "/run/secrets/agent-client-ca.crt" ||
		cfg.AgentIdentity.CAPrivateKeyFile != "/run/secrets/agent-client-ca.key" ||
		cfg.AgentIdentity.CertificateTTL != 48*time.Hour {
		t.Fatalf("unexpected Agent identity config: %+v", cfg.AgentIdentity)
	}
	if cfg.AgentEnrollment.OperationTimeout != 9*time.Second ||
		cfg.AgentEnrollment.RateLimit.Window != 3*time.Minute ||
		cfg.AgentEnrollment.RateLimit.MaxAttemptsPerSource != 42 {
		t.Fatalf("unexpected Agent enrollment config: %+v", cfg.AgentEnrollment)
	}
	if cfg.AgentListener.Address != "127.0.0.1:9443" ||
		cfg.AgentListener.TLS.CertificateFile != "/run/secrets/agent-listener.crt" ||
		cfg.AgentListener.TLS.PrivateKeyFile != "/run/secrets/agent-listener.key" ||
		cfg.AgentListener.HandshakeTimeout != 8*time.Second ||
		cfg.AgentListener.HeartbeatInterval != 12*time.Second ||
		cfg.AgentListener.HeartbeatTimeout != 36*time.Second ||
		cfg.AgentListener.LastSeenWriteInterval != 2*time.Minute ||
		cfg.AgentListener.OperationTimeout != 7*time.Second ||
		cfg.AgentListener.ResourceRequestTimeout != 90*time.Second ||
		cfg.AgentListener.ConnectionDrainTimeout != 12*time.Second ||
		cfg.AgentListener.MaxResourceBodyBytes != 16*1024*1024 ||
		cfg.AgentListener.MaxBufferedResourceBytes != 128*1024*1024 ||
		cfg.AgentListener.MaxResourceStreams != 32 ||
		cfg.AgentListener.MaxResourceRequests != 2048 ||
		cfg.AgentListener.PodLogsRequestTimeout != 20*time.Minute ||
		cfg.AgentListener.MaxPodLogBytes != 8*1024*1024 ||
		cfg.AgentListener.MaxPodLogsStreams != 12 ||
		cfg.AgentListener.MaxPodLogsRequests != 512 {
		t.Fatalf("unexpected Agent Listener config: %+v", cfg.AgentListener)
	}
	if cfg.AgentListener.ResourceWatchRequestTimeout != 25*time.Minute ||
		cfg.AgentListener.MaxResourceWatchStreams != 10 ||
		cfg.AgentListener.MaxResourceWatchRequests != 600 {
		t.Fatalf("unexpected Agent Resource Watch config: %+v", cfg.AgentListener)
	}
	if cfg.AgentListener.PodExecRequestTimeout != 12*time.Minute ||
		cfg.AgentListener.MaxPodExecInputBytes != 4*1024*1024 ||
		cfg.AgentListener.MaxPodExecOutputBytes != 12*1024*1024 ||
		cfg.AgentListener.MaxPodExecStreams != 6 ||
		cfg.AgentListener.MaxPodExecRequests != 256 ||
		cfg.AgentListener.PodExecSessionTTL != 25*time.Second ||
		cfg.AgentListener.MaxPendingPodExecSessions != 768 {
		t.Fatalf("unexpected Agent Pod Exec config: %+v", cfg.AgentListener)
	}
	if cfg.AgentListener.PodPortForwardRequestTimeout != 31*time.Minute ||
		cfg.AgentListener.PodPortForwardMaximumDuration != 10*time.Minute ||
		cfg.AgentListener.MaxPodPortForwardClientBytes != 5*1024*1024 ||
		cfg.AgentListener.MaxPodPortForwardPodBytes != 9*1024*1024 ||
		cfg.AgentListener.PodPortForwardSessionClientBytes != 3*1024*1024 ||
		cfg.AgentListener.PodPortForwardSessionPodBytes != 7*1024*1024 ||
		cfg.AgentListener.MaxPodPortForwardStreams != 5 ||
		cfg.AgentListener.MaxPodPortForwardRequests != 192 ||
		cfg.AgentListener.PodPortForwardSessionTTL != 20*time.Second ||
		cfg.AgentListener.MaxPendingPodPortForwardSessions != 640 {
		t.Fatalf("unexpected Agent Pod Port Forward config: %+v", cfg.AgentListener)
	}
	invalidHeartbeatConfig := cfg
	invalidHeartbeatConfig.AgentListener.HeartbeatTimeout =
		invalidHeartbeatConfig.AgentListener.HeartbeatInterval
	if err := invalidHeartbeatConfig.Validate(); err == nil {
		t.Fatal("Validate() accepted an Agent heartbeat interval at the timeout")
	}
	undersizedBufferConfig := cfg
	undersizedBufferConfig.AgentListener.MaxBufferedResourceBytes =
		undersizedBufferConfig.AgentListener.MaxResourceBodyBytes - 1
	if err := undersizedBufferConfig.Validate(); err == nil {
		t.Fatal("Validate() accepted a response buffer below one Resource body")
	}
	oversizedBufferConfig := cfg
	oversizedBufferConfig.AgentListener.MaxBufferedResourceBytes =
		8*1024*1024*1024 + 1
	if err := oversizedBufferConfig.Validate(); err == nil {
		t.Fatal("Validate() accepted a response buffer above 8 GiB")
	}
	invalidPodExecConfig := cfg
	invalidPodExecConfig.AgentListener.MaxPodExecRequests =
		invalidPodExecConfig.AgentListener.MaxPodExecStreams - 1
	if err := invalidPodExecConfig.Validate(); err == nil {
		t.Fatal("Validate() accepted global Pod Exec concurrency below the per-Agent limit")
	}
	partialCAConfig := cfg
	partialCAConfig.AgentIdentity.CAPrivateKeyFile = ""
	if err := partialCAConfig.Validate(); err == nil {
		t.Fatal("Validate() accepted an Agent identity CA certificate without its private key")
	}
	partialHTTPTLSConfig := cfg
	partialHTTPTLSConfig.HTTP.TLS.PrivateKeyFile = ""
	if err := partialHTTPTLSConfig.Validate(); err == nil {
		t.Fatal("Validate() accepted an HTTP TLS certificate without its private key")
	}
	insecureHTTPTLSConfig := cfg
	insecureHTTPTLSConfig.Auth.CookieSecure = false
	if err := insecureHTTPTLSConfig.Validate(); err == nil {
		t.Fatal("Validate() accepted HTTP TLS with insecure session cookies")
	}
	missingInitialAdminPassword := cfg
	missingInitialAdminPassword.Auth.InitialAdmin.PasswordFile = ""
	if err := missingInitialAdminPassword.Validate(); err == nil {
		t.Fatal("Validate() accepted an initial administrator without a password file")
	}
	sharedListenerConfig := cfg
	sharedListenerConfig.AgentListener.Address = "localhost:9000"
	if err := sharedListenerConfig.Validate(); err == nil {
		t.Fatal("Validate() accepted the HTTP port for the Agent Listener")
	}
	sharedPodAccessConfig := cfg
	sharedPodAccessConfig.PodAccess.Address = "localhost:9000"
	if err := sharedPodAccessConfig.Validate(); err == nil {
		t.Fatal("Validate() accepted the HTTP port for Pod Access")
	}
	insecurePodAccessConfig := cfg
	insecurePodAccessConfig.PodAccess.TLS = TLSIdentityConfig{}
	insecurePodAccessConfig.PodAccess.ExternalURL = "http://access.example.com:10443"
	if err := insecurePodAccessConfig.Validate(); err == nil {
		t.Fatal("Validate() accepted a non-loopback HTTP Pod Access URL")
	}
	partialPodAccessTLSConfig := cfg
	partialPodAccessTLSConfig.PodAccess.TLS.PrivateKeyFile = ""
	if err := partialPodAccessTLSConfig.Validate(); err == nil {
		t.Fatal("Validate() accepted a Pod Access TLS certificate without its private key")
	}
	shortPodAccessSession := cfg
	shortPodAccessSession.PodAccess.SessionTTL = 14 * time.Minute
	if err := shortPodAccessSession.Validate(); err == nil {
		t.Fatal("Validate() accepted a Pod Access maximum session TTL below 15 minutes")
	}
	longPodAccessSession := cfg
	longPodAccessSession.PodAccess.SessionTTL = 61 * time.Minute
	if err := longPodAccessSession.Validate(); err == nil {
		t.Fatal("Validate() accepted a Pod Access maximum session TTL above one hour")
	}
	shortPodAccessTransport := cfg
	shortPodAccessTransport.AgentListener.PodPortForwardRequestTimeout = 29 * time.Minute
	if err := shortPodAccessTransport.Validate(); err == nil {
		t.Fatal("Validate() accepted a Pod Access session TTL above its Agent transport timeout")
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

func TestRepositoryServerConfigLoads(t *testing.T) {
	t.Parallel()

	if _, err := LoadConfig([]string{
		"--config",
		filepath.Join("..", "..", "configs", "zke-server.yaml"),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigManagedPKIAndAgentInstall(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "managed-server.yaml")
	content := []byte(`
http:
  address: 127.0.0.1:8080
  read_header_timeout: 5s
  read_timeout: 15s
  write_timeout: 15s
  idle_timeout: 60s
database:
  url: postgres://example
  connect_timeout: 5s
  migration_timeout: 2m
auth:
  session_idle_timeout: 30m
  session_absolute_timeout: 8h
  operation_timeout: 10s
  max_concurrent_password_checks: 4
  cookie_secure: false
  login_rate_limit:
    window: 1m
    max_attempts_per_account: 5
    max_attempts_per_source: 20
agent_pki:
  mode: managed
  agent_client_certificate_validity: 720h
  managed:
    directory: /var/lib/zke/pki
    auto_generate: true
    agent_client_ca_validity: 87600h
    agent_listener_ca_validity: 175200h
    agent_listener_certificate_validity: 87600h
    agent_listener_renew_before: 8760h
    listener_sans:
      dns_names: [zke.example.com]
      ip_addresses: [127.0.0.1]
agent_enrollment:
  operation_timeout: 10s
  rate_limit:
    window: 1m
    max_attempts_per_source: 30
agent_install:
  enabled: true
  public_http_url: https://zke.example.com
  public_quic_address: zke.example.com:8443
  image: registry.example.com/zke-agent:test
  namespace: zke-system
  image_pull_policy: IfNotPresent
agent_listener:
  address: 127.0.0.1:8443
  handshake_timeout: 10s
  heartbeat_interval: 10s
  heartbeat_timeout: 30s
  last_seen_write_interval: 1m
  operation_timeout: 10s
certificate_monitor:
  warning_before: 720h
  check_interval: 1h
shutdown_timeout: 10s
log_level: info
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentPKI.Mode != "managed" ||
		cfg.AgentPKI.Managed.Directory != "/var/lib/zke/pki" ||
		!cfg.AgentPKI.Managed.AutoGenerate ||
		len(cfg.AgentPKI.Managed.ListenerSANs.DNSNames) != 1 {
		t.Fatalf("unexpected managed Agent PKI config: %+v", cfg.AgentPKI)
	}
	if !cfg.AgentInstall.Enabled ||
		cfg.AgentInstall.PublicHTTPURL != "https://zke.example.com" ||
		cfg.AgentInstall.Image != "registry.example.com/zke-agent:test" {
		t.Fatalf("unexpected Agent installation config: %+v", cfg.AgentInstall)
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
		AgentIdentity: AgentIdentityConfig{
			CACertificateFile: "/agent-client-ca.crt",
			CAPrivateKeyFile:  "/agent-client-ca.key",
			CertificateTTL:    30 * 24 * time.Hour,
		},
		AgentEnrollment: AgentEnrollmentConfig{
			OperationTimeout: 10 * time.Second,
			RateLimit: AgentEnrollmentRateLimitConfig{
				Window:               time.Minute,
				MaxAttemptsPerSource: 30,
			},
		},
		AgentListener: AgentListenerConfig{
			Address: "127.0.0.1:8443",
			TLS: TLSIdentityConfig{
				CertificateFile: "/server.crt",
				PrivateKeyFile:  "/server.key",
			},
			HandshakeTimeout:      10 * time.Second,
			HeartbeatInterval:     10 * time.Second,
			HeartbeatTimeout:      30 * time.Second,
			LastSeenWriteInterval: time.Minute,
			OperationTimeout:      10 * time.Second,
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
		AgentIdentity: AgentIdentityConfig{
			CACertificateFile: "/agent-client-ca.crt",
			CAPrivateKeyFile:  "/agent-client-ca.key",
			CertificateTTL:    30 * 24 * time.Hour,
		},
		AgentEnrollment: AgentEnrollmentConfig{
			OperationTimeout: 10 * time.Second,
			RateLimit: AgentEnrollmentRateLimitConfig{
				Window:               time.Minute,
				MaxAttemptsPerSource: 30,
			},
		},
		AgentListener: AgentListenerConfig{
			Address: "127.0.0.1:8443",
			TLS: TLSIdentityConfig{
				CertificateFile: "/server.crt",
				PrivateKeyFile:  "/server.key",
			},
			HandshakeTimeout:      10 * time.Second,
			HeartbeatInterval:     10 * time.Second,
			HeartbeatTimeout:      30 * time.Second,
			LastSeenWriteInterval: time.Minute,
			OperationTimeout:      10 * time.Second,
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
		AgentIdentity: AgentIdentityConfig{
			CACertificateFile: "/agent-client-ca.crt",
			CAPrivateKeyFile:  "/agent-client-ca.key",
			CertificateTTL:    30 * 24 * time.Hour,
		},
		AgentEnrollment: AgentEnrollmentConfig{
			OperationTimeout: 9 * time.Second,
			RateLimit: AgentEnrollmentRateLimitConfig{
				Window:               time.Minute,
				MaxAttemptsPerSource: 30,
			},
		},
		AgentListener: AgentListenerConfig{
			Address: "127.0.0.1:8443",
			TLS: TLSIdentityConfig{
				CertificateFile: "/server.crt",
				PrivateKeyFile:  "/server.key",
			},
			HandshakeTimeout:      10 * time.Second,
			HeartbeatInterval:     10 * time.Second,
			HeartbeatTimeout:      30 * time.Second,
			LastSeenWriteInterval: time.Minute,
			OperationTimeout:      10 * time.Second,
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

func TestLoadConfigRejectsFlatNativeHTTPTLSFields(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "server.yaml")
	content := []byte(`
http:
  address: 127.0.0.1:8080
  tls_certificate_file: /run/secrets/http.crt
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfig([]string{"--config", path}); err == nil {
		t.Fatal("LoadConfig() accepted flat native HTTP TLS configuration")
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
