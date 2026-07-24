package agent

import (
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

func TestValidateServerHello(t *testing.T) {
	t.Parallel()

	interval, timeout, err := validateServerHello(
		agentprotocol.ProtocolVersion,
		&agentv1.ServerHello{
			ConnectionId:            "00000000-0000-4000-8000-000000000001",
			HeartbeatIntervalMillis: 10_000,
			HeartbeatTimeoutMillis:  30_000,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if interval != 10*time.Second || timeout != 30*time.Second {
		t.Fatalf("unexpected heartbeat durations: %s / %s", interval, timeout)
	}
}

func TestValidateServerHelloRejectsUnsafeHeartbeatConfiguration(t *testing.T) {
	t.Parallel()

	_, _, err := validateServerHello(
		agentprotocol.ProtocolVersion,
		&agentv1.ServerHello{
			ConnectionId:            "00000000-0000-4000-8000-000000000001",
			HeartbeatIntervalMillis: 30_000,
			HeartbeatTimeoutMillis:  30_000,
		},
	)
	if err == nil {
		t.Fatal("validateServerHello() accepted heartbeat interval at timeout")
	}
}
