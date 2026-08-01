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
	LogLevel               string
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
	MaxResourceWatchStreamTimeout     time.Duration
	MaxConcurrentResourceWatchStreams int
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
		MaxResourceWatchStreamTimeout     string  `yaml:"max_resource_watch_stream_timeout"`
		MaxConcurrentResourceWatchStreams *int    `yaml:"max_concurrent_resource_watch_streams"`
	} `yaml:"connection"`
	LogLevel string `yaml:"log_level"`
}

func LoadConfig(args []string) (Config, error) {
	configPath, err := findConfigPath(args)
	if err != nil {
		return Config{}, err
	}
	if configPath == "" {
		return Config{}, errors.New("--config is required")
	}

	cfg := Config{
		IdentityNamespace:      defaultIdentityNamespace,
		IdentitySecretName:     defaultIdentitySecretName,
		CertificateRenewBefore: 7 * 24 * time.Hour,
		Registration: RegistrationConfig{
			Timeout:              10 * time.Second,
			RetryInitialInterval: time.Second,
			RetryMaxInterval:     15 * time.Second,
		},
		Connection: ConnectionConfig{
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
			MaxResourceWatchStreamTimeout:     30 * time.Minute,
			MaxConcurrentResourceWatchStreams: 16,
		},
		LogLevel: defaultLogLevel,
	}
	if err := applyFile(&cfg, configPath); err != nil {
		return Config{}, err
	}
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
	if err := decoder.Decode(&raw); err != nil {
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
	if raw.LogLevel != "" {
		cfg.LogLevel = raw.LogLevel
	}

	return nil
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
		if !isLoopbackHost(serverURL.Hostname()) {
			return errors.New(
				"HTTP registration Server URL is only allowed for a loopback host",
			)
		}
	default:
		return errors.New("registration Server URL must use HTTPS")
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
		{cfg.Connection.MaxResourceWatchStreamTimeout, time.Hour, "Resource Watch Stream timeout"},
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
	if strings.TrimSpace(cfg.LogLevel) == "" {
		return errors.New("log level is required")
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

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
