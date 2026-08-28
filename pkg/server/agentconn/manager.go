package agentconn

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/identifier"
	"github.com/togettoyou/zke/pkg/shared/requestctx"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

// ConnectionStore is the persistence surface the Agent connection manager
// needs. Declaring it here keeps the manager testable without PostgreSQL and
// documents exactly which writes an Agent connection can perform.
type ConnectionStore interface {
	Activate(ctx context.Context, params store.ActivateAgentConnectionParams) error
	RecordHeartbeat(ctx context.Context, params store.RecordAgentHeartbeatParams) error
	WatchRevocations(
		ctx context.Context,
		onReady func(),
		handle func(store.AgentConnectionRevocation),
	) error
}

type Config struct {
	Address                      string
	TLSCertificateFile           string
	TLSPrivateKeyFile            string
	TLSCertificateReloader       *TLSCertificateReloader
	ClientCACertificateFile      string
	HandshakeTimeout             time.Duration
	HeartbeatInterval            time.Duration
	HeartbeatTimeout             time.Duration
	LastSeenWriteInterval        time.Duration
	OperationTimeout             time.Duration
	MaxConcurrentAgents          int
	MaxIncomingStreams           int64
	WriteTimeout                 time.Duration
	ResourceRequestTimeout       time.Duration
	ConnectionDrainTimeout       time.Duration
	MaxResourceBodyBytes         uint64
	MaxResourceStreams           int
	MaxResourceRequests          int
	PodLogsRequestTimeout        time.Duration
	MaxPodLogBytes               uint64
	MaxPodLogsStreams            int
	MaxPodLogsRequests           int
	PodExecRequestTimeout        time.Duration
	MaxPodExecInputBytes         uint64
	MaxPodExecOutputBytes        uint64
	MaxPodExecStreams            int
	MaxPodExecRequests           int
	PodPortForwardRequestTimeout time.Duration
	MaxPodPortForwardClientBytes uint64
	MaxPodPortForwardPodBytes    uint64
	MaxPodPortForwardStreams     int
	MaxPodPortForwardRequests    int
	ResourceWatchRequestTimeout  time.Duration
	MaxResourceWatchStreams      int
	MaxResourceWatchRequests     int
	// Helm has budgets of its own because a release change is not shaped like
	// a resource request: it takes minutes rather than seconds when it waits
	// for a rollout, and it must not be able to hold the allowance that every
	// ordinary read draws on.
	HelmRequestTimeout time.Duration
	MaxHelmStreams     int
	MaxHelmRequests    int
	// Metrics Ingest is the one business Stream the Agent opens. Its budgets
	// are separate from every Server-initiated kind so that a Cluster shipping
	// metrics cannot consume the allowance that resource requests, logs and
	// terminals draw on.
	MetricsIngestTimeout    time.Duration
	MaxMetricsBatchBytes    uint64
	MaxMetricsIngestStreams int
	// MetricsSink receives accepted batches. Leaving it nil disables ingest
	// entirely: the Server then never advertises the capability, so a
	// collecting Agent learns there is nowhere to send data instead of opening
	// a Stream that would be refused batch by batch.
	MetricsSink MetricsSink
	// MaxRememberedDisconnects bounds how many disconnected Agents keep a
	// last-known status in memory, so Cluster churn cannot grow the Server
	// heap without limit.
	MaxRememberedDisconnects int
}

type Manager struct {
	config  Config
	logger  *slog.Logger
	store   ConnectionStore
	renewal *enrollment.CertificateRenewalService
	tls     *tls.Config
	streams *agentprotocol.StreamServer

	// handlers tracks in-flight connection goroutines so that Run only
	// reports completion once none of them can still touch the database.
	handlers sync.WaitGroup
	// admissions bounds concurrent connection handling so that a burst of
	// dials cannot exhaust Server memory before authentication completes.
	admissions chan struct{}
	// resourceAdmissions is an instance-wide bound. Per-Agent bounds live on
	// each session, so one busy Cluster cannot consume the whole allowance.
	resourceAdmissions       chan struct{}
	podLogsAdmissions        chan struct{}
	podExecAdmissions        chan struct{}
	podPortForwardAdmissions chan struct{}
	resourceWatchAdmissions  chan struct{}
	helmAdmissions           chan struct{}
	metricsIngestAdmissions  chan struct{}

	mutex                sync.Mutex
	connections          map[string]*session
	connectionsByCluster map[string]*session
	lastDisconnected     map[string]ConnectionStatus
	disconnectOrder      []string
	subscribers          map[uint64]chan ConnectionEvent
	nextSubscriberID     uint64
}

// MetricsScope is the identity the Server binds to every accepted batch. It is
// derived from the mTLS connection and the Server's own records, never from
// the payload or anything the Agent declares: whoever administers a Cluster
// controls what its collector sends, but must not be able to write data under
// another Cluster's identity.
type MetricsScope struct {
	TenantID  string
	ProjectID string
	ClusterID string
	AgentID   string
}

// MetricsSink consumes exactly one batch payload, bounded to size bytes. The
// verdict is a value rather than an error because rejection is a normal
// outcome that travels back to the collector.
type MetricsSink interface {
	IngestMetrics(
		ctx context.Context,
		scope MetricsScope,
		payload io.Reader,
		size uint64,
	) agentprotocol.MetricsIngestResult
}

type managedConnection interface {
	Context() context.Context
	CloseWithError(quic.ApplicationErrorCode, string) error
}

type controlStream interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

type session struct {
	id                       string
	identity                 store.AgentConnectionIdentity
	certificateSerial        string
	certificateExpiresAt     time.Time
	connectedAt              time.Time
	conn                     managedConnection
	business                 *quic.Conn
	stream                   controlStream
	writeTimeout             time.Duration
	writeMu                  sync.Mutex
	statusMu                 sync.Mutex
	lastHeartbeatAt          time.Time
	disconnectReason         string
	capabilities             map[string]struct{}
	resourceAdmissions       chan struct{}
	podLogsAdmissions        chan struct{}
	podExecAdmissions        chan struct{}
	podPortForwardAdmissions chan struct{}
	resourceWatchAdmissions  chan struct{}
	helmAdmissions           chan struct{}
	businessMu               sync.Mutex
	businessInFlight         int
	draining                 bool
	drainOnce                sync.Once
	drainTimer               *time.Timer
	drainFinish              func()
}

type ConnectionStatus struct {
	State                string
	ConnectionID         string
	ConnectedAt          time.Time
	LastHeartbeatAt      time.Time
	LastDisconnectedAt   time.Time
	LastDisconnectReason string
}

type ConnectionEvent struct {
	ID         string
	TenantID   string
	ProjectID  string
	ClusterID  string
	AgentID    string
	State      string
	OccurredAt time.Time
}

const (
	ConnectionStateOnline  = "online"
	ConnectionStateOffline = "offline"

	defaultMaxRememberedDisconnects     = 4096
	defaultSessionWriteTimeout          = 5 * time.Second
	defaultResourceRequestTimeout       = 2 * time.Minute
	defaultConnectionDrainTimeout       = 10 * time.Second
	defaultMaxResourceBodyBytes         = 32 * 1024 * 1024
	defaultMaxResourceStreams           = 64
	defaultMaxResourceRequests          = 4096
	defaultPodLogsRequestTimeout        = 30 * time.Minute
	defaultMaxPodLogBytes               = 16 * 1024 * 1024
	defaultMaxPodLogsStreams            = 8
	defaultMaxPodLogsRequests           = 256
	defaultPodExecRequestTimeout        = 15 * time.Minute
	defaultMaxPodExecInputBytes         = 16 * 1024 * 1024
	defaultMaxPodExecOutputBytes        = 32 * 1024 * 1024
	defaultMaxPodExecStreams            = 4
	defaultMaxPodExecRequests           = 128
	defaultPodPortForwardRequestTimeout = time.Hour
	defaultMaxPodPortForwardClientBytes = agentprotocol.MaxPodPortForwardBytes
	defaultMaxPodPortForwardPodBytes    = agentprotocol.MaxPodPortForwardBytes
	defaultMaxPodPortForwardStreams     = 4
	defaultMaxPodPortForwardRequests    = 128
	defaultHelmRequestTimeout           = 15 * time.Minute
	defaultMaxHelmStreams               = 1
	defaultMaxHelmRequests              = 64
	defaultResourceWatchRequestTimeout  = 30 * time.Minute
	defaultMaxResourceWatchStreams      = 16
	defaultMaxResourceWatchRequests     = 512
	defaultMetricsIngestTimeout         = agentprotocol.DefaultMetricsIngestTimeout
	defaultMaxMetricsBatchBytes         = agentprotocol.DefaultMaxMetricsBatchBytes
	// One long-lived Stream per collecting Agent, so this bounds how many
	// Clusters may ship metrics at once rather than how bursty one of them is.
	defaultMaxMetricsIngestStreams = 512

	// Bounds for re-establishing the revocation watch after the listening
	// PostgreSQL connection drops. The ceiling is kept well under the
	// heartbeat persistence interval so that the immediate path is restored
	// long before the fallback matters.
	revocationRetryInitialInterval = time.Second
	revocationRetryMaxInterval     = 30 * time.Second
)

type certificateIdentity struct {
	store.AgentConnectionIdentity
	CertificateSerial    string
	CertificateExpiresAt time.Time
}

