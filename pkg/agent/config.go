package agent

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"gopkg.in/yaml.v3"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	defaultIdentityNamespace  = "zke-system"
	defaultIdentitySecretName = "zke-agent-identity"
	defaultLogLevel           = "info"
	maxAgentIncomingStreams   = 4096
	maxResourceBodyBytes      = 1024 * 1024 * 1024
)

type Config struct {
	KubeconfigFile         string
	IdentityNamespace      string
	IdentitySecretName     string
	CertificateRenewBefore time.Duration
	Registration           RegistrationConfig
	Connection             ConnectionConfig
	MetricsIngest          MetricsIngestConfig
	LogLevel               string
}

// MetricsIngestConfig configures the collector-facing endpoint and the Stream
// that forwards what it receives.
//
// There is no on/off switch here. Whether metrics flow is the Server's
// decision — it advertises the ingest capability only when it has storage, and
// the collector is installed only when an operator asks for it — so a switch in
// this file could only disagree with that. The endpoint is always listening and
// always authenticated; until a collector is installed no credential exists, so
// it authorizes nobody.
type MetricsIngestConfig struct {
	// Address is reachable inside the Cluster only. The endpoint serves
	// nothing but remote write, and it is never exposed through an Ingress.
	Address string
	// AdvertisedURL is the origin the in-cluster collector is told to write to.
	//
	// Empty means "the ClusterIP Service in front of this Agent", which is
	// right whenever the Agent runs as a Pod. It exists for the case where it
	// does not: an Agent started on a developer's machine has no Pod, so no
	// Endpoint, and a collector pointed at the Service would retry forever.
	// Setting it to the address the Cluster can reach the host on — commonly
	// http://host.docker.internal:8429 — makes that setup work without
	// pretending the Agent is somewhere it is not.
	AdvertisedURL        string
	MaxBatchBytes        uint64
	MaxConcurrentBatches int
	// SessionTimeout bounds one ingest Stream. The Stream is reopened on the
	// next batch, so this is a lifetime cap rather than an idle timeout.
	SessionTimeout        time.Duration
	TokenRefreshInterval  time.Duration
	UnavailableRetryAfter time.Duration
	ReadHeaderTimeout     time.Duration
	ReadTimeout           time.Duration
	WriteTimeout          time.Duration
	IdleTimeout           time.Duration
	ShutdownTimeout       time.Duration
}

type RegistrationConfig struct {
	ServerURL            string
	CACertificateFile    string
	CACertificatePEM     []byte
	Timeout              time.Duration
	RetryInitialInterval time.Duration
	RetryMaxInterval     time.Duration
}

type ConnectionConfig struct {
	ServerAddress        string
	CACertificateFile    string
	CACertificatePEM     []byte
	ConnectTimeout       time.Duration
	RetryInitialInterval time.Duration
	RetryMaxInterval     time.Duration
	// IdleTimeout and KeepAliveInterval configure the QUIC transport. They are
	// applied when the connection is dialled, which is before the Server can
	// announce its application-level heartbeat timings in ServerHello, so the
	// two cannot be derived from each other. IdleTimeout must stay above the
	// Server's heartbeat timeout, otherwise the transport would tear down a
	// connection the application still considers healthy.
	IdleTimeout       time.Duration
	KeepAliveInterval time.Duration
	// MaxIncomingStreams bounds the streams a Server may open towards this
	// Agent. The Agent-created Control Stream is not counted in this direction.
	MaxIncomingStreams                int64
	StreamHeaderTimeout               time.Duration
	MaxResourceRequestTimeout         time.Duration
	MaxConcurrentResourceStreams      int
	MaxResourceBodyBytes              uint64
	MaxPodLogsStreamTimeout           time.Duration
	MaxConcurrentPodLogsStreams       int
	MaxPodLogBytes                    uint64
	MaxPodExecStreamTimeout           time.Duration
	MaxConcurrentPodExecStreams       int
	MaxPodExecInputBytes              uint64
	MaxPodExecOutputBytes             uint64
	MaxPodAccessStreamTimeout         time.Duration
	MaxConcurrentPodAccessStreams     int
	MaxPodAccessClientBytes           uint64
	MaxPodAccessPodBytes              uint64
	MaxResourceWatchStreamTimeout     time.Duration
	MaxConcurrentResourceWatchStreams int
	// Helm gets its own budget rather than borrowing the resource one. An
	// install that waits for a rollout is minutes of work, not the seconds a
	// single-object write is, and one release change at a time per Cluster is
	// deliberate: two concurrent operations on the same release race over
	// Helm's own storage, and Helm has no lock to stop them.
	MaxHelmStreamTimeout     time.Duration
	MaxConcurrentHelmStreams int
}

