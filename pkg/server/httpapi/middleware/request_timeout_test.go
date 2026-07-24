package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRequestTimeoutProvidesOneSharedDeadline(t *testing.T) {
	t.Parallel()

	router := gin.New()
	var firstDeadline time.Time
	router.Use(
		RequestTimeout(time.Second),
		func(c *gin.Context) {
			var exists bool
			firstDeadline, exists = c.Request.Context().Deadline()
			if !exists {
				t.Fatal("request context has no deadline")
			}
			c.Next()
		},
	)
	router.GET("/deadline", func(c *gin.Context) {
		secondDeadline, exists := c.Request.Context().Deadline()
		if !exists {
			t.Fatal("handler context has no deadline")
		}
		if !secondDeadline.Equal(firstDeadline) {
			t.Fatalf(
				"handler deadline = %s, want shared deadline %s",
				secondDeadline,
				firstDeadline,
			)
		}
		c.Status(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/deadline", nil)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}
