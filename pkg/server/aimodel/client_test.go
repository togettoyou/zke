package aimodel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func probeAgainst(t *testing.T, handler http.HandlerFunc, timeout time.Duration) Outcome {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewHTTPProber().Probe(context.Background(), Target{
		BaseURL:     server.URL + "/v1",
		Model:       "qwen2.5-32b-instruct",
		APIProtocol: APIProtocolChatCompletions,
		APIKey:      "sk-test",
		Timeout:     timeout,
	})
}

func TestProbeSucceedsOnACompletionResponse(t *testing.T) {
	t.Parallel()

	var receivedPath, receivedAuthorization string
	var receivedBody map[string]any
	outcome := probeAgainst(t, func(writer http.ResponseWriter, request *http.Request) {
		receivedPath = request.URL.Path
		receivedAuthorization = request.Header.Get("Authorization")
		payload, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(payload, &receivedBody)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	}, 5*time.Second)

	if !outcome.Succeeded || outcome.Status != http.StatusOK {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	if receivedPath != "/v1"+chatCompletionsPath {
		t.Fatalf("probe called %q", receivedPath)
	}
	if receivedAuthorization != "Bearer sk-test" {
		t.Fatalf("credential not presented: %q", receivedAuthorization)
	}
	if receivedBody["model"] != "qwen2.5-32b-instruct" {
		t.Fatalf("probe did not carry the configured model: %v", receivedBody["model"])
	}
}

func TestProbeSupportsResponsesAPI(t *testing.T) {
	t.Parallel()

	var receivedPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		receivedPath = request.URL.Path
		_, _ = writer.Write([]byte(`{"output":[{"type":"message"}]}`))
	}))
	t.Cleanup(server.Close)

	outcome := NewHTTPProber().Probe(context.Background(), Target{
		BaseURL: server.URL + "/v1", Model: "agent-model",
		APIProtocol: APIProtocolResponses, Timeout: 5 * time.Second,
	})
	if !outcome.Succeeded || receivedPath != "/v1"+responsesPath {
		t.Fatalf("unexpected Responses probe: path=%q outcome=%+v", receivedPath, outcome)
	}
}

// An endpoint that takes no credential is a supported configuration, and the
// probe must not invent an Authorization header for it.
func TestProbeOmitsTheHeaderWithoutAKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, present := request.Header["Authorization"]; present {
			t.Error("unauthenticated endpoint received an Authorization header")
		}
		_, _ = writer.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(server.Close)

	outcome := NewHTTPProber().Probe(context.Background(), Target{
		BaseURL: server.URL, Model: "local-model",
		APIProtocol: APIProtocolChatCompletions, Timeout: 5 * time.Second,
	})
	if !outcome.Succeeded {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
}

func TestProbeClassifiesStatusCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		status  int
		failure Failure
	}{
		{"凭证被拒", http.StatusUnauthorized, FailureUnauthorized},
		{"没有权限", http.StatusForbidden, FailureUnauthorized},
		{"模型或路径不存在", http.StatusNotFound, FailureModelNotFound},
		{"请求被拒绝", http.StatusBadRequest, FailureUnexpectedResponse},
		{"端点自身故障", http.StatusInternalServerError, FailureUnexpectedResponse},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			outcome := probeAgainst(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte(`{"error":{"message":"sk-test leaked into the body"}}`))
			}, 5*time.Second)

			if outcome.Succeeded || outcome.Failure != testCase.failure {
				t.Fatalf("unexpected outcome: %+v", outcome)
			}
			if outcome.Status != testCase.status {
				t.Fatalf("status %d not reported, got %d", testCase.status, outcome.Status)
			}
			// The endpoint's body is outside ZKE and may quote the request.
			// Nothing from it reaches the operator.
			if outcome.Detail == "" || strings.Contains(outcome.Detail, "sk-test") {
				t.Fatalf("detail must be written by ZKE: %q", outcome.Detail)
			}
		})
	}
}

// A 200 from something that is not a completions endpoint — a reverse proxy's
// index page at the wrong path, say — is a misconfiguration, not a success.
func TestProbeRefusesANonCompletionBody(t *testing.T) {
	t.Parallel()

	outcome := probeAgainst(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte("<html><body>nginx</body></html>"))
	}, 5*time.Second)

	if outcome.Succeeded || outcome.Failure != FailureUnexpectedResponse {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
}

func TestProbeReportsATimeout(t *testing.T) {
	t.Parallel()

	// The handler waits for the client to give up rather than for a fixed
	// delay, so the test spends exactly the probe's timeout and the server can
	// still shut down cleanly afterwards.
	outcome := probeAgainst(t, func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-time.After(2 * time.Second):
		}
		writer.WriteHeader(http.StatusOK)
	}, 100*time.Millisecond)

	if outcome.Succeeded || outcome.Failure != FailureTimeout {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	if outcome.Status != 0 {
		t.Fatalf("no response arrived, so no status: %d", outcome.Status)
	}
}

func TestProbeReportsAnUnreachableEndpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()

	outcome := NewHTTPProber().Probe(context.Background(), Target{
		BaseURL: address + "/v1", Model: "qwen2.5-32b-instruct",
		APIProtocol: APIProtocolChatCompletions, Timeout: 5 * time.Second,
	})
	if outcome.Succeeded || outcome.Failure != FailureUnreachable {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
}

// Following a redirect would hand the credential to a host the operator never
// configured, so the redirect is reported rather than followed.
func TestProbeDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	elsewhere := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Errorf("redirect was followed to %s with %q", request.URL.Path, request.Header.Get("Authorization"))
		_, _ = writer.Write([]byte(`{"choices":[]}`))
	}))
	t.Cleanup(elsewhere.Close)

	outcome := probeAgainst(t, func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, &http.Request{}, elsewhere.URL, http.StatusTemporaryRedirect)
	}, 5*time.Second)

	if outcome.Succeeded || outcome.Failure != FailureUnexpectedResponse {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
}
