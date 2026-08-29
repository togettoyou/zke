package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type workloadRevisionArguments struct {
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	Revision        int64  `json:"revision"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resource_version"`
}

type rollbackPreview struct {
	owner        string
	clusterID    string
	arguments    workloadRevisionArguments
	target       *aisession.Target
	sensitive    bool
	expiresAt    time.Time
	executionKey string
	executing    bool
	result       *airuntime.ToolResult
}

func workloadRevisionTarget(arguments json.RawMessage) *aisession.Target {
	var input workloadRevisionArguments
	if decode(arguments, &input) != nil {
		return nil
	}
	return &aisession.Target{
		Namespace: input.Namespace,
		GVK:       groupVersionKind("apps/v1", input.Kind),
		Name:      input.Name,
	}
}

func (catalogue *Catalogue) listWorkloadRevisions(
	ctx context.Context,
	invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments workloadRevisionArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	resource, ok := revisionWorkloadResource(arguments.Kind)
	if !ok {
		return airuntime.ToolResult{}, fmt.Errorf("%w: 不支持该工作负载类型", airuntime.ErrInvalidInput)
	}
	page, err := catalogue.dependencies.Revisions.ListWorkloadRevisions(ctx, kubernetesresource.ListWorkloadRevisionsInput{
		ClusterID: invocation.ClusterID, Namespace: arguments.Namespace,
		Resource: resource, Name: arguments.Name,
	})
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	identity, _ := kubernetesresource.WorkloadResourceIdentity(resource)
	object, err := catalogue.dependencies.Resources.GetResource(ctx, kubernetesresource.GetResourceInput{
		ClusterID: invocation.ClusterID, Resource: identity,
		Namespace: arguments.Namespace, Name: arguments.Name,
	})
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	live := &unstructured.Unstructured{Object: object}
	target := workloadRevisionTarget(invocation.Arguments)
	return airuntime.ToolResult{
		Text: "已读取工作负载历史版本；只能选择 current=false 的 revision，并必须原样使用 workload_uid 与 resource_version。\n" + catalogue.encode(map[string]any{
			"workload_uid":     string(live.GetUID()),
			"resource_version": live.GetResourceVersion(),
			"truncated":        page.Truncated,
			"revisions":        page.Revisions,
		}),
		Evidence: []aisession.Evidence{{
			Kind: aisession.EvidenceResource, Namespace: arguments.Namespace,
			GVK: target.GVK, Name: arguments.Name, ResourceVersion: live.GetResourceVersion(),
		}},
		Target: target,
	}, nil
}

func (catalogue *Catalogue) previewWorkloadRollback(
	ctx context.Context,
	invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments workloadRevisionArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	resource, ok := revisionWorkloadResource(arguments.Kind)
	if !ok || arguments.Revision < 1 || arguments.UID == "" || arguments.ResourceVersion == "" {
		return airuntime.ToolResult{}, fmt.Errorf("%w: 回滚目标或并发前置条件无效", airuntime.ErrInvalidInput)
	}
	sensitive, missing, err := catalogue.authorizeWorkloadMutation(ctx, invocation, arguments.Namespace)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	target := workloadRevisionTarget(invocation.Arguments)
	if missing != "" {
		return deniedClusterMutation(missing, target), nil
	}
	input := rollbackInput(invocation.ClusterID, invocation.IdempotencyKey, resource, arguments, true)
	result, err := catalogue.dependencies.Revisions.RollbackWorkload(ctx, input)
	if err != nil {
		if failure, ok := workloadRollbackFailure(err, target); ok {
			return failure, nil
		}
		return airuntime.ToolResult{}, err
	}
	id, err := newRollbackPreviewID()
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	catalogue.storeRollback(id, &rollbackPreview{
		owner: invocation.UserID, clusterID: invocation.ClusterID,
		arguments: arguments, target: target, sensitive: sensitive,
		expiresAt: time.Now().Add(catalogue.config.ManifestPreviewTTL),
	})
	return catalogue.rollbackToolResult(result, arguments.Revision, true, id, target), nil
}

