package agentconn

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/url"
	"testing"

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
