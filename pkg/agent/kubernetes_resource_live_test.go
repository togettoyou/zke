package agent

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// TestLiveKubernetesResourceCRUDOverRealQUIC is opt-in because it mutates the
// currently selected Kubernetes cluster. Every object has a unique name and a
// direct-client cleanup fallback, so a failed protocol assertion does not leave
// the developer's cluster polluted.
func TestLiveKubernetesResourceCRUDOverRealQUIC(t *testing.T) {
	if os.Getenv("ZKE_LIVE_KUBERNETES_E2E") != "1" {
		t.Skip("set ZKE_LIVE_KUBERNETES_E2E=1 to use the current kubeconfig")
	}
	if runtime.GOOS == "windows" {
		t.Skip("real QUIC listener timing is not stable on Windows")
	}

	config, err := loadKubernetesConfig("")
	if err != nil {
		t.Fatal(err)
	}
	config.Timeout = 15 * time.Second
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	typedClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}

	suffix := strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	namespace := "zke-e2e-" + suffix
	group := "e2e-" + suffix + ".zke.io"
	crdName := "widgets." + group
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	namespaceGVR := schema.GroupVersionResource{
		Version: "v1", Resource: "namespaces",
	}
	crdGVR := schema.GroupVersionResource{
		Group: "apiextensions.k8s.io", Version: "v1",
		Resource: "customresourcedefinitions",
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(
			context.Background(),
			30*time.Second,
		)
		defer cleanupCancel()
		propagation := metav1.DeletePropagationBackground
		options := metav1.DeleteOptions{PropagationPolicy: &propagation}
		_ = dynamicClient.Resource(crdGVR).Delete(
			cleanupContext,
			crdName,
			options,
		)
		_ = dynamicClient.Resource(namespaceGVR).Delete(
			cleanupContext,
			namespace,
			options,
		)
		waitForLiveDeletion(
			t,
			cleanupContext,
			dynamicClient.Resource(crdGVR),
			crdName,
		)
		waitForLiveDeletion(
			t,
			cleanupContext,
			dynamicClient.Resource(namespaceGVR),
			namespace,
		)
	})

	limits := defaultResourceTestLimits()
	limits.resourceTimeout = 30 * time.Second
	environment := startResourceStreamEnvironment(
		t,
		newKubernetesResourceHandler(
			dynamicClient,
			typedClient.Discovery(),
			limits.maxBodyBytes,

			"zke-system",
		),
		limits,
	)
	service := kubernetesresource.NewService(environment.manager)

	createdNamespace, err := service.CreateNamespace(
		ctx,
		kubernetesresource.CreateNamespaceInput{
			ClusterID: testClusterID,
			Name:      namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "zke-e2e",
			},
			Confirm:        true,
			IdempotencyKey: liveKey("namespace-create", suffix),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	namespaceDetail, err := service.GetNamespace(ctx, testClusterID, namespace)
	if err != nil ||
		namespaceDetail.UID != createdNamespace.UID ||
		namespaceDetail.Labels["app.kubernetes.io/managed-by"] != "zke-e2e" {
		t.Fatalf("Namespace detail=%+v err=%v", namespaceDetail, err)
	}
	namespacePage, err := service.ListNamespaces(
		ctx,
		kubernetesresource.ListNamespacesInput{
			ClusterID:     testClusterID,
			Limit:         10,
			FieldSelector: "metadata.name=" + namespace,
		},
	)
	if err != nil ||
		len(namespacePage.Namespaces) != 1 ||
		namespacePage.Namespaces[0].Name != namespace {
		t.Fatalf("Namespace page=%+v err=%v", namespacePage, err)
	}

	configMapIdentity := kubernetesresource.ResourceIdentity{
		Version: "v1", Resource: "configmaps",
	}
	previewName := "preview"
	_, err = service.CreateResource(
		ctx,
		kubernetesresource.CreateResourceInput{
			ClusterID: testClusterID,
			Resource:  configMapIdentity,
			Namespace: namespace,
			Object: liveObject(
				"v1",
				"ConfigMap",
				namespace,
				previewName,
				map[string]any{"data": map[string]any{"mode": "preview"}},
			),
			Options:        kubernetesresource.MutationOptions{DryRun: true},
			IdempotencyKey: liveKey("configmap-dry-run", suffix),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dynamicClient.Resource(schema.GroupVersionResource{
		Version: "v1", Resource: "configmaps",
	}).Namespace(namespace).Get(ctx, previewName, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("dry-run ConfigMap lookup error = %v, want not found", err)
	}

	configMapName := "sample"
	createConfigMap := kubernetesresource.CreateResourceInput{
		ClusterID: testClusterID,
		Resource:  configMapIdentity,
		Namespace: namespace,
		Object: liveObject(
			"v1",
			"ConfigMap",
			namespace,
			configMapName,
			map[string]any{
				"metadata": map[string]any{
					"labels": map[string]any{"stage": "created"},
				},
				"data": map[string]any{"mode": "created"},
			},
		),
		Confirm:        true,
		IdempotencyKey: liveKey("configmap-create", suffix),
	}
	createdConfigMap, err := service.CreateResource(ctx, createConfigMap)
	if err != nil {
		t.Fatal(err)
	}
	replayedConfigMap, err := service.CreateResource(ctx, createConfigMap)
	if err != nil ||
		objectString(replayedConfigMap, "metadata", "uid") !=
			objectString(createdConfigMap, "metadata", "uid") {
		t.Fatalf("idempotent create replay object=%+v err=%v", replayedConfigMap, err)
	}

	createdConfigMap["data"] = map[string]any{"mode": "updated"}
	updatedConfigMap, err := service.UpdateResource(
		ctx,
		kubernetesresource.UpdateResourceInput{
			ClusterID:      testClusterID,
			Resource:       configMapIdentity,
			Namespace:      namespace,
			Name:           configMapName,
			Object:         createdConfigMap,
			Confirm:        true,
			IdempotencyKey: liveKey("configmap-update", suffix),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	patchedConfigMap, err := service.PatchResource(
		ctx,
		kubernetesresource.PatchResourceInput{
			ClusterID: testClusterID,
			Resource:  configMapIdentity,
			Namespace: namespace,
			Name:      configMapName,
			PatchType: agentv1.PatchType_PATCH_TYPE_MERGE,
			Patch:     []byte(`{"data":{"mode":"merge-patched"}}`),
			Confirm:   true,
			IdempotencyKey: liveKey(
				"configmap-merge-patch",
				suffix,
			),
		},
	)
	if err != nil ||
		objectString(patchedConfigMap, "data", "mode") != "merge-patched" {
		t.Fatalf("merge patch object=%+v err=%v", patchedConfigMap, err)
	}
	_, err = service.PatchResource(
		ctx,
		kubernetesresource.PatchResourceInput{
			ClusterID: testClusterID,
			Resource:  configMapIdentity,
			Namespace: namespace,
			Name:      configMapName,
			PatchType: agentv1.PatchType_PATCH_TYPE_JSON,
			Patch: []byte(
				`[{"op":"replace","path":"/metadata/labels/stage","value":"json-patched"}]`,
			),
			Confirm:        true,
			IdempotencyKey: liveKey("configmap-json-patch", suffix),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	appliedConfigMap := liveObject(
		"v1",
		"ConfigMap",
		namespace,
		configMapName,
		map[string]any{"data": map[string]any{"applied": "true"}},
	)
	_, err = service.PatchResource(
		ctx,
		kubernetesresource.PatchResourceInput{
			ClusterID: testClusterID,
			Resource:  configMapIdentity,
			Namespace: namespace,
			Name:      configMapName,
			PatchType: agentv1.PatchType_PATCH_TYPE_APPLY,
			Patch:     mustJSON(t, appliedConfigMap),
			Options: kubernetesresource.MutationOptions{
				FieldManager: "zke-e2e",
			},
			Confirm:        true,
			IdempotencyKey: liveKey("configmap-apply", suffix),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	staleConfigMap := updatedConfigMap
	staleConfigMap["data"] = map[string]any{"mode": "stale-update"}
	_, err = service.UpdateResource(
		ctx,
		kubernetesresource.UpdateResourceInput{
			ClusterID:      testClusterID,
			Resource:       configMapIdentity,
			Namespace:      namespace,
			Name:           configMapName,
			Object:         staleConfigMap,
			Confirm:        true,
			IdempotencyKey: liveKey("configmap-stale-update", suffix),
		},
	)
	if !errors.Is(err, kubernetesresource.ErrUpstreamConflict) {
		t.Fatalf("stale update error = %v, want conflict", err)
	}

	deploymentIdentity := kubernetesresource.ResourceIdentity{
		Group: "apps", Version: "v1", Resource: "deployments",
	}
	deploymentName := "sample"
	_, err = service.CreateResource(
		ctx,
		kubernetesresource.CreateResourceInput{
			ClusterID: testClusterID,
			Resource:  deploymentIdentity,
			Namespace: namespace,
			Object: liveObject(
				"apps/v1",
				"Deployment",
				namespace,
				deploymentName,
				map[string]any{
					"spec": map[string]any{
						"replicas": int64(0),
						"selector": map[string]any{
							"matchLabels": map[string]any{"app": "zke-e2e"},
						},
						"template": map[string]any{
							"metadata": map[string]any{
								"labels": map[string]any{"app": "zke-e2e"},
							},
							"spec": map[string]any{
								"containers": []any{map[string]any{
									"name":  "placeholder",
									"image": "registry.k8s.io/pause:3.10",
								}},
							},
						},
					},
				},
			),
			Confirm:        true,
			IdempotencyKey: liveKey("deployment-create", suffix),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.PatchResource(
		ctx,
		kubernetesresource.PatchResourceInput{
			ClusterID: testClusterID,
			Resource:  deploymentIdentity,
			Namespace: namespace,
			Name:      deploymentName,
			PatchType: agentv1.PatchType_PATCH_TYPE_STRATEGIC_MERGE,
			Patch: []byte(
				`{"metadata":{"labels":{"strategy":"strategic-merge"}}}`,
			),
			Confirm:        true,
			IdempotencyKey: liveKey("deployment-strategic", suffix),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	crdIdentity := kubernetesresource.ResourceIdentity{
		Group: "apiextensions.k8s.io", Version: "v1",
		Resource: "customresourcedefinitions",
	}
	_, err = service.CreateResource(
		ctx,
		kubernetesresource.CreateResourceInput{
			ClusterID: testClusterID,
			Resource:  crdIdentity,
			Object: map[string]any{
				"apiVersion": "apiextensions.k8s.io/v1",
				"kind":       "CustomResourceDefinition",
				"metadata":   map[string]any{"name": crdName},
				"spec": map[string]any{
					"group": group,
					"scope": "Namespaced",
					"names": map[string]any{
						"plural":   "widgets",
						"singular": "widget",
						"kind":     "Widget",
					},
					"versions": []any{map[string]any{
						"name":    "v1",
						"served":  true,
						"storage": true,
						"schema": map[string]any{
							"openAPIV3Schema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"spec": map[string]any{
										"type":                                 "object",
										"x-kubernetes-preserve-unknown-fields": true,
									},
								},
							},
						},
					}},
				},
			},
			Confirm:        true,
			IdempotencyKey: liveKey("crd-create", suffix),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	waitForCRDEstablished(t, ctx, dynamicClient, crdGVR, crdName)

	widgetIdentity := kubernetesresource.ResourceIdentity{
		Group: group, Version: "v1", Resource: "widgets",
	}
	waitForDiscoveredResource(t, ctx, service, widgetIdentity)
	widgetName := "sample"
	createdWidget, err := service.CreateResource(
		ctx,
		kubernetesresource.CreateResourceInput{
			ClusterID: testClusterID,
			Resource:  widgetIdentity,
			Namespace: namespace,
			Object: liveObject(
				group+"/v1",
				"Widget",
				namespace,
				widgetName,
				map[string]any{"spec": map[string]any{"size": "small"}},
			),
			Confirm:        true,
			IdempotencyKey: liveKey("widget-create", suffix),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	createdWidget["spec"] = map[string]any{"size": "medium"}
	_, err = service.UpdateResource(
		ctx,
		kubernetesresource.UpdateResourceInput{
			ClusterID:      testClusterID,
			Resource:       widgetIdentity,
			Namespace:      namespace,
			Name:           widgetName,
			Object:         createdWidget,
			Confirm:        true,
			IdempotencyKey: liveKey("widget-update", suffix),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	widget, err := service.PatchResource(
		ctx,
		kubernetesresource.PatchResourceInput{
			ClusterID: testClusterID,
			Resource:  widgetIdentity,
			Namespace: namespace,
			Name:      widgetName,
			PatchType: agentv1.PatchType_PATCH_TYPE_MERGE,
			Patch:     []byte(`{"spec":{"size":"large"}}`),
			Confirm:   true,
			IdempotencyKey: liveKey(
				"widget-patch",
				suffix,
			),
		},
	)
	if err != nil || objectString(widget, "spec", "size") != "large" {
		t.Fatalf("custom resource patch object=%+v err=%v", widget, err)
	}
	err = service.DeleteResource(
		ctx,
		kubernetesresource.DeleteResourceInput{
			ClusterID: testClusterID,
			Resource:  widgetIdentity,
			Namespace: namespace,
			Name:      widgetName,
			Confirm:   true,
			Preconditions: kubernetesresource.DeletePreconditions{
				UID: objectString(widget, "metadata", "uid"),
				ResourceVersion: objectString(
					widget,
					"metadata",
					"resourceVersion",
				),
			},
			IdempotencyKey: liveKey("widget-delete", suffix),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	currentConfigMap, err := service.GetResource(
		ctx,
		kubernetesresource.GetResourceInput{
			ClusterID: testClusterID,
			Resource:  configMapIdentity,
			Namespace: namespace,
			Name:      configMapName,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	err = service.DeleteResource(
		ctx,
		kubernetesresource.DeleteResourceInput{
			ClusterID: testClusterID,
			Resource:  configMapIdentity,
			Namespace: namespace,
			Name:      configMapName,
			Confirm:   true,
			Preconditions: kubernetesresource.DeletePreconditions{
				UID: objectString(currentConfigMap, "metadata", "uid"),
				ResourceVersion: objectString(
					currentConfigMap,
					"metadata",
					"resourceVersion",
				),
			},
			IdempotencyKey: liveKey("configmap-delete", suffix),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	currentNamespace, err := service.GetNamespace(ctx, testClusterID, namespace)
	if err != nil {
		t.Fatal(err)
	}
	err = service.DeleteNamespace(
		ctx,
		kubernetesresource.DeleteNamespaceInput{
			ClusterID:       testClusterID,
			Name:            namespace,
			UID:             currentNamespace.UID,
			ResourceVersion: currentNamespace.ResourceVersion,
			Confirm:         true,
			IdempotencyKey:  liveKey("namespace-delete", suffix),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func liveObject(
	apiVersion string,
	kind string,
	namespace string,
	name string,
	extra map[string]any,
) map[string]any {
	object := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
	}
	for key, value := range extra {
		if key == "metadata" {
			metadata, _ := value.(map[string]any)
			for metadataKey, metadataValue := range metadata {
				object["metadata"].(map[string]any)[metadataKey] = metadataValue
			}
			continue
		}
		object[key] = value
	}
	return object
}

func liveKey(prefix string, suffix string) string {
	value := prefix + "-" + suffix
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func objectString(object map[string]any, path ...string) string {
	var current any = object
	for _, segment := range path {
		container, _ := current.(map[string]any)
		current = container[segment]
	}
	value, _ := current.(string)
	return value
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	object := &unstructured.Unstructured{Object: value.(map[string]any)}
	body, err := object.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func waitForCRDEstablished(
	t *testing.T,
	ctx context.Context,
	client dynamic.Interface,
	gvr schema.GroupVersionResource,
	name string,
) {
	t.Helper()
	for {
		object, err := client.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			conditions, _, _ := unstructured.NestedSlice(
				object.Object,
				"status",
				"conditions",
			)
			for _, item := range conditions {
				condition, _ := item.(map[string]any)
				if condition["type"] == "Established" &&
					condition["status"] == "True" {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for CRD %q: %v", name, ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func waitForDiscoveredResource(
	t *testing.T,
	ctx context.Context,
	service *kubernetesresource.Service,
	expected kubernetesresource.ResourceIdentity,
) {
	t.Helper()
	for {
		catalog, err := service.DiscoverResources(ctx, testClusterID)
		if err == nil {
			for _, resource := range catalog.Resources {
				if resource.Group == expected.Group &&
					resource.Version == expected.Version &&
					resource.Resource == expected.Resource &&
					strings.Contains(
						strings.Join(resource.Verbs, ","),
						"create",
					) {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for resource discovery: %v", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func waitForLiveDeletion(
	t *testing.T,
	ctx context.Context,
	resource dynamic.ResourceInterface,
	name string,
) {
	t.Helper()
	for {
		_, err := resource.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return
		}
		if err != nil {
			t.Errorf("verify cleanup of %q: %v", name, err)
			return
		}
		select {
		case <-ctx.Done():
			t.Errorf("cleanup of %q did not finish: %v", name, ctx.Err())
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}
