package enrollment

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"
)

func TestNewTokenUsesSHA256Digest(t *testing.T) {
	t.Parallel()

	token, digest, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("token is empty")
	}
	expected := sha256.Sum256([]byte(token))
	if string(digest) != string(expected[:]) {
		t.Fatal("token digest does not match token")
	}
}

func TestCreateRejectsInvalidInputBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	service := NewService(nil, ServiceConfig{TokenTTL: DefaultTokenTTL})
	_, err := service.Create(context.Background(), CreateInput{
		ProjectID:      "not-a-uuid",
		ClusterName:    "test-cluster",
		UserID:         "00000000-0000-0000-0000-000000000001",
		RequestID:      "request-1",
		IdempotencyKey: "01234567-89ab-cdef-0123-456789abcdef",
		Now:            time.Now(),
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Create() error = %v, want ErrInvalidInput", err)
	}
}

func TestCreateRejectsInvalidTokenTTLBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	service := NewService(nil, ServiceConfig{})
	_, err := service.Create(context.Background(), CreateInput{
		ProjectID:      "00000000-0000-0000-0000-000000000001",
		ClusterName:    "test-cluster",
		UserID:         "00000000-0000-0000-0000-000000000002",
		RequestID:      "request-1",
		IdempotencyKey: "01234567-89ab-cdef-0123-456789abcdef",
		Now:            time.Now(),
	})
	if err == nil {
		t.Fatal("Create() accepted an invalid token TTL")
	}
}

func TestCreateRejectsInvalidIdempotencyKeyBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	service := NewService(nil, ServiceConfig{TokenTTL: DefaultTokenTTL})
	_, err := service.Create(context.Background(), CreateInput{
		ProjectID:      "00000000-0000-0000-0000-000000000001",
		ClusterName:    "test-cluster",
		UserID:         "00000000-0000-0000-0000-000000000002",
		RequestID:      "request-1",
		IdempotencyKey: "too-short",
		Now:            time.Now(),
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Create() error = %v, want ErrInvalidInput", err)
	}
}

func TestCreateRejectsInvalidClusterNameBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	service := NewService(nil, ServiceConfig{TokenTTL: DefaultTokenTTL})
	_, err := service.Create(context.Background(), CreateInput{
		ProjectID:      "00000000-0000-0000-0000-000000000001",
		ClusterName:    " ",
		UserID:         "00000000-0000-0000-0000-000000000002",
		RequestID:      "request-1",
		IdempotencyKey: "01234567-89ab-cdef-0123-456789abcdef",
		Now:            time.Now(),
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Create() error = %v, want ErrInvalidInput", err)
	}
}

func TestBeginRejectsMalformedTokenAndCSRBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	service := NewService(nil, ServiceConfig{TokenTTL: DefaultTokenTTL})
	now := time.Now().UTC()
	validCSR, _ := createTestIdentity(t, now)
	_, err := service.Begin(context.Background(), BeginInput{
		Token:          "not-a-token",
		IdempotencyKey: "01234567-89ab-cdef-0123-456789abcdef",
		CSRPEM:         validCSR,
		RequestID:      "request-enroll",
		Now:            now,
	})
	if !errors.Is(err, ErrTokenRejected) {
		t.Fatalf("malformed token error = %v, want ErrTokenRejected", err)
	}

	tokenValue := make([]byte, tokenBytes)
	if _, err := rand.Read(tokenValue); err != nil {
		t.Fatal(err)
	}
	_, err = service.Begin(context.Background(), BeginInput{
		Token:          base64.RawURLEncoding.EncodeToString(tokenValue),
		IdempotencyKey: "01234567-89ab-cdef-0123-456789abcdef",
		CSRPEM:         []byte("not a CSR"),
		RequestID:      "request-enroll",
		Now:            now,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("malformed CSR error = %v, want ErrInvalidInput", err)
	}
}

func TestCompleteRejectsCertificateForDifferentCSRBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	csrPEM, _ := createTestIdentity(t, now)
	_, certificatePEM := createTestIdentity(t, now)
	service := NewService(nil, ServiceConfig{TokenTTL: DefaultTokenTTL})
	_, err := service.Complete(context.Background(), CompleteInput{
		EnrollmentID:    "00000000-0000-0000-0000-000000000001",
		AttemptID:       "00000000-0000-0000-0000-000000000002",
		IdempotencyKey:  "01234567-89ab-cdef-0123-456789abcdef",
		CSRPEM:          csrPEM,
		ClusterID:       "00000000-0000-0000-0000-000000000003",
		AgentID:         "00000000-0000-0000-0000-000000000004",
		AgentVersion:    "v0.1.0",
		ProtocolVersion: "v1",
		CertificatePEM:  string(certificatePEM),
		RequestID:       "request-complete",
		Now:             now,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("mismatched certificate error = %v, want ErrInvalidInput", err)
	}
}

func createTestIdentity(t *testing.T, now time.Time) ([]byte, []byte) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(
		rand.Reader,
		&x509.CertificateRequest{
			Subject: pkix.Name{CommonName: "zke-agent"},
		},
		privateKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	})
	certificateDER, err := x509.CreateCertificate(
		rand.Reader,
		&x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject:      pkix.Name{CommonName: "zke-agent"},
			NotBefore:    now.Add(-time.Minute),
			NotAfter:     now.Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
		},
		&x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject:      pkix.Name{CommonName: "zke-agent"},
			NotBefore:    now.Add(-time.Minute),
			NotAfter:     now.Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
		},
		&privateKey.PublicKey,
		privateKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certificateDER,
	})
	return csrPEM, certificatePEM
}
