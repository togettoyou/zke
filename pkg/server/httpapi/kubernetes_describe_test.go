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
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesdescribe"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

type fakeDescribeService struct {
	result       kubernetesdescribe.Result
	err          error
	podInput     kubernetesdescribe.PodInput
	nodeInput    kubernetesdescribe.NodeInput
	workloadCall kubernetesdescribe.WorkloadInput
	resourceCall kubernetesdescribe.ResourceInput
	claimCall    kubernetesdescribe.PersistentVolumeClaimInput
	serviceCall  kubernetesdescribe.ServiceInput
	ingressCall  kubernetesdescribe.IngressInput
	gatewayCall  kubernetesdescribe.GatewayInput
}

func (service *fakeDescribeService) DescribeGateway(
	_ context.Context,
	input kubernetesdescribe.GatewayInput,
) (kubernetesdescribe.Result, error) {
	service.gatewayCall = input
	return service.result, service.err
}

func (service *fakeDescribeService) DescribeIngress(
	_ context.Context,
	input kubernetesdescribe.IngressInput,
) (kubernetesdescribe.Result, error) {
	service.ingressCall = input
	return service.result, service.err
}

func (service *fakeDescribeService) DescribeService(
	_ context.Context,
	input kubernetesdescribe.ServiceInput,
) (kubernetesdescribe.Result, error) {
	service.serviceCall = input
	return service.result, service.err
}

func (service *fakeDescribeService) DescribePersistentVolumeClaim(
	_ context.Context,
	input kubernetesdescribe.PersistentVolumeClaimInput,
) (kubernetesdescribe.Result, error) {
	service.claimCall = input
	return service.result, service.err
}

func (service *fakeDescribeService) DescribeNode(
	_ context.Context,
	input kubernetesdescribe.NodeInput,
) (kubernetesdescribe.Result, error) {
	service.nodeInput = input
	return service.result, service.err
}

func (service *fakeDescribeService) DescribePod(
	_ context.Context,
	input kubernetesdescribe.PodInput,
) (kubernetesdescribe.Result, error) {
	service.podInput = input
	return service.result, service.err
}

func (service *fakeDescribeService) DescribeWorkload(
	_ context.Context,
	input kubernetesdescribe.WorkloadInput,
) (kubernetesdescribe.Result, error) {
	service.workloadCall = input
	return service.result, service.err
}

func (service *fakeDescribeService) DescribeResource(
	_ context.Context,
	input kubernetesdescribe.ResourceInput,
) (kubernetesdescribe.Result, error) {
	service.resourceCall = input
	return service.result, service.err
}

func describeTestRouter(service kubernetesDescribeService) *gin.Engine {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newKubernetesDescribeHandler(logger, service, nil, time.Second)
	router := gin.New()
	router.Use(httpmiddleware.RequestLogger(logger))
	router.GET(
		"/clusters/:cluster_id/nodes/:node_name/describe",
		handler.node,
	)
	// Mirrors the existing typed storage detail route. The describe route must
	// share its dynamic prefix; a static `persistentvolumeclaims` sibling would
	// shadow this GET and turn a valid detail read into 405.
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/storage/:storage_resource/:storage_name",
		func(c *gin.Context) { c.Status(http.StatusNoContent) },
	)
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/storage/:storage_resource/:storage_name/describe",
		handler.persistentVolumeClaim,
	)
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/networking/:network_resource/:network_name",
		func(c *gin.Context) { c.Status(http.StatusNoContent) },
	)
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/networking/:network_resource/:network_name/describe",
		handler.networkingResource,
	)
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/pods/:pod_name/describe",
		handler.pod,
	)
	router.GET(
		"/clusters/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name/describe",
		handler.workload,
	)
	router.GET(
		"/clusters/:cluster_id/kubernetes/resources/:resource_name/describe",
		handler.resource,
	)
	return router
}

