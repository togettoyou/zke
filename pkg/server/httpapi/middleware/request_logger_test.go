package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/shared/requestctx"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

func TestRequestLoggerPropagatesCorrelationIDToRequestContext(t *testing.T) {
	t.Parallel()

	router := gin.New()
	router.Use(RequestLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	var contextID string
	router.GET("/request", func(c *gin.Context) {
		contextID = requestctx.ID(c.Request.Context())
		c.Status(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/request", nil),
	)
	headerID := response.Header().Get("X-Request-ID")
	if !validation.IsUUID(headerID) {
		t.Fatalf("X-Request-ID = %q, want UUID", headerID)
	}
	if contextID != headerID {
		t.Fatalf("context ID = %q header ID = %q", contextID, headerID)
	}
}
