package agent

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

func runConnectionLoop(
	ctx context.Context,
	cfg Config,
	store *IdentityStore,
	identity LocalIdentity,
	version string,
	logger *slog.Logger,
) error {
	startupID, err := newConnectionID()
	if err != nil {
		return err
	}

	interval := cfg.Connection.RetryInitialInterval
	for {
		tlsConfig, err := connectionTLSConfig(cfg, identity)
		if err != nil {
			return err
		}
		err = runConnection(
			ctx,
			cfg,
			store,
			tlsConfig,
			identity,
			version,
			startupID,
			logger,
		)
		if ctx.Err() != nil {
			return nil
		}
		var renewed *certificateRenewedError
		if errors.As(err, &renewed) {
			identity = renewed.identity
			interval = cfg.Connection.RetryInitialInterval
			logger.Info(
				"Agent certificate renewed",
				slog.Time(
					"certificate_expires_at",
					identity.CertificateExpiresAt,
				),
			)
			continue
		}
		if permanentAgentConnectionError(err) {
			return err
		}
		delay := retryDelay(interval, 0, cfg.Connection.RetryMaxInterval)
		logger.Warn(
			"Agent connection will be retried",
			slog.String("error", err.Error()),
			slog.Duration("retry_after", delay),
		)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-timer.C:
		}
		interval = min(interval*2, cfg.Connection.RetryMaxInterval)
	}
}

func runConnection(
	ctx context.Context,
	cfg Config,
	store *IdentityStore,
	tlsConfig *tls.Config,
	identity LocalIdentity,
	version string,
	startupID string,
	logger *slog.Logger,
) error {
	connectContext, cancelConnect := context.WithTimeout(
		ctx,
		cfg.Connection.ConnectTimeout,
	)
	connection, err := quic.DialAddr(
		connectContext,
		cfg.ConnectionServerAddress(),
		tlsConfig,
		&quic.Config{
			HandshakeIdleTimeout:  cfg.Connection.ConnectTimeout,
			MaxIdleTimeout:        15 * time.Minute,
			KeepAlivePeriod:       10 * time.Second,
			MaxIncomingStreams:    16,
			MaxIncomingUniStreams: -1,
		},
	)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("connect to Agent Listener: %w", err)
	}
	defer connection.CloseWithError(agentprotocol.CloseNormal, "Agent connection closed")

	streamContext, cancelStream := context.WithTimeout(
		ctx,
		cfg.Connection.ConnectTimeout,
	)
	controlStream, err := connection.OpenStreamSync(streamContext)
	cancelStream()
	if err != nil {
		return fmt.Errorf("open Agent control stream: %w", err)
	}
	defer controlStream.Close()

	if err := controlStream.SetDeadline(
		time.Now().Add(cfg.Connection.ConnectTimeout),
	); err != nil {
		return fmt.Errorf("set Agent control handshake deadline: %w", err)
	}
	if err := agentprotocol.WriteFrame(controlStream, &agentv1.ControlFrame{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		Message: &agentv1.ControlFrame_ClientHello{
			ClientHello: &agentv1.ClientHello{
				AgentId:      identity.AgentID,
				ClusterId:    identity.ClusterID,
				AgentVersion: version,
				StartupId:    startupID,
				Capabilities: []string{
					agentprotocol.CapabilityCertificateRenewal,
				},
			},
		},
	}); err != nil {
		return err
	}
	frame, err := agentprotocol.ReadFrame(controlStream)
	if err != nil {
		return err
	}
	serverHello := frame.GetServerHello()
	heartbeatInterval, heartbeatTimeout, err := validateServerHello(
		frame.GetProtocolVersion(),
		serverHello,
	)
	if err != nil {
		return err
	}
	if err := controlStream.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear Agent control handshake deadline: %w", err)
	}

	logger.Info(
		"Agent connection established",
		slog.String("connection_id", serverHello.GetConnectionId()),
		slog.Duration("heartbeat_interval", heartbeatInterval),
		slog.Duration("heartbeat_timeout", heartbeatTimeout),
	)
	renewalRequired := time.Until(identity.CertificateExpiresAt) <=
		cfg.CertificateRenewBefore
	if renewalRequired &&
		!serverSupportsCapability(
			serverHello,
			agentprotocol.CapabilityCertificateRenewal,
		) {
		logger.Warn(
			"Agent certificate is within the renewal window but the Server does not support renewal",
			slog.Time(
				"certificate_expires_at",
				identity.CertificateExpiresAt,
			),
		)
	}
	if store != nil &&
		renewalRequired &&
		serverSupportsCapability(
			serverHello,
			agentprotocol.CapabilityCertificateRenewal,
		) {
		renewed, err := renewAgentCertificate(
			ctx,
			cfg,
			store,
			controlStream,
			identity,
		)
		if err != nil {
			return err
		}
		return &certificateRenewedError{identity: renewed}
	}
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	var sequence uint64
	for {
		select {
		case <-ctx.Done():
			_ = controlStream.SetWriteDeadline(time.Now().Add(time.Second))
			_ = agentprotocol.WriteFrame(controlStream, &agentv1.ControlFrame{
				ProtocolVersion: agentprotocol.ProtocolVersion,
				Message: &agentv1.ControlFrame_ClientGoodbye{
					ClientGoodbye: &agentv1.ClientGoodbye{
						Reason: "shutdown",
					},
				},
			})
			return nil
		case <-connection.Context().Done():
			if cause := context.Cause(connection.Context()); cause != nil {
				return cause
			}
			return connection.Context().Err()
		case <-ticker.C:
			if store != nil &&
				serverSupportsCapability(
					serverHello,
					agentprotocol.CapabilityCertificateRenewal,
				) &&
				time.Until(identity.CertificateExpiresAt) <=
					cfg.CertificateRenewBefore {
				renewed, err := renewAgentCertificate(
					ctx,
					cfg,
					store,
					controlStream,
					identity,
				)
				if err != nil {
					return err
				}
				return &certificateRenewedError{identity: renewed}
			}
			sequence++
			now := time.Now().UTC()
			if err := controlStream.SetWriteDeadline(
				now.Add(heartbeatTimeout),
			); err != nil {
				return err
			}
			if err := agentprotocol.WriteFrame(controlStream, &agentv1.ControlFrame{
				ProtocolVersion: agentprotocol.ProtocolVersion,
				Message: &agentv1.ControlFrame_Heartbeat{
					Heartbeat: &agentv1.Heartbeat{
						Sequence:        sequence,
						SentAtUnixMilli: now.UnixMilli(),
						Health:          agentv1.HealthStatus_HEALTH_STATUS_HEALTHY,
					},
				},
			}); err != nil {
				return err
			}
			if err := controlStream.SetReadDeadline(
				now.Add(heartbeatTimeout),
			); err != nil {
				return err
			}
			response, err := agentprotocol.ReadFrame(controlStream)
			if err != nil {
				return err
			}
			if goAway := response.GetGoAway(); goAway != nil {
				return fmt.Errorf("Server requested Agent disconnect: %s", goAway.GetReason())
			}
			ack := response.GetHeartbeatAck()
			if response.GetProtocolVersion() != agentprotocol.ProtocolVersion ||
				ack == nil ||
				ack.GetSequence() != sequence {
				return errors.New("Agent heartbeat acknowledgement is invalid")
			}
		}
	}
}

