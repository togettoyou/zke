package metricsingest

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang/snappy"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/agentconn"
)

func testGateway(t *testing.T, handler http.HandlerFunc) (*Gateway, func()) {
	t.Helper()
	backend := httptest.NewServer(handler)
	gateway, err := New(
		Config{
			WriteURL:             backend.URL + "/api/v1/write",
			WriteTimeout:         2 * time.Second,
			MaxDecompressedBytes: 4096,
			Limits:               testLimits(),
			HTTPClient:           backend.Client(),
			Now:                  func() time.Time { return time.Unix(1_755_216_000, 0).UTC() },
		},
		slog.New(slog.DiscardHandler),
	)
	if err != nil {
		backend.Close()
		t.Fatal(err)
	}
	return gateway, backend.Close
}

func testBatch(t *testing.T, labels ...label) []byte {
	t.Helper()
	series := testSeries{
		labels:  labels,
		samples: []testSample{{value: 1, timestampMS: 1_755_216_000_000}},
	}
	return snappy.Encode(nil, encodeWriteRequest(series))
}

func TestGatewayStampsConnectionIdentityOnForwardedSamples(t *testing.T) {
	t.Parallel()

	forwarded := make(chan []byte, 1)
	gateway, stop := testGateway(t, func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
		if err != nil {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		if request.Header.Get("Content-Encoding") != "snappy" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		forwarded <- body
		writer.WriteHeader(http.StatusNoContent)
	})
	defer stop()

	batch := testBatch(
		t,
		label{name: "__name__", value: "up"},
		label{name: ClusterLabel, value: "cls_impersonated"},
	)
	outcome := gateway.IngestMetrics(
		context.Background(),
		agentconn.MetricsScope{
			TenantID:  "tenant_1",
			ProjectID: "project_1",
			ClusterID: "cls_real",
		},
		bytes.NewReader(batch),
		uint64(len(batch)),
	)
	if outcome.Result != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("outcome = %+v", outcome)
	}
	body := <-forwarded
	decoded, err := snappy.Decode(nil, body)
	if err != nil {
		t.Fatal(err)
	}
	labels := decodeLabels(t, decoded)
	if len(labels) != 1 {
		t.Fatalf("forwarded series = %d, want 1", len(labels))
	}
	var scopeValues []string
	for _, current := range labels[0] {
		if strings.HasPrefix(current.name, ScopeLabelPrefix) {
			scopeValues = append(scopeValues, current.name+"="+current.value)
		}
	}
	if len(scopeValues) != 1 || scopeValues[0] != ClusterLabel+"=cls_real" {
		t.Fatalf("scope labels = %v", scopeValues)
	}
}

func TestGatewayTranslatesBackendVerdicts(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		status       int
		retryAfter   string
		wantResult   agentv1.ResultCode
		wantRetry    bool
		wantExactMS  uint64
		wantForwards bool
	}{
		"throttled": {
			status:       http.StatusTooManyRequests,
			retryAfter:   "3",
			wantResult:   agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED,
			wantRetry:    true,
			wantExactMS:  3_000,
			wantForwards: true,
		},
		"rejected": {
			status:       http.StatusBadRequest,
			wantResult:   agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			wantForwards: true,
		},
		"unavailable": {
			status:       http.StatusBadGateway,
			wantResult:   agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
			wantRetry:    true,
			wantForwards: true,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			gateway, stop := testGateway(t, func(writer http.ResponseWriter, _ *http.Request) {
				if testCase.retryAfter != "" {
					writer.Header().Set("Retry-After", testCase.retryAfter)
				}
				writer.WriteHeader(testCase.status)
			})
			defer stop()

			batch := testBatch(t, label{name: "__name__", value: "up"})
			outcome := gateway.IngestMetrics(
				context.Background(),
				agentconn.MetricsScope{ClusterID: "cls_real"},
				bytes.NewReader(batch),
				uint64(len(batch)),
			)
			if outcome.Result != testCase.wantResult {
				t.Fatalf("result = %v, want %v", outcome.Result, testCase.wantResult)
			}
			if testCase.wantRetry && outcome.RetryAfterMillis == 0 {
				t.Fatal("a retryable verdict carried no retry hint")
			}
			if !testCase.wantRetry && outcome.RetryAfterMillis != 0 {
				t.Fatalf(
					"a permanent verdict carried a %d ms retry hint",
					outcome.RetryAfterMillis,
				)
			}
			if testCase.wantExactMS != 0 &&
				outcome.RetryAfterMillis != testCase.wantExactMS {
				t.Fatalf(
					"retry hint = %d ms, want %d ms",
					outcome.RetryAfterMillis,
					testCase.wantExactMS,
				)
			}
			if outcome.Reason == "" || outcome.Message == "" {
				t.Fatal("a rejection must explain itself")
			}
		})
	}
}

func TestGatewayRefusesOversizedDecompressedPayloadsWithoutForwarding(t *testing.T) {
	t.Parallel()

	forwarded := false
	gateway, stop := testGateway(t, func(writer http.ResponseWriter, _ *http.Request) {
		forwarded = true
		writer.WriteHeader(http.StatusNoContent)
	})
	defer stop()

	// Highly compressible input: a small batch that expands far past the
	// configured decompressed ceiling. The size is read from the snappy header
	// and refused before a buffer that large is ever allocated.
	bomb := snappy.Encode(nil, bytes.Repeat([]byte{0}, 1<<20))
	outcome := gateway.IngestMetrics(
		context.Background(),
		agentconn.MetricsScope{ClusterID: "cls_real"},
		bytes.NewReader(bomb),
		uint64(len(bomb)),
	)
	if outcome.Result != agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT ||
		outcome.Reason != "PayloadTooLarge" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if forwarded {
		t.Fatal("a rejected batch reached the storage backend")
	}
}

func TestGatewayRejectsTruncatedPayloads(t *testing.T) {
	t.Parallel()

	gateway, stop := testGateway(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	defer stop()

	batch := testBatch(t, label{name: "__name__", value: "up"})
	outcome := gateway.IngestMetrics(
		context.Background(),
		agentconn.MetricsScope{ClusterID: "cls_real"},
		bytes.NewReader(batch[:len(batch)-1]),
		uint64(len(batch)),
	)
	if outcome.Result != agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT ||
		outcome.Reason != "PayloadTruncated" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestNewRequiresAnAbsoluteStorageURL(t *testing.T) {
	t.Parallel()

	for _, writeURL := range []string{"", "   ", "/api/v1/write", "ftp://host/write"} {
		if _, err := New(
			Config{WriteURL: writeURL},
			slog.New(slog.DiscardHandler),
		); err == nil {
			t.Fatalf("write URL %q was accepted", writeURL)
		}
	}
}
