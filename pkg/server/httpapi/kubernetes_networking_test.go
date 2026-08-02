package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

type fakeKubernetesNetworkingService struct {
	listInput   kubernetesresource.ListNetworkingResourcesInput
	createInput kubernetesresource.CreateNetworkingResourceInput
	updateInput kubernetesresource.UpdateNetworkingResourceInput
	deleteInput kubernetesresource.DeleteNetworkingResourceInput
	err         error
}

func (service *fakeKubernetesNetworkingService) ListNetworkingResources(
	_ context.Context,
	input kubernetesresource.ListNetworkingResourcesInput,
) (kubernetesresource.NetworkingResourcePage, error) {
	service.listInput = input
	return kubernetesresource.NetworkingResourcePage{Resources: []kubernetesresource.NetworkingResourceSummary{{
		Resource: kubernetesresource.NetworkingServices, Name: "api", Namespace: "default",
	}}}, service.err
}

func (service *fakeKubernetesNetworkingService) GetNetworkingResource(
	context.Context,
	string,
	string,
	kubernetesresource.NetworkingResource,
	string,
) (kubernetesresource.NetworkingResourceDetail, error) {
	return kubernetesresource.NetworkingResourceDetail{}, service.err
}

func (service *fakeKubernetesNetworkingService) CreateNetworkingResource(
	_ context.Context,
	input kubernetesresource.CreateNetworkingResourceInput,
) (kubernetesresource.NetworkingResourceDetail, error) {
	service.createInput = input
	return kubernetesresource.NetworkingResourceDetail{NetworkingResourceSummary: kubernetesresource.NetworkingResourceSummary{Name: input.Name}}, service.err
}

func (service *fakeKubernetesNetworkingService) UpdateNetworkingResource(
	_ context.Context,
	input kubernetesresource.UpdateNetworkingResourceInput,
) (kubernetesresource.NetworkingResourceDetail, error) {
	service.updateInput = input
	return kubernetesresource.NetworkingResourceDetail{NetworkingResourceSummary: kubernetesresource.NetworkingResourceSummary{Name: input.Name}}, service.err
}

func (service *fakeKubernetesNetworkingService) DeleteNetworkingResource(
	_ context.Context,
	input kubernetesresource.DeleteNetworkingResourceInput,
) error {
	service.deleteInput = input
	return service.err
}

func TestKubernetesNetworkingHandlerListsTypedResources(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesNetworkingService{}
	response := httptest.NewRecorder()
	networkingHandlerTestRouter(service).ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/networking/services?limit=25&label_selector=app%3Dapi",
		nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if service.listInput.ClusterID != testHTTPClusterID || service.listInput.Namespace != "default" ||
		service.listInput.Resource != kubernetesresource.NetworkingServices || service.listInput.Limit != 25 ||
		service.listInput.LabelSelector != "app=api" {
		t.Fatalf("unexpected list input: %+v", service.listInput)
	}
	var page kubernetesresource.NetworkingResourcePage
	if err := decodeSuccessResponse(response, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Resources) != 1 || page.Resources[0].Name != "api" {
		t.Fatalf("unexpected page: %+v", page)
	}
}

func TestKubernetesNetworkingHandlerCreatesWithSafetyContext(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesNetworkingService{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/networking/services",
		strings.NewReader(`{"name":"api","service":{"type":"ClusterIP","ports":[{"name":"http","port":80,"target_port":"8080"}]},"confirm":true}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, "network-create-0001")
	networkingHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if service.createInput.Name != "api" || service.createInput.Service == nil ||
		service.createInput.Service.Ports[0].TargetPort != "8080" || !service.createInput.Confirm ||
		service.createInput.IdempotencyKey != "network-create-0001" {
		t.Fatalf("unexpected create input: %+v", service.createInput)
	}
}

func TestKubernetesNetworkingHandlerRequiresConfirmation(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesNetworkingService{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/networking/services",
		strings.NewReader(`{"name":"api","service":{"type":"ClusterIP","ports":[{"port":80}]}}`),
	)
	request.Header.Set("Content-Type", "application/json")
	networkingHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	assertErrorCode(t, response, "confirmation_required")
	if service.createInput.Name != "" {
		t.Fatal("unconfirmed request reached service")
	}
}

func TestKubernetesNetworkingHandlerMapsMissingGatewayAPI(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesNetworkingService{err: kubernetesresource.ErrGatewayAPIUnavailable}
	response := httptest.NewRecorder()
	networkingHandlerTestRouter(service).ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/networking/gateways",
		nil,
	))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	assertErrorCode(t, response, "gateway_api_unavailable")
}

func networkingHandlerTestRouter(service kubernetesNetworkingService) http.Handler {
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	router := gin.New()
	handler := newKubernetesNetworkingHandler(discardLogger(), service, nil, 5*time.Second)
	base := "/api/v1/clusters/:cluster_id/namespaces/:namespace_name/networking/:network_resource"
	router.GET(base, handler.list)
	router.POST(base, handler.create)
	router.GET(base+"/:network_name", handler.get)
	router.PUT(base+"/:network_name", handler.update)
	router.DELETE(base+"/:network_name", handler.delete)
	return router
}
