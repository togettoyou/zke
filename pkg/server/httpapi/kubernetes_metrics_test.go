package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

type fakeKubernetesMetricsService struct {
	nodeClusterID string
	podClusterID  string
	podNamespace  string
}

func (service *fakeKubernetesMetricsService) ListNodeMetrics(
	_ context.Context,
	clusterID string,
) (kubernetesresource.NodeMetricsSnapshot, error) {
	service.nodeClusterID = clusterID
	return kubernetesresource.NodeMetricsSnapshot{
		MetricsAvailability: kubernetesresource.MetricsAvailability{
			Reason:  kubernetesresource.MetricsUnavailableNotInstalled,
			Message: "metrics missing",
		},
		GeneratedAt: time.Now().UTC(),
		Items:       []kubernetesresource.NodeMetric{},
	}, nil
}

func (service *fakeKubernetesMetricsService) ListPodMetrics(
	_ context.Context,
	clusterID string,
	namespace string,
) (kubernetesresource.PodMetricsSnapshot, error) {
	service.podClusterID = clusterID
	service.podNamespace = namespace
	return kubernetesresource.PodMetricsSnapshot{
		MetricsAvailability: kubernetesresource.MetricsAvailability{Available: true},
		GeneratedAt:         time.Now().UTC(),
		Items:               []kubernetesresource.PodMetric{{Name: "api-0", ContainerCount: 1}},
	}, nil
}

func TestKubernetesMetricsHandlerReturnsAvailabilityAndScopedPods(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesMetricsService{}
	router := kubernetesMetricsHandlerTestRouter(service)

	nodes := httptest.NewRecorder()
	router.ServeHTTP(nodes, httptest.NewRequest(
		http.MethodGet, "/api/v1/clusters/"+testHTTPClusterID+"/metrics/nodes", nil,
	))
	if nodes.Code != http.StatusOK || service.nodeClusterID != testHTTPClusterID {
		t.Fatalf("Node metrics: status=%d body=%s cluster=%q", nodes.Code, nodes.Body, service.nodeClusterID)
	}
	var nodeResult kubernetesresource.NodeMetricsSnapshot
	if err := decodeSuccessResponse(nodes, &nodeResult); err != nil {
		t.Fatal(err)
	}
	if nodeResult.Available || nodeResult.Reason != kubernetesresource.MetricsUnavailableNotInstalled {
		t.Fatalf("Node metrics = %+v", nodeResult)
	}

	pods := httptest.NewRecorder()
	router.ServeHTTP(pods, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/metrics/pods",
		nil,
	))
	if pods.Code != http.StatusOK || service.podClusterID != testHTTPClusterID ||
		service.podNamespace != "default" {
		t.Fatalf("Pod metrics: status=%d body=%s scope=%q/%q", pods.Code, pods.Body, service.podClusterID, service.podNamespace)
	}
}

func kubernetesMetricsHandlerTestRouter(service kubernetesMetricsService) http.Handler {
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	router := gin.New()
	handler := newKubernetesMetricsHandler(discardLogger(), service, nil, 5*time.Second)
	router.GET("/api/v1/clusters/:cluster_id/metrics/nodes", handler.nodes)
	router.GET(
		"/api/v1/clusters/:cluster_id/namespaces/:namespace_name/metrics/pods",
		handler.pods,
	)
	return router
}
