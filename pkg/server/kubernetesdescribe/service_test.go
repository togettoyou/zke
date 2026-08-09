package kubernetesdescribe

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/resourcewatch"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const testClusterID = "0c2ba9a5-15fb-4bfa-9fbe-7f43a2ba8a53"

type fakeResourceAccess struct {
	networking            kubernetesresource.NetworkingResourceDetail
	networkingErr         error
	networkingPage        kubernetesresource.NetworkingResourcePage
	networkingListErr     error
	networkingListInput   kubernetesresource.ListNetworkingResourcesInput
	node                  kubernetesresource.NodeDetail
	nodeErr               error
	nodePods              []kubernetesresource.NodePodDetail
	nodePodsErr           error
	nodePodsMore          bool
	nodePodsInput         kubernetesresource.ListPodsInput
	pod                   kubernetesresource.PodDetail
	podErr                error
	podDetails            []kubernetesresource.PodDetail
	podListErr            error
	podListInput          kubernetesresource.ListPodsInput
	workload              kubernetesresource.WorkloadDetail
	workloadErr           error
	workloadResource      kubernetesresource.WorkloadResource
	workloadName          string
	autoscaler            kubernetesresource.HorizontalPodAutoscalerDetail
	autoscalerErr         error
	verticalAutoscaler    kubernetesresource.VPADetail
	verticalAutoscalerErr error
	kedaScaledObject      kubernetesresource.KEDAScaledObjectDetail
	kedaScaledObjectErr   error
	policy                kubernetesresource.PolicyResourceDetail
	policyErr             error
	claims                map[string]kubernetesresource.StorageResourceDetail
	claimErr              map[string]error
	// Keyed by the resource name of the listed type, e.g. `replicasets`.
	lists     map[string]kubernetesresource.ResourcePage
	listErr   error
	listInput map[string]kubernetesresource.ListResourcesInput
	object    map[string]any
	objectErr error
	lastGet   kubernetesresource.GetResourceInput
}

func (access *fakeResourceAccess) ListNetworkingResources(
	_ context.Context,
	input kubernetesresource.ListNetworkingResourcesInput,
) (kubernetesresource.NetworkingResourcePage, error) {
	access.networkingListInput = input
	return access.networkingPage, access.networkingListErr
}

func (access *fakeResourceAccess) GetNetworkingResource(
	_ context.Context,
	_ string,
	_ string,
	_ kubernetesresource.NetworkingResource,
	_ string,
) (kubernetesresource.NetworkingResourceDetail, error) {
	return access.networking, access.networkingErr
}

func (access *fakeResourceAccess) GetNode(
	_ context.Context,
	_ string,
	_ string,
) (kubernetesresource.NodeDetail, error) {
	return access.node, access.nodeErr
}

func (access *fakeResourceAccess) ListNodePodDetails(
	_ context.Context,
	input kubernetesresource.ListPodsInput,
) ([]kubernetesresource.NodePodDetail, bool, error) {
	access.nodePodsInput = input
	return access.nodePods, access.nodePodsMore, access.nodePodsErr
}

func (access *fakeResourceAccess) GetPod(
	_ context.Context,
	_ string,
	_ string,
	_ string,
) (kubernetesresource.PodDetail, error) {
	return access.pod, access.podErr
}

func (access *fakeResourceAccess) ListPodDetails(
	_ context.Context,
	input kubernetesresource.ListPodsInput,
) ([]kubernetesresource.PodDetail, bool, error) {
	access.podListInput = input
	return access.podDetails, false, access.podListErr
}

func (access *fakeResourceAccess) GetWorkload(
	_ context.Context,
	_ string,
	_ string,
	resource kubernetesresource.WorkloadResource,
	name string,
) (kubernetesresource.WorkloadDetail, error) {
	access.workloadResource = resource
	access.workloadName = name
	return access.workload, access.workloadErr
}

func (access *fakeResourceAccess) GetHorizontalPodAutoscaler(
	_ context.Context,
	_ string,
	_ string,
	_ string,
) (kubernetesresource.HorizontalPodAutoscalerDetail, error) {
	return access.autoscaler, access.autoscalerErr
}

func (access *fakeResourceAccess) GetVerticalPodAutoscaler(
	_ context.Context,
	_ string,
	_ string,
	_ string,
) (kubernetesresource.VPADetail, error) {
	return access.verticalAutoscaler, access.verticalAutoscalerErr
}

func (access *fakeResourceAccess) GetKEDAScaledObject(
	_ context.Context,
	_ string,
	_ string,
	_ string,
) (kubernetesresource.KEDAScaledObjectDetail, error) {
	return access.kedaScaledObject, access.kedaScaledObjectErr
}

