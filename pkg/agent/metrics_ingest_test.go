package agent

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/observability"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	testIngestToken   = "0123456789abcdef0123456789abcdef"
	testPreviousToken = "fedcba9876543210fedcba9876543210"
)

func testIngestForwarder(t *testing.T, secretData map[string][]byte) *metricsIngestForwarder {
	t.Helper()
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      observability.IngestSecretName,
			Namespace: defaultIdentityNamespace,
		},
		Data: secretData,
	})
	tokens := newMetricsIngestTokens(client, defaultIdentityNamespace)
	if err := tokens.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig().MetricsIngest
	config.MaxBatchBytes = 64
	config.MaxConcurrentBatches = 1
	return newMetricsIngestForwarder(config, tokens, slog.New(slog.DiscardHandler))
}

func ingestRequest(body string, token string) *http.Request {
	request := httptest.NewRequest(
		http.MethodPost,
		observability.IngestWritePath,
		strings.NewReader(body),
	)
	request.Header.Set("Content-Encoding", "snappy")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request
}

func TestMetricsIngestEndpointRequiresAKnownToken(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		token  string
		header string
		want   int
	}{
		// Without a Connection an authorized request can get no further than
		// 503, which is exactly what distinguishes it from a rejected one.
		"current token":  {token: testIngestToken, want: http.StatusServiceUnavailable},
		"previous token": {token: testPreviousToken, want: http.StatusServiceUnavailable},
		"no token":       {want: http.StatusUnauthorized},
		"wrong token":    {token: strings.Repeat("z", 32), want: http.StatusUnauthorized},
		"prefix of a valid token": {
			token: testIngestToken[:16],
			want:  http.StatusUnauthorized,
		},
		"wrong scheme": {header: "Basic " + testIngestToken, want: http.StatusUnauthorized},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// One forwarder per subtest: they run in parallel and the
			// in-flight budget is deliberately one, so a shared forwarder
			// would have them answering each other's 429s.
			forwarder := testIngestForwarder(t, map[string][]byte{
				observability.IngestTokenKey:         []byte(testIngestToken),
				observability.IngestPreviousTokenKey: []byte(testPreviousToken),
			})
			request := ingestRequest("payload", testCase.token)
			if testCase.header != "" {
				request.Header.Set("Authorization", testCase.header)
			}
			recorder := httptest.NewRecorder()
			forwarder.ServeHTTP(recorder, request)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.want)
			}
		})
	}
}

func TestMetricsIngestEndpointTellsCollectorsToRetryWhileOffline(t *testing.T) {
	t.Parallel()

	forwarder := testIngestForwarder(t, map[string][]byte{
		observability.IngestTokenKey: []byte(testIngestToken),
	})
	recorder := httptest.NewRecorder()
	forwarder.ServeHTTP(recorder, ingestRequest("payload", testIngestToken))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	// Without a retry hint the collector would drop what it is holding rather
	// than buffer it until the Agent reconnects.
	if recorder.Header().Get("Retry-After") == "" {
		t.Fatal("an offline Agent gave the collector no retry hint")
	}
}

func TestMetricsIngestEndpointRejectsUnusableRequests(t *testing.T) {
	t.Parallel()

	forwarder := testIngestForwarder(t, map[string][]byte{
		observability.IngestTokenKey: []byte(testIngestToken),
	})
	cases := map[string]struct {
		build func() *http.Request
		want  int
	}{
		"wrong path": {
			build: func() *http.Request {
				request := ingestRequest("payload", testIngestToken)
				request.URL.Path = "/metrics"
				return request
			},
			want: http.StatusNotFound,
		},
		"wrong method": {
			build: func() *http.Request {
				request := ingestRequest("payload", testIngestToken)
				request.Method = http.MethodGet
				return request
			},
			want: http.StatusMethodNotAllowed,
		},
		"uncompressed": {
			build: func() *http.Request {
				request := ingestRequest("payload", testIngestToken)
				request.Header.Del("Content-Encoding")
				return request
			},
			want: http.StatusUnsupportedMediaType,
		},
		"remote write 2.0 by version": {
			build: func() *http.Request {
				request := ingestRequest("payload", testIngestToken)
				request.Header.Set("X-Prometheus-Remote-Write-Version", "2.0.0")
				return request
			},
			want: http.StatusUnsupportedMediaType,
		},
		"remote write 2.0 by content type": {
			build: func() *http.Request {
				request := ingestRequest("payload", testIngestToken)
				request.Header.Set(
					"Content-Type",
					"application/x-protobuf;proto=io.prometheus.write.v2.Request",
				)
				return request
			},
			want: http.StatusUnsupportedMediaType,
		},
		"oversized batch": {
			build: func() *http.Request {
				return ingestRequest(strings.Repeat("x", 65), testIngestToken)
			},
			want: http.StatusRequestEntityTooLarge,
		},
		"empty batch": {
			build: func() *http.Request {
				return ingestRequest("", testIngestToken)
			},
			want: http.StatusNoContent,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			forwarder.ServeHTTP(recorder, testCase.build())
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.want)
			}
		})
	}
}

