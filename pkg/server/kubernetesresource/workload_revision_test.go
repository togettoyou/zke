package kubernetesresource

import (
	"context"
	"errors"
	"io"
	"strconv"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	testRevisionNamespace = "model-serving"
	testWorkloadUID       = "00000000-0000-4000-8000-0000000000a1"
)

// A recorded revision history and the workload it belongs to, as the Agent
// would return them.
type revisionCluster struct {
	// The requests the service made, in order, so a test can assert what it
	// asked the Cluster for rather than only what it returned.
	listSelectors []string
	updates       []map[string]any
}

func (cluster *revisionCluster) requester(
	t *testing.T,
	workload map[string]any,
	resource string,
	history []map[string]any,
	historyResource string,
	historyKind string,
) *fakeResourceRequester {
	t.Helper()
	return &fakeResourceRequester{
		handle: func(
			_ context.Context,
			_ string,
			request *agentv1.ResourceRequest,
			responseBody io.Writer,
		) (*agentv1.ResourceResponse, error) {
			switch request.GetResource().GetResource() {
			case resource:
				return writeKubernetesObject(t, responseBody, workload), nil
			case historyResource:
				cluster.listSelectors = append(
					cluster.listSelectors,
					request.GetListOptions().GetLabelSelector(),
				)
				return writeKubernetesObject(t, responseBody, map[string]any{
					"apiVersion": "apps/v1",
					"kind":       historyKind + "List",
					"metadata":   map[string]any{"resourceVersion": "900"},
					"items":      toAnySlice(history),
				}), nil
			default:
				t.Fatalf("unexpected resource request: %+v", request)
				return nil, nil
			}
		},
		mutate: func(
			_ context.Context,
			_ string,
			request *agentv1.ResourceRequest,
			requestBody io.Reader,
			responseBody io.Writer,
			_ string,
		) (*agentv1.ResourceResponse, error) {
			if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_UPDATE {
				t.Fatalf("rollback used verb %v, want UPDATE", request.GetVerb())
			}
			raw, err := io.ReadAll(requestBody)
			if err != nil {
				t.Fatal(err)
			}
			// Decoded the way the Kubernetes API Server would, so integers stay
			// integers and the assertions read the object rather than JSON's
			// float64s.
			var body unstructured.Unstructured
			if err := body.UnmarshalJSON(raw); err != nil {
				t.Fatal(err)
			}
			cluster.updates = append(cluster.updates, body.Object)
			return writeKubernetesObject(t, responseBody, body.Object), nil
		},
	}
}

