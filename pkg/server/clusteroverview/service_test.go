package clusteroverview

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

const (
	overviewClusterID = "00000000-0000-4000-8000-000000000003"
	otherClusterID    = "00000000-0000-4000-8000-000000000004"
)

type fakeResourceReader struct {
	nodes      []kubernetesresource.NodeSummary
	namespaces []map[string]any
	pods       []map[string]any
	volumes    []map[string]any
	claims     []map[string]any
	workloads  map[kubernetesresource.WorkloadResource][]kubernetesresource.WorkloadSummary
	errors     map[string]error

	mutex sync.Mutex
	// Listings performed per section, so a test can tell a cache hit from a
	// second read of the Cluster.
	calls map[string]int
}

func (reader *fakeResourceReader) record(section string) {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	if reader.calls == nil {
		reader.calls = map[string]int{}
	}
	reader.calls[section]++
}

func (reader *fakeResourceReader) callCount(section string) int {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	return reader.calls[section]
}

func (reader *fakeResourceReader) ListNodes(
	_ context.Context,
	input kubernetesresource.ListNodesInput,
) (kubernetesresource.NodePage, error) {
	reader.record("nodes")
	if err := reader.errors["nodes"]; err != nil {
		return kubernetesresource.NodePage{}, err
	}
	return kubernetesresource.NodePage{Nodes: reader.nodes}, nil
}

func (reader *fakeResourceReader) ListResources(
	_ context.Context,
	input kubernetesresource.ListResourcesInput,
) (kubernetesresource.ResourcePage, error) {
	section := input.Resource.Resource
	reader.record(section)
	if err := reader.errors[section]; err != nil {
		return kubernetesresource.ResourcePage{}, err
	}
	items := map[string][]map[string]any{
		"namespaces":             reader.namespaces,
		"pods":                   reader.pods,
		"persistentvolumes":      reader.volumes,
		"persistentvolumeclaims": reader.claims,
	}[section]
	return kubernetesresource.ResourcePage{Items: items}, nil
}

func (reader *fakeResourceReader) ListWorkloads(
	_ context.Context,
	input kubernetesresource.ListWorkloadsInput,
) (kubernetesresource.WorkloadPage, error) {
	reader.record("workloads." + string(input.Resource))
	if input.Namespace != "" {
		return kubernetesresource.WorkloadPage{}, kubernetesresource.ErrInvalidInput
	}
	if err := reader.errors["workloads."+string(input.Resource)]; err != nil {
		return kubernetesresource.WorkloadPage{}, err
	}
	return kubernetesresource.WorkloadPage{Workloads: reader.workloads[input.Resource]}, nil
}

