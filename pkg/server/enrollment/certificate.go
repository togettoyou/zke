package enrollment

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"time"

	"github.com/togettoyou/zke/pkg/shared/validation"
)

const (
	DefaultCertificateTTL = 30 * 24 * time.Hour
	certificateClockSkew  = time.Minute
	maxCAPEMBytes         = 1 << 20
	maxCAKeyPEMBytes      = 64 << 10
)

type CertificateSigner struct {
	certificate    *x509.Certificate
	certificatePEM string
	privateKey     crypto.Signer
	ttl            time.Duration
}

type CertificateIdentity struct {
	TenantID  string
	ProjectID string
	ClusterID string
	AgentID   string
}

type SignedCertificate struct {
	PEM       string
	Serial    string
	ExpiresAt time.Time
}

func NewCertificateSigner(
	certificatePEM []byte,
	privateKeyPEM []byte,
	ttl time.Duration,
) (*CertificateSigner, error) {
	if ttl <= 0 {
		return nil, errors.New("agent certificate TTL must be greater than zero")
	}
	certificate, err := parseCACertificate(certificatePEM)
	if err != nil {
		return nil, err
	}
	privateKey, err := parseCAPrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	certificatePublicKey, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		return nil, errors.New("marshal Agent CA certificate public key")
	}
	privatePublicKey, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil {
		return nil, errors.New("marshal Agent CA private key public key")
	}
	if !bytes.Equal(certificatePublicKey, privatePublicKey) {
		return nil, errors.New("Agent CA certificate and private key do not match")
	}
	now := time.Now().UTC()
	if now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return nil, errors.New("Agent CA certificate is not currently valid")
	}
	return &CertificateSigner{
		certificate:    certificate,
		certificatePEM: string(certificatePEM),
		privateKey:     privateKey,
		ttl:            ttl,
	}, nil
}

func (signer *CertificateSigner) Sign(
	certificateRequest *x509.CertificateRequest,
	identity CertificateIdentity,
	now time.Time,
) (SignedCertificate, error) {
	if certificateRequest == nil ||
		!validCertificateIdentity(identity) ||
		now.IsZero() {
		return SignedCertificate{}, errors.New("agent certificate signing fields are required")
	}
	if err := certificateRequest.CheckSignature(); err != nil {
		return SignedCertificate{}, errors.New("verify Agent CSR signature")
	}
	if err := validateCertificatePublicKey(certificateRequest.PublicKey, "Agent"); err != nil {
		return SignedCertificate{}, err
	}
	if now.Before(signer.certificate.NotBefore) || !now.Before(signer.certificate.NotAfter) {
		return SignedCertificate{}, errors.New("Agent CA certificate is not valid at signing time")
	}

	serialNumber, err := newCertificateSerial()
	if err != nil {
		return SignedCertificate{}, err
	}
	notBefore := now.Add(-certificateClockSkew)
	if notBefore.Before(signer.certificate.NotBefore) {
		notBefore = signer.certificate.NotBefore
	}
	notAfter := now.Add(signer.ttl)
	if notAfter.After(signer.certificate.NotAfter) {
		notAfter = signer.certificate.NotAfter
	}
	if !notAfter.After(now) {
		return SignedCertificate{}, errors.New("Agent CA expires before client certificate")
	}
	identityURI := &url.URL{
		Scheme: "zke",
		Host:   "agent",
		Path: fmt.Sprintf(
			"/tenants/%s/projects/%s/clusters/%s/agents/%s",
			identity.TenantID,
			identity.ProjectID,
			identity.ClusterID,
			identity.AgentID,
		),
	}
	subjectPublicKey, err := x509.MarshalPKIXPublicKey(certificateRequest.PublicKey)
	if err != nil {
		return SignedCertificate{}, errors.New("marshal Agent public key")
	}
	subjectKeyID := sha256.Sum256(subjectPublicKey)
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"ZKE"},
			CommonName:   identity.AgentID,
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		SubjectKeyId:          subjectKeyID[:20],
		AuthorityKeyId:        signer.certificate.SubjectKeyId,
		URIs:                  []*url.URL{identityURI},
	}
	certificateDER, err := x509.CreateCertificate(
		rand.Reader,
		template,
		signer.certificate,
		certificateRequest.PublicKey,
		signer.privateKey,
	)
	if err != nil {
		return SignedCertificate{}, errors.New("sign Agent client certificate")
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certificateDER,
	})
	return SignedCertificate{
		PEM:       string(leafPEM) + signer.certificatePEM,
		Serial:    serialNumber.String(),
		ExpiresAt: notAfter,
	}, nil
}

func parseCACertificate(value []byte) (*x509.Certificate, error) {
	if len(value) == 0 || len(value) > maxCAPEMBytes {
		return nil, errors.New("Agent CA certificate PEM size is invalid")
	}
	block, rest := pem.Decode(value)
	if block == nil ||
		block.Type != "CERTIFICATE" ||
		len(block.Headers) != 0 ||
		len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("Agent CA certificate PEM is invalid")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("parse Agent CA certificate")
	}
	if !certificate.IsCA ||
		!certificate.BasicConstraintsValid ||
		certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("Agent CA certificate cannot sign certificates")
	}
	if err := validateCertificatePublicKey(certificate.PublicKey, "Agent CA"); err != nil {
		return nil, err
	}
	return certificate, nil
}

func parseCAPrivateKey(value []byte) (crypto.Signer, error) {
	if len(value) == 0 || len(value) > maxCAKeyPEMBytes {
		return nil, errors.New("Agent CA private key PEM size is invalid")
	}
	block, rest := pem.Decode(value)
	if block == nil || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("Agent CA private key PEM is invalid")
	}
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, errors.New("Agent CA private key type is unsupported")
	}
	if err != nil {
		return nil, errors.New("parse Agent CA private key")
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("Agent CA private key cannot sign certificates")
	}
	return signer, nil
}

func validCertificateIdentity(identity CertificateIdentity) bool {
	return validation.IsUUID(identity.TenantID) &&
		validation.IsUUID(identity.ProjectID) &&
		validation.IsUUID(identity.ClusterID) &&
		validation.IsUUID(identity.AgentID)
}

func validateCertificatePublicKey(publicKey any, owner string) error {
	switch key := publicKey.(type) {
	case *rsa.PublicKey:
		if key.N.BitLen() < 2048 {
			return fmt.Errorf("%s RSA public key must be at least 2048 bits", owner)
		}
	case *ecdsa.PublicKey:
		if key.Curve == nil || key.Curve.Params().BitSize < 256 {
			return fmt.Errorf("%s ECDSA public key must be at least 256 bits", owner)
		}
	case ed25519.PublicKey:
		if len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("%s Ed25519 public key is invalid", owner)
		}
	default:
		return fmt.Errorf("%s public key type is unsupported", owner)
	}
	return nil
}

func newCertificateSerial() (*big.Int, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return nil, errors.New("generate Agent certificate serial")
	}
	value[0] &= 0x7f
	serialNumber := new(big.Int).SetBytes(value)
	if serialNumber.Sign() == 0 {
		serialNumber.SetInt64(1)
	}
	return serialNumber, nil
}
