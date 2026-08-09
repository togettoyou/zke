package server

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config mirrors the YAML configuration file one-to-one. Decoding happens
// directly into a value pre-populated with defaults, so a field is added in a
// single place and an absent key simply keeps its default.
type Config struct {
	HTTP               HTTPConfig               `yaml:"http"`
	PodAccess          PodAccessConfig          `yaml:"pod_access"`
	Database           DatabaseConfig           `yaml:"database"`
	Auth               AuthConfig               `yaml:"auth"`
	AgentPKI           AgentPKIConfig           `yaml:"agent_pki"`
	AgentEnrollment    AgentEnrollmentConfig    `yaml:"agent_enrollment"`
	AgentInstall       AgentInstallConfig       `yaml:"agent_install"`
	AgentListener      AgentListenerConfig      `yaml:"agent_listener"`
	CertificateMonitor CertificateMonitorConfig `yaml:"certificate_monitor"`
	ShutdownTimeout    time.Duration            `yaml:"shutdown_timeout"`
	LogLevel           string                   `yaml:"log_level"`

	// AgentIdentity is derived from agent_pki once the PKI mode is known and
	// is never read from the configuration file.
	AgentIdentity AgentIdentityConfig `yaml:"-"`
}

type HTTPConfig struct {
	Address           string            `yaml:"address"`
	TLS               TLSIdentityConfig `yaml:"tls"`
	ReadHeaderTimeout time.Duration     `yaml:"read_header_timeout"`
	ReadTimeout       time.Duration     `yaml:"read_timeout"`
	WriteTimeout      time.Duration     `yaml:"write_timeout"`
	IdleTimeout       time.Duration     `yaml:"idle_timeout"`
}

// PodAccessConfig owns a second HTTP listener whose origin is reserved for
// proxied Pod applications. It deliberately does not share the API router:
// arbitrary Pod content must never execute in the Console/API origin.
// SessionTTL is the maximum duration a user may select for one access session.
type PodAccessConfig struct {
	Enabled                  bool              `yaml:"enabled"`
	Address                  string            `yaml:"address"`
	ExternalURL              string            `yaml:"external_url"`
	TLS                      TLSIdentityConfig `yaml:"tls"`
	ReadHeaderTimeout        time.Duration     `yaml:"read_header_timeout"`
	IdleTimeout              time.Duration     `yaml:"idle_timeout"`
	ActivationTTL            time.Duration     `yaml:"activation_ttl"`
	SessionTTL               time.Duration     `yaml:"session_ttl"`
	RevalidateInterval       time.Duration     `yaml:"revalidate_interval"`
	MaxPendingSessions       int               `yaml:"max_pending_sessions"`
	MaxActiveSessions        int               `yaml:"max_active_sessions"`
	MaxConnections           int               `yaml:"max_connections"`
	MaxConnectionsPerSession int               `yaml:"max_connections_per_session"`
	MaxConnectionsPerAgent   int               `yaml:"max_connections_per_agent"`
	MaxClientBytes           uint64            `yaml:"max_client_bytes"`
	MaxPodBytes              uint64            `yaml:"max_pod_bytes"`
}

type TLSIdentityConfig struct {
	CertificateFile string `yaml:"certificate_file"`
	PrivateKeyFile  string `yaml:"private_key_file"`
}

type CACertificateConfig struct {
	CertificateFile string `yaml:"certificate_file"`
}

type DatabaseConfig struct {
	URL              string        `yaml:"url"`
	ConnectTimeout   time.Duration `yaml:"connect_timeout"`
	MigrationTimeout time.Duration `yaml:"migration_timeout"`
	MaxConnections   int32         `yaml:"max_connections"`
	MinConnections   int32         `yaml:"min_connections"`
	MaxConnLifetime  time.Duration `yaml:"max_connection_lifetime"`
	MaxConnIdleTime  time.Duration `yaml:"max_connection_idle_time"`
}

