package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
)

type fakeGenericKubernetesResourceService struct {
	discover func(context.Context, string) (kubernetescatalog.Catalog, error)
	list     func(
		context.Context,
		kubernetesresource.ListResourcesInput,
	) (kubernetesresource.ResourcePage, error)
	get func(
		context.Context,
		kubernetesresource.GetResourceInput,
	) (map[string]any, error)
}

func (service *fakeGenericKubernetesResourceService) DiscoverResources(
	ctx context.Context,
	clusterID string,
) (kubernetescatalog.Catalog, error) {
	return service.discover(ctx, clusterID)
}

func (service *fakeGenericKubernetesResourceService) ListResources(
	ctx context.Context,
	input kubernetesresource.ListResourcesInput,
) (kubernetesresource.ResourcePage, error) {
	return service.list(ctx, input)
}

func (service *fakeGenericKubernetesResourceService) GetResource(
	ctx context.Context,
	input kubernetesresource.GetResourceInput,
) (map[string]any, error) {
	return service.get(ctx, input)
}

func TestGenericKubernetesResourceHandlers(t *testing.T) {
	t.Parallel()

	remaining := int64(2)
	service := &fakeGenericKubernetesResourceService{
		discover: func(
			_ context.Context,
			clusterID string,
		) (kubernetescatalog.Catalog, error) {
			if clusterID != testHTTPClusterID {
				t.Fatalf("discovery cluster = %q", clusterID)
			}
			return kubernetescatalog.Catalog{
				Resources: []kubernetescatalog.Resource{{
					Group:      "example.io",
					Version:    "v1alpha1",
					Resource:   "widgets",
					Kind:       "Widget",
					Namespaced: true,
					Verbs:      []string{"get", "list"},
				}},
			}, nil
		},
		list: func(
			_ context.Context,
			input kubernetesresource.ListResourcesInput,
		) (kubernetesresource.ResourcePage, error) {
			if input.ClusterID != testHTTPClusterID ||
				input.Resource.Group != "example.io" ||
				input.Resource.Version != "v1alpha1" ||
				input.Resource.Resource != "widgets" ||
				input.Namespace != "tenant-a" ||
				input.Limit != 25 ||
				input.ContinueToken != "current" ||
				input.LabelSelector != "tier=control" {
				t.Fatalf("unexpected generic list input: %+v", input)
			}
			return kubernetesresource.ResourcePage{
				APIVersion: "example.io/v1alpha1",
				Kind:       "WidgetList",
				Items: []map[string]any{{
					"metadata": map[string]any{"name": "widget-a"},
				}},
				ContinueToken:      "next",
				ResourceVersion:    "42",
				RemainingItemCount: &remaining,
			}, nil
		},
		get: func(
			_ context.Context,
			input kubernetesresource.GetResourceInput,
		) (map[string]any, error) {
			if input.ClusterID != testHTTPClusterID ||
				input.Resource.Group != "example.io" ||
				input.Resource.Version != "v1alpha1" ||
				input.Resource.Resource != "widgets" ||
				input.Namespace != "tenant-a" ||
				input.Name != "widget-a" {
				t.Fatalf("unexpected generic get input: %+v", input)
			}
			return map[string]any{
				"apiVersion": "example.io/v1alpha1",
				"kind":       "Widget",
				"metadata":   map[string]any{"name": "widget-a"},
			}, nil
		},
	}
	router := genericResourceHandlerTestRouter(service)

	discoveryResponse := httptest.NewRecorder()
	router.ServeHTTP(
		discoveryResponse,
		httptest.NewRequest(
			http.MethodGet,
			"/api/v1/clusters/"+testHTTPClusterID+
				"/kubernetes/resource-types",
			nil,
		),
	)
	if discoveryResponse.Code != http.StatusOK {
		t.Fatalf(
			"discovery status = %d: %s",
			discoveryResponse.Code,
			discoveryResponse.Body,
		)
	}
	var discoveryData kubernetescatalog.Catalog
	if err := decodeSuccessResponse(discoveryResponse, &discoveryData); err != nil {
		t.Fatal(err)
	}
	if len(discoveryData.Resources) != 1 ||
		discoveryData.Resources[0].Resource != "widgets" {
		t.Fatalf("unexpected discovery data: %+v", discoveryData)
	}

	listResponse := httptest.NewRecorder()
	router.ServeHTTP(
		listResponse,
		httptest.NewRequest(
			http.MethodGet,
			"/api/v1/clusters/"+testHTTPClusterID+
				"/kubernetes/resources?group=example.io&version=v1alpha1"+
				"&resource=widgets&namespace=tenant-a&limit=25"+
				"&continue=current&label_selector=tier%3Dcontrol",
			nil,
		),
	)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", listResponse.Code, listResponse.Body)
	}
	var listData struct {
		Items []map[string]any `json:"items"`
		Kind  string           `json:"kind"`
	}
	if err := decodeSuccessResponse(listResponse, &listData); err != nil {
		t.Fatal(err)
	}
	if listData.Kind != "WidgetList" || len(listData.Items) != 1 {
		t.Fatalf("unexpected generic list data: %+v", listData)
	}

	getResponse := httptest.NewRecorder()
	router.ServeHTTP(
		getResponse,
		httptest.NewRequest(
			http.MethodGet,
			"/api/v1/clusters/"+testHTTPClusterID+
				"/kubernetes/resources/widget-a?group=example.io"+
				"&version=v1alpha1&resource=widgets&namespace=tenant-a",
			nil,
		),
	)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", getResponse.Code, getResponse.Body)
	}
	var getData map[string]any
	if err := decodeSuccessResponse(getResponse, &getData); err != nil {
		t.Fatal(err)
	}
	if getData["kind"] != "Widget" {
		t.Fatalf("unexpected generic detail data: %+v", getData)
	}
}