var (
	ErrAgentNotConnected                 = errors.New("target Cluster Agent is not connected")
	ErrResourceCapabilityMissing         = errors.New("target Cluster Agent does not support Resource Streams")
	ErrResourceRequestExhausted          = errors.New("Resource Stream request capacity is exhausted")
	ErrResourceVerbUnsupported           = errors.New("Resource Stream verb is not implemented")
	ErrPodLogsCapabilityMissing          = errors.New("target Cluster Agent does not support Pod Logs Streams")
	ErrPodLogsRequestExhausted           = errors.New("Pod Logs Stream request capacity is exhausted")
	ErrPodExecCapabilityMissing          = errors.New("target Cluster Agent does not support Pod Exec Streams")
	ErrTerminalCommandCapabilityMissing  = errors.New("target Cluster Agent does not support Terminal commands")
	ErrPodExecRequestExhausted           = errors.New("Pod Exec Stream request capacity is exhausted")
	ErrPodPortForwardCapabilityMissing   = errors.New("target Cluster Agent does not support Pod Port Forward Streams")
	ErrPodPortForwardRequestExhausted    = errors.New("Pod Port Forward Stream request capacity is exhausted")
	ErrResourceWatchCapabilityMissing    = errors.New("target Cluster Agent does not support Resource Watch Streams")
	ErrResourceWatchRequestExhausted     = errors.New("Resource Watch Stream request capacity is exhausted")
	ErrTerminalSessionCapabilityMissing  = errors.New("target Cluster Agent does not support Terminal Session Streams")
	ErrMetricsCollectorCapabilityMissing = errors.New("target Cluster Agent does not support metrics collector management")
	ErrHelmCapabilityMissing             = errors.New("target Cluster Agent does not support Helm release management")
	ErrHelmRequestExhausted              = errors.New("Helm Stream request capacity is exhausted")
)

func New(
	config Config,
	logger *slog.Logger,
	connectionStore ConnectionStore,
	renewalService *enrollment.CertificateRenewalService,
) (*Manager, error) {
	tlsConfig, err := loadTLSConfig(config)
	if err != nil {
		return nil, err
	}
	if config.ResourceRequestTimeout <= 0 {
		config.ResourceRequestTimeout = defaultResourceRequestTimeout
	}
	if config.ConnectionDrainTimeout <= 0 {
		config.ConnectionDrainTimeout = defaultConnectionDrainTimeout
	}
	if config.MaxResourceBodyBytes == 0 {
		config.MaxResourceBodyBytes = defaultMaxResourceBodyBytes
	}
	if config.MaxResourceStreams <= 0 {
		config.MaxResourceStreams = defaultMaxResourceStreams
	}
	if config.MaxResourceRequests <= 0 {
		config.MaxResourceRequests = defaultMaxResourceRequests
	}
	if config.HelmRequestTimeout <= 0 {
		config.HelmRequestTimeout = defaultHelmRequestTimeout
	}
	if config.MaxHelmStreams <= 0 {
		config.MaxHelmStreams = defaultMaxHelmStreams
	}
	if config.MaxHelmRequests <= 0 {
		config.MaxHelmRequests = defaultMaxHelmRequests
	}
	if config.PodLogsRequestTimeout <= 0 {
		config.PodLogsRequestTimeout = defaultPodLogsRequestTimeout
	}
	if config.MaxPodLogBytes == 0 {
		config.MaxPodLogBytes = defaultMaxPodLogBytes
	}
	if config.MaxPodLogsStreams <= 0 {
		config.MaxPodLogsStreams = defaultMaxPodLogsStreams
	}
	if config.MaxPodLogsRequests <= 0 {
		config.MaxPodLogsRequests = defaultMaxPodLogsRequests
	}
	if config.PodExecRequestTimeout <= 0 {
		config.PodExecRequestTimeout = defaultPodExecRequestTimeout
	}
	if config.MaxPodExecInputBytes == 0 {
		config.MaxPodExecInputBytes = defaultMaxPodExecInputBytes
	}
	if config.MaxPodExecOutputBytes == 0 {
		config.MaxPodExecOutputBytes = defaultMaxPodExecOutputBytes
	}
	if config.MaxPodExecStreams <= 0 {
		config.MaxPodExecStreams = defaultMaxPodExecStreams
	}
	if config.MaxPodExecRequests <= 0 {
		config.MaxPodExecRequests = defaultMaxPodExecRequests
	}
	if config.PodPortForwardRequestTimeout <= 0 {
		config.PodPortForwardRequestTimeout = defaultPodPortForwardRequestTimeout
	}
	if config.MaxPodPortForwardClientBytes == 0 {
		config.MaxPodPortForwardClientBytes = defaultMaxPodPortForwardClientBytes
	}
	if config.MaxPodPortForwardPodBytes == 0 {
		config.MaxPodPortForwardPodBytes = defaultMaxPodPortForwardPodBytes
	}
	if config.MaxPodPortForwardStreams <= 0 {
		config.MaxPodPortForwardStreams = defaultMaxPodPortForwardStreams
	}
	if config.MaxPodPortForwardRequests <= 0 {
		config.MaxPodPortForwardRequests = defaultMaxPodPortForwardRequests
	}
	if config.ResourceWatchRequestTimeout <= 0 {
		config.ResourceWatchRequestTimeout = defaultResourceWatchRequestTimeout
	}
	if config.MaxResourceWatchStreams <= 0 {
		config.MaxResourceWatchStreams = defaultMaxResourceWatchStreams
	}
	if config.MaxResourceWatchRequests <= 0 {
		config.MaxResourceWatchRequests = defaultMaxResourceWatchRequests
	}
	if config.MetricsIngestTimeout <= 0 {
		config.MetricsIngestTimeout = defaultMetricsIngestTimeout
	}
	if config.MaxMetricsBatchBytes == 0 {
		config.MaxMetricsBatchBytes = defaultMaxMetricsBatchBytes
	}
	if config.MaxMetricsBatchBytes > agentprotocol.MaxMetricsBatchBytesCeiling {
		return nil, fmt.Errorf(
			"metrics batch limit exceeds the protocol ceiling of %d bytes",
			agentprotocol.MaxMetricsBatchBytesCeiling,
		)
	}
	if config.MaxMetricsIngestStreams <= 0 {
		config.MaxMetricsIngestStreams = defaultMaxMetricsIngestStreams
	}
	manager := &Manager{
		config:     config,
		logger:     logger,
		store:      connectionStore,
		renewal:    renewalService,
		tls:        tlsConfig,
		admissions: make(chan struct{}, max(1, config.MaxConcurrentAgents)),
		resourceAdmissions: make(
			chan struct{},
			config.MaxResourceRequests,
		),
		podLogsAdmissions:        make(chan struct{}, config.MaxPodLogsRequests),
		podExecAdmissions:        make(chan struct{}, config.MaxPodExecRequests),
		podPortForwardAdmissions: make(chan struct{}, config.MaxPodPortForwardRequests),
		resourceWatchAdmissions:  make(chan struct{}, config.MaxResourceWatchRequests),
		helmAdmissions:           make(chan struct{}, config.MaxHelmRequests),
		metricsIngestAdmissions:  make(chan struct{}, config.MaxMetricsIngestStreams),
		connections:              make(map[string]*session),
		connectionsByCluster:     make(map[string]*session),
		lastDisconnected:         make(map[string]ConnectionStatus),
		subscribers:              make(map[uint64]chan ConnectionEvent),
	}
	streamServer, err := manager.newStreamServer(nil)
	if err != nil {
		return nil, err
	}
	manager.streams = streamServer
	return manager, nil
}

// newStreamServer builds the accept-side dispatcher for one Connection.
// Handlers that need the Connection's identity are bound here, because the
// shared dispatcher has no way to tell one Agent from another once a Stream
// reaches a handler. Passing a nil scope produces the dispatcher used before a
// Connection has an identity, and by Agents that negotiated no ingest.
func (manager *Manager) newStreamServer(
	scope *MetricsScope,
) (*agentprotocol.StreamServer, error) {
	logger := manager.logger
	handlers := manager.incomingStreamHandlers(scope)
	return agentprotocol.NewStreamServer(
		agentprotocol.StreamServerConfig{
			HeaderTimeout: manager.config.HandshakeTimeout,
			MaxTimeout: max(
				manager.config.ResourceRequestTimeout,
				manager.config.PodExecRequestTimeout,
				manager.config.PodPortForwardRequestTimeout,
				manager.config.MetricsIngestTimeout,
			),
			Handlers: handlers,
			OnError: func(header *agentv1.StreamHeader, err error) {
				attributes := []any{slog.String("error", err.Error())}
				if header != nil {
					attributes = append(
						attributes,
						slog.String("request_id", header.GetRequestId()),
						slog.String("stream_kind", header.GetKind().String()),
					)
				}
				logger.Debug("Server business Stream stopped", attributes...)
			},
		},
	)
}

// incomingStreamHandlers reports what this Server accepts from one Connection.
// Metrics ingest is the only entry today, and it appears only when both sides
// can hold up their end: the Agent negotiated the capability (a non-nil scope)
// and this deployment has somewhere to put the samples.
func (manager *Manager) incomingStreamHandlers(
	scope *MetricsScope,
) map[agentv1.StreamKind]agentprotocol.StreamHandlerConfig {
	handlers := map[agentv1.StreamKind]agentprotocol.StreamHandlerConfig{}
	if scope == nil || manager.config.MetricsSink == nil {
		return handlers
	}
	handlers[agentv1.StreamKind_STREAM_KIND_METRICS_INGEST] =
		agentprotocol.StreamHandlerConfig{
			// One ingest Stream per Connection. A second one is a protocol
			// violation, not extra capacity.
			MaxConcurrent: 1,
			MaxTimeout:    manager.config.MetricsIngestTimeout,
			Handle:        manager.metricsIngestHandler(*scope),
		}
	return handlers
}

// metricsIngestHandler binds one Connection's scope to the ingest Stream and
// adds the instance-wide admission the per-Connection dispatcher cannot see.
func (manager *Manager) metricsIngestHandler(
	scope MetricsScope,
) agentprotocol.IncomingStreamHandler {
	handle := agentprotocol.MetricsIngestStreamHandler(
		manager.config.MaxMetricsBatchBytes,
		func(
			ctx context.Context,
			batch *agentv1.MetricsIngestBatch,
			payload io.Reader,
		) agentprotocol.MetricsIngestResult {
			return manager.config.MetricsSink.IngestMetrics(
				ctx,
				scope,
				payload,
				batch.GetPayloadSize(),
			)
		},
	)
	return func(
		ctx context.Context,
		stream *quic.Stream,
		header *agentv1.StreamHeader,
	) error {
		select {
		case manager.metricsIngestAdmissions <- struct{}{}:
			defer func() { <-manager.metricsIngestAdmissions }()
		default:
			return &agentprotocol.StreamFailure{
				Code: agentprotocol.StreamErrorResourceExhausted,
				Err:  agentprotocol.ErrStreamResourceExhausted,
			}
		}
		return handle(ctx, stream, header)
	}
}

