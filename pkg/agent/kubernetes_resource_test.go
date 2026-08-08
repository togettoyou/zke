package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestKubernetesResourceHandlerMutatesGenericResources(t *testing.T) {
	t.Parallel()

	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	handler := newKubernetesResourceHandler(client, nil, 1024*1024, "zke-system")
	resource := &agentv1.GroupVersionResource{
		Group:    "example.io",
		Version:  "v1alpha1",
		Resource: "widgets",
	}
	createBody := []byte(`{
		"apiVersion":"example.io/v1alpha1",
		"kind":"Widget",
		"metadata":{"name":"widget-a","namespace":"tenant-a"},
		"spec":{"size":"small"}
	}`)
	response, responseBody, err := handler(
		context.Background(),
		&agentv1.ResourceRequest{
			Verb:            agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
			Resource:        resource,
			Namespace:       "tenant-a",
			Representation:  agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			BodySize:        uint64(len(createBody)),
			MutationOptions: &agentv1.MutationOptions{},
		},
		bytes.NewReader(createBody),
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		response.GetKubernetesStatusCode() != http.StatusCreated ||
		responseBody == nil {
		t.Fatalf("unexpected create response: %+v", response)
	}

	updateBody := []byte(`{
		"apiVersion":"example.io/v1alpha1",
		"kind":"Widget",
		"metadata":{
			"name":"widget-a",
			"namespace":"tenant-a",
			"resourceVersion":"1"
		},
		"spec":{"size":"medium"}
	}`)
	response, _, err = handler(
		context.Background(),
		&agentv1.ResourceRequest{
			Verb:            agentv1.ResourceVerb_RESOURCE_VERB_UPDATE,
			Resource:        resource,
			Namespace:       "tenant-a",
			Name:            "widget-a",
			Representation:  agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			BodySize:        uint64(len(updateBody)),
			MutationOptions: &agentv1.MutationOptions{},
		},
		bytes.NewReader(updateBody),
	)
	if err != nil ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("unexpected update response: response=%+v err=%v", response, err)
	}

	patchBody := []byte(`{"spec":{"size":"large"}}`)
	response, _, err = handler(
		context.Background(),
		&agentv1.ResourceRequest{
			Verb:            agentv1.ResourceVerb_RESOURCE_VERB_PATCH,
			Resource:        resource,
			Namespace:       "tenant-a",
			Name:            "widget-a",
			Representation:  agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			PatchType:       agentv1.PatchType_PATCH_TYPE_MERGE,
			BodySize:        uint64(len(patchBody)),
			MutationOptions: &agentv1.MutationOptions{},
		},
		bytes.NewReader(patchBody),
	)
	if err != nil ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("unexpected patch response: response=%+v err=%v", response, err)
	}

	response, _, err = handler(
		context.Background(),
		&agentv1.ResourceRequest{
			Verb:      agentv1.ResourceVerb_RESOURCE_VERB_DELETE,
			Resource:  resource,
			Namespace: "tenant-a",
			Name:      "widget-a",
			DeleteOptions: &agentv1.DeleteOptions{
				Propagation: agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND,
			},
		},
		nil,
	)
	if err != nil ||
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		response.GetBodySize() != 0 {
		t.Fatalf("unexpected delete response: response=%+v err=%v", response, err)
	}

	_, getErr := client.Resource(schema.GroupVersionResource{
		Group:    "example.io",
		Version:  "v1alpha1",
		Resource: "widgets",
	}).Namespace("tenant-a").Get(
		context.Background(),
		"widget-a",
		metav1.GetOptions{},
	)
	if !apierrors.IsNotFound(getErr) {
		t.Fatalf("deleted resource lookup error = %v, want not found", getErr)
	}
}

