package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

type connectionServices struct {
	resourceHandler        agentprotocol.ResourceHandler
	podLogsHandler         agentprotocol.PodLogsHandler
	podExecHandler         agentprotocol.PodExecHandler
	podPortForwardHandler  agentprotocol.PodPortForwardHandler
	resourceWatchHandler   agentprotocol.ResourceWatchHandler
	terminalSessionHandler agentprotocol.TerminalSessionHandler
	// metricsForwarder is not a Stream handler: metrics travel on a Stream
	// this Agent opens, so it holds the Connection rather than serving one.
	metricsForwarder        *metricsIngestForwarder
	metricsCollectorHandler agentprotocol.MetricsCollectorHandler
}

func newBusinessStreamServer(
	cfg Config,
	services connectionServices,
	resourceSupported bool,
	clusterID string,
	logger *slog.Logger,
) (*agentprotocol.StreamServer, error) {
	handlers := make(
		map[agentv1.StreamKind]agentprotocol.StreamHandlerConfig,
		3,
	)
	if resourceSupported && services.resourceHandler != nil {
		handlers[agentv1.StreamKind_STREAM_KIND_RESOURCE] =
			agentprotocol.StreamHandlerConfig{
				MaxConcurrent: cfg.Connection.MaxConcurrentResourceStreams,
				MaxTimeout:    cfg.Connection.MaxResourceRequestTimeout,
				Handle: agentprotocol.ResourceStreamHandler(
					cfg.Connection.MaxResourceBodyBytes,
					services.resourceHandler,
				),
			}
	}
	if services.podLogsHandler != nil {
		handlers[agentv1.StreamKind_STREAM_KIND_POD_LOGS] =
			agentprotocol.StreamHandlerConfig{
				MaxConcurrent: cfg.Connection.MaxConcurrentPodLogsStreams,
				MaxTimeout:    cfg.Connection.MaxPodLogsStreamTimeout,
				Handle: agentprotocol.PodLogsStreamHandler(
					cfg.Connection.MaxPodLogBytes,
					services.podLogsHandler,
				),
			}
	}
	if services.podExecHandler != nil {
		handlers[agentv1.StreamKind_STREAM_KIND_POD_EXEC] =
			agentprotocol.StreamHandlerConfig{
				MaxConcurrent: cfg.Connection.MaxConcurrentPodExecStreams,
				MaxTimeout:    cfg.Connection.MaxPodExecStreamTimeout,
				Handle: agentprotocol.PodExecStreamHandler(
					cfg.Connection.MaxPodExecInputBytes,
					cfg.Connection.MaxPodExecOutputBytes,
					services.podExecHandler,
				),
			}
	}
	if services.podPortForwardHandler != nil {
		handlers[agentv1.StreamKind_STREAM_KIND_POD_PORT_FORWARD] =
			agentprotocol.StreamHandlerConfig{
				MaxConcurrent: cfg.Connection.MaxConcurrentPodAccessStreams,
				MaxTimeout:    cfg.Connection.MaxPodAccessStreamTimeout,
				Handle: agentprotocol.PodPortForwardStreamHandler(
					cfg.Connection.MaxPodAccessClientBytes,
					cfg.Connection.MaxPodAccessPodBytes,
					services.podPortForwardHandler,
					podAccessStreamObserver(logger, clusterID),
				),
			}
	}
	if services.resourceWatchHandler != nil {
		handlers[agentv1.StreamKind_STREAM_KIND_RESOURCE_WATCH] =
			agentprotocol.StreamHandlerConfig{
				MaxConcurrent: cfg.Connection.MaxConcurrentResourceWatchStreams,
				MaxTimeout:    cfg.Connection.MaxResourceWatchStreamTimeout,
				Handle:        agentprotocol.ResourceWatchStreamHandler(services.resourceWatchHandler),
			}
	}
	if services.metricsCollectorHandler != nil {
		handlers[agentv1.StreamKind_STREAM_KIND_METRICS_COLLECTOR] =
			agentprotocol.StreamHandlerConfig{
				// Installing or removing the collector is a handful of
				// Kubernetes writes; one at a time is enough, and it keeps two
				// concurrent installs from racing over the same objects.
				MaxConcurrent: 1,
				MaxTimeout:    cfg.Connection.MaxResourceRequestTimeout,
				Handle: agentprotocol.MetricsCollectorStreamHandler(
					services.metricsCollectorHandler,
				),
			}
	}
	if services.terminalSessionHandler != nil {
		handlers[agentv1.StreamKind_STREAM_KIND_TERMINAL_SESSION] =
			agentprotocol.StreamHandlerConfig{
				MaxConcurrent: 4,
				MaxTimeout:    cfg.Connection.MaxResourceRequestTimeout,
				Handle:        agentprotocol.TerminalSessionStreamHandler(services.terminalSessionHandler),
			}
	}
	return agentprotocol.NewStreamServer(agentprotocol.StreamServerConfig{
		HeaderTimeout: cfg.Connection.StreamHeaderTimeout,
		MaxTimeout: max(
			cfg.Connection.MaxResourceRequestTimeout,
			cfg.Connection.MaxPodLogsStreamTimeout,
			cfg.Connection.MaxPodExecStreamTimeout,
			cfg.Connection.MaxPodAccessStreamTimeout,
			cfg.Connection.MaxResourceWatchStreamTimeout,
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
			if clusterID != "" {
				attributes = append(
					attributes,
					slog.String("cluster_id", clusterID),
				)
			}
			logger.Debug("Agent business Stream stopped", attributes...)
		},
	})
}

