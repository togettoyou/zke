package server

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

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
	maxDatabaseConnections        = 512
	maxConcurrentAgents           = 100_000
	maxRememberedDisconnects      = 1_000_000
	maxResourceBodyBytes          = 1024 * 1024 * 1024
	maxBufferedResourceBytes      = 8 * 1024 * 1024 * 1024
	maxResourceStreams            = 4096
	maxResourceRequests           = 1_000_000
)

// boundedDuration describes a duration that must be positive and capped.
type boundedDuration struct {
	value time.Duration
	max   time.Duration
	name  string
}

func validateDurations(items []boundedDuration) error {
	for _, item := range items {
		if item.value <= 0 {
			return fmt.Errorf("%s must be greater than zero", item.name)
		}
		if item.value > item.max {
			return fmt.Errorf("%s must not exceed %s", item.name, item.max)
		}
	}
	return nil
}

// requiredPath describes a file path that must be present and unpadded.
type requiredPath struct {
	value string
	name  string
}

func validateRequiredPaths(items []requiredPath) error {
	for _, item := range items {
		if strings.TrimSpace(item.value) == "" ||
			strings.TrimSpace(item.value) != item.value {
			return fmt.Errorf(
				"%s is required and must not contain surrounding whitespace",
				item.name,
			)
		}
	}
	return nil
}

func validateUnpaddedPaths(items []requiredPath) error {
	for _, item := range items {
		if strings.TrimSpace(item.value) != item.value {
			return fmt.Errorf("%s must not contain surrounding whitespace", item.name)
		}
	}
	return nil
}

// Validate reports the first configuration problem that would make the Server
// start in an unsafe or unusable state.
func (cfg Config) Validate() error {
	for _, validate := range []func() error{
		cfg.validateHTTP,
		cfg.validateDatabase,
		cfg.validateAuth,
		cfg.validateAgentPKI,
		cfg.validateAgentEnrollment,
		cfg.validateAgentInstall,
		cfg.validateAgentListener,
		cfg.validateCertificateMonitor,
		cfg.validateProcess,
		cfg.validateCrossSection,
	} {
		if err := validate(); err != nil {
			return err
		}
	}
	return nil
}

func (cfg Config) validateHTTP() error {
	if strings.TrimSpace(cfg.HTTP.Address) == "" {
		return errors.New("http address is required")
	}
	if _, _, err := net.SplitHostPort(cfg.HTTP.Address); err != nil {
		return fmt.Errorf("HTTP address must include a valid host and port: %w", err)
	}
	certificateConfigured := strings.TrimSpace(cfg.HTTP.TLS.CertificateFile) != ""
	privateKeyConfigured := strings.TrimSpace(cfg.HTTP.TLS.PrivateKeyFile) != ""
	if certificateConfigured != privateKeyConfigured {
		return errors.New(
			"HTTP TLS certificate and private key files must be configured together",
		)
	}
	if certificateConfigured && !cfg.Auth.CookieSecure {
		return errors.New("HTTP TLS requires secure session cookies")
	}
	if err := validateUnpaddedPaths([]requiredPath{
		{cfg.HTTP.TLS.CertificateFile, "HTTP TLS certificate file"},
		{cfg.HTTP.TLS.PrivateKeyFile, "HTTP TLS private key file"},
	}); err != nil {
		return err
	}
	return validateDurations([]boundedDuration{
		{cfg.HTTP.ReadHeaderTimeout, maxHTTPTimeout, "http read header timeout"},
		{cfg.HTTP.ReadTimeout, maxHTTPTimeout, "http read timeout"},
		{cfg.HTTP.WriteTimeout, maxHTTPTimeout, "http write timeout"},
		{cfg.HTTP.IdleTimeout, maxIdleTimeout, "http idle timeout"},
	})
}

func (cfg Config) validateDatabase() error {
	if strings.TrimSpace(cfg.Database.URL) == "" {
		return errors.New("PostgreSQL URL is required")
	}
	if cfg.Database.MaxConnections <= 0 ||
		cfg.Database.MaxConnections > maxDatabaseConnections {
		return fmt.Errorf(
			"database max connections must be between 1 and %d",
			maxDatabaseConnections,
		)
	}
	if cfg.Database.MinConnections < 0 ||
		cfg.Database.MinConnections > cfg.Database.MaxConnections {
		return errors.New("database min connections must be between zero and max connections")
	}
	return validateDurations([]boundedDuration{
		{cfg.Database.ConnectTimeout, maxDatabaseTimeout, "database connect timeout"},
		{cfg.Database.MigrationTimeout, maxMigrationTimeout, "database migration timeout"},
		{cfg.Database.MaxConnLifetime, maxSessionAbsolute, "database connection lifetime"},
		{cfg.Database.MaxConnIdleTime, maxSessionAbsolute, "database connection idle time"},
	})
}