func (access *fakeResourceAccess) GetPolicyResource(
	_ context.Context,
	_ string,
	_ string,
	_ kubernetesresource.PolicyResource,
	_ string,
) (kubernetesresource.PolicyResourceDetail, error) {
	return access.policy, access.policyErr
}

func (access *fakeResourceAccess) GetStorageResource(
	_ context.Context,
	_ string,
	_ string,
	_ kubernetesresource.StorageResource,
	name string,
) (kubernetesresource.StorageResourceDetail, error) {
	if err := access.claimErr[name]; err != nil {
		return kubernetesresource.StorageResourceDetail{}, err
	}
	return access.claims[name], nil
}

func (access *fakeResourceAccess) ListResources(
	_ context.Context,
	input kubernetesresource.ListResourcesInput,
) (kubernetesresource.ResourcePage, error) {
	if access.listInput == nil {
		access.listInput = map[string]kubernetesresource.ListResourcesInput{}
	}
	access.listInput[input.Resource.Resource] = input
	if access.listErr != nil {
		return kubernetesresource.ResourcePage{}, access.listErr
	}
	return access.lists[input.Resource.Resource], nil
}

func (access *fakeResourceAccess) GetResource(
	_ context.Context,
	input kubernetesresource.GetResourceInput,
) (map[string]any, error) {
	access.lastGet = input
	return access.object, access.objectErr
}

type fakeEventSource struct {
	events []corev1.Event
	// Events per object UID, for the describes that read more than one object.
	// A UID with no entry here falls back to `events`.
	byUID map[string][]corev1.Event
	// UIDs whose read fails, for the partial-failure paths.
	failUID   map[string]error
	truncated bool
	err       error
	called    bool
	mu        sync.Mutex
	requested []string
	input     resourcewatch.Input
}

func (source *fakeEventSource) Stream(
	_ context.Context,
	input resourcewatch.Input,
	sink agentprotocol.ResourceWatchSink,
) (resourcewatch.Result, error) {
	source.mu.Lock()
	source.called = true
	source.input = input
	source.requested = append(source.requested, input.ResourceUID)
	events := source.events
	if scoped, exists := source.byUID[input.ResourceUID]; exists {
		events = scoped
	} else if source.byUID != nil {
		events = nil
	}
	err := source.err
	if scoped, exists := source.failUID[input.ResourceUID]; exists {
		err = scoped
	}
	source.mu.Unlock()
	if err != nil {
		return resourcewatch.Result{}, err
	}
	if err := sink.Start(&agentv1.ResourceWatchResponse{
		Result:                 agentv1.ResultCode_RESULT_CODE_OK,
		ResourceVersion:        "120",
		InitialEventsTruncated: source.truncated,
	}); err != nil {
		return resourcewatch.Result{}, err
	}
	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			return resourcewatch.Result{}, err
		}
		if err := sink.Event(&agentv1.ResourceWatchEvent{
			Type:   agentv1.ResourceWatchEventType_RESOURCE_WATCH_EVENT_TYPE_ADDED,
			Object: payload,
		}); err != nil {
			return resourcewatch.Result{}, err
		}
	}
	return resourcewatch.Result{ResourceVersion: "120"}, nil
}

func testPodDetail() kubernetesresource.PodDetail {
	return kubernetesresource.PodDetail{
		PodSummary: kubernetesresource.PodSummary{
			APIVersion:      "v1",
			Kind:            "Pod",
			Namespace:       "model-serving",
			Name:            "inference-0",
			UID:             "6f0f6d55-0c0e-4a3f-9a2d-8c6a1f0a9d11",
			ResourceVersion: "118",
			Phase:           "Pending",
		},
		Containers: []kubernetesresource.PodContainer{{Name: "server"}},
		Conditions: []kubernetesresource.PodCondition{},
	}
}

func clusterEvent(
	uid string,
	reason string,
	message string,
	at time.Time,
) corev1.Event {
	return corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inference-0." + uid,
			Namespace: "model-serving",
			UID:       types.UID("event-" + uid),
		},
		InvolvedObject: corev1.ObjectReference{
			Kind:      "Pod",
			Name:      "inference-0",
			Namespace: "model-serving",
		},
		Type:          "Warning",
		Reason:        reason,
		Message:       message,
		Count:         1,
		Source:        corev1.EventSource{Component: "default-scheduler"},
		LastTimestamp: metav1.NewTime(at),
	}
}

