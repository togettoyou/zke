package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestKubernetesResourceHandlerDiscoversBuiltInAndCustomResources(
	t *testing.T,
) {
	t.Parallel()

	fakeDiscovery := &discoveryfake.FakeDiscovery{
		Fake: &k8stesting.Fake{
			Resources: []*metav1.APIResourceList{
				{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{
						{
							Name:       "nodes",
							Kind:       "Node",
							Verbs:      metav1.Verbs{"get", "list", "watch"},
							Categories: []string{"all"},
						},
						{
							Name:       "secrets",
							Kind:       "Secret",
							Namespaced: true,
							Verbs:      metav1.Verbs{"get", "list"},
						},
						{
							Name:       "pods/log",
							Kind:       "Pod",
							Namespaced: true,
							Verbs:      metav1.Verbs{"get"},
						},
					},
				},
				{
					GroupVersion: "example.io/v1alpha1",
					APIResources: []metav1.APIResource{{
						Name:       "widgets",
						Kind:       "Widget",
						Namespaced: true,
						Verbs:      metav1.Verbs{"delete", "get", "list"},
						ShortNames: []string{"wdg"},
					}},
				},
			},
		},
	}
	handler := newKubernetesResourceHandler(nil, fakeDiscovery, 1024*1024)
	response, body, err := handler(
		context.Background(),
		&agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	var catalog kubernetescatalog.Catalog
	if err := json.Unmarshal(payload, &catalog); err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		response.GetBodySize() != uint64(len(payload)) ||
		catalog.Partial ||
		len(catalog.Resources) != 2 {
		t.Fatalf(
			"unexpected discovery response: response=%+v catalog=%+v",
			response,
			catalog,
		)
	}
	if catalog.Resources[0].Resource != "nodes" ||
		catalog.Resources[1].Group != "example.io" ||
		catalog.Resources[1].Resource != "widgets" ||
		len(catalog.Resources[1].Verbs) != 2 {
		t.Fatalf("unexpected discovered resources: %+v", catalog.Resources)
	}
}

func TestKubernetesResourceHandlerListsAndGetsCustomResources(t *testing.T) {
	t.Parallel()

	widget := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.io/v1alpha1",
		"kind":       "Widget",
		"metadata": map[string]any{
			"name":      "widget-a",
			"namespace": "tenant-a",
		},
		"spec": map[string]any{"size": "large"},
	}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), widget)
	handler := newKubernetesResourceHandler(client, nil, 1024*1024)
	resource := &agentv1.GroupVersionResource{
		Group:    "example.io",
		Version:  "v1alpha1",
		Resource: "widgets",
	}

	response, body, err := handler(
		context.Background(),
		&agentv1.ResourceRequest{
			Verb:           agentv1.ResourceVerb_RESOURCE_VERB_LIST,
			Resource:       resource,
			Namespace:      "tenant-a",
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	var list unstructured.UnstructuredList
	if err := list.UnmarshalJSON(payload); err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		len(list.Items) != 1 ||
		list.Items[0].GetName() != "widget-a" {
		t.Fatalf("unexpected custom resource list: %+v", list.Items)
	}

	response, body, err = handler(
		context.Background(),
		&agentv1.ResourceRequest{
			Verb:           agentv1.ResourceVerb_RESOURCE_VERB_GET,
			Resource:       resource,
			Namespace:      "tenant-a",
			Name:           "widget-a",
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	payload, err = io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	var found unstructured.Unstructured
	if err := found.UnmarshalJSON(payload); err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		found.GetName() != "widget-a" {
		t.Fatalf(
			"unexpected custom resource detail: response=%+v object=%+v",
			response,
			found.Object,
		)
	}
}

func TestKubernetesResourceHandlerListsAndGetsNodes(t *testing.T) {
	t.Parallel()

	first := testUnstructuredNode(t, "node-a", map[string]string{"pool": "gpu"})
	second := testUnstructuredNode(t, "node-b", map[string]string{"pool": "cpu"})
	client := dynamicfake.NewSimpleDynamicClient(
		runtime.NewScheme(),
		first,
		second,
	)
	handler := newKubernetesResourceHandler(client, nil, 1024*1024)

	response, body, err := handler(
		context.Background(),
		&agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_LIST,
			Resource: &agentv1.GroupVersionResource{
				Version:  "v1",
				Resource: "nodes",
			},
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			ListOptions: &agentv1.ListOptions{
				LabelSelector: "pool=gpu",
				Limit:         25,
			},
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		response.GetKubernetesStatusCode() != http.StatusOK ||
		response.GetContentType() != kubernetesJSONContentType ||
		response.GetBodySize() != uint64(len(payload)) {
		t.Fatalf("unexpected Resource response: %+v", response)
	}
	var listed unstructured.UnstructuredList
	if err := listed.UnmarshalJSON(payload); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 || listed.Items[0].GetName() != "node-a" {
		t.Fatalf("unexpected listed Nodes: %+v", listed.Items)
	}

	response, body, err = handler(
		context.Background(),
		&agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_GET,
			Resource: &agentv1.GroupVersionResource{
				Version:  "v1",
				Resource: "nodes",
			},
			Name:           "node-b",
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	payload, err = io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	var found unstructured.Unstructured
	if err := found.UnmarshalJSON(payload); err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		found.GetName() != "node-b" {
		t.Fatalf("unexpected Node detail response: response=%+v node=%+v", response, found)
	}
}

func TestKubernetesResourceHandlerMapsKubernetesErrors(t *testing.T) {
	t.Parallel()

	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	client.PrependReactor("get", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Resource: "nodes"},
			"node-a",
			nil,
		)
	})
	handler := newKubernetesResourceHandler(client, nil, 1024)
	response, body, err := handler(
		context.Background(),
		&agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_GET,
			Resource: &agentv1.GroupVersionResource{
				Version:  "v1",
				Resource: "nodes",
			},
			Name:           "node-a",
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_FORBIDDEN ||
		response.GetKubernetesStatusCode() != http.StatusForbidden ||
		response.GetReason() != string(metav1.StatusReasonForbidden) ||
		response.GetMessage() != "Kubernetes API request failed" {
		t.Fatalf("unexpected Kubernetes error response: %+v", response)
	}
}

func TestKubernetesResourceHandlerRejectsUnsupportedRepresentationAndLargeBody(
	t *testing.T,
) {
	t.Parallel()

	node := testUnstructuredNode(t, "node-a", map[string]string{
		"large": "value-that-does-not-fit",
	})
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), node)
	handler := newKubernetesResourceHandler(client, nil, 8)

	response, body, err := handler(
		context.Background(),
		&agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_GET,
			Resource: &agentv1.GroupVersionResource{
				Version:  "v1",
				Resource: "nodes",
			},
			Name:           "node-a",
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED ||
		response.GetKubernetesStatusCode() != http.StatusRequestEntityTooLarge {
		t.Fatalf("unexpected body limit response: %+v", response)
	}

	response, body, err = handler(
		context.Background(),
		&agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_GET,
			Resource: &agentv1.GroupVersionResource{
				Version:  "v1",
				Resource: "nodes",
			},
			Name:           "node-a",
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_TABLE,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT {
		t.Fatalf("unexpected representation response: %+v", response)
	}

	response, body, err = handler(
		context.Background(),
		&agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_GET,
			Resource: &agentv1.GroupVersionResource{
				Version:  "v1",
				Resource: "secrets",
			},
			Name:           "private",
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_FORBIDDEN ||
		response.GetReason() != "ResourceNotAllowed" {
		t.Fatalf("unexpected Resource allowlist response: %+v", response)
	}
}

func testUnstructuredNode(
	t *testing.T,
	name string,
	nodeLabels map[string]string,
) *unstructured.Unstructured {
	t.Helper()
	unstructuredLabels := make(map[string]any, len(nodeLabels))
	for key, value := range nodeLabels {
		unstructuredLabels[key] = value
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Node",
		"metadata": map[string]any{
			"name":   name,
			"labels": unstructuredLabels,
		},
	}}
}
