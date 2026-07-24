package agentconn

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

type Config struct {
	Address                string
	TLSCertificateFile     string
	TLSPrivateKeyFile      string
	AgentCACertificateFile string
	HandshakeTimeout       time.Duration
	HeartbeatInterval      time.Duration
	HeartbeatTimeout       time.Duration
	LastSeenWriteInterval  time.Duration
	OperationTimeout       time.Duration
}

type Manager struct {
	config Config
	logger *slog.Logger
	store  *store.AgentConnectionStore
	tls    *tls.Config

	mutex       sync.Mutex
	connections map[string]*session
}

type session struct {
	id       string
	identity store.AgentConnectionIdentity
	conn     *quic.Conn
	stream   *quic.Stream
	writeMu  sync.Mutex
}

type certificateIdentity struct {
	store.AgentConnectionIdentity
	CertificateSerial string
}

func New(
	config Config,
	logger *slog.Logger,
	connectionStore *store.AgentConnectionStore,
) (*Manager, error) {
	tlsConfig, err := loadTLSConfig(config)
	if err != nil {
		return nil, err
	}
	return &Manager{
		config:      config,
		logger:      logger,
		store:       connectionStore,
		tls:         tlsConfig,
		connections: make(map[string]*session),
	}, nil
}

func (manager *Manager) Run(ctx context.Context) error {
	listener, err := quic.ListenAddr(
		manager.config.Address,
		manager.tls,
		&quic.Config{
			HandshakeIdleTimeout:  manager.config.HandshakeTimeout,
			MaxIdleTimeout:        manager.config.HeartbeatTimeout,
			KeepAlivePeriod:       manager.config.HeartbeatInterval,
			MaxIncomingStreams:    16,
			MaxIncomingUniStreams: -1,
			Allow0RTT:             false,
		},
	)
	if err != nil {
		return fmt.Errorf("listen for Agent QUIC connections: %w", err)
	}
	defer listener.Close()

	manager.logger.Info(
		"Agent QUIC listener starting",
		slog.String("address", listener.Addr().String()),
	)
	for {
		connection, err := listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				manager.closeAll()
				return nil
			}
			return fmt.Errorf("accept Agent QUIC connection: %w", err)
		}
		go manager.handleConnection(ctx, connection)
	}
}

func (manager *Manager) handleConnection(parent context.Context, connection *quic.Conn) {
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
		manager.reject(connection, agentprotocol.CloseAuthenticationError, "Agent credential rejected", err)
		return
	}

	connectionID, err := newID()
	if err != nil {
		manager.reject(connection, agentprotocol.CloseProtocolError, "connection identifier unavailable", err)
		return
	}
	current := &session{
		id:       connectionID,
		identity: identity.AgentConnectionIdentity,
		conn:     connection,
		stream:   controlStream,
	}
	previous := manager.register(current)
	if previous != nil {
		_ = previous.write(&agentv1.ControlFrame{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Message: &agentv1.ControlFrame_GoAway{
				GoAway: &agentv1.GoAway{Reason: "connection_replaced"},
			},
		})
		_ = previous.conn.CloseWithError(
			agentprotocol.CloseConnectionReplaced,
			"connection replaced",
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
	_ = connection.CloseWithError(agentprotocol.CloseNormal, "connection closed")
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
		if err := current.stream.SetReadDeadline(
			time.Now().Add(manager.config.HeartbeatTimeout),
		); err != nil {
			return err
		}
		frame, err := agentprotocol.ReadFrame(current.stream)
		if err != nil {
			if ctx.Err() == nil && current.conn.Context().Err() == nil {
				_ = current.conn.CloseWithError(
					agentprotocol.CloseHeartbeatTimeout,
					"heartbeat timeout",
				)
			}
			return err
		}
		if frame.GetProtocolVersion() != agentprotocol.ProtocolVersion {
			_ = current.conn.CloseWithError(
				agentprotocol.CloseProtocolError,
				"protocol version mismatch",
			)
			return errors.New("Agent protocol version mismatch")
		}
		if goodbye := frame.GetClientGoodbye(); goodbye != nil {
			logger.Info(
				"Agent sent goodbye",
				slog.String("reason", goodbye.GetReason()),
			)
			return nil
		}
		heartbeat := frame.GetHeartbeat()
		if heartbeat == nil ||
			heartbeat.GetSequence() == 0 ||
			heartbeat.GetSequence() <= lastSequence {
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
		now := time.Now().UTC()
		if nextHealthStatus != healthStatus ||
			now.Sub(lastPersisted) >= manager.config.LastSeenWriteInterval {
			operationContext, cancelOperation := context.WithTimeout(
				ctx,
				manager.config.OperationTimeout,
			)
			err = manager.store.RecordHeartbeat(
				operationContext,
				store.RecordAgentHeartbeatParams{
					Identity:     current.identity,
					HealthStatus: nextHealthStatus,
					Now:          now,
				},
			)
			cancelOperation()
			if err != nil {
				return fmt.Errorf("persist Agent heartbeat: %w", err)
			}
			lastPersisted = now
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
			return err
		}
	}
}

func (manager *Manager) register(current *session) *session {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	previous := manager.connections[current.identity.AgentID]
	manager.connections[current.identity.AgentID] = current
	return previous
}

func (manager *Manager) unregister(current *session) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	if manager.connections[current.identity.AgentID] == current {
		delete(manager.connections, current.identity.AgentID)
	}
}

func (manager *Manager) closeAll() {
	manager.mutex.Lock()
	connections := make([]*session, 0, len(manager.connections))
	for _, connection := range manager.connections {
		connections = append(connections, connection)
	}
	manager.mutex.Unlock()

	for _, connection := range connections {
		_ = connection.write(&agentv1.ControlFrame{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Message: &agentv1.ControlFrame_GoAway{
				GoAway: &agentv1.GoAway{Reason: "server_shutdown"},
			},
		})
		_ = connection.conn.CloseWithError(agentprotocol.CloseNormal, "server shutdown")
	}
}

func (current *session) write(frame *agentv1.ControlFrame) error {
	current.writeMu.Lock()
	defer current.writeMu.Unlock()
	if err := current.stream.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
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
	clientCAPEM, err := os.ReadFile(config.AgentCACertificateFile)
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
		CertificateSerial: certificate.SerialNumber.String(),
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

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("generate random identifier")
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], value[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], value[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], value[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], value[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], value[10:16])
	return string(encoded), nil
}