func TestDescribePodJoinsTheObjectWithItsOwnEvents(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	pod := testPodDetail()
	newest := clusterEvent("b", "FailedScheduling", "0/5 nodes are available", base.Add(time.Minute))
	oldest := clusterEvent("a", "Scheduled", "assigned", base)
	access := &fakeResourceAccess{pod: pod}
	// Reported newest first, the way a Kubernetes list may return them.
	events := &fakeEventSource{events: []corev1.Event{newest, oldest}}
	service := NewService(access, events, Config{})

	result, err := service.DescribePod(context.Background(), PodInput{
		ClusterID: testClusterID,
		Namespace: "model-serving",
		Name:      "inference-0",
	})
	if err != nil {
		t.Fatal(err)
	}

	if result.Family != FamilyPod || result.Pod == nil {
		t.Fatalf("unexpected family projection: %+v", result)
	}
	if result.Target.UID != pod.UID ||
		result.Target.Kind != "Pod" ||
		result.Target.ResourceVersion != "118" {
		t.Fatalf("unexpected target: %+v", result.Target)
	}
	if events.input.ResourceUID != pod.UID ||
		events.input.Namespace != "model-serving" ||
		events.input.ClusterID != testClusterID ||
		events.input.Follow ||
		!events.input.IncludeInitial ||
		events.input.InitialLimit != DefaultEventLimit {
		t.Fatalf("Events were not scoped to the object: %+v", events.input)
	}
	if len(result.Events.Items) != 2 ||
		result.Events.Items[0].Reason != "Scheduled" ||
		result.Events.Items[1].Reason != "FailedScheduling" {
		t.Fatalf("Events are not oldest first: %+v", result.Events.Items)
	}
	if result.Events.Items[1].Source != "default-scheduler" {
		t.Fatalf("unexpected Event source: %+v", result.Events.Items[1])
	}
	if result.Events.Truncated || result.Events.Omitted != "" {
		t.Fatalf("unexpected Event section state: %+v", result.Events)
	}
	if len(result.DegradedSections) != 0 {
		t.Fatalf("unexpected degraded sections: %v", result.DegradedSections)
	}
}

func TestDescribePodReadsTheContainerAnEventNames(t *testing.T) {
	t.Parallel()

	event := clusterEvent("a", "BackOff", "Back-off restarting failed container",
		time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC))
	event.InvolvedObject.FieldPath = "spec.containers{server}"
	service := NewService(
		&fakeResourceAccess{pod: testPodDetail()},
		&fakeEventSource{events: []corev1.Event{event}},
		Config{},
	)

	result, err := service.DescribePod(context.Background(), PodInput{
		ClusterID: testClusterID,
		Namespace: "model-serving",
		Name:      "inference-0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events.Items) != 1 ||
		result.Events.Items[0].Container != "server" {
		t.Fatalf("container was not read from the field path: %+v", result.Events.Items)
	}
}

// The object half of a describe is worth returning without the Events, and the
// gap has to be visible: an empty Event list that silently meant "the read
// failed" reads as "nothing happened to this Pod".
func TestDescribePodReportsAnEventReadThatFailed(t *testing.T) {
	t.Parallel()

	pod := testPodDetail()
	pod.Conditions = []kubernetesresource.PodCondition{{
		Type:    "PodScheduled",
		Status:  "False",
		Reason:  "Unschedulable",
		Message: "0/5 nodes are available: 3 Insufficient cpu.",
	}}
	service := NewService(
		&fakeResourceAccess{pod: pod},
		&fakeEventSource{err: resourcewatch.ErrAgentNotConnected},
		Config{},
	)

	result, err := service.DescribePod(context.Background(), PodInput{
		ClusterID: testClusterID,
		Namespace: "model-serving",
		Name:      "inference-0",
	})
	if err != nil {
		t.Fatalf("a failed Event read must not fail the describe: %v", err)
	}
	if result.Events.Omitted != EventsOmittedUnavailable ||
		len(result.Events.Items) != 0 {
		t.Fatalf("unexpected Event section: %+v", result.Events)
	}
	if len(result.DegradedSections) != 1 ||
		result.DegradedSections[0] != "events" {
		t.Fatalf("unexpected degraded sections: %v", result.DegradedSections)
	}
	// The findings that come from the object's own status still hold.
	if len(result.Findings) != 1 ||
		result.Findings[0].Code != FindingPodUnschedulable {
		t.Fatalf("unexpected findings: %+v", result.Findings)
	}
}

func TestDescribePodBoundsTheEventWindow(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	stored := make([]corev1.Event, 0, 4)
	for index := range 4 {
		stored = append(stored, clusterEvent(
			string(rune('a'+index)),
			"BackOff",
			"Back-off restarting failed container",
			base.Add(time.Duration(index)*time.Minute),
		))
	}
	events := &fakeEventSource{events: stored}
	service := NewService(
		&fakeResourceAccess{pod: testPodDetail()},
		events,
		Config{EventLimit: 2},
	)

	result, err := service.DescribePod(context.Background(), PodInput{
		ClusterID: testClusterID,
		Namespace: "model-serving",
		Name:      "inference-0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if events.input.InitialLimit != 2 {
		t.Fatalf("the limit was not pushed to the Cluster: %+v", events.input)
	}
	if len(result.Events.Items) != 2 || !result.Events.Truncated {
		t.Fatalf("unexpected Event window: %+v", result.Events)
	}
}

func TestDescribePodPropagatesAFailedObjectRead(t *testing.T) {
	t.Parallel()

	events := &fakeEventSource{}
	service := NewService(
		&fakeResourceAccess{podErr: kubernetesresource.ErrResourceNotFound},
		events,
		Config{},
	)

	_, err := service.DescribePod(context.Background(), PodInput{
		ClusterID: testClusterID,
		Namespace: "model-serving",
		Name:      "inference-0",
	})
	if !errors.Is(err, kubernetesresource.ErrResourceNotFound) {
		t.Fatalf("unexpected error: %v", err)
	}
	if events.called {
		t.Fatal("Events were read for an object that was not found")
	}
}

func TestDescribeResourceCarriesEventsAndNoFindings(t *testing.T) {
	t.Parallel()

	access := &fakeResourceAccess{object: map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]any{
			"name":            "model-cache",
			"namespace":       "model-serving",
			"uid":             "a3f6a3d2-0b1c-4e7a-9f31-2c9e2b6d5a10",
			"resourceVersion": "77",
		},
		"status": map[string]any{"phase": "Pending"},
	}}
	events := &fakeEventSource{events: []corev1.Event{clusterEvent(
		"a",
		"ProvisioningFailed",
		"storageclass.storage.k8s.io \"fast\" not found",
		time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC),
	)}}
	service := NewService(access, events, Config{})

	result, err := service.DescribeResource(context.Background(), ResourceInput{
		ClusterID: testClusterID,
		Resource: kubernetesresource.ResourceIdentity{
			Version:  "v1",
			Resource: "persistentvolumeclaims",
		},
		Namespace: "model-serving",
		Name:      "model-cache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Family != FamilyGeneric || result.Pod != nil {
		t.Fatalf("unexpected family projection: %+v", result)
	}
	if result.Target.Kind != "PersistentVolumeClaim" ||
		result.Target.UID != "a3f6a3d2-0b1c-4e7a-9f31-2c9e2b6d5a10" {
		t.Fatalf("unexpected target: %+v", result.Target)
	}
	if len(result.Events.Items) != 1 ||
		result.Events.Items[0].Reason != "ProvisioningFailed" {
		t.Fatalf("unexpected Events: %+v", result.Events)
	}
	// The rules are written per family. A family without them describes; it does
	// not guess.
	if len(result.Findings) != 0 {
		t.Fatalf("unexpected findings for an unmodelled family: %+v", result.Findings)
	}
}

