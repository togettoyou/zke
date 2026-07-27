// Identity material: parsing and validating the private key, CSR and
// certificate an Agent identity is made of. Kept apart from the Secret-backed
// store in identity.go so that persistence and cryptographic validation stay
// separately reviewable.

package agent

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/shared/validation"
)

func validateRenewalCSR(identity LocalIdentity, csrPEM []byte) error {
	privateKey, err := parseIdentityPrivateKey(identity.PrivateKeyPEM)
	if err != nil {
		return err
	}
	csr, err := parseIdentityCSR(csrPEM)
	if err != nil {
		return err
	}
	if !publicKeysMatch(&privateKey.PublicKey, csr.PublicKey) {
		return errors.New(
			"Agent certificate renewal CSR does not match its private key",
		)
	}
	return nil
}

func validatePendingIdentity(pending PendingIdentity) error {
	privateKey, err := parseIdentityPrivateKey(pending.PrivateKeyPEM)
	if err != nil {
		return err
	}
	csr, err := parseIdentityCSR(pending.CSRPEM)
	if err != nil {
		return err
	}
	if !publicKeysMatch(&privateKey.PublicKey, csr.PublicKey) {
		return errors.New("Agent pending CSR does not match its private key")
	}
	if len(pending.IdempotencyKey) < 16 ||
		len(pending.IdempotencyKey) > 128 ||
		strings.TrimSpace(pending.IdempotencyKey) != pending.IdempotencyKey {
		return errors.New("Agent pending idempotency key is invalid")
	}
	return nil
}

func validateRegistrationIdentity(
	pending PendingIdentity,
	registration RegistrationIdentity,
	now time.Time,
) (LocalIdentity, error) {
	identity := LocalIdentity{
		ClusterID:            registration.ClusterID,
		AgentID:              registration.AgentID,
		PrivateKeyPEM:        append([]byte(nil), pending.PrivateKeyPEM...),
		CertificatePEM:       append([]byte(nil), registration.CertificatePEM...),
		CertificateExpiresAt: registration.CertificateExpiresAt,
	}
	if err := validateLocalIdentity(identity, now); err != nil {
		return LocalIdentity{}, err
	}
	return identity, nil
}

func validateLocalIdentity(identity LocalIdentity, now time.Time) error {
	if strings.TrimSpace(identity.ClusterID) == "" ||
		strings.TrimSpace(identity.AgentID) == "" ||
		!identity.CertificateExpiresAt.After(now) {
		return errors.New("Agent identity metadata is invalid or expired")
	}
	privateKey, err := parseIdentityPrivateKey(identity.PrivateKeyPEM)
	if err != nil {
		return err
	}
	certificate, err := parseIdentityCertificate(identity.CertificatePEM, now)
	if err != nil {
		return err
	}
	if !certificate.NotAfter.Equal(identity.CertificateExpiresAt) ||
		certificate.NotBefore.After(now) ||
		!certificate.NotAfter.After(now) {
		return errors.New("Agent identity certificate expiry does not match metadata")
	}
	if !publicKeysMatch(&privateKey.PublicKey, certificate.PublicKey) {
		return errors.New("Agent identity certificate does not match its private key")
	}
	if certificate.IsCA ||
		certificate.KeyUsage&x509.KeyUsageDigitalSignature == 0 ||
		certificate.Subject.CommonName != identity.AgentID ||
		!certificateAllowsClientAuthentication(certificate) ||
		!certificateIdentityMatches(certificate, identity.ClusterID, identity.AgentID) {
		return errors.New("Agent identity certificate scope is invalid")
	}
	return nil
}

func parseIdentityPrivateKey(value []byte) (*ecdsa.PrivateKey, error) {
	block, rest := pem.Decode(value)
	if block == nil ||
		block.Type != "PRIVATE KEY" ||
		len(block.Headers) != 0 ||
		len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("Agent identity private key PEM is invalid")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("parse Agent identity private key")
	}
	privateKey, ok := key.(*ecdsa.PrivateKey)
	if !ok || privateKey.Curve != elliptic.P256() {
		return nil, errors.New("Agent identity private key must use ECDSA P-256")
	}
	return privateKey, nil
}

func parseIdentityCSR(value []byte) (*x509.CertificateRequest, error) {
	block, rest := pem.Decode(value)
	if block == nil ||
		block.Type != "CERTIFICATE REQUEST" ||
		len(block.Headers) != 0 ||
		len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("Agent identity CSR PEM is invalid")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, errors.New("parse Agent identity CSR")
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, errors.New("verify Agent identity CSR")
	}
	return csr, nil
}