// Only a mutation that may have reached the cluster reserves its idempotency
// key. A preflight and a refusal leave the cluster untouched, and the attempt
// that follows either of them under the same key is normally the corrected
// request — answering that with IdempotencyConflict would leave the operator
// nothing to do but abandon the form and fill it in again.
func TestMutationResultAppliedOnlyWhenClusterStateMayHaveChanged(t *testing.T) {
	t.Parallel()

	widgets := schema.GroupVersionResource{
		Group:    "example.io",
		Version:  "v1alpha1",
		Resource: "widgets",
	}
	resource := &agentv1.GroupVersionResource{
		Group:    widgets.Group,
		Version:  widgets.Version,
		Resource: widgets.Resource,
	}
	body := []byte(`{
		"apiVersion":"example.io/v1alpha1",
		"kind":"Widget",
		"metadata":{"name":"widget-a","namespace":"tenant-a"},
		"spec":{"size":"small"}
	}`)
	createRequest := func(dryRun bool) *agentv1.ResourceRequest {
		return &agentv1.ResourceRequest{
			Verb:            agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
			Resource:        resource,
			Namespace:       "tenant-a",
			Representation:  agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			BodySize:        uint64(len(body)),
			MutationOptions: &agentv1.MutationOptions{DryRun: dryRun},
		}
	}
	target := func(objects ...runtime.Object) dynamic.ResourceInterface {
		return dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...).
			Resource(widgets).
			Namespace("tenant-a")
	}
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.io/v1alpha1",
		"kind":       "Widget",
		"metadata": map[string]any{
			"name":      "widget-a",
			"namespace": "tenant-a",
		},
	}}

	// The preflight the operator runs before writing anything.
	preflight, err := executeKubernetesResourceMutation(
		context.Background(),
		target(),
		createRequest(true),
		body,
		1024*1024,
	)
	if err != nil ||
		preflight.response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		preflight.applied {
		t.Fatalf("dry run result=%+v applied=%t err=%v", preflight.response, preflight.applied, err)
	}

	// The preflight that fails because the name is taken — the case the operator
	// corrects and submits again.
	refused, err := executeKubernetesResourceMutation(
		context.Background(),
		target(existing),
		createRequest(true),
		body,
		1024*1024,
	)
	if err != nil ||
		refused.response.GetKubernetesStatusCode() != http.StatusConflict ||
		refused.applied {
		t.Fatalf("refused result=%+v applied=%t err=%v", refused.response, refused.applied, err)
	}

	// The same refusal on a real write: still nothing written, still no reason to
	// hold the key.
	refusedWrite, err := executeKubernetesResourceMutation(
		context.Background(),
		target(existing),
		createRequest(false),
		body,
		1024*1024,
	)
	if err != nil ||
		refusedWrite.response.GetKubernetesStatusCode() != http.StatusConflict ||
		refusedWrite.applied {
		t.Fatalf(
			"refused write result=%+v applied=%t err=%v",
			refusedWrite.response,
			refusedWrite.applied,
			err,
		)
	}

	// The write that succeeded, which is what the key exists to protect.
	created, err := executeKubernetesResourceMutation(
		context.Background(),
		target(),
		createRequest(false),
		body,
		1024*1024,
	)
	if err != nil ||
		created.response.GetKubernetesStatusCode() != http.StatusCreated ||
		!created.applied {
		t.Fatalf("create result=%+v applied=%t err=%v", created.response, created.applied, err)
	}

	// An identity the Agent refuses before Kubernetes is ever asked.
	invalid, err := executeKubernetesResourceMutation(
		context.Background(),
		target(),
		createRequest(false),
		[]byte(`{"apiVersion":"other.io/v1","kind":"Widget","metadata":{"name":"widget-a"}}`),
		1024*1024,
	)
	if err != nil ||
		invalid.response.GetResult() != agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT ||
		invalid.applied {
		t.Fatalf("invalid result=%+v applied=%t err=%v", invalid.response, invalid.applied, err)
	}
}