type AuthConfig struct {
	SessionIdleTimeout          time.Duration        `yaml:"session_idle_timeout"`
	SessionAbsoluteTimeout      time.Duration        `yaml:"session_absolute_timeout"`
	OperationTimeout            time.Duration        `yaml:"operation_timeout"`
	MaxConcurrentPasswordChecks int                  `yaml:"max_concurrent_password_checks"`
	CookieSecure                bool                 `yaml:"cookie_secure"`
	LoginRateLimit              LoginRateLimitConfig `yaml:"login_rate_limit"`
	AccountLockout              AccountLockoutConfig `yaml:"account_lockout"`
	InitialAdmin                InitialAdminConfig   `yaml:"initial_admin"`
}

type LoginRateLimitConfig struct {
	Window                time.Duration `yaml:"window"`
	MaxAttemptsPerAccount int           `yaml:"max_attempts_per_account"`
	MaxAttemptsPerSource  int           `yaml:"max_attempts_per_source"`
}

type AccountLockoutConfig struct {
	MaxFailedAttempts int           `yaml:"max_failed_attempts"`
	Duration          time.Duration `yaml:"duration"`
}

type InitialAdminConfig struct {
	Enabled              bool   `yaml:"enabled"`
	Username             string `yaml:"username"`
	DisplayName          string `yaml:"display_name"`
	PasswordFile         string `yaml:"password_file"`
	AutoGeneratePassword bool   `yaml:"auto_generate_password"`
}

// AgentIdentityConfig is derived from AgentPKIConfig rather than configured
// directly, so that a deployment cannot mix a managed PKI with hand-written
// CA paths.
type AgentIdentityConfig struct {
	CACertificateFile string
	CAPrivateKeyFile  string
	CertificateTTL    time.Duration
}

type AgentPKIConfig struct {
	Mode                           string                 `yaml:"mode"`
	AgentClientCertificateValidity time.Duration          `yaml:"agent_client_certificate_validity"`
	Managed                        ManagedAgentPKIConfig  `yaml:"managed"`
	External                       ExternalAgentPKIConfig `yaml:"external"`
}

type ManagedAgentPKIConfig struct {
	Directory                string             `yaml:"directory"`
	AutoGenerate             bool               `yaml:"auto_generate"`
	AgentClientCAValidity    time.Duration      `yaml:"agent_client_ca_validity"`
	AgentListenerCAValidity  time.Duration      `yaml:"agent_listener_ca_validity"`
	AgentListenerValidity    time.Duration      `yaml:"agent_listener_certificate_validity"`
	AgentListenerRenewBefore time.Duration      `yaml:"agent_listener_renew_before"`
	ListenerSANs             ListenerSANsConfig `yaml:"listener_sans"`
}

type ListenerSANsConfig struct {
	DNSNames    []string `yaml:"dns_names"`
	IPAddresses []string `yaml:"ip_addresses"`
}

type ExternalAgentPKIConfig struct {
	AgentClientCA   TLSIdentityConfig   `yaml:"agent_client_ca"`
	AgentListenerCA CACertificateConfig `yaml:"agent_listener_ca"`
	AgentListener   TLSIdentityConfig   `yaml:"agent_listener"`
}

type AgentEnrollmentConfig struct {
	OperationTimeout time.Duration                  `yaml:"operation_timeout"`
	RateLimit        AgentEnrollmentRateLimitConfig `yaml:"rate_limit"`
}

type AgentEnrollmentRateLimitConfig struct {
	Window               time.Duration `yaml:"window"`
	MaxAttemptsPerSource int           `yaml:"max_attempts_per_source"`
}

type AgentInstallConfig struct {
	Enabled                       bool   `yaml:"enabled"`
	PublicHTTPURL                 string `yaml:"public_http_url"`
	PublicQUICAddress             string `yaml:"public_quic_address"`
	Image                         string `yaml:"image"`
	Namespace                     string `yaml:"namespace"`
	ImagePullPolicy               string `yaml:"image_pull_policy"`
	RegistrationCACertificateFile string `yaml:"registration_ca_certificate_file"`
}

type CertificateMonitorConfig struct {
	WarningBefore time.Duration `yaml:"warning_before"`
	CheckInterval time.Duration `yaml:"check_interval"`
}

