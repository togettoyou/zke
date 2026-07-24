package agent

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegistrationClientEnrollsOverTLS(t *testing.T) {
	t.Parallel()

	pending, err := newPendingIdentity()
	if err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost ||
			request.URL.Path != agentEnrollmentPath {
			t.Errorf("unexpected request target: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+token {
			t.Error("request does not contain the expected Bearer token")
		}
		if request.Header.Get("Idempotency-Key") != pending.IdempotencyKey {
			t.Error("request does not contain the persisted idempotency key")
		}
		var body registrationRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.CSRPEM != string(pending.CSRPEM) ||
			body.ProtocolVersion != agentProtocolVersion ||
			body.AgentVersion == "" {
			t.Errorf("unexpected enrollment body: %+v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(registrationResponse{
			ClusterID:            testClusterID,
			AgentID:              testAgentID,
			CertificatePEM:       "certificate",
			CertificateExpiresAt: expiresAt,
		})
	}))
	defer server.Close()

	caPath := filepath.Join(t.TempDir(), "server-ca.crt")
	caPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: server.Certificate().Raw,
	})
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := newRegistrationClient(Config{
		ServerAddress:       server.URL,
		ServerCAFile:        caPath,
		RegistrationTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Enroll(
		context.Background(),
		token,
		*pending,
		"development",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ClusterID != testClusterID ||
		result.AgentID != testAgentID ||
		string(result.CertificatePEM) != "certificate" ||
		!result.CertificateExpiresAt.Equal(expiresAt) {
		t.Fatalf("unexpected enrollment result: %+v", result)
	}
}

func TestRegistrationClientAllowsExplicitLoopbackHTTP(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client, err := newRegistrationClient(Config{
		ServerAddress:         server.URL,
		AllowInsecureLoopback: true,
		RegistrationTimeout:   time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.endpoint != server.URL+agentEnrollmentPath {
		t.Fatalf("endpoint = %q, want loopback enrollment endpoint", client.endpoint)
	}
}

func TestRegistrationClientClassifiesRateLimitAsRetryable(t *testing.T) {
	t.Parallel()

	pending, err := newPendingIdentity()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set("Retry-After", "3")
		writer.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(writer).Encode(registrationAPIError{
			Code: "too_many_requests",
		})
	}))
	defer server.Close()

	client := &registrationClient{
		endpoint: server.URL,
		client:   server.Client(),
	}
	_, err = client.Enroll(
		context.Background(),
		"redacted-token",
		*pending,
		"development",
	)
	retry, retryAfter := registrationRetry(err)
	if !retry || retryAfter != 3*time.Second {
		t.Fatalf(
			"registrationRetry() = %t, %s, want true, 3s; error: %v",
			retry,
			retryAfter,
			err,
		)
	}
	if err == nil || strings.Contains(err.Error(), "redacted-token") {
		t.Fatal("registration error is missing or exposes the enrollment token")
	}
}

func TestRegistrationRetryRejectsPermanentTLSError(t *testing.T) {
	t.Parallel()

	retry, _ := registrationRetry(&url.Error{
		Op:  "Post",
		URL: "https://server.example.invalid/agent-api/v1/enroll",
		Err: x509.HostnameError{
			Certificate: &x509.Certificate{},
			Host:        "server.example.invalid",
		},
	})
	if retry {
		t.Fatal("registrationRetry() accepted a permanent TLS hostname error")
	}

	retry, _ = registrationRetry(&url.Error{
		Op:  "Post",
		URL: "https://server.example.invalid/agent-api/v1/enroll",
		Err: errors.New("tls handshake configuration failed"),
	})
	if retry {
		t.Fatal("registrationRetry() accepted a permanent TLS handshake error")
	}
}

func TestRegistrationRetryAcceptsConnectionEOF(t *testing.T) {
	t.Parallel()

	retry, _ := registrationRetry(&url.Error{
		Op:  "Post",
		URL: "https://server.example.invalid/agent-api/v1/enroll",
		Err: io.EOF,
	})
	if !retry {
		t.Fatal("registrationRetry() rejected a transient connection EOF")
	}
}

func TestRegistrationClientRetriesTruncatedSuccessResponse(t *testing.T) {
	t.Parallel()

	pending, err := newPendingIdentity()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"cluster_id":"incomplete`))
	}))
	defer server.Close()

	client := &registrationClient{
		endpoint: server.URL,
		client:   server.Client(),
	}
	_, err = client.Enroll(
		context.Background(),
		"redacted-token",
		*pending,
		"development",
	)
	retry, _ := registrationRetry(err)
	if err == nil || !retry {
		t.Fatalf("truncated successful response error = %v, retry = %t", err, retry)
	}
}

func TestReadEnrollmentToken(t *testing.T) {
	t.Parallel()

	expected := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(expected+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := readEnrollmentToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if token != expected {
		t.Fatalf("token = %q, want mounted token", token)
	}
}
