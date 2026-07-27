package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// The installation manifest is reachable without a session, so it must be
// throttled per source like the enrollment endpoint it is paired with.
func TestAgentInstallationManifestIsRateLimitedPerSource(t *testing.T) {
	t.Parallel()

	handler := newAgentInstallationHandler(
		discardLogger(),
		nil,
		nil,
		time.Second,
		AgentEnrollmentHTTPConfig{
			RateLimitWindow:      time.Minute,
			MaxAttemptsPerSource: 2,
		},
	)
	router := gin.New()
	router.GET("/agent-install/v1/manifest", handler.manifest)

	statuses := make([]int, 0, 3)
	for range 3 {
		request := httptest.NewRequest(
			http.MethodGet,
			"/agent-install/v1/manifest",
			nil,
		)
		request.RemoteAddr = "192.0.2.10:34567"
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		statuses = append(statuses, response.Code)
		if response.Code == http.StatusTooManyRequests &&
			response.Header().Get("Retry-After") == "" {
			t.Fatal("rate-limited manifest response omitted Retry-After")
		}
	}
	if statuses[2] != http.StatusTooManyRequests {
		t.Fatalf("manifest statuses = %v, want the third to be 429", statuses)
	}
}

// Both token-bearing endpoints must read the Authorization header the same
// way, so a scheme RFC 7235 allows is not accepted by one and refused by the
// other.
func TestBearerTokenParsing(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{"BEARER abc", "abc", true},
		{"Bearer   abc", "abc", true},
		{"Bearer", "", false},
		{"Bearer ", "", false},
		{"Basic abc", "", false},
		{"", "", false},
		{"Bearer abc def", "", false},
	} {
		token, ok := bearerToken(testCase.header)
		if ok != testCase.ok || token != testCase.want {
			t.Errorf(
				"bearerToken(%q) = (%q, %v), want (%q, %v)",
				testCase.header, token, ok, testCase.want, testCase.ok,
			)
		}
	}
}
