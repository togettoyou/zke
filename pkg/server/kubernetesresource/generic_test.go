package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestServiceMutatesGenericCustomResources(t *testing.T) {
	t.Parallel()

	const idempotencyKey = "0123456789abcdef"
	calls := 0
	requester := &fakeResourceRequester{
		handle: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Writer,
		) (*agentv1.ResourceResponse, error) {
			t.Fatal("mutation used read-only transport")
			return nil, nil
		},
		mutate: func(
			_ context.Context,
			clusterID string,
			request *agentv1.ResourceRequest,
			requestBody io.Reader,
			responseBody io.Writer,
			key string,
		) (*agentv1.ResourceResponse, error) {
			calls++
			if clusterID != testClusterID || key != idempotencyKey {
				t.Fatalf("unexpected mutation scope: cluster=%q key=%q", clusterID, key)
			}
			var body []byte
			if requestBody != nil {
				var err error
				body, err = io.ReadAll(requestBody)
				if err != nil {
					t.Fatal(err)
				}
			}
			if uint64(len(body)) != request.GetBodySize() {
				t.Fatalf("mutation body length = %d request=%+v", len(body), request)
			}
			if request.GetVerb() == agentv1.ResourceVerb_RESOURCE_VERB_DELETE {
				if len(body) != 0 ||
					request.GetDeleteOptions().GetPropagation() !=
						agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND {
					t.Fatalf("unexpected delete request: %+v body=%q", request, body)
				}
				return &agentv1.ResourceResponse{
					Result:               agentv1.ResultCode_RESULT_CODE_OK,
					KubernetesStatusCode: 200,
				}, nil
			}
			var object unstructured.Unstructured
			switch request.GetVerb() {
			case agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
				agentv1.ResourceVerb_RESOURCE_VERB_UPDATE:
				if err := object.UnmarshalJSON(body); err != nil {
					t.Fatal(err)
				}
			case agentv1.ResourceVerb_RESOURCE_VERB_PATCH:
				object = unstructured.Unstructured{Object: map[string]any{
					"apiVersion": "example.io/v1alpha1",
					"kind":       "Widget",
					"metadata": map[string]any{
						"name":      "widget-a",
						"namespace": "tenant-a",
					},
					"spec": map[string]any{"size": "large"},
				}}
			default:
				t.Fatalf("unexpected mutation verb: %s", request.GetVerb())
			}
			return writeKubernetesObject(t, responseBody, &object), nil
		},
	}
	service := NewService(requester)
	baseObject := map[string]any{
		"apiVersion": "example.io/v1alpha1",
		"kind":       "Widget",
		"metadata": map[string]any{
			"name":      "widget-a",
			"namespace": "tenant-a",
		},
		"spec": map[string]any{"size": "small"},
	}
	created, err := service.CreateResource(
		context.Background(),
		CreateResourceInput{
			ClusterID:      testClusterID,
			Resource:       widgetIdentity,
			Namespace:      "tenant-a",
			Object:         baseObject,
			Confirm:        true,
			IdempotencyKey: idempotencyKey,
		},
	)
	if err != nil || objectNameFromMap(created) != "widget-a" {
		t.Fatalf("CreateResource() object=%+v err=%v", created, err)
	}

	updatedObject := runtime.DeepCopyJSONValue(baseObject).(map[string]any)
	metadata := updatedObject["metadata"].(map[string]any)
	metadata["resourceVersion"] = "42"
	updated, err := service.UpdateResource(
		context.Background(),
		UpdateResourceInput{
			ClusterID:      testClusterID,
			Resource:       widgetIdentity,
			Namespace:      "tenant-a",
			Name:           "widget-a",
			Object:         updatedObject,
			Confirm:        true,
			IdempotencyKey: idempotencyKey,
		},
	)
	if err != nil || objectNameFromMap(updated) != "widget-a" {
		t.Fatalf("UpdateResource() object=%+v err=%v", updated, err)
	}

	patched, err := service.PatchResource(
		context.Background(),
		PatchResourceInput{
			ClusterID:      testClusterID,
			Resource:       widgetIdentity,
			Namespace:      "tenant-a",
			Name:           "widget-a",
			PatchType:      agentv1.PatchType_PATCH_TYPE_MERGE,
			Patch:          json.RawMessage(`{"spec":{"size":"large"}}`),
			Confirm:        true,
			IdempotencyKey: idempotencyKey,
		},
	)
	if err != nil || objectNameFromMap(patched) != "widget-a" {
		t.Fatalf("PatchResource() object=%+v err=%v", patched, err)
	}

	grace := int64(0)
	err = service.DeleteResource(
		context.Background(),
		DeleteResourceInput{
			ClusterID:          testClusterID,
			Resource:           widgetIdentity,
			Namespace:          "tenant-a",
			Name:               "widget-a",
			Confirm:            true,
			GracePeriodSeconds: &grace,
			Propagation:        agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND,
			IdempotencyKey:     idempotencyKey,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("mutation calls = %d, want 4", calls)
	}
}

