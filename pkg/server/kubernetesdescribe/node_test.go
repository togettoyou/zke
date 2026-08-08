package kubernetesdescribe

import (
	"context"
	"errors"
	"testing"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func testNodeDetail() kubernetesresource.NodeDetail {
	return kubernetesresource.NodeDetail{
		NodeSummary: kubernetesresource.NodeSummary{
			Name:              "worker-a",
			UID:               "node-uid",
			Status:            "not_ready",
			Unschedulable:     true,
			CPUAllocatable:    "4",
			MemoryAllocatable: "8Gi",
			PodsAllocatable:   "10",
		},
		ResourceVersion: "123",
		Conditions: []kubernetesresource.NodeCondition{
			{
				Type: "Ready", Status: "False", Reason: "KubeletNotReady",
				Message: "container runtime is down",
			},
			{Type: "MemoryPressure", Status: "True", Reason: "KubeletHasInsufficientMemory"},
		},
	}
}

func nodePod(namespace, name, phase string, ready bool, cpu, memory int64) kubernetesresource.NodePodDetail {
	return kubernetesresource.NodePodDetail{
		PodDetail: kubernetesresource.PodDetail{PodSummary: kubernetesresource.PodSummary{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  namespace,
			Name:       name,
			UID:        name + "-uid",
			Phase:      phase,
			Ready:      ready,
		}},
		CPURequestMillis:   cpu,
		MemoryRequestBytes: memory,
	}
}

func TestDescribeNodeJoinsConditionsAssignedPodsResourcesAndEvents(t *testing.T) {
	t.Parallel()

	node := testNodeDetail()
	access := &fakeResourceAccess{
		node: node,
		nodePods: []kubernetesresource.NodePodDetail{
			nodePod("default", "healthy", "Running", true, 100, 1<<30),
			nodePod("models", "pending", "Pending", false, 3600, 7<<30),
			// Terminal Pods no longer consume scheduler capacity and are omitted.
			nodePod("jobs", "finished", "Succeeded", false, 4000, 8<<30),
		},
	}
	events := &fakeEventSource{events: []corev1.Event{{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-a.event", UID: types.UID("event-node")},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Node", Name: "worker-a", UID: types.UID(node.UID),
		},
		Type: "Warning", Reason: "NodeNotReady", Message: "Node is not ready",
	}}}
	service := NewService(access, events, Config{})

	result, err := service.DescribeNode(context.Background(), NodeInput{
		ClusterID: testClusterID,
		Name:      node.Name,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Family != FamilyNode || result.Node == nil || result.Target.Kind != "Node" ||
		result.Target.ResourceVersion != "123" {
		t.Fatalf("unexpected Node projection: %+v", result)
	}
	if access.nodePodsInput.Namespace != "" ||
		access.nodePodsInput.FieldSelector != "spec.nodeName=worker-a" ||
		access.nodePodsInput.Limit != kubernetesresource.MaxResourceListLimit {
		t.Fatalf("assigned Pod list was not safely bounded: %+v", access.nodePodsInput)
	}
	if events.input.Namespace != "" || events.input.ResourceUID != node.UID ||
		events.input.ResourceKind != "Node" || events.input.Follow ||
		!events.input.IncludeInitial {
		t.Fatalf("Node Event snapshot was not exactly scoped: %+v", events.input)
	}
	if result.NodeResources == nil ||
		result.NodeResources.CPURequestedMillis != 3700 ||
		result.NodeResources.MemoryRequestedBytes != 8<<30 ||
		result.NodeResources.NonTerminalPods != 2 ||
		result.NodeResources.Truncated {
		t.Fatalf("unexpected Node resources: %+v", result.NodeResources)
	}
	if len(result.Related.Pods) != 2 || result.Related.Pods[0].Name != "pending" {
		t.Fatalf("unhealthy assigned Pod was not first: %+v", result.Related.Pods)
	}
	for _, code := range []string{
		FindingNodeNotReady,
		FindingNodeMemoryPressure,
		FindingNodeSchedulingDisabled,
		FindingNodeCPURequestsHigh,
		FindingNodeMemoryRequestsHigh,
	} {
		if !hasFindingCode(result.Findings, code) {
			t.Fatalf("missing finding %s: %+v", code, result.Findings)
		}
	}
}

func TestDescribeNodeDoesNotInferRatiosFromATruncatedPodList(t *testing.T) {
	t.Parallel()

	access := &fakeResourceAccess{
		node:         testNodeDetail(),
		nodePods:     []kubernetesresource.NodePodDetail{nodePod("default", "busy", "Running", true, 4000, 8<<30)},
		nodePodsMore: true,
	}
	result, err := NewService(access, &fakeEventSource{}, Config{}).DescribeNode(
		context.Background(), NodeInput{ClusterID: testClusterID, Name: "worker-a"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.NodeResources == nil || !result.NodeResources.Truncated || !result.Related.Truncated {
		t.Fatalf("truncation was not carried through: %+v", result)
	}
	for _, code := range []string{
		FindingNodeCPURequestsHigh,
		FindingNodeMemoryRequestsHigh,
		FindingNodePodCapacityHigh,
	} {
		if hasFindingCode(result.Findings, code) {
			t.Fatalf("derived %s from incomplete totals: %+v", code, result.Findings)
		}
	}
}

func TestDescribeNodeDegradesAssignedPodsWithoutLosingNodeEvents(t *testing.T) {
	t.Parallel()

	access := &fakeResourceAccess{
		node:        testNodeDetail(),
		nodePodsErr: errors.New("list failed"),
	}
	result, err := NewService(access, &fakeEventSource{}, Config{}).DescribeNode(
		context.Background(), NodeInput{ClusterID: testClusterID, Name: "worker-a"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.NodeResources != nil ||
		!containsString(result.DegradedSections, "related") ||
		!containsString(result.DegradedSections, "node.resources") ||
		result.Events.Omitted != "" {
		t.Fatalf("unexpected degraded result: %+v", result)
	}
}

func hasFindingCode(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
