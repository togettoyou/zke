package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/clusteroverview"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

type fakeClusterOverviewService struct {
	get func(context.Context, string) (clusteroverview.Overview, error)
}

func (service *fakeClusterOverviewService) Get(
	ctx context.Context,
	clusterID string,
) (clusteroverview.Overview, error) {
	return service.get(ctx, clusterID)
}

func TestClusterOverviewHandlerReturnsPartialSnapshot(t *testing.T) {
	t.Parallel()

	generatedAt := time.Date(2026, time.August, 2, 1, 2, 3, 0, time.UTC)
	service := &fakeClusterOverviewService{get: func(
		_ context.Context,
		clusterID string,
	) (clusteroverview.Overview, error) {
		if clusterID != testHTTPClusterID {
			t.Fatalf("cluster ID = %q", clusterID)
		}
		return clusteroverview.Overview{
			GeneratedAt: generatedAt,
			Partial:     true,
			Issues: []clusteroverview.SectionIssue{{
				Section: "workloads.jobs",
				Code:    "cluster_api_forbidden",
			}},
			Nodes: clusteroverview.NodeOverview{
				Total: 2, StatusCounts: map[string]int64{"ready": 2},
			},
			Namespaces: clusteroverview.NamespaceOverview{
				Total: 3, StatusCounts: map[string]int64{"active": 3},
			},
			Pods: clusteroverview.PodOverview{
				Total: 5, Ready: 4, NotReady: 1,
				StatusCounts: map[string]int64{"running": 5},
			},
			Workloads: clusteroverview.WorkloadOverview{
				Total: 1, StatusCounts: map[string]int64{"ready": 1},
				ByResource: []clusteroverview.WorkloadResourceOverview{},
			},
			Resources: clusteroverview.ClusterResourceTotals{
				CPUAllocatableMillis: 8000,
				CPURequestedMillis:   1250,
			},
		}, nil
	}}
	response := httptest.NewRecorder()
	clusterOverviewHandlerTestRouter(service).ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodGet,
			"/api/v1/clusters/"+testHTTPClusterID+"/overview",
			nil,
		),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	var result clusteroverview.Overview
	if err := decodeSuccessResponse(response, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Partial || len(result.Issues) != 1 ||
		result.Issues[0].Section != "workloads.jobs" ||
		result.Nodes.Total != 2 || result.Resources.CPURequestedMillis != 1250 ||
		!result.GeneratedAt.Equal(generatedAt) {
		t.Fatalf("unexpected overview: %+v", result)
	}
}

func TestClusterOverviewHandlerRejectsQueryParameters(t *testing.T) {
	t.Parallel()

	service := &fakeClusterOverviewService{get: func(
		context.Context,
		string,
	) (clusteroverview.Overview, error) {
		t.Fatal("invalid request reached service")
		return clusteroverview.Overview{}, nil
	}}
	response := httptest.NewRecorder()
	clusterOverviewHandlerTestRouter(service).ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodGet,
			"/api/v1/clusters/"+testHTTPClusterID+"/overview?refresh=true",
			nil,
		),
	)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	assertErrorCode(t, response, "invalid_request")
}

func TestClusterOverviewHandlerMapsFailure(t *testing.T) {
	t.Parallel()

	service := &fakeClusterOverviewService{get: func(
		context.Context,
		string,
	) (clusteroverview.Overview, error) {
		return clusteroverview.Overview{}, kubernetesresource.ErrAgentNotConnected
	}}
	response := httptest.NewRecorder()
	clusterOverviewHandlerTestRouter(service).ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodGet,
			"/api/v1/clusters/"+testHTTPClusterID+"/overview",
			nil,
		),
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	assertErrorCode(t, response, "agent_not_connected")
}

func clusterOverviewHandlerTestRouter(service clusterOverviewService) http.Handler {
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	router := gin.New()
	handler := newClusterOverviewHandler(discardLogger(), service, 5*time.Second)
	router.GET("/api/v1/clusters/:cluster_id/overview", handler.get)
	return router
}