func TestServiceReadsDeploymentRevisionsFromOwnedReplicaSets(t *testing.T) {
	t.Parallel()

	cluster := &revisionCluster{}
	service := NewService(cluster.requester(
		t,
		testDeployment("1"),
		"deployments",
		[]map[string]any{
			testReplicaSet("inference-3", 3, "example/model:v3", "1000m", testWorkloadUID),
			testReplicaSet("inference-1", 1, "example/model:v1", "1", testWorkloadUID),
			testReplicaSet("inference-2", 2, "example/model:v2", "1", testWorkloadUID),
			// Another controller's ReplicaSet, matching the same labels. The
			// selector cannot tell it apart; the owner UID can.
			testReplicaSet("other-9", 9, "example/other:v9", "1", "00000000-0000-4000-8000-0000000000ff"),
		},
		"replicasets",
		"ReplicaSet",
	))

	page, err := service.ListWorkloadRevisions(
		context.Background(),
		ListWorkloadRevisionsInput{
			ClusterID: testClusterID,
			Namespace: testRevisionNamespace,
			Resource:  WorkloadDeployments,
			Name:      "inference",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if page.Truncated {
		t.Fatal("revision page reported truncated")
	}
	if len(cluster.listSelectors) != 1 || cluster.listSelectors[0] != "app=inference" {
		t.Fatalf("history list selectors = %q", cluster.listSelectors)
	}
	if len(page.Revisions) != 3 {
		t.Fatalf("revisions = %+v, want 3", page.Revisions)
	}
	if page.Revisions[0].Revision != 3 ||
		page.Revisions[1].Revision != 2 ||
		page.Revisions[2].Revision != 1 {
		t.Fatalf("revision order = %+v", page.Revisions)
	}
	// The running template writes the CPU request as `1` and revision 3 as
	// `1000m`. They are the same request, so revision 3 is the current one.
	if !page.Revisions[0].Current ||
		page.Revisions[1].Current ||
		page.Revisions[2].Current {
		t.Fatalf("current flags = %+v", page.Revisions)
	}
	if page.Revisions[0].Name != "inference-3" ||
		page.Revisions[0].ChangeCause != "image example/model:v3" ||
		len(page.Revisions[0].Images) != 1 ||
		page.Revisions[0].Images[0] != "example/model:v3" ||
		len(page.Revisions[0].Containers) != 1 ||
		page.Revisions[0].Containers[0].Name != "server" {
		t.Fatalf("newest revision = %+v", page.Revisions[0])
	}
}

func TestServiceRollsDeploymentBackToRecordedTemplateOnly(t *testing.T) {
	t.Parallel()

	cluster := &revisionCluster{}
	service := NewService(cluster.requester(
		t,
		testDeployment("1"),
		"deployments",
		[]map[string]any{
			testReplicaSet("inference-3", 3, "example/model:v3", "1000m", testWorkloadUID),
			testReplicaSet("inference-1", 1, "example/model:v1", "1", testWorkloadUID),
		},
		"replicasets",
		"ReplicaSet",
	))

	if _, err := service.RollbackWorkload(context.Background(), RollbackWorkloadInput{
		WorkloadMutationInput: WorkloadMutationInput{
			ClusterID:      testClusterID,
			Namespace:      testRevisionNamespace,
			Resource:       WorkloadDeployments,
			Name:           "inference",
			Confirm:        true,
			IdempotencyKey: "0123456789abcdef",
		},
		Revision:        1,
		UID:             testWorkloadUID,
		ResourceVersion: "512",
	}); err != nil {
		t.Fatal(err)
	}
	if len(cluster.updates) != 1 {
		t.Fatalf("update calls = %d, want 1", len(cluster.updates))
	}
	written := cluster.updates[0]
	containers, _, err := unstructured.NestedSlice(
		written, "spec", "template", "spec", "containers",
	)
	if err != nil || len(containers) != 1 {
		t.Fatalf("rolled back containers = %+v (%v)", containers, err)
	}
	container, _ := containers[0].(map[string]any)
	if container["image"] != "example/model:v1" {
		t.Fatalf("rolled back image = %v", container["image"])
	}
	// The ReplicaSet's own identity must not travel back up to the Deployment.
	if _, found, _ := unstructured.NestedString(
		written, "spec", "template", "metadata", "labels", podTemplateHashLabel,
	); found {
		t.Fatal("rollback carried the ReplicaSet pod-template-hash onto the Deployment")
	}
	// Everything outside the Pod template is what the revision never recorded.
	replicas, _, _ := unstructured.NestedInt64(written, "spec", "replicas")
	if replicas != 3 {
		t.Fatalf("rollback changed replicas to %d", replicas)
	}
	description, _, _ := unstructured.NestedString(
		written, "metadata", "annotations", "zke.io/description",
	)
	if description != "推理服务" {
		t.Fatalf("rollback changed the object annotations: %q", description)
	}
	// The write carries the resourceVersion the caller was reading, so the API
	// Server refuses it if anything landed in between.
	if version, _, _ := unstructured.NestedString(
		written, "metadata", "resourceVersion",
	); version != "512" {
		t.Fatalf("rollback resourceVersion = %q", version)
	}
}

func TestServiceRefusesRollbacksThatChangeNothingOrNameAnotherObject(t *testing.T) {
	t.Parallel()

	history := []map[string]any{
		testReplicaSet("inference-3", 3, "example/model:v3", "1000m", testWorkloadUID),
		testReplicaSet("inference-1", 1, "example/model:v1", "1", testWorkloadUID),
	}
	newService := func() *Service {
		cluster := &revisionCluster{}
		return NewService(cluster.requester(
			t,
			testDeployment("1"),
			"deployments",
			history,
			"replicasets",
			"ReplicaSet",
		))
	}
	base := RollbackWorkloadInput{
		WorkloadMutationInput: WorkloadMutationInput{
			ClusterID:      testClusterID,
			Namespace:      testRevisionNamespace,
			Resource:       WorkloadDeployments,
			Name:           "inference",
			Confirm:        true,
			IdempotencyKey: "0123456789abcdef",
		},
		Revision:        1,
		UID:             testWorkloadUID,
		ResourceVersion: "512",
	}

	current := base
	current.Revision = 3
	if _, err := newService().RollbackWorkload(
		context.Background(),
		current,
	); !errors.Is(err, ErrWorkloadRevisionUnchanged) {
		t.Fatalf("rollback to the running revision error = %v", err)
	}

	missing := base
	missing.Revision = 2
	if _, err := newService().RollbackWorkload(
		context.Background(),
		missing,
	); !errors.Is(err, ErrWorkloadRevisionNotFound) {
		t.Fatalf("rollback to a pruned revision error = %v", err)
	}

	stale := base
	stale.ResourceVersion = "511"
	if _, err := newService().RollbackWorkload(
		context.Background(),
		stale,
	); !errors.Is(err, ErrUpstreamConflict) {
		t.Fatalf("stale rollback error = %v", err)
	}

	renamed := base
	renamed.UID = "00000000-0000-4000-8000-0000000000ff"
	if _, err := newService().RollbackWorkload(
		context.Background(),
		renamed,
	); !errors.Is(err, ErrUpstreamConflict) {
		t.Fatalf("rollback against another object's UID error = %v", err)
	}
}

func TestServiceRefusesRevisionsForTypesWithoutHistory(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{
		handle: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Writer,
		) (*agentv1.ResourceResponse, error) {
			t.Fatal("a type without revision history reached the Cluster")
			return nil, nil
		},
		mutate: func(
			context.Context,
			string,
			*agentv1.ResourceRequest,
			io.Reader,
			io.Writer,
			string,
		) (*agentv1.ResourceResponse, error) {
			t.Fatal("a type without revision history reached the Cluster")
			return nil, nil
		},
	}
	service := NewService(requester)
	for _, resource := range []WorkloadResource{WorkloadJobs, WorkloadCronJobs} {
		if _, err := service.ListWorkloadRevisions(
			context.Background(),
			ListWorkloadRevisionsInput{
				ClusterID: testClusterID,
				Namespace: testRevisionNamespace,
				Resource:  resource,
				Name:      "batch",
			},
		); !errors.Is(err, ErrWorkloadRevisionsUnsupported) {
			t.Fatalf("%s revision list error = %v", resource, err)
		}
		if _, err := service.RollbackWorkload(
			context.Background(),
			RollbackWorkloadInput{
				WorkloadMutationInput: WorkloadMutationInput{
					ClusterID:      testClusterID,
					Namespace:      testRevisionNamespace,
					Resource:       resource,
					Name:           "batch",
					Confirm:        true,
					IdempotencyKey: "0123456789abcdef",
				},
				Revision:        1,
				UID:             testWorkloadUID,
				ResourceVersion: "512",
			},
		); !errors.Is(err, ErrWorkloadRevisionsUnsupported) {
			t.Fatalf("%s rollback error = %v", resource, err)
		}
	}
}

func TestServiceReadsStatefulSetRevisionsFromControllerRevisions(t *testing.T) {
	t.Parallel()

	cluster := &revisionCluster{}
	service := NewService(cluster.requester(
		t,
		testStatefulSet("example/store:v2"),
		"statefulsets",
		[]map[string]any{
			testControllerRevision("store-1", 1, "example/store:v1", testWorkloadUID),
			testControllerRevision("store-2", 2, "example/store:v2", testWorkloadUID),
		},
		"controllerrevisions",
		"ControllerRevision",
	))

	page, err := service.ListWorkloadRevisions(
		context.Background(),
		ListWorkloadRevisionsInput{
			ClusterID: testClusterID,
			Namespace: testRevisionNamespace,
			Resource:  WorkloadStatefulSets,
			Name:      "store",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Revisions) != 2 ||
		page.Revisions[0].Revision != 2 ||
		!page.Revisions[0].Current ||
		page.Revisions[1].Current {
		t.Fatalf("StatefulSet revisions = %+v", page.Revisions)
	}

	if _, err := service.RollbackWorkload(context.Background(), RollbackWorkloadInput{
		WorkloadMutationInput: WorkloadMutationInput{
			ClusterID:      testClusterID,
			Namespace:      testRevisionNamespace,
			Resource:       WorkloadStatefulSets,
			Name:           "store",
			Confirm:        true,
			IdempotencyKey: "0123456789abcdef",
		},
		Revision:        1,
		UID:             testWorkloadUID,
		ResourceVersion: "512",
	}); err != nil {
		t.Fatal(err)
	}
	written := cluster.updates[0]
	template, _, _ := unstructured.NestedMap(written, "spec", "template")
	// `$patch` instructs whoever applies a strategic merge patch; it is not a
	// field of a Pod template and must not be written onto the object.
	if _, found := template[strategicMergeDirective]; found {
		t.Fatalf("rollback wrote the strategic merge directive: %+v", template)
	}
	containers, _, _ := unstructured.NestedSlice(
		written, "spec", "template", "spec", "containers",
	)
	container, _ := containers[0].(map[string]any)
	if container["image"] != "example/store:v1" {
		t.Fatalf("rolled back image = %v", container["image"])
	}
}

func testDeployment(cpu string) map[string]any {
	return map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":            "inference",
			"namespace":       testRevisionNamespace,
			"uid":             testWorkloadUID,
			"resourceVersion": "512",
			"annotations":     map[string]any{"zke.io/description": "推理服务"},
		},
		"spec": map[string]any{
			"replicas": int64(3),
			"selector": map[string]any{
				"matchLabels": map[string]any{"app": "inference"},
			},
			"template": testRevisionPodTemplate("example/model:v3", cpu, ""),
		},
	}
}