func (manager *Manager) Run(ctx context.Context) error {
	runContext, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	listener, err := quic.ListenAddr(
		manager.config.Address,
		manager.tls,
		&quic.Config{
			HandshakeIdleTimeout:  manager.config.HandshakeTimeout,
			MaxIdleTimeout:        manager.config.HeartbeatTimeout,
			KeepAlivePeriod:       manager.config.HeartbeatInterval,
			MaxIncomingStreams:    manager.config.MaxIncomingStreams,
			MaxIncomingUniStreams: -1,
			Allow0RTT:             false,
		},
	)
	if err != nil {
		return fmt.Errorf("listen for Agent QUIC connections: %w", err)
	}
	defer listener.Close()

	revocationErrors := make(chan error, 1)
	revocationsReady := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Add(1)
	go func() {
		defer watcher.Done()
		revocationErrors <- manager.superviseRevocations(
			runContext,
			revocationsReady,
		)
		cancelRun()
	}()
	// Every exit path must drain the connection handlers and the revocation
	// watcher before returning: the caller closes the database pool as soon
	// as Run reports completion, and an in-flight heartbeat write would then
	// fail against a closed pool.
	defer func() {
		cancelRun()
		manager.closeAll()
		manager.handlers.Wait()
		watcher.Wait()
	}()

	select {
	case <-revocationsReady:
	case revocationErr := <-revocationErrors:
		return revocationErr
	case <-ctx.Done():
		return nil
	}

	manager.logger.Info(
		"Agent QUIC listener starting",
		slog.String("address", listener.Addr().String()),
	)
	for {
		connection, err := listener.Accept(runContext)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			select {
			case revocationErr := <-revocationErrors:
				return revocationErr
			default:
			}
			return fmt.Errorf("accept Agent QUIC connection: %w", err)
		}
		manager.handlers.Add(1)
		go func() {
			defer manager.handlers.Done()
			manager.handleConnection(runContext, connection)
		}()
	}
}

func (manager *Manager) handleConnection(parent context.Context, connection *quic.Conn) {
	// Admission is checked before any work is done for the connection so
	// that a dial burst cannot allocate unbounded Server state. A rejected
	// Agent reconnects under its own backoff.
	select {
	case manager.admissions <- struct{}{}:
		defer func() { <-manager.admissions }()
	default:
		manager.reject(
			connection,
			agentprotocol.CloseInternalError,
			"Agent connection capacity reached",
			errors.New("concurrent Agent connection limit reached"),
		)
		return
	}

	identity, err := identityFromConnection(connection)
	if err != nil {
		manager.reject(connection, agentprotocol.CloseAuthenticationError, "invalid Agent identity", err)
		return
	}
	logger := manager.logger.With(
		slog.String("tenant_id", identity.TenantID),
		slog.String("project_id", identity.ProjectID),
		slog.String("cluster_id", identity.ClusterID),
		slog.String("agent_id", identity.AgentID),
	)

	handshakeContext, cancelHandshake := context.WithTimeout(
		parent,
		manager.config.HandshakeTimeout,
	)
	controlStream, err := connection.AcceptStream(handshakeContext)
	cancelHandshake()
	if err != nil {
		manager.reject(connection, agentprotocol.CloseProtocolError, "control stream required", err)
		return
	}
	if err := controlStream.SetDeadline(time.Now().Add(manager.config.HandshakeTimeout)); err != nil {
		manager.reject(connection, agentprotocol.CloseProtocolError, "control stream deadline failed", err)
		return
	}
	frame, err := agentprotocol.ReadFrame(controlStream)
	if err != nil {
		manager.reject(connection, agentprotocol.CloseProtocolError, "invalid ClientHello frame", err)
		return
	}
	hello := frame.GetClientHello()
	if err := validateClientHello(frame.GetProtocolVersion(), hello, identity); err != nil {
		manager.reject(connection, agentprotocol.CloseAuthenticationError, "ClientHello rejected", err)
		return
	}

	now := time.Now().UTC()
	operationContext, cancelOperation := context.WithTimeout(
		parent,
		manager.config.OperationTimeout,
	)
	err = manager.store.Activate(operationContext, store.ActivateAgentConnectionParams{
		Identity:          identity.AgentConnectionIdentity,
		CertificateSerial: identity.CertificateSerial,
		AgentVersion:      hello.GetAgentVersion(),
		ProtocolVersion:   agentprotocol.ProtocolVersionLabel,
		HealthStatus:      "healthy",
		Now:               now,
	})
	cancelOperation()
	if err != nil {
		if errors.Is(err, store.ErrAgentScopeSuspended) {
			manager.reject(
				connection,
				agentprotocol.CloseScopeSuspended,
				"Agent scope suspended",
				err,
			)
			return
		}
		manager.reject(connection, agentprotocol.CloseAuthenticationError, "Agent credential rejected", err)
		return
	}

	connectionID, err := identifier.NewUUID()
	if err != nil {
		manager.reject(connection, agentprotocol.CloseProtocolError, "connection identifier unavailable", err)
		return
	}
	current := &session{
		id:                   connectionID,
		identity:             identity.AgentConnectionIdentity,
		certificateSerial:    identity.CertificateSerial,
		certificateExpiresAt: identity.CertificateExpiresAt,
		connectedAt:          now,
		conn:                 connection,
		business:             connection,
		stream:               controlStream,
		writeTimeout:         manager.config.WriteTimeout,
		disconnectReason:     "connection_closed",
		capabilities:         make(map[string]struct{}),
		resourceAdmissions: make(
			chan struct{},
			manager.config.MaxResourceStreams,
		),
		podLogsAdmissions: make(
			chan struct{},
			manager.config.MaxPodLogsStreams,
		),
		podExecAdmissions: make(
			chan struct{},
			manager.config.MaxPodExecStreams,
		),
		podPortForwardAdmissions: make(
			chan struct{},
			manager.config.MaxPodPortForwardStreams,
		),
		resourceWatchAdmissions: make(
			chan struct{},
			manager.config.MaxResourceWatchStreams,
		),
		helmAdmissions: make(
			chan struct{},
			manager.config.MaxHelmStreams,
		),
	}
	serverCapabilities := []string{
		agentprotocol.CapabilityCertificateRenewal,
	}
	if hasCapability(
		hello.GetCapabilities(),
		agentprotocol.CapabilityResourceV1,
	) {
		serverCapabilities = append(
			serverCapabilities,
			agentprotocol.CapabilityResourceV1,
		)
		current.capabilities[agentprotocol.CapabilityResourceV1] = struct{}{}
		if hasCapability(
			hello.GetCapabilities(),
			agentprotocol.CapabilityResourceDiscoveryV1,
		) {
			serverCapabilities = append(
				serverCapabilities,
				agentprotocol.CapabilityResourceDiscoveryV1,
			)
			current.capabilities[agentprotocol.CapabilityResourceDiscoveryV1] =
				struct{}{}
		}
		if hasCapability(
			hello.GetCapabilities(),
			agentprotocol.CapabilityResourceWriteV1,
		) {
			serverCapabilities = append(
				serverCapabilities,
				agentprotocol.CapabilityResourceWriteV1,
			)
			current.capabilities[agentprotocol.CapabilityResourceWriteV1] =
				struct{}{}
		}
	}
	if hasCapability(hello.GetCapabilities(), agentprotocol.CapabilityPodLogsV1) {
		serverCapabilities = append(
			serverCapabilities,
			agentprotocol.CapabilityPodLogsV1,
		)
		current.capabilities[agentprotocol.CapabilityPodLogsV1] = struct{}{}
	}
	if hasCapability(hello.GetCapabilities(), agentprotocol.CapabilityPodExecV1) {
		serverCapabilities = append(
			serverCapabilities,
			agentprotocol.CapabilityPodExecV1,
		)
		current.capabilities[agentprotocol.CapabilityPodExecV1] = struct{}{}
	}
	if hasCapability(hello.GetCapabilities(), agentprotocol.CapabilityPodPortForwardV1) {
		serverCapabilities = append(
			serverCapabilities,
			agentprotocol.CapabilityPodPortForwardV1,
		)
		current.capabilities[agentprotocol.CapabilityPodPortForwardV1] = struct{}{}
	}
	if hasCapability(hello.GetCapabilities(), agentprotocol.CapabilityResourceWatchV1) {
		serverCapabilities = append(serverCapabilities, agentprotocol.CapabilityResourceWatchV1)
		current.capabilities[agentprotocol.CapabilityResourceWatchV1] = struct{}{}
	}
	if hasCapability(hello.GetCapabilities(), agentprotocol.CapabilityTerminalSessionV1) {
		serverCapabilities = append(serverCapabilities, agentprotocol.CapabilityTerminalSessionV1)
		current.capabilities[agentprotocol.CapabilityTerminalSessionV1] = struct{}{}
	}
	if hasCapability(hello.GetCapabilities(), agentprotocol.CapabilityTerminalCommandV1) {
		serverCapabilities = append(serverCapabilities, agentprotocol.CapabilityTerminalCommandV1)
		current.capabilities[agentprotocol.CapabilityTerminalCommandV1] = struct{}{}
	}
	if hasCapability(hello.GetCapabilities(), agentprotocol.CapabilityHelmV1) {
		serverCapabilities = append(serverCapabilities, agentprotocol.CapabilityHelmV1)
		current.capabilities[agentprotocol.CapabilityHelmV1] = struct{}{}
	}
	if hasCapability(hello.GetCapabilities(), agentprotocol.CapabilityHelmProgressV1) {
		serverCapabilities = append(
			serverCapabilities,
			agentprotocol.CapabilityHelmProgressV1,
		)
		current.capabilities[agentprotocol.CapabilityHelmProgressV1] = struct{}{}
	}
	// Managing the collector is offered only when this Server has storage: an
	// installed collector with nowhere to send data is worse than none.
	if manager.config.MetricsSink != nil &&
		hasCapability(hello.GetCapabilities(), agentprotocol.CapabilityMetricsCollectorV1) {
		serverCapabilities = append(serverCapabilities, agentprotocol.CapabilityMetricsCollectorV1)
		current.capabilities[agentprotocol.CapabilityMetricsCollectorV1] = struct{}{}
	}
	// Metrics Ingest is only offered when this Server can actually store what
	// arrives. Advertising it without a sink would invite an Agent to hold a
	// Stream open and have every batch refused.
	connectionStreams := manager.streams
	if manager.config.MetricsSink != nil &&
		hasCapability(hello.GetCapabilities(), agentprotocol.CapabilityMetricsIngestV1) {
		scope := MetricsScope{
			TenantID:  identity.TenantID,
			ProjectID: identity.ProjectID,
			ClusterID: identity.ClusterID,
			AgentID:   identity.AgentID,
		}
		scopedStreams, err := manager.newStreamServer(&scope)
		if err != nil {
			manager.reject(
				connection,
				agentprotocol.CloseInternalError,
				"metrics ingest dispatcher unavailable",
				err,
			)
			return
		}
		connectionStreams = scopedStreams
		serverCapabilities = append(
			serverCapabilities,
			agentprotocol.CapabilityMetricsIngestV1,
		)
		current.capabilities[agentprotocol.CapabilityMetricsIngestV1] = struct{}{}
	}
	previous := manager.register(current)
	if previous != nil {
		previous.startDrain(
			manager.config.ConnectionDrainTimeout,
			func() {
				previous.setDisconnectReason(
					agentprotocol.GoAwayConnectionReplaced,
				)
				_ = previous.write(&agentv1.ControlFrame{
					ProtocolVersion: agentprotocol.ProtocolVersion,
					Message: &agentv1.ControlFrame_GoAway{
						GoAway: &agentv1.GoAway{
							Reason: agentprotocol.GoAwayConnectionReplaced,
						},
					},
				})
				_ = previous.conn.CloseWithError(
					agentprotocol.CloseConnectionReplaced,
					"connection replaced",
				)
			},
		)
	}
	defer manager.unregister(current)

	if err := current.write(&agentv1.ControlFrame{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		Message: &agentv1.ControlFrame_ServerHello{
			ServerHello: &agentv1.ServerHello{
				ConnectionId:            connectionID,
				ServerTimeUnixMilli:     now.UnixMilli(),
				HeartbeatIntervalMillis: uint64(manager.config.HeartbeatInterval.Milliseconds()),
				HeartbeatTimeoutMillis:  uint64(manager.config.HeartbeatTimeout.Milliseconds()),
				Capabilities:            serverCapabilities,
			},
		},
	}); err != nil {
		manager.reject(connection, agentprotocol.CloseProtocolError, "write ServerHello", err)
		return
	}
	if err := controlStream.SetDeadline(time.Time{}); err != nil {
		manager.reject(connection, agentprotocol.CloseProtocolError, "clear control stream deadline", err)
		return
	}
	businessContext, cancelBusiness := context.WithCancel(parent)
	businessErrors := make(chan error, 1)
	businessDone := make(chan struct{})
	go func() {
		defer close(businessDone)
		err := connectionStreams.Serve(businessContext, connection)
		businessErrors <- err
		if err != nil && businessContext.Err() == nil {
			_ = connection.CloseWithError(
				agentprotocol.CloseInternalError,
				"business Stream accept loop stopped",
			)
		}
	}()

	logger.Info(
		"Agent connection established",
		slog.String("connection_id", connectionID),
		slog.String("agent_version", hello.GetAgentVersion()),
		slog.String("startup_id", hello.GetStartupId()),
	)
	err = manager.serveControl(parent, current, logger, now)
	if err != nil && parent.Err() == nil && connection.Context().Err() == nil {
		logger.Warn(
			"Agent control stream stopped",
			slog.String("connection_id", connectionID),
			slog.String("error", err.Error()),
		)
	}
	cancelBusiness()
	_ = connection.CloseWithError(agentprotocol.CloseNormal, "connection closed")
	<-businessDone
	select {
	case businessErr := <-businessErrors:
		if businessErr != nil && parent.Err() == nil {
			logger.Warn(
				"Server business Stream accept loop stopped",
				slog.String("connection_id", connectionID),
				slog.String("error", businessErr.Error()),
			)
		}
	default:
	}
	logger.Info(
		"Agent connection closed",
		slog.String("connection_id", connectionID),
	)
}