type certificateRenewedError struct {
	identity LocalIdentity
}

func (err *certificateRenewedError) Error() string {
	return "Agent certificate renewed; reconnecting with the new identity"
}

func renewAgentCertificate(
	ctx context.Context,
	cfg Config,
	store *IdentityStore,
	controlStream *quic.Stream,
	identity LocalIdentity,
) (LocalIdentity, error) {
	csrPEM, err := store.LoadOrCreateRenewalCSR(ctx, identity)
	if err != nil {
		return LocalIdentity{}, err
	}
	deadline := time.Now().Add(cfg.Connection.ConnectTimeout)
	if err := controlStream.SetDeadline(deadline); err != nil {
		return LocalIdentity{}, fmt.Errorf(
			"set Agent certificate renewal deadline: %w",
			err,
		)
	}
	if err := agentprotocol.WriteFrame(controlStream, &agentv1.ControlFrame{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		Message: &agentv1.ControlFrame_CertificateRenewalRequest{
			CertificateRenewalRequest: &agentv1.CertificateRenewalRequest{
				CsrPem: string(csrPEM),
			},
		},
	}); err != nil {
		return LocalIdentity{}, err
	}
	frame, err := agentprotocol.ReadFrame(controlStream)
	if err != nil {
		return LocalIdentity{}, err
	}
	response := frame.GetCertificateRenewalResponse()
	if frame.GetProtocolVersion() != agentprotocol.ProtocolVersion ||
		response == nil ||
		strings.TrimSpace(response.GetCertificatePem()) == "" ||
		response.GetCertificateExpiresAtUnixMilli() <= 0 {
		return LocalIdentity{}, errors.New(
			"Agent certificate renewal response is invalid",
		)
	}
	expiresAt := time.UnixMilli(
		response.GetCertificateExpiresAtUnixMilli(),
	).UTC()
	if !expiresAt.After(time.Now().Add(cfg.CertificateRenewBefore)) {
		return LocalIdentity{}, errors.New(
			"renewed Agent certificate does not extend beyond the renewal window",
		)
	}
	renewed, err := store.CompleteRenewal(
		ctx,
		identity,
		csrPEM,
		[]byte(response.GetCertificatePem()),
		expiresAt,
		time.Now().UTC(),
	)
	if err != nil {
		return LocalIdentity{}, err
	}
	if err := controlStream.SetDeadline(time.Time{}); err != nil {
		return LocalIdentity{}, fmt.Errorf(
			"clear Agent certificate renewal deadline: %w",
			err,
		)
	}
	return renewed, nil
}

