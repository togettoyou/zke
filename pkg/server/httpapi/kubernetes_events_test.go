package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/resourcewatch"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type fakeEventWatchService struct {
	input         resourcewatch.Input
	err           error
	waitForCancel bool
}

func (service *fakeEventWatchService) Stream(
	ctx context.Context, input resourcewatch.Input, sink agentprotocol.ResourceWatchSink,
) (resourcewatch.Result, error) {
	service.input = input
	if service.err != nil {
		return resourcewatch.Result{}, service.err
	}
	response := &agentv1.ResourceWatchResponse{Result: agentv1.ResultCode_RESULT_CODE_OK,
		KubernetesStatusCode: 200, ContentType: "application/json", ResourceVersion: "30"}
	if err := sink.Start(response); err != nil {
		return resourcewatch.Result{}, err
	}
	if service.waitForCancel {
		<-ctx.Done()
		return resourcewatch.Result{ResourceVersion: "30", LastResourceVersion: "30"}, ctx.Err()
	}
	event := corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "scheduled", Namespace: "default", UID: "event-uid"},
		Type: "Normal", Reason: "Scheduled", Message: "assigned",
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "example", UID: "pod-uid"}}
	data, _ := json.Marshal(event)
	if err := sink.Event(&agentv1.ResourceWatchEvent{Type: agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_ADDED,
		Object: data, ResourceVersion: "31"}); err != nil {
		return resourcewatch.Result{}, err
	}
	return resourcewatch.Result{ResourceVersion: "30", EventsSent: 1, BytesSent: uint64(len(data)), LastResourceVersion: "31"}, nil
}

func TestKubernetesEventsFollowRevocationClosesInBand(t *testing.T) {
	service := &fakeEventWatchService{waitForCancel: true}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newKubernetesEventsHandler(logger, service, nil, nil, nil, time.Second, KubernetesEventsHTTPConfig{
		MaximumFollowDuration: time.Second, RevalidateInterval: 5 * time.Millisecond, WriteTimeout: time.Second,
	})
	router := gin.New()
	router.Use(httpmiddleware.RequestLogger(logger), func(c *gin.Context) {
		c.Set("authenticated_identity", auth.Identity{User: auth.User{ID: "00000000-0000-4000-8000-000000000001"},
			SessionID: "00000000-0000-4000-8000-000000000002"})
		c.Next()
	})
	router.GET("/clusters/:cluster_id/namespaces/:namespace_name/events", handler.stream)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/default/events?follow=true", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"reason":"access_revoked"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestKubernetesEventsHandlerStreamsStableSSEAndParsesScope(t *testing.T) {
	service := &fakeEventWatchService{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newKubernetesEventsHandler(logger, service, nil, nil, nil, time.Second, KubernetesEventsHTTPConfig{})
	router := gin.New()
	router.Use(httpmiddleware.RequestLogger(logger), func(c *gin.Context) {
		c.Set("authenticated_identity", auth.Identity{User: auth.User{ID: "00000000-0000-4000-8000-000000000001"}})
		c.Next()
	})
	router.GET("/clusters/:cluster_id/namespaces/:namespace_name/events", handler.stream)
	request := httptest.NewRequest(http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/default/events?limit=25&type=Normal&resource_uid=pod-uid", nil)
	request.Header.Set("Last-Event-ID", "29")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"event: ready", "event: kubernetes.event", "event: close", `"reason":"Scheduled"`, `"message":"assigned"`, `"reason":"completed"`} {
		if !strings.Contains(body, expected) {
			t.Errorf("SSE body missing %q: %s", expected, body)
		}
	}
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" ||
		service.input.ClusterID != "00000000-0000-4000-8000-000000000003" || service.input.Namespace != "default" ||
		service.input.InitialLimit != 25 || service.input.EventType != "Normal" || service.input.ResourceVersion != "29" {
		t.Fatalf("status=%d headers=%v input=%+v", response.Code, response.Header(), service.input)
	}
}

func TestKubernetesEventsHandlerKeepsStructuredErrorsBeforeSSEStarts(t *testing.T) {
	service := &fakeEventWatchService{err: resourcewatch.ErrResourceVersionExpired}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newKubernetesEventsHandler(logger, service, nil, nil, nil, time.Second, KubernetesEventsHTTPConfig{})
	router := gin.New()
	router.Use(httpmiddleware.RequestLogger(logger), func(c *gin.Context) {
		c.Set("authenticated_identity", auth.Identity{User: auth.User{ID: "00000000-0000-4000-8000-000000000001"}})
		c.Next()
	})
	router.GET("/clusters/:cluster_id/namespaces/:namespace_name/events", handler.stream)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/default/events", nil))
	if response.Code != http.StatusConflict || response.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		!strings.Contains(response.Body.String(), "resource_version_expired") {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}