func testStatefulSet(image string) map[string]any {
	return map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "StatefulSet",
		"metadata": map[string]any{
			"name":            "store",
			"namespace":       testRevisionNamespace,
			"uid":             testWorkloadUID,
			"resourceVersion": "512",
		},
		"spec": map[string]any{
			"replicas":    int64(2),
			"serviceName": "store",
			"selector": map[string]any{
				"matchLabels": map[string]any{"app": "inference"},
			},
			"template": testRevisionPodTemplate(image, "1", ""),
		},
	}
}

func testReplicaSet(
	name string,
	revision int64,
	image string,
	cpu string,
	ownerUID string,
) map[string]any {
	controller := true
	return map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "ReplicaSet",
		"metadata": map[string]any{
			"name":      name,
			"namespace": testRevisionNamespace,
			"uid":       name + "-uid",
			"annotations": map[string]any{
				deploymentRevisionAnnotation: strconv.FormatInt(revision, 10),
				changeCauseAnnotation:        "image " + image,
			},
			"ownerReferences": []any{map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"name":       "inference",
				"uid":        ownerUID,
				"controller": controller,
			}},
		},
		"spec": map[string]any{
			"template": testRevisionPodTemplate(image, cpu, strconv.FormatInt(revision, 10)+"abc"),
		},
	}
}

func testControllerRevision(
	name string,
	revision int64,
	image string,
	ownerUID string,
) map[string]any {
	template := testRevisionPodTemplate(image, "1", "")
	template[strategicMergeDirective] = "replace"
	return map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "ControllerRevision",
		"metadata": map[string]any{
			"name":      name,
			"namespace": testRevisionNamespace,
			"uid":       name + "-uid",
			"ownerReferences": []any{map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "StatefulSet",
				"name":       "store",
				"uid":        ownerUID,
				"controller": true,
			}},
		},
		"revision": revision,
		"data": map[string]any{
			"spec": map[string]any{"template": template},
		},
	}
}

func testRevisionPodTemplate(image string, cpu string, hash string) map[string]any {
	labels := map[string]any{"app": "inference"}
	if hash != "" {
		labels[podTemplateHashLabel] = hash
	}
	return map[string]any{
		"metadata": map[string]any{"labels": labels},
		"spec": map[string]any{
			"containers": []any{map[string]any{
				"name":  "server",
				"image": image,
				"resources": map[string]any{
					"requests": map[string]any{"cpu": cpu},
				},
			}},
		},
	}
}

func toAnySlice(items []map[string]any) []any {
	result := make([]any, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	return result
}
