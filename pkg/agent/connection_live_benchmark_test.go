package agent

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/identifier"
	"k8s.io/client-go/kubernetes"
)

// BenchmarkLiveAgentHeartbeatRoundTrip measures the currently implemented
// Agent-to-Server control-stream request/ack path against running processes.
// It is opt-in because it requires a live Server, Kubernetes Secrets, and an
// enrolled identity.
func BenchmarkLiveAgentHeartbeatRoundTrip(b *testing.B) {
	configPath := os.Getenv("ZKE_LIVE_AGENT_CONFIG")
	if configPath == "" {
		b.Skip("ZKE_LIVE_AGENT_CONFIG is not configured")
	}
	cfg, err := LoadConfig([]string{"--config", configPath})
	if err != nil {
		b.Fatal(err)
	}
	kubernetesConfig, err := loadKubernetesConfig(cfg.KubeconfigFile)
	if err != nil {
		b.Fatal(err)
	}
	kubernetesClient, err := kubernetes.NewForConfig(kubernetesConfig)
	if err != nil {
		b.Fatal(err)
	}
	store := NewIdentityStore(
		kubernetesClient,
		cfg.IdentityNamespace,
		cfg.IdentitySecretName,
	)
	state, err := store.LoadOrCreatePending(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	if state.Identity == nil {
		b.Fatal("live benchmark requires an enrolled Agent identity")
	}
	if err := loadTrust(context.Background(), kubernetesClient, &cfg); err != nil {
		b.Fatal(err)
	}
	tlsConfig, err := connectionTLSConfig(cfg, *state.Identity)
	if err != nil {
		b.Fatal(err)
	}
	connection, err := quic.DialAddr(
		context.Background(),
		cfg.ConnectionServerAddress(),
		tlsConfig,
		&quic.Config{
			HandshakeIdleTimeout: cfg.Connection.ConnectTimeout,
			MaxIdleTimeout:       15 * time.Minute,
			KeepAlivePeriod:      10 * time.Second,
		},
	)
	if err != nil {
		b.Fatal(err)
	}
	defer connection.CloseWithError(
		agentprotocol.CloseNormal,
		"benchmark complete",
	)
	stream, err := connection.OpenStreamSync(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	defer stream.Close()
	startupID, err := identifier.NewUUID()
	if err != nil {
		b.Fatal(err)
	}
	if err := agentprotocol.WriteFrame(stream, &agentv1.ControlFrame{
		ProtocolVersion: agentprotocol.ProtocolVersion,
		Message: &agentv1.ControlFrame_ClientHello{
			ClientHello: &agentv1.ClientHello{
				AgentId:      state.Identity.AgentID,
				ClusterId:    state.Identity.ClusterID,
				AgentVersion: "live-benchmark",
				StartupId:    startupID,
			},
		},
	}); err != nil {
		b.Fatal(err)
	}
	frame, err := agentprotocol.ReadFrame(stream)
	if err != nil {
		b.Fatal(err)
	}
	if _, _, err := validateServerHello(
		frame.GetProtocolVersion(),
		frame.GetServerHello(),
	); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		sequence := uint64(index + 1)
		if err := stream.SetDeadline(
			time.Now().Add(cfg.Connection.ConnectTimeout),
		); err != nil {
			b.Fatal(err)
		}
		if err := agentprotocol.WriteFrame(stream, &agentv1.ControlFrame{
			ProtocolVersion: agentprotocol.ProtocolVersion,
			Message: &agentv1.ControlFrame_Heartbeat{
				Heartbeat: &agentv1.Heartbeat{
					Sequence:        sequence,
					SentAtUnixMilli: time.Now().UnixMilli(),
					Health: agentv1.
						HealthStatus_HEALTH_STATUS_HEALTHY,
				},
			},
		}); err != nil {
			b.Fatal(err)
		}
		response, err := agentprotocol.ReadFrame(stream)
		if err != nil {
			b.Fatal(err)
		}
		ack := response.GetHeartbeatAck()
		if response.GetProtocolVersion() != agentprotocol.ProtocolVersion ||
			ack == nil ||
			ack.GetSequence() != sequence {
			b.Fatal(fmt.Errorf(
				"unexpected heartbeat acknowledgement for sequence %d",
				sequence,
			))
		}
	}
}
