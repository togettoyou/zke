package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/enrollment"
	"github.com/togettoyou/zke/pkg/server/store"
)

func TestAgentRegistrationHTTPFlow(t *testing.T) {
	databaseURL := requireHTTPTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openHTTPTestDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	var tenantID, projectID, userID string
	if err := pool.QueryRow(ctx, `
INSERT INTO tenants (id, name, status)
VALUES (gen_random_uuid(), 'Agent Registration Tenant', 'active')
RETURNING id::text
`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO projects (id, tenant_id, name, status)
VALUES (gen_random_uuid(), $1, 'Agent Registration Project', 'active')
RETURNING id::text
`, tenantID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO users (
    id,
    username_normalized,
    display_name,
    password_hash,
    status,
    password_changed_at
)
VALUES (
    gen_random_uuid(),
    'agent-registration-admin',
    'Agent Registration Admin',
    'not-used',
    'active',
    now()
)
RETURNING id::text
`).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	caCertificatePEM, caPrivateKeyPEM, caCertificate :=
		createHTTPTestAgentCA(t, now)
	certificateSigner, err := enrollment.NewCertificateSigner(
		caCertificatePEM,
		caPrivateKeyPEM,
		24*time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	enrollmentService := enrollment.NewService(
		store.NewEnrollmentStore(pool),
		enrollment.ServiceConfig{
			TokenTTL:              enrollment.DefaultTokenTTL,
			CertificateSigner:     certificateSigner,
			ConfigurationResolver: staticEnrollmentConfigurationResolver{},
		},
	)
	created, err := enrollmentService.Create(ctx, enrollment.CreateInput{
		ProjectID:      projectID,
		ClusterName:    "integration-cluster",
		AgentNamespace: "integration-agent",
		UserID:         userID,
		RequestID:      "request-create-registration-token",
		IdempotencyKey: "create-registration-token-0001",
		Now:            now,
	})
	if err != nil {
		t.Fatal(err)
	}
	router := New(
		discardLogger(),
		Dependencies{
			ReadinessCheck:    pool.Ping,
			EnrollmentService: enrollmentService,
		},
		Config{
			Authentication: defaultAuthenticationTestConfig(),
			AgentEnrollment: AgentEnrollmentHTTPConfig{
				OperationTimeout:     5 * time.Second,
				RateLimitWindow:      time.Minute,
				MaxAttemptsPerSource: 100,
			},
		},
	)

	csrPEM, csrPublicKey := createHTTPTestAgentCSR(t)
	requestBody := agentRegistrationRequest{
		CSRPEM:          string(csrPEM),
		AgentVersion:    "v0.1.0",
		ProtocolVersion: "v1",
	}
	response := performAgentRegistrationRequest(
		t,
		router,
		created.Token,
		"consume-registration-token-0001",
		requestBody,
	)
	if response.Code != http.StatusCreated {
		t.Fatalf(
			"registration status = %d, want %d: %s",
			response.Code,
			http.StatusCreated,
			response.Body,
		)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
	}
	var result agentRegistrationResponse
	if err := decodeSuccessResponse(response, &result); err != nil {
		t.Fatal(err)
	}
	if result.ClusterID == "" ||
		result.AgentID == "" ||
		result.CertificatePEM == "" ||
		result.CertificateExpiresAt.IsZero() {
		t.Fatalf("incomplete registration response: %+v", result)
	}
	assertUTC8Time(t, "certificate_expires_at", result.CertificateExpiresAt)

	leafBlock, _ := pem.Decode([]byte(result.CertificatePEM))
	if leafBlock == nil {
		t.Fatal("registration response contains no leaf certificate")
	}
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.CheckSignatureFrom(caCertificate); err != nil {
		t.Fatalf("verify registered Agent certificate: %v", err)
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
		t.Fatal("registered certificate does not use the CSR public key")
	}
	expectedIdentityURI := "zke://agent/tenants/" + tenantID +
		"/projects/" + projectID +
		"/clusters/" + result.ClusterID +
		"/agents/" + result.AgentID
	if len(leaf.URIs) != 1 || leaf.URIs[0].String() != expectedIdentityURI {
		t.Fatalf("certificate identity URI = %v, want %s", leaf.URIs, expectedIdentityURI)
	}

	retry := performAgentRegistrationRequest(
		t,
		router,
		created.Token,
		"consume-registration-token-0001",
		requestBody,
	)
	if retry.Code != http.StatusOK {
		t.Fatalf("registration retry status = %d, want %d: %s", retry.Code, http.StatusOK, retry.Body)
	}
	var retryResult agentRegistrationResponse
	if err := decodeSuccessResponse(retry, &retryResult); err != nil {
		t.Fatal(err)
	}
	if retryResult != result {
		t.Fatalf("registration retry result = %+v, want %+v", retryResult, result)
	}

	conflictingCSR, _ := createHTTPTestAgentCSR(t)
	requestBody.CSRPEM = string(conflictingCSR)
	conflict := performAgentRegistrationRequest(
		t,
		router,
		created.Token,
		"consume-registration-token-0001",
		requestBody,
	)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("CSR conflict status = %d, want %d: %s", conflict.Code, http.StatusConflict, conflict.Body)
	}
	assertErrorCode(t, conflict, "idempotency_conflict")

	requestBody.CSRPEM = string(csrPEM)
	reused := performAgentRegistrationRequest(
		t,
		router,
		created.Token,
		"consume-registration-token-0002",
		requestBody,
	)
	if reused.Code != http.StatusUnauthorized {
		t.Fatalf("reused token status = %d, want %d: %s", reused.Code, http.StatusUnauthorized, reused.Body)
	}
	assertErrorCode(t, reused, "invalid_enrollment_token")

	var clusterCount, agentCount, credentialCount, successAuditCount int
	for query, target := range map[string]*int{
		"SELECT count(*) FROM clusters":          &clusterCount,
		"SELECT count(*) FROM agents":            &agentCount,
		"SELECT count(*) FROM agent_credentials": &credentialCount,
		`SELECT count(*) FROM audit_events
WHERE action = 'cluster.enroll' AND result = 'succeeded'`: &successAuditCount,
	} {
		if err := pool.QueryRow(ctx, query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if clusterCount != 1 ||
		agentCount != 1 ||
		credentialCount != 1 ||
		successAuditCount != 1 {
		t.Fatalf(
			"clusters/agents/credentials/audits = %d/%d/%d/%d, want 1/1/1/1",
			clusterCount,
			agentCount,
			credentialCount,
			successAuditCount,
		)
	}
	var storedClusterName, storedAgentNamespace string
	if err := pool.QueryRow(
		ctx,
		"SELECT name, agent_namespace FROM clusters WHERE id = $1",
		result.ClusterID,
	).Scan(&storedClusterName, &storedAgentNamespace); err != nil {
		t.Fatal(err)
	}
	if storedClusterName != "integration-cluster" {
		t.Fatalf("registered cluster name = %q, want integration-cluster", storedClusterName)
	}
	if storedAgentNamespace != "integration-agent" {
		t.Fatalf("registered Agent Namespace = %q, want integration-agent", storedAgentNamespace)
	}

	unavailableService := enrollment.NewService(
		store.NewEnrollmentStore(pool),
		enrollment.ServiceConfig{TokenTTL: enrollment.DefaultTokenTTL, ConfigurationResolver: staticEnrollmentConfigurationResolver{}},
	)
	unavailableToken, err := unavailableService.Create(ctx, enrollment.CreateInput{
		ProjectID:      projectID,
		ClusterName:    "unavailable-integration-cluster",
		AgentNamespace: "unavailable-agent",
		UserID:         userID,
		RequestID:      "request-create-unavailable-registration-token",
		IdempotencyKey: "create-unavailable-registration-token-0001",
		Now:            now,
	})
	if err != nil {
		t.Fatal(err)
	}
	unavailableRouter := New(
		discardLogger(),
		Dependencies{EnrollmentService: unavailableService},
		Config{
			Authentication: defaultAuthenticationTestConfig(),
			AgentEnrollment: AgentEnrollmentHTTPConfig{
				OperationTimeout:     5 * time.Second,
				RateLimitWindow:      time.Minute,
				MaxAttemptsPerSource: 100,
			},
		},
	)
	unavailable := performAgentRegistrationRequest(
		t,
		unavailableRouter,
		unavailableToken.Token,
		"consume-unavailable-registration-0001",
		requestBody,
	)
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"unconfigured CA status = %d, want %d: %s",
			unavailable.Code,
			http.StatusServiceUnavailable,
			unavailable.Body,
		)
	}
	assertErrorCode(t, unavailable, "service_unavailable")
	var failedAuditCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM audit_events
WHERE action = 'cluster.enroll'
  AND target_id = $1
  AND result = 'failed'
`, unavailableToken.ID).Scan(&failedAuditCount); err != nil {
		t.Fatal(err)
	}
	if failedAuditCount != 1 {
		t.Fatalf("unconfigured CA failure audit count = %d, want 1", failedAuditCount)
	}

	resumed := performAgentRegistrationRequest(
		t,
		router,
		unavailableToken.Token,
		"consume-unavailable-registration-0001",
		requestBody,
	)
	if resumed.Code != http.StatusCreated {
		t.Fatalf(
			"resumed registration status = %d, want %d: %s",
			resumed.Code,
			http.StatusCreated,
			resumed.Body,
		)
	}
}

func TestAgentRegistrationRejectsMissingTokenAndOversizedBody(t *testing.T) {
	router := New(
		discardLogger(),
		Dependencies{},
		Config{
			Authentication: defaultAuthenticationTestConfig(),
			AgentEnrollment: AgentEnrollmentHTTPConfig{
				OperationTimeout:     time.Second,
				RateLimitWindow:      time.Minute,
				MaxAttemptsPerSource: 10,
			},
		},
	)
	missingTokenResponse := httptest.NewRecorder()
	missingTokenRequest := httptest.NewRequest(
		http.MethodPost,
		"/agent-api/v1/enroll",
		strings.NewReader(`{}`),
	)
	missingTokenRequest.RemoteAddr = "127.0.0.1:12345"
	router.ServeHTTP(missingTokenResponse, missingTokenRequest)
	if missingTokenResponse.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want %d", missingTokenResponse.Code, http.StatusUnauthorized)
	}

	oversizedResponse := httptest.NewRecorder()
	oversizedRequest := httptest.NewRequest(
		http.MethodPost,
		"/agent-api/v1/enroll",
		strings.NewReader(`{"csr_pem":"`+
			strings.Repeat("x", maxAgentEnrollmentRequestBytes)+`"}`),
	)
	oversizedRequest.Header.Set("Authorization", "Bearer token")
	oversizedRequest.RemoteAddr = "127.0.0.1:12345"
	router.ServeHTTP(oversizedResponse, oversizedRequest)
	if oversizedResponse.Code != http.StatusBadRequest {
		t.Fatalf("oversized body status = %d, want %d", oversizedResponse.Code, http.StatusBadRequest)
	}
	assertErrorCode(t, oversizedResponse, "invalid_request")

	agentNamedClusterResponse := httptest.NewRecorder()
	agentNamedClusterRequest := httptest.NewRequest(
		http.MethodPost,
		"/agent-api/v1/enroll",
		strings.NewReader(`{
			"csr_pem":"csr",
			"cluster_name":"agent-controlled-name",
			"agent_version":"development",
			"protocol_version":"v1"
		}`),
	)
	agentNamedClusterRequest.Header.Set("Authorization", "Bearer token")
	agentNamedClusterRequest.RemoteAddr = "127.0.0.1:12345"
	router.ServeHTTP(agentNamedClusterResponse, agentNamedClusterRequest)
	if agentNamedClusterResponse.Code != http.StatusBadRequest {
		t.Fatalf(
			"Agent-provided cluster name status = %d, want %d",
			agentNamedClusterResponse.Code,
			http.StatusBadRequest,
		)
	}
	assertErrorCode(t, agentNamedClusterResponse, "invalid_request")
}

