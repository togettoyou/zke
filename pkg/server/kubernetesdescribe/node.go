package kubernetesdescribe

import (
	"context"
	"math"
	"sort"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"k8s.io/apimachinery/pkg/api/resource"
)

const maxRelatedNodePods = 10

type NodeInput struct {
	ClusterID string
	Name      string
}

// DescribeNode joins the Node's conditions and Events with a bounded view of
// the non-terminal Pods assigned to it and their scheduler resource requests.
func (service *Service) DescribeNode(
	ctx context.Context,
	input NodeInput,
) (Result, error) {
	if service == nil || service.resources == nil {
		return Result{}, ErrInvalidInput
	}
	node, err := service.resources.GetNode(ctx, input.ClusterID, input.Name)
	if err != nil {
		return Result{}, err
	}
	if node.UID == "" {
		return Result{}, kubernetesresource.ErrInvalidResponse
	}
	result := Result{
		Target: Target{
			APIVersion:      "v1",
			Kind:            "Node",
			Name:            node.Name,
			UID:             node.UID,
			ResourceVersion: node.ResourceVersion,
		},
		Family: FamilyNode,
		Node:   &node,
		Related: &Related{
			Controllers:            []RelatedObject{},
			Pods:                   []RelatedObject{},
			PersistentVolumeClaims: []RelatedObject{},
		},
		Findings:         nodeConditionFindings(node),
		DegradedSections: []string{},
	}

	pods, listTruncated, listErr := service.resources.ListNodePodDetails(
		ctx,
		kubernetesresource.ListPodsInput{
			ClusterID:     input.ClusterID,
			Limit:         kubernetesresource.MaxResourceListLimit,
			FieldSelector: "spec.nodeName=" + node.Name,
		},
	)
	if listErr != nil {
		result.DegradedSections = append(
			result.DegradedSections, "related", "node.resources",
		)
	} else {
		nonTerminal := nonTerminalNodePods(pods)
		result.Related.Pods, result.Related.Truncated = nodePodObjects(
			nonTerminal, listTruncated,
		)
		resources, resourceErr := summarizeNodeResources(node, nonTerminal, listTruncated)
		if resourceErr != nil {
			result.DegradedSections = append(result.DegradedSections, "node.resources")
		} else {
			result.NodeResources = &resources
			result.Findings = append(
				result.Findings,
				nodeResourceFindings(resources)...,
			)
		}
	}

	items, truncated, eventErr := service.readEvents(
		ctx, input.ClusterID, "", node.UID, "Node",
	)
	if eventErr != nil {
		result.Events = Events{Items: []Event{}, Omitted: EventsOmittedUnavailable}
		result.DegradedSections = append(result.DegradedSections, "events")
	} else {
		result.Events = Events{Items: items, Truncated: truncated}
	}
	return result, nil
}

func nonTerminalNodePods(
	pods []kubernetesresource.NodePodDetail,
) []kubernetesresource.NodePodDetail {
	result := make([]kubernetesresource.NodePodDetail, 0, len(pods))
	for _, pod := range pods {
		if pod.Phase == "Succeeded" || pod.Phase == "Failed" {
			continue
		}
		result = append(result, pod)
	}
	return result
}

func nodePodObjects(
	pods []kubernetesresource.NodePodDetail,
	listTruncated bool,
) ([]RelatedObject, bool) {
	objects := make([]RelatedObject, 0, len(pods))
	for _, item := range pods {
		pod := item.PodDetail
		findings := podFindings(pod, nil)
		objects = append(objects, RelatedObject{
			Kind:      "Pod",
			Name:      pod.Name,
			UID:       pod.UID,
			Namespace: pod.Namespace,
			Status:    podStatusText(pod),
			Ready:     pod.Ready && len(findings) == 0,
			Findings:  findings,
		})
	}
	sort.SliceStable(objects, func(left, right int) bool {
		return !objects[left].Ready && objects[right].Ready
	})
	truncated := listTruncated || len(objects) > maxRelatedNodePods
	if len(objects) > maxRelatedNodePods {
		objects = objects[:maxRelatedNodePods]
	}
	return objects, truncated
}

func summarizeNodeResources(
	node kubernetesresource.NodeDetail,
	pods []kubernetesresource.NodePodDetail,
	truncated bool,
) (NodeResources, error) {
	cpu, err := resource.ParseQuantity(node.CPUAllocatable)
	if err != nil || cpu.Sign() < 0 {
		return NodeResources{}, kubernetesresource.ErrInvalidResponse
	}
	memory, err := resource.ParseQuantity(node.MemoryAllocatable)
	if err != nil || memory.Sign() < 0 {
		return NodeResources{}, kubernetesresource.ErrInvalidResponse
	}
	podCapacity, err := resource.ParseQuantity(node.PodsAllocatable)
	if err != nil || podCapacity.Sign() < 0 {
		return NodeResources{}, kubernetesresource.ErrInvalidResponse
	}
	cpuMillis := cpu.MilliValue()
	memoryBytes := memory.Value()
	podSlots := podCapacity.Value()
	if cpuMillis < 0 || memoryBytes < 0 || podSlots < 0 {
		return NodeResources{}, kubernetesresource.ErrInvalidResponse
	}
	result := NodeResources{
		CPUAllocatableMillis:   cpuMillis,
		MemoryAllocatableBytes: memoryBytes,
		PodAllocatable:         podSlots,
		NonTerminalPods:        int64(len(pods)),
		Truncated:              truncated,
	}
	for _, pod := range pods {
		if pod.CPURequestMillis > math.MaxInt64-result.CPURequestedMillis ||
			pod.MemoryRequestBytes > math.MaxInt64-result.MemoryRequestedBytes {
			return NodeResources{}, kubernetesresource.ErrInvalidResponse
		}
		result.CPURequestedMillis += pod.CPURequestMillis
		result.MemoryRequestedBytes += pod.MemoryRequestBytes
	}
	return result, nil
}
