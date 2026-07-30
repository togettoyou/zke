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
	Address                 string
	TLSCertificateFile      string
	TLSPrivateKeyFile       string
	ClientCACertificateFile string
	HandshakeTimeout        time.Duration
	HeartbeatInterval       time.Duration
	HeartbeatTimeout        time.Duration
	LastSeenWriteInterval   time.Duration
	OperationTimeout        time.Duration
	MaxConcurrentAgents     int
	MaxIncomingStreams      int64
	WriteTimeout            time.Duration
	ResourceRequestTimeout  time.Duration
	ConnectionDrainTimeout  time.Duration
	MaxResourceBodyBytes    uint64
	MaxResourceStreams      int
	MaxResourceRequests     int
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
	resourceAdmissions chan struct{}

	mutex                sync.Mutex
	connections          map[string]*session
	connectionsByCluster map[string]*session
	lastDisconnected     map[string]ConnectionStatus
	disconnectOrder      []string
	subscribers          map[uint64]chan ConnectionEvent
	nextSubscriberID     uint64
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
	id                   string
	identity             store.AgentConnectionIdentity
	certificateSerial    string
	certificateExpiresAt time.Time
	connectedAt          time.Time
	conn                 managedConnection
	business             *quic.Conn
	stream               controlStream
	writeTimeout         time.Duration
	writeMu              sync.Mutex
	statusMu             sync.Mutex
	lastHeartbeatAt      time.Time
	disconnectReason     string
	capabilities         map[string]struct{}
	resourceAdmissions   chan struct{}
	resourceMu           sync.Mutex
	resourceInFlight     int
	draining             bool
	drainOnce            sync.Once
	drainTimer           *time.Timer
	drainFinish          func()
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

	defaultMaxRememberedDisconnects = 4096
	defaultSessionWriteTimeout      = 5 * time.Second
	defaultResourceRequestTimeout   = 2 * time.Minute
	defaultConnectionDrainTimeout   = 10 * time.Second
	defaultMaxResourceBodyBytes     = 32 * 1024 * 1024
	defaultMaxResourceStreams       = 64
	defaultMaxResourceRequests      = 4096

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
	ErrAgentNotConnected         = errors.New("target Cluster Agent is not connected")
	ErrResourceCapabilityMissing = errors.New("target Cluster Agent does not support Resource Streams")
	ErrResourceRequestExhausted  = errors.New("Resource Stream request capacity is exhausted")
	ErrResourceVerbUnsupported   = errors.New("Resource Stream verb is not implemented")
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
	streamServer, err := agentprotocol.NewStreamServer(
		agentprotocol.StreamServerConfig{
			HeaderTimeout: config.HandshakeTimeout,
			MaxTimeout:    config.ResourceRequestTimeout,
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
	if err != nil {
		return nil, err
	}
	return &Manager{
		config:     config,
		logger:     logger,
		store:      connectionStore,
		renewal:    renewalService,
		tls:        tlsConfig,
		streams:    streamServer,
		admissions: make(chan struct{}, max(1, config.MaxConcurrentAgents)),
		resourceAdmissions: make(
			chan struct{},
			config.MaxResourceRequests,
		),
		connections:          make(map[string]*session),
		connectionsByCluster: make(map[string]*session),
		lastDisconnected:     make(map[string]ConnectionStatus),
		subscribers:          make(map[uint64]chan ConnectionEvent),
	}, nil
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
		err := manager.streams.Serve(businessContext, connection)
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
	if !current.beginResource() {
		return nil, ErrAgentNotConnected
	}
	defer current.endResource()
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
	requestID, err := resourceRequestID(ctx)
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

func resourceRequestID(ctx context.Context) (string, error) {
	requestID := requestctx.ID(ctx)
	if validation.IsUUID(requestID) {
		return requestID, nil
	}
	requestID, err := identifier.NewUUID()
	if err != nil {
		return "", fmt.Errorf("generate Resource request identifier: %w", err)
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

func (current *session) beginResource() bool {
	current.resourceMu.Lock()
	defer current.resourceMu.Unlock()
	if current.draining {
		return false
	}
	current.resourceInFlight++
	return true
}

func (current *session) endResource() {
	current.resourceMu.Lock()
	if current.resourceInFlight > 0 {
		current.resourceInFlight--
	}
	drained := current.draining && current.resourceInFlight == 0
	current.resourceMu.Unlock()
	if drained {
		current.finishDrain()
	}
}

func (current *session) startDrain(timeout time.Duration, finish func()) {
	current.resourceMu.Lock()
	if current.draining {
		current.resourceMu.Unlock()
		return
	}
	current.draining = true
	current.drainFinish = finish
	drained := current.resourceInFlight == 0
	if !drained {
		current.drainTimer = time.AfterFunc(timeout, current.finishDrain)
	}
	current.resourceMu.Unlock()
	if drained {
		current.finishDrain()
	}
}

func (current *session) finishDrain() {
	current.drainOnce.Do(func() {
		current.resourceMu.Lock()
		if current.drainTimer != nil {
			current.drainTimer.Stop()
			current.drainTimer = nil
		}
		finish := current.drainFinish
		current.resourceMu.Unlock()
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
	certificate, err := tls.LoadX509KeyPair(
		config.TLSCertificateFile,
		config.TLSPrivateKeyFile,
	)
	if err != nil {
		return nil, fmt.Errorf("load Agent Listener TLS certificate: %w", err)
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
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		NextProtos:   []string{agentprotocol.ALPN},
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
