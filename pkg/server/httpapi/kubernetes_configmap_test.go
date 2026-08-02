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

type fakeKubernetesConfigMapService struct {
	listInput   kubernetesresource.ListConfigMapsInput
	createInput kubernetesresource.CreateConfigMapInput
	updateInput kubernetesresource.UpdateConfigMapInput
	deleteInput kubernetesresource.DeleteConfigMapInput
	err         error
}

func (service *fakeKubernetesConfigMapService) ListConfigMaps(
	_ context.Context,
	input kubernetesresource.ListConfigMapsInput,
) (kubernetesresource.ConfigMapPage, error) {
	service.listInput = input
	return kubernetesresource.ConfigMapPage{ConfigMaps: []kubernetesresource.ConfigMapSummary{{
		Name: "app-config", Namespace: "default", DataKeys: []string{"app.yaml"},
	}}}, service.err
}

func (service *fakeKubernetesConfigMapService) GetConfigMap(
	context.Context,
	string,
	string,
	string,
) (kubernetesresource.ConfigMapDetail, error) {
	return kubernetesresource.ConfigMapDetail{}, service.err
}

func (service *fakeKubernetesConfigMapService) CreateConfigMap(
	_ context.Context,
	input kubernetesresource.CreateConfigMapInput,
) (kubernetesresource.ConfigMapDetail, error) {
	service.createInput = input
	return kubernetesresource.ConfigMapDetail{ConfigMapSummary: kubernetesresource.ConfigMapSummary{Name: input.Name}}, service.err
}

func (service *fakeKubernetesConfigMapService) UpdateConfigMap(
	_ context.Context,
	input kubernetesresource.UpdateConfigMapInput,
) (kubernetesresource.ConfigMapDetail, error) {
	service.updateInput = input
	return kubernetesresource.ConfigMapDetail{ConfigMapSummary: kubernetesresource.ConfigMapSummary{Name: input.Name}}, service.err
}

func (service *fakeKubernetesConfigMapService) DeleteConfigMap(
	_ context.Context,
	input kubernetesresource.DeleteConfigMapInput,
) error {
	service.deleteInput = input
	return service.err
}

func TestKubernetesConfigMapHandlerListsSummaries(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesConfigMapService{}
	response := httptest.NewRecorder()
	configMapHandlerTestRouter(service).ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/configmaps?limit=25&label_selector=app%3Dapi",
		nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if service.listInput.ClusterID != testHTTPClusterID || service.listInput.Namespace != "default" ||
		service.listInput.Limit != 25 || service.listInput.LabelSelector != "app=api" {
		t.Fatalf("unexpected list input: %+v", service.listInput)
	}
}

func TestKubernetesConfigMapHandlerCreatesWithSafetyContext(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesConfigMapService{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/configmaps",
		strings.NewReader(`{"name":"app-config","data":{"app.yaml":"enabled: true\n"},"binary_data":{"logo.bin":"AAEC"},"confirm":true}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, "config-map-create-0001")
	configMapHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if service.createInput.Name != "app-config" || service.createInput.Data["app.yaml"] == "" ||
		service.createInput.BinaryData["logo.bin"] != "AAEC" || !service.createInput.Confirm ||
		service.createInput.IdempotencyKey != "config-map-create-0001" {
		t.Fatalf("unexpected create input: %+v", service.createInput)
	}
}

func TestKubernetesConfigMapHandlerRequiresConfirmation(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesConfigMapService{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/configmaps/app-config",
		strings.NewReader(`{"uid":"config-uid","resource_version":"7","data":{},"binary_data":{}}`),
	)
	request.Header.Set("Content-Type", "application/json")
	configMapHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	assertErrorCode(t, response, "confirmation_required")
	if service.updateInput.Name != "" {
		t.Fatal("unconfirmed request reached service")
	}
}

func TestKubernetesConfigMapHandlerMapsImmutableConflict(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesConfigMapService{err: kubernetesresource.ErrConfigMapImmutable}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/configmaps/app-config",
		strings.NewReader(`{"uid":"config-uid","resource_version":"7","data":{},"binary_data":{},"confirm":true}`),
	)
	request.Header.Set("Content-Type", "application/json")
	configMapHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	assertErrorCode(t, response, "config_map_immutable")
}

func configMapHandlerTestRouter(service kubernetesConfigMapService) http.Handler {
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	router := gin.New()
	handler := newKubernetesConfigMapHandler(discardLogger(), service, nil, 5*time.Second)
	base := "/api/v1/clusters/:cluster_id/namespaces/:namespace_name/configmaps"
	router.GET(base, handler.list)
	router.POST(base, handler.create)
	router.GET(base+"/:config_map_name", handler.get)
	router.PUT(base+"/:config_map_name", handler.update)
	router.DELETE(base+"/:config_map_name", handler.delete)
	return router
}
