package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

type fakeKubernetesAuthorizationService struct {
	listInput   kubernetesresource.ListAuthorizationResourcesInput
	createInput kubernetesresource.CreateAuthorizationResourceInput
	deleteInput kubernetesresource.DeleteAuthorizationResourceInput
}

func (service *fakeKubernetesAuthorizationService) ListAuthorizationResources(_ context.Context, input kubernetesresource.ListAuthorizationResourcesInput) (kubernetesresource.AuthorizationResourcePage, error) {
	service.listInput = input
	return kubernetesresource.AuthorizationResourcePage{Resources: []kubernetesresource.AuthorizationResourceSummary{}}, nil
}
func (*fakeKubernetesAuthorizationService) GetAuthorizationResource(context.Context, string, string, kubernetesresource.AuthorizationResource, string) (kubernetesresource.AuthorizationResourceDetail, error) {
	return kubernetesresource.AuthorizationResourceDetail{}, nil
}
func (service *fakeKubernetesAuthorizationService) CreateAuthorizationResource(_ context.Context, input kubernetesresource.CreateAuthorizationResourceInput) (kubernetesresource.AuthorizationResourceDetail, error) {
	service.createInput = input
	return kubernetesresource.AuthorizationResourceDetail{AuthorizationResourceSummary: kubernetesresource.AuthorizationResourceSummary{Name: input.Name}}, nil
}
func (*fakeKubernetesAuthorizationService) UpdateAuthorizationResource(context.Context, kubernetesresource.UpdateAuthorizationResourceInput) (kubernetesresource.AuthorizationResourceDetail, error) {
	return kubernetesresource.AuthorizationResourceDetail{}, nil
}
func (service *fakeKubernetesAuthorizationService) DeleteAuthorizationResource(_ context.Context, input kubernetesresource.DeleteAuthorizationResourceInput) error {
	service.deleteInput = input
	return nil
}

func TestKubernetesAuthorizationHandlerScopesList(t *testing.T) {
	t.Parallel()
	service := &fakeKubernetesAuthorizationService{}
	response := httptest.NewRecorder()
	authorizationHandlerTestRouter(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/clusters/"+testHTTPClusterID+"/namespaces/default/authorization/roles?limit=25", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if service.listInput.ClusterID != testHTTPClusterID || service.listInput.Namespace != "default" || service.listInput.Resource != kubernetesresource.AuthorizationRoles || service.listInput.Limit != 25 {
		t.Fatalf("unexpected list input: %+v", service.listInput)
	}
}

func TestKubernetesAuthorizationHandlerCreatesWithSafetyContext(t *testing.T) {
	t.Parallel()
	service := &fakeKubernetesAuthorizationService{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/clusters/"+testHTTPClusterID+"/authorization/clusterroles", strings.NewReader(`{"name":"pod-reader","rules":[{"verbs":["get"],"api_groups":[""],"resources":["pods"],"resource_names":[],"non_resource_urls":[]}],"dry_run":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, "authorization-create-0001")
	authorizationHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if service.createInput.Resource != kubernetesresource.AuthorizationClusterRoles || service.createInput.Name != "pod-reader" || !service.createInput.DryRun || service.createInput.IdempotencyKey != "authorization-create-0001" {
		t.Fatalf("unexpected create input: %+v", service.createInput)
	}
}

func TestKubernetesAuthorizationHandlerRequiresConfirmation(t *testing.T) {
	t.Parallel()
	service := &fakeKubernetesAuthorizationService{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/clusters/"+testHTTPClusterID+"/authorization/clusterroles/pod-reader", strings.NewReader(`{"uid":"role-uid","resource_version":"8"}`))
	request.Header.Set("Content-Type", "application/json")
	authorizationHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	assertErrorCode(t, response, "confirmation_required")
	if service.deleteInput.Name != "" {
		t.Fatal("unconfirmed request reached service")
	}
}

func TestGenericResourceIdentityRejectsAuthorizationResources(t *testing.T) {
	t.Parallel()
	_, _, err := genericResourceIdentity(url.Values{
		"group": {"rbac.authorization.k8s.io"}, "version": {"v1"}, "resource": {"clusterroles"},
	})
	if err == nil {
		t.Fatal("generic resource identity accepted a dedicated authorization resource")
	}
}

func authorizationHandlerTestRouter(service kubernetesAuthorizationService) http.Handler {
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	router := gin.New()
	handler := newKubernetesAuthorizationHandler(discardLogger(), service, nil, 5*time.Second)
	cluster := "/api/v1/clusters/:cluster_id/authorization/:authorization_resource"
	namespaced := "/api/v1/clusters/:cluster_id/namespaces/:namespace_name/authorization/:authorization_resource"
	for _, base := range []string{cluster, namespaced} {
		router.GET(base, handler.list)
		router.POST(base, handler.create)
		router.GET(base+"/:authorization_name", handler.get)
		router.PUT(base+"/:authorization_name", handler.update)
		router.DELETE(base+"/:authorization_name", handler.delete)
	}
	return router
}