func (catalogue *Catalogue) rollbackWorkload(
	ctx context.Context,
	invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var reference previewReference
	if err := decode(invocation.Arguments, &reference); err != nil {
		return airuntime.ToolResult{}, err
	}
	preview, cached, err := catalogue.reserveRollback(reference.PreviewID, invocation)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	if cached != nil {
		return *cached, nil
	}
	succeeded := false
	defer func() { catalogue.releaseRollback(reference.PreviewID, succeeded) }()

	_, missing, err := catalogue.authorizeWorkloadMutation(ctx, invocation, preview.arguments.Namespace)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	if missing != "" {
		return deniedClusterMutation(missing, preview.target), nil
	}
	resource, _ := revisionWorkloadResource(preview.arguments.Kind)
	preflight := rollbackInput(invocation.ClusterID, preview.executionKey+":preflight", resource, preview.arguments, true)
	if _, err = catalogue.dependencies.Revisions.RollbackWorkload(ctx, preflight); err != nil {
		if failure, ok := workloadRollbackFailure(err, preview.target); ok {
			return failure, nil
		}
		return airuntime.ToolResult{}, err
	}
	input := rollbackInput(invocation.ClusterID, preview.executionKey, resource, preview.arguments, false)
	result, err := catalogue.dependencies.Revisions.RollbackWorkload(ctx, input)
	if err != nil {
		if failure, ok := workloadRollbackFailure(err, preview.target); ok {
			return failure, nil
		}
		return airuntime.ToolResult{}, err
	}
	toolResult := catalogue.rollbackToolResult(result, preview.arguments.Revision, false, "", preview.target)
	succeeded = true
	catalogue.completeRollback(reference.PreviewID, toolResult)
	return toolResult, nil
}

func rollbackInput(
	clusterID, idempotencyKey string,
	resource kubernetesresource.WorkloadResource,
	arguments workloadRevisionArguments,
	dryRun bool,
) kubernetesresource.RollbackWorkloadInput {
	return kubernetesresource.RollbackWorkloadInput{
		WorkloadMutationInput: kubernetesresource.WorkloadMutationInput{
			ClusterID: clusterID, Namespace: arguments.Namespace,
			Resource: resource, Name: arguments.Name,
			DryRun: dryRun, Confirm: !dryRun, IdempotencyKey: idempotencyKey,
		},
		Revision: arguments.Revision, UID: arguments.UID,
		ResourceVersion: arguments.ResourceVersion,
	}
}

func (catalogue *Catalogue) authorizeWorkloadMutation(
	ctx context.Context,
	invocation airuntime.ToolInvocation,
	namespace string,
) (bool, rbac.Permission, error) {
	scope, err := catalogue.dependencies.Scopes.ResolveClusterScope(ctx, invocation.ClusterID)
	if err != nil {
		return false, "", err
	}
	permission := rbac.PermissionClusterResourceUpdate
	sensitive := false
	switch {
	case namespace == scope.AgentNamespace:
		permission = rbac.PermissionClusterAgentNamespaceManage
		sensitive = true
	case strings.HasPrefix(namespace, "kube-"):
		permission = rbac.PermissionClusterSystemNamespaceManage
		sensitive = true
	}
	err = catalogue.dependencies.Scopes.AuthorizeResolvedCluster(ctx, invocation.UserID, permission, scope)
	if errors.Is(err, rbac.ErrDenied) {
		return sensitive, permission, nil
	}
	return sensitive, "", err
}

// deniedClusterMutation is the answer to a write the operator may not make.
//
// It is a result rather than an error: the model is told which grant is
// missing so it can say so, and the audit row records the same thing. Shared
// by every write tool here, because "which permission was missing" is the same
// question whether the write was a workload scale or a Helm release change.
func deniedClusterMutation(
	permission rbac.Permission,
	target *aisession.Target,
) airuntime.ToolResult {
	result := airuntime.ToolResult{
		Text:   fmt.Sprintf("当前账户在该 Cluster 上没有 %s 权限，未执行。", permission),
		Target: target, Failed: true, Denied: true,
	}
	if target != nil {
		result.AuditTargets = []airuntime.ToolAuditTarget{{
			Target: *target, Result: "denied", MissingPermission: string(permission),
		}}
	}
	return result
}

// workloadRollbackFailure keeps expected concurrency and revision outcomes
// explicit without exposing arbitrary upstream error strings to the model or
// durable trajectory. They are actionable domain results, not evidence that
// the Agent is disconnected.
func workloadRollbackFailure(err error, target *aisession.Target) (airuntime.ToolResult, bool) {
	result := airuntime.ToolResult{Target: target, Failed: true}
	switch {
	case errors.Is(err, kubernetesresource.ErrWorkloadRevisionUnchanged):
		result.Text = "目标 revision 已是工作负载当前版本，未执行回滚；请从历史列表选择 current=false 的 revision。"
	case errors.Is(err, kubernetesresource.ErrWorkloadRevisionNotFound):
		result.Text = "目标历史 revision 已不存在，未执行回滚；请重新读取工作负载历史版本。"
	case errors.Is(err, kubernetesresource.ErrUpstreamConflict):
		result.Text = "工作负载在预检后已发生变化，未执行回滚；请重新读取历史版本和并发前置条件。"
	default:
		return airuntime.ToolResult{}, false
	}
	return result, true
}