func (manager *Manager) serveControl(
	ctx context.Context,
	current *session,
	logger *slog.Logger,
	lastPersisted time.Time,
) error {
	var lastSequence uint64
	healthStatus := "healthy"
	for {
		now := time.Now().UTC()
		if !current.certificateExpiresAt.After(now) {
			current.setDisconnectReason("certificate_expired")
			_ = current.conn.CloseWithError(
				agentprotocol.CloseAuthenticationError,
				"Agent certificate expired",
			)
			return errors.New("Agent certificate expired")
		}
		readDeadline := now.Add(manager.config.HeartbeatTimeout)
		if current.certificateExpiresAt.Before(readDeadline) {
			readDeadline = current.certificateExpiresAt
		}
		if err := current.stream.SetReadDeadline(readDeadline); err != nil {
			return err
		}
		frame, err := agentprotocol.ReadFrame(current.stream)
		if err != nil {
			if !current.certificateExpiresAt.After(time.Now().UTC()) {
				current.setDisconnectReason("certificate_expired")
				_ = current.conn.CloseWithError(
					agentprotocol.CloseAuthenticationError,
					"Agent certificate expired",
				)
				return errors.New("Agent certificate expired")
			}
			if ctx.Err() == nil && current.conn.Context().Err() == nil {
				current.setDisconnectReason("heartbeat_timeout")
				_ = current.conn.CloseWithError(
					agentprotocol.CloseHeartbeatTimeout,
					"heartbeat timeout",
				)
			}
			return err
		}
		if frame.GetProtocolVersion() != agentprotocol.ProtocolVersion {
			current.setDisconnectReason("protocol_version_mismatch")
			_ = current.conn.CloseWithError(
				agentprotocol.CloseProtocolError,
				"protocol version mismatch",
			)
			return errors.New("Agent protocol version mismatch")
		}
		if goodbye := frame.GetClientGoodbye(); goodbye != nil {
			current.setDisconnectReason("client_goodbye")
			logger.Info(
				"Agent sent goodbye",
				slog.String("reason", goodbye.GetReason()),
			)
			return nil
		}
		if renewalRequest := frame.GetCertificateRenewalRequest(); renewalRequest != nil {
			if err := manager.renewCertificate(
				ctx,
				current,
				renewalRequest,
			); err != nil {
				current.setDisconnectReason("certificate_renewal_rejected")
				closeCode := agentprotocol.CloseInternalError
				if errors.Is(err, enrollment.ErrScopeSuspended) {
					current.setDisconnectReason(agentprotocol.GoAwayScopeSuspended)
					_ = current.write(&agentv1.ControlFrame{
						ProtocolVersion: agentprotocol.ProtocolVersion,
						Message: &agentv1.ControlFrame_GoAway{
							GoAway: &agentv1.GoAway{
								Reason: agentprotocol.GoAwayScopeSuspended,
							},
						},
					})
					closeCode = agentprotocol.CloseScopeSuspended
				}
				if errors.Is(err, enrollment.ErrInvalidInput) ||
					errors.Is(err, enrollment.ErrCredentialRejected) {
					closeCode = agentprotocol.CloseAuthenticationError
				}
				_ = current.conn.CloseWithError(
					closeCode,
					"Agent certificate renewal rejected",
				)
				return err
			}
			continue
		}
		heartbeat := frame.GetHeartbeat()
		if heartbeat == nil ||
			heartbeat.GetSequence() == 0 ||
			heartbeat.GetSequence() <= lastSequence {
			current.setDisconnectReason("invalid_heartbeat")
			_ = current.conn.CloseWithError(
				agentprotocol.CloseProtocolError,
				"invalid heartbeat",
			)
			return errors.New("Agent heartbeat sequence is invalid")
		}
		nextHealthStatus, err := healthStatusValue(heartbeat.GetHealth())
		if err != nil {
			_ = current.conn.CloseWithError(
				agentprotocol.CloseProtocolError,
				"invalid heartbeat health",
			)
			return err
		}
		lastSequence = heartbeat.GetSequence()
		now = time.Now().UTC()
		current.recordHeartbeat(now)
		if nextHealthStatus != healthStatus ||
			now.Sub(lastPersisted) >= manager.config.LastSeenWriteInterval {
			operationContext, cancelOperation := context.WithTimeout(
				ctx,
				manager.config.OperationTimeout,
			)
			err = manager.store.RecordHeartbeat(
				operationContext,
				store.RecordAgentHeartbeatParams{
					Identity:          current.identity,
					CertificateSerial: current.certificateSerial,
					HealthStatus:      nextHealthStatus,
					Now:               now,
				},
			)
			cancelOperation()
			if err != nil {
				current.setDisconnectReason("heartbeat_persistence_failed")
				switch {
				case errors.Is(err, store.ErrAgentScopeSuspended):
					current.setDisconnectReason(agentprotocol.GoAwayScopeSuspended)
					_ = current.write(&agentv1.ControlFrame{
						ProtocolVersion: agentprotocol.ProtocolVersion,
						Message: &agentv1.ControlFrame_GoAway{
							GoAway: &agentv1.GoAway{
								Reason: agentprotocol.GoAwayScopeSuspended,
							},
						},
					})
					_ = current.conn.CloseWithError(
						agentprotocol.CloseScopeSuspended,
						"Agent scope suspended",
					)
				case errors.Is(err, store.ErrAgentCredentialRejected):
					current.setDisconnectReason("credential_rejected")
					_ = current.conn.CloseWithError(
						agentprotocol.CloseAuthenticationError,
						"Agent credential rejected",
					)
				}
				return fmt.Errorf("persist Agent heartbeat: %w", err)
			}
			lastPersisted = now
			if nextHealthStatus != healthStatus {
				manager.publish(current, ConnectionStateOnline, now)
			}
			healthStatus = nextHealthStatus
		}
		if err := current.write(&agentv1.ControlFrame{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Message: &agentv1.ControlFrame_HeartbeatAck{
				HeartbeatAck: &agentv1.HeartbeatAck{
					Sequence:            lastSequence,
					ServerTimeUnixMilli: now.UnixMilli(),
				},
			},
		}); err != nil {
			current.setDisconnectReason("heartbeat_ack_failed")
			return err
		}
	}
}

