package kubernetesresource

import (
	"context"
	"errors"
	"io"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestServiceClonesFixedWorkloadSnapshot(t *testing.T) {
	t.Parallel()

	source := cloneDeploymentObject()
	mutationCalls := 0
	requester := &fakeResourceRequester{
		handle: func(
			_ context.Context,
			clusterID string,
			request *agentv1.ResourceRequest,
			responseBody io.Writer,
		) (*agentv1.ResourceResponse, error) {
			if clusterID != testClusterID ||
				request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_GET ||
				request.GetResource().GetResource() != string(WorkloadDeployments) ||
				request.GetNamespace() != "default" ||
				request.GetName() != "source" {
				t.Fatalf("unexpected clone source request: cluster=%q request=%+v", clusterID, request)
			}
			return writeKubernetesObject(t, responseBody, &unstructured.Unstructured{Object: source}), nil
		},
		mutate: func(
			_ context.Context,
			clusterID string,
			request *agentv1.ResourceRequest,
			requestBody io.Reader,
			responseBody io.Writer,
			key string,
		) (*agentv1.ResourceResponse, error) {
			mutationCalls++
			if clusterID != testClusterID ||
				request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_CREATE ||
				request.GetNamespace() != "default" || request.GetName() != "" ||
				key != "0123456789abcdef" {
				t.Fatalf("unexpected clone create request: cluster=%q key=%q request=%+v", clusterID, key, request)
			}
			body, err := io.ReadAll(requestBody)
			if err != nil {
				t.Fatal(err)
			}
			var object unstructured.Unstructured
			if err := object.UnmarshalJSON(body); err != nil {
				t.Fatal(err)
			}
			object.SetUID(types.UID("copy-uid"))
			object.SetResourceVersion("1")
			return writeKubernetesObject(t, responseBody, &object), nil
		},
	}
	service := NewService(requester)
	input := CloneWorkloadInput{
		ClusterID:             testClusterID,
		Namespace:             "default",
		Resource:              WorkloadDeployments,
		SourceName:            "source",
		SourceUID:             "source-uid",
		SourceResourceVersion: "42",
		Name:                  "copy",
		Confirm:               true,
		IdempotencyKey:        "0123456789abcdef",
	}
	detail, err := service.CloneWorkload(context.Background(), input)
	if err != nil {
		t.Fatalf("CloneWorkload() error = %v", err)
	}
	if detail.Name != "copy" || detail.UID != "copy-uid" ||
		len(detail.Containers) != 1 || detail.Containers[0].Image != "example/app:v1" ||
		mutationCalls != 1 {
		t.Fatalf("CloneWorkload() detail=%+v mutation calls=%d", detail, mutationCalls)
	}

	input.SourceResourceVersion = "41"
	if _, err := service.CloneWorkload(context.Background(), input); !errors.Is(err, ErrUpstreamConflict) {
		t.Fatalf("CloneWorkload() stale source error = %v", err)
	}
	if mutationCalls != 1 {
		t.Fatalf("stale source reached create transport; mutation calls=%d", mutationCalls)
	}
}

func cloneDeploymentObject() map[string]any {
	return map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name": "source", "namespace": "default", "uid": "source-uid", "resourceVersion": "42",
		},
		"spec": map[string]any{
			"selector": map[string]any{
				"matchLabels": map[string]any{workloadSelectorLabel: "deployments.source"},
			},
			"template": map[string]any{
				"metadata": map[string]any{
					"finalizers": []any{"example.com/pod-cleanup"},
					"labels":     map[string]any{workloadSelectorLabel: "deployments.source"},
				},
				"spec": map[string]any{
					"serviceAccountName": "custom-account",
					"containers": []any{map[string]any{
						"name": "main", "image": "example/app:v1",
					}},
				},
			},
		},
	}
}