func TestMutationDryRunReadsTheOptionsOfItsVerb(t *testing.T) {
	t.Parallel()

	if !mutationDryRun(&agentv1.ResourceRequest{
		Verb:            agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
		MutationOptions: &agentv1.MutationOptions{DryRun: true},
	}) {
		t.Fatal("create dry run not detected")
	}
	// Delete carries the flag in its own options message; reading MutationOptions
	// for it would report every dry-run delete as a real one.
	if !mutationDryRun(&agentv1.ResourceRequest{
		Verb:          agentv1.ResourceVerb_RESOURCE_VERB_DELETE,
		DeleteOptions: &agentv1.DeleteOptions{DryRun: true},
	}) {
		t.Fatal("delete dry run not detected")
	}
	if mutationDryRun(&agentv1.ResourceRequest{
		Verb:            agentv1.ResourceVerb_RESOURCE_VERB_UPDATE,
		MutationOptions: &agentv1.MutationOptions{},
	}) {
		t.Fatal("update without dry run reported as dry run")
	}
	if mutationDryRun(&agentv1.ResourceRequest{
		Verb: agentv1.ResourceVerb_RESOURCE_VERB_DELETE,
	}) {
		t.Fatal("delete without options reported as dry run")
	}
}

func TestKubernetesResourceDiscoveryPolicyEnforcesVerbAndScope(t *testing.T) {
	t.Parallel()

	fakeDiscovery := &discoveryfake.FakeDiscovery{
		Fake: &k8stesting.Fake{
			Resources: []*metav1.APIResourceList{{
				GroupVersion: "v1",
				APIResources: []metav1.APIResource{
					{
						Name:       "pods",
						Namespaced: true,
						Verbs:      metav1.Verbs{"get", "list", "create"},
					},
					{
						Name:       "nodes",
						Namespaced: false,
						Verbs:      metav1.Verbs{"get", "list"},
					},
				},
			}},
		},
	}
	policy := &kubernetesResourceDiscoveryPolicy{
		client:  fakeDiscovery,
		entries: make(map[schema.GroupVersionResource]discoveredKubernetesResource),
	}
	resource := func(name string) *agentv1.GroupVersionResource {
		return &agentv1.GroupVersionResource{Version: "v1", Resource: name}
	}
	testCases := []struct {
		name         string
		request      *agentv1.ResourceRequest
		wantAllowed  bool
		wantBadScope bool
	}{
		{
			name: "all Namespace list is allowed",
			request: &agentv1.ResourceRequest{
				Verb: agentv1.ResourceVerb_RESOURCE_VERB_LIST, Resource: resource("pods"),
			},
			wantAllowed: true,
		},
		{
			name: "namespaced get requires Namespace",
			request: &agentv1.ResourceRequest{
				Verb: agentv1.ResourceVerb_RESOURCE_VERB_GET, Resource: resource("pods"),
			},
			wantBadScope: true,
		},
		{
			name: "cluster scoped resource rejects Namespace",
			request: &agentv1.ResourceRequest{
				Verb:      agentv1.ResourceVerb_RESOURCE_VERB_GET,
				Resource:  resource("nodes"),
				Namespace: "tenant-a",
			},
			wantBadScope: true,
		},
		{
			name: "unsupported verb is denied",
			request: &agentv1.ResourceRequest{
				Verb: agentv1.ResourceVerb_RESOURCE_VERB_DELETE, Resource: resource("nodes"),
			},
		},
		{
			name: "undiscovered resource is denied",
			request: &agentv1.ResourceRequest{
				Verb: agentv1.ResourceVerb_RESOURCE_VERB_LIST, Resource: resource("widgets"),
			},
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			allowed, invalidScope, err := policy.allowed(
				context.Background(),
				testCase.request,
			)
			if err != nil {
				t.Fatal(err)
			}
			if allowed != testCase.wantAllowed ||
				invalidScope != testCase.wantBadScope {
				t.Fatalf(
					"allowed=%t invalidScope=%t, want allowed=%t invalidScope=%t",
					allowed,
					invalidScope,
					testCase.wantAllowed,
					testCase.wantBadScope,
				)
			}
		})
	}
}

