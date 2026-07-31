package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

type fakeKubernetesWorkloadService struct {
	listInput    kubernetesresource.ListWorkloadsInput
	getClusterID string
	getNamespace string
	getResource  kubernetesresource.WorkloadResource
	getName      string
}

func (service *fakeKubernetesWorkloadService) ListWorkloads(
	_ context.Context,
	input kubernetesresource.ListWorkloadsInput,
) (kubernetesresource.WorkloadPage, error) {
	service.listInput = input
	return kubernetesresource.WorkloadPage{
		Workloads: []kubernetesresource.WorkloadSummary{{
			Resource:   input.Resource,
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Namespace:  input.Namespace,
			Name:       "inference",
			Labels:     map[string]string{},
			Status:     "available",
			Images:     []string{"example/model:v2"},
		}},
	}, nil
}

func (service *fakeKubernetesWorkloadService) GetWorkload(
	_ context.Context,
	clusterID string,
	namespace string,
	resource kubernetesresource.WorkloadResource,
	name string,
) (kubernetesresource.WorkloadDetail, error) {
	service.getClusterID = clusterID
	service.getNamespace = namespace
	service.getResource = resource
	service.getName = name
	return kubernetesresource.WorkloadDetail{
		WorkloadSummary: kubernetesresource.WorkloadSummary{
			Resource:   resource,
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Namespace:  namespace,
			Name:       name,
			Labels:     map[string]string{},
			Status:     "available",
			Images:     []string{"example/model:v2"},
		},
		Annotations:    map[string]string{},
		Containers:     []kubernetesresource.WorkloadContainer{},
		InitContainers: []kubernetesresource.WorkloadContainer{},
		Conditions:     []kubernetesresource.WorkloadCondition{},
	}, nil
}

func TestKubernetesWorkloadHandlersPreserveClusterNamespaceAndResource(t *testing.T) {
	t.Parallel()

	const clusterID = "00000000-0000-4000-8000-000000000003"
	service := &fakeKubernetesWorkloadService{}
	handler := newKubernetesWorkloadHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		service,
		time.Second,
	)
	router := gin.New()
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource",
		handler.list,
	)
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name",
		handler.get,
	)

	response := httptest.NewRecorder()
	router.ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodGet,
			"/clusters/"+clusterID+
				"/namespaces/model-serving/workloads/deployments"+
				"?limit=25&continue=next&label_selector=app%3Dinference",
			nil,
		),
	)
	if response.Code != http.StatusOK ||
		service.listInput.ClusterID != clusterID ||
		service.listInput.Namespace != "model-serving" ||
		service.listInput.Resource != kubernetesresource.WorkloadDeployments ||
		service.listInput.Limit != 25 ||
		service.listInput.ContinueToken != "next" ||
		service.listInput.LabelSelector != "app=inference" ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"workloads"`)) {
		t.Fatalf(
			"list status=%d input=%+v body=%s",
			response.Code,
			service.listInput,
			response.Body.String(),
		)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodGet,
			"/clusters/"+clusterID+
				"/namespaces/model-serving/workloads/deployments/inference",
			nil,
		),
	)
	if response.Code != http.StatusOK ||
		service.getClusterID != clusterID ||
		service.getNamespace != "model-serving" ||
		service.getResource != kubernetesresource.WorkloadDeployments ||
		service.getName != "inference" ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"name":"inference"`)) {
		t.Fatalf(
			"get status=%d scope=%q/%q/%q/%q body=%s",
			response.Code,
			service.getClusterID,
			service.getNamespace,
			service.getResource,
			service.getName,
			response.Body.String(),
		)
	}
}

func TestKubernetesWorkloadHandlersRejectUnknownResourceAndDetailQuery(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesWorkloadService{}
	handler := newKubernetesWorkloadHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		service,
		time.Second,
	)
	router := gin.New()
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource",
		handler.list,
	)
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name",
		handler.get,
	)

	for _, path := range []string{
		"/clusters/cluster/namespaces/default/workloads/pods",
		"/clusters/cluster/namespaces/default/workloads/deployments/demo?watch=true",
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(
			response,
			httptest.NewRequest(http.MethodGet, path, nil),
		)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if service.listInput.ClusterID != "" || service.getClusterID != "" {
		t.Fatal("invalid workload request reached service")
	}
}
