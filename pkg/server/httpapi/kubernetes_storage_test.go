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

type fakeKubernetesStorageService struct {
	listInput   kubernetesresource.ListStorageResourcesInput
	createInput kubernetesresource.CreateStorageResourceInput
	updateInput kubernetesresource.UpdateStorageResourceInput
	deleteInput kubernetesresource.DeleteStorageResourceInput
	err         error
}

func (service *fakeKubernetesStorageService) ListStorageResources(
	_ context.Context,
	input kubernetesresource.ListStorageResourcesInput,
) (kubernetesresource.StorageResourcePage, error) {
	service.listInput = input
	return kubernetesresource.StorageResourcePage{Resources: []kubernetesresource.StorageResourceSummary{{
		Resource: input.Resource, Name: "data", Namespace: input.Namespace,
	}}}, service.err
}

func (service *fakeKubernetesStorageService) GetStorageResource(
	context.Context,
	string,
	string,
	kubernetesresource.StorageResource,
	string,
) (kubernetesresource.StorageResourceDetail, error) {
	return kubernetesresource.StorageResourceDetail{}, service.err
}

func (service *fakeKubernetesStorageService) CreateStorageResource(
	_ context.Context,
	input kubernetesresource.CreateStorageResourceInput,
) (kubernetesresource.StorageResourceDetail, error) {
	service.createInput = input
	return kubernetesresource.StorageResourceDetail{StorageResourceSummary: kubernetesresource.StorageResourceSummary{Name: input.Name}}, service.err
}

func (service *fakeKubernetesStorageService) UpdateStorageResource(
	_ context.Context,
	input kubernetesresource.UpdateStorageResourceInput,
) (kubernetesresource.StorageResourceDetail, error) {
	service.updateInput = input
	return kubernetesresource.StorageResourceDetail{StorageResourceSummary: kubernetesresource.StorageResourceSummary{Name: input.Name}}, service.err
}

func (service *fakeKubernetesStorageService) DeleteStorageResource(
	_ context.Context,
	input kubernetesresource.DeleteStorageResourceInput,
) error {
	service.deleteInput = input
	return service.err
}

func TestKubernetesStorageHandlerListsClusterAndNamespacedResources(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesStorageService{}
	router := storageHandlerTestRouter(service)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/clusters/"+testHTTPClusterID+"/storage/persistentvolumes?limit=25&label_selector=app%3Ddatabase",
		nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("PV status = %d: %s", response.Code, response.Body)
	}
	if service.listInput.ClusterID != testHTTPClusterID || service.listInput.Namespace != "" ||
		service.listInput.Resource != kubernetesresource.StoragePersistentVolumes || service.listInput.Limit != 25 ||
		service.listInput.LabelSelector != "app=database" {
		t.Fatalf("unexpected PV list input: %+v", service.listInput)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/storage/persistentvolumeclaims",
		nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("PVC status = %d: %s", response.Code, response.Body)
	}
	if service.listInput.Namespace != "default" || service.listInput.Resource != kubernetesresource.StoragePersistentVolumeClaims {
		t.Fatalf("unexpected PVC list input: %+v", service.listInput)
	}
}

func TestKubernetesStorageHandlerRejectsWrongResourceScope(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesStorageService{}
	router := storageHandlerTestRouter(service)
	for _, target := range []string{
		"/api/v1/clusters/" + testHTTPClusterID + "/storage/persistentvolumeclaims",
		"/api/v1/clusters/" + testHTTPClusterID + "/namespaces/default/storage/storageclasses",
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("target %s status = %d: %s", target, response.Code, response.Body)
		}
		assertErrorCode(t, response, "invalid_request")
	}
	if service.listInput.ClusterID != "" {
		t.Fatal("invalid scope reached service")
	}
}

func TestKubernetesStorageHandlerCreatesWithSafetyContext(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesStorageService{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/storage/persistentvolumeclaims",
		strings.NewReader(`{"name":"data","persistent_volume_claim":{"requested_capacity":"10Gi","access_modes":["ReadWriteOnce"]},"dry_run":true}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, "storage-create-0001")
	storageHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if service.createInput.Resource != kubernetesresource.StoragePersistentVolumeClaims ||
		service.createInput.PersistentVolumeClaim == nil ||
		service.createInput.PersistentVolumeClaim.RequestedCapacity != "10Gi" || !service.createInput.DryRun ||
		service.createInput.IdempotencyKey != "storage-create-0001" {
		t.Fatalf("unexpected create input: %+v", service.createInput)
	}
}

func TestKubernetesStorageHandlerRequiresConfirmation(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesStorageService{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/clusters/"+testHTTPClusterID+"/storage/storageclasses",
		strings.NewReader(`{"name":"fast","storage_class":{"provisioner":"csi.example.com"}}`),
	)
	request.Header.Set("Content-Type", "application/json")
	storageHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	assertErrorCode(t, response, "confirmation_required")
	if service.createInput.Name != "" {
		t.Fatal("unconfirmed request reached service")
	}
}

func storageHandlerTestRouter(service kubernetesStorageService) http.Handler {
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	router := gin.New()
	handler := newKubernetesStorageHandler(discardLogger(), service, nil, 5*time.Second)
	clusterBase := "/api/v1/clusters/:cluster_id/storage/:storage_resource"
	namespaceBase := "/api/v1/clusters/:cluster_id/namespaces/:namespace_name/storage/:storage_resource"
	for _, base := range []string{clusterBase, namespaceBase} {
		router.GET(base, handler.list)
		router.POST(base, handler.create)
		router.GET(base+"/:storage_name", handler.get)
		router.PUT(base+"/:storage_name", handler.update)
		router.DELETE(base+"/:storage_name", handler.delete)
	}
	return router
}
