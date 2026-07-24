package agent

import (
	"encoding/pem"
	"os"
	"path/filepath"
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

func BenchmarkConnectionTLSConfig(b *testing.B) {
	pending, err := newPendingIdentity()
	if err != nil {
		b.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	certificateChainPEM, expiresAt := issueTestAgentCertificate(
		b,
		*pending,
		now,
	)
	leafBlock, caPEM := pem.Decode(certificateChainPEM)
	if leafBlock == nil || len(caPEM) == 0 {
		b.Fatal("test Agent certificate chain is invalid")
	}
	caFile := filepath.Join(b.TempDir(), "agent-listener-ca.crt")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		b.Fatal(err)
	}
	cfg := Config{
		Connection: ConnectionConfig{
			ServerAddress:     "127.0.0.1:8443",
			CACertificateFile: caFile,
		},
	}
	identity := LocalIdentity{
		ClusterID:     testClusterID,
		AgentID:       testAgentID,
		PrivateKeyPEM: pending.PrivateKeyPEM,
		CertificatePEM: pem.EncodeToMemory(&pem.Block{
			Type:  leafBlock.Type,
			Bytes: leafBlock.Bytes,
		}),
		CertificateExpiresAt: expiresAt,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := connectionTLSConfig(cfg, identity); err != nil {
			b.Fatal(err)
		}
	}
}