func (manager *Manager) renewCertificate(
	ctx context.Context,
	current *session,
	request *agentv1.CertificateRenewalRequest,
) error {
	if manager.renewal == nil || request == nil {
		return errors.New("Agent certificate renewal is unavailable")
	}
	operationContext, cancelOperation := context.WithTimeout(
		ctx,
		manager.config.OperationTimeout,
	)
	defer cancelOperation()
	result, err := manager.renewal.Renew(
		operationContext,
		enrollment.RenewCertificateInput{
			Identity:                 current.identity,
			CurrentCertificateSerial: current.certificateSerial,
			CSRPEM:                   []byte(request.GetCsrPem()),
			RequestID:                current.id,
			Now:                      time.Now().UTC(),
		},
	)
	if err != nil {
		return err
	}
	return current.write(&agentv1.ControlFrame{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		Message: &agentv1.ControlFrame_CertificateRenewalResponse{
			CertificateRenewalResponse: &agentv1.CertificateRenewalResponse{
				CertificatePem: result.CertificatePEM,
				CertificateExpiresAtUnixMilli: result.CertificateExpiresAt.
					UnixMilli(),
			},
		},
	})
}

func (manager *Manager) register(current *session) *session {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	if manager.connections == nil {
		manager.connections = make(map[string]*session)
	}
	if manager.connectionsByCluster == nil {
		manager.connectionsByCluster = make(map[string]*session)
	}
	previous := manager.connections[current.identity.AgentID]
	manager.connections[current.identity.AgentID] = current
	manager.connectionsByCluster[current.identity.ClusterID] = current
	manager.publishLocked(current, ConnectionStateOnline, current.connectedAt)
	return previous
}

func (manager *Manager) unregister(current *session) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	if manager.connections[current.identity.AgentID] != current {
		return
	}
	agentID := current.identity.AgentID
	delete(manager.connections, agentID)
	if manager.connectionsByCluster[current.identity.ClusterID] == current {
		delete(manager.connectionsByCluster, current.identity.ClusterID)
	}
	manager.rememberDisconnectLocked(
		agentID,
		current.disconnectedStatus(time.Now().UTC()),
	)
	manager.publishLocked(
		current,
		ConnectionStateOffline,
		manager.lastDisconnected[agentID].LastDisconnectedAt,
	)
}

// rememberDisconnectLocked keeps the most recent disconnect reasons available
// for status queries while evicting the oldest entries, so that Agent churn
// cannot grow this map without bound.
func (manager *Manager) rememberDisconnectLocked(
	agentID string,
	status ConnectionStatus,
) {
	if _, exists := manager.lastDisconnected[agentID]; !exists {
		manager.disconnectOrder = append(manager.disconnectOrder, agentID)
	}
	manager.lastDisconnected[agentID] = status

	limit := manager.config.MaxRememberedDisconnects
	if limit <= 0 {
		limit = defaultMaxRememberedDisconnects
	}
	for len(manager.disconnectOrder) > limit {
		oldest := manager.disconnectOrder[0]
		manager.disconnectOrder = manager.disconnectOrder[1:]
		if _, connected := manager.connections[oldest]; !connected {
			delete(manager.lastDisconnected, oldest)
		}
	}
}

func (manager *Manager) Subscribe() (<-chan ConnectionEvent, func()) {
	manager.mutex.Lock()
	if manager.subscribers == nil {
		manager.subscribers = make(map[uint64]chan ConnectionEvent)
	}
	manager.nextSubscriberID++
	id := manager.nextSubscriberID
	channel := make(chan ConnectionEvent, 128)
	manager.subscribers[id] = channel
	manager.mutex.Unlock()
	var once sync.Once
	return channel, func() {
		once.Do(func() {
			manager.mutex.Lock()
			delete(manager.subscribers, id)
			manager.mutex.Unlock()
		})
	}
}

func (manager *Manager) PublishAgentStatusChange(
	tenantID string,
	projectID string,
	clusterID string,
	agentID string,
) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	state := ConnectionStateOffline
	if manager.connections[agentID] != nil {
		state = ConnectionStateOnline
	}
	manager.publishIdentityLocked(
		store.AgentConnectionIdentity{
			TenantID:  tenantID,
			ProjectID: projectID,
			ClusterID: clusterID,
			AgentID:   agentID,
		},
		state,
		time.Now().UTC(),
	)
}

func (manager *Manager) publish(
	current *session,
	state string,
	at time.Time,
) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	manager.publishLocked(current, state, at)
}

func (manager *Manager) publishLocked(
	current *session,
	state string,
	at time.Time,
) {
	manager.publishIdentityLocked(current.identity, state, at)
}

func (manager *Manager) publishIdentityLocked(
	identity store.AgentConnectionIdentity,
	state string,
	at time.Time,
) {
	eventID, err := identifier.NewUUID()
	if err != nil {
		manager.logger.Error(
			"generate Agent connection event ID",
			slog.String("agent_id", identity.AgentID),
			slog.String("error", err.Error()),
		)
		return
	}
	event := ConnectionEvent{
		ID:         eventID,
		TenantID:   identity.TenantID,
		ProjectID:  identity.ProjectID,
		ClusterID:  identity.ClusterID,
		AgentID:    identity.AgentID,
		State:      state,
		OccurredAt: at,
	}
	for id, subscriber := range manager.subscribers {
		select {
		case subscriber <- event:
		default:
			// A subscriber that cannot keep up loses this transition. Say so,
			// because a silently dropped event leaves a Console showing a
			// Cluster state that never converges.
			manager.logger.Warn(
				"Agent connection event dropped for a slow subscriber",
				slog.Uint64("subscriber_id", id),
				slog.String("tenant_id", identity.TenantID),
				slog.String("project_id", identity.ProjectID),
				slog.String("cluster_id", identity.ClusterID),
				slog.String("agent_id", identity.AgentID),
				slog.String("state", state),
			)
		}
	}
}

func (manager *Manager) RequestResource(
	ctx context.Context,
	clusterID string,
	request *agentv1.ResourceRequest,
	requestBody io.Reader,
	responseBody io.Writer,
) (*agentv1.ResourceResponse, error) {
	if request == nil ||
		(request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_LIST &&
			request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_GET &&
			request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER) {
		return nil, ErrResourceVerbUnsupported
	}
	return manager.requestResource(
		ctx,
		clusterID,
		request,
		requestBody,
		responseBody,
		"",
	)
}

func (manager *Manager) RequestResourceMutation(
	ctx context.Context,
	clusterID string,
	request *agentv1.ResourceRequest,
	requestBody io.Reader,
	responseBody io.Writer,
	idempotencyKey string,
) (*agentv1.ResourceResponse, error) {
	if request == nil ||
		(request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_CREATE &&
			request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_UPDATE &&
			request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_PATCH &&
			request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_DELETE) {
		return nil, ErrResourceVerbUnsupported
	}
	return manager.requestResource(
		ctx,
		clusterID,
		request,
		requestBody,
		responseBody,
		idempotencyKey,
	)
}

func (manager *Manager) requestResource(
	ctx context.Context,
	clusterID string,
	request *agentv1.ResourceRequest,
	requestBody io.Reader,
	responseBody io.Writer,
	idempotencyKey string,
) (*agentv1.ResourceResponse, error) {
	if ctx == nil {
		return nil, errors.New("Resource request Context is required")
	}
	if !validation.IsUUID(clusterID) {
		return nil, errors.New("target Cluster ID is invalid")
	}
	manager.mutex.Lock()
	current := manager.connectionsByCluster[clusterID]
	manager.mutex.Unlock()
	if current == nil || current.business == nil {
		return nil, ErrAgentNotConnected
	}
	if !current.beginBusiness() {
		return nil, ErrAgentNotConnected
	}
	defer current.endBusiness()
	if _, supported := current.capabilities[agentprotocol.CapabilityResourceV1]; !supported {
		return nil, ErrResourceCapabilityMissing
	}
	if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER {
		if _, supported := current.capabilities[agentprotocol.CapabilityResourceDiscoveryV1]; !supported {
			return nil, ErrResourceCapabilityMissing
		}
	}
	if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_CREATE ||
		request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_UPDATE ||
		request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_PATCH ||
		request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_DELETE {
		if _, supported := current.capabilities[agentprotocol.CapabilityResourceWriteV1]; !supported {
			return nil, ErrResourceCapabilityMissing
		}
	}
	if !tryAcquire(manager.resourceAdmissions) {
		return nil, ErrResourceRequestExhausted
	}
	defer release(manager.resourceAdmissions)
	if !tryAcquire(current.resourceAdmissions) {
		return nil, ErrResourceRequestExhausted
	}
	defer release(current.resourceAdmissions)

	timeout := manager.config.ResourceRequestTimeout
	if timeout <= 0 {
		timeout = defaultResourceRequestTimeout
	}
	requestContext, cancelRequest := context.WithTimeout(ctx, timeout)
	defer cancelRequest()
	deadline, _ := requestContext.Deadline()
	timeoutMillis := max(int64(1), time.Until(deadline).Milliseconds())
	requestID, err := streamRequestID(ctx)
	if err != nil {
		return nil, err
	}
	return agentprotocol.DoResource(
		requestContext,
		current.business,
		&agentv1.StreamHeader{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Kind:            agentv1.StreamKind_STREAM_KIND_RESOURCE,
			RequestId:       requestID,
			TimeoutMillis:   uint64(timeoutMillis),
			IdempotencyKey:  idempotencyKey,
		},
		request,
		requestBody,
		responseBody,
		manager.config.MaxResourceBodyBytes,
	)
}

