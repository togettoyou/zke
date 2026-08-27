package server

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/aitools"
	"github.com/togettoyou/zke/pkg/server/metricscollector"
	"github.com/togettoyou/zke/pkg/server/metricsingest"
	"github.com/togettoyou/zke/pkg/server/metricsquery"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"gopkg.in/yaml.v3"
)

// Config models the YAML configuration file. Decoding happens directly into a
// value pre-populated with defaults, so an absent key keeps its default.
type Config struct {
	HTTP            HTTPConfig            `yaml:"http"`
	PodAccess       PodAccessConfig       `yaml:"pod_access"`
	Database        DatabaseConfig        `yaml:"database"`
	Auth            AuthConfig            `yaml:"auth"`
	AgentPKI        AgentPKIConfig        `yaml:"agent_pki"`
	AgentInstall    AgentInstallConfig    `yaml:"agent_install"`
	AgentEnrollment AgentEnrollmentConfig `yaml:"agent_enrollment"`
	AgentListener   AgentListenerConfig   `yaml:"agent_listener"`
	Helm            HelmConfig            `yaml:"helm"`
	Observability   ObservabilityConfig   `yaml:"observability"`
	AIOps           AIOpsConfig           `yaml:"aiops"`
	Retention       RetentionConfig       `yaml:"retention"`
	ShutdownTimeout time.Duration         `yaml:"shutdown_timeout"`
	LogLevel        string                `yaml:"log_level"`

	// AgentIdentity is derived after the Server PKI material is prepared and is
	// never read from the configuration file.
	AgentIdentity AgentIdentityConfig `yaml:"-"`
}

// RetentionConfig bounds the tables that would otherwise only ever grow:
// sessions, enrollments and their attempts, and superseded Agent credentials.
// Each duration is how long a row is kept after it stopped mattering, counted
// from its own end -- expiry, revocation or consumption.
//
// The defaults are conservative because the cost of keeping a finished row too
// long is disk, and the cost of deleting one too early is an unanswerable
// support question. `audit_events` is not covered here; see RetentionStore.
type RetentionConfig struct {
	// SweepInterval is how often the sweep runs. Nothing depends on it being
	// prompt -- the rows are already unusable -- so it is a background chore,
	// not a deadline.
	SweepInterval time.Duration `yaml:"sweep_interval"`
	Sessions      time.Duration `yaml:"sessions"`
	Enrollments   time.Duration `yaml:"enrollments"`
	Credentials   time.Duration `yaml:"credentials"`
}

// ObservabilityConfig is the deployment-level side of the metrics pipeline:
// where samples are stored and how much one Cluster may send. It is disabled
// by default, and a Server with it disabled never offers metrics ingest to an
// Agent at all.
type ObservabilityConfig struct {
	Metrics MetricsConfig `yaml:"metrics"`
}

type MetricsConfig struct {
	Enabled bool `yaml:"enabled"`
	// StorageWriteURL is the backend's Prometheus remote write endpoint. Only
	// the Server talks to it; it is never proxied to a browser.
	StorageWriteURL string `yaml:"storage_write_url"`
	// The collector image and its pull policy are platform settings rather than
	// configuration file entries: collection is enabled per Cluster long after
	// the Server started, and changing which image a Cluster pulls must not
	// require a restart.
	CollectorBufferSize string        `yaml:"collector_buffer_size"`
	ScrapeInterval      time.Duration `yaml:"scrape_interval"`
	KubeletMetricsPort  int           `yaml:"kubelet_metrics_port"`
	StorageWriteTimeout time.Duration `yaml:"storage_write_timeout"`
	// StorageQueryURL is the same backend's Prometheus-compatible query base.
	// It is configured separately from the write URL because they are
	// different paths, and some deployments put them behind different
	// addresses.
	StorageQueryURL      string        `yaml:"storage_query_url"`
	StorageQueryTimeout  time.Duration `yaml:"storage_query_timeout"`
	MaxQueryPoints       int           `yaml:"max_query_points"`
	MaxQuerySeries       int           `yaml:"max_query_series"`
	MaxQueryRange        time.Duration `yaml:"max_query_range"`
	MinQueryStep         time.Duration `yaml:"min_query_step"`
	IngestSessionTimeout time.Duration `yaml:"ingest_session_timeout"`
	MaxBatchBytes        uint64        `yaml:"max_batch_bytes"`
	MaxIngestStreams     int           `yaml:"max_ingest_streams"`
	MaxDecompressedBytes int           `yaml:"max_decompressed_batch_bytes"`
	MaxSeriesPerBatch    int           `yaml:"max_series_per_batch"`
	MaxSamplesPerBatch   int           `yaml:"max_samples_per_batch"`
	MaxLabelsPerSeries   int           `yaml:"max_labels_per_series"`
	MaxLabelNameBytes    int           `yaml:"max_label_name_bytes"`
	MaxLabelValueBytes   int           `yaml:"max_label_value_bytes"`
	MaxSampleAge         time.Duration `yaml:"max_sample_age"`
	MaxSampleFuture      time.Duration `yaml:"max_sample_future"`
	// The limits above bound one batch. These bound one Cluster over time,
	// which is the only thing that stops a single Cluster from filling the
	// storage every other Cluster shares.
	MaxSamplesPerSecondPerCluster int           `yaml:"max_samples_per_second_per_cluster"`
	SampleBurstWindow             time.Duration `yaml:"sample_burst_window"`
	MaxActiveSeriesPerCluster     int           `yaml:"max_active_series_per_cluster"`
	ActiveSeriesWindow            time.Duration `yaml:"active_series_window"`
}