type fileConfig struct {
	KubeconfigFile string `yaml:"kubeconfig_file"`
	Identity       struct {
		Namespace   string `yaml:"namespace"`
		SecretName  string `yaml:"secret_name"`
		RenewBefore string `yaml:"renew_before"`
	} `yaml:"identity"`
	Registration struct {
		ServerURL            string `yaml:"server_url"`
		CACertificateFile    string `yaml:"ca_certificate_file"`
		Timeout              string `yaml:"timeout"`
		RetryInitialInterval string `yaml:"retry_initial_interval"`
		RetryMaxInterval     string `yaml:"retry_max_interval"`
	} `yaml:"registration"`
	Connection struct {
		ServerAddress                     string  `yaml:"server_address"`
		CACertificateFile                 string  `yaml:"ca_certificate_file"`
		ConnectTimeout                    string  `yaml:"connect_timeout"`
		RetryInitialInterval              string  `yaml:"retry_initial_interval"`
		RetryMaxInterval                  string  `yaml:"retry_max_interval"`
		IdleTimeout                       string  `yaml:"idle_timeout"`
		KeepAliveInterval                 string  `yaml:"keep_alive_interval"`
		MaxIncomingStreams                *int64  `yaml:"max_incoming_streams"`
		StreamHeaderTimeout               string  `yaml:"stream_header_timeout"`
		MaxResourceRequestTimeout         string  `yaml:"max_resource_request_timeout"`
		MaxConcurrentResourceStreams      *int    `yaml:"max_concurrent_resource_streams"`
		MaxResourceBodyBytes              *uint64 `yaml:"max_resource_body_bytes"`
		MaxPodLogsStreamTimeout           string  `yaml:"max_pod_logs_stream_timeout"`
		MaxConcurrentPodLogsStreams       *int    `yaml:"max_concurrent_pod_logs_streams"`
		MaxPodLogBytes                    *uint64 `yaml:"max_pod_log_bytes"`
		MaxPodExecStreamTimeout           string  `yaml:"max_pod_exec_stream_timeout"`
		MaxConcurrentPodExecStreams       *int    `yaml:"max_concurrent_pod_exec_streams"`
		MaxPodExecInputBytes              *uint64 `yaml:"max_pod_exec_input_bytes"`
		MaxPodExecOutputBytes             *uint64 `yaml:"max_pod_exec_output_bytes"`
		MaxPodAccessStreamTimeout         string  `yaml:"max_pod_access_stream_timeout"`
		MaxConcurrentPodAccessStreams     *int    `yaml:"max_concurrent_pod_access_streams"`
		MaxPodAccessClientBytes           *uint64 `yaml:"max_pod_access_client_bytes"`
		MaxPodAccessPodBytes              *uint64 `yaml:"max_pod_access_pod_bytes"`
		MaxResourceWatchStreamTimeout     string  `yaml:"max_resource_watch_stream_timeout"`
		MaxConcurrentResourceWatchStreams *int    `yaml:"max_concurrent_resource_watch_streams"`
		MaxHelmStreamTimeout              string  `yaml:"max_helm_stream_timeout"`
		MaxConcurrentHelmStreams          *int    `yaml:"max_concurrent_helm_streams"`
	} `yaml:"connection"`
	MetricsIngest struct {
		Address               string  `yaml:"address"`
		AdvertisedURL         string  `yaml:"advertised_url"`
		MaxBatchBytes         *uint64 `yaml:"max_batch_bytes"`
		MaxConcurrentBatches  *int    `yaml:"max_concurrent_batches"`
		SessionTimeout        string  `yaml:"session_timeout"`
		TokenRefreshInterval  string  `yaml:"token_refresh_interval"`
		UnavailableRetryAfter string  `yaml:"unavailable_retry_after"`
		ReadHeaderTimeout     string  `yaml:"read_header_timeout"`
		ReadTimeout           string  `yaml:"read_timeout"`
		WriteTimeout          string  `yaml:"write_timeout"`
		IdleTimeout           string  `yaml:"idle_timeout"`
		ShutdownTimeout       string  `yaml:"shutdown_timeout"`
	} `yaml:"metrics_ingest"`
	LogLevel string `yaml:"log_level"`
}

