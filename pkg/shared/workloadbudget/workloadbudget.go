// Package workloadbudget turns the four quantity strings an operator sets on a
// platform workload into a container's resource block.
//
// Shared because both sides build containers from the same four strings: the
// Server renders the Agent's own Deployment, and the Agent creates the Cluster
// Terminal Pod and the metrics workloads. One reading of "empty means leave the
// entry off" is worth more than three copies of it.
package workloadbudget

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Requirements builds the resource block, dropping empty entries rather than
// parsing them: Kubernetes has no spelling for "no limit" other than the
// absence of the key, and a Namespace governed by a LimitRange has to be able
// to defer to it.
//
// A requirements value with no entries at all is returned as the zero value,
// which serializes as no resources block rather than as two empty maps.
func Requirements(
	cpuRequest string,
	memoryRequest string,
	cpuLimit string,
	memoryLimit string,
) (corev1.ResourceRequirements, error) {
	requests := corev1.ResourceList{}
	limits := corev1.ResourceList{}
	for _, item := range []struct {
		list  corev1.ResourceList
		name  corev1.ResourceName
		value string
	}{
		{requests, corev1.ResourceCPU, cpuRequest},
		{requests, corev1.ResourceMemory, memoryRequest},
		{limits, corev1.ResourceCPU, cpuLimit},
		{limits, corev1.ResourceMemory, memoryLimit},
	} {
		if item.value == "" {
			continue
		}
		quantity, err := resource.ParseQuantity(item.value)
		if err != nil {
			return corev1.ResourceRequirements{}, err
		}
		item.list[item.name] = quantity
	}
	requirements := corev1.ResourceRequirements{}
	if len(requests) != 0 {
		requirements.Requests = requests
	}
	if len(limits) != 0 {
		requirements.Limits = limits
	}
	return requirements, nil
}