func TestServiceAggregatesClusterResourceState(t *testing.T) {
	t.Parallel()
	reader := &fakeResourceReader{
		nodes: []kubernetesresource.NodeSummary{
			{Status: "ready", KubernetesVersion: "v1.31.4", CPUCapacity: "4", CPUAllocatable: "3500m", MemoryCapacity: "8Gi", MemoryAllocatable: "7Gi", PodsCapacity: "110", PodsAllocatable: "100"},
			{Status: "not_ready", KubernetesVersion: "v1.30.8", Unschedulable: true, CPUCapacity: "2", CPUAllocatable: "1800m", MemoryCapacity: "4Gi", MemoryAllocatable: "3500Mi", PodsCapacity: "110", PodsAllocatable: "100"},
		},
		namespaces: []map[string]any{
			toUnstructured(t, &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: "default"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}}),
			toUnstructured(t, &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: "old", DeletionTimestamp: &metav1.Time{Time: time.Now()}}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating}}),
		},
		pods: []map[string]any{
			toUnstructured(t, overviewPod("running-a", corev1.PodRunning, true, "500m", "128Mi", "1", "64Mi")),
			toUnstructured(t, overviewPod("running-b", corev1.PodPending, false, "250m", "32Mi", "", "")),
			toUnstructured(t, overviewPod("complete", corev1.PodSucceeded, false, "2", "1Gi", "", "")),
		},
		volumes: []map[string]any{
			toUnstructured(t, overviewVolume("bound-a", corev1.VolumeBound, "20Gi")),
			toUnstructured(t, overviewVolume("released-a", corev1.VolumeReleased, "10Gi")),
		},
		claims: []map[string]any{
			toUnstructured(t, overviewClaim("bound-a", corev1.ClaimBound, "20Gi")),
			toUnstructured(t, overviewClaim("waiting", corev1.ClaimPending, "5Gi")),
		},
		workloads: map[kubernetesresource.WorkloadResource][]kubernetesresource.WorkloadSummary{
			kubernetesresource.WorkloadDeployments:  {{Status: "available"}},
			kubernetesresource.WorkloadStatefulSets: {{Status: "progressing"}},
			kubernetesresource.WorkloadDaemonSets:   {{Status: "available"}},
			kubernetesresource.WorkloadJobs:         {{Status: "completed"}},
			kubernetesresource.WorkloadCronJobs:     {{Status: "suspended"}},
		},
		errors: map[string]error{},
	}

	overview, err := NewService(reader).Get(context.Background(), overviewClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Partial || len(overview.Issues) != 0 || overview.GeneratedAt.IsZero() {
		t.Fatalf("unexpected overview metadata: %+v", overview)
	}
	if overview.Nodes.Total != 2 || overview.Nodes.StatusCounts["ready"] != 1 ||
		overview.Nodes.StatusCounts["not_ready"] != 1 || overview.Nodes.Unschedulable != 1 ||
		len(overview.Nodes.KubernetesVersions) != 2 ||
		overview.Nodes.KubernetesVersions["v1.31.4"] != 1 ||
		overview.Nodes.KubernetesVersions["v1.30.8"] != 1 {
		t.Fatalf("nodes = %+v", overview.Nodes)
	}
	if overview.Namespaces.Total != 2 || overview.Namespaces.StatusCounts["active"] != 1 ||
		overview.Namespaces.StatusCounts["terminating"] != 1 {
		t.Fatalf("namespaces = %+v", overview.Namespaces)
	}
	if overview.Pods.Total != 3 || overview.Pods.Ready != 1 || overview.Pods.NotReady != 1 ||
		overview.Pods.StatusCounts["succeeded"] != 1 {
		t.Fatalf("pods = %+v", overview.Pods)
	}
	if overview.Resources.CPUCapacityMillis != 6000 || overview.Resources.CPUAllocatableMillis != 5300 ||
		overview.Resources.MemoryCapacityBytes != 12*1024*1024*1024 ||
		overview.Resources.MemoryAllocatableBytes != (7*1024+3500)*1024*1024 ||
		overview.Resources.CPURequestedMillis != 1250 ||
		overview.Resources.MemoryRequestedBytes != 160*1024*1024 ||
		overview.Resources.NonTerminalPods != 2 || overview.Resources.PodCapacity != 220 {
		t.Fatalf("resources = %+v", overview.Resources)
	}
	if overview.Workloads.Total != 5 || overview.Workloads.StatusCounts["available"] != 2 ||
		overview.Workloads.StatusCounts["completed"] != 1 || len(overview.Workloads.ByResource) != 5 {
		t.Fatalf("workloads = %+v", overview.Workloads)
	}
	volumes := overview.Storage.PersistentVolumes
	claims := overview.Storage.PersistentVolumeClaims
	if volumes.Total != 2 || volumes.CapacityBytes != 30*1024*1024*1024 ||
		volumes.StatusCounts["bound"] != 1 || volumes.StatusCounts["released"] != 1 {
		t.Fatalf("persistent volumes = %+v", volumes)
	}
	if claims.Total != 2 || claims.RequestedBytes != 25*1024*1024*1024 ||
		claims.StatusCounts["bound"] != 1 || claims.StatusCounts["pending"] != 1 {
		t.Fatalf("persistent volume claims = %+v", claims)
	}
}