func TestCloneWorkloadObjectPreservesRawSpecAndRebuildsIdentity(t *testing.T) {
	t.Parallel()

	existing := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":              "source",
			"namespace":         "default",
			"uid":               "source-uid",
			"resourceVersion":   "42",
			"generation":        int64(7),
			"creationTimestamp": "2026-08-29T00:00:00Z",
			"ownerReferences":   []any{map[string]any{"uid": "owner"}},
			"finalizers":        []any{"example.com/cleanup"},
			"labels": map[string]any{
				workloadSelectorLabel: "deployments.source",
				"team":                "platform",
			},
			"annotations": map[string]any{
				"deployment.kubernetes.io/revision": "7",
				"example.com/kept":                  "yes",
			},
		},
		"spec": map[string]any{
			"selector": map[string]any{
				"matchLabels": map[string]any{workloadSelectorLabel: "deployments.source"},
			},
			"template": map[string]any{
				"metadata": map[string]any{
					"finalizers": []any{"example.com/pod-cleanup"},
					"labels": map[string]any{
						workloadSelectorLabel: "deployments.source",
						"app":                 "inference",
					},
					"annotations": map[string]any{
						workloadRestartAnnotation: "old-request",
						"example.com/kept":        "yes",
					},
				},
				"spec": map[string]any{
					"serviceAccountName": "custom-account",
					"hostNetwork":        true,
					"containers": []any{map[string]any{
						"name":  "main",
						"image": "example/app:v1",
						"securityContext": map[string]any{
							"runAsNonRoot": true,
						},
					}},
				},
			},
		},
		"status": map[string]any{"availableReplicas": int64(3)},
	}

	cloned, err := cloneWorkloadObject(existing, CloneWorkloadInput{
		Namespace: "default",
		Resource:  WorkloadDeployments,
		Name:      "copy",
	})
	if err != nil {
		t.Fatalf("cloneWorkloadObject() error = %v", err)
	}
	object := &unstructured.Unstructured{Object: cloned}
	if object.GetName() != "copy" || object.GetNamespace() != "default" {
		t.Fatalf("clone identity = %q/%q", object.GetNamespace(), object.GetName())
	}
	if object.GetUID() != "" || object.GetResourceVersion() != "" ||
		object.GetGeneration() != 0 || object.GetCreationTimestamp().Time.IsZero() == false ||
		len(object.GetOwnerReferences()) != 0 || len(object.GetFinalizers()) != 0 {
		t.Fatalf("server identity was retained: metadata=%+v", cloned["metadata"])
	}
	if _, found, _ := unstructured.NestedMap(cloned, "status"); found {
		t.Fatal("status was retained")
	}
	if object.GetLabels()[workloadSelectorLabel] != "" || object.GetLabels()["team"] != "platform" {
		t.Fatalf("object labels = %v", object.GetLabels())
	}

	selector, _, _ := unstructured.NestedStringMap(cloned, "spec", "selector", "matchLabels")
	templateLabels, _, _ := unstructured.NestedStringMap(
		cloned,
		"spec", "template", "metadata", "labels",
	)
	wantSelector := workloadSelectorValue(WorkloadDeployments, "copy")
	if selector[workloadSelectorLabel] != wantSelector ||
		templateLabels[workloadSelectorLabel] != wantSelector ||
		templateLabels["app"] != "inference" {
		t.Fatalf("selector=%v template labels=%v", selector, templateLabels)
	}
	serviceAccount, _, _ := unstructured.NestedString(
		cloned,
		"spec", "template", "spec", "serviceAccountName",
	)
	hostNetwork, _, _ := unstructured.NestedBool(cloned, "spec", "template", "spec", "hostNetwork")
	// Nested helpers cannot address array indexes, so inspect that field directly.
	containers, _, _ := unstructured.NestedSlice(cloned, "spec", "template", "spec", "containers")
	securityContext := containers[0].(map[string]any)["securityContext"].(map[string]any)
	runAsNonRoot, _ := securityContext["runAsNonRoot"].(bool)
	if serviceAccount != "custom-account" || !hostNetwork || !runAsNonRoot {
		t.Fatalf("raw pod spec was not preserved: %v", cloned["spec"])
	}
	templateAnnotations, _, _ := unstructured.NestedStringMap(
		cloned,
		"spec", "template", "metadata", "annotations",
	)
	if _, found := templateAnnotations[workloadRestartAnnotation]; found ||
		templateAnnotations["example.com/kept"] != "yes" {
		t.Fatalf("template annotations = %v", templateAnnotations)
	}
	templateFinalizers, _, _ := unstructured.NestedStringSlice(
		cloned,
		"spec", "template", "metadata", "finalizers",
	)
	if len(templateFinalizers) != 1 || templateFinalizers[0] != "example.com/pod-cleanup" {
		t.Fatalf("template finalizers = %v", templateFinalizers)
	}
}

