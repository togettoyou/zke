package httpapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/kubernetesyaml"
)

func TestKubernetesYAMLHandlers(t *testing.T) {
	t.Parallel()
	const manifest = "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n"
	service := &fakeKubernetesYAMLService{
		get: func(_ context.Context, input kubernetesyaml.GetInput) (kubernetesyaml.Result, error) {
			if input.ClusterID != testHTTPClusterID || input.Resource.Group != "apps" ||
				input.Resource.Version != "v1" || input.Resource.Resource != "deployments" ||
				input.Namespace != "team-a" || input.Name != "api" {
				t.Fatalf("unexpected get input: %+v", input)
			}
			return kubernetesyaml.Result{
				Manifest: []byte(manifest), UID: "uid-1", ResourceVersion: "42",
			}, nil
		},
		update: func(_ context.Context, input kubernetesyaml.UpdateInput) (kubernetesyaml.Result, error) {
			if !input.DryRun || input.Confirm || input.FieldManager != "zke-yaml" ||
				input.IdempotencyKey != "0123456789abcdef" ||
				string(input.Manifest) != manifest {
				t.Fatalf("unexpected update input: %+v", input)
			}
			return kubernetesyaml.Result{
				Manifest: input.Manifest, UID: "uid-1", ResourceVersion: "42",
			}, nil
		},
	}
	router := kubernetesYAMLHandlerTestRouter(service)
	baseURL := "/api/v1/clusters/" + testHTTPClusterID +
		"/kubernetes/resources/api/yaml?group=apps&version=v1" +
		"&resource=deployments&namespace=team-a"

	getResponse := httptest.NewRecorder()
	router.ServeHTTP(getResponse, httptest.NewRequest(http.MethodGet, baseURL, nil))
	if getResponse.Code != http.StatusOK || getResponse.Body.String() != manifest {
		t.Fatalf("GET response = %d: %s", getResponse.Code, getResponse.Body)
	}
	if contentType := getResponse.Header().Get("Content-Type"); contentType != "application/yaml; charset=utf-8" {
		t.Fatalf("GET Content-Type = %q", contentType)
	}
	if getResponse.Header().Get("Cache-Control") != "no-store" ||
		getResponse.Header().Get("X-Content-Type-Options") != "nosniff" ||
		getResponse.Header().Get("X-ZKE-Resource-UID") != "uid-1" {
		t.Fatalf("unexpected GET headers: %v", getResponse.Header())
	}

	updateResponse := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPut,
		baseURL+"&dry_run=true&field_manager=zke-yaml",
		bytes.NewBufferString(manifest),
	)
	request.Header.Set("Content-Type", "application/yaml; charset=utf-8")
	request.Header.Set(idempotencyKeyHeaderName, "0123456789abcdef")
	router.ServeHTTP(updateResponse, request)
	if updateResponse.Code != http.StatusOK || updateResponse.Body.String() != manifest {
		t.Fatalf("PUT response = %d: %s", updateResponse.Code, updateResponse.Body)
	}
	if updateResponse.Header().Get("X-ZKE-Dry-Run") != "true" {
		t.Fatalf("PUT dry-run header = %q", updateResponse.Header().Get("X-ZKE-Dry-Run"))
	}
}

func TestKubernetesYAMLUpdateRequestValidation(t *testing.T) {
	t.Parallel()
	service := &fakeKubernetesYAMLService{
		update: func(context.Context, kubernetesyaml.UpdateInput) (kubernetesyaml.Result, error) {
			t.Fatal("Update() called for an invalid HTTP request")
			return kubernetesyaml.Result{}, nil
		},
	}
	router := kubernetesYAMLHandlerTestRouter(service)
	baseURL := "/api/v1/clusters/" + testHTTPClusterID +
		"/kubernetes/resources/api/yaml?group=apps&version=v1&resource=deployments"
	testCases := []struct {
		name        string
		url         string
		contentType string
		body        []byte
		status      int
		code        string
	}{
		{
			name: "confirmation required", url: baseURL,
			contentType: "application/yaml", body: []byte("kind: Deployment\n"),
			status: http.StatusBadRequest, code: "confirmation_required",
		},
		{
			name: "invalid boolean", url: baseURL + "&dry_run=1",
			contentType: "application/yaml", body: []byte("kind: Deployment\n"),
			status: http.StatusBadRequest, code: "invalid_request",
		},
		{
			name: "media type", url: baseURL + "&dry_run=true",
			contentType: "application/json", body: []byte("{}"),
			status: http.StatusUnsupportedMediaType, code: "unsupported_media_type",
		},
		{
			name: "too large", url: baseURL + "&dry_run=true",
			contentType: "application/yaml",
			body:        bytes.Repeat([]byte("x"), maxKubernetesYAMLBytes+1),
			status:      http.StatusRequestEntityTooLarge, code: "manifest_too_large",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPut,
				testCase.url,
				bytes.NewReader(testCase.body),
			)
			request.Header.Set("Content-Type", testCase.contentType)
			router.ServeHTTP(response, request)
			if response.Code != testCase.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, testCase.status, response.Body)
			}
			assertErrorCode(t, response, testCase.code)
		})
	}
}

