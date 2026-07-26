package server

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
	maxHTTPTimeout                = 5 * time.Minute
	maxIdleTimeout                = 10 * time.Minute
	maxDatabaseTimeout            = time.Minute
	maxMigrationTimeout           = 10 * time.Minute
	maxShutdownTimeout            = 2 * time.Minute
	maxSessionIdle                = 24 * time.Hour
	maxSessionAbsolute            = 30 * 24 * time.Hour
	maxLoginRateWindow            = 24 * time.Hour
	maxAuthOperation              = time.Minute
	maxPasswordChecks             = 64
	maxAgentCertificateTTL        = 365 * 24 * time.Hour
	maxAgentEnrollmentRateWindow  = 24 * time.Hour
	maxAgentEnrollmentAttempts    = 10_000
	maxAgentHandshakeTimeout      = time.Minute
	maxAgentHeartbeatInterval     = 5 * time.Minute
	maxAgentHeartbeatTimeout      = 15 * time.Minute
	maxAgentLastSeenWriteInterval = 15 * time.Minute
	maxAgentPKIValidity           = 30 * 365 * 24 * time.Hour
)

type Config struct {
	HTTP               HTTPConfig
	Database           DatabaseConfig
	Auth               AuthConfig
	AgentPKI           AgentPKIConfig
	AgentIdentity      AgentIdentityConfig
	AgentEnrollment    AgentEnrollmentConfig
	AgentInstall       AgentInstallConfig
	AgentListener      AgentListenerConfig
	CertificateMonitor CertificateMonitorConfig
	ShutdownTimeout    time.Duration
	LogLevel           string
}

