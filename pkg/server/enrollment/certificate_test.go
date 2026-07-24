package enrollment

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func TestCertificateSignerIssuesScopedClientCertificate(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	caPEM, caKeyPEM := createTestCA(t, now)
	signer, err := NewCertificateSigner(caPEM, caKeyPEM, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	csr, csrPublicKey := createSignerTestCSR(t)
	identity := CertificateIdentity{
		TenantID:  "00000000-0000-4000-8000-000000000001",
		ProjectID: "00000000-0000-4000-8000-000000000002",
		ClusterID: "00000000-0000-4000-8000-000000000003",
		AgentID:   "00000000-0000-4000-8000-000000000004",
	}
	signed, err := signer.Sign(csr, identity, now)
	if err != nil {
		t.Fatal(err)
	}
	if signed.Serial == "" || signed.ExpiresAt != now.Add(24*time.Hour) {
		t.Fatalf("unexpected signed certificate metadata: %+v", signed)
	}

	leafBlock, remaining := pem.Decode([]byte(signed.PEM))
	if leafBlock == nil || leafBlock.Type != "CERTIFICATE" {
		t.Fatal("signed certificate has no leaf certificate")
	}
	if len(bytes.TrimSpace(remaining)) == 0 {
		t.Fatal("signed certificate does not include the CA chain")
	}
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Subject.CommonName != identity.AgentID ||
		len(leaf.URIs) != 1 ||
		leaf.URIs[0].String() !=
			"zke://agent/tenants/"+identity.TenantID+
				"/projects/"+identity.ProjectID+
				"/clusters/"+identity.ClusterID+
				"/agents/"+identity.AgentID {
		t.Fatalf("certificate identity = %#v / %#v", leaf.Subject, leaf.URIs)
	}
	leafPublicKey, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	expectedPublicKey, err := x509.MarshalPKIXPublicKey(csrPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leafPublicKey, expectedPublicKey) {
		t.Fatal("signed certificate does not contain the CSR public key")
	}

	caBlock, _ := pem.Decode(caPEM)
	caCertificate, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCertificate)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("verify signed client certificate: %v", err)
	}
}

func TestCertificateSignerRejectsMismatchedCAKey(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	caPEM, _ := createTestCA(t, now)
	_, differentKeyPEM := createTestCA(t, now)
	if _, err := NewCertificateSigner(
		caPEM,
		differentKeyPEM,
		DefaultCertificateTTL,
	); err == nil {
		t.Fatal("NewCertificateSigner() accepted a mismatched CA private key")
	}
}

func TestCertificateSignerRejectsWeakAgentRSAKey(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	caPEM, caKeyPEM := createTestCA(t, now)
	signer, err := NewCertificateSigner(caPEM, caKeyPEM, DefaultCertificateTTL)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(
		rand.Reader,
		&x509.CertificateRequest{},
		privateKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatal(err)
	}
	weakCSRPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	})
	if _, _, err := parseCertificateRequest(weakCSRPEM); err == nil {
		t.Fatal("parseCertificateRequest() accepted a 1024-bit Agent RSA key")
	}
	_, err = signer.Sign(csr, CertificateIdentity{
		TenantID:  "00000000-0000-4000-8000-000000000001",
		ProjectID: "00000000-0000-4000-8000-000000000002",
		ClusterID: "00000000-0000-4000-8000-000000000003",
		AgentID:   "00000000-0000-4000-8000-000000000004",
	}, now)
	if err == nil {
		t.Fatal("Sign() accepted a 1024-bit Agent RSA key")
	}
}

func createTestCA(t *testing.T, now time.Time) ([]byte, []byte) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ZKE Test Agent CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		SubjectKeyId:          []byte("zke-test-agent-ca"),
	}
	certificateDER, err := x509.CreateCertificate(
		rand.Reader,
		template,
		template,
		&privateKey.PublicKey,
		privateKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: certificateDER,
		}), pem.EncodeToMemory(&pem.Block{
			Type:  "PRIVATE KEY",
			Bytes: privateKeyDER,
		})
}

func createSignerTestCSR(t *testing.T) (*x509.CertificateRequest, any) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(
		rand.Reader,
		&x509.CertificateRequest{
			Subject: pkix.Name{CommonName: "ignored-agent-controlled-subject"},
		},
		privateKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatal(err)
	}
	return csr, &privateKey.PublicKey
}
