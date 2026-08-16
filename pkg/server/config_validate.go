package server

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
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
	maxPodLogsTimeout             = time.Hour
	maxPodLogsStreams             = 4096
	maxPodLogsRequests            = 100_000
	maxPodAccessSessionTTL        = time.Hour
	maxPendingPodExecSessions     = 100_000
	maxResourceWatchStreams       = 4096
	maxResourceWatchRequests      = 100_000
	maxPodAccessSessions          = 100_000
	maxPodAccessConnections       = 100_000
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
		cfg.validatePodAccess,
		cfg.validateDatabase,
		cfg.validateAuth,
		cfg.validateAgentPKI,
		cfg.validateAgentInstall,
		cfg.validateAgentEnrollment,
		cfg.validateAgentListener,
		cfg.validateObservability,
		cfg.validateRetention,
		cfg.validateProcess,
		cfg.validateCrossSection,
	} {
		if err := validate(); err != nil {
			return err
		}
	}
	return nil
}

// validateObservability only applies when metrics are enabled. A Server that
// was never asked to store metrics must not fail to start over a block nobody
// filled in.
func (cfg Config) validateObservability() error {
	metrics := cfg.Observability.Metrics
	if !metrics.Enabled {
		return nil
	}
	writeURL := strings.TrimSpace(metrics.StorageWriteURL)
	if writeURL == "" {
		return errors.New(
			"observability metrics storage write URL is required when metrics are enabled",
		)
	}
	if writeURL != metrics.StorageWriteURL {
		return errors.New(
			"observability metrics storage write URL must not contain surrounding whitespace",
		)
	}
	parsed, err := url.Parse(writeURL)
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil {
		return errors.New(
			"observability metrics storage write URL must be an HTTP(S) URL without credentials",
		)
	}
	queryURL := strings.TrimSpace(metrics.StorageQueryURL)
	if queryURL == "" {
		return errors.New(
			"observability metrics storage query URL is required when metrics are enabled",
		)
	}
	parsedQuery, err := url.Parse(queryURL)
	if err != nil || queryURL != metrics.StorageQueryURL ||
		parsedQuery.Host == "" ||
		(parsedQuery.Scheme != "http" && parsedQuery.Scheme != "https") ||
		parsedQuery.User != nil {
		return errors.New(
			"observability metrics storage query URL must be an HTTP(S) URL without credentials",
		)
	}
	if metrics.MaxBatchBytes < 1 ||
		metrics.MaxBatchBytes > agentprotocol.MaxMetricsBatchBytesCeiling {
		return fmt.Errorf(
			"observability metrics maximum batch bytes must be between 1 and %d",
			agentprotocol.MaxMetricsBatchBytesCeiling,
		)
	}
	if uint64(metrics.MaxDecompressedBytes) < metrics.MaxBatchBytes {
		return errors.New(
			"observability metrics decompressed batch limit must not be below the compressed limit",
		)
	}
	for _, item := range []struct {
		value int
		name  string
	}{
		{metrics.MaxIngestStreams, "maximum ingest streams"},
		{metrics.MaxQueryClusters, "maximum query clusters"},
		{metrics.MaxQueryPoints, "maximum query points"},
		{metrics.MaxQuerySeries, "maximum query series"},
		{metrics.KubeletMetricsPort, "kubelet metrics port"},
		{metrics.MaxSeriesPerBatch, "maximum series per batch"},
		{metrics.MaxSamplesPerBatch, "maximum samples per batch"},
		{metrics.MaxLabelsPerSeries, "maximum labels per series"},
		{metrics.MaxLabelNameBytes, "maximum label name bytes"},
		{metrics.MaxLabelValueBytes, "maximum label value bytes"},
	} {
		if item.value < 1 {
			return fmt.Errorf("observability metrics %s must be positive", item.name)
		}
	}
	for _, item := range []struct {
		value time.Duration
		max   time.Duration
		name  string
	}{
		{metrics.StorageWriteTimeout, 5 * time.Minute, "storage write timeout"},
		{metrics.StorageQueryTimeout, 5 * time.Minute, "storage query timeout"},
		{metrics.MaxQueryRange, 90 * 24 * time.Hour, "maximum query range"},
		{metrics.MinQueryStep, time.Hour, "minimum query step"},
		{metrics.ScrapeInterval, time.Hour, "collector scrape interval"},
		{metrics.IngestSessionTimeout, time.Hour, "ingest session timeout"},
		{metrics.MaxSampleAge, 30 * 24 * time.Hour, "maximum sample age"},
		{metrics.MaxSampleFuture, time.Hour, "maximum sample future skew"},
	} {
		if item.value <= 0 {
			return fmt.Errorf(
				"observability metrics %s must be greater than zero",
				item.name,
			)
		}
		if item.value > item.max {
			return fmt.Errorf(
				"observability metrics %s must not exceed %s",
				item.name,
				item.max,
			)
		}
	}
	if metrics.KubeletMetricsPort > 65535 {
		return errors.New("observability metrics kubelet metrics port must be a valid port")
	}
	if metrics.ScrapeInterval%time.Second != 0 {
		return errors.New("observability metrics collector scrape interval must be whole seconds")
	}
	if strings.TrimSpace(metrics.CollectorBufferSize) == "" {
		return errors.New("observability metrics collector buffer size is required")
	}
	// The ingest Stream is accepted by the same dispatcher as every other
	// business Stream, and its header must arrive within the handshake budget.
	if cfg.AgentListener.HandshakeTimeout > metrics.IngestSessionTimeout {
		return errors.New(
			"Agent Listener handshake timeout must not exceed the metrics ingest session timeout",
		)
	}
	return nil
}

