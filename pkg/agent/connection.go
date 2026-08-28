package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/identifier"
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
	startupID, err := identifier.NewUUID()
	if err != nil {
		return fmt.Errorf("generate Agent startup identifier: %w", err)
	}
	return runConnectionLoopWithServices(
		ctx,
		cfg,
		store,
		identity,
		version,
		startupID,
		logger,
		connectionServices{},
	)
}

func runConnectionLoopWithServices(
	ctx context.Context,
	cfg Config,
	store *IdentityStore,
	identity LocalIdentity,
	version string,
	startupID string,
	logger *slog.Logger,
	services connectionServices,
) error {
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
			services,
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
		if err == nil {
			// Only a cancelled context ends a connection without an error, and
			// that is handled above. Reconnect rather than dereference nil if a
			// future return path ever breaks that invariant.
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
	services connectionServices,
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
			MaxIdleTimeout:        cfg.Connection.IdleTimeout,
			KeepAlivePeriod:       cfg.Connection.KeepAliveInterval,
			MaxIncomingStreams:    cfg.Connection.MaxIncomingStreams,
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
	capabilities := []string{
		agentprotocol.CapabilityCertificateRenewal,
	}
	if services.resourceHandler != nil {
		capabilities = append(
			capabilities,
			agentprotocol.CapabilityResourceV1,
			agentprotocol.CapabilityResourceDiscoveryV1,
			agentprotocol.CapabilityResourceWriteV1,
		)
	}
	if services.podLogsHandler != nil {
		capabilities = append(capabilities, agentprotocol.CapabilityPodLogsV1)
	}
	if services.podExecHandler != nil {
		capabilities = append(capabilities, agentprotocol.CapabilityPodExecV1)
	}
	if services.podPortForwardHandler != nil {
		capabilities = append(capabilities, agentprotocol.CapabilityPodPortForwardV1)
	}
	if services.resourceWatchHandler != nil {
		capabilities = append(capabilities, agentprotocol.CapabilityResourceWatchV1)
	}
	if services.terminalSessionHandler != nil {
		capabilities = append(capabilities, agentprotocol.CapabilityTerminalSessionV1)
		if services.podExecHandler != nil {
			capabilities = append(capabilities, agentprotocol.CapabilityTerminalCommandV1)
		}
	}
	if services.metricsForwarder != nil {
		capabilities = append(capabilities, agentprotocol.CapabilityMetricsIngestV1)
	}
	if services.metricsCollectorHandler != nil {
		capabilities = append(capabilities, agentprotocol.CapabilityMetricsCollectorV1)
	}
	if services.helmHandler != nil {
		capabilities = append(capabilities, agentprotocol.CapabilityHelmV1)
		// Advertised beside the Helm capability rather than folded into it: the
		// Server has to know, before it opens the Stream, whether this Agent
		// will answer with progress frames or with a bare response, because the
		// two are read differently.
		capabilities = append(capabilities, agentprotocol.CapabilityHelmProgressV1)
	}
	if err := agentprotocol.WriteFrame(controlStream, &agentv1.ControlFrame{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		Message: &agentv1.ControlFrame_ClientHello{
			ClientHello: &agentv1.ClientHello{
				AgentId:      identity.AgentID,
				ClusterId:    identity.ClusterID,
				AgentVersion: version,
				StartupId:    startupID,
				Capabilities: capabilities,
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
	resourceSupported := services.resourceHandler != nil &&
		serverSupportsCapability(
			serverHello,
			agentprotocol.CapabilityResourceV1,
		)
	podLogsSupported := services.podLogsHandler != nil &&
		serverSupportsCapability(
			serverHello,
			agentprotocol.CapabilityPodLogsV1,
		)
	if !podLogsSupported {
		services.podLogsHandler = nil
	}
	podExecSupported := services.podExecHandler != nil &&
		serverSupportsCapability(
			serverHello,
			agentprotocol.CapabilityPodExecV1,
		)
	if !podExecSupported {
		services.podExecHandler = nil
	}
	podPortForwardSupported := services.podPortForwardHandler != nil &&
		serverSupportsCapability(
			serverHello,
			agentprotocol.CapabilityPodPortForwardV1,
		)
	if !podPortForwardSupported {
		services.podPortForwardHandler = nil
	}
	resourceWatchSupported := services.resourceWatchHandler != nil &&
		serverSupportsCapability(serverHello, agentprotocol.CapabilityResourceWatchV1)
	if !resourceWatchSupported {
		services.resourceWatchHandler = nil
	}
	terminalSessionSupported := services.terminalSessionHandler != nil &&
		serverSupportsCapability(serverHello, agentprotocol.CapabilityTerminalSessionV1)
	if !terminalSessionSupported {
		services.terminalSessionHandler = nil
	}
	metricsCollectorSupported := services.metricsCollectorHandler != nil &&
		serverSupportsCapability(serverHello, agentprotocol.CapabilityMetricsCollectorV1)
	if !metricsCollectorSupported {
		services.metricsCollectorHandler = nil
	}
	helmSupported := services.helmHandler != nil &&
		serverSupportsCapability(serverHello, agentprotocol.CapabilityHelmV1)
	if !helmSupported {
		services.helmHandler = nil
	}
	// A Server that does not accept metrics leaves the forwarder detached, so
	// the collector is told immediately that there is nowhere to send data
	// rather than having each batch refused on an open Stream.
	if services.metricsForwarder != nil &&
		serverSupportsCapability(serverHello, agentprotocol.CapabilityMetricsIngestV1) {
		services.metricsForwarder.attach(connection)
		defer services.metricsForwarder.detach()
	}
	businessServer, err := newBusinessStreamServer(
		cfg,
		services,
		resourceSupported,
		identity.ClusterID,
		logger,
	)
	if err != nil {
		return err
	}
	businessContext, cancelBusiness := context.WithCancel(ctx)
	businessErrors := make(chan error, 1)
	businessDone := make(chan struct{})
	go func() {
		defer close(businessDone)
		businessErrors <- runBusinessStreamServer(
			businessContext,
			businessServer,
			connection,
		)
	}()
	defer func() {
		cancelBusiness()
		_ = connection.CloseWithError(
			agentprotocol.CloseNormal,
			"Agent connection closed",
		)
		<-businessDone
	}()

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
	// From here on the Control Stream is read continuously by its own
	// goroutine, so a Server-initiated frame is handled when it arrives rather
	// than at the next heartbeat tick.
	reader := startControlReader(controlStream)
	defer reader.stop()

	renewalSupported := store != nil && serverSupportsCapability(
		serverHello,
		agentprotocol.CapabilityCertificateRenewal,
	)
	if renewalRequired && renewalSupported {
		renewed, err := renewAgentCertificate(
			ctx,
			cfg,
			store,
			controlStream,
			reader,
			identity,
		)
		if err != nil {
			return err
		}
		return &certificateRenewedError{identity: renewed}
	}
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	// The acknowledgement timer runs only while a heartbeat is outstanding. It
	// replaces the read deadline the lock-step loop used, and reports the same
	// condition: the Server stopped answering.
	acknowledgement := time.NewTimer(heartbeatTimeout)
	stopTimer(acknowledgement)
	defer acknowledgement.Stop()

	var sentSequence, acknowledgedSequence uint64
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
		case err := <-businessErrors:
			if err != nil {
				return err
			}
			return errors.New("Agent business Stream accept loop stopped")
		case err := <-reader.failures:
			return err
		case goAway := <-reader.goAways:
			return &GoAwayError{Reason: goAway.GetReason()}
		case <-reader.renewals:
			// A renewal response that nothing asked for means the two sides
			// disagree about the exchange in progress.
			return ErrControlFrameUnexpected
		case acknowledged := <-reader.heartbeatAcks:
			sequence := acknowledged.GetSequence()
			// Acknowledgements are cumulative and monotonic: anything that goes
			// backwards, repeats, or claims a heartbeat that was never sent is
			// a protocol violation.
			if sequence <= acknowledgedSequence || sequence > sentSequence {
				return ErrHeartbeatAckInvalid
			}
			acknowledgedSequence = sequence
			if acknowledgedSequence == sentSequence {
				stopTimer(acknowledgement)
				continue
			}
			resetTimer(acknowledgement, heartbeatTimeout)
		case <-acknowledgement.C:
			return ErrHeartbeatAckTimeout
		case <-ticker.C:
			if renewalSupported &&
				time.Until(identity.CertificateExpiresAt) <=
					cfg.CertificateRenewBefore {
				renewed, err := renewAgentCertificate(
					ctx,
					cfg,
					store,
					controlStream,
					reader,
					identity,
				)
				if err != nil {
					return err
				}
				return &certificateRenewedError{identity: renewed}
			}
			sentSequence++
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
						Sequence:        sentSequence,
						SentAtUnixMilli: now.UnixMilli(),
						Health:          agentv1.HealthStatus_HEALTH_STATUS_HEALTHY,
					},
				},
			}); err != nil {
				return err
			}
			if sentSequence == acknowledgedSequence+1 {
				resetTimer(acknowledgement, heartbeatTimeout)
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
	reader *controlReader,
	identity LocalIdentity,
) (LocalIdentity, error) {
	csrPEM, err := store.LoadOrCreateRenewalCSR(ctx, identity)
	if err != nil {
		return LocalIdentity{}, err
	}
	// Only the write side is bounded here. The read belongs to the Control
	// Stream reader, and a read deadline set from this goroutine would abort
	// the frame it is blocked on.
	if err := controlStream.SetWriteDeadline(
		time.Now().Add(cfg.Connection.ConnectTimeout),
	); err != nil {
		return LocalIdentity{}, fmt.Errorf(
			"set Agent certificate renewal write deadline: %w",
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
	response, err := awaitRenewalResponse(ctx, cfg, reader)
	if err != nil {
		return LocalIdentity{}, err
	}
	if strings.TrimSpace(response.GetCertificatePem()) == "" ||
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
	if err := controlStream.SetWriteDeadline(time.Time{}); err != nil {
		return LocalIdentity{}, fmt.Errorf(
			"clear Agent certificate renewal write deadline: %w",
			err,
		)
	}
	return renewed, nil
}

// awaitRenewalResponse waits for the Server's answer while still reacting to
// everything else the Control Stream can carry. Waiting only for the renewal
// would leave a GoAway or a read failure unnoticed until the exchange timed
// out, and would stall the reader on an undelivered heartbeat acknowledgement.
func awaitRenewalResponse(
	ctx context.Context,
	cfg Config,
	reader *controlReader,
) (*agentv1.CertificateRenewalResponse, error) {
	timeout := time.NewTimer(cfg.Connection.ConnectTimeout)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case response := <-reader.renewals:
			return response, nil
		case err := <-reader.failures:
			return nil, err
		case goAway := <-reader.goAways:
			return nil, &GoAwayError{Reason: goAway.GetReason()}
		case <-reader.heartbeatAcks:
			// An acknowledgement for a heartbeat sent before the renewal
			// started. Nothing is waiting for it, but it must be consumed so
			// the reader can move on to the renewal response.
		case <-timeout.C:
			return nil, errors.New(
				"Server did not answer the Agent certificate renewal in time",
			)
		}
	}
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

func validateServerHello(
	protocolVersion uint32,
	hello *agentv1.ServerHello,
) (time.Duration, time.Duration, error) {
	if protocolVersion != agentprotocol.ProtocolVersion ||
		hello == nil ||
		!validation.IsUUID(hello.GetConnectionId()) {
		return 0, 0, ErrServerHelloInvalid
	}
	heartbeatInterval := time.Duration(hello.GetHeartbeatIntervalMillis()) * time.Millisecond
	heartbeatTimeout := time.Duration(hello.GetHeartbeatTimeoutMillis()) * time.Millisecond
	if heartbeatInterval < time.Second ||
		heartbeatInterval > 5*time.Minute ||
		heartbeatTimeout <= heartbeatInterval ||
		heartbeatTimeout > 15*time.Minute {
		return 0, 0, ErrHeartbeatConfigInvalid
	}
	if len(hello.GetCapabilities()) > 64 {
		return 0, 0, fmt.Errorf("%w: capability count exceeds the limit", ErrServerCapability)
	}
	for _, capability := range hello.GetCapabilities() {
		if capability == "" ||
			strings.TrimSpace(capability) != capability ||
			len(capability) > 128 {
			return 0, 0, ErrServerCapability
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
