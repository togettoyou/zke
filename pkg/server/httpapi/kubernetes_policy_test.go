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

type fakeKubernetesPolicyService struct {
	listInput   kubernetesresource.ListPolicyResourcesInput
	createInput kubernetesresource.CreatePolicyResourceInput
	updateInput kubernetesresource.UpdatePolicyResourceInput
	deleteInput kubernetesresource.DeletePolicyResourceInput
	err         error
}

func (service *fakeKubernetesPolicyService) ListPolicyResources(
	_ context.Context,
	input kubernetesresource.ListPolicyResourcesInput,
) (kubernetesresource.PolicyResourcePage, error) {
	service.listInput = input
	return kubernetesresource.PolicyResourcePage{
		Resources: []kubernetesresource.PolicyResourceSummary{{Name: "compute", Namespace: input.Namespace}},
	}, service.err
}

func (service *fakeKubernetesPolicyService) GetPolicyResource(
	context.Context, string, string, kubernetesresource.PolicyResource, string,
) (kubernetesresource.PolicyResourceDetail, error) {
	return kubernetesresource.PolicyResourceDetail{}, service.err
}

func (service *fakeKubernetesPolicyService) CreatePolicyResource(
	_ context.Context,
	input kubernetesresource.CreatePolicyResourceInput,
) (kubernetesresource.PolicyResourceDetail, error) {
	service.createInput = input
	return kubernetesresource.PolicyResourceDetail{
		PolicyResourceSummary: kubernetesresource.PolicyResourceSummary{Name: input.Name},
	}, service.err
}

func (service *fakeKubernetesPolicyService) UpdatePolicyResource(
	_ context.Context,
	input kubernetesresource.UpdatePolicyResourceInput,
) (kubernetesresource.PolicyResourceDetail, error) {
	service.updateInput = input
	return kubernetesresource.PolicyResourceDetail{
		PolicyResourceSummary: kubernetesresource.PolicyResourceSummary{Name: input.Name},
	}, service.err
}

func (service *fakeKubernetesPolicyService) DeletePolicyResource(
	_ context.Context,
	input kubernetesresource.DeletePolicyResourceInput,
) error {
	service.deleteInput = input
	return service.err
}

func TestKubernetesPolicyHandlerListsNamespacedPolicies(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesPolicyService{}
	response := httptest.NewRecorder()
	policyHandlerTestRouter(service).ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/team-a/policies/resourcequotas?limit=25&label_selector=team%3Da",
		nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if service.listInput.ClusterID != testHTTPClusterID || service.listInput.Namespace != "team-a" ||
		service.listInput.Resource != kubernetesresource.PolicyResourceQuotas ||
		service.listInput.Limit != 25 || service.listInput.LabelSelector != "team=a" {
		t.Fatalf("unexpected list input: %+v", service.listInput)
	}
}

// PriorityClass has no Namespace and the other four have nothing but one, so
// each route family serves exactly the kinds whose scope it can express.
func TestKubernetesPolicyHandlerRejectsWrongResourceScope(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"cluster kind on the namespaced route": "/api/v1/clusters/" + testHTTPClusterID + "/namespaces/team-a/policies/priorityclasses",
		"namespaced kind on the cluster route": "/api/v1/clusters/" + testHTTPClusterID + "/policies/networkpolicies",
		"unknown kind":                         "/api/v1/clusters/" + testHTTPClusterID + "/policies/podsecuritypolicies",
	}
	for name, path := range cases {
		service := &fakeKubernetesPolicyService{}
		response := httptest.NewRecorder()
		policyHandlerTestRouter(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d: %s", name, response.Code, response.Body)
		}
		if service.listInput.ClusterID != "" {
			t.Fatalf("%s: invalid scope reached service", name)
		}
	}
}

func TestKubernetesPolicyHandlerCreatesWithSafetyContext(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesPolicyService{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/team-a/policies/resourcequotas",
		strings.NewReader(`{"name":"compute","resource_quota":{"hard":{"requests.cpu":"10"},"scopes":["NotBestEffort"]},"dry_run":true}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, "policy-create-0001")
	policyHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if service.createInput.Name != "compute" || service.createInput.Namespace != "team-a" ||
		service.createInput.Spec.ResourceQuota == nil ||
		service.createInput.Spec.ResourceQuota.Hard["requests.cpu"] != "10" ||
		!service.createInput.DryRun || service.createInput.IdempotencyKey != "policy-create-0001" {
		t.Fatalf("unexpected create input: %+v", service.createInput)
	}
}

func TestKubernetesPolicyHandlerUpdatesClusterScopedPriorityClass(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesPolicyService{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/clusters/"+testHTTPClusterID+"/policies/priorityclasses/training-high",
		strings.NewReader(`{"uid":"class-uid","resource_version":"3","priority_class":{"description":"训练","global_default":false},"confirm":true}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(idempotencyKeyHeaderName, "policy-update-0001")
	policyHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if service.updateInput.Name != "training-high" || service.updateInput.Namespace != "" ||
		service.updateInput.UID != "class-uid" || service.updateInput.ResourceVersion != "3" ||
		service.updateInput.Spec.PriorityClass == nil || service.updateInput.Spec.PriorityClass.GlobalDefault == nil ||
		*service.updateInput.Spec.PriorityClass.GlobalDefault {
		t.Fatalf("unexpected update input: %+v", service.updateInput)
	}
}

func TestKubernetesPolicyHandlerRequiresConfirmation(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesPolicyService{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodDelete,
		"/api/v1/clusters/"+testHTTPClusterID+"/namespaces/team-a/policies/networkpolicies/api",
		strings.NewReader(`{"uid":"policy-uid","resource_version":"8"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	policyHandlerTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	assertErrorCode(t, response, "confirmation_required")
	if service.deleteInput.Name != "" {
		t.Fatal("unconfirmed request reached service")
	}
}

func policyHandlerTestRouter(service kubernetesPolicyService) http.Handler {
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	router := gin.New()
	handler := newKubernetesPolicyHandler(discardLogger(), service, nil, 5*time.Second)
	clusterBase := "/api/v1/clusters/:cluster_id/policies/:policy_resource"
	namespacedBase := "/api/v1/clusters/:cluster_id/namespaces/:namespace_name/policies/:policy_resource"
	for _, base := range []string{clusterBase, namespacedBase} {
		router.GET(base, handler.list)
		router.POST(base, handler.create)
		router.GET(base+"/:policy_name", handler.get)
		router.PUT(base+"/:policy_name", handler.update)
		router.DELETE(base+"/:policy_name", handler.delete)
	}
	return router
}