func (cfg Config) validateAgentInstall() error {
	httpURL := strings.TrimSpace(cfg.AgentInstall.PublicHTTPURL)
	quicAddress := strings.TrimSpace(cfg.AgentInstall.PublicQUICAddress)
	if httpURL == "" && quicAddress == "" {
		return nil
	}
	if httpURL == "" || quicAddress == "" {
		return errors.New("agent_install public HTTP URL and public QUIC address must be configured together")
	}
	if httpURL != cfg.AgentInstall.PublicHTTPURL || quicAddress != cfg.AgentInstall.PublicQUICAddress {
		return errors.New("agent_install public endpoint values must not contain surrounding whitespace")
	}
	parsed, err := url.Parse(httpURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return errors.New("agent_install public HTTP URL must be an HTTP(S) origin without credentials, path, query, or fragment")
	}
	host, port, err := net.SplitHostPort(quicAddress)
	if err != nil || strings.TrimSpace(host) == "" {
		return errors.New("agent_install public QUIC address must use host:port")
	}
	parsedPort, parseErr := strconv.Atoi(port)
	if parseErr != nil || parsedPort < 1 || parsedPort > 65535 {
		return errors.New("agent_install public QUIC address must contain a valid port")
	}
	return nil
}

func (cfg Config) validatePodAccess() error {
	if !cfg.PodAccess.Enabled {
		return validateUnpaddedPaths([]requiredPath{
			{cfg.PodAccess.TLS.CertificateFile, "Pod Access TLS certificate file"},
			{cfg.PodAccess.TLS.PrivateKeyFile, "Pod Access TLS private key file"},
		})
	}
	if _, _, err := net.SplitHostPort(cfg.PodAccess.Address); err != nil {
		return fmt.Errorf("Pod Access address must include a valid host and port: %w", err)
	}
	externalURL, err := url.Parse(cfg.PodAccess.ExternalURL)
	if err != nil || externalURL.Host == "" ||
		(externalURL.Scheme != "https" && externalURL.Scheme != "http") ||
		externalURL.User != nil || externalURL.RawQuery != "" || externalURL.Fragment != "" ||
		(externalURL.Path != "" && externalURL.Path != "/") {
		return errors.New("enabled Pod Access external URL must be an HTTP(S) origin without credentials, path, query, or fragment")
	}
	certificateConfigured := strings.TrimSpace(cfg.PodAccess.TLS.CertificateFile) != ""
	privateKeyConfigured := strings.TrimSpace(cfg.PodAccess.TLS.PrivateKeyFile) != ""
	if certificateConfigured != privateKeyConfigured {
		return errors.New("Pod Access TLS certificate and private key files must be configured together")
	}
	if certificateConfigured && externalURL.Scheme != "https" {
		return errors.New("native Pod Access TLS requires an HTTPS external URL")
	}
	if err := validateUnpaddedPaths([]requiredPath{
		{cfg.PodAccess.TLS.CertificateFile, "Pod Access TLS certificate file"},
		{cfg.PodAccess.TLS.PrivateKeyFile, "Pod Access TLS private key file"},
	}); err != nil {
		return err
	}
	if err := validateDurations([]boundedDuration{
		{cfg.PodAccess.ReadHeaderTimeout, maxHTTPTimeout, "Pod Access read header timeout"},
		{cfg.PodAccess.IdleTimeout, maxIdleTimeout, "Pod Access idle timeout"},
		{cfg.PodAccess.ActivationTTL, time.Minute, "Pod Access activation TTL"},
		{cfg.PodAccess.SessionTTL, maxPodAccessSessionTTL, "Pod Access maximum session TTL"},
		{cfg.PodAccess.RevalidateInterval, time.Minute, "Pod Access revalidation interval"},
	}); err != nil {
		return err
	}
	if cfg.PodAccess.SessionTTL < 15*time.Minute {
		return errors.New("Pod Access maximum session TTL must be at least 15m")
	}
	if cfg.PodAccess.RevalidateInterval >= cfg.PodAccess.SessionTTL {
		return errors.New("Pod Access revalidation interval must be below its session TTL")
	}
	if cfg.PodAccess.MaxPendingSessions <= 0 || cfg.PodAccess.MaxPendingSessions > maxPodAccessSessions ||
		cfg.PodAccess.MaxActiveSessions <= 0 || cfg.PodAccess.MaxActiveSessions > maxPodAccessSessions {
		return fmt.Errorf("Pod Access pending and active session limits must be between 1 and %d", maxPodAccessSessions)
	}
	if cfg.PodAccess.MaxConnections <= 0 || cfg.PodAccess.MaxConnections > maxPodAccessConnections {
		return fmt.Errorf("Pod Access connection limit must be between 1 and %d", maxPodAccessConnections)
	}
	if cfg.PodAccess.MaxConnectionsPerSession <= 0 ||
		cfg.PodAccess.MaxConnectionsPerSession > cfg.PodAccess.MaxConnectionsPerAgent {
		return errors.New("Pod Access per-session connection limit must be between 1 and the per-Agent connection limit")
	}
	if cfg.PodAccess.MaxConnectionsPerAgent <= 0 ||
		cfg.PodAccess.MaxConnectionsPerAgent > cfg.PodAccess.MaxConnections ||
		cfg.PodAccess.MaxConnectionsPerAgent > maxPodLogsStreams {
		return fmt.Errorf("Pod Access per-Agent connection limit must be between 1 and %d and not exceed the global limit", maxPodLogsStreams)
	}
	if cfg.PodAccess.MaxClientBytes == 0 || cfg.PodAccess.MaxClientBytes > maxResourceBodyBytes ||
		cfg.PodAccess.MaxPodBytes == 0 || cfg.PodAccess.MaxPodBytes > maxResourceBodyBytes {
		return fmt.Errorf("Pod Access byte limits must be between 1 and %d", maxResourceBodyBytes)
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
	if err := validateUnpaddedPaths([]requiredPath{
		{cfg.HTTP.ConsoleDirectory, "Console directory"},
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
	return nil
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
	return cfg.validateAgentPKILifecycle()
}

func (cfg Config) validateAgentPKILifecycle() error {
	pkiSettings := cfg.AgentPKI
	if err := validateRequiredPaths([]requiredPath{
		{pkiSettings.Directory, "Agent PKI directory"},
	}); err != nil {
		return err
	}
	for _, item := range []struct {
		value time.Duration
		name  string
	}{
		{pkiSettings.ClientCAValidity, "Agent Client CA validity"},
		{pkiSettings.ListenerCAValidity, "Agent Listener CA validity"},
		{pkiSettings.ListenerValidity, "Agent Listener certificate validity"},
		{pkiSettings.ListenerRenewBefore, "Agent Listener renewal window"},
	} {
		if item.value <= 0 || item.value > maxAgentPKIValidity {
			return fmt.Errorf(
				"%s must be greater than zero and not exceed %s",
				item.name,
				maxAgentPKIValidity,
			)
		}
	}
	if pkiSettings.ListenerValidity >= pkiSettings.ListenerCAValidity {
		return errors.New("Agent Listener certificate validity must be below its CA validity")
	}
	if pkiSettings.ListenerRenewBefore >= pkiSettings.ListenerValidity {
		return errors.New("Agent Listener renewal window must be below its certificate validity")
	}
	return cfg.validateAgentPKIMonitor()
}

// validateAgentPKIMonitor rejects a warning window that a Client certificate
// can never outlive, which would otherwise report every Agent as expiring from
// the moment its certificate is issued and turn the warnings into noise.
func (cfg Config) validateAgentPKIMonitor() error {
	monitor := cfg.AgentPKI.Monitor
	if monitor.WarnBefore <= 0 {
		return errors.New("Agent PKI expiry warning window must be greater than zero")
	}
	if monitor.WarnBefore >= cfg.AgentPKI.ClientCertificateValidity {
		return errors.New(
			"Agent PKI expiry warning window must be below Agent Client certificate validity",
		)
	}
	if monitor.CheckInterval <= 0 || monitor.CheckInterval > 24*time.Hour {
		return errors.New(
			"Agent PKI expiry check interval must be greater than zero and not exceed 24 hours",
		)
	}
	return nil
}

// validateRetention rejects a sweep that would delete rows that are still in
// use. A zero or negative grace deletes a session at the instant it expires and
// a credential at the instant it is superseded, which is exactly when someone
// is most likely to be asking about it; a zero interval would spin.
func (cfg Config) validateRetention() error {
	retention := cfg.Retention
	if retention.SweepInterval <= 0 || retention.SweepInterval > 24*time.Hour {
		return errors.New(
			"retention sweep interval must be greater than zero and not exceed 24 hours",
		)
	}
	for _, window := range []struct {
		value time.Duration
		name  string
	}{
		{retention.Sessions, "session"},
		{retention.Enrollments, "enrollment"},
		{retention.Credentials, "Agent credential"},
	} {
		if window.value <= 0 {
			return fmt.Errorf("%s retention must be greater than zero", window.name)
		}
	}
	return nil
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
		{cfg.AgentListener.PodLogsRequestTimeout, maxPodLogsTimeout, "Agent Pod Logs request timeout"},
		{cfg.AgentListener.PodExecRequestTimeout, maxPodLogsTimeout, "Agent Pod Exec request timeout"},
		{cfg.AgentListener.PodExecSessionTTL, time.Minute, "Pod Exec pending session TTL"},
		{cfg.AgentListener.ResourceWatchRequestTimeout, maxPodLogsTimeout, "Agent Resource Watch request timeout"},
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
	if cfg.AgentListener.MaxPodLogBytes == 0 ||
		cfg.AgentListener.MaxPodLogBytes > maxResourceBodyBytes {
		return fmt.Errorf(
			"Agent Pod log byte limit must be between 1 and %d",
			maxResourceBodyBytes,
		)
	}
	if cfg.AgentListener.MaxPodLogsStreams <= 0 ||
		cfg.AgentListener.MaxPodLogsStreams > maxPodLogsStreams {
		return fmt.Errorf(
			"Agent per-connection Pod Logs Stream limit must be between 1 and %d",
			maxPodLogsStreams,
		)
	}
	if cfg.AgentListener.MaxPodLogsRequests <
		cfg.AgentListener.MaxPodLogsStreams ||
		cfg.AgentListener.MaxPodLogsRequests > maxPodLogsRequests {
		return fmt.Errorf(
			"Server Pod Logs request limit must be between the per-connection limit and %d",
			maxPodLogsRequests,
		)
	}
	if cfg.AgentListener.MaxPodExecInputBytes == 0 ||
		cfg.AgentListener.MaxPodExecInputBytes > maxResourceBodyBytes {
		return fmt.Errorf(
			"Agent Pod Exec input byte limit must be between 1 and %d",
			maxResourceBodyBytes,
		)
	}
	if cfg.AgentListener.MaxPodExecOutputBytes == 0 ||
		cfg.AgentListener.MaxPodExecOutputBytes > maxResourceBodyBytes {
		return fmt.Errorf(
			"Agent Pod Exec output byte limit must be between 1 and %d",
			maxResourceBodyBytes,
		)
	}
	if cfg.AgentListener.MaxPodExecStreams <= 0 ||
		cfg.AgentListener.MaxPodExecStreams > maxPodLogsStreams {
		return fmt.Errorf(
			"Agent per-connection Pod Exec Stream limit must be between 1 and %d",
			maxPodLogsStreams,
		)
	}
	if cfg.AgentListener.MaxPodExecRequests < cfg.AgentListener.MaxPodExecStreams ||
		cfg.AgentListener.MaxPodExecRequests > maxPodLogsRequests {
		return fmt.Errorf(
			"Server Pod Exec request limit must be between the per-connection limit and %d",
			maxPodLogsRequests,
		)
	}
	if cfg.AgentListener.MaxPendingPodExecSessions <= 0 ||
		cfg.AgentListener.MaxPendingPodExecSessions > maxPendingPodExecSessions {
		return fmt.Errorf(
			"Server pending Pod Exec session limit must be between 1 and %d",
			maxPendingPodExecSessions,
		)
	}
	if cfg.AgentListener.MaxResourceWatchStreams <= 0 ||
		cfg.AgentListener.MaxResourceWatchStreams > maxResourceWatchStreams {
		return fmt.Errorf(
			"Agent per-connection Resource Watch Stream limit must be between 1 and %d",
			maxResourceWatchStreams,
		)
	}
	if cfg.AgentListener.MaxResourceWatchRequests <
		cfg.AgentListener.MaxResourceWatchStreams ||
		cfg.AgentListener.MaxResourceWatchRequests > maxResourceWatchRequests {
		return fmt.Errorf(
			"Server Resource Watch request limit must be between the per-connection limit and %d",
			maxResourceWatchRequests,
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
	if cfg.PodAccess.Enabled {
		_, podAccessPort, err := net.SplitHostPort(cfg.PodAccess.Address)
		if err != nil {
			return fmt.Errorf("Pod Access address must include a valid host and port: %w", err)
		}
		if podAccessPort == httpPort || podAccessPort == agentListenerPort {
			return errors.New("Pod Access, HTTP, and Agent Listener ports must be different")
		}
	}
	return nil
}