// AIOpsConfig bounds the AIOps agent loop and its context management.
//
// The endpoint itself is deliberately absent: which model, how wide its context
// window is and how much it may emit are platform settings an administrator
// edits in the Console long after the Server started, because they change when
// the endpoint changes. What lives here is deployment policy — how long a turn
// may run, how far the loop may go, when the conversation is compacted, and how
// hard a failed model request is retried — which changes when the installation
// does.
//
// Compaction is expressed as fractions of whatever context window the endpoint
// actually has rather than as absolute token counts, so pointing the same
// deployment at a wider model widens the conversation instead of leaving a
// stored number quietly wrong.
type AIOpsConfig struct {
	// TurnTimeout bounds one whole turn, model calls and tool reads together.
	TurnTimeout time.Duration `yaml:"turn_timeout"`
	// MaxSteps and MaxToolCalls are the two budgets that guarantee the loop
	// stops: how many times one question may go back to the model, and how many
	// reads the whole turn may perform.
	MaxSteps     int `yaml:"max_steps"`
	MaxToolCalls int `yaml:"max_tool_calls"`
	// MaxParallelToolCalls is how many of one step's reads run at once. Every
	// mutating tool, including a terminal command, makes its step run in model
	// order instead; the bound stops a read-only step from opening a Stream per
	// object to an Agent that also serves the rest of the platform.
	MaxParallelToolCalls int `yaml:"max_parallel_tool_calls"`
	// RepeatedCallLimit is the convergence guard: the same tool with the same
	// arguments beyond this is answered with a note rather than another read.
	RepeatedCallLimit int `yaml:"repeated_call_limit"`
	// ApprovalTimeout is how long a parked call waits for a person, and
	// TitleTimeout how long the naming call beside a first turn may take.
	ApprovalTimeout time.Duration         `yaml:"approval_timeout"`
	TitleTimeout    time.Duration         `yaml:"title_timeout"`
	ModelRetry      AIOpsModelRetryConfig `yaml:"model_retry"`
	Compaction      AIOpsCompactionConfig `yaml:"compaction"`
	ToolResult      AIOpsToolResultConfig `yaml:"tool_result"`
	Subtask         AIOpsSubtaskConfig    `yaml:"subtask"`
}

// AIOpsSubtaskConfig bounds delegated investigation branches.
//
// A branch is a second agent loop, so it needs its own version of every bound
// the main loop has. They are deliberately much tighter: delegation exists to
// answer a few independent questions at once, not to let one question become
// several investigations running on the same operator's budget.
type AIOpsSubtaskConfig struct {
	// MaxParallel is how many branches one delegation may open, and therefore
	// how many model conversations a single step may start. Zero switches
	// delegation off and removes the tool from the catalogue entirely, which is
	// the right setting for a deployment whose endpoint has little headroom.
	MaxParallel int `yaml:"max_parallel"`
	// MaxSteps and MaxToolCalls bound one branch; Timeout bounds it in wall
	// clock inside the turn that owns it.
	MaxSteps     int           `yaml:"max_steps"`
	MaxToolCalls int           `yaml:"max_tool_calls"`
	Timeout      time.Duration `yaml:"timeout"`
}

