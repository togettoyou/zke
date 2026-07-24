package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/shared/validation"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

const (
	identityPrivateKeyKey     = "tls.key"
	identityCertificateKey    = "tls.crt"
	identityClusterIDKey      = "cluster-id"
	identityAgentIDKey        = "agent-id"
	identityCertificateExpiry = "certificate-expires-at"
	enrollmentCSRKey          = "enrollment.csr"
	enrollmentIdempotencyKey  = "enrollment.idempotency-key"
)

type IdentityStore struct {
	client     kubernetes.Interface
	namespace  string
	secretName string
}

type PendingIdentity struct {
	PrivateKeyPEM  []byte
	CSRPEM         []byte
	IdempotencyKey string
}

type LocalIdentity struct {
	ClusterID            string
	AgentID              string
	PrivateKeyPEM        []byte
	CertificatePEM       []byte
	CertificateExpiresAt time.Time
}

type IdentityState struct {
	Pending  *PendingIdentity
	Identity *LocalIdentity
}

type RegistrationIdentity struct {
	ClusterID            string
	AgentID              string
	CertificatePEM       []byte
	CertificateExpiresAt time.Time
}

func NewIdentityStore(
	client kubernetes.Interface,
	namespace string,
	secretName string,
) *IdentityStore {
	return &IdentityStore{
		client:     client,
		namespace:  namespace,
		secretName: secretName,
	}
}

func (store *IdentityStore) LoadOrCreatePending(
	ctx context.Context,
) (IdentityState, error) {
	var result IdentityState
	var candidate *PendingIdentity
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		secret, err := store.client.CoreV1().
			Secrets(store.namespace).
			Get(ctx, store.secretName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return errors.New("pre-created Agent identity Secret was not found")
		}
		if err != nil {
			return fmt.Errorf("read Agent identity Secret: %w", err)
		}
		state, empty, err := parseIdentitySecret(secret, time.Now().UTC())
		if err != nil {
			return err
		}
		if !empty {
			result = state
			return nil
		}
		if candidate == nil {
			candidate, err = newPendingIdentity()
			if err != nil {
				return err
			}
		}
		updated := secret.DeepCopy()
		if updated.Data == nil {
			updated.Data = make(map[string][]byte)
		}
		updated.Data[identityPrivateKeyKey] =
			append([]byte(nil), candidate.PrivateKeyPEM...)
		updated.Data[enrollmentCSRKey] =
			append([]byte(nil), candidate.CSRPEM...)
		updated.Data[enrollmentIdempotencyKey] =
			[]byte(candidate.IdempotencyKey)
		updated, err = store.client.CoreV1().
			Secrets(store.namespace).
			Update(ctx, updated, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("initialize Agent identity Secret: %w", err)
		}
		result, _, err = parseIdentitySecret(updated, time.Now().UTC())
		return err
	})
	if err != nil {
		return IdentityState{}, err
	}
	return result, nil
}

func (store *IdentityStore) Complete(
	ctx context.Context,
	pending PendingIdentity,
	registration RegistrationIdentity,
	now time.Time,
) (LocalIdentity, error) {
	identity, err := validateRegistrationIdentity(pending, registration, now)
	if err != nil {
		return LocalIdentity{}, err
	}
	var stored LocalIdentity
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		secret, err := store.client.CoreV1().
			Secrets(store.namespace).
			Get(ctx, store.secretName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("read Agent identity Secret for completion: %w", err)
		}
		state, empty, err := parseIdentitySecret(secret, now)
		if err != nil {
			return err
		}
		if empty {
			return errors.New("Agent identity Secret lost pending enrollment state")
		}
		if state.Identity != nil {
			if sameLocalIdentity(*state.Identity, identity) {
				stored = *state.Identity
				return nil
			}
			return errors.New("Agent identity Secret already contains another identity")
		}
		if !samePendingIdentity(*state.Pending, pending) {
			return errors.New("Agent identity Secret pending enrollment state changed")
		}

		updated := secret.DeepCopy()
		updated.Data[identityCertificateKey] =
			append([]byte(nil), identity.CertificatePEM...)
		updated.Data[identityClusterIDKey] = []byte(identity.ClusterID)
		updated.Data[identityAgentIDKey] = []byte(identity.AgentID)
		updated.Data[identityCertificateExpiry] =
			[]byte(identity.CertificateExpiresAt.Format(time.RFC3339Nano))
		delete(updated.Data, enrollmentCSRKey)
		delete(updated.Data, enrollmentIdempotencyKey)
		updated, err = store.client.CoreV1().
			Secrets(store.namespace).
			Update(ctx, updated, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("complete Agent identity Secret: %w", err)
		}
		state, _, err = parseIdentitySecret(updated, now)
		if err != nil {
			return err
		}
		stored = *state.Identity
		return nil
	})
	if err != nil {
		return LocalIdentity{}, err
	}
	return stored, nil
}

