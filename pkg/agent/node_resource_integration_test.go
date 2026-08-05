package agent

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestNodeResourceDynamicClientOverRealQUIC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real QUIC listener timing is not stable on Windows")
	}

	client := dynamicfake.NewSimpleDynamicClient(
		k8sruntime.NewScheme(),
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Node",
			"metadata": map[string]any{
				"name": "worker-a",
				"uid":  "worker-a-uid",
				"labels": map[string]any{
					"pool": "gpu",
				},
			},
			"status": map[string]any{
				"addresses": []any{map[string]any{
					"type":    "InternalIP",
					"address": "10.0.0.10",
				}},
			},
		}},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Node",
			"metadata": map[string]any{
				"name": "worker-b",
				"uid":  "worker-b-uid",
				"labels": map[string]any{
					"pool": "cpu",
				},
			},
		}},
	)
	limits := defaultResourceTestLimits()
	environment := startResourceStreamEnvironment(
		t,
		newKubernetesResourceHandler(client, nil, limits.maxBodyBytes, "zke-system"),
		limits,
	)
	service := kubernetesresource.NewService(environment.manager)

	ctx, cancel := context.WithTimeout(environment.ctx, 3*time.Second)
	defer cancel()
	page, err := service.ListNodes(ctx, kubernetesresource.ListNodesInput{
		ClusterID:     testClusterID,
		Limit:         50,
		LabelSelector: "pool=gpu",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Nodes) != 1 ||
		page.Nodes[0].Name != "worker-a" ||
		page.Nodes[0].UID != "worker-a-uid" ||
		page.Nodes[0].InternalIP != "10.0.0.10" {
		t.Fatalf("unexpected Node list over QUIC: %+v", page)
	}

	detail, err := service.GetNode(ctx, testClusterID, "worker-b")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Name != "worker-b" || detail.UID != "worker-b-uid" {
		t.Fatalf("unexpected Node detail over QUIC: %+v", detail)
	}

	_, err = service.GetNode(ctx, testClusterID, "missing-node")
	if !errors.Is(err, kubernetesresource.ErrNodeNotFound) {
		t.Fatalf("missing Node error = %v, want not found", err)
	}
}