func TestKubernetesResourceDiscoveryPolicyBoundsCache(t *testing.T) {
	t.Parallel()

	policy := &kubernetesResourceDiscoveryPolicy{
		entries: make(map[schema.GroupVersionResource]discoveredKubernetesResource),
	}
	for index := 0; index <= maxKubernetesResourceDiscoveryEntries; index++ {
		policy.remember(
			schema.GroupVersionResource{
				Group:    "example.io",
				Version:  "v1",
				Resource: "resource-" + strconv.Itoa(index),
			},
			discoveredKubernetesResource{
				found:     true,
				expiresAt: time.Now().Add(time.Duration(index) * time.Second),
			},
		)
	}
	if len(policy.entries) != maxKubernetesResourceDiscoveryEntries {
		t.Fatalf(
			"Discovery cache entries = %d, want %d",
			len(policy.entries),
			maxKubernetesResourceDiscoveryEntries,
		)
	}
}

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
	handler := newKubernetesResourceHandler(nil, fakeDiscovery, 1024*1024, "zke-system")
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
		len(catalog.Resources[1].Verbs) != 3 ||
		catalog.Resources[1].Verbs[2] != "delete" {
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
	handler := newKubernetesResourceHandler(client, nil, 1024*1024, "zke-system")
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
	handler := newKubernetesResourceHandler(client, nil, 1024*1024, "zke-system")

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
	handler := newKubernetesResourceHandler(client, nil, 1024, "zke-system")
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
	handler := newKubernetesResourceHandler(client, nil, 8, "zke-system")

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

// The discovery catalog has to say which resources come from a CRD, because
// Kubernetes discovery does not: a CRD-backed resource looks exactly like a
// built-in one, and the browser's "custom resources only" filter is built on
// this bit.
func TestKubernetesResourceDiscoveryMarksCustomResourceDefinitions(t *testing.T) {
	t.Parallel()

	definition := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": "widgets.example.io"},
		"spec": map[string]any{
			"group": "example.io",
			"names": map[string]any{"plural": "widgets", "kind": "Widget"},
		},
	}}
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{
			Group:   "apiextensions.k8s.io",
			Version: "v1",
			Kind:    "CustomResourceDefinitionList",
		},
		&unstructured.UnstructuredList{},
	)
	client := dynamicfake.NewSimpleDynamicClient(scheme, definition)
	fakeDiscovery := &discoveryfake.FakeDiscovery{
		Fake: &k8stesting.Fake{
			Resources: []*metav1.APIResourceList{
				{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{{
						Name:       "nodes",
						Kind:       "Node",
						Namespaced: false,
						Verbs:      metav1.Verbs{"get", "list"},
					}},
				},
				{
					GroupVersion: "example.io/v1alpha1",
					APIResources: []metav1.APIResource{{
						Name:       "widgets",
						Kind:       "Widget",
						Namespaced: true,
						Verbs:      metav1.Verbs{"get", "list"},
					}},
				},
			},
		},
	}
	handler := newKubernetesResourceHandler(client, fakeDiscovery, 1024*1024, "zke-system")
	response, body, err := handler(
		context.Background(),
		&agentv1.ResourceRequest{Verb: agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER},
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
		!catalog.CustomResourcesKnown ||
		len(catalog.Resources) != 2 {
		t.Fatalf("unexpected catalog: %+v", catalog)
	}
	if catalog.Resources[0].Resource != "nodes" || catalog.Resources[0].CustomResource ||
		catalog.Resources[1].Resource != "widgets" || !catalog.Resources[1].CustomResource {
		t.Fatalf("unexpected custom resource marking: %+v", catalog.Resources)
	}
}