func (manager *Manager) RequestTerminalSession(
	ctx context.Context,
	clusterID string,
	request *agentv1.TerminalSessionRequest,
	idempotencyKey string,
) (*agentv1.TerminalSessionResponse, error) {
	if ctx == nil || !validation.IsUUID(clusterID) || request == nil ||
		!validation.IsIdempotencyKey(idempotencyKey) {
		return nil, errors.New("Terminal Session request is invalid")
	}
	manager.mutex.Lock()
	current := manager.connectionsByCluster[clusterID]
	manager.mutex.Unlock()
	if current == nil || current.business == nil || !current.beginBusiness() {
		return nil, ErrAgentNotConnected
	}
	defer current.endBusiness()
	if err := terminalSessionCapabilityError(current.capabilities, request); err != nil {
		return nil, err
	}
	if !tryAcquire(manager.resourceAdmissions) {
		return nil, ErrResourceRequestExhausted
	}
	defer release(manager.resourceAdmissions)
	if !tryAcquire(current.resourceAdmissions) {
		return nil, ErrResourceRequestExhausted
	}
	defer release(current.resourceAdmissions)
	requestContext, cancelRequest := context.WithTimeout(ctx, manager.config.ResourceRequestTimeout)
	defer cancelRequest()
	deadline, _ := requestContext.Deadline()
	requestID, err := streamRequestID(ctx)
	if err != nil {
		return nil, err
	}
	return agentprotocol.DoTerminalSession(requestContext, current.business, &agentv1.StreamHeader{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		Kind:            agentv1.StreamKind_STREAM_KIND_TERMINAL_SESSION,
		RequestId:       requestID,
		TimeoutMillis:   uint64(max(int64(1), time.Until(deadline).Milliseconds())),
		IdempotencyKey:  idempotencyKey,
	}, request)
}

func terminalSessionCapabilityError(
	capabilities map[string]struct{},
	request *agentv1.TerminalSessionRequest,
) error {
	if _, supported := capabilities[agentprotocol.CapabilityTerminalSessionV1]; !supported {
		return ErrTerminalSessionCapabilityMissing
	}
	// credential_proxy is the AIOps command-session shape introduced together
	// with terminal-command.v1. An older Agent would ignore the unknown proto
	// field and create a token-mounted interactive Pod, only for command exec to
	// fail afterwards. Reject before sending CREATE so version skew cannot create
	// the wrong security shape or leave needless temporary resources.
	if request.GetCredentialProxy() {
		if _, supported := capabilities[agentprotocol.CapabilityTerminalCommandV1]; !supported {
			return ErrTerminalCommandCapabilityMissing
		}
	}
	return nil
}

// RequestMetricsCollector asks one Cluster's Agent to install, remove or
// describe its metrics collector. The Server sends the desired configuration,
// never the objects: the Agent owns their shape.
func (manager *Manager) RequestMetricsCollector(
	ctx context.Context,
	clusterID string,
	request *agentv1.MetricsCollectorRequest,
) (*agentv1.MetricsCollectorResponse, error) {
	if ctx == nil || !validation.IsUUID(clusterID) || request == nil {
		return nil, errors.New("metrics collector request is invalid")
	}
	manager.mutex.Lock()
	current := manager.connectionsByCluster[clusterID]
	manager.mutex.Unlock()
	if current == nil || current.business == nil || !current.beginBusiness() {
		return nil, ErrAgentNotConnected
	}
	defer current.endBusiness()
	if _, supported := current.capabilities[agentprotocol.CapabilityMetricsCollectorV1]; !supported {
		return nil, ErrMetricsCollectorCapabilityMissing
	}
	if !tryAcquire(manager.resourceAdmissions) {
		return nil, ErrResourceRequestExhausted
	}
	defer release(manager.resourceAdmissions)
	if !tryAcquire(current.resourceAdmissions) {
		return nil, ErrResourceRequestExhausted
	}
	defer release(current.resourceAdmissions)
	requestContext, cancelRequest := context.WithTimeout(
		ctx,
		manager.config.ResourceRequestTimeout,
	)
	defer cancelRequest()
	deadline, _ := requestContext.Deadline()
	requestID, err := streamRequestID(ctx)
	if err != nil {
		return nil, err
	}
	return agentprotocol.DoMetricsCollector(
		requestContext,
		current.business,
		&agentv1.StreamHeader{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Kind:            agentv1.StreamKind_STREAM_KIND_METRICS_COLLECTOR,
			RequestId:       requestID,
			TimeoutMillis:   uint64(max(int64(1), time.Until(deadline).Milliseconds())),
		},
		request,
	)
}

// RequestHelm asks one Cluster's Agent to run a Helm operation.
//
// The Server sends the chart and the values; the Agent runs Helm. An Agent that
// does not advertise the capability is refused rather than fallen back on:
// there is no second way to write a release that would not corrupt the history
// the real `helm` client reads.
func (manager *Manager) RequestHelm(
	ctx context.Context,
	clusterID string,
	request *agentv1.HelmRequest,
	values io.Reader,
	chart io.Reader,
	report io.Writer,
	idempotencyKey string,
	progress func(*agentv1.HelmProgress),
) (*agentv1.HelmResponse, error) {
	if ctx == nil || !validation.IsUUID(clusterID) || request == nil {
		return nil, errors.New("Helm request is invalid")
	}
	if idempotencyKey != "" && !validation.IsIdempotencyKey(idempotencyKey) {
		return nil, errors.New("Helm idempotency key is invalid")
	}
	manager.mutex.Lock()
	current := manager.connectionsByCluster[clusterID]
	manager.mutex.Unlock()
	if current == nil || current.business == nil || !current.beginBusiness() {
		return nil, ErrAgentNotConnected
	}
	defer current.endBusiness()
	if _, supported := current.capabilities[agentprotocol.CapabilityHelmV1]; !supported {
		return nil, ErrHelmCapabilityMissing
	}
	// Asking for progress changes how the answer is framed, so it is asked for
	// only when this Agent said it understands the question. An Agent that did
	// not still runs the operation; what is missing is the account of it while
	// it runs, not the operation.
	_, streamsProgress := current.capabilities[agentprotocol.CapabilityHelmProgressV1]
	request.StreamProgress = streamsProgress && progress != nil
	if !tryAcquire(manager.helmAdmissions) {
		return nil, ErrHelmRequestExhausted
	}
	defer release(manager.helmAdmissions)
	// The per-Agent bound defaults to one, so a second release change on the
	// same Cluster is refused while the first is running rather than queued
	// behind it: the caller is an HTTP request with its own deadline, and
	// telling it to try again is more useful than holding it open.
	if !tryAcquire(current.helmAdmissions) {
		return nil, ErrHelmRequestExhausted
	}
	defer release(current.helmAdmissions)
	requestContext, cancelRequest := context.WithTimeout(
		ctx,
		manager.config.HelmRequestTimeout,
	)
	defer cancelRequest()
	deadline, _ := requestContext.Deadline()
	requestID, err := streamRequestID(ctx)
	if err != nil {
		return nil, err
	}
	return agentprotocol.DoHelm(
		requestContext,
		current.business,
		&agentv1.StreamHeader{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Kind:            agentv1.StreamKind_STREAM_KIND_HELM,
			RequestId:       requestID,
			TimeoutMillis:   uint64(max(int64(1), time.Until(deadline).Milliseconds())),
			IdempotencyKey:  idempotencyKey,
		},
		request,
		values,
		chart,
		report,
		progress,
	)
}

func (manager *Manager) RequestPodLogs(
	ctx context.Context,
	clusterID string,
	request *agentv1.PodLogsRequest,
	destination io.Writer,
) (*agentv1.PodLogsResponse, *agentv1.PodLogsTrailer, error) {
	if ctx == nil {
		return nil, nil, errors.New("Pod logs request Context is required")
	}
	if !validation.IsUUID(clusterID) {
		return nil, nil, errors.New("target Cluster ID is invalid")
	}
	manager.mutex.Lock()
	current := manager.connectionsByCluster[clusterID]
	manager.mutex.Unlock()
	if current == nil || current.business == nil {
		return nil, nil, ErrAgentNotConnected
	}
	if !current.beginBusiness() {
		return nil, nil, ErrAgentNotConnected
	}
	defer current.endBusiness()
	if _, supported := current.capabilities[agentprotocol.CapabilityPodLogsV1]; !supported {
		return nil, nil, ErrPodLogsCapabilityMissing
	}
	if !tryAcquire(manager.podLogsAdmissions) {
		return nil, nil, ErrPodLogsRequestExhausted
	}
	defer release(manager.podLogsAdmissions)
	if !tryAcquire(current.podLogsAdmissions) {
		return nil, nil, ErrPodLogsRequestExhausted
	}
	defer release(current.podLogsAdmissions)

	timeout := manager.config.PodLogsRequestTimeout
	if timeout <= 0 {
		timeout = defaultPodLogsRequestTimeout
	}
	requestContext, cancelRequest := context.WithTimeout(ctx, timeout)
	defer cancelRequest()
	deadline, _ := requestContext.Deadline()
	timeoutMillis := max(int64(1), time.Until(deadline).Milliseconds())
	requestID, err := streamRequestID(ctx)
	if err != nil {
		return nil, nil, err
	}
	return agentprotocol.DoPodLogs(
		requestContext,
		current.business,
		&agentv1.StreamHeader{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Kind:            agentv1.StreamKind_STREAM_KIND_POD_LOGS,
			RequestId:       requestID,
			TimeoutMillis:   uint64(timeoutMillis),
		},
		request,
		destination,
		manager.config.MaxPodLogBytes,
	)
}

func (manager *Manager) RequestPodExec(
	ctx context.Context,
	clusterID string,
	request *agentv1.PodExecRequest,
	peer agentprotocol.PodExecPeer,
) (*agentv1.PodExecResponse, *agentv1.PodExecExit, error) {
	return manager.requestPodExec(
		ctx, clusterID, request, peer,
		agentprotocol.CapabilityPodExecV1, ErrPodExecCapabilityMissing,
	)
}

// RequestTerminalCommand uses the Pod Exec transport but requires a separate
// negotiated capability. Older Agents understand interactive Pod Exec yet do
// not know the command field, so treating pod-exec.v1 as sufficient would turn
// a requested bounded command into an interactive shell by version skew.
func (manager *Manager) RequestTerminalCommand(
	ctx context.Context,
	clusterID string,
	request *agentv1.PodExecRequest,
	peer agentprotocol.PodExecPeer,
) (*agentv1.PodExecResponse, *agentv1.PodExecExit, error) {
	return manager.requestPodExec(
		ctx, clusterID, request, peer,
		agentprotocol.CapabilityTerminalCommandV1, ErrTerminalCommandCapabilityMissing,
	)
}