// DefaultConfig reports the configuration used when the file omits a key.
func DefaultConfig() Config {
	return Config{
		IdentityNamespace:      defaultIdentityNamespace,
		IdentitySecretName:     defaultIdentitySecretName,
		CertificateRenewBefore: 7 * 24 * time.Hour,
		Registration: RegistrationConfig{
			ServerURL:            "http://127.0.0.1:8080",
			Timeout:              10 * time.Second,
			RetryInitialInterval: time.Second,
			RetryMaxInterval:     15 * time.Second,
		},
		Connection: ConnectionConfig{
			ServerAddress:                     "127.0.0.1:8443",
			ConnectTimeout:                    10 * time.Second,
			RetryInitialInterval:              time.Second,
			RetryMaxInterval:                  30 * time.Second,
			IdleTimeout:                       15 * time.Minute,
			KeepAliveInterval:                 10 * time.Second,
			MaxIncomingStreams:                128,
			StreamHeaderTimeout:               5 * time.Second,
			MaxResourceRequestTimeout:         2 * time.Minute,
			MaxConcurrentResourceStreams:      64,
			MaxResourceBodyBytes:              32 * 1024 * 1024,
			MaxPodLogsStreamTimeout:           30 * time.Minute,
			MaxConcurrentPodLogsStreams:       8,
			MaxPodLogBytes:                    16 * 1024 * 1024,
			MaxPodExecStreamTimeout:           15 * time.Minute,
			MaxConcurrentPodExecStreams:       4,
			MaxPodExecInputBytes:              16 * 1024 * 1024,
			MaxPodExecOutputBytes:             32 * 1024 * 1024,
			MaxPodAccessStreamTimeout:         time.Hour,
			MaxConcurrentPodAccessStreams:     16,
			MaxPodAccessClientBytes:           1024 * 1024 * 1024,
			MaxPodAccessPodBytes:              1024 * 1024 * 1024,
			MaxResourceWatchStreamTimeout:     30 * time.Minute,
			MaxConcurrentResourceWatchStreams: 16,
			MaxHelmStreamTimeout:              15 * time.Minute,
			MaxConcurrentHelmStreams:          1,
		},
		MetricsIngest: MetricsIngestConfig{
			Address:               "0.0.0.0:8429",
			MaxBatchBytes:         agentprotocol.DefaultMaxMetricsBatchBytes,
			MaxConcurrentBatches:  4,
			SessionTimeout:        agentprotocol.DefaultMetricsIngestTimeout,
			TokenRefreshInterval:  time.Minute,
			UnavailableRetryAfter: 15 * time.Second,
			ReadHeaderTimeout:     5 * time.Second,
			ReadTimeout:           30 * time.Second,
			WriteTimeout:          30 * time.Second,
			IdleTimeout:           60 * time.Second,
			ShutdownTimeout:       5 * time.Second,
		},
		LogLevel: defaultLogLevel,
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
	if err := applyFile(&cfg, configPath); err != nil {
		return Config{}, err
	}
	applyEnvironmentOverrides(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func applyFile(cfg *Config, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open config file %q: %w", path, err)
	}
	defer file.Close()

	var raw fileConfig
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode config file %q: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode config file %q: multiple YAML documents", path)
		}
		return fmt.Errorf("decode config file %q: %w", path, err)
	}

	if raw.KubeconfigFile != "" {
		cfg.KubeconfigFile = raw.KubeconfigFile
	}
	if raw.Identity.Namespace != "" {
		cfg.IdentityNamespace = raw.Identity.Namespace
	}
	if raw.Identity.SecretName != "" {
		cfg.IdentitySecretName = raw.Identity.SecretName
	}
	if err := applyAgentDuration(
		&cfg.CertificateRenewBefore,
		raw.Identity.RenewBefore,
		"identity.renew_before",
	); err != nil {
		return err
	}
	if raw.Registration.ServerURL != "" {
		cfg.Registration.ServerURL = raw.Registration.ServerURL
	}
	if raw.Registration.CACertificateFile != "" {
		cfg.Registration.CACertificateFile =
			raw.Registration.CACertificateFile
	}
	if err := applyAgentDuration(
		&cfg.Registration.Timeout,
		raw.Registration.Timeout,
		"registration.timeout",
	); err != nil {
		return err
	}
	if err := applyAgentDuration(
		&cfg.Registration.RetryInitialInterval,
		raw.Registration.RetryInitialInterval,
		"registration.retry_initial_interval",
	); err != nil {
		return err
	}
	if err := applyAgentDuration(
		&cfg.Registration.RetryMaxInterval,
		raw.Registration.RetryMaxInterval,
		"registration.retry_max_interval",
	); err != nil {
		return err
	}
	if raw.Connection.ServerAddress != "" {
		cfg.Connection.ServerAddress = raw.Connection.ServerAddress
	}
	if raw.Connection.CACertificateFile != "" {
		cfg.Connection.CACertificateFile =
			raw.Connection.CACertificateFile
	}
	if err := applyAgentDuration(
		&cfg.Connection.ConnectTimeout,
		raw.Connection.ConnectTimeout,
		"connection.connect_timeout",
	); err != nil {
		return err
	}
	if err := applyAgentDuration(
		&cfg.Connection.RetryInitialInterval,
		raw.Connection.RetryInitialInterval,
		"connection.retry_initial_interval",
	); err != nil {
		return err
	}
	if err := applyAgentDuration(
		&cfg.Connection.RetryMaxInterval,
		raw.Connection.RetryMaxInterval,
		"connection.retry_max_interval",
	); err != nil {
		return err
	}
	if err := applyAgentDuration(
		&cfg.Connection.IdleTimeout,
		raw.Connection.IdleTimeout,
		"connection.idle_timeout",
	); err != nil {
		return err
	}
	if err := applyAgentDuration(
		&cfg.Connection.KeepAliveInterval,
		raw.Connection.KeepAliveInterval,
		"connection.keep_alive_interval",
	); err != nil {
		return err
	}
	if raw.Connection.MaxIncomingStreams != nil {
		cfg.Connection.MaxIncomingStreams = *raw.Connection.MaxIncomingStreams
	}
	if err := applyAgentDuration(
		&cfg.Connection.StreamHeaderTimeout,
		raw.Connection.StreamHeaderTimeout,
		"connection.stream_header_timeout",
	); err != nil {
		return err
	}
	if err := applyAgentDuration(
		&cfg.Connection.MaxResourceRequestTimeout,
		raw.Connection.MaxResourceRequestTimeout,
		"connection.max_resource_request_timeout",
	); err != nil {
		return err
	}
	if raw.Connection.MaxConcurrentResourceStreams != nil {
		cfg.Connection.MaxConcurrentResourceStreams =
			*raw.Connection.MaxConcurrentResourceStreams
	}
	if raw.Connection.MaxResourceBodyBytes != nil {
		cfg.Connection.MaxResourceBodyBytes =
			*raw.Connection.MaxResourceBodyBytes
	}
	if err := applyAgentDuration(
		&cfg.Connection.MaxPodLogsStreamTimeout,
		raw.Connection.MaxPodLogsStreamTimeout,
		"connection.max_pod_logs_stream_timeout",
	); err != nil {
		return err
	}
	if raw.Connection.MaxConcurrentPodLogsStreams != nil {
		cfg.Connection.MaxConcurrentPodLogsStreams =
			*raw.Connection.MaxConcurrentPodLogsStreams
	}
	if raw.Connection.MaxPodLogBytes != nil {
		cfg.Connection.MaxPodLogBytes = *raw.Connection.MaxPodLogBytes
	}
	if err := applyAgentDuration(
		&cfg.Connection.MaxPodExecStreamTimeout,
		raw.Connection.MaxPodExecStreamTimeout,
		"connection.max_pod_exec_stream_timeout",
	); err != nil {
		return err
	}
	if raw.Connection.MaxConcurrentPodExecStreams != nil {
		cfg.Connection.MaxConcurrentPodExecStreams = *raw.Connection.MaxConcurrentPodExecStreams
	}
	if raw.Connection.MaxPodExecInputBytes != nil {
		cfg.Connection.MaxPodExecInputBytes = *raw.Connection.MaxPodExecInputBytes
	}
	if raw.Connection.MaxPodExecOutputBytes != nil {
		cfg.Connection.MaxPodExecOutputBytes = *raw.Connection.MaxPodExecOutputBytes
	}
	if err := applyAgentDuration(
		&cfg.Connection.MaxPodAccessStreamTimeout,
		raw.Connection.MaxPodAccessStreamTimeout,
		"connection.max_pod_access_stream_timeout",
	); err != nil {
		return err
	}
	if raw.Connection.MaxConcurrentPodAccessStreams != nil {
		cfg.Connection.MaxConcurrentPodAccessStreams = *raw.Connection.MaxConcurrentPodAccessStreams
	}
	if raw.Connection.MaxPodAccessClientBytes != nil {
		cfg.Connection.MaxPodAccessClientBytes = *raw.Connection.MaxPodAccessClientBytes
	}
	if raw.Connection.MaxPodAccessPodBytes != nil {
		cfg.Connection.MaxPodAccessPodBytes = *raw.Connection.MaxPodAccessPodBytes
	}
	if err := applyAgentDuration(
		&cfg.Connection.MaxResourceWatchStreamTimeout,
		raw.Connection.MaxResourceWatchStreamTimeout,
		"connection.max_resource_watch_stream_timeout",
	); err != nil {
		return err
	}
	if raw.Connection.MaxConcurrentResourceWatchStreams != nil {
		cfg.Connection.MaxConcurrentResourceWatchStreams =
			*raw.Connection.MaxConcurrentResourceWatchStreams
	}
	if err := applyAgentDuration(
		&cfg.Connection.MaxHelmStreamTimeout,
		raw.Connection.MaxHelmStreamTimeout,
		"connection.max_helm_stream_timeout",
	); err != nil {
		return err
	}
	if raw.Connection.MaxConcurrentHelmStreams != nil {
		cfg.Connection.MaxConcurrentHelmStreams =
			*raw.Connection.MaxConcurrentHelmStreams
	}
	if raw.MetricsIngest.Address != "" {
		cfg.MetricsIngest.Address = raw.MetricsIngest.Address
	}
	if raw.MetricsIngest.AdvertisedURL != "" {
		cfg.MetricsIngest.AdvertisedURL = raw.MetricsIngest.AdvertisedURL
	}
	if raw.MetricsIngest.MaxBatchBytes != nil {
		cfg.MetricsIngest.MaxBatchBytes = *raw.MetricsIngest.MaxBatchBytes
	}
	if raw.MetricsIngest.MaxConcurrentBatches != nil {
		cfg.MetricsIngest.MaxConcurrentBatches = *raw.MetricsIngest.MaxConcurrentBatches
	}
	for _, item := range []struct {
		target *time.Duration
		value  string
		name   string
	}{
		{&cfg.MetricsIngest.SessionTimeout, raw.MetricsIngest.SessionTimeout, "metrics_ingest.session_timeout"},
		{&cfg.MetricsIngest.TokenRefreshInterval, raw.MetricsIngest.TokenRefreshInterval, "metrics_ingest.token_refresh_interval"},
		{&cfg.MetricsIngest.UnavailableRetryAfter, raw.MetricsIngest.UnavailableRetryAfter, "metrics_ingest.unavailable_retry_after"},
		{&cfg.MetricsIngest.ReadHeaderTimeout, raw.MetricsIngest.ReadHeaderTimeout, "metrics_ingest.read_header_timeout"},
		{&cfg.MetricsIngest.ReadTimeout, raw.MetricsIngest.ReadTimeout, "metrics_ingest.read_timeout"},
		{&cfg.MetricsIngest.WriteTimeout, raw.MetricsIngest.WriteTimeout, "metrics_ingest.write_timeout"},
		{&cfg.MetricsIngest.IdleTimeout, raw.MetricsIngest.IdleTimeout, "metrics_ingest.idle_timeout"},
		{&cfg.MetricsIngest.ShutdownTimeout, raw.MetricsIngest.ShutdownTimeout, "metrics_ingest.shutdown_timeout"},
	} {
		if err := applyAgentDuration(item.target, item.value, item.name); err != nil {
			return err
		}
	}
	if raw.LogLevel != "" {
		cfg.LogLevel = raw.LogLevel
	}

	return nil
}