func TestCloneWorkloadObjectClearsJobControllerIdentity(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name     string
		resource WorkloadResource
		existing map[string]any
		path     []string
	}{
		{
			name:     "job",
			resource: WorkloadJobs,
			existing: cloneJobObject(false),
			path:     []string{"spec", "template"},
		},
		{
			name:     "cron job",
			resource: WorkloadCronJobs,
			existing: cloneJobObject(true),
			path:     []string{"spec", "jobTemplate", "spec", "template"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			cloned, err := cloneWorkloadObject(testCase.existing, CloneWorkloadInput{
				Namespace: "default",
				Resource:  testCase.resource,
				Name:      "copy",
			})
			if err != nil {
				t.Fatalf("cloneWorkloadObject() error = %v", err)
			}
			var specPath []string
			if testCase.resource == WorkloadJobs {
				specPath = []string{"spec"}
			} else {
				specPath = []string{"spec", "jobTemplate", "spec"}
			}
			if _, found, _ := unstructured.NestedMap(cloned, append(specPath, "selector")...); found {
				t.Fatal("source Job selector was retained")
			}
			labelsPath := append(append([]string{}, testCase.path...), "metadata", "labels")
			labels, _, _ := unstructured.NestedStringMap(cloned, labelsPath...)
			for _, name := range []string{"job-name", "controller-uid", "batch.kubernetes.io/job-name", "batch.kubernetes.io/controller-uid"} {
				if _, found := labels[name]; found {
					t.Fatalf("controller label %q was retained: %v", name, labels)
				}
			}
			if labels[workloadSelectorLabel] != workloadSelectorValue(testCase.resource, "copy") {
				t.Fatalf("clone selector identity = %v", labels)
			}
		})
	}
}

func cloneJobObject(cron bool) map[string]any {
	template := map[string]any{
		"metadata": map[string]any{
			"labels": map[string]any{
				"job-name":                           "source",
				"controller-uid":                     "source-uid",
				"batch.kubernetes.io/job-name":       "source",
				"batch.kubernetes.io/controller-uid": "source-uid",
			},
		},
		"spec": map[string]any{
			"restartPolicy": "Never",
			"containers": []any{map[string]any{
				"name": "main", "image": "example/job:v1",
			}},
		},
	}
	jobSpec := map[string]any{
		"manualSelector": true,
		"selector": map[string]any{
			"matchLabels": map[string]any{"controller-uid": "source-uid"},
		},
		"template": template,
	}
	spec := jobSpec
	kind := "Job"
	apiVersion := "batch/v1"
	if cron {
		kind = "CronJob"
		spec = map[string]any{
			"schedule": "0 * * * *",
			"jobTemplate": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": "cleanup"}},
				"spec":     jobSpec,
			},
		}
	}
	return map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name": "source", "namespace": "default", "uid": "source-uid", "resourceVersion": "12",
		},
		"spec": spec,
	}
}

func TestValidateCloneWorkloadInputRejectsInvalidIdentityAndDestination(t *testing.T) {
	t.Parallel()

	base := CloneWorkloadInput{
		Namespace:             "default",
		Resource:              WorkloadDeployments,
		SourceName:            "source",
		SourceUID:             "source-uid",
		SourceResourceVersion: "12",
		Name:                  "copy",
	}
	for _, mutate := range []func(*CloneWorkloadInput){
		func(input *CloneWorkloadInput) { input.Name = input.SourceName },
		func(input *CloneWorkloadInput) { input.Name = "INVALID" },
		func(input *CloneWorkloadInput) { input.SourceUID = "" },
		func(input *CloneWorkloadInput) { input.SourceResourceVersion = "" },
	} {
		input := base
		mutate(&input)
		if validateCloneWorkloadInput(input) == nil {
			t.Fatalf("validateCloneWorkloadInput(%+v) succeeded", input)
		}
	}
}
