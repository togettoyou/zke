package agentconn

import (
	"bytes"
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/requestctx"
)

func TestSessionDrainWaitsForInFlightResources(t *testing.T) {
	t.Parallel()

	current := &session{}
	first := current.beginResource()
	second := current.beginResource()
	if !first || !second {
		t.Fatal("session rejected a Resource request before draining")
	}
	var finished atomic.Uint64
	current.startDrain(time.Second, func() {
		finished.Add(1)
	})
	if current.beginResource() {
		t.Fatal("draining session accepted a new Resource request")
	}
	current.endResource()
	if finished.Load() != 0 {
		t.Fatal("session drained before all Resource requests ended")
	}
	current.endResource()
	if finished.Load() != 1 {
		t.Fatalf("drain completion calls = %d, want 1", finished.Load())
	}
}

func TestResourceRequestIDReusesHTTPCorrelationID(t *testing.T) {
	t.Parallel()

	const requestID = "00000000-0000-4000-8000-000000000099"
	got, err := resourceRequestID(requestctx.WithID(context.Background(), requestID))
	if err != nil {
		t.Fatal(err)
	}
	if got != requestID {
		t.Fatalf("resourceRequestID() = %q, want %q", got, requestID)
	}

	fallback, err := resourceRequestID(
		requestctx.WithID(context.Background(), "not-a-uuid"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if fallback == requestID || len(fallback) != 36 {
		t.Fatalf("fallback Resource request ID = %q", fallback)
	}
}

func TestSessionDrainHasBoundedDeadline(t *testing.T) {
	t.Parallel()

	current := &session{}
	if !current.beginResource() {
		t.Fatal("session rejected a Resource request before draining")
	}
	finished := make(chan struct{})
	current.startDrain(20*time.Millisecond, func() {
		close(finished)
	})
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("session drain did not honor its deadline")
	}
	current.endResource()
}

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

func TestManagerParentScopeSuspensionClosesOnlyMatchingConnections(t *testing.T) {
	t.Parallel()

	const (
		tenantB  = "00000000-0000-4000-8000-000000000011"
		projectB = "00000000-0000-4000-8000-000000000012"
		projectC = "00000000-0000-4000-8000-000000000013"
		clusterB = "00000000-0000-4000-8000-000000000014"
		clusterC = "00000000-0000-4000-8000-000000000015"
		agentB   = "00000000-0000-4000-8000-000000000016"
		agentC   = "00000000-0000-4000-8000-000000000017"
	)

	tests := []struct {
		name        string
		event       store.AgentConnectionRevocation
		closedAgent map[string]bool
	}{
		{
			name: "Project suspension",
			event: store.AgentConnectionRevocation{
				ProjectID: testProjectID,
				Reason:    agentprotocol.GoAwayScopeSuspended,
			},
			closedAgent: map[string]bool{testAgentID: true},
		},
		{
			name: "Tenant suspension",
			event: store.AgentConnectionRevocation{
				TenantID: testTenantID,
				Reason:   agentprotocol.GoAwayScopeSuspended,
			},
			closedAgent: map[string]bool{
				testAgentID: true,
				agentB:      true,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			sessions := []*session{
				newRevocationTestSession(
					testTenantID,
					testProjectID,
					testClusterID,
					testAgentID,
				),
				newRevocationTestSession(
					testTenantID,
					projectB,
					clusterB,
					agentB,
				),
				newRevocationTestSession(
					tenantB,
					projectC,
					clusterC,
					agentC,
				),
			}
			manager := &Manager{connections: make(map[string]*session)}
			for _, current := range sessions {
				manager.connections[current.identity.AgentID] = current
			}

			manager.handleRevocation(test.event)

			for _, current := range sessions {
				agentID := current.identity.AgentID
				wantClosed := test.closedAgent[agentID]
				connection := current.conn.(*recordingManagedConnection)
				if gotClosed := len(connection.closeCodes) == 1; gotClosed != wantClosed {
					t.Errorf(
						"Agent %s closed = %t, want %t",
						agentID,
						gotClosed,
						wantClosed,
					)
				}
				if !wantClosed {
					if current.disconnectReason != "connection_closed" {
						t.Errorf(
							"Agent %s disconnect reason = %q, want unchanged",
							agentID,
							current.disconnectReason,
						)
					}
					continue
				}
				if connection.closeCodes[0] != agentprotocol.CloseScopeSuspended ||
					connection.closeReasons[0] != "Agent scope suspended" {
					t.Errorf(
						"Agent %s close = %d/%q",
						agentID,
						connection.closeCodes[0],
						connection.closeReasons[0],
					)
				}
				if current.disconnectReason != agentprotocol.GoAwayScopeSuspended {
					t.Errorf(
						"Agent %s disconnect reason = %q",
						agentID,
						current.disconnectReason,
					)
				}
				frame, err := agentprotocol.ReadFrame(
					&current.stream.(*recordingControlStream).Buffer,
				)
				if err != nil {
					t.Errorf("Agent %s GoAway frame: %v", agentID, err)
				} else if frame.GetGoAway().GetReason() !=
					agentprotocol.GoAwayScopeSuspended {
					t.Errorf("Agent %s GoAway = %+v", agentID, frame.GetGoAway())
				}
			}
		})
	}
}

type recordingManagedConnection struct {
	closeCodes   []quic.ApplicationErrorCode
	closeReasons []string
}

func (*recordingManagedConnection) Context() context.Context {
	return context.Background()
}

func (connection *recordingManagedConnection) CloseWithError(
	code quic.ApplicationErrorCode,
	reason string,
) error {
	connection.closeCodes = append(connection.closeCodes, code)
	connection.closeReasons = append(connection.closeReasons, reason)
	return nil
}

type recordingControlStream struct {
	bytes.Buffer
}

func (*recordingControlStream) SetReadDeadline(time.Time) error {
	return nil
}

func (*recordingControlStream) SetWriteDeadline(time.Time) error {
	return nil
}

func newRevocationTestSession(
	tenantID string,
	projectID string,
	clusterID string,
	agentID string,
) *session {
	return &session{
		id: agentID + "-connection",
		identity: store.AgentConnectionIdentity{
			TenantID:  tenantID,
			ProjectID: projectID,
			ClusterID: clusterID,
			AgentID:   agentID,
		},
		conn:             &recordingManagedConnection{},
		stream:           &recordingControlStream{},
		disconnectReason: "connection_closed",
	}
}