func podAccessStreamObserver(logger *slog.Logger, clusterID string) agentprotocol.PodPortForwardObserver {
	return agentprotocol.PodPortForwardObserver{
		Opened: func(observation agentprotocol.PodPortForwardObservation) {
			if logger == nil {
				return
			}
			attributes := append(
				podAccessStreamAttributes(clusterID, observation),
				slog.Duration("setup_duration", observation.Duration),
			)
			logger.Debug("Pod access upstream opened", attributes...)
		},
		Closed: func(observation agentprotocol.PodPortForwardObservation) {
			if logger == nil {
				return
			}
			attributes := podAccessStreamAttributes(clusterID, observation)
			attributes = append(attributes, slog.Duration("duration", observation.Duration))
			level := slog.LevelDebug
			message := "Pod access upstream closed"
			result, reason := podAccessStreamResult(observation)
			attributes = append(attributes, slog.String("result", result), slog.String("reason", reason))
			if observation.Exit != nil {
				attributes = append(attributes,
					slog.Uint64("client_bytes", observation.Exit.GetClientBytes()),
					slog.Uint64("pod_bytes", observation.Exit.GetPodBytes()),
				)
			}
			if observation.Err != nil {
				attributes = append(attributes, slog.String("error", observation.Err.Error()))
			}
			if result != "ok" && result != "canceled" {
				level = slog.LevelWarn
				message = "Pod access upstream failed"
			}
			logger.Log(context.Background(), level, message, attributes...)
		},
	}
}

func podAccessStreamAttributes(clusterID string, observation agentprotocol.PodPortForwardObservation) []any {
	request := observation.Request
	header := observation.Header
	attributes := []any{slog.String("cluster_id", clusterID)}
	if header != nil {
		attributes = append(attributes, slog.String("request_id", header.GetRequestId()))
	}
	if request != nil {
		attributes = append(attributes,
			slog.String("namespace", request.GetNamespace()),
			slog.String("pod_name", request.GetPodName()),
			slog.String("pod_uid", request.GetPodUid()),
			slog.Int("pod_port", int(request.GetPort())),
		)
	}
	return attributes
}

func podAccessStreamResult(observation agentprotocol.PodPortForwardObservation) (string, string) {
	if observation.Exit != nil {
		reason := observation.Exit.GetReason()
		if reason == "" {
			reason = "completed"
		}
		return agentResultName(observation.Exit.GetResult()), reason
	}
	if observation.Response != nil && observation.Response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		reason := observation.Response.GetReason()
		if reason == "" {
			reason = "rejected"
		}
		return agentResultName(observation.Response.GetResult()), reason
	}
	if observation.Err != nil {
		if errors.Is(observation.Err, context.Canceled) {
			return "canceled", "stream_canceled"
		}
		return "failed", "stream_error"
	}
	return "ok", "completed"
}

func agentResultName(result agentv1.ResultCode) string {
	return strings.ToLower(strings.TrimPrefix(result.String(), "RESULT_CODE_"))
}

func runBusinessStreamServer(
	ctx context.Context,
	server *agentprotocol.StreamServer,
	connection *quic.Conn,
) error {
	if server == nil {
		return nil
	}
	return server.Serve(ctx, connection)
}