func revisionWorkloadResource(kind string) (kubernetesresource.WorkloadResource, bool) {
	switch kind {
	case "Deployment":
		return kubernetesresource.WorkloadDeployments, true
	case "StatefulSet":
		return kubernetesresource.WorkloadStatefulSets, true
	case "DaemonSet":
		return kubernetesresource.WorkloadDaemonSets, true
	default:
		return "", false
	}
}

func (catalogue *Catalogue) rollbackToolResult(
	result kubernetesresource.WorkloadDetail,
	revision int64,
	dryRun bool,
	previewID string,
	target *aisession.Target,
) airuntime.ToolResult {
	payload := map[string]any{
		"dry_run": dryRun, "revision": revision,
		"workload": map[string]any{
			"api_version": result.APIVersion, "kind": result.Kind,
			"namespace": result.Namespace, "name": result.Name,
			"uid": result.UID, "resource_version": result.ResourceVersion,
			"generation": result.Generation, "status": result.Status,
		},
	}
	if previewID != "" {
		payload["preview_id"] = previewID
		payload["expires_in_seconds"] = int(catalogue.config.ManifestPreviewTTL.Seconds())
	}
	verb := "工作负载回滚 DryRun 已通过；集群状态未改变"
	if !dryRun {
		verb = "工作负载回滚已提交"
	}
	return airuntime.ToolResult{
		Text: verb + "。\n" + catalogue.encode(payload),
		Evidence: []aisession.Evidence{{
			Kind: aisession.EvidenceResource, Namespace: result.Namespace,
			GVK: target.GVK, Name: result.Name, ResourceVersion: result.ResourceVersion,
		}},
		Target: target,
	}
}

func newRollbackPreviewID() (string, error) {
	id, err := newPreviewID()
	if err != nil {
		return "", err
	}
	return "rollback_" + strings.TrimPrefix(id, "manifest_"), nil
}

func (catalogue *Catalogue) storeRollback(id string, preview *rollbackPreview) {
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	now := time.Now()
	for key, item := range catalogue.rollbacks {
		if now.After(item.expiresAt) {
			delete(catalogue.rollbacks, key)
		}
	}
	if len(catalogue.rollbacks) >= 256 {
		var oldestKey string
		var oldest time.Time
		for key, item := range catalogue.rollbacks {
			if oldestKey == "" || item.expiresAt.Before(oldest) {
				oldestKey, oldest = key, item.expiresAt
			}
		}
		delete(catalogue.rollbacks, oldestKey)
	}
	catalogue.rollbacks[id] = preview
}

func (catalogue *Catalogue) reserveRollback(
	id string,
	invocation airuntime.ToolInvocation,
) (*rollbackPreview, *airuntime.ToolResult, error) {
	if !strings.HasPrefix(id, "rollback_") {
		return nil, nil, fmt.Errorf("%w: preview_id 无效", airuntime.ErrInvalidInput)
	}
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	preview := catalogue.rollbacks[id]
	if preview == nil {
		return nil, nil, fmt.Errorf("%w: 预检不存在、已过期或不属于当前用户和 Cluster", airuntime.ErrInvalidInput)
	}
	if time.Now().After(preview.expiresAt) {
		delete(catalogue.rollbacks, id)
		return nil, nil, fmt.Errorf("%w: 预检不存在、已过期或不属于当前用户和 Cluster", airuntime.ErrInvalidInput)
	}
	if preview.owner != invocation.UserID || preview.clusterID != invocation.ClusterID {
		return nil, nil, fmt.Errorf("%w: 预检不存在、已过期或不属于当前用户和 Cluster", airuntime.ErrInvalidInput)
	}
	if preview.result != nil {
		copy := *preview.result
		return preview, &copy, nil
	}
	if preview.executing {
		return nil, nil, fmt.Errorf("%w: 该预检正在提交", airuntime.ErrInvalidInput)
	}
	preview.executing = true
	if preview.executionKey == "" {
		preview.executionKey = invocation.IdempotencyKey
	}
	return preview, nil, nil
}

func (catalogue *Catalogue) releaseRollback(id string, succeeded bool) {
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	if preview := catalogue.rollbacks[id]; preview != nil {
		preview.executing = false
		if succeeded {
			preview.expiresAt = time.Now().Add(catalogue.config.ManifestPreviewTTL)
		}
	}
}

func (catalogue *Catalogue) completeRollback(id string, result airuntime.ToolResult) {
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	if preview := catalogue.rollbacks[id]; preview != nil {
		copy := result
		preview.result = &copy
	}
}