func TestGenericKubernetesResourceHandlerRejectsInvalidQueryAndMapsErrors(
	t *testing.T,
) {
	t.Parallel()

	service := &fakeGenericKubernetesResourceService{
		discover: func(context.Context, string) (kubernetescatalog.Catalog, error) {
			return kubernetescatalog.Catalog{}, errors.New("unused")
		},
		list: func(
			context.Context,
			kubernetesresource.ListResourcesInput,
		) (kubernetesresource.ResourcePage, error) {
			return kubernetesresource.ResourcePage{},
				kubernetesresource.ErrClusterAccessDenied
		},
		get: func(
			context.Context,
			kubernetesresource.GetResourceInput,
		) (map[string]any, error) {
			return nil, kubernetesresource.ErrResourceNotFound
		},
	}
	router := genericResourceHandlerTestRouter(service)

	invalid := httptest.NewRecorder()
	router.ServeHTTP(
		invalid,
		httptest.NewRequest(
			http.MethodGet,
			"/api/v1/clusters/"+testHTTPClusterID+
				"/kubernetes/resources?version=v1&resource=pods&extra=true",
			nil,
		),
	)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid query status = %d: %s", invalid.Code, invalid.Body)
	}

	forbidden := httptest.NewRecorder()
	router.ServeHTTP(
		forbidden,
		httptest.NewRequest(
			http.MethodGet,
			"/api/v1/clusters/"+testHTTPClusterID+
				"/kubernetes/resources?version=v1&resource=pods",
			nil,
		),
	)
	if forbidden.Code != http.StatusBadGateway {
		t.Fatalf("forbidden status = %d: %s", forbidden.Code, forbidden.Body)
	}
	assertErrorCode(t, forbidden, "cluster_api_forbidden")

	notFound := httptest.NewRecorder()
	router.ServeHTTP(
		notFound,
		httptest.NewRequest(
			http.MethodGet,
			"/api/v1/clusters/"+testHTTPClusterID+
				"/kubernetes/resources/missing?group=example.io"+
				"&version=v1&resource=widgets",
			nil,
		),
	)
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("not found status = %d: %s", notFound.Code, notFound.Body)
	}
	assertErrorCode(t, notFound, "resource_not_found")
}

func genericResourceHandlerTestRouter(
	service genericKubernetesResourceService,
) http.Handler {
	configureGinMode.Do(func() {
		gin.SetMode(gin.ReleaseMode)
	})
	router := gin.New()
	handler := newKubernetesResourceHandler(
		discardLogger(),
		service,
		5*time.Second,
	)
	router.GET(
		"/api/v1/clusters/:cluster_id/kubernetes/resource-types",
		handler.discover,
	)
	router.GET(
		"/api/v1/clusters/:cluster_id/kubernetes/resources",
		handler.list,
	)
	router.GET(
		"/api/v1/clusters/:cluster_id/kubernetes/resources/:resource_name",
		handler.get,
	)
	return router
}