func TestKubernetesYAMLConflictMapping(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		err  error
		code string
	}{
		{err: kubernetesyaml.ErrResourceUIDChanged, code: "resource_uid_changed"},
		{err: kubernetesyaml.ErrResourceVersionChanged, code: "resource_version_changed"},
		{err: kubernetesyaml.ErrInvalidManifest, code: "invalid_yaml"},
	}
	for _, testCase := range testCases {
		service := &fakeKubernetesYAMLService{
			update: func(context.Context, kubernetesyaml.UpdateInput) (kubernetesyaml.Result, error) {
				return kubernetesyaml.Result{}, testCase.err
			},
		}
		router := kubernetesYAMLHandlerTestRouter(service)
		response := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodPut,
			"/api/v1/clusters/"+testHTTPClusterID+
				"/kubernetes/resources/api/yaml?version=v1&resource=pods&dry_run=true",
			strings.NewReader("apiVersion: v1\nkind: Pod\n"),
		)
		request.Header.Set("Content-Type", "application/yaml")
		router.ServeHTTP(response, request)
		if testCase.code == "invalid_yaml" && response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d: %s", response.Code, response.Body)
		}
		if testCase.code != "invalid_yaml" && response.Code != http.StatusConflict {
			t.Fatalf("status = %d: %s", response.Code, response.Body)
		}
		assertErrorCode(t, response, testCase.code)
	}
}

type fakeKubernetesYAMLService struct {
	get    func(context.Context, kubernetesyaml.GetInput) (kubernetesyaml.Result, error)
	update func(context.Context, kubernetesyaml.UpdateInput) (kubernetesyaml.Result, error)
}

func (service *fakeKubernetesYAMLService) Get(
	ctx context.Context,
	input kubernetesyaml.GetInput,
) (kubernetesyaml.Result, error) {
	if service.get == nil {
		return kubernetesyaml.Result{}, errors.New("unexpected Get call")
	}
	return service.get(ctx, input)
}

func (service *fakeKubernetesYAMLService) Update(
	ctx context.Context,
	input kubernetesyaml.UpdateInput,
) (kubernetesyaml.Result, error) {
	return service.update(ctx, input)
}

func kubernetesYAMLHandlerTestRouter(service kubernetesYAMLService) http.Handler {
	configureGinMode.Do(func() {
		gin.SetMode(gin.ReleaseMode)
	})
	router := gin.New()
	handler := newKubernetesYAMLHandler(
		discardLogger(), service, nil, 5*time.Second,
	)
	router.GET(
		"/api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name/yaml",
		handler.get,
	)
	router.PUT(
		"/api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name/yaml",
		handler.update,
	)
	return router
}

/*
 * The generic endpoint still refuses the two families that have their own.
 *
 * Both now have YAML routes of their own, and those routes exist precisely
 * because this one answers to `cluster.resource.*`. If the exclusion here were
 * ever relaxed, holding the resource permissions would be enough to read a
 * Secret and to rewrite a RoleBinding, and neither of the dedicated routes
 * would be worth anything.
 */
func TestKubernetesYAMLHandlerStillRefusesSecretsAndAuthorizationResources(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesYAMLService{
		get: func(context.Context, kubernetesyaml.GetInput) (kubernetesyaml.Result, error) {
			t.Fatal("an excluded resource reached the generic YAML service")
			return kubernetesyaml.Result{}, nil
		},
		update: func(context.Context, kubernetesyaml.UpdateInput) (kubernetesyaml.Result, error) {
			t.Fatal("an excluded resource reached the generic YAML service")
			return kubernetesyaml.Result{}, nil
		},
	}
	router := kubernetesYAMLHandlerTestRouter(service)
	for _, query := range []string{
		"version=v1&resource=secrets&namespace=team-a",
		"group=rbac.authorization.k8s.io&version=v1&resource=roles&namespace=team-a",
		"group=rbac.authorization.k8s.io&version=v1&resource=clusterrolebindings",
		"version=v1&resource=serviceaccounts&namespace=team-a",
	} {
		url := "/api/v1/clusters/" + testHTTPClusterID +
			"/kubernetes/resources/target/yaml?" + query

		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, url, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d: %s", query, response.Code, response.Body)
		}

		updated := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodPut,
			url+"&confirm=true",
			bytes.NewBufferString("apiVersion: v1\nkind: Secret\n"),
		)
		request.Header.Set("Content-Type", "application/yaml")
		router.ServeHTTP(updated, request)
		if updated.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s status = %d: %s", query, updated.Code, updated.Body)
		}
	}
}
