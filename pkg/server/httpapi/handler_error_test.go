package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

// A failure that can account for itself replaces the mapping's fixed message,
// so what the API Server refused reaches the operator instead of "invalid
// request", which says nothing they can act on.
func TestRespondErrorReturnsTheRejectionText(t *testing.T) {
	t.Parallel()

	detail := `Service "test1" is invalid: spec.ports[0].nodePort: Invalid value: 38080: ` +
		"provided port is not in the valid range. The range of valid ports is 30000-32767"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newBaseHandler(logger, nil, 0)
	router := gin.New()
	router.Use(httpmiddleware.RequestLogger(logger))
	var responseError error
	router.GET("/probe", func(c *gin.Context) {
		handler.respondError(c, "probe", responseError, kubernetesResourceErrorMappings()...)
	})

	responseError = &kubernetesresource.UpstreamRejection{Message: detail}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/probe", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
	var envelope struct {
		Message string `json:"message"`
		Data    struct {
			ErrorCode string `json:"error_code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.ErrorCode != "cluster_api_rejected" ||
		!strings.Contains(envelope.Message, "30000-32767") {
		t.Fatalf("rejection envelope = %s", response.Body.String())
	}

	// An error with nothing of its own to say keeps the mapping's message.
	responseError = kubernetesresource.ErrInvalidInput
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/probe", nil))
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.ErrorCode != "invalid_request" ||
		envelope.Message != "invalid Kubernetes resource request" {
		t.Fatalf("generic envelope = %s", response.Body.String())
	}
}
