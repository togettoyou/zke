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
	defaultEnrollmentTokenFile = "/var/run/secrets/zke-enrollment/token"
	defaultIdentityNamespace   = "zke-system"
	defaultIdentitySecretName  = "zke-agent-identity"
	defaultLogLevel            = "info"
)

type Config struct {
	ServerAddress         string
	AllowInsecureLoopback bool
	ServerCAFile          string
	KubeconfigFile        string
	EnrollmentTokenFile   string
	IdentityNamespace     string
	IdentitySecretName    string
	RegistrationTimeout   time.Duration
	RetryInitialInterval  time.Duration
	RetryMaxInterval      time.Duration
	LogLevel              string
}

type fileConfig struct {
	ServerAddress         string `yaml:"server_address"`
	AllowInsecureLoopback *bool  `yaml:"allow_insecure_loopback"`
	ServerCAFile          string `yaml:"server_ca_file"`
	KubeconfigFile        string `yaml:"kubeconfig_file"`
	EnrollmentTokenFile   string `yaml:"enrollment_token_file"`
	Identity              struct {
		Namespace  string `yaml:"namespace"`
		SecretName string `yaml:"secret_name"`
	} `yaml:"identity"`
	Registration struct {
		Timeout              string `yaml:"timeout"`
		RetryInitialInterval string `yaml:"retry_initial_interval"`
		RetryMaxInterval     string `yaml:"retry_max_interval"`
	} `yaml:"registration"`
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
		EnrollmentTokenFile:  defaultEnrollmentTokenFile,
		IdentityNamespace:    defaultIdentityNamespace,
		IdentitySecretName:   defaultIdentitySecretName,
		RegistrationTimeout:  10 * time.Second,
		RetryInitialInterval: time.Second,
		RetryMaxInterval:     15 * time.Second,
		LogLevel:             defaultLogLevel,
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

	if raw.ServerAddress != "" {
		cfg.ServerAddress = raw.ServerAddress
	}
	if raw.AllowInsecureLoopback != nil {
		cfg.AllowInsecureLoopback = *raw.AllowInsecureLoopback
	}
	if raw.ServerCAFile != "" {
		cfg.ServerCAFile = raw.ServerCAFile
	}
	if raw.KubeconfigFile != "" {
		cfg.KubeconfigFile = raw.KubeconfigFile
	}
	if raw.EnrollmentTokenFile != "" {
		cfg.EnrollmentTokenFile = raw.EnrollmentTokenFile
	}
	if raw.Identity.Namespace != "" {
		cfg.IdentityNamespace = raw.Identity.Namespace
	}
	if raw.Identity.SecretName != "" {
		cfg.IdentitySecretName = raw.Identity.SecretName
	}
	if err := applyAgentDuration(
		&cfg.RegistrationTimeout,
		raw.Registration.Timeout,
		"registration.timeout",
	); err != nil {
		return err
	}
	if err := applyAgentDuration(
		&cfg.RetryInitialInterval,
		raw.Registration.RetryInitialInterval,
		"registration.retry_initial_interval",
	); err != nil {
		return err
	}
	if err := applyAgentDuration(
		&cfg.RetryMaxInterval,
		raw.Registration.RetryMaxInterval,
		"registration.retry_max_interval",
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
	serverURL, err := url.Parse(cfg.ServerAddress)
	if err != nil {
		return errors.New("server address must be a valid URL")
	}
	if serverURL.Host == "" {
		return errors.New("server address must include a host")
	}
	switch serverURL.Scheme {
	case "https":
		if cfg.AllowInsecureLoopback {
			return errors.New("insecure loopback mode requires an HTTP Server address")
		}
	case "http":
		host := serverURL.Hostname()
		ip := net.ParseIP(host)
		if !cfg.AllowInsecureLoopback ||
			(!strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback())) {
			return errors.New(
				"HTTP Server address is only allowed with insecure loopback mode on a loopback host",
			)
		}
		if cfg.ServerCAFile != "" {
			return errors.New("Server CA file cannot be used with an HTTP Server address")
		}
	default:
		return errors.New("server address must use HTTPS")
	}
	if serverURL.User != nil {
		return errors.New("server address must not contain credentials")
	}
	if (serverURL.Path != "" && serverURL.Path != "/") ||
		serverURL.RawQuery != "" ||
		serverURL.Fragment != "" {
		return errors.New("server address must not contain a path, query, or fragment")
	}
	if strings.TrimSpace(cfg.ServerCAFile) != cfg.ServerCAFile {
		return errors.New("server CA file path must not contain surrounding whitespace")
	}
	if strings.TrimSpace(cfg.KubeconfigFile) != cfg.KubeconfigFile {
		return errors.New("kubeconfig file path must not contain surrounding whitespace")
	}
	if strings.TrimSpace(cfg.EnrollmentTokenFile) == "" ||
		strings.TrimSpace(cfg.EnrollmentTokenFile) != cfg.EnrollmentTokenFile {
		return errors.New(
			"enrollment token file is required and must not contain surrounding whitespace",
		)
	}
	if errors := k8svalidation.IsDNS1123Label(cfg.IdentityNamespace); len(errors) != 0 {
		return fmt.Errorf("identity namespace is invalid: %s", strings.Join(errors, "; "))
	}
	if errors := k8svalidation.IsDNS1123Subdomain(cfg.IdentitySecretName); len(errors) != 0 {
		return fmt.Errorf("identity Secret name is invalid: %s", strings.Join(errors, "; "))
	}
	for _, item := range []struct {
		value time.Duration
		max   time.Duration
		name  string
	}{
		{cfg.RegistrationTimeout, time.Minute, "registration timeout"},
		{cfg.RetryInitialInterval, time.Minute, "registration initial retry interval"},
		{cfg.RetryMaxInterval, 5 * time.Minute, "registration maximum retry interval"},
	} {
		if item.value <= 0 {
			return fmt.Errorf("%s must be greater than zero", item.name)
		}
		if item.value > item.max {
			return fmt.Errorf("%s must not exceed %s", item.name, item.max)
		}
	}
	if cfg.RetryInitialInterval > cfg.RetryMaxInterval {
		return errors.New(
			"registration initial retry interval must not exceed maximum retry interval",
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

func (cfg Config) ServerHost() string {
	serverURL, err := url.Parse(cfg.ServerAddress)
	if err != nil {
		return ""
	}
	return serverURL.Host
}