// Without the CRD read the answer is unknown rather than false, so a filter
// built on it can say so instead of showing an empty cluster.
func TestKubernetesResourceDiscoveryWithoutCustomResourceAccess(t *testing.T) {
	t.Parallel()

	fakeDiscovery := &discoveryfake.FakeDiscovery{
		Fake: &k8stesting.Fake{
			Resources: []*metav1.APIResourceList{{
				GroupVersion: "v1",
				APIResources: []metav1.APIResource{{
					Name:       "nodes",
					Kind:       "Node",
					Namespaced: false,
					Verbs:      metav1.Verbs{"get", "list"},
				}},
			}},
		},
	}
	handler := newKubernetesResourceHandler(nil, fakeDiscovery, 1024*1024, "zke-system")
	_, body, err := handler(
		context.Background(),
		&agentv1.ResourceRequest{Verb: agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER},
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
	if catalog.CustomResourcesKnown || len(catalog.Resources) != 1 || catalog.Resources[0].CustomResource {
		t.Fatalf("unexpected catalog without CRD access: %+v", catalog)
	}
}

// The Agent's own refusal, which is not the Server's.
//
// A Secret is reachable only when the Server's dedicated Secret API asked for
// it, and never in the namespace holding this Agent's identity key, enrollment
// token and the certificates it trusts the Server by. Both rules are checked
// here rather than assumed of the Server, because a Server that had been made
// to ask is exactly the case they exist for.
func TestSecretRequestsAreRefusedWithoutTheFlagOrInTheAgentNamespace(t *testing.T) {
	t.Parallel()

	secret := func(namespace string, access bool) *agentv1.ResourceRequest {
		return &agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_GET,
			Resource: &agentv1.GroupVersionResource{
				Version: "v1", Resource: "secrets",
			},
			Namespace:    namespace,
			Name:         "registry",
			SecretAccess: access,
		}
	}
	if refuseKubernetesResourceRequest(secret("default", false), "zke-system") != refusalNotEnabled {
		t.Fatal("a Secret request without the Secret API flag was allowed")
	}
	// Refused for a reason of its own: this one is a boundary of ZKE's, and an
	// operator told only that "the Agent is not allowed" would go and widen the
	// Agent's ClusterRole, which will never make it readable.
	if refuseKubernetesResourceRequest(secret("zke-system", true), "zke-system") != refusalAgentNamespace {
		t.Fatal("a Secret request in the Agent's own namespace was not refused as such")
	}
	if refuseKubernetesResourceRequest(secret("default", true), "zke-system") != nil {
		t.Fatal("a Secret request from the Secret API was refused")
	}
	// The flag says nothing about anything else: it is not a general override.
	events := &agentv1.ResourceRequest{
		Verb: agentv1.ResourceVerb_RESOURCE_VERB_LIST,
		Resource: &agentv1.GroupVersionResource{
			Version: "v1", Resource: "events",
		},
		Namespace:    "default",
		SecretAccess: true,
	}
	if refuseKubernetesResourceRequest(events, "zke-system") == nil {
		t.Fatal("the Secret flag allowed an Event request")
	}
}

func TestPodEvictionIsTheOnlyAllowedResourceSubresource(t *testing.T) {
	t.Parallel()

	request := func(resource, subresource string, access bool) *agentv1.ResourceRequest {
		return &agentv1.ResourceRequest{
			Verb:              agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
			Resource:          &agentv1.GroupVersionResource{Version: "v1", Resource: resource},
			Namespace:         "default",
			Name:              "api-123",
			Subresource:       subresource,
			PodEvictionAccess: access,
		}
	}
	eviction := request("pods", "eviction", true)
	if refusal := refuseKubernetesResourceRequest(eviction, "zke-system"); refusal != nil {
		t.Fatalf("dedicated Pod eviction was refused: %+v", refusal)
	}
	if refuseKubernetesResourceRequest(request("pods", "eviction", false), "zke-system") != refusalNotEnabled {
		t.Fatal("Pod eviction without the dedicated flag was allowed")
	}
	if refuseKubernetesResourceRequest(request("services", "eviction", true), "zke-system") != refusalNotEnabled {
		t.Fatal("eviction flag widened another resource subresource")
	}
	if refuseKubernetesResourceRequest(request("pods", "exec", true), "zke-system") != refusalNotEnabled {
		t.Fatal("eviction flag widened another Pod subresource")
	}
	validBody := []byte(`{"apiVersion":"policy/v1","kind":"Eviction","metadata":{"name":"api-123","namespace":"default"},"deleteOptions":{"preconditions":{"uid":"pod-uid"}}}`)
	if _, err := decodePodEvictionObject(eviction, validBody); err != nil {
		t.Fatalf("valid eviction body refused: %v", err)
	}
	withoutUID := []byte(`{"apiVersion":"policy/v1","kind":"Eviction","metadata":{"name":"api-123","namespace":"default"}}`)
	if _, err := decodePodEvictionObject(eviction, withoutUID); err == nil {
		t.Fatal("eviction without a Pod UID precondition was accepted")
	}
}