// AIOpsModelRetryConfig is the bounded backoff applied to a transient model
// failure — a rate limit, a 5xx, a dropped stream. MaxRetries counts attempts
// after the first, so zero disables retrying entirely.
type AIOpsModelRetryConfig struct {
	MaxRetries   int           `yaml:"max_retries"`
	InitialDelay time.Duration `yaml:"initial_delay"`
	MaxDelay     time.Duration `yaml:"max_delay"`
	JitterRatio  float64       `yaml:"jitter_ratio"`
}

// AIOpsCompactionConfig is when a conversation is compacted and how much of it
// survives verbatim.
type AIOpsCompactionConfig struct {
	// ThresholdRatio is the fraction of the context window at which the
	// conversation is compacted before the next request, and RetainRatio the
	// fraction kept verbatim at the tail. Recent steps are what the next step
	// reasons from, so they survive compaction unsummarized.
	ThresholdRatio float64 `yaml:"threshold_ratio"`
	RetainRatio    float64 `yaml:"retain_ratio"`
	// MaxSummaryTokens bounds the summarization call's own output: a checkpoint
	// is a structured brief, not a transcript.
	MaxSummaryTokens int `yaml:"max_summary_tokens"`
	// Retries is how many extra attempts one summarization gets before the
	// runtime falls back to a mechanical summary; MaxOverflowRetries how many
	// times a request the endpoint refused as too large may be compacted and
	// sent again.
	Retries            int `yaml:"retries"`
	MaxOverflowRetries int `yaml:"max_overflow_retries"`
}

// AIOpsToolResultConfig bounds what one tool answer may occupy in the model
// context, in characters.
//
// A result above the threshold keeps its head and its tail with a marker
// between them rather than being cut at the front: the head says what was asked
// and what shape the answer has, and the tail is where a crashed container says
// why it crashed.
type AIOpsToolResultConfig struct {
	ThresholdChars int `yaml:"threshold_chars"`
	HeadChars      int `yaml:"head_chars"`
	TailChars      int `yaml:"tail_chars"`
}

// HelmConfig owns what the chart catalogue keeps on this Server's disk.
type HelmConfig struct {
	Cache HelmCacheConfig `yaml:"cache"`
}

// HelmCacheConfig is the on-disk copy of what chart repositories published.
//
// Without it every catalogue page and every chart opened is a request to the
// repository, a restart throws away everything, and a repository that is slow
// or unreachable makes the Console slow or empty. Directory may be left blank
// to turn the cache off, which is the old behaviour and not recommended.
type HelmCacheConfig struct {
	Directory string `yaml:"directory"`
	// IndexTTL is how long a repository index is used before the repository is
	// asked about it again — a conditional request, so an index that has not
	// changed costs a 304 and no re-parse rather than another download. It is
	// not how long the copy on disk is kept, which is until the repository is
	// edited or deleted; it is only how long a newly published chart may stay
	// invisible, and the Console can force the read at any time.
	IndexTTL time.Duration `yaml:"index_ttl"`
	// MaxBytes bounds the cached chart archives. Indexes are not counted: they
	// are small next to the archives, and they are what keeps a catalogue
	// readable when its repository is not.
	MaxBytes uint64 `yaml:"max_bytes"`
}

type AgentInstallConfig struct {
	PublicHTTPURL     string `yaml:"public_http_url"`
	PublicQUICAddress string `yaml:"public_quic_address"`
}

func (config AgentInstallConfig) EffectiveEndpoint() (string, string) {
	if strings.TrimSpace(config.PublicHTTPURL) == "" && strings.TrimSpace(config.PublicQUICAddress) == "" {
		return "http://127.0.0.1:8080", "127.0.0.1:8443"
	}
	return config.PublicHTTPURL, config.PublicQUICAddress
}

