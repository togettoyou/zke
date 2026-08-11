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

type fakeKubernetesSecretService struct {
	err error
}

func (service *fakeKubernetesSecretService) ListSecrets(
	context.Context,
	kubernetesresource.ListSecretsInput,
) (kubernetesresource.SecretPage, error) {
	return kubernetesresource.SecretPage{}, service.err
}

func (service *fakeKubernetesSecretService) GetSecret(
	context.Context,
	string,
	string,
	string,
) (kubernetesresource.SecretDetail, error) {
	return kubernetesresource.SecretDetail{}, service.err
}

func (service *fakeKubernetesSecretService) CreateSecret(
	context.Context,
	kubernetesresource.CreateSecretInput,
) (kubernetesresource.SecretDetail, error) {
	return kubernetesresource.SecretDetail{}, service.err
}

func (service *fakeKubernetesSecretService) UpdateSecret(
	context.Context,
	kubernetesresource.UpdateSecretInput,
) (kubernetesresource.SecretDetail, error) {
	return kubernetesresource.SecretDetail{}, service.err
}

func (service *fakeKubernetesSecretService) DeleteSecret(
	context.Context,
	kubernetesresource.DeleteSecretInput,
) error {
	return service.err
}

// Refusals that are ZKE's own, reported as such.
//
// Both of these were answered with a 5xx before: one as a Kubernetes API
// failure, the other as an unmapped internal error. That status is wrong twice
// over — the request was understood and will never be served, and a 5xx is what
// the Console retries. An operator was shown three attempts, a "加载失败" and a
// message blaming the Agent's Kubernetes permissions for a rule no permission
// reaches.
func TestKubernetesSecretHandlerReportsPlatformRefusalsAsForbidden(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		err    error
		method string
		path   string
		code   string
	}{
		{
			name:   "listing the Agent's own Namespace",
			err:    kubernetesresource.ErrAgentNamespaceForbidden,
			method: http.MethodGet,
			path:   "/namespaces/zke-system/secrets",
			code:   "agent_namespace_forbidden",
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			response := httptest.NewRecorder()
			secretHandlerTestRouter(&fakeKubernetesSecretService{err: testCase.err}).ServeHTTP(
				response,
				httptest.NewRequest(
					testCase.method,
					"/api/v1/clusters/"+testHTTPClusterID+testCase.path,
					nil,
				),
			)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d: %s", response.Code, response.Body)
			}
			assertErrorCode(t, response, testCase.code)
		})
	}
}

func secretHandlerTestRouter(service kubernetesSecretService) http.Handler {
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	router := gin.New()
	handler := newKubernetesSecretHandler(discardLogger(), service, nil, 5*time.Second)
	base := "/api/v1/clusters/:cluster_id/namespaces/:namespace_name/secrets"
	router.GET(base, handler.list)
	router.POST(base, handler.create)
	router.GET(base+"/:secret_name", handler.get)
	router.PUT(base+"/:secret_name", handler.update)
	router.DELETE(base+"/:secret_name", handler.delete)
	return router
}
