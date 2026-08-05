package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/podlogs"
)

type fakePodLogsService struct {
	input         podlogs.Input
	err           error
	waitForCancel bool
}

func (service *fakePodLogsService) Stream(
	ctx context.Context,
	input podlogs.Input,
	destination io.Writer,
) (podlogs.Result, error) {
	service.input = input
	if service.waitForCancel {
		<-ctx.Done()
		return podlogs.Result{}, ctx.Err()
	}
	if service.err != nil {
		return podlogs.Result{}, service.err
	}
	written, err := io.WriteString(destination, "secret-log-line\n")
	return podlogs.Result{BytesSent: uint64(written)}, err
}

func TestKubernetesPodLogsHandlerStreamsSnapshotAndFollowWithoutJSONEnvelope(t *testing.T) {
	t.Parallel()

	for _, follow := range []bool{false, true} {
		follow := follow
		t.Run(map[bool]string{false: "snapshot", true: "follow"}[follow], func(t *testing.T) {
			t.Parallel()
			service := &fakePodLogsService{}
			auditStore := &recordingPodAuditStore{}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			handler := newKubernetesPodLogsHandler(
				logger,
				service,
				nil,
				nil,
				audit.NewService(auditStore, nil),
				time.Second,
				PodLogsHTTPConfig{
					MaximumFollowDuration: time.Second,
					RevalidateInterval:    time.Hour,
					WriteTimeout:          time.Second,
				},
			)
			router := gin.New()
			router.Use(httpmiddleware.RequestLogger(logger))
			router.Use(func(c *gin.Context) {
				c.Set("authenticated_identity", auth.Identity{
					User: auth.User{ID: "00000000-0000-4000-8000-000000000001"},
				})
				c.Next()
			})
			router.GET(
				"/clusters/:cluster_id/namespaces/:namespace_name/pods/:pod_name/logs",
				handler.stream,
			)
			followValue := "false"
			if follow {
				followValue = "true"
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(
				http.MethodGet,
				"/clusters/00000000-0000-4000-8000-000000000003/namespaces/model-serving/pods/inference-abcde/logs?uid=pod-uid&container=main&follow="+followValue+"&tail_lines=25&timestamps=true",
				nil,
			))
			result := response.Result()
			if response.Code != http.StatusOK ||
				response.Body.String() != "secret-log-line\n" ||
				result.Header.Get("Content-Type") != "text/plain; charset=utf-8" ||
				result.Trailer.Get("X-ZKE-Log-Result") != "succeeded" ||
				result.Trailer.Get("X-ZKE-Log-Bytes") != "16" ||
				service.input.Follow != follow || service.input.TailLines == nil ||
				*service.input.TailLines != 25 || !service.input.Timestamps {
				t.Fatalf(
					"status=%d headers=%v trailers=%v body=%q input=%+v",
					response.Code,
					result.Header,
					result.Trailer,
					response.Body.String(),
					service.input,
				)
			}
			if follow && result.Header.Get("X-Accel-Buffering") != "no" {
				t.Fatal("follow response does not disable proxy buffering")
			}
			if len(auditStore.events) != 1 ||
				auditStore.events[0].Action != auditaction.KubernetesPodLogsRead ||
				auditStore.events[0].Result != "succeeded" ||
				strings.Contains(auditStore.events[0].TargetName, "secret-log-line") {
				t.Fatalf("unexpected Pod logs audit: %+v", auditStore.events)
			}
		})
	}
}

func TestKubernetesPodLogsHandlerKeepsStructuredErrorsBeforeStreamStarts(t *testing.T) {
	t.Parallel()

	service := &fakePodLogsService{err: podlogs.ErrPodReplaced}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newKubernetesPodLogsHandler(
		logger,
		service,
		nil,
		nil,
		nil,
		time.Second,
		PodLogsHTTPConfig{},
	)
	router := gin.New()
	router.Use(httpmiddleware.RequestLogger(logger))
	router.Use(func(c *gin.Context) {
		c.Set("authenticated_identity", auth.Identity{
			User: auth.User{ID: "00000000-0000-4000-8000-000000000001"},
		})
		c.Next()
	})
	router.GET("/clusters/:cluster_id/namespaces/:namespace_name/pods/:pod_name/logs", handler.stream)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/default/pods/example/logs?uid=stale&container=main",
		nil,
	))
	if response.Code != http.StatusConflict ||
		response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}

	// A container with no previous instance is a missing object, not a request
	// the operator should be told to go and check.
	service.err = podlogs.ErrPreviousLogsNotFound
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/default/pods/example/logs?uid=current&container=main&previous=true",
		nil,
	))
	if response.Code != http.StatusNotFound ||
		!strings.Contains(response.Body.String(), "previous_logs_not_found") {
		t.Fatalf("previous logs status=%d body=%s", response.Code, response.Body.String())
	}

	service.err = errors.New("must not be reached")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/default/pods/example/logs?uid=stale&uid=duplicate&container=main",
		nil,
	))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("duplicate query status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestKubernetesPodLogsFollowRevalidationCancelsStreamFailClosed(t *testing.T) {
	t.Parallel()

	service := &fakePodLogsService{waitForCancel: true}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newKubernetesPodLogsHandler(
		logger,
		service,
		nil,
		nil,
		nil,
		time.Second,
		PodLogsHTTPConfig{
			MaximumFollowDuration: time.Second,
			RevalidateInterval:    5 * time.Millisecond,
			WriteTimeout:          time.Second,
		},
	)
	router := gin.New()
	router.Use(httpmiddleware.RequestLogger(logger))
	router.Use(func(c *gin.Context) {
		c.Set("authenticated_identity", auth.Identity{
			User:      auth.User{ID: "00000000-0000-4000-8000-000000000001"},
			SessionID: "00000000-0000-4000-8000-000000000002",
		})
		c.Next()
	})
	router.GET("/clusters/:cluster_id/namespaces/:namespace_name/pods/:pod_name/logs", handler.stream)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/default/pods/example/logs?uid=pod-uid&container=main&follow=true",
		nil,
	))
	if response.Code != http.StatusForbidden || response.Body.String() == "" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
