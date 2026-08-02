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

type fakeKubernetesHPAService struct {
	listInput   kubernetesresource.ListHorizontalPodAutoscalersInput
	createInput kubernetesresource.CreateHorizontalPodAutoscalerInput
	updateInput kubernetesresource.UpdateHorizontalPodAutoscalerInput
	deleteInput kubernetesresource.DeleteHorizontalPodAutoscalerInput
	err         error
}

func (service *fakeKubernetesHPAService) ListHorizontalPodAutoscalers(
	_ context.Context,
	input kubernetesresource.ListHorizontalPodAutoscalersInput,
) (kubernetesresource.HorizontalPodAutoscalerPage, error) {
	service.listInput = input
	return kubernetesresource.HorizontalPodAutoscalerPage{Autoscalers: []kubernetesresource.HorizontalPodAutoscalerSummary{{Name: "api", Namespace: input.Namespace}}}, service.err
}

func (service *fakeKubernetesHPAService) GetHorizontalPodAutoscaler(context.Context, string, string, string) (kubernetesresource.HorizontalPodAutoscalerDetail, error) {
	return kubernetesresource.HorizontalPodAutoscalerDetail{}, service.err
}

func (service *fakeKubernetesHPAService) CreateHorizontalPodAutoscaler(
	_ context.Context,
	input kubernetesresource.CreateHorizontalPodAutoscalerInput,
) (kubernetesresource.HorizontalPodAutoscalerDetail, error) {
	service.createInput = input
	return kubernetesresource.HorizontalPodAutoscalerDetail{HorizontalPodAutoscalerSummary: kubernetesresource.HorizontalPodAutoscalerSummary{Name: input.Name}}, service.err
}

func (service *fakeKubernetesHPAService) UpdateHorizontalPodAutoscaler(
	_ context.Context,
	input kubernetesresource.UpdateHorizontalPodAutoscalerInput,
) (kubernetesresource.HorizontalPodAutoscalerDetail, error) {
	service.updateInput = input
	return kubernetesresource.HorizontalPodAutoscalerDetail{HorizontalPodAutoscalerSummary: kubernetesresource.HorizontalPodAutoscalerSummary{Name: input.Name}}, service.err
}

func (service *fakeKubernetesHPAService) DeleteHorizontalPodAutoscaler(
	_ context.Context,
	input kubernetesresource.DeleteHorizontalPodAutoscalerInput,
) error {
	service.deleteInput = input
	return service.err
}

func TestKubernetesHPAHandlerListsAutoscalers(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesHPAService{}
	response := httptest.NewRecorder()
	hpaHandlerTestRouter(service).ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/autoscaling/horizontalpodautoscalers?limit=25&label_selector=app%3Dapi",
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

func TestKubernetesHPAHandlerCreatesWithSafetyContext(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesHPAService{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/autoscaling/horizontalpodautoscalers",
		strings.NewReader(`{"name":"api","spec":{"target":{"api_version":"apps/v1","kind":"Deployment","name":"api"},"max_replicas":10,"metrics":[]},"dry_run":true}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, "hpa-create-0001")
	hpaHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if service.createInput.Name != "api" || service.createInput.Spec.Target.Kind != "Deployment" ||
		service.createInput.Spec.MaxReplicas != 10 || !service.createInput.DryRun ||
		service.createInput.IdempotencyKey != "hpa-create-0001" {
		t.Fatalf("unexpected create input: %+v", service.createInput)
	}
}

func TestKubernetesHPAHandlerRequiresConfirmation(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesHPAService{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodDelete,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/autoscaling/horizontalpodautoscalers/api",
		strings.NewReader(`{"uid":"hpa-uid","resource_version":"8"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	hpaHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	assertErrorCode(t, response, "confirmation_required")
	if service.deleteInput.Name != "" {
		t.Fatal("unconfirmed request reached service")
	}
}

func hpaHandlerTestRouter(service kubernetesHorizontalPodAutoscalerService) http.Handler {
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	router := gin.New()
	handler := newKubernetesHorizontalPodAutoscalerHandler(discardLogger(), service, nil, 5*time.Second)
	base := "/api/v1/clusters/:cluster_id/namespaces/:namespace_name/autoscaling/horizontalpodautoscalers"
	router.GET(base, handler.list)
	router.POST(base, handler.create)
	router.GET(base+"/:hpa_name", handler.get)
	router.PUT(base+"/:hpa_name", handler.update)
	router.DELETE(base+"/:hpa_name", handler.delete)
	return router
}
