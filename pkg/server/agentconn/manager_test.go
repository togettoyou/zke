package agentconn

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/url"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
)

const (
	testTenantID  = "00000000-0000-4000-8000-000000000001"
	testProjectID = "00000000-0000-4000-8000-000000000002"
	testClusterID = "00000000-0000-4000-8000-000000000003"
	testAgentID   = "00000000-0000-4000-8000-000000000004"
	testStartupID = "00000000-0000-4000-8000-000000000005"
)

func TestIdentityFromCertificate(t *testing.T) {
	t.Parallel()

	identityURI, err := url.Parse(
		"zke://agent/tenants/" + testTenantID +
			"/projects/" + testProjectID +
			"/clusters/" + testClusterID +
			"/agents/" + testAgentID,
	)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := identityFromCertificate(&x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: testAgentID},
		URIs:         []*url.URL{identityURI},
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.TenantID != testTenantID ||
		identity.ProjectID != testProjectID ||
		identity.ClusterID != testClusterID ||
		identity.AgentID != testAgentID ||
		identity.CertificateSerial != "42" {
		t.Fatalf("unexpected certificate identity: %+v", identity)
	}
}

func TestValidateClientHelloRejectsCertificateMismatch(t *testing.T) {
	t.Parallel()

	err := validateClientHello(
		agentprotocol.ProtocolVersion,
		&agentv1.ClientHello{
			AgentId:      "00000000-0000-4000-8000-000000000099",
			ClusterId:    testClusterID,
			AgentVersion: "development",
			StartupId:    testStartupID,
		},
		certificateIdentity{
			AgentConnectionIdentity: store.AgentConnectionIdentity{
				TenantID:  testTenantID,
				ProjectID: testProjectID,
				ClusterID: testClusterID,
				AgentID:   testAgentID,
			},
		},
	)
	if err == nil {
		t.Fatal("validateClientHello() accepted an Agent ID mismatch")
	}
}

func TestHealthStatusValue(t *testing.T) {
	t.Parallel()

	if value, err := healthStatusValue(
		agentv1.HealthStatus_HEALTH_STATUS_HEALTHY,
	); err != nil || value != "healthy" {
		t.Fatalf("healthStatusValue() = %q, %v", value, err)
	}
	if _, err := healthStatusValue(
		agentv1.HealthStatus_HEALTH_STATUS_UNSPECIFIED,
	); err == nil {
		t.Fatal("healthStatusValue() accepted an unspecified status")
	}
}

func TestManagerConnectionSnapshotLifecycle(t *testing.T) {
	t.Parallel()

	connectedAt := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	heartbeatAt := connectedAt.Add(10 * time.Second)
	manager := &Manager{
		connections:      make(map[string]*session),
		lastDisconnected: make(map[string]ConnectionStatus),
		subscribers:      make(map[uint64]chan ConnectionEvent),
	}
	events, unsubscribe := manager.Subscribe()
	defer unsubscribe()
	current := &session{
		id: "connection-1",
		identity: store.AgentConnectionIdentity{
			TenantID:  testTenantID,
			ProjectID: testProjectID,
			ClusterID: testClusterID,
			AgentID:   testAgentID,
		},
		connectedAt:      connectedAt,
		disconnectReason: "connection_closed",
	}
	if previous := manager.register(current); previous != nil {
		t.Fatal("first connection unexpectedly replaced another session")
	}
	connectedEvent := <-events
	if connectedEvent.AgentID != testAgentID ||
		connectedEvent.State != ConnectionStateOnline {
		t.Fatalf("unexpected connected event: %+v", connectedEvent)
	}
	current.recordHeartbeat(heartbeatAt)

	online := manager.Snapshot([]string{testAgentID})[testAgentID]
	if online.State != "online" ||
		online.ConnectionID != "connection-1" ||
		!online.ConnectedAt.Equal(connectedAt) ||
		!online.LastHeartbeatAt.Equal(heartbeatAt) {
		t.Fatalf("unexpected online snapshot: %+v", online)
	}

	current.setDisconnectReason("agent_revoked")
	current.setDisconnectReason("credential_revoked")
	manager.unregister(current)
	disconnectedEvent := <-events
	if disconnectedEvent.AgentID != testAgentID ||
		disconnectedEvent.State != ConnectionStateOffline {
		t.Fatalf("unexpected disconnected event: %+v", disconnectedEvent)
	}
	offline := manager.Snapshot([]string{testAgentID})[testAgentID]
	if offline.State != "offline" ||
		offline.ConnectionID != "" ||
		offline.LastDisconnectedAt.IsZero() ||
		offline.LastDisconnectReason != "agent_revoked" ||
		!offline.LastHeartbeatAt.Equal(heartbeatAt) {
		t.Fatalf("unexpected offline snapshot: %+v", offline)
	}

	unknownID := "00000000-0000-4000-8000-000000000099"
	unknown := manager.Snapshot([]string{unknownID})[unknownID]
	if unknown.State != "offline" ||
		!unknown.LastDisconnectedAt.IsZero() ||
		unknown.LastDisconnectReason != "" {
		t.Fatalf("unexpected unknown Agent snapshot: %+v", unknown)
	}
}