type HTTPConfig struct {
	Address           string            `yaml:"address"`
	ConsoleDirectory  string            `yaml:"console_directory"`
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
	LoginRateLimit              LoginRateLimitConfig `yaml:"login_rate_limit"`
	AccountLockout              AccountLockoutConfig `yaml:"account_lockout"`
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

// AgentIdentityConfig is derived from AgentPKIConfig rather than configured
// directly, keeping certificate paths owned by the Server PKI lifecycle.
type AgentIdentityConfig struct {
	CACertificateFile string
	CAPrivateKeyFile  string
	CertificateTTL    time.Duration
}

// AgentPKIConfig owns every certificate the Server itself issues: the two CAs,
// the Listener identity presented on QUIC, and the client certificates Agents
// receive at enrollment. Fields are ordered storage, Client chain, Listener
// chain, then the expiry surveillance over all of them. Certificates supplied
// from outside the Server, such as http.tls and pod_access.tls, are not part of
// this lifecycle and are not monitored here.
type AgentPKIConfig struct {
	Directory                 string                `yaml:"directory"`
	ClientCAValidity          time.Duration         `yaml:"client_ca_validity"`
	ClientCertificateValidity time.Duration         `yaml:"client_certificate_validity"`
	ListenerCAValidity        time.Duration         `yaml:"listener_ca_validity"`
	ListenerValidity          time.Duration         `yaml:"listener_certificate_validity"`
	ListenerRenewBefore       time.Duration         `yaml:"listener_renew_before"`
	Monitor                   AgentPKIMonitorConfig `yaml:"monitor"`
}

// AgentPKIMonitorConfig drives the Agent status API's expiring state and the
// periodic structured expiry warnings. WarnBefore must stay below
// ClientCertificateValidity, otherwise every Agent reports expiring from the
// moment its certificate is issued.
type AgentPKIMonitorConfig struct {
	WarnBefore    time.Duration `yaml:"warn_before"`
	CheckInterval time.Duration `yaml:"check_interval"`
}

type AgentEnrollmentConfig struct {
	OperationTimeout time.Duration                  `yaml:"operation_timeout"`
	RateLimit        AgentEnrollmentRateLimitConfig `yaml:"rate_limit"`
}

type AgentEnrollmentRateLimitConfig struct {
	Window               time.Duration `yaml:"window"`
	MaxAttemptsPerSource int           `yaml:"max_attempts_per_source"`
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
	// Helm release changes. The per-Agent default is one because two
	// operations on the same release race over Helm's own storage, and the
	// timeout is minutes rather than seconds because an install that waits for
	// a rollout is doing exactly that.
	HelmRequestTimeout time.Duration `yaml:"helm_request_timeout"`
	MaxHelmStreams     int           `yaml:"max_helm_streams_per_agent"`
	MaxHelmRequests    int           `yaml:"max_concurrent_helm_requests"`

	// TLS is derived from agent_pki, not configured under agent_listener.
	TLS TLSIdentityConfig `yaml:"-"`
}

// DefaultConfig reports the configuration used when the file omits a key.
func DefaultConfig() Config {
	return Config{
		HTTP: HTTPConfig{
			Address:           "0.0.0.0:8080",
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		PodAccess: PodAccessConfig{
			Enabled:                  true,
			Address:                  "0.0.0.0:8081",
			ExternalURL:              "http://127.0.0.1:8081",
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
			URL:              "postgres://zke_dev:zke_local_development_only@127.0.0.1:5432/zke?sslmode=disable",
			ConnectTimeout:   5 * time.Second,
			MigrationTimeout: 2 * time.Minute,
			MaxConnections:   16,
			MinConnections:   2,
			MaxConnLifetime:  time.Hour,
			MaxConnIdleTime:  30 * time.Minute,
		},
		Auth: AuthConfig{
			SessionIdleTimeout:          30 * time.Minute,
			SessionAbsoluteTimeout:      8 * time.Hour,
			OperationTimeout:            10 * time.Second,
			MaxConcurrentPasswordChecks: 4,
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
			Directory:                 "data/pki",
			ClientCAValidity:          10 * 365 * 24 * time.Hour,
			ClientCertificateValidity: 30 * 24 * time.Hour,
			ListenerCAValidity:        20 * 365 * 24 * time.Hour,
			ListenerValidity:          10 * 365 * 24 * time.Hour,
			ListenerRenewBefore:       365 * 24 * time.Hour,
			Monitor: AgentPKIMonitorConfig{
				WarnBefore:    7 * 24 * time.Hour,
				CheckInterval: time.Hour,
			},
		},
		Helm: HelmConfig{
			Cache: HelmCacheConfig{
				Directory: "data/helm",
				IndexTTL:  time.Hour,
				MaxBytes:  2 << 30,
			},
		},
		Retention: RetentionConfig{
			SweepInterval: time.Hour,
			Sessions:      24 * time.Hour,
			Enrollments:   30 * 24 * time.Hour,
			Credentials:   30 * 24 * time.Hour,
		},
		AgentEnrollment: AgentEnrollmentConfig{
			OperationTimeout: 10 * time.Second,
			RateLimit: AgentEnrollmentRateLimitConfig{
				Window:               time.Minute,
				MaxAttemptsPerSource: 30,
			},
		},
		AgentListener: AgentListenerConfig{
			Address:                     "0.0.0.0:8443",
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
			HelmRequestTimeout:          15 * time.Minute,
			MaxHelmStreams:              1,
			MaxHelmRequests:             64,
		},
		Observability: ObservabilityConfig{
			Metrics: MetricsConfig{
				Enabled:              true,
				StorageWriteURL:      "http://127.0.0.1:8428/api/v1/write",
				StorageQueryURL:      "http://127.0.0.1:8428/prometheus",
				CollectorBufferSize:  metricscollector.DefaultBufferSize,
				ScrapeInterval:       metricscollector.DefaultScrapeInterval,
				KubeletMetricsPort:   metricscollector.DefaultKubeletMetricsPort,
				StorageWriteTimeout:  metricsingest.DefaultWriteTimeout,
				StorageQueryTimeout:  metricsquery.DefaultQueryTimeout,
				MaxQueryPoints:       metricsquery.DefaultMaxPoints,
				MaxQuerySeries:       metricsquery.DefaultMaxSeries,
				MaxQueryRange:        metricsquery.DefaultMaxRange,
				MinQueryStep:         metricsquery.DefaultMinStep,
				IngestSessionTimeout: agentprotocol.DefaultMetricsIngestTimeout,
				MaxBatchBytes:        agentprotocol.DefaultMaxMetricsBatchBytes,
				MaxIngestStreams:     512,
				MaxDecompressedBytes: metricsingest.DefaultMaxDecompressedBytes,
				MaxSeriesPerBatch:    metricsingest.DefaultMaxSeriesPerBatch,
				MaxSamplesPerBatch:   metricsingest.DefaultMaxSamplesPerBatch,
				MaxLabelsPerSeries:   metricsingest.DefaultMaxLabelsPerSeries,
				MaxLabelNameBytes:    metricsingest.DefaultMaxLabelNameBytes,
				MaxLabelValueBytes:   metricsingest.DefaultMaxLabelValueBytes,
				MaxSampleAge:         metricsingest.DefaultMaxSampleAge,
				MaxSampleFuture:      metricsingest.DefaultMaxSampleFuture,

				MaxSamplesPerSecondPerCluster: metricsingest.DefaultMaxSamplesPerSecond,
				SampleBurstWindow:             metricsingest.DefaultSampleBurstWindow,
				MaxActiveSeriesPerCluster:     metricsingest.DefaultMaxActiveSeries,
				ActiveSeriesWindow:            metricsingest.DefaultActiveSeriesWindow,
			},
		},
		AIOps: AIOpsConfig{
			TurnTimeout:          airuntime.DefaultTurnTimeout,
			MaxSteps:             airuntime.DefaultMaxSteps,
			MaxToolCalls:         airuntime.DefaultMaxToolCalls,
			MaxParallelToolCalls: airuntime.DefaultMaxParallelToolCalls,
			RepeatedCallLimit:    airuntime.DefaultRepeatedCallLimit,
			ApprovalTimeout:      airuntime.DefaultApprovalTimeout,
			TitleTimeout:         airuntime.DefaultTitleTimeout,
			ModelRetry: AIOpsModelRetryConfig{
				MaxRetries:   airuntime.DefaultModelRetries,
				InitialDelay: airuntime.DefaultModelRetryInitialDelay,
				MaxDelay:     airuntime.DefaultModelRetryMaxDelay,
				JitterRatio:  airuntime.DefaultModelRetryJitterRatio,
			},
			Compaction: AIOpsCompactionConfig{
				ThresholdRatio:     airuntime.DefaultCompactionThresholdRatio,
				RetainRatio:        airuntime.DefaultCompactionRetainRatio,
				MaxSummaryTokens:   airuntime.DefaultCompactionMaxSummaryTokens,
				Retries:            airuntime.DefaultCompactionRetries,
				MaxOverflowRetries: airuntime.DefaultMaxOverflowRetries,
			},
			ToolResult: AIOpsToolResultConfig{
				ThresholdChars: aitools.DefaultResultThresholdRunes,
				HeadChars:      aitools.DefaultResultHeadRunes,
				TailChars:      aitools.DefaultResultTailRunes,
			},
			Subtask: AIOpsSubtaskConfig{
				MaxParallel:  airuntime.DefaultMaxParallelSubtasks,
				MaxSteps:     airuntime.DefaultSubtaskSteps,
				MaxToolCalls: airuntime.DefaultSubtaskToolCalls,
				Timeout:      airuntime.DefaultSubtaskTimeout,
			},
		},
		ShutdownTimeout: 10 * time.Second,
		LogLevel:        "info",
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
	if err := applyEnvironmentOverrides(&cfg); err != nil {
		return Config{}, err
	}
	cfg.resolveDerivedIdentity()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// applyEnvironmentOverrides keeps the checked-in configuration usable as the
// single container default while allowing an orchestrator to inject values
// that are deployment identities or credentials. Partial and complete config
// files are supported; these explicitly set variables take precedence.
func applyEnvironmentOverrides(cfg *Config) error {
	overrides := []struct {
		name   string
		target *string
	}{
		{"ZKE_DATABASE_URL", &cfg.Database.URL},
		{"ZKE_CONSOLE_DIRECTORY", &cfg.HTTP.ConsoleDirectory},
		{"ZKE_POD_ACCESS_EXTERNAL_URL", &cfg.PodAccess.ExternalURL},
		{"ZKE_AGENT_INSTALL_PUBLIC_HTTP_URL", &cfg.AgentInstall.PublicHTTPURL},
		{"ZKE_AGENT_INSTALL_PUBLIC_QUIC_ADDRESS", &cfg.AgentInstall.PublicQUICAddress},
		// The metrics storage lives outside ZKE and differs per deployment, so
		// it belongs with the other addresses here. Without these three, the
		// only way to point a container at real storage — or to turn metrics
		// off — is to replace the whole configuration file it ships with.
		{
			"ZKE_OBSERVABILITY_METRICS_STORAGE_WRITE_URL",
			&cfg.Observability.Metrics.StorageWriteURL,
		},
		{
			"ZKE_OBSERVABILITY_METRICS_STORAGE_QUERY_URL",
			&cfg.Observability.Metrics.StorageQueryURL,
		},
	}
	// An empty value counts as unset. Deployment tooling defines these
	// variables whether or not the operator filled them in — Compose does it
	// for every entry in its environment block — and an empty string that
	// erased what the configuration file said would be a surprise nobody asked
	// for. Every value here already defaults to empty, so nothing is lost by
	// refusing to set one that way.
	for _, override := range overrides {
		if value := strings.TrimSpace(os.Getenv(override.name)); value != "" {
			*override.target = value
		}
	}
	// Parsed rather than compared against "true": a deployment that meant to
	// disable metrics and wrote "yes" must be told, not quietly left running
	// with them on.
	if value := strings.TrimSpace(
		os.Getenv("ZKE_OBSERVABILITY_METRICS_ENABLED"),
	); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf(
				"ZKE_OBSERVABILITY_METRICS_ENABLED must be a boolean: %w",
				err,
			)
		}
		cfg.Observability.Metrics.Enabled = enabled
	}
	return nil
}

func decodeConfigFile(cfg *Config, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open config file %q: %w", path, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode config file %q: %w", path, err)
	}
	if err := ensureYAMLEOF(decoder); err != nil {
		return fmt.Errorf("decode config file %q: %w", path, err)
	}
	return nil
}

// resolveDerivedIdentity fills the non-path identity setting. Server PKI paths
// are resolved after the database-backed endpoint SANs are loaded at startup.
func (cfg *Config) resolveDerivedIdentity() {
	cfg.AgentIdentity.CertificateTTL = cfg.AgentPKI.ClientCertificateValidity
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
