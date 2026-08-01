package agent

import (
	"context"
	"log/slog"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

type connectionServices struct {
	resourceHandler      agentprotocol.ResourceHandler
	podLogsHandler       agentprotocol.PodLogsHandler
	resourceWatchHandler agentprotocol.ResourceWatchHandler
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
	if services.resourceWatchHandler != nil {
		handlers[agentv1.StreamKind_STREAM_KIND_RESOURCE_WATCH] =
			agentprotocol.StreamHandlerConfig{
				MaxConcurrent: cfg.Connection.MaxConcurrentResourceWatchStreams,
				MaxTimeout:    cfg.Connection.MaxResourceWatchStreamTimeout,
				Handle:        agentprotocol.ResourceWatchStreamHandler(services.resourceWatchHandler),
			}
	}
	return agentprotocol.NewStreamServer(agentprotocol.StreamServerConfig{
		HeaderTimeout: cfg.Connection.StreamHeaderTimeout,
		MaxTimeout: max(
			cfg.Connection.MaxResourceRequestTimeout,
			cfg.Connection.MaxPodLogsStreamTimeout,
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