// applyEnvironmentOverrides covers the values that differ per environment and
// therefore must not be written into a file that is checked in.
//
// There is one so far, and it exists for local development: an Agent started on
// a developer's machine has to tell the in-cluster collector an address that
// reaches the host, while the same file shipped in this repository has to keep
// describing what an Agent in a Pod does.
func applyEnvironmentOverrides(cfg *Config) {
	overrides := []struct {
		name   string
		target *string
	}{
		{"ZKE_METRICS_INGEST_ADVERTISED_URL", &cfg.MetricsIngest.AdvertisedURL},
	}
	for _, override := range overrides {
		if value, exists := os.LookupEnv(override.name); exists {
			*override.target = value
		}
	}
}

func findConfigPath(args []string) (string, error) {
	fs := flag.NewFlagSet("zke-agent", flag.ContinueOnError)
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

func (cfg Config) Validate() error {
	serverURL, err := url.Parse(cfg.Registration.ServerURL)
	if err != nil {
		return errors.New("registration Server URL must be a valid URL")
	}
	if serverURL.Host == "" {
		return errors.New("registration Server URL must include a host")
	}
	switch serverURL.Scheme {
	case "https":
	case "http":
		if cfg.Registration.CACertificateFile != "" {
			return errors.New(
				"registration CA certificate file cannot be used with HTTP",
			)
		}
	default:
		return errors.New("registration Server URL must use HTTP or HTTPS")
	}
	if serverURL.User != nil {
		return errors.New("registration Server URL must not contain credentials")
	}
	if (serverURL.Path != "" && serverURL.Path != "/") ||
		serverURL.RawQuery != "" ||
		serverURL.Fragment != "" {
		return errors.New("registration Server URL must not contain a path, query, or fragment")
	}
	if strings.TrimSpace(cfg.Registration.CACertificateFile) !=
		cfg.Registration.CACertificateFile {
		return errors.New(
			"registration CA certificate file path must not contain surrounding whitespace",
		)
	}
	if strings.TrimSpace(cfg.KubeconfigFile) != cfg.KubeconfigFile {
		return errors.New("kubeconfig file path must not contain surrounding whitespace")
	}
	if errors := k8svalidation.IsDNS1123Label(cfg.IdentityNamespace); len(errors) != 0 {
		return fmt.Errorf("identity namespace is invalid: %s", strings.Join(errors, "; "))
	}
	if errors := k8svalidation.IsDNS1123Subdomain(cfg.IdentitySecretName); len(errors) != 0 {
		return fmt.Errorf("identity Secret name is invalid: %s", strings.Join(errors, "; "))
	}
	if cfg.CertificateRenewBefore <= 0 ||
		cfg.CertificateRenewBefore > 365*24*time.Hour {
		return errors.New(
			"identity certificate renewal window must be greater than zero and not exceed 365 days",
		)
	}
	for _, item := range []struct {
		value time.Duration
		max   time.Duration
		name  string
	}{
		{cfg.Registration.Timeout, time.Minute, "registration timeout"},
		{cfg.Registration.RetryInitialInterval, time.Minute, "registration initial retry interval"},
		{cfg.Registration.RetryMaxInterval, 5 * time.Minute, "registration maximum retry interval"},
	} {
		if item.value <= 0 {
			return fmt.Errorf("%s must be greater than zero", item.name)
		}
		if item.value > item.max {
			return fmt.Errorf("%s must not exceed %s", item.name, item.max)
		}
	}
	if cfg.Registration.RetryInitialInterval >
		cfg.Registration.RetryMaxInterval {
		return errors.New(
			"registration initial retry interval must not exceed maximum retry interval",
		)
	}
	connectionHost, _, err := net.SplitHostPort(cfg.Connection.ServerAddress)
	if err != nil || strings.TrimSpace(connectionHost) == "" {
		return errors.New(
			"connection Server address must include a valid host and port",
		)
	}
	if strings.TrimSpace(cfg.Connection.CACertificateFile) !=
		cfg.Connection.CACertificateFile {
		return errors.New(
			"connection CA certificate file path must not contain surrounding whitespace",
		)
	}
	for _, item := range []struct {
		value time.Duration
		max   time.Duration
		name  string
	}{
		{cfg.Connection.ConnectTimeout, time.Minute, "connection timeout"},
		{cfg.Connection.RetryInitialInterval, time.Minute, "connection initial retry interval"},
		{cfg.Connection.RetryMaxInterval, 5 * time.Minute, "connection maximum retry interval"},
		{cfg.Connection.IdleTimeout, time.Hour, "connection idle timeout"},
		{cfg.Connection.KeepAliveInterval, 5 * time.Minute, "connection keep-alive interval"},
		{cfg.Connection.StreamHeaderTimeout, time.Minute, "business Stream header timeout"},
		{cfg.Connection.MaxResourceRequestTimeout, time.Hour, "Resource Stream request timeout"},
		{cfg.Connection.MaxPodLogsStreamTimeout, time.Hour, "Pod Logs Stream timeout"},
		{cfg.Connection.MaxPodExecStreamTimeout, time.Hour, "Pod Exec Stream timeout"},
		{cfg.Connection.MaxPodAccessStreamTimeout, time.Hour, "Pod Access Stream timeout"},
		{cfg.Connection.MaxResourceWatchStreamTimeout, time.Hour, "Resource Watch Stream timeout"},
		{cfg.Connection.MaxHelmStreamTimeout, time.Hour, "Helm Stream timeout"},
	} {
		if item.value <= 0 {
			return fmt.Errorf("%s must be greater than zero", item.name)
		}
		if item.value > item.max {
			return fmt.Errorf("%s must not exceed %s", item.name, item.max)
		}
	}
	if cfg.Connection.RetryInitialInterval > cfg.Connection.RetryMaxInterval {
		return errors.New(
			"connection initial retry interval must not exceed maximum retry interval",
		)
	}
	// A keep-alive at or above the idle timeout never refreshes the connection
	// before the transport gives up on it.
	if cfg.Connection.KeepAliveInterval >= cfg.Connection.IdleTimeout {
		return errors.New(
			"connection keep-alive interval must be shorter than the idle timeout",
		)
	}
	if cfg.Connection.MaxIncomingStreams < 1 ||
		cfg.Connection.MaxIncomingStreams > maxAgentIncomingStreams {
		return errors.New(
			"connection maximum incoming streams must be between 1 and 4096",
		)
	}
	if cfg.Connection.StreamHeaderTimeout >
		cfg.Connection.MaxResourceRequestTimeout {
		return errors.New(
			"business Stream header timeout must not exceed Resource Stream request timeout",
		)
	}
	if cfg.Connection.StreamHeaderTimeout >
		cfg.Connection.MaxResourceWatchStreamTimeout {
		return errors.New(
			"business Stream header timeout must not exceed Resource Watch Stream timeout",
		)
	}
	if cfg.Connection.StreamHeaderTimeout > cfg.Connection.MaxPodExecStreamTimeout {
		return errors.New(
			"business Stream header timeout must not exceed Pod Exec Stream timeout",
		)
	}
	if cfg.Connection.StreamHeaderTimeout > cfg.Connection.MaxPodAccessStreamTimeout {
		return errors.New(
			"business Stream header timeout must not exceed Pod Access Stream timeout",
		)
	}
	if cfg.Connection.StreamHeaderTimeout > cfg.Connection.MaxHelmStreamTimeout {
		return errors.New(
			"business Stream header timeout must not exceed Helm Stream timeout",
		)
	}
	if cfg.Connection.MaxConcurrentHelmStreams < 1 ||
		int64(cfg.Connection.MaxConcurrentHelmStreams) >
			cfg.Connection.MaxIncomingStreams {
		return errors.New(
			"maximum concurrent Helm Streams must be between 1 and maximum incoming streams",
		)
	}
	if cfg.Connection.MaxConcurrentResourceStreams < 1 ||
		int64(cfg.Connection.MaxConcurrentResourceStreams) >
			cfg.Connection.MaxIncomingStreams {
		return errors.New(
			"maximum concurrent Resource Streams must be between 1 and maximum incoming streams",
		)
	}
	if cfg.Connection.MaxResourceBodyBytes < 1 ||
		cfg.Connection.MaxResourceBodyBytes > maxResourceBodyBytes {
		return errors.New(
			"maximum Resource body bytes must be between 1 and 1073741824",
		)
	}
	if cfg.Connection.MaxConcurrentPodLogsStreams < 1 ||
		int64(cfg.Connection.MaxConcurrentResourceStreams)+
			int64(cfg.Connection.MaxConcurrentPodLogsStreams) >
			cfg.Connection.MaxIncomingStreams {
		return errors.New(
			"maximum concurrent Pod Logs Streams must be positive and fit within maximum incoming streams together with Resource Streams",
		)
	}
	if cfg.Connection.MaxPodLogBytes < 1 ||
		cfg.Connection.MaxPodLogBytes > maxResourceBodyBytes {
		return errors.New(
			"maximum Pod log bytes must be between 1 and 1073741824",
		)
	}
	if cfg.Connection.MaxConcurrentResourceWatchStreams < 1 ||
		int64(cfg.Connection.MaxConcurrentResourceStreams)+
			int64(cfg.Connection.MaxConcurrentPodLogsStreams)+
			int64(cfg.Connection.MaxConcurrentPodExecStreams)+
			int64(cfg.Connection.MaxConcurrentPodAccessStreams)+
			int64(cfg.Connection.MaxConcurrentResourceWatchStreams) >
			cfg.Connection.MaxIncomingStreams {
		return errors.New(
			"maximum concurrent Resource Watch Streams must be positive and fit within maximum incoming streams together with other business Streams",
		)
	}
	if cfg.Connection.MaxConcurrentPodExecStreams < 1 {
		return errors.New("maximum concurrent Pod Exec Streams must be positive")
	}
	if cfg.Connection.MaxPodExecInputBytes < 1 ||
		cfg.Connection.MaxPodExecInputBytes > maxResourceBodyBytes {
		return errors.New(
			"maximum Pod Exec input bytes must be between 1 and 1073741824",
		)
	}
	if cfg.Connection.MaxPodExecOutputBytes < 1 ||
		cfg.Connection.MaxPodExecOutputBytes > maxResourceBodyBytes {
		return errors.New(
			"maximum Pod Exec output bytes must be between 1 and 1073741824",
		)
	}
	if cfg.Connection.MaxConcurrentPodAccessStreams < 1 {
		return errors.New("maximum concurrent Pod Access Streams must be positive")
	}
	if cfg.Connection.MaxPodAccessClientBytes < 1 ||
		cfg.Connection.MaxPodAccessClientBytes > maxResourceBodyBytes {
		return errors.New(
			"maximum Pod Access client bytes must be between 1 and 1073741824",
		)
	}
	if cfg.Connection.MaxPodAccessPodBytes < 1 ||
		cfg.Connection.MaxPodAccessPodBytes > maxResourceBodyBytes {
		return errors.New(
			"maximum Pod Access Pod bytes must be between 1 and 1073741824",
		)
	}
	if err := cfg.MetricsIngest.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.LogLevel) == "" {
		return errors.New("log level is required")
	}
	return nil
}

