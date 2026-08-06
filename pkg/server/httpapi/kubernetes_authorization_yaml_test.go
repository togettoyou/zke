package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/kubernetesyaml"
)

/*
 * The family and its scope come from the path, and both are resolved before the
 * request is accepted.
 */
func TestKubernetesAuthorizationYAMLHandlerResolvesTheTargetFromThePath(t *testing.T) {
	t.Parallel()

	const manifest = "apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata:\n  name: pod-reader\n"
	service := &fakeKubernetesYAMLService{
		get: func(_ context.Context, input kubernetesyaml.GetInput) (kubernetesyaml.Result, error) {
			if input.Resource.Group != "rbac.authorization.k8s.io" ||
				input.Resource.Resource != "roles" ||
				input.Namespace != "team-a" || input.Name != "pod-reader" {
				t.Fatalf("unexpected get input: %+v", input)
			}
			return kubernetesyaml.Result{Manifest: []byte(manifest), UID: "uid-1", ResourceVersion: "42"}, nil
		},
		update: func(_ context.Context, input kubernetesyaml.UpdateInput) (kubernetesyaml.Result, error) {
			if !input.Confirm || input.DryRun || string(input.Manifest) != manifest {
				t.Fatalf("unexpected update input: %+v", input)
			}
			return kubernetesyaml.Result{Manifest: input.Manifest, UID: "uid-1", ResourceVersion: "43"}, nil
		},
	}
	router := authorizationYAMLHandlerTestRouter(service)
	base := "/api/v1/clusters/" + testHTTPClusterID +
		"/namespaces/team-a/authorization/roles/pod-reader/yaml"

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base, nil))
	if response.Code != http.StatusOK || response.Body.String() != manifest {
		t.Fatalf("GET response = %d: %s", response.Code, response.Body)
	}

	updated := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, base+"?confirm=true", bytes.NewBufferString(manifest))
	request.Header.Set("Content-Type", "application/yaml")
	request.Header.Set(idempotencyKeyHeaderName, "authorization-yaml-0001")
	router.ServeHTTP(updated, request)
	if updated.Code != http.StatusOK {
		t.Fatalf("PUT response = %d: %s", updated.Code, updated.Body)
	}
}

/*
 * A scope the family does not have names no object.
 *
 * A ClusterRole asked for inside a Namespace, or a Role asked for without one,
 * would otherwise be resolved against whichever the URL happened to carry — and
 * an authorization object read or written in the wrong scope is not a near
 * miss, it is a different grant.
 */
func TestKubernetesAuthorizationYAMLHandlerRefusesAMismatchedScope(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesYAMLService{
		get: func(context.Context, kubernetesyaml.GetInput) (kubernetesyaml.Result, error) {
			t.Fatal("a mismatched scope reached the service")
			return kubernetesyaml.Result{}, nil
		},
	}
	router := authorizationYAMLHandlerTestRouter(service)
	for _, path := range []string{
		"/namespaces/team-a/authorization/clusterroles/editor/yaml",
		"/authorization/roles/pod-reader/yaml",
		"/namespaces/team-a/authorization/deployments/api/yaml",
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(
			http.MethodGet,
			"/api/v1/clusters/"+testHTTPClusterID+path,
			nil,
		))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d: %s", path, response.Code, response.Body)
		}
	}
}

func authorizationYAMLHandlerTestRouter(service kubernetesYAMLService) http.Handler {
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	router := gin.New()
	handler := newKubernetesAuthorizationYAMLHandler(discardLogger(), service, nil, 5*time.Second)
	cluster := "/api/v1/clusters/:cluster_id/authorization/:authorization_resource/:authorization_name/yaml"
	namespaced := "/api/v1/clusters/:cluster_id/namespaces/:namespace_name/authorization/:authorization_resource/:authorization_name/yaml"
	router.GET(cluster, handler.get)
	router.PUT(cluster, handler.update)
	router.GET(namespaced, handler.get)
	router.PUT(namespaced, handler.update)
	return router
}