func parseIdentityCertificate(
	value []byte,
	now time.Time,
) (*x509.Certificate, error) {
	remaining := value
	var certificates []*x509.Certificate
	for len(bytes.TrimSpace(remaining)) != 0 {
		block, rest := pem.Decode(remaining)
		if block == nil ||
			block.Type != "CERTIFICATE" ||
			len(block.Headers) != 0 {
			return nil, errors.New("Agent identity certificate chain PEM is invalid")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, errors.New("parse Agent identity certificate chain")
		}
		certificates = append(certificates, certificate)
		remaining = rest
	}
	if len(certificates) == 0 {
		return nil, errors.New("Agent identity certificate PEM is empty")
	}
	for index := 1; index < len(certificates); index++ {
		issuer := certificates[index]
		if !issuer.IsCA ||
			!issuer.BasicConstraintsValid ||
			issuer.KeyUsage&x509.KeyUsageCertSign == 0 ||
			now.Before(issuer.NotBefore) ||
			!now.Before(issuer.NotAfter) {
			return nil, errors.New("Agent identity certificate signing chain is invalid")
		}
		if err := certificates[index-1].CheckSignatureFrom(issuer); err != nil {
			return nil, errors.New("verify Agent identity certificate signing chain")
		}
	}
	return certificates[0], nil
}

func publicKeysMatch(first any, second any) bool {
	firstDER, err := x509.MarshalPKIXPublicKey(first)
	if err != nil {
		return false
	}
	secondDER, err := x509.MarshalPKIXPublicKey(second)
	if err != nil {
		return false
	}
	return bytes.Equal(firstDER, secondDER)
}

func certificateAllowsClientAuthentication(certificate *x509.Certificate) bool {
	for _, usage := range certificate.ExtKeyUsage {
		if usage == x509.ExtKeyUsageClientAuth {
			return true
		}
	}
	return false
}

func certificateIdentityMatches(
	certificate *x509.Certificate,
	clusterID string,
	agentID string,
) bool {
	if len(certificate.URIs) != 1 {
		return false
	}
	identityURI := certificate.URIs[0]
	if identityURI.Scheme != "zke" ||
		identityURI.Host != "agent" ||
		identityURI.User != nil ||
		identityURI.Opaque != "" ||
		identityURI.RawPath != "" ||
		identityURI.RawQuery != "" ||
		identityURI.Fragment != "" ||
		identityURI.ForceQuery {
		return false
	}
	parts := strings.Split(strings.Trim(identityURI.Path, "/"), "/")
	if len(parts) != 8 ||
		parts[0] != "tenants" ||
		!validation.IsUUID(parts[1]) ||
		parts[2] != "projects" ||
		!validation.IsUUID(parts[3]) ||
		parts[4] != "clusters" ||
		parts[5] != clusterID ||
		parts[6] != "agents" ||
		parts[7] != agentID ||
		!validation.IsUUID(clusterID) ||
		!validation.IsUUID(agentID) {
		return false
	}
	expected := &url.URL{
		Scheme: "zke",
		Host:   "agent",
		Path: strings.Join([]string{
			"/tenants",
			parts[1],
			"projects",
			parts[3],
			"clusters",
			clusterID,
			"agents",
			agentID,
		}, "/"),
	}
	return identityURI.String() == expected.String()
}

func countPresent(values ...[]byte) int {
	count := 0
	for _, value := range values {
		if len(value) != 0 {
			count++
		}
	}
	return count
}

func samePendingIdentity(first PendingIdentity, second PendingIdentity) bool {
	return bytes.Equal(first.PrivateKeyPEM, second.PrivateKeyPEM) &&
		bytes.Equal(first.CSRPEM, second.CSRPEM) &&
		first.IdempotencyKey == second.IdempotencyKey
}

func sameLocalIdentity(first LocalIdentity, second LocalIdentity) bool {
	return first.ClusterID == second.ClusterID &&
		first.AgentID == second.AgentID &&
		bytes.Equal(first.PrivateKeyPEM, second.PrivateKeyPEM) &&
		bytes.Equal(first.CertificatePEM, second.CertificatePEM) &&
		first.CertificateExpiresAt.Equal(second.CertificateExpiresAt)
}