func TestServiceRejectsUnsafeGenericMutations(t *testing.T) {
	t.Parallel()

	var called bool
	requester := &fakeResourceRequester{
		handle: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Writer,
		) (*agentv1.ResourceResponse, error) {
			return nil, errors.New("unused")
		},
		mutate: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Reader,
			io.Writer,
			string,
		) (*agentv1.ResourceResponse, error) {
			called = true
			return nil, nil
		},
	}
	service := NewService(requester)
	_, err := service.CreateResource(
		context.Background(),
		CreateResourceInput{
			ClusterID: testClusterID,
			Resource: ResourceIdentity{
				Version:  "v1",
				Resource: "secrets",
			},
			Object: map[string]any{},
		},
	)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Secret create error = %v", err)
	}
	_, err = service.PatchResource(
		context.Background(),
		PatchResourceInput{
			ClusterID: testClusterID,
			Resource:  widgetIdentity,
			Namespace: "tenant-a",
			Name:      "widget-a",
			PatchType: agentv1.PatchType_PATCH_TYPE_JSON,
			Patch: json.RawMessage(
				`[{"op":"replace","path":"/metadata/name","value":"other"}]`,
			),
			Confirm:        true,
			IdempotencyKey: "0123456789abcdef",
		},
	)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("identity patch error = %v", err)
	}
	if called {
		t.Fatal("unsafe mutation reached transport")
	}
}

func TestMutationResponseDistinguishesIdempotencyConflict(t *testing.T) {
	t.Parallel()

	err := mutationResponseError(&agentv1.ResourceResponse{
		Result:               agentv1.ResultCode_RESULT_CODE_CONFLICT,
		KubernetesStatusCode: 409,
		Reason:               "IdempotencyConflict",
	}, false)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("mutation response error = %v, want idempotency conflict", err)
	}
}