func TestMetricsIngestVerdictsReachTheCollector(t *testing.T) {
	t.Parallel()

	forwarder := testIngestForwarder(t, map[string][]byte{
		observability.IngestTokenKey: []byte(testIngestToken),
	})
	cases := map[string]struct {
		ack       *agentv1.MetricsIngestAck
		want      int
		wantRetry string
	}{
		"accepted": {
			ack:  &agentv1.MetricsIngestAck{Result: agentv1.ResultCode_RESULT_CODE_OK},
			want: http.StatusNoContent,
		},
		"throttled": {
			ack: &agentv1.MetricsIngestAck{
				Result:           agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
				RetryAfterMillis: 4_000,
			},
			want:      http.StatusTooManyRequests,
			wantRetry: "4",
		},
		"rejected": {
			ack: &agentv1.MetricsIngestAck{
				Result: agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			},
			want: http.StatusBadRequest,
		},
		"forbidden": {
			ack:  &agentv1.MetricsIngestAck{Result: agentv1.ResultCode_RESULT_CODE_FORBIDDEN},
			want: http.StatusForbidden,
		},
		"storage down": {
			ack:  &agentv1.MetricsIngestAck{Result: agentv1.ResultCode_RESULT_CODE_UNAVAILABLE},
			want: http.StatusServiceUnavailable,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			forwarder.writeAck(recorder, testCase.ack)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.want)
			}
			if testCase.wantRetry != "" &&
				recorder.Header().Get("Retry-After") != testCase.wantRetry {
				t.Fatalf(
					"Retry-After = %q, want %q",
					recorder.Header().Get("Retry-After"),
					testCase.wantRetry,
				)
			}
			// A rejection the collector cannot fix must not invite a retry.
			if testCase.want == http.StatusBadRequest &&
				recorder.Header().Get("Retry-After") != "" {
				t.Fatal("a permanent rejection carried a retry hint")
			}
		})
	}
}

func TestMetricsIngestTokensRejectShortSecretValues(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      observability.IngestSecretName,
			Namespace: defaultIdentityNamespace,
		},
		Data: map[string][]byte{observability.IngestTokenKey: []byte("short")},
	})
	tokens := newMetricsIngestTokens(client, defaultIdentityNamespace)
	if err := tokens.refresh(context.Background()); err == nil {
		t.Fatal("a token below the minimum length was accepted")
	}
	if tokens.authorize("Bearer short") {
		t.Fatal("an unusable token authorized a request")
	}
}

func TestMetricsIngestConfigIsAlwaysValidated(t *testing.T) {
	t.Parallel()

	// There is no on/off switch, so an unusable value is always a startup
	// failure rather than something a disabled block could hide.
	configured := DefaultConfig().MetricsIngest
	if err := configured.validate(); err != nil {
		t.Fatal(err)
	}
	configured.SessionTimeout = 2 * time.Hour
	if err := configured.validate(); err == nil {
		t.Fatal("an unbounded session timeout was accepted")
	}
	broken := DefaultConfig().MetricsIngest
	broken.Address = "not-an-address"
	if err := broken.validate(); err == nil {
		t.Fatal("an invalid listen address was accepted")
	}
}