func TestPersistentVolumeClaimDescribeDoesNotShadowStorageDetail(t *testing.T) {
	t.Parallel()

	router := describeTestRouter(&fakeDescribeService{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/models/storage/persistentvolumeclaims/weights",
		nil,
	)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("storage detail was shadowed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestServiceDescribeDoesNotShadowNetworkingDetail(t *testing.T) {
	t.Parallel()

	router := describeTestRouter(&fakeDescribeService{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/models/networking/services/inference",
		nil,
	)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("networking detail was shadowed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// Describe reads Events, and reading Events is its own permission. A route that
// asked only for cluster.read would hand out a Namespace's Events to callers
// the Event stream refuses, which is the separation cluster.event.read exists
// for.
func TestDescribeRoutesRequireBothTheResourceAndEventPermissions(t *testing.T) {
	t.Parallel()

	wanted := map[string]bool{
		"GET /api/v1/clusters/:cluster_id/nodes/:node_name/describe":                                                       false,
		"GET /api/v1/clusters/:cluster_id/namespaces/:namespace_name/pods/:pod_name/describe":                              false,
		"GET /api/v1/clusters/:cluster_id/namespaces/:namespace_name/workloads/:workload_resource/:workload_name/describe": false,
		"GET /api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name/describe":                                    false,
		"GET /api/v1/clusters/:cluster_id/namespaces/:namespace_name/storage/:storage_resource/:storage_name/describe":     false,
		"GET /api/v1/clusters/:cluster_id/namespaces/:namespace_name/networking/:network_resource/:network_name/describe":  false,
	}
	for _, route := range parseRegisteredRoutes(t) {
		if _, tracked := wanted[route.key()]; !tracked {
			continue
		}
		wanted[route.key()] = true
		// The route table is read as source, so the middleware is matched by the
		// permission constant it names. Rendered middleware spans several lines,
		// so the comparison is made on the text with its layout removed.
		applied := strings.Join(route.middleware, "\n")
		applied = strings.Join(strings.Fields(applied), "")
		for _, permission := range []struct {
			identifier string
			value      rbac.Permission
		}{
			{"rbac.PermissionClusterRead", rbac.PermissionClusterRead},
			{"rbac.PermissionClusterEventRead", rbac.PermissionClusterEventRead},
		} {
			if !strings.Contains(
				applied,
				"RequireCluster("+permission.identifier+",",
			) {
				t.Errorf("%s does not require %s", route.key(), permission.value)
			}
		}
	}
	for key, found := range wanted {
		if !found {
			t.Errorf("describe route %s is no longer registered", key)
		}
	}
}

func TestDescribeServiceReturnsNetworkingDiagnosis(t *testing.T) {
	t.Parallel()

	service := &fakeDescribeService{result: kubernetesdescribe.Result{
		Target: kubernetesdescribe.Target{
			APIVersion: "v1", Kind: "Service", Namespace: "models",
			Name: "inference", UID: "service-uid",
		},
		Family:           kubernetesdescribe.FamilyNetworking,
		Networking:       &kubernetesresource.NetworkingResourceDetail{},
		ServiceEndpoints: &kubernetesdescribe.ServiceEndpoints{Endpoints: 2, ReadyEndpoints: 1},
		Events:           kubernetesdescribe.Events{Items: []kubernetesdescribe.Event{}},
		Findings:         []kubernetesdescribe.Finding{},
		DegradedSections: []string{},
	}}
	router := describeTestRouter(service)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/models/networking/services/inference/describe",
		nil,
	)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if service.serviceCall.ClusterID != "00000000-0000-4000-8000-000000000003" ||
		service.serviceCall.Namespace != "models" || service.serviceCall.Name != "inference" {
		t.Fatalf("unexpected Service describe input: %+v", service.serviceCall)
	}
	if !strings.Contains(recorder.Body.String(), `"family":"networking"`) ||
		!strings.Contains(recorder.Body.String(), `"ready_endpoints":1`) {
		t.Fatalf("Service diagnosis missing from response: %s", recorder.Body.String())
	}
}

func TestDescribeIngressReturnsBackendDiagnosis(t *testing.T) {
	t.Parallel()

	service := &fakeDescribeService{result: kubernetesdescribe.Result{
		Target: kubernetesdescribe.Target{
			APIVersion: "networking.k8s.io/v1", Kind: "Ingress", Namespace: "models",
			Name: "inference", UID: "ingress-uid",
		},
		Family:     kubernetesdescribe.FamilyNetworking,
		Networking: &kubernetesresource.NetworkingResourceDetail{},
		IngressBackends: &kubernetesdescribe.IngressBackends{
			Items: []kubernetesdescribe.IngressBackend{{
				ServiceName: "inference-api", PortNumber: 80,
				Findings: []kubernetesdescribe.Finding{},
			}},
		},
		Events:           kubernetesdescribe.Events{Items: []kubernetesdescribe.Event{}},
		Findings:         []kubernetesdescribe.Finding{},
		DegradedSections: []string{},
	}}
	router := describeTestRouter(service)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/models/networking/ingresses/inference/describe",
		nil,
	)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if service.ingressCall.ClusterID != "00000000-0000-4000-8000-000000000003" ||
		service.ingressCall.Namespace != "models" || service.ingressCall.Name != "inference" {
		t.Fatalf("unexpected Ingress describe input: %+v", service.ingressCall)
	}
	if !strings.Contains(recorder.Body.String(), `"service_name":"inference-api"`) {
		t.Fatalf("Ingress diagnosis missing from response: %s", recorder.Body.String())
	}
}

func TestDescribeGatewayReturnsListenerDiagnosis(t *testing.T) {
	t.Parallel()

	service := &fakeDescribeService{result: kubernetesdescribe.Result{
		Family: kubernetesdescribe.FamilyNetworking,
		GatewayStatus: &kubernetesdescribe.GatewayStatus{
			Listeners: []kubernetesdescribe.GatewayListenerStatus{{
				Name: "https", Findings: []kubernetesdescribe.Finding{},
			}},
		},
		Events:   kubernetesdescribe.Events{Items: []kubernetesdescribe.Event{}},
		Findings: []kubernetesdescribe.Finding{}, DegradedSections: []string{},
	}}
	router := describeTestRouter(service)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/models/networking/gateways/public/describe",
		nil,
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if service.gatewayCall.Namespace != "models" || service.gatewayCall.Name != "public" {
		t.Fatalf("unexpected Gateway describe input: %+v", service.gatewayCall)
	}
	if !strings.Contains(recorder.Body.String(), `"name":"https"`) {
		t.Fatalf("Gateway diagnosis missing from response: %s", recorder.Body.String())
	}
}

func TestDescribeNodeReturnsTheClusterScopedDiagnosis(t *testing.T) {
	t.Parallel()

	node := kubernetesresource.NodeDetail{NodeSummary: kubernetesresource.NodeSummary{
		Name: "worker-a", UID: "node-uid", CPUAllocatable: "4",
	}}
	service := &fakeDescribeService{result: kubernetesdescribe.Result{
		Target: kubernetesdescribe.Target{
			APIVersion: "v1",
			Kind:       "Node",
			Name:       "worker-a",
			UID:        "node-uid",
		},
		Family:           kubernetesdescribe.FamilyNode,
		Node:             &node,
		Events:           kubernetesdescribe.Events{Items: []kubernetesdescribe.Event{}},
		Findings:         []kubernetesdescribe.Finding{},
		DegradedSections: []string{},
	}}
	router := describeTestRouter(service)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/nodes/worker-a/describe",
		nil,
	)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if service.nodeInput.ClusterID != "00000000-0000-4000-8000-000000000003" ||
		service.nodeInput.Name != "worker-a" {
		t.Fatalf("unexpected Node describe input: %+v", service.nodeInput)
	}
	if !strings.Contains(recorder.Body.String(), `"family":"node"`) ||
		!strings.Contains(recorder.Body.String(), `"cpu_allocatable":"4"`) ||
		strings.Contains(recorder.Body.String(), `"NodeSummary"`) ||
		strings.Contains(recorder.Body.String(), `"CPUAllocatable"`) {
		t.Fatalf("Node diagnosis missing from response: %s", recorder.Body.String())
	}
}

func TestDescribePersistentVolumeClaimReturnsStorageDiagnosis(t *testing.T) {
	t.Parallel()

	service := &fakeDescribeService{result: kubernetesdescribe.Result{
		Target: kubernetesdescribe.Target{
			APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: "models",
			Name: "weights", UID: "claim-uid",
		},
		Family:           kubernetesdescribe.FamilyStorage,
		Storage:          &kubernetesresource.StorageResourceDetail{},
		Events:           kubernetesdescribe.Events{Items: []kubernetesdescribe.Event{}},
		Findings:         []kubernetesdescribe.Finding{},
		DegradedSections: []string{},
	}}
	router := describeTestRouter(service)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/models/storage/persistentvolumeclaims/weights/describe",
		nil,
	)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if service.claimCall.ClusterID != "00000000-0000-4000-8000-000000000003" ||
		service.claimCall.Namespace != "models" || service.claimCall.Name != "weights" {
		t.Fatalf("unexpected PVC describe input: %+v", service.claimCall)
	}
	if !strings.Contains(recorder.Body.String(), `"family":"storage"`) {
		t.Fatalf("PVC diagnosis missing from response: %s", recorder.Body.String())
	}
}

func TestDescribePodHandlerReturnsTheJoinedView(t *testing.T) {
	t.Parallel()

	exitCode := int32(1)
	service := &fakeDescribeService{result: kubernetesdescribe.Result{
		Target: kubernetesdescribe.Target{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  "model-serving",
			Name:       "inference-0",
			UID:        "6f0f6d55-0c0e-4a3f-9a2d-8c6a1f0a9d11",
		},
		Family: kubernetesdescribe.FamilyPod,
		Pod: &kubernetesresource.PodDetail{
			PodSummary: kubernetesresource.PodSummary{Name: "inference-0"},
		},
		Events: kubernetesdescribe.Events{Items: []kubernetesdescribe.Event{{
			UID:    "event-a",
			Reason: "BackOff",
		}}},
		Findings: []kubernetesdescribe.Finding{{
			Code:     kubernetesdescribe.FindingCrashLoopBackOff,
			Severity: kubernetesdescribe.SeverityWarning,
			Scope:    "server",
			ExitCode: &exitCode,
			Evidence: []kubernetesdescribe.Evidence{{
				Kind: kubernetesdescribe.EvidenceContainerState,
				Name: "server",
			}},
		}},
		DegradedSections: []string{},
	}}
	router := describeTestRouter(service)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/model-serving/pods/inference-0/describe",
		nil,
	))

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.podInput.ClusterID != "00000000-0000-4000-8000-000000000003" ||
		service.podInput.Namespace != "model-serving" ||
		service.podInput.Name != "inference-0" {
		t.Fatalf("unexpected describe input: %+v", service.podInput)
	}
	var body struct {
		Data struct {
			Family   string `json:"family"`
			Findings []struct {
				Code     string `json:"code"`
				ExitCode *int32 `json:"exit_code"`
			} `json:"findings"`
			Events struct {
				Items []struct {
					Reason string `json:"reason"`
				} `json:"items"`
			} `json:"events"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Family != kubernetesdescribe.FamilyPod ||
		len(body.Data.Findings) != 1 ||
		body.Data.Findings[0].Code != kubernetesdescribe.FindingCrashLoopBackOff ||
		body.Data.Findings[0].ExitCode == nil ||
		*body.Data.Findings[0].ExitCode != 1 ||
		len(body.Data.Events.Items) != 1 {
		t.Fatalf("unexpected body: %s", response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected cache header: %q", response.Header().Get("Cache-Control"))
	}
}

func TestDescribePodHandlerRefusesQueryParameters(t *testing.T) {
	t.Parallel()

	service := &fakeDescribeService{}
	router := describeTestRouter(service)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/model-serving/pods/inference-0/describe?limit=10",
		nil,
	))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.podInput.Name != "" {
		t.Fatal("the Cluster was asked about a request that was refused")
	}
}

func TestDescribeWorkloadHandlerCarriesTheWorkloadResource(t *testing.T) {
	t.Parallel()

	service := &fakeDescribeService{result: kubernetesdescribe.Result{
		Family: kubernetesdescribe.FamilyWorkload,
		Related: &kubernetesdescribe.Related{
			Controllers: []kubernetesdescribe.RelatedObject{},
			Pods: []kubernetesdescribe.RelatedObject{{
				Kind: "Pod",
				Name: "inference-7d9f-aaa",
				UID:  "pod-a",
				Findings: []kubernetesdescribe.Finding{{
					Code:     kubernetesdescribe.FindingImagePullFailure,
					Severity: kubernetesdescribe.SeverityWarning,
					Evidence: []kubernetesdescribe.Evidence{},
				}},
			}},
		},
		Findings:         []kubernetesdescribe.Finding{},
		DegradedSections: []string{},
	}}
	router := describeTestRouter(service)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/model-serving/workloads/deployments/inference/describe",
		nil,
	))

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.workloadCall.Resource != kubernetesresource.WorkloadDeployments ||
		service.workloadCall.Namespace != "model-serving" ||
		service.workloadCall.Name != "inference" {
		t.Fatalf("unexpected describe input: %+v", service.workloadCall)
	}
	if !strings.Contains(response.Body.String(), `"related"`) ||
		!strings.Contains(response.Body.String(), kubernetesdescribe.FindingImagePullFailure) {
		t.Fatalf("the related objects did not reach the response: %s", response.Body.String())
	}
}

// A workload type the Server does not model is refused before the Cluster is
// asked about it.
func TestDescribeWorkloadHandlerRefusesAnUnknownWorkloadResource(t *testing.T) {
	t.Parallel()

	service := &fakeDescribeService{}
	router := describeTestRouter(service)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/model-serving/workloads/replicasets/inference-7d9f/describe",
		nil,
	))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.workloadCall.Name != "" {
		t.Fatal("the Cluster was asked about a request that was refused")
	}
}

func TestDescribeResourceHandlerCarriesTheIdentityQuery(t *testing.T) {
	t.Parallel()

	service := &fakeDescribeService{result: kubernetesdescribe.Result{
		Family:           kubernetesdescribe.FamilyGeneric,
		Findings:         []kubernetesdescribe.Finding{},
		DegradedSections: []string{},
	}}
	router := describeTestRouter(service)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/kubernetes/resources/model-cache/describe"+
			"?version=v1&resource=persistentvolumeclaims&namespace=model-serving",
		nil,
	))

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.resourceCall.Resource.Version != "v1" ||
		service.resourceCall.Resource.Resource != "persistentvolumeclaims" ||
		service.resourceCall.Namespace != "model-serving" ||
		service.resourceCall.Name != "model-cache" {
		t.Fatalf("unexpected describe input: %+v", service.resourceCall)
	}
}

func TestDescribeHandlerMapsClusterErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"missing object", kubernetesresource.ErrResourceNotFound, http.StatusNotFound},
		{"agent gone", kubernetesresource.ErrAgentNotConnected, http.StatusServiceUnavailable},
		{"bad target", kubernetesresource.ErrInvalidInput, http.StatusBadRequest},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			router := describeTestRouter(&fakeDescribeService{err: testCase.err})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(
				http.MethodGet,
				"/clusters/00000000-0000-4000-8000-000000000003/namespaces/model-serving/pods/inference-0/describe",
				nil,
			))
			if response.Code != testCase.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestDescribeHandlerReportsAnUnconfiguredService(t *testing.T) {
	t.Parallel()

	router := describeTestRouter(nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/clusters/00000000-0000-4000-8000-000000000003/namespaces/model-serving/pods/inference-0/describe",
		nil,
	))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