func (manager *Manager) requestPodExec(
	ctx context.Context,
	clusterID string,
	request *agentv1.PodExecRequest,
	peer agentprotocol.PodExecPeer,
	requiredCapability string,
	capabilityError error,
) (*agentv1.PodExecResponse, *agentv1.PodExecExit, error) {
	if ctx == nil || !validation.IsUUID(clusterID) {
		return nil, nil, errors.New("Pod Exec request Context or target Cluster ID is invalid")
	}
	manager.mutex.Lock()
	current := manager.connectionsByCluster[clusterID]
	manager.mutex.Unlock()
	if current == nil || current.business == nil || !current.beginBusiness() {
		return nil, nil, ErrAgentNotConnected
	}
	defer current.endBusiness()
	if _, supported := current.capabilities[requiredCapability]; !supported {
		return nil, nil, capabilityError
	}
	if !tryAcquire(manager.podExecAdmissions) {
		return nil, nil, ErrPodExecRequestExhausted
	}
	defer release(manager.podExecAdmissions)
	if !tryAcquire(current.podExecAdmissions) {
		return nil, nil, ErrPodExecRequestExhausted
	}
	defer release(current.podExecAdmissions)

	requestContext, cancelRequest := context.WithTimeout(ctx, manager.config.PodExecRequestTimeout)
	defer cancelRequest()
	deadline, _ := requestContext.Deadline()
	timeoutMillis := max(int64(1), time.Until(deadline).Milliseconds())
	requestID, err := streamRequestID(ctx)
	if err != nil {
		return nil, nil, err
	}
	return agentprotocol.DoPodExec(
		requestContext,
		current.business,
		&agentv1.StreamHeader{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Kind:            agentv1.StreamKind_STREAM_KIND_POD_EXEC,
			RequestId:       requestID,
			TimeoutMillis:   uint64(timeoutMillis),
		},
		request,
		peer,
		manager.config.MaxPodExecInputBytes,
		manager.config.MaxPodExecOutputBytes,
	)
}

func (manager *Manager) RequestPodPortForward(
	ctx context.Context,
	clusterID string,
	request *agentv1.PodPortForwardRequest,
	peer agentprotocol.PodPortForwardPeer,
) (*agentv1.PodPortForwardResponse, *agentv1.PodPortForwardExit, error) {
	if ctx == nil || !validation.IsUUID(clusterID) {
		return nil, nil, errors.New("Pod Port Forward request Context or target Cluster ID is invalid")
	}
	manager.mutex.Lock()
	current := manager.connectionsByCluster[clusterID]
	manager.mutex.Unlock()
	if current == nil || current.business == nil || !current.beginBusiness() {
		return nil, nil, ErrAgentNotConnected
	}
	defer current.endBusiness()
	if _, supported := current.capabilities[agentprotocol.CapabilityPodPortForwardV1]; !supported {
		return nil, nil, ErrPodPortForwardCapabilityMissing
	}
	if !tryAcquire(manager.podPortForwardAdmissions) {
		return nil, nil, ErrPodPortForwardRequestExhausted
	}
	defer release(manager.podPortForwardAdmissions)
	if !tryAcquire(current.podPortForwardAdmissions) {
		return nil, nil, ErrPodPortForwardRequestExhausted
	}
	defer release(current.podPortForwardAdmissions)

	requestContext, cancelRequest := context.WithTimeout(ctx, manager.config.PodPortForwardRequestTimeout)
	defer cancelRequest()
	deadline, _ := requestContext.Deadline()
	timeoutMillis := max(int64(1), time.Until(deadline).Milliseconds())
	requestID, err := streamRequestID(ctx)
	if err != nil {
		return nil, nil, err
	}
	return agentprotocol.DoPodPortForward(
		requestContext,
		current.business,
		&agentv1.StreamHeader{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Kind:            agentv1.StreamKind_STREAM_KIND_POD_PORT_FORWARD,
			RequestId:       requestID,
			TimeoutMillis:   uint64(timeoutMillis),
		},
		request,
		peer,
	)
}

func (manager *Manager) RequestResourceWatch(
	ctx context.Context,
	clusterID string,
	request *agentv1.ResourceWatchRequest,
	sink agentprotocol.ResourceWatchSink,
) (*agentv1.ResourceWatchResponse, *agentv1.ResourceWatchTrailer, error) {
	if ctx == nil || !validation.IsUUID(clusterID) {
		return nil, nil, errors.New("Resource Watch request Context or target Cluster ID is invalid")
	}
	manager.mutex.Lock()
	current := manager.connectionsByCluster[clusterID]
	manager.mutex.Unlock()
	if current == nil || current.business == nil || !current.beginBusiness() {
		return nil, nil, ErrAgentNotConnected
	}
	defer current.endBusiness()
	if _, supported := current.capabilities[agentprotocol.CapabilityResourceWatchV1]; !supported {
		return nil, nil, ErrResourceWatchCapabilityMissing
	}
	if !tryAcquire(manager.resourceWatchAdmissions) {
		return nil, nil, ErrResourceWatchRequestExhausted
	}
	defer release(manager.resourceWatchAdmissions)
	if !tryAcquire(current.resourceWatchAdmissions) {
		return nil, nil, ErrResourceWatchRequestExhausted
	}
	defer release(current.resourceWatchAdmissions)
	requestContext, cancelRequest := context.WithTimeout(ctx, manager.config.ResourceWatchRequestTimeout)
	defer cancelRequest()
	deadline, _ := requestContext.Deadline()
	timeoutMillis := max(int64(1), time.Until(deadline).Milliseconds())
	requestID, err := streamRequestID(ctx)
	if err != nil {
		return nil, nil, err
	}
	return agentprotocol.DoResourceWatch(
		requestContext,
		current.business,
		&agentv1.StreamHeader{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Kind:            agentv1.StreamKind_STREAM_KIND_RESOURCE_WATCH,
			RequestId:       requestID,
			TimeoutMillis:   uint64(timeoutMillis),
		},
		request,
		sink,
	)
}

func streamRequestID(ctx context.Context) (string, error) {
	requestID := requestctx.ID(ctx)
	if validation.IsUUID(requestID) {
		return requestID, nil
	}
	requestID, err := identifier.NewUUID()
	if err != nil {
		return "", fmt.Errorf("generate business Stream request identifier: %w", err)
	}
	return requestID, nil
}

func tryAcquire(admissions chan struct{}) bool {
	if admissions == nil {
		return false
	}
	select {
	case admissions <- struct{}{}:
		return true
	default:
		return false
	}
}

func release(admissions chan struct{}) {
	<-admissions
}

func (manager *Manager) Snapshot(
	agentIDs []string,
) map[string]ConnectionStatus {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	result := make(map[string]ConnectionStatus, len(agentIDs))
	for _, agentID := range agentIDs {
		if current := manager.connections[agentID]; current != nil {
			result[agentID] = current.onlineStatus()
			continue
		}
		if previous, exists := manager.lastDisconnected[agentID]; exists {
			result[agentID] = previous
			continue
		}
		result[agentID] = ConnectionStatus{State: ConnectionStateOffline}
	}
	return result
}

func (manager *Manager) closeAll() {
	manager.mutex.Lock()
	connections := make([]*session, 0, len(manager.connections))
	for _, connection := range manager.connections {
		connections = append(connections, connection)
	}
	manager.mutex.Unlock()

	for _, connection := range connections {
		connection.setDisconnectReason(agentprotocol.GoAwayServerShutdown)
		_ = connection.write(&agentv1.ControlFrame{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Message: &agentv1.ControlFrame_GoAway{
				GoAway: &agentv1.GoAway{
					Reason: agentprotocol.GoAwayServerShutdown,
				},
			},
		})
		_ = connection.conn.CloseWithError(agentprotocol.CloseNormal, "server shutdown")
	}
}

// superviseRevocations keeps the revocation watch running for the lifetime of
// the listener.
//
// The first attempt must succeed: accepting an Agent before revocation
// enforcement is live would leave no way to disconnect a credential revoked
// moments later, so a Server that cannot establish the watch at startup fails
// to start.
//
// A later failure is different. The watch rides on one PostgreSQL connection,
// and losing it is an ordinary event — a database restart, a failover, a
// dropped TCP session. Ending the listener there would take the whole Server
// down with it, which is a far worse outcome than a delayed disconnect: while
// the watch is down, a revoked credential is still refused by the next
// heartbeat write, so revocation degrades from immediate to bounded by
// agent_listener.last_seen_write_interval rather than being lost.
func (manager *Manager) superviseRevocations(
	ctx context.Context,
	ready chan<- struct{},
) error {
	signalReady := sync.OnceFunc(func() { close(ready) })
	established := false
	delay := revocationRetryInitialInterval

	for {
		watched := false
		err := manager.store.WatchRevocations(
			ctx,
			func() {
				watched = true
				established = true
				signalReady()
			},
			manager.handleRevocation,
		)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			err = errors.New("Agent revocation watch ended unexpectedly")
		}
		if !established {
			return err
		}
		if watched {
			// The watch ran before dropping, so this is a fresh failure rather
			// than a database that keeps refusing; start the backoff over.
			delay = revocationRetryInitialInterval
		}
		manager.logger.Warn(
			"Agent revocation watch dropped; retrying",
			slog.String("error", err.Error()),
			slog.Duration("retry_after", delay),
			slog.Duration(
				"revocation_delay_until_restored",
				manager.config.LastSeenWriteInterval,
			),
		)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		delay = min(delay*2, revocationRetryMaxInterval)
	}
}