// Which Namespace holds a Cluster-scoped object's Events is a convention, not a
// rule, so the section says it was not read rather than reading the wrong one.
func TestDescribeResourceRefusesToGuessClusterScopedEvents(t *testing.T) {
	t.Parallel()

	events := &fakeEventSource{}
	service := NewService(&fakeResourceAccess{object: map[string]any{
		"apiVersion": "storage.k8s.io/v1",
		"kind":       "StorageClass",
		"metadata": map[string]any{
			"name":            "fast",
			"uid":             "1f0d0a2c-9d2b-4a56-9a0f-6f2d3b7c1e42",
			"resourceVersion": "12",
		},
	}}, events, Config{})

	result, err := service.DescribeResource(context.Background(), ResourceInput{
		ClusterID: testClusterID,
		Resource: kubernetesresource.ResourceIdentity{
			Group:    "storage.k8s.io",
			Version:  "v1",
			Resource: "storageclasses",
		},
		Name: "fast",
	})
	if err != nil {
		t.Fatal(err)
	}
	if events.called {
		t.Fatal("Events were read for a Cluster-scoped object")
	}
	if result.Events.Omitted != EventsOmittedUnsupportedScope {
		t.Fatalf("unexpected Event section: %+v", result.Events)
	}
	if len(result.DegradedSections) != 0 {
		t.Fatalf("a scope that carries no Events is not a degraded read: %v",
			result.DegradedSections)
	}
}

func TestDescribeResourceRefusesAnObjectWithNoIdentity(t *testing.T) {
	t.Parallel()

	service := NewService(
		&fakeResourceAccess{object: map[string]any{"apiVersion": "v1"}},
		&fakeEventSource{},
		Config{},
	)

	_, err := service.DescribeResource(context.Background(), ResourceInput{
		ClusterID: testClusterID,
		Resource: kubernetesresource.ResourceIdentity{
			Version:  "v1",
			Resource: "configmaps",
		},
		Namespace: "model-serving",
		Name:      "settings",
	})
	if !errors.Is(err, kubernetesresource.ErrInvalidResponse) {
		t.Fatalf("unexpected error: %v", err)
	}
}