func connectionTLSConfig(
	cfg Config,
	identity LocalIdentity,
) (*tls.Config, error) {
	certificate, err := tls.X509KeyPair(
		identity.CertificatePEM,
		identity.PrivateKeyPEM,
	)
	if err != nil {
		return nil, errors.New("load Agent client identity")
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		ServerName:   cfg.ConnectionServerName(),
		Certificates: []tls.Certificate{certificate},
		NextProtos:   []string{agentprotocol.ALPN},
	}

	roots := x509.NewCertPool()
	serverCAPEM := cfg.Connection.CACertificatePEM
	if cfg.Connection.CACertificateFile != "" {
		var readErr error
		serverCAPEM, readErr = readBoundedFile(
			cfg.Connection.CACertificateFile,
			maxCACertificateFileBytes,
			"Agent connection CA certificate file",
		)
		if readErr != nil {
			return nil, readErr
		}
	}
	if len(serverCAPEM) == 0 {
		return nil, errors.New("Agent connection CA certificate is required")
	}
	if err := appendRootCertificates(roots, serverCAPEM); err != nil {
		return nil, err
	}
	tlsConfig.RootCAs = roots
	return tlsConfig, nil
}

func newConnectionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("generate Agent startup identifier")
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

func validateServerHello(
	protocolVersion uint32,
	hello *agentv1.ServerHello,
) (time.Duration, time.Duration, error) {
	if protocolVersion != agentprotocol.ProtocolVersion ||
		hello == nil ||
		!validation.IsUUID(hello.GetConnectionId()) {
		return 0, 0, errors.New("ServerHello is invalid")
	}
	heartbeatInterval := time.Duration(hello.GetHeartbeatIntervalMillis()) * time.Millisecond
	heartbeatTimeout := time.Duration(hello.GetHeartbeatTimeoutMillis()) * time.Millisecond
	if heartbeatInterval < time.Second ||
		heartbeatInterval > 5*time.Minute ||
		heartbeatTimeout <= heartbeatInterval ||
		heartbeatTimeout > 15*time.Minute {
		return 0, 0, errors.New("Server heartbeat configuration is invalid")
	}
	if len(hello.GetCapabilities()) > 64 {
		return 0, 0, errors.New("Server capability count exceeds the limit")
	}
	for _, capability := range hello.GetCapabilities() {
		if capability == "" ||
			strings.TrimSpace(capability) != capability ||
			len(capability) > 128 {
			return 0, 0, errors.New("Server capability is invalid")
		}
	}
	return heartbeatInterval, heartbeatTimeout, nil
}

func serverSupportsCapability(
	hello *agentv1.ServerHello,
	expected string,
) bool {
	if hello == nil {
		return false
	}
	for _, capability := range hello.GetCapabilities() {
		if capability == expected {
			return true
		}
	}
	return false
}

func permanentAgentConnectionError(err error) bool {
	if err == nil {
		return false
	}
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	if errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostnameError) ||
		errors.As(err, &invalidCertificate) {
		return true
	}
	var applicationError *quic.ApplicationError
	if errors.As(err, &applicationError) {
		return applicationError.Remote &&
			(applicationError.ErrorCode == agentprotocol.CloseProtocolError ||
				applicationError.ErrorCode == agentprotocol.CloseAuthenticationError)
	}
	return strings.Contains(err.Error(), "Agent heartbeat acknowledgement is invalid") ||
		strings.Contains(err.Error(), "ServerHello is invalid") ||
		strings.Contains(err.Error(), "Server heartbeat configuration is invalid") ||
		strings.Contains(err.Error(), "credential_revoked") ||
		strings.Contains(err.Error(), "Agent certificate expired")
}
