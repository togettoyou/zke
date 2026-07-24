package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/server/agentconn"
	"github.com/togettoyou/zke/pkg/server/enrollment"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAgentQUICConnectionAndHeartbeat(t *testing.T) {
	databaseURL := os.Getenv("ZKE_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("ZKE_TEST_DATABASE_URL is required in CI")
		}
		t.Skip("ZKE_TEST_DATABASE_URL is not configured")
	}

	setupContext, cancelSetup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSetup()
	pool := openAgentConnectionTestDatabase(t, setupContext, databaseURL)
	if _, err := migrations.Apply(setupContext, pool); err != nil {
		t.Fatal(err)
	}

	const (
		tenantID     = "00000000-0000-4000-8000-000000000001"
		projectID    = "00000000-0000-4000-8000-000000000002"
		clusterID    = "00000000-0000-4000-8000-000000000003"
		agentID      = "00000000-0000-4000-8000-000000000004"
		credentialID = "00000000-0000-4000-8000-000000000005"
	)
	now := time.Now().UTC()
	clientCACertificate, clientCAPrivateKey, clientCAPEM, clientCAKeyPEM :=
		createConnectionTestCA(t, "Agent Client CA", 1, now)
	_, listenerCAPrivateKey, listenerCAPEM, _ :=
		createConnectionTestCA(t, "Agent Listener CA", 2, now)
	listenerCertificatePEM, listenerPrivateKeyPEM :=
		createConnectionTestListenerCertificate(
			t,
			listenerCAPEM,
			listenerCAPrivateKey,
			now,
		)

	identityStore := NewIdentityStore(
		fake.NewClientset(),
		"zke-system",
		"zke-agent-identity",
	)
	identityState, err := identityStore.LoadOrCreatePending(setupContext)
	if err != nil {
		t.Fatal(err)
	}
	pending := identityState.Pending
	if pending == nil {
		t.Fatal("identity store did not create a pending identity")
	}
	csrBlock, _ := pem.Decode(pending.CSRPEM)
	if csrBlock == nil {
		t.Fatal("decode Agent CSR")
	}
	certificateRequest, err := x509.ParseCertificateRequest(csrBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	initialSigner, err := enrollment.NewCertificateSigner(
		clientCAPEM,
		clientCAKeyPEM,
		5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	signedAgentCertificate, err := initialSigner.Sign(
		certificateRequest,
		enrollment.CertificateIdentity{
			TenantID:  tenantID,
			ProjectID: projectID,
			ClusterID: clusterID,
			AgentID:   agentID,
		},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if clientCACertificate == nil || clientCAPrivateKey == nil {
		t.Fatal("Agent Client CA generation failed")
	}
	localIdentity, err := identityStore.Complete(
		setupContext,
		*pending,
		RegistrationIdentity{
			ClusterID:            clusterID,
			AgentID:              agentID,
			CertificatePEM:       []byte(signedAgentCertificate.PEM),
			CertificateExpiresAt: signedAgentCertificate.ExpiresAt,
		},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}

	batch := &pgx.Batch{}
	batch.Queue(
		"INSERT INTO tenants (id, name, status) VALUES ($1, 'tenant', 'active')",
		tenantID,
	)
	batch.Queue(
		`INSERT INTO projects (id, tenant_id, name, status)
VALUES ($2, $1, 'project', 'active')`,
		tenantID,
		projectID,
	)
	batch.Queue(
		`INSERT INTO clusters (id, tenant_id, project_id, name, status)
VALUES ($3, $1, $2, 'cluster', 'pending')`,
		tenantID,
		projectID,
		clusterID,
	)
	batch.Queue(
		`INSERT INTO agents (
    id, tenant_id, project_id, cluster_id, version, protocol_version,
    lifecycle_status, health_status
) VALUES ($4, $1, $2, $3, 'registered', 'v1', 'pending', 'unknown')`,
		tenantID,
		projectID,
		clusterID,
		agentID,
	)
	batch.Queue(
		`INSERT INTO agent_credentials (
    id, tenant_id, project_id, cluster_id, agent_id, serial,
    csr_fingerprint, certificate_pem, expires_at
) VALUES ($5, $1, $2, $3, $4, $6, decode('01', 'hex'), $7, $8)`,
		tenantID,
		projectID,
		clusterID,
		agentID,
		credentialID,
		signedAgentCertificate.Serial,
		signedAgentCertificate.PEM,
		signedAgentCertificate.ExpiresAt,
	)
	if err := pool.SendBatch(setupContext, batch).Close(); err != nil {
		t.Fatal(err)
	}

	certificateDirectory := t.TempDir()
	listenerCertificateFile := filepath.Join(certificateDirectory, "agent-listener.crt")
	listenerPrivateKeyFile := filepath.Join(certificateDirectory, "agent-listener.key")
	listenerCAFile := filepath.Join(certificateDirectory, "agent-listener-ca.crt")
	clientCAFile := filepath.Join(certificateDirectory, "agent-client-ca.crt")
	for path, content := range map[string][]byte{
		listenerCertificateFile: listenerCertificatePEM,
		listenerPrivateKeyFile:  listenerPrivateKeyPEM,
		listenerCAFile:          listenerCAPEM,
		clientCAFile:            clientCAPEM,
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	address := reserveUDPAddress(t)
	logger := discardAgentLogger()
	connectionStore := store.NewAgentConnectionStore(pool)
	renewalSigner, err := enrollment.NewCertificateSigner(
		clientCAPEM,
		clientCAKeyPEM,
		2*time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := agentconn.New(
		agentconn.Config{
			Address:                 address,
			TLSCertificateFile:      listenerCertificateFile,
			TLSPrivateKeyFile:       listenerPrivateKeyFile,
			ClientCACertificateFile: clientCAFile,
			HandshakeTimeout:        time.Second,
			HeartbeatInterval:       time.Second,
			HeartbeatTimeout:        3 * time.Second,
			LastSeenWriteInterval:   time.Second,
			OperationTimeout:        time.Second,
		},
		logger,
		connectionStore,
		enrollment.NewCertificateRenewalService(
			connectionStore,
			renewalSigner,
		),
	)
	if err != nil {
		t.Fatal(err)
	}

	runContext, cancelRun := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancelRun()
	managerErrors := make(chan error, 1)
	go func() {
		managerErrors <- manager.Run(runContext)
	}()

	agentErrors := make(chan error, 1)
	go func() {
		agentErrors <- runConnectionLoop(
			runContext,
			Config{
				CertificateRenewBefore: 2 * time.Second,
				Connection: ConnectionConfig{
					ServerAddress:        address,
					CACertificateFile:    listenerCAFile,
					ConnectTimeout:       time.Second,
					RetryInitialInterval: 10 * time.Millisecond,
					RetryMaxInterval:     50 * time.Millisecond,
				},
			},
			identityStore,
			localIdentity,
			"development",
			logger,
		)
	}()

	var lifecycleStatus, healthStatus, protocolVersion string
	var lastSeenAt *time.Time
	var credentialCount, activeCredentialCount int
	var activeCertificateSerial string
	stateReady := false
	pollTicker := time.NewTicker(20 * time.Millisecond)
	defer pollTicker.Stop()
	for !stateReady {
		select {
		case agentErr := <-agentErrors:
			t.Fatalf("Agent stopped before renewal completed: %v", agentErr)
		case <-runContext.Done():
			t.Fatal("timed out waiting for Agent certificate renewal")
		case <-pollTicker.C:
		}
		err = pool.QueryRow(setupContext, `
SELECT lifecycle_status, health_status, protocol_version, last_seen_at
FROM agents
WHERE id = $1
`,
			agentID,
		).Scan(
			&lifecycleStatus,
			&healthStatus,
			&protocolVersion,
			&lastSeenAt,
		)
		if err != nil {
			t.Fatal(err)
		}
		err = pool.QueryRow(setupContext, `
SELECT
    count(*),
    count(*) FILTER (WHERE revoked_at IS NULL),
    COALESCE(max(serial) FILTER (WHERE revoked_at IS NULL), '')
FROM agent_credentials
WHERE agent_id = $1
`, agentID).Scan(
			&credentialCount,
			&activeCredentialCount,
			&activeCertificateSerial,
		)
		if err != nil {
			t.Fatal(err)
		}
		stateReady = lifecycleStatus == "active" &&
			healthStatus == "healthy" &&
			protocolVersion == "v1" &&
			lastSeenAt != nil &&
			lastSeenAt.After(now) &&
			credentialCount == 2 &&
			activeCredentialCount == 1 &&
			activeCertificateSerial != signedAgentCertificate.Serial
	}

	renewedState, err := identityStore.LoadOrCreatePending(setupContext)
	if err != nil {
		t.Fatal(err)
	}
	if renewedState.Identity == nil ||
		!renewedState.Identity.CertificateExpiresAt.After(
			signedAgentCertificate.ExpiresAt,
		) {
		t.Fatalf("Agent identity was not renewed: %+v", renewedState)
	}
	if _, err := pool.Exec(
		setupContext,
		`UPDATE agent_credentials
SET revoked_at = now()
WHERE agent_id = $1
  AND serial = $2
  AND revoked_at IS NULL`,
		agentID,
		activeCertificateSerial,
	); err != nil {
		t.Fatal(err)
	}
	select {
	case agentErr := <-agentErrors:
		if agentErr == nil ||
			!permanentAgentConnectionError(agentErr) {
			t.Fatalf(
				"Agent revocation error = %v, want permanent rejection",
				agentErr,
			)
		}
	case <-runContext.Done():
		t.Fatal("revoked Agent connection did not close")
	}
	cancelRun()
	if managerErr := <-managerErrors; managerErr != nil {
		t.Fatal(managerErr)
	}
}

func createConnectionTestCA(
	t *testing.T,
	commonName string,
	serial int64,
	now time.Time,
) (*x509.Certificate, *ecdsa.PrivateKey, []byte, []byte) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{Organization: []string{"ZKE"}, CommonName: commonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
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
	certificate, err := x509.ParseCertificate(certificateDER)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, privateKey,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateKeyDER})
}

func createConnectionTestListenerCertificate(
	t *testing.T,
	caPEM []byte,
	caPrivateKey *ecdsa.PrivateKey,
	now time.Time,
) ([]byte, []byte) {
	t.Helper()
	caBlock, _ := pem.Decode(caPEM)
	if caBlock == nil {
		t.Fatal("decode Agent Listener CA")
	}
	caCertificate, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(3),
		Subject:               pkix.Name{Organization: []string{"ZKE"}, CommonName: "127.0.0.1"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	certificateDER, err := x509.CreateCertificate(
		rand.Reader,
		template,
		caCertificate,
		&privateKey.PublicKey,
		caPrivateKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: certificateDER,
		}),
		pem.EncodeToMemory(&pem.Block{
			Type:  "EC PRIVATE KEY",
			Bytes: privateKeyDER,
		})
}

func reserveUDPAddress(t *testing.T) string {
	t.Helper()
	connection, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := connection.LocalAddr().String()
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func openAgentConnectionTestDatabase(
	t *testing.T,
	ctx context.Context,
	databaseURL string,
) *pgxpool.Pool {
	t.Helper()
	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	var randomValue [8]byte
	if _, err := rand.Read(randomValue[:]); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	schemaName := "zke_agent_connection_test_" + hex.EncodeToString(randomValue[:])
	quotedSchemaName := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchemaName); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cleanupCancel()
		if _, err := adminPool.Exec(
			cleanupContext,
			"DROP SCHEMA "+quotedSchemaName+" CASCADE",
		); err != nil {
			t.Errorf("drop Agent connection test schema: %v", err)
		}
		adminPool.Close()
	})
	return pool
}

func discardAgentLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}
