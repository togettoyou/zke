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
	"time"

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
	renewalCSRKey             = "certificate.renewal.csr"
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
	err := retry.OnError(retry.DefaultBackoff, func(err error) bool {
		return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)
	}, func() error {
		secret, err := store.client.CoreV1().
			Secrets(store.namespace).
			Get(ctx, store.secretName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if candidate == nil {
				candidate, err = newPendingIdentity()
				if err != nil {
					return err
				}
			}
			created, err := store.client.CoreV1().
				Secrets(store.namespace).
				Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: store.namespace,
						Name:      store.secretName,
					},
					Type: corev1.SecretTypeOpaque,
					Data: map[string][]byte{
						identityPrivateKeyKey: append(
							[]byte(nil),
							candidate.PrivateKeyPEM...,
						),
						enrollmentCSRKey: append(
							[]byte(nil),
							candidate.CSRPEM...,
						),
						enrollmentIdempotencyKey: []byte(candidate.IdempotencyKey),
					},
				}, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("create Agent identity Secret: %w", err)
			}
			result, _, err = parseIdentitySecret(created, time.Now().UTC())
			return err
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

func (store *IdentityStore) LoadOrCreateRenewalCSR(
	ctx context.Context,
	identity LocalIdentity,
) ([]byte, error) {
	if err := validateLocalIdentity(identity, time.Now().UTC()); err != nil {
		return nil, err
	}
	var result []byte
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		secret, err := store.client.CoreV1().
			Secrets(store.namespace).
			Get(ctx, store.secretName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("read Agent identity Secret for renewal: %w", err)
		}
		state, empty, err := parseIdentitySecret(secret, time.Now().UTC())
		if err != nil {
			return err
		}
		if empty || state.Identity == nil ||
			!sameLocalIdentity(*state.Identity, identity) {
			return errors.New(
				"Agent identity Secret changed before certificate renewal",
			)
		}
		if existing := secret.Data[renewalCSRKey]; len(existing) != 0 {
			if err := validateRenewalCSR(identity, existing); err != nil {
				return err
			}
			result = append([]byte(nil), existing...)
			return nil
		}
		csrPEM, err := createIdentityCSR(identity.PrivateKeyPEM)
		if err != nil {
			return err
		}
		updated := secret.DeepCopy()
		if updated.Data == nil {
			updated.Data = make(map[string][]byte)
		}
		updated.Data[renewalCSRKey] = append([]byte(nil), csrPEM...)
		updated, err = store.client.CoreV1().
			Secrets(store.namespace).
			Update(ctx, updated, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf(
				"store Agent certificate renewal CSR: %w",
				err,
			)
		}
		result = append([]byte(nil), updated.Data[renewalCSRKey]...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (store *IdentityStore) CompleteRenewal(
	ctx context.Context,
	previous LocalIdentity,
	csrPEM []byte,
	certificatePEM []byte,
	expiresAt time.Time,
	now time.Time,
) (LocalIdentity, error) {
	if err := validateRenewalCSR(previous, csrPEM); err != nil {
		return LocalIdentity{}, err
	}
	renewed := LocalIdentity{
		ClusterID:            previous.ClusterID,
		AgentID:              previous.AgentID,
		PrivateKeyPEM:        append([]byte(nil), previous.PrivateKeyPEM...),
		CertificatePEM:       append([]byte(nil), certificatePEM...),
		CertificateExpiresAt: expiresAt,
	}
	if err := validateLocalIdentity(renewed, now); err != nil {
		return LocalIdentity{}, err
	}

	var stored LocalIdentity
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		secret, err := store.client.CoreV1().
			Secrets(store.namespace).
			Get(ctx, store.secretName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf(
				"read Agent identity Secret for renewed certificate: %w",
				err,
			)
		}
		state, empty, err := parseIdentitySecret(secret, now)
		if err != nil {
			return err
		}
		if empty || state.Identity == nil {
			return errors.New(
				"Agent identity Secret lost its completed identity",
			)
		}
		if sameLocalIdentity(*state.Identity, renewed) {
			stored = *state.Identity
			return nil
		}
		if !sameLocalIdentity(*state.Identity, previous) ||
			!bytes.Equal(secret.Data[renewalCSRKey], csrPEM) {
			return errors.New(
				"Agent identity Secret changed during certificate renewal",
			)
		}
		updated := secret.DeepCopy()
		updated.Data[identityCertificateKey] =
			append([]byte(nil), renewed.CertificatePEM...)
		updated.Data[identityCertificateExpiry] =
			[]byte(renewed.CertificateExpiresAt.Format(time.RFC3339Nano))
		delete(updated.Data, renewalCSRKey)
		updated, err = store.client.CoreV1().
			Secrets(store.namespace).
			Update(ctx, updated, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf(
				"store renewed Agent certificate: %w",
				err,
			)
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

func createIdentityCSR(privateKeyPEM []byte) ([]byte, error) {
	privateKey, err := parseIdentityPrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	csrDER, err := x509.CreateCertificateRequest(
		rand.Reader,
		&x509.CertificateRequest{},
		privateKey,
	)
	if err != nil {
		return nil, errors.New("create Agent identity renewal CSR")
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	}), nil
}