func (manager *Manager) handleRevocation(
	event store.AgentConnectionRevocation,
) {
	manager.mutex.Lock()
	connections := make([]*session, 0)
	for _, current := range manager.connections {
		if event.TenantID != "" &&
			current.identity.TenantID != event.TenantID {
			continue
		}
		if event.ProjectID != "" &&
			current.identity.ProjectID != event.ProjectID {
			continue
		}
		if event.AgentID != "" && current.identity.AgentID != event.AgentID {
			continue
		}
		if event.ClusterID != "" &&
			current.identity.ClusterID != event.ClusterID {
			continue
		}
		if event.CertificateSerial != "" &&
			current.certificateSerial != event.CertificateSerial {
			continue
		}
		connections = append(connections, current)
	}
	manager.mutex.Unlock()

	for _, current := range connections {
		reason := agentprotocol.GoAwayCredentialRevoked
		switch {
		case event.Reason == agentprotocol.GoAwayScopeSuspended:
			reason = agentprotocol.GoAwayScopeSuspended
		case event.AgentID != "" && event.CertificateSerial == "":
			reason = agentprotocol.GoAwayAgentRevoked
		case event.ClusterID != "":
			reason = agentprotocol.GoAwayClusterRevoked
		}
		current.setDisconnectReason(reason)
		_ = current.write(&agentv1.ControlFrame{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Message: &agentv1.ControlFrame_GoAway{
				GoAway: &agentv1.GoAway{Reason: reason},
			},
		})
		closeCode := agentprotocol.CloseAuthenticationError
		message := "Agent access revoked"
		if reason == agentprotocol.GoAwayScopeSuspended {
			closeCode = agentprotocol.CloseScopeSuspended
			message = "Agent scope suspended"
		}
		_ = current.conn.CloseWithError(closeCode, message)
	}
}

func (current *session) beginBusiness() bool {
	current.businessMu.Lock()
	defer current.businessMu.Unlock()
	if current.draining {
		return false
	}
	current.businessInFlight++
	return true
}

func (current *session) endBusiness() {
	current.businessMu.Lock()
	if current.businessInFlight > 0 {
		current.businessInFlight--
	}
	drained := current.draining && current.businessInFlight == 0
	current.businessMu.Unlock()
	if drained {
		current.finishDrain()
	}
}

func (current *session) startDrain(timeout time.Duration, finish func()) {
	current.businessMu.Lock()
	if current.draining {
		current.businessMu.Unlock()
		return
	}
	current.draining = true
	current.drainFinish = finish
	drained := current.businessInFlight == 0
	if !drained {
		current.drainTimer = time.AfterFunc(timeout, current.finishDrain)
	}
	current.businessMu.Unlock()
	if drained {
		current.finishDrain()
	}
}

func (current *session) finishDrain() {
	current.drainOnce.Do(func() {
		current.businessMu.Lock()
		if current.drainTimer != nil {
			current.drainTimer.Stop()
			current.drainTimer = nil
		}
		finish := current.drainFinish
		current.businessMu.Unlock()
		if finish != nil {
			finish()
		}
	})
}

func (current *session) recordHeartbeat(at time.Time) {
	current.statusMu.Lock()
	defer current.statusMu.Unlock()
	current.lastHeartbeatAt = at
}

func (current *session) setDisconnectReason(reason string) {
	current.statusMu.Lock()
	defer current.statusMu.Unlock()
	if disconnectReasonPriority(reason) <
		disconnectReasonPriority(current.disconnectReason) {
		return
	}
	current.disconnectReason = reason
}

func disconnectReasonPriority(reason string) int {
	switch reason {
	case "agent_revoked", "cluster_revoked":
		return 4
	case "credential_revoked", "credential_rejected", "scope_suspended",
		"certificate_expired":
		return 3
	case "connection_replaced", "server_shutdown", "client_goodbye":
		return 2
	case "":
		return 0
	default:
		return 1
	}
}

func (current *session) onlineStatus() ConnectionStatus {
	current.statusMu.Lock()
	defer current.statusMu.Unlock()
	return ConnectionStatus{
		State:           ConnectionStateOnline,
		ConnectionID:    current.id,
		ConnectedAt:     current.connectedAt,
		LastHeartbeatAt: current.lastHeartbeatAt,
	}
}

func (current *session) disconnectedStatus(at time.Time) ConnectionStatus {
	current.statusMu.Lock()
	defer current.statusMu.Unlock()
	reason := current.disconnectReason
	if strings.TrimSpace(reason) == "" {
		reason = "connection_closed"
	}
	return ConnectionStatus{
		State:                ConnectionStateOffline,
		LastHeartbeatAt:      current.lastHeartbeatAt,
		LastDisconnectedAt:   at,
		LastDisconnectReason: reason,
	}
}

func (current *session) write(frame *agentv1.ControlFrame) error {
	current.writeMu.Lock()
	defer current.writeMu.Unlock()
	timeout := current.writeTimeout
	if timeout <= 0 {
		timeout = defaultSessionWriteTimeout
	}
	if err := current.stream.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	return agentprotocol.WriteFrame(current.stream, frame)
}

func (manager *Manager) reject(
	connection *quic.Conn,
	code quic.ApplicationErrorCode,
	reason string,
	err error,
) {
	manager.logger.Warn(
		"Agent connection rejected",
		slog.String("remote_address", connection.RemoteAddr().String()),
		slog.String("reason", reason),
		slog.String("error", err.Error()),
	)
	_ = connection.CloseWithError(code, reason)
}

func loadTLSConfig(config Config) (*tls.Config, error) {
	reloader := config.TLSCertificateReloader
	if reloader == nil {
		var err error
		reloader, err = NewTLSCertificateReloader(
			config.TLSCertificateFile,
			config.TLSPrivateKeyFile,
		)
		if err != nil {
			return nil, err
		}
	}
	clientCAPEM, err := os.ReadFile(config.ClientCACertificateFile)
	if err != nil {
		return nil, fmt.Errorf("read Agent client CA certificate: %w", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(clientCAPEM) {
		return nil, errors.New("Agent client CA certificate PEM is invalid")
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: reloader.GetCertificate,
		ClientAuth:     tls.RequireAndVerifyClientCert,
		ClientCAs:      clientCAs,
		NextProtos:     []string{agentprotocol.ALPN},
	}, nil
}

func identityFromConnection(connection *quic.Conn) (certificateIdentity, error) {
	state := connection.ConnectionState()
	if state.Used0RTT {
		return certificateIdentity{}, errors.New("QUIC 0-RTT is not allowed")
	}
	if state.TLS.NegotiatedProtocol != agentprotocol.ALPN ||
		len(state.TLS.PeerCertificates) == 0 {
		return certificateIdentity{}, errors.New("verified Agent certificate is required")
	}
	return identityFromCertificate(state.TLS.PeerCertificates[0])
}

func identityFromCertificate(certificate *x509.Certificate) (certificateIdentity, error) {
	if certificate == nil ||
		certificate.SerialNumber == nil ||
		certificate.SerialNumber.Sign() <= 0 ||
		len(certificate.URIs) != 1 {
		return certificateIdentity{}, errors.New("Agent certificate identity is invalid")
	}
	identityURI := certificate.URIs[0]
	if identityURI.Scheme != "zke" ||
		identityURI.Host != "agent" ||
		identityURI.User != nil ||
		identityURI.Opaque != "" ||
		identityURI.RawPath != "" ||
		identityURI.RawQuery != "" ||
		identityURI.Fragment != "" ||
		identityURI.ForceQuery {
		return certificateIdentity{}, errors.New("Agent certificate identity URI is invalid")
	}
	parts := strings.Split(strings.Trim(identityURI.Path, "/"), "/")
	if len(parts) != 8 ||
		parts[0] != "tenants" ||
		parts[2] != "projects" ||
		parts[4] != "clusters" ||
		parts[6] != "agents" {
		return certificateIdentity{}, errors.New("Agent certificate identity path is invalid")
	}
	for _, value := range []string{parts[1], parts[3], parts[5], parts[7]} {
		if !validation.IsUUID(value) {
			return certificateIdentity{}, errors.New("Agent certificate identity contains an invalid identifier")
		}
	}
	expected := (&url.URL{
		Scheme: "zke",
		Host:   "agent",
		Path: strings.Join([]string{
			"/tenants", parts[1],
			"projects", parts[3],
			"clusters", parts[5],
			"agents", parts[7],
		}, "/"),
	}).String()
	if identityURI.String() != expected ||
		certificate.Subject.CommonName != parts[7] {
		return certificateIdentity{}, errors.New("Agent certificate identity is not canonical")
	}
	return certificateIdentity{
		AgentConnectionIdentity: store.AgentConnectionIdentity{
			TenantID:  parts[1],
			ProjectID: parts[3],
			ClusterID: parts[5],
			AgentID:   parts[7],
		},
		CertificateSerial:    certificate.SerialNumber.String(),
		CertificateExpiresAt: certificate.NotAfter,
	}, nil
}

func validateClientHello(
	protocolVersion uint32,
	hello *agentv1.ClientHello,
	identity certificateIdentity,
) error {
	if protocolVersion != agentprotocol.ProtocolVersion {
		return errors.New("Agent protocol version is unsupported")
	}
	if hello == nil ||
		hello.GetAgentId() != identity.AgentID ||
		hello.GetClusterId() != identity.ClusterID ||
		!validation.IsUUID(hello.GetStartupId()) {
		return errors.New("ClientHello identity does not match the certificate")
	}
	version := strings.TrimSpace(hello.GetAgentVersion())
	if version == "" || version != hello.GetAgentVersion() || len(version) > 128 {
		return errors.New("ClientHello Agent version is invalid")
	}
	if len(hello.GetCapabilities()) > 64 {
		return errors.New("ClientHello capability count exceeds the limit")
	}
	for _, capability := range hello.GetCapabilities() {
		if capability == "" ||
			strings.TrimSpace(capability) != capability ||
			len(capability) > 128 {
			return errors.New("ClientHello capability is invalid")
		}
	}
	return nil
}

func hasCapability(capabilities []string, expected string) bool {
	for _, capability := range capabilities {
		if capability == expected {
			return true
		}
	}
	return false
}

func healthStatusValue(value agentv1.HealthStatus) (string, error) {
	switch value {
	case agentv1.HealthStatus_HEALTH_STATUS_HEALTHY:
		return "healthy", nil
	case agentv1.HealthStatus_HEALTH_STATUS_DEGRADED:
		return "degraded", nil
	default:
		return "", errors.New("Agent health status is invalid")
	}
}