func (config MetricsIngestConfig) validate() error {
	host, _, err := net.SplitHostPort(config.Address)
	if err != nil || strings.TrimSpace(host) != host {
		return errors.New(
			"metrics ingest address must include a valid host and port",
		)
	}
	if advertised := strings.TrimSpace(config.AdvertisedURL); advertised != "" {
		parsed, err := url.Parse(advertised)
		if err != nil || advertised != config.AdvertisedURL ||
			parsed.Host == "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
			(parsed.Path != "" && parsed.Path != "/") {
			return errors.New(
				"metrics ingest advertised URL must be an HTTP(S) origin without credentials, path, query, or fragment",
			)
		}
	}
	if config.MaxBatchBytes < 1 ||
		config.MaxBatchBytes > agentprotocol.MaxMetricsBatchBytesCeiling {
		return fmt.Errorf(
			"metrics ingest maximum batch bytes must be between 1 and %d",
			agentprotocol.MaxMetricsBatchBytesCeiling,
		)
	}
	if config.MaxConcurrentBatches < 1 || config.MaxConcurrentBatches > 64 {
		return errors.New(
			"metrics ingest maximum concurrent batches must be between 1 and 64",
		)
	}
	for _, item := range []struct {
		value time.Duration
		min   time.Duration
		max   time.Duration
		name  string
	}{
		{config.SessionTimeout, time.Minute, time.Hour, "metrics ingest session timeout"},
		{config.TokenRefreshInterval, 10 * time.Second, time.Hour, "metrics ingest token refresh interval"},
		{config.UnavailableRetryAfter, time.Second, 5 * time.Minute, "metrics ingest unavailable retry delay"},
		{config.ReadHeaderTimeout, time.Second, time.Minute, "metrics ingest read header timeout"},
		{config.ReadTimeout, time.Second, 5 * time.Minute, "metrics ingest read timeout"},
		{config.WriteTimeout, time.Second, 5 * time.Minute, "metrics ingest write timeout"},
		{config.IdleTimeout, time.Second, 10 * time.Minute, "metrics ingest idle timeout"},
		{config.ShutdownTimeout, time.Second, time.Minute, "metrics ingest shutdown timeout"},
	} {
		if item.value < item.min {
			return fmt.Errorf("%s must be at least %s", item.name, item.min)
		}
		if item.value > item.max {
			return fmt.Errorf("%s must not exceed %s", item.name, item.max)
		}
	}
	// A batch may take the whole read timeout to arrive and then still has to
	// reach the Server. Sizing the write timeout below the read timeout would
	// cut off responses to requests the endpoint itself allowed.
	if config.WriteTimeout < config.ReadTimeout {
		return errors.New(
			"metrics ingest write timeout must not be shorter than the read timeout",
		)
	}
	return nil
}

func applyAgentDuration(target *time.Duration, value, name string) error {
	if value == "" {
		return nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s must be a valid duration: %w", name, err)
	}
	*target = duration
	return nil
}

func (cfg Config) ConnectionServerAddress() string {
	return cfg.Connection.ServerAddress
}

func (cfg Config) ConnectionServerName() string {
	host, _, err := net.SplitHostPort(cfg.Connection.ServerAddress)
	if err != nil {
		return ""
	}
	return host
}