func objectNameFromMap(object map[string]any) string {
	metadata, _ := object["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	return name
}

var widgetIdentity = ResourceIdentity{
	Group:    "example.io",
	Version:  "v1alpha1",
	Resource: "widgets",
}

func TestServiceDiscoversGenericResources(t *testing.T) {
	t.Parallel()

	catalog := kubernetescatalog.Catalog{
		Resources: []kubernetescatalog.Resource{
			{
				Version:  "v1",
				Resource: "secrets",
				Kind:     "Secret",
				Verbs:    []string{"get", "list"},
			},
			{
				Group:      widgetIdentity.Group,
				Version:    widgetIdentity.Version,
				Resource:   widgetIdentity.Resource,
				Kind:       "Widget",
				Namespaced: true,
				Verbs:      []string{"delete", "list", "get"},
			},
		},
	}
	requester := &fakeResourceRequester{handle: func(
		_ context.Context,
		clusterID string,
		request *agentv1.ResourceRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error) {
		if clusterID != testClusterID ||
			request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER ||
			request.GetResource() != nil {
			t.Fatalf(
				"unexpected discovery request: cluster=%q request=%+v",
				clusterID,
				request,
			)
		}
		return writeKubernetesObject(t, responseBody, catalog), nil
	}}

	result, err := NewService(requester).DiscoverResources(
		context.Background(),
		testClusterID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Resources) != 1 ||
		result.Resources[0].Resource != "widgets" ||
		len(result.Resources[0].Verbs) != 3 ||
		result.Resources[0].Verbs[0] != "get" ||
		result.Resources[0].Verbs[1] != "list" ||
		result.Resources[0].Verbs[2] != "delete" {
		t.Fatalf("unexpected filtered discovery catalog: %+v", result)
	}
}

func TestServiceListsAndGetsGenericCustomResources(t *testing.T) {
	t.Parallel()

	remaining := int64(3)
	widget := unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.io/v1alpha1",
		"kind":       "Widget",
		"metadata": map[string]any{
			"name":      "widget-a",
			"namespace": "tenant-a",
			"managedFields": []any{
				map[string]any{"manager": "test"},
			},
		},
		"spec": map[string]any{"size": "large"},
	}}
	list := unstructured.UnstructuredList{
		Object: map[string]any{
			"apiVersion": "example.io/v1alpha1",
			"kind":       "WidgetList",
			"metadata": map[string]any{
				"continue":           "next-page",
				"resourceVersion":    "42",
				"remainingItemCount": remaining,
			},
		},
		Items: []unstructured.Unstructured{widget},
	}
	requester := &fakeResourceRequester{handle: func(
		_ context.Context,
		clusterID string,
		request *agentv1.ResourceRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error) {
		if clusterID != testClusterID ||
			request.GetResource().GetGroup() != widgetIdentity.Group ||
			request.GetResource().GetVersion() != widgetIdentity.Version ||
			request.GetResource().GetResource() != widgetIdentity.Resource ||
			request.GetNamespace() != "tenant-a" {
			t.Fatalf(
				"unexpected generic Resource request: cluster=%q request=%+v",
				clusterID,
				request,
			)
		}
		switch request.GetVerb() {
		case agentv1.ResourceVerb_RESOURCE_VERB_LIST:
			if request.GetListOptions().GetLimit() != 25 ||
				request.GetListOptions().GetContinueToken() != "current-page" ||
				request.GetListOptions().GetResourceVersion() != "40" {
				t.Fatalf("unexpected generic list options: %+v", request)
			}
			return writeKubernetesObject(t, responseBody, &list), nil
		case agentv1.ResourceVerb_RESOURCE_VERB_GET:
			if request.GetName() != "widget-a" {
				t.Fatalf("unexpected generic resource name: %+v", request)
			}
			return writeKubernetesObject(t, responseBody, &widget), nil
		default:
			t.Fatalf("unexpected generic Resource verb: %s", request.GetVerb())
			return nil, nil
		}
	}}
	service := NewService(requester)

	page, err := service.ListResources(
		context.Background(),
		ListResourcesInput{
			ClusterID:       testClusterID,
			Resource:        widgetIdentity,
			Namespace:       "tenant-a",
			Limit:           25,
			ContinueToken:   "current-page",
			ResourceVersion: "40",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := page.Items[0]["metadata"].(map[string]any)
	if page.Kind != "WidgetList" ||
		page.ContinueToken != "next-page" ||
		page.ResourceVersion != "42" ||
		page.RemainingItemCount == nil ||
		*page.RemainingItemCount != 3 ||
		len(page.Items) != 1 ||
		metadata["managedFields"] != nil {
		t.Fatalf("unexpected generic resource page: %+v", page)
	}

	object, err := service.GetResource(
		context.Background(),
		GetResourceInput{
			ClusterID: testClusterID,
			Resource:  widgetIdentity,
			Namespace: "tenant-a",
			Name:      "widget-a",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	objectMetadata, _ := object["metadata"].(map[string]any)
	if objectMetadata["name"] != "widget-a" ||
		objectMetadata["managedFields"] != nil {
		t.Fatalf("unexpected generic resource detail: %+v", object)
	}
}

func TestServiceRejectsSensitiveAndInvalidGenericResources(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{handle: func(
		context.Context,
		string,
		*agentv1.ResourceRequest,
		io.Writer,
	) (*agentv1.ResourceResponse, error) {
		t.Fatal("invalid generic request reached Resource transport")
		return nil, nil
	}}
	service := NewService(requester)
	_, err := service.ListResources(
		context.Background(),
		ListResourcesInput{
			ClusterID: testClusterID,
			Resource: ResourceIdentity{
				Version:  "v1",
				Resource: "secrets",
			},
			Limit: 10,
		},
	)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Secret list error = %v, want invalid input", err)
	}
	_, err = service.GetResource(
		context.Background(),
		GetResourceInput{
			ClusterID: testClusterID,
			Resource:  widgetIdentity,
			Namespace: "INVALID_NAMESPACE",
			Name:      "widget-a",
		},
	)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid namespace error = %v, want invalid input", err)
	}
}

func TestGenericResourceNotFoundUsesGenericError(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{handle: func(
		context.Context,
		string,
		*agentv1.ResourceRequest,
		io.Writer,
	) (*agentv1.ResourceResponse, error) {
		return &agentv1.ResourceResponse{
			Result:               agentv1.ResultCode_RESULT_CODE_NOT_FOUND,
			KubernetesStatusCode: 404,
			Reason:               string(metav1.StatusReasonNotFound),
		}, nil
	}}
	_, err := NewService(requester).GetResource(
		context.Background(),
		GetResourceInput{
			ClusterID: testClusterID,
			Resource:  widgetIdentity,
			Namespace: "tenant-a",
			Name:      "missing",
		},
	)
	if !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("GetResource() error = %v, want generic not found", err)
	}
}