func parseIdentitySecret(
	secret *corev1.Secret,
	now time.Time,
) (IdentityState, bool, error) {
	privateKeyPEM := secret.Data[identityPrivateKeyKey]
	certificatePEM := secret.Data[identityCertificateKey]
	clusterID := string(secret.Data[identityClusterIDKey])
	agentID := string(secret.Data[identityAgentIDKey])
	expiresAtValue := string(secret.Data[identityCertificateExpiry])
	csrPEM := secret.Data[enrollmentCSRKey]
	idempotencyKey := string(secret.Data[enrollmentIdempotencyKey])

	completeFieldCount := countPresent(
		certificatePEM,
		[]byte(clusterID),
		[]byte(agentID),
		[]byte(expiresAtValue),
	)
	pendingFieldCount := countPresent(csrPEM, []byte(idempotencyKey))
	if len(privateKeyPEM) == 0 &&
		completeFieldCount == 0 &&
		pendingFieldCount == 0 {
		return IdentityState{}, true, nil
	}
	if len(privateKeyPEM) == 0 {
		return IdentityState{}, false, errors.New(
			"Agent identity Secret contains identity data without a private key",
		)
	}
	if completeFieldCount == 4 && pendingFieldCount == 0 {
		expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtValue)
		if err != nil {
			return IdentityState{}, false, errors.New(
				"Agent identity Secret contains an invalid certificate expiry",
			)
		}
		identity := LocalIdentity{
			ClusterID:            clusterID,
			AgentID:              agentID,
			PrivateKeyPEM:        append([]byte(nil), privateKeyPEM...),
			CertificatePEM:       append([]byte(nil), certificatePEM...),
			CertificateExpiresAt: expiresAt,
		}
		if err := validateLocalIdentity(identity, now); err != nil {
			return IdentityState{}, false, err
		}
		return IdentityState{Identity: &identity}, false, nil
	}
	if completeFieldCount == 0 && pendingFieldCount == 2 {
		pending := PendingIdentity{
			PrivateKeyPEM:  append([]byte(nil), privateKeyPEM...),
			CSRPEM:         append([]byte(nil), csrPEM...),
			IdempotencyKey: idempotencyKey,
		}
		if err := validatePendingIdentity(pending); err != nil {
			return IdentityState{}, false, err
		}
		return IdentityState{Pending: &pending}, false, nil
	}
	return IdentityState{}, false, errors.New(
		"Agent identity Secret contains a partial or conflicting identity state",
	)
}

func newPendingIdentity() (*PendingIdentity, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, errors.New("generate Agent identity private key")
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, errors.New("encode Agent identity private key")
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privateKeyDER,
	})
	csrDER, err := x509.CreateCertificateRequest(
		rand.Reader,
		&x509.CertificateRequest{},
		privateKey,
	)
	if err != nil {
		return nil, errors.New("create Agent identity CSR")
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	})
	idempotencyValue := make([]byte, 24)
	if _, err := rand.Read(idempotencyValue); err != nil {
		return nil, errors.New("generate Agent enrollment idempotency key")
	}
	return &PendingIdentity{
		PrivateKeyPEM:  privateKeyPEM,
		CSRPEM:         csrPEM,
		IdempotencyKey: base64.RawURLEncoding.EncodeToString(idempotencyValue),
	}, nil
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
	if len(certificates) < 2 {
		return nil, errors.New("Agent identity certificate signing chain is missing")
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