// A second read inside the window is answered from the snapshot rather than by
// listing the Cluster again: this is the page every operator lands on, and one
// overview is ten full listings.
func TestServiceServesCachedOverviewWithinTTL(t *testing.T) {
	t.Parallel()
	reader := &fakeResourceReader{
		nodes:     []kubernetesresource.NodeSummary{{Status: "ready"}},
		workloads: map[kubernetesresource.WorkloadResource][]kubernetesresource.WorkloadSummary{},
		errors:    map[string]error{},
	}
	service := NewService(reader, Config{CacheTTL: time.Minute})
	first, err := service.Get(context.Background(), overviewClusterID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Get(context.Background(), overviewClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if reader.callCount("nodes") != 1 || reader.callCount("pods") != 1 {
		t.Fatalf("cluster was listed again: %d node reads", reader.callCount("nodes"))
	}
	if !second.GeneratedAt.Equal(first.GeneratedAt) {
		t.Fatalf("generated_at = %v, want the cached %v", second.GeneratedAt, first.GeneratedAt)
	}
	// Two callers must not share the maps they were handed.
	second.Nodes.StatusCounts["ready"] = 99
	third, err := service.Get(context.Background(), overviewClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if third.Nodes.StatusCounts["ready"] != 1 {
		t.Fatalf("cached counts were reachable from a caller: %+v", third.Nodes)
	}
	// A different Cluster is a different snapshot.
	if _, err := service.Get(context.Background(), otherClusterID); err != nil {
		t.Fatal(err)
	}
	if reader.callCount("nodes") != 2 {
		t.Fatalf("second Cluster was answered from another Cluster's snapshot")
	}
}

// A failed section is never held: refresh is how an operator asks whether the
// section is still unavailable, and a cache that answers from the failure has
// turned that button off.
func TestServiceDoesNotCachePartialOverview(t *testing.T) {
	t.Parallel()
	reader := &fakeResourceReader{
		workloads: map[kubernetesresource.WorkloadResource][]kubernetesresource.WorkloadSummary{},
		errors:    map[string]error{"pods": kubernetesresource.ErrClusterTimeout},
	}
	service := NewService(reader, Config{CacheTTL: time.Minute})
	for round := 0; round < 2; round++ {
		overview, err := service.Get(context.Background(), overviewClusterID)
		if err != nil {
			t.Fatal(err)
		}
		if !overview.Partial {
			t.Fatalf("overview = %+v", overview)
		}
	}
	if reader.callCount("pods") != 2 {
		t.Fatalf("a failed section was cached: %d pod reads", reader.callCount("pods"))
	}
}

// A section that only hit its item ceiling is cached: re-reading a Cluster too
// large to count produces the same ceiling at the same cost.
func TestServiceCachesTruncatedOverview(t *testing.T) {
	t.Parallel()
	reader := &fakeResourceReader{
		nodes:     []kubernetesresource.NodeSummary{{Status: "ready"}, {Status: "ready"}},
		workloads: map[kubernetesresource.WorkloadResource][]kubernetesresource.WorkloadSummary{},
		errors:    map[string]error{},
	}
	service := NewService(reader, Config{MaxItemsPerSection: 1, CacheTTL: time.Minute})
	for round := 0; round < 2; round++ {
		if _, err := service.Get(context.Background(), overviewClusterID); err != nil {
			t.Fatal(err)
		}
	}
	if reader.callCount("nodes") != 1 {
		t.Fatalf("truncated overview was not cached: %d node reads", reader.callCount("nodes"))
	}
}

func TestServiceReadsClusterAgainWhenCacheIsDisabled(t *testing.T) {
	t.Parallel()
	reader := &fakeResourceReader{
		workloads: map[kubernetesresource.WorkloadResource][]kubernetesresource.WorkloadSummary{},
		errors:    map[string]error{},
	}
	service := NewService(reader, Config{CacheTTL: -1})
	for round := 0; round < 2; round++ {
		if _, err := service.Get(context.Background(), overviewClusterID); err != nil {
			t.Fatal(err)
		}
	}
	if reader.callCount("nodes") != 2 {
		t.Fatalf("cache was consulted while disabled: %d node reads", reader.callCount("nodes"))
	}
}

func TestServiceReturnsPartialOverviewWithStableSectionIssue(t *testing.T) {
	t.Parallel()
	reader := &fakeResourceReader{
		workloads: map[kubernetesresource.WorkloadResource][]kubernetesresource.WorkloadSummary{},
		errors: map[string]error{
			"workloads.deployments": kubernetesresource.ErrClusterAccessDenied,
		},
	}
	overview, err := NewService(reader).Get(context.Background(), overviewClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if !overview.Partial || len(overview.Issues) != 1 ||
		overview.Issues[0] != (SectionIssue{Section: "workloads.deployments", Code: "cluster_api_forbidden"}) {
		t.Fatalf("issues = %+v", overview.Issues)
	}
}

func TestServiceFailsWhenEverySectionFails(t *testing.T) {
	t.Parallel()
	allErrors := map[string]error{
		"nodes":                  kubernetesresource.ErrAgentNotConnected,
		"namespaces":             kubernetesresource.ErrAgentNotConnected,
		"pods":                   kubernetesresource.ErrAgentNotConnected,
		"persistentvolumes":      kubernetesresource.ErrAgentNotConnected,
		"persistentvolumeclaims": kubernetesresource.ErrAgentNotConnected,
	}
	for _, resourceName := range []kubernetesresource.WorkloadResource{
		kubernetesresource.WorkloadDeployments, kubernetesresource.WorkloadStatefulSets,
		kubernetesresource.WorkloadDaemonSets, kubernetesresource.WorkloadJobs,
		kubernetesresource.WorkloadCronJobs,
	} {
		allErrors["workloads."+string(resourceName)] = kubernetesresource.ErrAgentNotConnected
	}
	_, err := NewService(&fakeResourceReader{
		workloads: map[kubernetesresource.WorkloadResource][]kubernetesresource.WorkloadSummary{},
		errors:    allErrors,
	}).Get(context.Background(), overviewClusterID)
	if !errors.Is(err, kubernetesresource.ErrAgentNotConnected) {
		t.Fatalf("error = %v", err)
	}
}

func TestServiceMarksBoundedSectionAsPartial(t *testing.T) {
	t.Parallel()
	reader := &fakeResourceReader{
		nodes:     []kubernetesresource.NodeSummary{{Status: "ready"}, {Status: "ready"}},
		workloads: map[kubernetesresource.WorkloadResource][]kubernetesresource.WorkloadSummary{},
		errors:    map[string]error{},
	}
	overview, err := NewService(reader, Config{MaxItemsPerSection: 1}).Get(
		context.Background(), overviewClusterID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !overview.Partial || overview.Nodes.Total != 1 || len(overview.Issues) != 1 ||
		overview.Issues[0] != (SectionIssue{Section: "nodes", Code: "item_limit_reached"}) {
		t.Fatalf("overview = %+v", overview)
	}
}

func overviewPod(
	name string,
	phase corev1.PodPhase,
	ready bool,
	cpu string,
	memory string,
	initCPU string,
	initMemory string,
) *corev1.Pod {
	conditions := []corev1.PodCondition{}
	if ready {
		conditions = append(conditions, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue})
	}
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default", UID: types.UID(name + "-uid"),
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "main", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
		}}},
		Status: corev1.PodStatus{Phase: phase, Conditions: conditions},
	}
	if cpu != "" {
		pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if memory != "" {
		pod.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse(memory)
	}
	if initCPU != "" || initMemory != "" {
		requests := corev1.ResourceList{}
		if initCPU != "" {
			requests[corev1.ResourceCPU] = resource.MustParse(initCPU)
		}
		if initMemory != "" {
			requests[corev1.ResourceMemory] = resource.MustParse(initMemory)
		}
		pod.Spec.InitContainers = []corev1.Container{{
			Name: "init", Resources: corev1.ResourceRequirements{Requests: requests},
		}}
	}
	return pod
}

func overviewVolume(
	name string,
	phase corev1.PersistentVolumePhase,
	capacity string,
) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolume"},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid")},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(capacity)},
		},
		Status: corev1.PersistentVolumeStatus{Phase: phase},
	}
}

func overviewClaim(
	name string,
	phase corev1.PersistentVolumeClaimPhase,
	requested string,
) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default", UID: types.UID(name + "-claim-uid"),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(requested)},
			},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: phase},
	}
}

func toUnstructured(t *testing.T, object any) map[string]any {
	t.Helper()
	result, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