type HTTPConfig struct {
	Address           string
	TLS               TLSIdentityConfig
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

type TLSIdentityConfig struct {
	CertificateFile string
	PrivateKeyFile  string
}

type DatabaseConfig struct {
	URL              string
	ConnectTimeout   time.Duration
	MigrationTimeout time.Duration
}

type AuthConfig struct {
	SessionIdleTimeout          time.Duration
	SessionAbsoluteTimeout      time.Duration
	OperationTimeout            time.Duration
	MaxConcurrentPasswordChecks int
	CookieSecure                bool
	LoginRateLimit              LoginRateLimitConfig
	AccountLockout              AccountLockoutConfig
	InitialAdmin                InitialAdminConfig
}

type LoginRateLimitConfig struct {
	Window                time.Duration
	MaxAttemptsPerAccount int
	MaxAttemptsPerSource  int
}

type AccountLockoutConfig struct {
	MaxFailedAttempts int
	Duration          time.Duration
}

type InitialAdminConfig struct {
	Enabled              bool
	Username             string
	DisplayName          string
	PasswordFile         string
	AutoGeneratePassword bool
}

type AgentIdentityConfig struct {
	CACertificateFile string
	CAPrivateKeyFile  string
	CertificateTTL    time.Duration
}

type AgentPKIConfig struct {
	Mode                               string
	Directory                          string
	AutoGenerate                       bool
	AgentClientCAValidity              time.Duration
	AgentListenerCAValidity            time.Duration
	AgentListenerValidity              time.Duration
	AgentListenerRenewBefore           time.Duration
	AgentListenerDNSNames              []string
	AgentListenerIPAddresses           []string
	ExternalAgentClientCACertificate   string
	ExternalAgentClientCAPrivateKey    string
	ExternalAgentListenerCACertificate string
	ExternalAgentListenerCertificate   string
	ExternalAgentListenerPrivateKey    string
}

type AgentEnrollmentConfig struct {
	OperationTimeout time.Duration
	RateLimit        AgentEnrollmentRateLimitConfig
}

type AgentInstallConfig struct {
	Enabled                       bool
	PublicHTTPURL                 string
	PublicQUICAddress             string
	Image                         string
	Namespace                     string
	ImagePullPolicy               string
	RegistrationCACertificateFile string
}

type CertificateMonitorConfig struct {
	WarningBefore time.Duration
	CheckInterval time.Duration
}

type AgentEnrollmentRateLimitConfig struct {
	Window               time.Duration
	MaxAttemptsPerSource int
}

type AgentListenerConfig struct {
	Address               string
	TLS                   TLSIdentityConfig
	HandshakeTimeout      time.Duration
	HeartbeatInterval     time.Duration
	HeartbeatTimeout      time.Duration
	LastSeenWriteInterval time.Duration
	OperationTimeout      time.Duration
}

type fileConfig struct {
	HTTP struct {
		Address string `yaml:"address"`
		TLS     struct {
			CertificateFile string `yaml:"certificate_file"`
			PrivateKeyFile  string `yaml:"private_key_file"`
		} `yaml:"tls"`
		ReadHeaderTimeout string `yaml:"read_header_timeout"`
		ReadTimeout       string `yaml:"read_timeout"`
		WriteTimeout      string `yaml:"write_timeout"`
		IdleTimeout       string `yaml:"idle_timeout"`
	} `yaml:"http"`
	Database struct {
		URL              string `yaml:"url"`
		ConnectTimeout   string `yaml:"connect_timeout"`
		MigrationTimeout string `yaml:"migration_timeout"`
	} `yaml:"database"`
	Auth struct {
		SessionIdleTimeout          string `yaml:"session_idle_timeout"`
		SessionAbsoluteTimeout      string `yaml:"session_absolute_timeout"`
		OperationTimeout            string `yaml:"operation_timeout"`
		MaxConcurrentPasswordChecks *int   `yaml:"max_concurrent_password_checks"`
		CookieSecure                *bool  `yaml:"cookie_secure"`
		LoginRateLimit              struct {
			Window                string `yaml:"window"`
			MaxAttemptsPerAccount *int   `yaml:"max_attempts_per_account"`
			MaxAttemptsPerSource  *int   `yaml:"max_attempts_per_source"`
		} `yaml:"login_rate_limit"`
		AccountLockout struct {
			MaxFailedAttempts *int   `yaml:"max_failed_attempts"`
			Duration          string `yaml:"duration"`
		} `yaml:"account_lockout"`
		InitialAdmin struct {
			Enabled              *bool  `yaml:"enabled"`
			Username             string `yaml:"username"`
			DisplayName          string `yaml:"display_name"`
			PasswordFile         string `yaml:"password_file"`
			AutoGeneratePassword *bool  `yaml:"auto_generate_password"`
		} `yaml:"initial_admin"`
	} `yaml:"auth"`
	AgentPKI struct {
		Mode                           string `yaml:"mode"`
		AgentClientCertificateValidity string `yaml:"agent_client_certificate_validity"`
		Managed                        struct {
			Directory                string `yaml:"directory"`
			AutoGenerate             *bool  `yaml:"auto_generate"`
			AgentClientCAValidity    string `yaml:"agent_client_ca_validity"`
			AgentListenerCAValidity  string `yaml:"agent_listener_ca_validity"`
			AgentListenerValidity    string `yaml:"agent_listener_certificate_validity"`
			AgentListenerRenewBefore string `yaml:"agent_listener_renew_before"`
			ListenerSANs             struct {
				DNSNames    []string `yaml:"dns_names"`
				IPAddresses []string `yaml:"ip_addresses"`
			} `yaml:"listener_sans"`
		} `yaml:"managed"`
		External struct {
			AgentClientCA struct {
				CertificateFile string `yaml:"certificate_file"`
				PrivateKeyFile  string `yaml:"private_key_file"`
			} `yaml:"agent_client_ca"`
			AgentListenerCA struct {
				CertificateFile string `yaml:"certificate_file"`
			} `yaml:"agent_listener_ca"`
			AgentListener struct {
				CertificateFile string `yaml:"certificate_file"`
				PrivateKeyFile  string `yaml:"private_key_file"`
			} `yaml:"agent_listener"`
		} `yaml:"external"`
	} `yaml:"agent_pki"`
	AgentEnrollment struct {
		OperationTimeout string `yaml:"operation_timeout"`
		RateLimit        struct {
			Window               string `yaml:"window"`
			MaxAttemptsPerSource *int   `yaml:"max_attempts_per_source"`
		} `yaml:"rate_limit"`
	} `yaml:"agent_enrollment"`
	AgentInstall struct {
		Enabled                       *bool  `yaml:"enabled"`
		PublicHTTPURL                 string `yaml:"public_http_url"`
		PublicQUICAddress             string `yaml:"public_quic_address"`
		Image                         string `yaml:"image"`
		Namespace                     string `yaml:"namespace"`
		ImagePullPolicy               string `yaml:"image_pull_policy"`
		RegistrationCACertificateFile string `yaml:"registration_ca_certificate_file"`
	} `yaml:"agent_install"`
	AgentListener struct {
		Address               string `yaml:"address"`
		HandshakeTimeout      string `yaml:"handshake_timeout"`
		HeartbeatInterval     string `yaml:"heartbeat_interval"`
		HeartbeatTimeout      string `yaml:"heartbeat_timeout"`
		LastSeenWriteInterval string `yaml:"last_seen_write_interval"`
		OperationTimeout      string `yaml:"operation_timeout"`
	} `yaml:"agent_listener"`
	CertificateMonitor struct {
		WarningBefore string `yaml:"warning_before"`
		CheckInterval string `yaml:"check_interval"`
	} `yaml:"certificate_monitor"`
	ShutdownTimeout string `yaml:"shutdown_timeout"`
	LogLevel        string `yaml:"log_level"`
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
			Mode:                     "external",
			AutoGenerate:             true,
			AgentClientCAValidity:    10 * 365 * 24 * time.Hour,
			AgentListenerCAValidity:  20 * 365 * 24 * time.Hour,
			AgentListenerValidity:    10 * 365 * 24 * time.Hour,
			AgentListenerRenewBefore: 365 * 24 * time.Hour,
		},
		AgentIdentity: AgentIdentityConfig{
			CertificateTTL: 30 * 24 * time.Hour,
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
			HandshakeTimeout:      10 * time.Second,
			HeartbeatInterval:     10 * time.Second,
			HeartbeatTimeout:      30 * time.Second,
			LastSeenWriteInterval: time.Minute,
			OperationTimeout:      10 * time.Second,
		},
		CertificateMonitor: CertificateMonitorConfig{
			WarningBefore: 30 * 24 * time.Hour,
			CheckInterval: time.Hour,
		},
	}
	if err := applyFile(&cfg, configPath); err != nil {
		return Config{}, err
	}
	cfg.resolveAgentPKIFilePaths()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (cfg *Config) resolveAgentPKIFilePaths() {
	if cfg.AgentPKI.Mode != "external" {
		return
	}
	cfg.AgentIdentity.CACertificateFile =
		cfg.AgentPKI.ExternalAgentClientCACertificate
	cfg.AgentIdentity.CAPrivateKeyFile =
		cfg.AgentPKI.ExternalAgentClientCAPrivateKey
	cfg.AgentListener.TLS.CertificateFile =
		cfg.AgentPKI.ExternalAgentListenerCertificate
	cfg.AgentListener.TLS.PrivateKeyFile =
		cfg.AgentPKI.ExternalAgentListenerPrivateKey
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
	if err := ensureYAMLEOF(decoder); err != nil {
		return fmt.Errorf("decode config file %q: %w", path, err)
	}

	if raw.HTTP.Address != "" {
		cfg.HTTP.Address = raw.HTTP.Address
	}
	if raw.HTTP.TLS.CertificateFile != "" {
		cfg.HTTP.TLS.CertificateFile = raw.HTTP.TLS.CertificateFile
	}
	if raw.HTTP.TLS.PrivateKeyFile != "" {
		cfg.HTTP.TLS.PrivateKeyFile = raw.HTTP.TLS.PrivateKeyFile
	}
	if err := applyDuration(&cfg.HTTP.ReadHeaderTimeout, raw.HTTP.ReadHeaderTimeout, "http.read_header_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.HTTP.ReadTimeout, raw.HTTP.ReadTimeout, "http.read_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.HTTP.WriteTimeout, raw.HTTP.WriteTimeout, "http.write_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.HTTP.IdleTimeout, raw.HTTP.IdleTimeout, "http.idle_timeout"); err != nil {
		return err
	}
	if raw.Database.URL != "" {
		cfg.Database.URL = raw.Database.URL
	}
	if err := applyDuration(&cfg.Database.ConnectTimeout, raw.Database.ConnectTimeout, "database.connect_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.Database.MigrationTimeout, raw.Database.MigrationTimeout, "database.migration_timeout"); err != nil {
		return err
	}
	if err := applyDuration(
		&cfg.Auth.SessionIdleTimeout,
		raw.Auth.SessionIdleTimeout,
		"auth.session_idle_timeout",
	); err != nil {
		return err
	}
	if err := applyDuration(
		&cfg.Auth.SessionAbsoluteTimeout,
		raw.Auth.SessionAbsoluteTimeout,
		"auth.session_absolute_timeout",
	); err != nil {
		return err
	}
	if err := applyDuration(
		&cfg.Auth.OperationTimeout,
		raw.Auth.OperationTimeout,
		"auth.operation_timeout",
	); err != nil {
		return err
	}
	if raw.Auth.MaxConcurrentPasswordChecks != nil {
		cfg.Auth.MaxConcurrentPasswordChecks = *raw.Auth.MaxConcurrentPasswordChecks
	}
	if raw.Auth.CookieSecure != nil {
		cfg.Auth.CookieSecure = *raw.Auth.CookieSecure
	}
	if err := applyDuration(
		&cfg.Auth.LoginRateLimit.Window,
		raw.Auth.LoginRateLimit.Window,
		"auth.login_rate_limit.window",
	); err != nil {
		return err
	}
	if raw.Auth.LoginRateLimit.MaxAttemptsPerAccount != nil {
		cfg.Auth.LoginRateLimit.MaxAttemptsPerAccount =
			*raw.Auth.LoginRateLimit.MaxAttemptsPerAccount
	}
	if raw.Auth.LoginRateLimit.MaxAttemptsPerSource != nil {
		cfg.Auth.LoginRateLimit.MaxAttemptsPerSource =
			*raw.Auth.LoginRateLimit.MaxAttemptsPerSource
	}
	if raw.Auth.AccountLockout.MaxFailedAttempts != nil {
		cfg.Auth.AccountLockout.MaxFailedAttempts =
			*raw.Auth.AccountLockout.MaxFailedAttempts
	}
	if err := applyDuration(
		&cfg.Auth.AccountLockout.Duration,
		raw.Auth.AccountLockout.Duration,
		"auth.account_lockout.duration",
	); err != nil {
		return err
	}
	if raw.Auth.InitialAdmin.Enabled != nil {
		cfg.Auth.InitialAdmin.Enabled = *raw.Auth.InitialAdmin.Enabled
	}
	if raw.Auth.InitialAdmin.Username != "" {
		cfg.Auth.InitialAdmin.Username = raw.Auth.InitialAdmin.Username
	}
	if raw.Auth.InitialAdmin.DisplayName != "" {
		cfg.Auth.InitialAdmin.DisplayName = raw.Auth.InitialAdmin.DisplayName
	}
	if raw.Auth.InitialAdmin.PasswordFile != "" {
		cfg.Auth.InitialAdmin.PasswordFile = raw.Auth.InitialAdmin.PasswordFile
	}
	if raw.Auth.InitialAdmin.AutoGeneratePassword != nil {
		cfg.Auth.InitialAdmin.AutoGeneratePassword =
			*raw.Auth.InitialAdmin.AutoGeneratePassword
	}
	if raw.AgentPKI.Mode != "" {
		cfg.AgentPKI.Mode = raw.AgentPKI.Mode
	}
	if raw.AgentPKI.Managed.Directory != "" {
		cfg.AgentPKI.Directory = raw.AgentPKI.Managed.Directory
	}
	if raw.AgentPKI.Managed.AutoGenerate != nil {
		cfg.AgentPKI.AutoGenerate = *raw.AgentPKI.Managed.AutoGenerate
	}
	for _, item := range []struct {
		target *time.Duration
		value  string
		name   string
	}{
		{&cfg.AgentIdentity.CertificateTTL, raw.AgentPKI.AgentClientCertificateValidity, "agent_pki.agent_client_certificate_validity"},
		{&cfg.AgentPKI.AgentClientCAValidity, raw.AgentPKI.Managed.AgentClientCAValidity, "agent_pki.managed.agent_client_ca_validity"},
		{&cfg.AgentPKI.AgentListenerCAValidity, raw.AgentPKI.Managed.AgentListenerCAValidity, "agent_pki.managed.agent_listener_ca_validity"},
		{&cfg.AgentPKI.AgentListenerValidity, raw.AgentPKI.Managed.AgentListenerValidity, "agent_pki.managed.agent_listener_certificate_validity"},
		{&cfg.AgentPKI.AgentListenerRenewBefore, raw.AgentPKI.Managed.AgentListenerRenewBefore, "agent_pki.managed.agent_listener_renew_before"},
	} {
		if err := applyDuration(item.target, item.value, item.name); err != nil {
			return err
		}
	}
	if raw.AgentPKI.Managed.ListenerSANs.DNSNames != nil {
		cfg.AgentPKI.AgentListenerDNSNames =
			append([]string(nil), raw.AgentPKI.Managed.ListenerSANs.DNSNames...)
	}
	if raw.AgentPKI.Managed.ListenerSANs.IPAddresses != nil {
		cfg.AgentPKI.AgentListenerIPAddresses =
			append([]string(nil), raw.AgentPKI.Managed.ListenerSANs.IPAddresses...)
	}
	if raw.AgentPKI.External.AgentClientCA.CertificateFile != "" {
		cfg.AgentPKI.ExternalAgentClientCACertificate =
			raw.AgentPKI.External.AgentClientCA.CertificateFile
	}
	if raw.AgentPKI.External.AgentClientCA.PrivateKeyFile != "" {
		cfg.AgentPKI.ExternalAgentClientCAPrivateKey =
			raw.AgentPKI.External.AgentClientCA.PrivateKeyFile
	}
	if raw.AgentPKI.External.AgentListenerCA.CertificateFile != "" {
		cfg.AgentPKI.ExternalAgentListenerCACertificate =
			raw.AgentPKI.External.AgentListenerCA.CertificateFile
	}
	if raw.AgentPKI.External.AgentListener.CertificateFile != "" {
		cfg.AgentPKI.ExternalAgentListenerCertificate =
			raw.AgentPKI.External.AgentListener.CertificateFile
	}
	if raw.AgentPKI.External.AgentListener.PrivateKeyFile != "" {
		cfg.AgentPKI.ExternalAgentListenerPrivateKey =
			raw.AgentPKI.External.AgentListener.PrivateKeyFile
	}
	if err := applyDuration(
		&cfg.AgentEnrollment.OperationTimeout,
		raw.AgentEnrollment.OperationTimeout,
		"agent_enrollment.operation_timeout",
	); err != nil {
		return err
	}
	if err := applyDuration(
		&cfg.AgentEnrollment.RateLimit.Window,
		raw.AgentEnrollment.RateLimit.Window,
		"agent_enrollment.rate_limit.window",
	); err != nil {
		return err
	}
	if raw.AgentEnrollment.RateLimit.MaxAttemptsPerSource != nil {
		cfg.AgentEnrollment.RateLimit.MaxAttemptsPerSource =
			*raw.AgentEnrollment.RateLimit.MaxAttemptsPerSource
	}
	if raw.AgentInstall.Enabled != nil {
		cfg.AgentInstall.Enabled = *raw.AgentInstall.Enabled
	}
	if raw.AgentInstall.PublicHTTPURL != "" {
		cfg.AgentInstall.PublicHTTPURL = raw.AgentInstall.PublicHTTPURL
	}
	if raw.AgentInstall.PublicQUICAddress != "" {
		cfg.AgentInstall.PublicQUICAddress = raw.AgentInstall.PublicQUICAddress
	}
	if raw.AgentInstall.Image != "" {
		cfg.AgentInstall.Image = raw.AgentInstall.Image
	}
	if raw.AgentInstall.Namespace != "" {
		cfg.AgentInstall.Namespace = raw.AgentInstall.Namespace
	}
	if raw.AgentInstall.ImagePullPolicy != "" {
		cfg.AgentInstall.ImagePullPolicy = raw.AgentInstall.ImagePullPolicy
	}
	if raw.AgentInstall.RegistrationCACertificateFile != "" {
		cfg.AgentInstall.RegistrationCACertificateFile =
			raw.AgentInstall.RegistrationCACertificateFile
	}
	if raw.AgentListener.Address != "" {
		cfg.AgentListener.Address = raw.AgentListener.Address
	}
	if err := applyDuration(
		&cfg.AgentListener.HandshakeTimeout,
		raw.AgentListener.HandshakeTimeout,
		"agent_listener.handshake_timeout",
	); err != nil {
		return err
	}
	if err := applyDuration(
		&cfg.CertificateMonitor.WarningBefore,
		raw.CertificateMonitor.WarningBefore,
		"certificate_monitor.warning_before",
	); err != nil {
		return err
	}
	if err := applyDuration(
		&cfg.CertificateMonitor.CheckInterval,
		raw.CertificateMonitor.CheckInterval,
		"certificate_monitor.check_interval",
	); err != nil {
		return err
	}
	if err := applyDuration(
		&cfg.AgentListener.HeartbeatInterval,
		raw.AgentListener.HeartbeatInterval,
		"agent_listener.heartbeat_interval",
	); err != nil {
		return err
	}
	if err := applyDuration(
		&cfg.AgentListener.HeartbeatTimeout,
		raw.AgentListener.HeartbeatTimeout,
		"agent_listener.heartbeat_timeout",
	); err != nil {
		return err
	}
	if err := applyDuration(
		&cfg.AgentListener.LastSeenWriteInterval,
		raw.AgentListener.LastSeenWriteInterval,
		"agent_listener.last_seen_write_interval",
	); err != nil {
		return err
	}
	if err := applyDuration(
		&cfg.AgentListener.OperationTimeout,
		raw.AgentListener.OperationTimeout,
		"agent_listener.operation_timeout",
	); err != nil {
		return err
	}
	if err := applyDuration(&cfg.ShutdownTimeout, raw.ShutdownTimeout, "shutdown_timeout"); err != nil {
		return err
	}
	if raw.LogLevel != "" {
		cfg.LogLevel = raw.LogLevel
	}

	return nil
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

func (cfg Config) Validate() error {
	if strings.TrimSpace(cfg.HTTP.Address) == "" {
		return errors.New("http address is required")
	}
	if strings.TrimSpace(cfg.Database.URL) == "" {
		return errors.New("PostgreSQL URL is required")
	}
	if strings.TrimSpace(cfg.LogLevel) == "" {
		return errors.New("log level is required")
	}
	httpTLSCertificateConfigured :=
		strings.TrimSpace(cfg.HTTP.TLS.CertificateFile) != ""
	httpTLSPrivateKeyConfigured :=
		strings.TrimSpace(cfg.HTTP.TLS.PrivateKeyFile) != ""
	if httpTLSCertificateConfigured != httpTLSPrivateKeyConfigured {
		return errors.New(
			"HTTP TLS certificate and private key files must be configured together",
		)
	}
	if httpTLSCertificateConfigured && !cfg.Auth.CookieSecure {
		return errors.New("HTTP TLS requires secure session cookies")
	}
	for _, item := range []struct {
		value string
		name  string
	}{
		{cfg.HTTP.TLS.CertificateFile, "HTTP TLS certificate file"},
		{cfg.HTTP.TLS.PrivateKeyFile, "HTTP TLS private key file"},
	} {
		if strings.TrimSpace(item.value) != item.value {
			return fmt.Errorf("%s must not contain surrounding whitespace", item.name)
		}
	}
	for _, item := range []struct {
		value time.Duration
		max   time.Duration
		name  string
	}{
		{cfg.HTTP.ReadHeaderTimeout, maxHTTPTimeout, "http read header timeout"},
		{cfg.HTTP.ReadTimeout, maxHTTPTimeout, "http read timeout"},
		{cfg.HTTP.WriteTimeout, maxHTTPTimeout, "http write timeout"},
		{cfg.HTTP.IdleTimeout, maxIdleTimeout, "http idle timeout"},
		{cfg.Database.ConnectTimeout, maxDatabaseTimeout, "database connect timeout"},
		{cfg.Database.MigrationTimeout, maxMigrationTimeout, "database migration timeout"},
		{cfg.Auth.SessionIdleTimeout, maxSessionIdle, "session idle timeout"},
		{cfg.Auth.SessionAbsoluteTimeout, maxSessionAbsolute, "session absolute timeout"},
		{cfg.Auth.OperationTimeout, maxAuthOperation, "authentication operation timeout"},
		{cfg.Auth.LoginRateLimit.Window, maxLoginRateWindow, "login rate limit window"},
		{cfg.Auth.AccountLockout.Duration, maxLoginRateWindow, "account lock duration"},
		{
			cfg.AgentIdentity.CertificateTTL,
			maxAgentCertificateTTL,
			"Agent certificate TTL",
		},
		{
			cfg.AgentEnrollment.OperationTimeout,
			maxAuthOperation,
			"Agent enrollment operation timeout",
		},
		{
			cfg.AgentEnrollment.RateLimit.Window,
			maxAgentEnrollmentRateWindow,
			"Agent enrollment rate limit window",
		},
		{cfg.ShutdownTimeout, maxShutdownTimeout, "shutdown timeout"},
	} {
		if item.value <= 0 {
			return fmt.Errorf("%s must be greater than zero", item.name)
		}
		if item.value > item.max {
			return fmt.Errorf("%s must not exceed %s", item.name, item.max)
		}
	}
	if cfg.Auth.SessionIdleTimeout > cfg.Auth.SessionAbsoluteTimeout {
		return errors.New("session idle timeout must not exceed session absolute timeout")
	}
	if cfg.Auth.OperationTimeout >= cfg.HTTP.WriteTimeout {
		return errors.New("authentication operation timeout must be below HTTP write timeout")
	}
	if cfg.AgentEnrollment.OperationTimeout >= cfg.HTTP.WriteTimeout {
		return errors.New("Agent enrollment operation timeout must be below HTTP write timeout")
	}
	if cfg.Auth.MaxConcurrentPasswordChecks <= 0 ||
		cfg.Auth.MaxConcurrentPasswordChecks > maxPasswordChecks {
		return fmt.Errorf(
			"maximum concurrent password checks must be between 1 and %d",
			maxPasswordChecks,
		)
	}
	if cfg.Auth.LoginRateLimit.MaxAttemptsPerAccount <= 0 {
		return errors.New("login account attempt limit must be greater than zero")
	}
	if cfg.Auth.LoginRateLimit.MaxAttemptsPerSource <= 0 {
		return errors.New("login source attempt limit must be greater than zero")
	}
	if cfg.Auth.LoginRateLimit.MaxAttemptsPerSource <
		cfg.Auth.LoginRateLimit.MaxAttemptsPerAccount {
		return errors.New("login source attempt limit must not be below account attempt limit")
	}
	if cfg.Auth.AccountLockout.MaxFailedAttempts <= 0 ||
		cfg.Auth.AccountLockout.MaxFailedAttempts > 100 {
		return errors.New("account lock failed attempt limit must be between 1 and 100")
	}
	if cfg.Auth.InitialAdmin.Enabled {
		if strings.TrimSpace(cfg.Auth.InitialAdmin.Username) == "" ||
			strings.TrimSpace(cfg.Auth.InitialAdmin.Username) !=
				cfg.Auth.InitialAdmin.Username {
			return errors.New(
				"initial administrator username is required and must not contain surrounding whitespace",
			)
		}
		if strings.TrimSpace(cfg.Auth.InitialAdmin.DisplayName) == "" ||
			strings.TrimSpace(cfg.Auth.InitialAdmin.DisplayName) !=
				cfg.Auth.InitialAdmin.DisplayName {
			return errors.New(
				"initial administrator display name is required and must not contain surrounding whitespace",
			)
		}
		if strings.TrimSpace(cfg.Auth.InitialAdmin.PasswordFile) == "" ||
			strings.TrimSpace(cfg.Auth.InitialAdmin.PasswordFile) !=
				cfg.Auth.InitialAdmin.PasswordFile {
			return errors.New(
				"initial administrator password file is required and must not contain surrounding whitespace",
			)
		}
	}
	certificateFileConfigured :=
		strings.TrimSpace(cfg.AgentIdentity.CACertificateFile) != ""
	privateKeyFileConfigured :=
		strings.TrimSpace(cfg.AgentIdentity.CAPrivateKeyFile) != ""
	if certificateFileConfigured != privateKeyFileConfigured {
		return errors.New(
			"Agent Client CA certificate and private key files must be configured together",
		)
	}
	switch cfg.AgentPKI.Mode {
	case "managed":
		if strings.TrimSpace(cfg.AgentPKI.Directory) == "" ||
			strings.TrimSpace(cfg.AgentPKI.Directory) != cfg.AgentPKI.Directory {
			return errors.New("managed Agent PKI directory is required and must not contain surrounding whitespace")
		}
		if len(cfg.AgentPKI.AgentListenerDNSNames) == 0 &&
			len(cfg.AgentPKI.AgentListenerIPAddresses) == 0 {
			return errors.New("managed Agent Listener certificate requires at least one explicit DNS or IP SAN")
		}
		for _, address := range cfg.AgentPKI.AgentListenerIPAddresses {
			if net.ParseIP(address) == nil {
				return fmt.Errorf("managed Agent Listener IP SAN %q is invalid", address)
			}
		}
		for _, dnsName := range cfg.AgentPKI.AgentListenerDNSNames {
			var validationErrors []string
			if strings.HasPrefix(dnsName, "*.") {
				validationErrors = k8svalidation.IsWildcardDNS1123Subdomain(dnsName)
			} else {
				validationErrors = k8svalidation.IsDNS1123Subdomain(dnsName)
			}
			if len(validationErrors) != 0 {
				return fmt.Errorf("managed Agent Listener DNS SAN %q is invalid: %s", dnsName, strings.Join(validationErrors, "; "))
			}
		}
		for _, item := range []struct {
			value time.Duration
			name  string
		}{
			{cfg.AgentPKI.AgentClientCAValidity, "Agent Client CA validity"},
			{cfg.AgentPKI.AgentListenerCAValidity, "Agent Listener CA validity"},
			{cfg.AgentPKI.AgentListenerValidity, "Agent Listener certificate validity"},
			{cfg.AgentPKI.AgentListenerRenewBefore, "Agent Listener renewal window"},
		} {
			if item.value <= 0 || item.value > maxAgentPKIValidity {
				return fmt.Errorf("%s must be greater than zero and not exceed %s", item.name, maxAgentPKIValidity)
			}
		}
		if cfg.AgentPKI.AgentListenerValidity >= cfg.AgentPKI.AgentListenerCAValidity {
			return errors.New("Agent Listener certificate validity must be below its CA validity")
		}
		if cfg.AgentPKI.AgentListenerRenewBefore >= cfg.AgentPKI.AgentListenerValidity {
			return errors.New("Agent Listener renewal window must be below its certificate validity")
		}
	case "external":
	default:
		return errors.New("agent_pki.mode must be managed or external")
	}
	if cfg.AgentEnrollment.RateLimit.MaxAttemptsPerSource <= 0 ||
		cfg.AgentEnrollment.RateLimit.MaxAttemptsPerSource >
			maxAgentEnrollmentAttempts {
		return fmt.Errorf(
			"Agent enrollment source attempt limit must be between 1 and %d",
			maxAgentEnrollmentAttempts,
		)
	}
	_, httpPort, err := net.SplitHostPort(cfg.HTTP.Address)
	if err != nil {
		return errors.New("HTTP address must include a valid host and port")
	}
	_, agentListenerPort, err := net.SplitHostPort(cfg.AgentListener.Address)
	if err != nil {
		return errors.New("Agent Listener address must include a valid host and port")
	}
	if agentListenerPort == httpPort {
		return errors.New("HTTP and Agent Listener ports must be different")
	}
	for _, item := range []struct {
		value string
		name  string
	}{
		{cfg.AgentListener.TLS.CertificateFile, "Agent Listener TLS certificate file"},
		{cfg.AgentListener.TLS.PrivateKeyFile, "Agent Listener TLS private key file"},
		{cfg.AgentIdentity.CACertificateFile, "Agent Client CA certificate file"},
		{cfg.AgentIdentity.CAPrivateKeyFile, "Agent Client CA private key file"},
		{cfg.AgentPKI.ExternalAgentListenerCACertificate, "Agent Listener CA certificate file"},
	} {
		if cfg.AgentPKI.Mode == "managed" {
			break
		}
		if strings.TrimSpace(item.value) == "" ||
			strings.TrimSpace(item.value) != item.value {
			return fmt.Errorf(
				"%s is required and must not contain surrounding whitespace",
				item.name,
			)
		}
	}
	if cfg.AgentInstall.Enabled {
		publicURL, err := url.Parse(cfg.AgentInstall.PublicHTTPURL)
		if err != nil || publicURL.Host == "" ||
			(publicURL.Scheme != "https" && publicURL.Scheme != "http") ||
			publicURL.User != nil || publicURL.RawQuery != "" || publicURL.Fragment != "" ||
			(publicURL.Path != "" && publicURL.Path != "/") {
			return errors.New("enabled Agent installation public HTTP URL must be an HTTP(S) origin without credentials, query, or fragment")
		}
		if publicURL.Scheme == "http" && !isLoopbackAddress(publicURL.Hostname()) {
			return errors.New("enabled Agent installation requires HTTPS except for loopback development")
		}
		if publicURL.Scheme == "http" &&
			strings.TrimSpace(cfg.AgentInstall.RegistrationCACertificateFile) != "" {
			return errors.New("Agent installation registration CA cannot be used with HTTP")
		}
		host, _, err := net.SplitHostPort(cfg.AgentInstall.PublicQUICAddress)
		if err != nil || strings.TrimSpace(host) == "" {
			return errors.New("enabled Agent installation public QUIC address must include a host and port")
		}
		for _, item := range []struct {
			value string
			name  string
		}{
			{cfg.AgentInstall.Image, "Agent installation image"},
			{cfg.AgentInstall.Namespace, "Agent installation namespace"},
			{cfg.AgentInstall.ImagePullPolicy, "Agent image pull policy"},
		} {
			if strings.TrimSpace(item.value) == "" ||
				strings.TrimSpace(item.value) != item.value {
				return fmt.Errorf("%s is required and must not contain surrounding whitespace", item.name)
			}
		}
		switch cfg.AgentInstall.ImagePullPolicy {
		case "Always", "IfNotPresent", "Never":
		default:
			return errors.New("Agent image pull policy must be Always, IfNotPresent, or Never")
		}
		if len(k8svalidation.IsDNS1123Label(cfg.AgentInstall.Namespace)) != 0 {
			return errors.New("Agent installation namespace must be a valid Kubernetes DNS label")
		}
		if strings.ContainsAny(cfg.AgentInstall.Image, " \t\r\n") {
			return errors.New("Agent installation image must not contain whitespace")
		}
		for _, item := range []struct {
			value string
			name  string
		}{
			{cfg.AgentPKI.ExternalAgentListenerCACertificate, "external Agent Listener CA certificate file"},
			{cfg.AgentInstall.RegistrationCACertificateFile, "Agent installation registration CA certificate file"},
		} {
			if strings.TrimSpace(item.value) != item.value {
				return fmt.Errorf("%s must not contain surrounding whitespace", item.name)
			}
		}
	}
	if cfg.CertificateMonitor.WarningBefore <= 0 {
		return errors.New("certificate warning window must be greater than zero")
	}
	if cfg.CertificateMonitor.CheckInterval <= 0 ||
		cfg.CertificateMonitor.CheckInterval > 24*time.Hour {
		return errors.New("certificate monitor interval must be greater than zero and not exceed 24 hours")
	}
	for _, item := range []struct {
		value time.Duration
		max   time.Duration
		name  string
	}{
		{cfg.AgentListener.HandshakeTimeout, maxAgentHandshakeTimeout, "Agent handshake timeout"},
		{cfg.AgentListener.HeartbeatInterval, maxAgentHeartbeatInterval, "Agent heartbeat interval"},
		{cfg.AgentListener.HeartbeatTimeout, maxAgentHeartbeatTimeout, "Agent heartbeat timeout"},
		{cfg.AgentListener.LastSeenWriteInterval, maxAgentLastSeenWriteInterval, "Agent last-seen write interval"},
		{cfg.AgentListener.OperationTimeout, maxAuthOperation, "Agent connection operation timeout"},
	} {
		if item.value <= 0 {
			return fmt.Errorf("%s must be greater than zero", item.name)
		}
		if item.value > item.max {
			return fmt.Errorf("%s must not exceed %s", item.name, item.max)
		}
	}
	if cfg.AgentListener.HeartbeatInterval >= cfg.AgentListener.HeartbeatTimeout {
		return errors.New("Agent heartbeat interval must be below heartbeat timeout")
	}
	if cfg.AgentListener.LastSeenWriteInterval < cfg.AgentListener.HeartbeatInterval {
		return errors.New("Agent last-seen write interval must not be below heartbeat interval")
	}

	return nil
}

func isLoopbackAddress(host string) bool {
	return strings.EqualFold(host, "localhost") ||
		(net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

func applyDuration(target *time.Duration, value, name string) error {
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

func ensureYAMLEOF(decoder *yaml.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("configuration contains multiple YAML documents")
}