type AgentListenerConfig struct {
	Address                     string        `yaml:"address"`
	HandshakeTimeout            time.Duration `yaml:"handshake_timeout"`
	HeartbeatInterval           time.Duration `yaml:"heartbeat_interval"`
	HeartbeatTimeout            time.Duration `yaml:"heartbeat_timeout"`
	LastSeenWriteInterval       time.Duration `yaml:"last_seen_write_interval"`
	OperationTimeout            time.Duration `yaml:"operation_timeout"`
	WriteTimeout                time.Duration `yaml:"write_timeout"`
	MaxConcurrentAgents         int           `yaml:"max_concurrent_agents"`
	MaxIncomingStreams          int64         `yaml:"max_incoming_streams"`
	MaxRememberedDisconnects    int           `yaml:"max_remembered_disconnects"`
	ResourceRequestTimeout      time.Duration `yaml:"resource_request_timeout"`
	ConnectionDrainTimeout      time.Duration `yaml:"connection_drain_timeout"`
	MaxResourceBodyBytes        uint64        `yaml:"max_resource_body_bytes"`
	MaxBufferedResourceBytes    uint64        `yaml:"max_buffered_resource_response_bytes"`
	MaxResourceStreams          int           `yaml:"max_resource_streams_per_agent"`
	MaxResourceRequests         int           `yaml:"max_concurrent_resource_requests"`
	PodLogsRequestTimeout       time.Duration `yaml:"pod_logs_request_timeout"`
	MaxPodLogBytes              uint64        `yaml:"max_pod_log_bytes"`
	MaxPodLogsStreams           int           `yaml:"max_pod_logs_streams_per_agent"`
	MaxPodLogsRequests          int           `yaml:"max_concurrent_pod_logs_requests"`
	PodExecRequestTimeout       time.Duration `yaml:"pod_exec_request_timeout"`
	MaxPodExecInputBytes        uint64        `yaml:"max_pod_exec_input_bytes"`
	MaxPodExecOutputBytes       uint64        `yaml:"max_pod_exec_output_bytes"`
	MaxPodExecStreams           int           `yaml:"max_pod_exec_streams_per_agent"`
	MaxPodExecRequests          int           `yaml:"max_concurrent_pod_exec_requests"`
	PodExecSessionTTL           time.Duration `yaml:"pod_exec_session_ttl"`
	MaxPendingPodExecSessions   int           `yaml:"max_pending_pod_exec_sessions"`
	ResourceWatchRequestTimeout time.Duration `yaml:"resource_watch_request_timeout"`
	MaxResourceWatchStreams     int           `yaml:"max_resource_watch_streams_per_agent"`
	MaxResourceWatchRequests    int           `yaml:"max_concurrent_resource_watch_requests"`

	// TLS is derived from agent_pki, not configured under agent_listener.
	TLS TLSIdentityConfig `yaml:"-"`
}