func TestCustomResourceDiscoveryListAndGetOverRealQUIC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real QUIC listener timing is not stable on Windows")
	}

	widgetGVR := schema.GroupVersionResource{
		Group:    "example.io",
		Version:  "v1alpha1",
		Resource: "widgets",
	}
	// Discovery also lists CustomResourceDefinitions, because Kubernetes
	// discovery does not say which resources come from a CRD. The fake dynamic
	// client panics on a list kind it was not given — where a real cluster would
	// return an error the Agent handles — so the CRD list kind has to be
	// registered here even though only its objects are of interest.
	definitionGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}
	listKinds := map[schema.GroupVersionResource]string{
		widgetGVR:     "WidgetList",
		definitionGVR: "CustomResourceDefinitionList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		k8sruntime.NewScheme(),
		listKinds,
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       "CustomResourceDefinition",
			"metadata":   map[string]any{"name": "widgets.example.io"},
			"spec": map[string]any{
				"group": "example.io",
				"names": map[string]any{"plural": "widgets", "kind": "Widget"},
			},
		}},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "example.io/v1alpha1",
			"kind":       "Widget",
			"metadata": map[string]any{
				"name":      "widget-a",
				"namespace": "tenant-a",
			},
			"spec": map[string]any{"size": "large"},
		}},
	)
	fakeDiscovery := &discoveryfake.FakeDiscovery{
		Fake: &k8stesting.Fake{
			Resources: []*metav1.APIResourceList{{
				GroupVersion: "example.io/v1alpha1",
				APIResources: []metav1.APIResource{{
					Name:       "widgets",
					Kind:       "Widget",
					Namespaced: true,
					Verbs:      metav1.Verbs{"get", "list"},
				}},
			}},
		},
	}
	limits := defaultResourceTestLimits()
	environment := startResourceStreamEnvironment(
		t,
		newKubernetesResourceHandler(
			client,
			fakeDiscovery,
			limits.maxBodyBytes,

			"zke-system",
		),
		limits,
	)
	service := kubernetesresource.NewService(environment.manager)
	ctx, cancel := context.WithTimeout(environment.ctx, 3*time.Second)
	defer cancel()

	catalog, err := service.DiscoverResources(ctx, testClusterID)
	if err != nil {
		t.Fatal(err)
	}
	// The CRD read is what makes the marking meaningful, so it is asserted here
	// rather than only in the unit test: over the real stream the catalog has to
	// arrive with both the resource and the fact that a CRD provides it.
	if len(catalog.Resources) != 1 ||
		catalog.Resources[0].Resource != "widgets" ||
		!catalog.CustomResourcesKnown ||
		!catalog.Resources[0].CustomResource {
		t.Fatalf("unexpected discovery catalog over QUIC: %+v", catalog)
	}

	identity := kubernetesresource.ResourceIdentity{
		Group:    widgetGVR.Group,
		Version:  widgetGVR.Version,
		Resource: widgetGVR.Resource,
	}
	page, err := service.ListResources(
		ctx,
		kubernetesresource.ListResourcesInput{
			ClusterID: testClusterID,
			Resource:  identity,
			Namespace: "tenant-a",
			Limit:     50,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("unexpected custom resource list over QUIC: %+v", page)
	}
	detail, err := service.GetResource(
		ctx,
		kubernetesresource.GetResourceInput{
			ClusterID: testClusterID,
			Resource:  identity,
			Namespace: "tenant-a",
			Name:      "widget-a",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := detail["metadata"].(map[string]any)
	if metadata["name"] != "widget-a" {
		t.Fatalf("unexpected custom resource detail over QUIC: %+v", detail)
	}
}

func TestCustomResourceCRUDOverRealQUIC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real QUIC listener timing is not stable on Windows")
	}

	widgetGVR := schema.GroupVersionResource{
		Group:    "example.io",
		Version:  "v1alpha1",
		Resource: "widgets",
	}
	client := dynamicfake.NewSimpleDynamicClient(
		k8sruntime.NewScheme(),
	)
	limits := defaultResourceTestLimits()
	environment := startResourceStreamEnvironment(
		t,
		newKubernetesResourceHandler(client, nil, limits.maxBodyBytes, "zke-system"),
		limits,
	)
	service := kubernetesresource.NewService(environment.manager)
	ctx, cancel := context.WithTimeout(environment.ctx, 5*time.Second)
	defer cancel()
	identity := kubernetesresource.ResourceIdentity{
		Group:    widgetGVR.Group,
		Version:  widgetGVR.Version,
		Resource: widgetGVR.Resource,
	}
	object := map[string]any{
		"apiVersion": "example.io/v1alpha1",
		"kind":       "Widget",
		"metadata": map[string]any{
			"name":      "widget-a",
			"namespace": "tenant-a",
		},
		"spec": map[string]any{"size": "small"},
	}
	created, err := service.CreateResource(
		ctx,
		kubernetesresource.CreateResourceInput{
			ClusterID:      testClusterID,
			Resource:       identity,
			Namespace:      "tenant-a",
			Object:         object,
			Confirm:        true,
			IdempotencyKey: "create-widget-0001",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	metadata := created["metadata"].(map[string]any)
	metadata["resourceVersion"] = "1"
	created["spec"] = map[string]any{"size": "medium"}
	updated, err := service.UpdateResource(
		ctx,
		kubernetesresource.UpdateResourceInput{
			ClusterID:      testClusterID,
			Resource:       identity,
			Namespace:      "tenant-a",
			Name:           "widget-a",
			Object:         created,
			Confirm:        true,
			IdempotencyKey: "update-widget-0001",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated["spec"].(map[string]any)["size"] != "medium" {
		t.Fatalf("unexpected updated object: %+v", updated)
	}
	patched, err := service.PatchResource(
		ctx,
		kubernetesresource.PatchResourceInput{
			ClusterID:      testClusterID,
			Resource:       identity,
			Namespace:      "tenant-a",
			Name:           "widget-a",
			PatchType:      agentv1.PatchType_PATCH_TYPE_MERGE,
			Patch:          []byte(`{"spec":{"size":"large"}}`),
			Confirm:        true,
			IdempotencyKey: "patch-widget-00001",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if patched["spec"].(map[string]any)["size"] != "large" {
		t.Fatalf("unexpected patched object: %+v", patched)
	}
	err = service.DeleteResource(
		ctx,
		kubernetesresource.DeleteResourceInput{
			ClusterID:      testClusterID,
			Resource:       identity,
			Namespace:      "tenant-a",
			Name:           "widget-a",
			Confirm:        true,
			IdempotencyKey: "delete-widget-0001",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.GetResource(
		ctx,
		kubernetesresource.GetResourceInput{
			ClusterID: testClusterID,
			Resource:  identity,
			Namespace: "tenant-a",
			Name:      "widget-a",
		},
	)
	if !errors.Is(err, kubernetesresource.ErrResourceNotFound) {
		t.Fatalf("deleted resource error = %v, want not found", err)
	}
}