func TestAgentRegistrationAllowsHTTPTransport(t *testing.T) {
	router := New(
		discardLogger(),
		Dependencies{},
		Config{
			Authentication: defaultAuthenticationTestConfig(),
			AgentEnrollment: AgentEnrollmentHTTPConfig{
				OperationTimeout:     time.Second,
				RateLimitWindow:      time.Minute,
				MaxAttemptsPerSource: 10,
			},
		},
	)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/agent-api/v1/enroll",
		strings.NewReader(`{}`),
	)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("HTTP status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	assertErrorCode(t, response, "invalid_enrollment_token")
}

func performAgentRegistrationRequest(
	t *testing.T,
	router http.Handler,
	token string,
	idempotencyKey string,
	body agentRegistrationRequest,
) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/agent-api/v1/enroll",
		bytes.NewReader(encoded),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(idempotencyKeyHeaderName, idempotencyKey)
	request.RemoteAddr = "127.0.0.1:12345"
	router.ServeHTTP(response, request)
	return response
}

func createHTTPTestAgentCA(
	t *testing.T,
	now time.Time,
) ([]byte, []byte, *x509.Certificate) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ZKE HTTP Test Agent CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		SubjectKeyId:          []byte("zke-http-test-agent-ca"),
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
		}), certificate
}

func createHTTPTestAgentCSR(t *testing.T) ([]byte, any) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
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
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	}), &privateKey.PublicKey
}