// DefaultConfig reports the configuration used when the file omits a key.
func DefaultConfig() Config {
	return Config{
		PodAccess: PodAccessConfig{
			Address:                  "127.0.0.1:8081",
			ReadHeaderTimeout:        5 * time.Second,
			IdleTimeout:              60 * time.Second,
			ActivationTTL:            30 * time.Second,
			SessionTTL:               time.Hour,
			RevalidateInterval:       15 * time.Second,
			MaxPendingSessions:       1024,
			MaxActiveSessions:        256,
			MaxConnections:           128,
			MaxConnectionsPerSession: 8,
			MaxConnectionsPerAgent:   16,
			MaxClientBytes:           1024 * 1024 * 1024,
			MaxPodBytes:              1024 * 1024 * 1024,
		},
		Database: DatabaseConfig{
			MaxConnections:  16,
			MinConnections:  2,
			MaxConnLifetime: time.Hour,
			MaxConnIdleTime: 30 * time.Minute,
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
			AccountLockout: AccountLockoutConfig{
				MaxFailedAttempts: 5,
				Duration:          15 * time.Minute,
			},
		},
		AgentPKI: AgentPKIConfig{
			Mode:                           "external",
			AgentClientCertificateValidity: 30 * 24 * time.Hour,
			Managed: ManagedAgentPKIConfig{
				AutoGenerate:             true,
				AgentClientCAValidity:    10 * 365 * 24 * time.Hour,
				AgentListenerCAValidity:  20 * 365 * 24 * time.Hour,
				AgentListenerValidity:    10 * 365 * 24 * time.Hour,
				AgentListenerRenewBefore: 365 * 24 * time.Hour,
			},
		},
		AgentEnrollment: AgentEnrollmentConfig{
			OperationTimeout: 10 * time.Second,
			RateLimit: AgentEnrollmentRateLimitConfig{
				Window:               time.Minute,
				MaxAttemptsPerSource: 30,
			},
		},
		AgentInstall: AgentInstallConfig{
			Namespace:       "zke-system",
			ImagePullPolicy: "IfNotPresent",
		},
		AgentListener: AgentListenerConfig{
			HandshakeTimeout:            10 * time.Second,
			HeartbeatInterval:           10 * time.Second,
			HeartbeatTimeout:            30 * time.Second,
			LastSeenWriteInterval:       time.Minute,
			OperationTimeout:            10 * time.Second,
			WriteTimeout:                5 * time.Second,
			MaxConcurrentAgents:         1024,
			MaxIncomingStreams:          16,
			MaxRememberedDisconnects:    4096,
			ResourceRequestTimeout:      2 * time.Minute,
			ConnectionDrainTimeout:      10 * time.Second,
			MaxResourceBodyBytes:        32 * 1024 * 1024,
			MaxBufferedResourceBytes:    256 * 1024 * 1024,
			MaxResourceStreams:          64,
			MaxResourceRequests:         4096,
			PodLogsRequestTimeout:       30 * time.Minute,
			MaxPodLogBytes:              16 * 1024 * 1024,
			MaxPodLogsStreams:           8,
			MaxPodLogsRequests:          256,
			PodExecRequestTimeout:       15 * time.Minute,
			MaxPodExecInputBytes:        16 * 1024 * 1024,
			MaxPodExecOutputBytes:       32 * 1024 * 1024,
			MaxPodExecStreams:           4,
			MaxPodExecRequests:          128,
			PodExecSessionTTL:           30 * time.Second,
			MaxPendingPodExecSessions:   1024,
			ResourceWatchRequestTimeout: 30 * time.Minute,
			MaxResourceWatchStreams:     16,
			MaxResourceWatchRequests:    512,
		},
		CertificateMonitor: CertificateMonitorConfig{
			WarningBefore: 30 * 24 * time.Hour,
			CheckInterval: time.Hour,
		},
	}
}

func LoadConfig(args []string) (Config, error) {
	configPath, err := findConfigPath(args)
	if err != nil {
		return Config{}, err
	}
	if configPath == "" {
		return Config{}, errors.New("--config is required")
	}

	cfg := DefaultConfig()
	if err := decodeConfigFile(&cfg, configPath); err != nil {
		return Config{}, err
	}
	cfg.resolveDerivedIdentity()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func decodeConfigFile(cfg *Config, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open config file %q: %w", path, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return fmt.Errorf("decode config file %q: %w", path, err)
	}
	if err := ensureYAMLEOF(decoder); err != nil {
		return fmt.Errorf("decode config file %q: %w", path, err)
	}
	return nil
}

// resolveDerivedIdentity fills in the certificate paths the rest of the Server
// consumes, choosing between the managed and external PKI layouts.
func (cfg *Config) resolveDerivedIdentity() {
	cfg.AgentIdentity.CertificateTTL = cfg.AgentPKI.AgentClientCertificateValidity
	if cfg.AgentPKI.Mode != "external" {
		return
	}
	cfg.AgentIdentity.CACertificateFile =
		cfg.AgentPKI.External.AgentClientCA.CertificateFile
	cfg.AgentIdentity.CAPrivateKeyFile =
		cfg.AgentPKI.External.AgentClientCA.PrivateKeyFile
	cfg.AgentListener.TLS.CertificateFile =
		cfg.AgentPKI.External.AgentListener.CertificateFile
	cfg.AgentListener.TLS.PrivateKeyFile =
		cfg.AgentPKI.External.AgentListener.PrivateKeyFile
}

func findConfigPath(args []string) (string, error) {
	fs := flag.NewFlagSet("zke-server", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	configPath := ""
	fs.StringVar(&configPath, "config", "", "path to a YAML configuration file")
	if err := fs.Parse(args); err != nil {
		return "", fmt.Errorf("parse flags: %w", err)
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}

	return configPath, nil
}

func ensureYAMLEOF(decoder *yaml.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("configuration contains multiple YAML documents")
}