func (cfg Config) validateAuth() error {
	if err := validateDurations([]boundedDuration{
		{cfg.Auth.SessionIdleTimeout, maxSessionIdle, "session idle timeout"},
		{cfg.Auth.SessionAbsoluteTimeout, maxSessionAbsolute, "session absolute timeout"},
		{cfg.Auth.OperationTimeout, maxAuthOperation, "authentication operation timeout"},
		{cfg.Auth.LoginRateLimit.Window, maxLoginRateWindow, "login rate limit window"},
		{cfg.Auth.AccountLockout.Duration, maxLoginRateWindow, "account lock duration"},
	}); err != nil {
		return err
	}
	if cfg.Auth.SessionIdleTimeout > cfg.Auth.SessionAbsoluteTimeout {
		return errors.New("session idle timeout must not exceed session absolute timeout")
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
	if !cfg.Auth.InitialAdmin.Enabled {
		return nil
	}
	return validateRequiredPaths([]requiredPath{
		{cfg.Auth.InitialAdmin.Username, "initial administrator username"},
		{cfg.Auth.InitialAdmin.DisplayName, "initial administrator display name"},
		{cfg.Auth.InitialAdmin.PasswordFile, "initial administrator password file"},
	})
}

func (cfg Config) validateAgentPKI() error {
	if err := validateDurations([]boundedDuration{
		{
			cfg.AgentIdentity.CertificateTTL,
			maxAgentCertificateTTL,
			"Agent certificate TTL",
		},
	}); err != nil {
		return err
	}
	switch cfg.AgentPKI.Mode {
	case "managed":
		return cfg.validateManagedAgentPKI()
	case "external":
		return cfg.validateExternalAgentPKI()
	default:
		return errors.New("agent_pki.mode must be managed or external")
	}
}

func (cfg Config) validateManagedAgentPKI() error {
	managed := cfg.AgentPKI.Managed
	if err := validateRequiredPaths([]requiredPath{
		{managed.Directory, "managed Agent PKI directory"},
	}); err != nil {
		return err
	}
	if len(managed.ListenerSANs.DNSNames) == 0 &&
		len(managed.ListenerSANs.IPAddresses) == 0 {
		return errors.New(
			"managed Agent Listener certificate requires at least one explicit DNS or IP SAN",
		)
	}
	for _, address := range managed.ListenerSANs.IPAddresses {
		if net.ParseIP(address) == nil {
			return fmt.Errorf("managed Agent Listener IP SAN %q is invalid", address)
		}
	}
	for _, dnsName := range managed.ListenerSANs.DNSNames {
		var validationErrors []string
		if strings.HasPrefix(dnsName, "*.") {
			validationErrors = k8svalidation.IsWildcardDNS1123Subdomain(dnsName)
		} else {
			validationErrors = k8svalidation.IsDNS1123Subdomain(dnsName)
		}
		if len(validationErrors) != 0 {
			return fmt.Errorf(
				"managed Agent Listener DNS SAN %q is invalid: %s",
				dnsName,
				strings.Join(validationErrors, "; "),
			)
		}
	}
	for _, item := range []struct {
		value time.Duration
		name  string
	}{
		{managed.AgentClientCAValidity, "Agent Client CA validity"},
		{managed.AgentListenerCAValidity, "Agent Listener CA validity"},
		{managed.AgentListenerValidity, "Agent Listener certificate validity"},
		{managed.AgentListenerRenewBefore, "Agent Listener renewal window"},
	} {
		if item.value <= 0 || item.value > maxAgentPKIValidity {
			return fmt.Errorf(
				"%s must be greater than zero and not exceed %s",
				item.name,
				maxAgentPKIValidity,
			)
		}
	}
	if managed.AgentListenerValidity >= managed.AgentListenerCAValidity {
		return errors.New("Agent Listener certificate validity must be below its CA validity")
	}
	if managed.AgentListenerRenewBefore >= managed.AgentListenerValidity {
		return errors.New("Agent Listener renewal window must be below its certificate validity")
	}
	return nil
}

// validateExternalAgentPKI checks the resolved paths rather than the raw
// agent_pki.external block, because those are what the Server actually loads.
func (cfg Config) validateExternalAgentPKI() error {
	certificateConfigured :=
		strings.TrimSpace(cfg.AgentIdentity.CACertificateFile) != ""
	privateKeyConfigured :=
		strings.TrimSpace(cfg.AgentIdentity.CAPrivateKeyFile) != ""
	if certificateConfigured != privateKeyConfigured {
		return errors.New(
			"Agent Client CA certificate and private key files must be configured together",
		)
	}
	return validateRequiredPaths([]requiredPath{
		{cfg.AgentListener.TLS.CertificateFile, "Agent Listener TLS certificate file"},
		{cfg.AgentListener.TLS.PrivateKeyFile, "Agent Listener TLS private key file"},
		{cfg.AgentIdentity.CACertificateFile, "Agent Client CA certificate file"},
		{cfg.AgentIdentity.CAPrivateKeyFile, "Agent Client CA private key file"},
		{
			cfg.AgentPKI.External.AgentListenerCA.CertificateFile,
			"Agent Listener CA certificate file",
		},
	})
}

func (cfg Config) validateAgentEnrollment() error {
	if err := validateDurations([]boundedDuration{
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
	}); err != nil {
		return err
	}
	if cfg.AgentEnrollment.RateLimit.MaxAttemptsPerSource <= 0 ||
		cfg.AgentEnrollment.RateLimit.MaxAttemptsPerSource > maxAgentEnrollmentAttempts {
		return fmt.Errorf(
			"Agent enrollment source attempt limit must be between 1 and %d",
			maxAgentEnrollmentAttempts,
		)
	}
	return nil
}

func (cfg Config) validateAgentInstall() error {
	if !cfg.AgentInstall.Enabled {
		return nil
	}
	publicURL, err := url.Parse(cfg.AgentInstall.PublicHTTPURL)
	if err != nil || publicURL.Host == "" ||
		(publicURL.Scheme != "https" && publicURL.Scheme != "http") ||
		publicURL.User != nil || publicURL.RawQuery != "" || publicURL.Fragment != "" ||
		(publicURL.Path != "" && publicURL.Path != "/") {
		return errors.New(
			"enabled Agent installation public HTTP URL must be an HTTP(S) origin without credentials, query, or fragment",
		)
	}
	if publicURL.Scheme == "http" && !isLoopbackAddress(publicURL.Hostname()) {
		return errors.New(
			"enabled Agent installation requires HTTPS except for loopback development",
		)
	}
	if publicURL.Scheme == "http" &&
		strings.TrimSpace(cfg.AgentInstall.RegistrationCACertificateFile) != "" {
		return errors.New("Agent installation registration CA cannot be used with HTTP")
	}
	host, _, err := net.SplitHostPort(cfg.AgentInstall.PublicQUICAddress)
	if err != nil || strings.TrimSpace(host) == "" {
		return errors.New(
			"enabled Agent installation public QUIC address must include a host and port",
		)
	}
	if err := validateRequiredPaths([]requiredPath{
		{cfg.AgentInstall.Image, "Agent installation image"},
		{cfg.AgentInstall.Namespace, "Agent installation namespace"},
		{cfg.AgentInstall.ImagePullPolicy, "Agent image pull policy"},
	}); err != nil {
		return err
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
	return validateUnpaddedPaths([]requiredPath{
		{
			cfg.AgentPKI.External.AgentListenerCA.CertificateFile,
			"external Agent Listener CA certificate file",
		},
		{
			cfg.AgentInstall.RegistrationCACertificateFile,
			"Agent installation registration CA certificate file",
		},
	})
}

func (cfg Config) validateAgentListener() error {
	if _, _, err := net.SplitHostPort(cfg.AgentListener.Address); err != nil {
		return fmt.Errorf(
			"Agent Listener address must include a valid host and port: %w",
			err,
		)
	}
	if err := validateDurations([]boundedDuration{
		{cfg.AgentListener.HandshakeTimeout, maxAgentHandshakeTimeout, "Agent handshake timeout"},
		{cfg.AgentListener.HeartbeatInterval, maxAgentHeartbeatInterval, "Agent heartbeat interval"},
		{cfg.AgentListener.HeartbeatTimeout, maxAgentHeartbeatTimeout, "Agent heartbeat timeout"},
		{
			cfg.AgentListener.LastSeenWriteInterval,
			maxAgentLastSeenWriteInterval,
			"Agent last-seen write interval",
		},
		{cfg.AgentListener.OperationTimeout, maxAuthOperation, "Agent connection operation timeout"},
		{cfg.AgentListener.WriteTimeout, maxAgentHandshakeTimeout, "Agent control write timeout"},
		{cfg.AgentListener.ResourceRequestTimeout, maxHTTPTimeout, "Agent Resource request timeout"},
		{cfg.AgentListener.ConnectionDrainTimeout, maxShutdownTimeout, "Agent Connection drain timeout"},
	}); err != nil {
		return err
	}
	if cfg.AgentListener.HeartbeatInterval >= cfg.AgentListener.HeartbeatTimeout {
		return errors.New("Agent heartbeat interval must be below heartbeat timeout")
	}
	if cfg.AgentListener.LastSeenWriteInterval < cfg.AgentListener.HeartbeatInterval {
		return errors.New("Agent last-seen write interval must not be below heartbeat interval")
	}
	if cfg.AgentListener.MaxConcurrentAgents <= 0 ||
		cfg.AgentListener.MaxConcurrentAgents > maxConcurrentAgents {
		return fmt.Errorf(
			"Agent Listener concurrent Agent limit must be between 1 and %d",
			maxConcurrentAgents,
		)
	}
	if cfg.AgentListener.MaxIncomingStreams <= 0 {
		return errors.New("Agent Listener incoming stream limit must be greater than zero")
	}
	if cfg.AgentListener.MaxRememberedDisconnects <= 0 ||
		cfg.AgentListener.MaxRememberedDisconnects > maxRememberedDisconnects {
		return fmt.Errorf(
			"Agent Listener remembered disconnect limit must be between 1 and %d",
			maxRememberedDisconnects,
		)
	}
	if cfg.AgentListener.MaxResourceBodyBytes == 0 ||
		cfg.AgentListener.MaxResourceBodyBytes > maxResourceBodyBytes {
		return fmt.Errorf(
			"Agent Resource body limit must be between 1 and %d",
			maxResourceBodyBytes,
		)
	}
	if cfg.AgentListener.MaxBufferedResourceBytes <
		cfg.AgentListener.MaxResourceBodyBytes ||
		cfg.AgentListener.MaxBufferedResourceBytes >
			maxBufferedResourceBytes {
		return fmt.Errorf(
			"Server buffered Resource response limit must be between the single body limit and %d",
			uint64(maxBufferedResourceBytes),
		)
	}
	if cfg.AgentListener.MaxResourceStreams <= 0 ||
		cfg.AgentListener.MaxResourceStreams > maxResourceStreams {
		return fmt.Errorf(
			"Agent per-connection Resource Stream limit must be between 1 and %d",
			maxResourceStreams,
		)
	}
	if cfg.AgentListener.MaxResourceRequests <
		cfg.AgentListener.MaxResourceStreams ||
		cfg.AgentListener.MaxResourceRequests > maxResourceRequests {
		return fmt.Errorf(
			"Server Resource request limit must be between the per-connection limit and %d",
			maxResourceRequests,
		)
	}
	return nil
}

func (cfg Config) validateCertificateMonitor() error {
	if cfg.CertificateMonitor.WarningBefore <= 0 {
		return errors.New("certificate warning window must be greater than zero")
	}
	if cfg.CertificateMonitor.CheckInterval <= 0 ||
		cfg.CertificateMonitor.CheckInterval > 24*time.Hour {
		return errors.New(
			"certificate monitor interval must be greater than zero and not exceed 24 hours",
		)
	}
	return nil
}

func (cfg Config) validateProcess() error {
	if strings.TrimSpace(cfg.LogLevel) == "" {
		return errors.New("log level is required")
	}
	return validateDurations([]boundedDuration{
		{cfg.ShutdownTimeout, maxShutdownTimeout, "shutdown timeout"},
	})
}

// validateCrossSection covers the constraints that only hold between sections,
// which are the ones most easily broken by editing a single value.
func (cfg Config) validateCrossSection() error {
	if cfg.Auth.OperationTimeout >= cfg.HTTP.WriteTimeout {
		return errors.New("authentication operation timeout must be below HTTP write timeout")
	}
	if cfg.AgentEnrollment.OperationTimeout >= cfg.HTTP.WriteTimeout {
		return errors.New("Agent enrollment operation timeout must be below HTTP write timeout")
	}
	_, httpPort, err := net.SplitHostPort(cfg.HTTP.Address)
	if err != nil {
		return fmt.Errorf("HTTP address must include a valid host and port: %w", err)
	}
	_, agentListenerPort, err := net.SplitHostPort(cfg.AgentListener.Address)
	if err != nil {
		return fmt.Errorf(
			"Agent Listener address must include a valid host and port: %w",
			err,
		)
	}
	if agentListenerPort == httpPort {
		return errors.New("HTTP and Agent Listener ports must be different")
	}
	return nil
}

func isLoopbackAddress(host string) bool {
	return strings.EqualFold(host, "localhost") ||
		(net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}
