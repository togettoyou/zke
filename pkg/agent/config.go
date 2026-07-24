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
		ServerAddress        string `yaml:"server_address"`
		CACertificateFile    string `yaml:"ca_certificate_file"`
		ConnectTimeout       string `yaml:"connect_timeout"`
		RetryInitialInterval string `yaml:"retry_initial_interval"`
		RetryMaxInterval     string `yaml:"retry_max_interval"`
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
			ConnectTimeout:       10 * time.Second,
			RetryInitialInterval: time.Second,
			RetryMaxInterval:     30 * time.Second,
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
	if err := decoder.Decode(&extra); err != io.EOF {
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
