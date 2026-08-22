package aitools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

type scaleWorkloadArguments struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Replicas  int32  `json:"replicas"`
}

func workloadScaleTarget(arguments json.RawMessage) *aisession.Target {
	var input scaleWorkloadArguments
	if decode(arguments, &input) != nil {
		return nil
	}
	return &aisession.Target{
		Namespace: input.Namespace,
		GVK:       groupVersionKind("apps/v1", input.Kind),
		Name:      input.Name,
	}
}

// scaleWorkload deliberately exposes DryRun and execution as separate tools.
// The preview is then a real Kubernetes API Server answer that the model can
// show before requesting the mutating tool, while the mutating marker and the
// three approval modes apply only to the call that can change the Cluster.
func (catalogue *Catalogue) scaleWorkload(
	ctx context.Context,
	invocation airuntime.ToolInvocation,
	dryRun bool,
) (airuntime.ToolResult, error) {
	var arguments scaleWorkloadArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	resource, ok := scalableWorkloadResource(arguments.Kind)
	if !ok || arguments.Replicas < 0 {
		return airuntime.ToolResult{}, fmt.Errorf(
			"%w: kind 只允许 Deployment 或 StatefulSet，replicas 必须大于等于 0",
			airuntime.ErrInvalidInput,
		)
	}
	scope, err := catalogue.dependencies.Scopes.ResolveClusterScope(ctx, invocation.ClusterID)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	if protectedWorkloadNamespace(arguments.Namespace, scope.AgentNamespace) {
		return airuntime.ToolResult{}, fmt.Errorf(
			"%w: AIOps 伸缩工具不操作 kube-* 或 Agent Namespace",
			airuntime.ErrInvalidInput,
		)
	}

	input := kubernetesresource.ScaleWorkloadInput{
		WorkloadMutationInput: kubernetesresource.WorkloadMutationInput{
			ClusterID:      invocation.ClusterID,
			Namespace:      arguments.Namespace,
			Resource:       resource,
			Name:           arguments.Name,
			DryRun:         true,
			Confirm:        false,
			IdempotencyKey: invocation.IdempotencyKey + ":dryrun",
		},
		Replicas: arguments.Replicas,
	}
	preview, err := catalogue.dependencies.Workloads.ScaleWorkload(ctx, input)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	result := preview
	if !dryRun {
		input.DryRun = false
		input.Confirm = true
		input.IdempotencyKey = invocation.IdempotencyKey
		result, err = catalogue.dependencies.Workloads.ScaleWorkload(ctx, input)
	}
	if err != nil {
		return airuntime.ToolResult{}, err
	}

	gvk := groupVersionKind("apps/v1", arguments.Kind)
	verb := "已通过 Kubernetes 服务端 DryRun，集群状态未改变"
	if !dryRun {
		verb = "已提交实际伸缩"
	}
	text := fmt.Sprintf(
		"%s：%s/%s %s，目标副本数 %d。\n%s",
		verb,
		arguments.Namespace,
		arguments.Name,
		arguments.Kind,
		arguments.Replicas,
		catalogue.encode(map[string]any{
			"dry_run": dryRun,
			"workload": map[string]any{
				"api_version":         result.APIVersion,
				"kind":                result.Kind,
				"namespace":           result.Namespace,
				"name":                result.Name,
				"uid":                 result.UID,
				"resource_version":    result.ResourceVersion,
				"generation":          result.Generation,
				"observed_generation": result.ObservedGeneration,
				"status":              result.Status,
				"replicas":            result.Replicas,
			},
		}),
	)
	return airuntime.ToolResult{
		Text: text,
		Evidence: []aisession.Evidence{{
			Kind: aisession.EvidenceResource, Cluster: invocation.ClusterID,
			Namespace: result.Namespace, GVK: gvk, Name: result.Name,
			ResourceVersion: result.ResourceVersion,
		}},
		Target: &aisession.Target{
			Namespace: result.Namespace, GVK: gvk, Name: result.Name,
		},
	}, nil
}

func scalableWorkloadResource(kind string) (kubernetesresource.WorkloadResource, bool) {
	switch kind {
	case "Deployment":
		return kubernetesresource.WorkloadDeployments, true
	case "StatefulSet":
		return kubernetesresource.WorkloadStatefulSets, true
	default:
		return "", false
	}
}

func protectedWorkloadNamespace(namespace, agentNamespace string) bool {
	return strings.HasPrefix(namespace, "kube-") ||
		(agentNamespace != "" && namespace == agentNamespace)
}
