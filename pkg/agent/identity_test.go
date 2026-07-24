package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	testIdentityNamespace = "zke-system"
	testIdentitySecret    = "zke-agent-identity"
	testTenantID          = "11111111-1111-1111-1111-111111111111"
	testProjectID         = "22222222-2222-2222-2222-222222222222"
	testClusterID         = "33333333-3333-3333-3333-333333333333"
	testAgentID           = "44444444-4444-4444-4444-444444444444"
)

func TestIdentityStorePersistsPendingEnrollment(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	store := NewIdentityStore(client, testIdentityNamespace, testIdentitySecret)

	first, err := store.LoadOrCreatePending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Pending == nil || first.Identity != nil {
		t.Fatalf("unexpected first identity state: %+v", first)
	}
	if err := validatePendingIdentity(*first.Pending); err != nil {
		t.Fatalf("stored pending identity is invalid: %v", err)
	}
	assertPendingKeyMatchesCSR(t, *first.Pending)
	secret, err := client.CoreV1().
		Secrets(testIdentityNamespace).
		Get(context.Background(), testIdentitySecret, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if secret.Type != corev1.SecretTypeOpaque {
		t.Fatalf("identity Secret type = %q, want Opaque", secret.Type)
	}

	second, err := store.LoadOrCreatePending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Pending == nil ||
		!samePendingIdentity(*first.Pending, *second.Pending) {
		t.Fatal("second load did not reuse the exact pending enrollment state")
	}
}

func TestIdentityStoreConcurrentCreationConverges(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	store := NewIdentityStore(client, testIdentityNamespace, testIdentitySecret)
	results := make(chan IdentityState, 2)
	failures := make(chan error, 2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	for range 2 {
		go func() {
			defer workers.Done()
			<-start
			state, err := store.LoadOrCreatePending(context.Background())
			if err != nil {
				failures <- err
				return
			}
			results <- state
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	close(failures)

	for err := range failures {
		t.Fatal(err)
	}
	var first *PendingIdentity
	count := 0
	for state := range results {
		count++
		if state.Pending == nil {
			t.Fatalf("concurrent state has no pending identity: %+v", state)
		}
		if first == nil {
			first = state.Pending
			continue
		}
		if !samePendingIdentity(*first, *state.Pending) {
			t.Fatal("concurrent Secret creation did not converge on one pending identity")
		}
	}
	if count != 2 {
		t.Fatalf("concurrent result count = %d, want 2", count)
	}
}

func TestIdentityStoreCompletesAndReloadsIdentity(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	store := NewIdentityStore(client, testIdentityNamespace, testIdentitySecret)
	state, err := store.LoadOrCreatePending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	certificatePEM, expiresAt := issueTestAgentCertificate(
		t,
		*state.Pending,
		now,
	)
	registration := RegistrationIdentity{
		ClusterID:            testClusterID,
		AgentID:              testAgentID,
		CertificatePEM:       certificatePEM,
		CertificateExpiresAt: expiresAt,
	}
	identity, err := store.Complete(
		context.Background(),
		*state.Pending,
		registration,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ClusterID != testClusterID || identity.AgentID != testAgentID {
		t.Fatalf("unexpected completed identity: %+v", identity)
	}

	secret, err := client.CoreV1().
		Secrets(testIdentityNamespace).
		Get(context.Background(), testIdentitySecret, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(secret.Data[identityPrivateKeyKey]) == 0 ||
		len(secret.Data[identityCertificateKey]) == 0 {
		t.Fatal("completed Secret does not contain the TLS identity")
	}
	if len(secret.Data[enrollmentCSRKey]) != 0 ||
		len(secret.Data[enrollmentIdempotencyKey]) != 0 {
		t.Fatal("completed Secret retained pending enrollment fields")
	}

	reloaded, err := store.LoadOrCreatePending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Identity == nil ||
		!sameLocalIdentity(identity, *reloaded.Identity) {
		t.Fatal("completed identity was not reloaded exactly")
	}
}

func TestIdentityStoreRejectsPartialState(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testIdentityNamespace,
			Name:      testIdentitySecret,
		},
		Data: map[string][]byte{
			identityPrivateKeyKey: []byte("partial"),
		},
	})
	store := NewIdentityStore(client, testIdentityNamespace, testIdentitySecret)
	if _, err := store.LoadOrCreatePending(context.Background()); err == nil {
		t.Fatal("LoadOrCreatePending() accepted a partial identity Secret")
	}
}

func issueTestAgentCertificate(
	t *testing.T,
	pending PendingIdentity,
	now time.Time,
) ([]byte, time.Time) {
	t.Helper()

	privateKey, err := parseIdentityPrivateKey(pending.PrivateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	identityURI, err := url.Parse(
		"zke://agent/tenants/" + testTenantID +
			"/projects/" + testProjectID +
			"/clusters/" + testClusterID +
			"/agents/" + testAgentID,
	)
	if err != nil {
		t.Fatal(err)
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Agent CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(
		rand.Reader,
		caTemplate,
		caTemplate,
		&caKey.PublicKey,
		caKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	expiresAt := now.Add(time.Hour)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			CommonName: testAgentID,
		},
		NotBefore:   now.Add(-time.Minute),
		NotAfter:    expiresAt,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:        []*url.URL{identityURI},
	}
	certificateDER, err := x509.CreateCertificate(
		rand.Reader,
		template,
		caCertificate,
		&privateKey.PublicKey,
		caKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certificateDER,
	})
	caPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caDER,
	})
	return append(leafPEM, caPEM...), expiresAt
}

func assertPendingKeyMatchesCSR(t *testing.T, pending PendingIdentity) {
	t.Helper()

	privateKey, err := parseIdentityPrivateKey(pending.PrivateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := parseIdentityCSR(pending.CSRPEM)
	if err != nil {
		t.Fatal(err)
	}
	csrKey, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || !privateKey.PublicKey.Equal(csrKey) {
		t.Fatal("pending private key does not match its CSR")
	}
	if bytes.Equal(pending.PrivateKeyPEM, pending.CSRPEM) {
		t.Fatal("pending private key and CSR unexpectedly have the same encoding")
	}
}
