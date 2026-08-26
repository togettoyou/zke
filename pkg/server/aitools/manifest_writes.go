package aitools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/kubernetesmanifest"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/kubernetesyaml"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type manifestArguments struct {
	Manifest  string `json:"manifest"`
	Namespace string `json:"namespace"`
	Force     bool   `json:"force"`
}

type previewReference struct {
	PreviewID string `json:"preview_id"`
}

type writePreview struct {
	owner        string
	clusterID    string
	operation    kubernetesmanifest.Operation
	input        kubernetesmanifest.Input
	target       *aisession.Target
	sensitive    bool
	expiresAt    time.Time
	executionKey string
	executing    bool
	result       *airuntime.ToolResult
}

func (catalogue *Catalogue) previewManifest(
	ctx context.Context,
	invocation airuntime.ToolInvocation,
	operation kubernetesmanifest.Operation,
) (airuntime.ToolResult, error) {
	var arguments manifestArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	manifest := []byte(arguments.Manifest)
	if len(manifest) == 0 || len(manifest) > catalogue.config.MaxManifestBytes {
		return airuntime.ToolResult{}, fmt.Errorf(
			"%w: manifest 必须为 1..%d 字节", airuntime.ErrInvalidInput,
			catalogue.config.MaxManifestBytes,
		)
	}
	if operation == kubernetesmanifest.OperationDelete && arguments.Force {
		return airuntime.ToolResult{}, fmt.Errorf("%w: delete 不接受 force", airuntime.ErrInvalidInput)
	}
	if err := rejectSecretManifest(manifest, catalogue.config.MaxManifestDocuments); err != nil {
		return airuntime.ToolResult{}, err
	}

	access, _, err := catalogue.manifestAccess(ctx, invocation)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	input := kubernetesmanifest.Input{
		ClusterID:      invocation.ClusterID,
		Manifest:       manifest,
		Namespace:      arguments.Namespace,
		Operation:      operation,
		DryRun:         true,
		Force:          arguments.Force,
		IdempotencyKey: invocation.IdempotencyKey,
	}
	result, err := catalogue.dependencies.Manifests.Execute(ctx, access, input)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	sensitive := arguments.Force || manifestResultSensitive(result)
	target := manifestResultTarget(result)
	toolResult := catalogue.manifestToolResult(result, "", target)
	if toolResult.Failed {
		return toolResult, nil
	}

	previewID, err := newPreviewID()
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	input.DryRun = false
	input.Confirm = true
	catalogue.storePreview(previewID, &writePreview{
		owner: invocation.UserID, clusterID: invocation.ClusterID,
		operation: operation, input: input, target: target,
		sensitive: sensitive || operation == kubernetesmanifest.OperationDelete,
		expiresAt: time.Now().Add(catalogue.config.ManifestPreviewTTL),
	})
	toolResult.Text = catalogue.manifestToolResult(result, previewID, target).Text
	return toolResult, nil
}

func (catalogue *Catalogue) executeManifest(
	ctx context.Context,
	invocation airuntime.ToolInvocation,
	operation kubernetesmanifest.Operation,
) (airuntime.ToolResult, error) {
	var reference previewReference
	if err := decode(invocation.Arguments, &reference); err != nil {
		return airuntime.ToolResult{}, err
	}
	preview, cached, err := catalogue.reservePreview(reference.PreviewID, invocation, operation)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	if cached != nil {
		return *cached, nil
	}
	succeeded := false
	defer func() { catalogue.releasePreview(reference.PreviewID, succeeded) }()

	access, _, err := catalogue.manifestAccess(ctx, invocation)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	preflight := preview.input
	preflight.DryRun = true
	preflight.Confirm = false
	preflight.IdempotencyKey = preview.executionKey + ":preflight"
	checked, err := catalogue.dependencies.Manifests.Execute(ctx, access, preflight)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	if checkResult := catalogue.manifestToolResult(checked, "", preview.target); checkResult.Failed {
		return checkResult, nil
	}

	input := preview.input
	input.IdempotencyKey = preview.executionKey
	result, err := catalogue.dependencies.Manifests.Execute(ctx, access, input)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	toolResult := catalogue.manifestToolResult(result, "", preview.target)
	if !toolResult.Failed {
		succeeded = true
		catalogue.completePreview(reference.PreviewID, toolResult)
	}
	return toolResult, nil
}

func rejectSecretManifest(manifest []byte, maxDocuments int) error {
	documents, err := kubernetesyaml.DecodeDocuments(manifest, maxDocuments)
	if err != nil {
		return err
	}
	for _, object := range documents {
		value := &unstructured.Unstructured{Object: object}
		if value.GetAPIVersion() == "v1" && value.GetKind() == "Secret" {
			return fmt.Errorf(
				"%w: AIOps 不接受 Secret 清单；请使用 ZKE Secret 专用入口",
				airuntime.ErrInvalidInput,
			)
		}
	}
	return nil
}

func (catalogue *Catalogue) manifestAccess(
	ctx context.Context,
	invocation airuntime.ToolInvocation,
) (kubernetesmanifest.ResourceAccess, rbac.ResolvedScope, error) {
	scope, err := catalogue.dependencies.Scopes.ResolveClusterScope(ctx, invocation.ClusterID)
	if err != nil {
		return nil, rbac.ResolvedScope{}, err
	}
	holds := func(permission rbac.Permission) (bool, error) {
		err := catalogue.dependencies.Scopes.AuthorizeResolvedCluster(
			ctx, invocation.UserID, permission, scope,
		)
		if errors.Is(err, rbac.ErrDenied) {
			return false, nil
		}
		return err == nil, err
	}
	permissions := []rbac.Permission{
		rbac.PermissionClusterResourceCreate,
		rbac.PermissionClusterResourceUpdate,
		rbac.PermissionClusterResourceDelete,
		rbac.PermissionClusterNamespaceManage,
		rbac.PermissionClusterNodeManage,
		rbac.PermissionClusterSecretRead,
		rbac.PermissionClusterSecretManage,
		rbac.PermissionClusterRBACManage,
		rbac.PermissionClusterSystemNamespaceManage,
		rbac.PermissionClusterAgentNamespaceManage,
	}
	granted := make(map[rbac.Permission]bool, len(permissions))
	for _, permission := range permissions {
		allowed, permissionErr := holds(permission)
		if permissionErr != nil {
			return nil, rbac.ResolvedScope{}, permissionErr
		}
		granted[permission] = allowed
	}
	grant := kubernetesresource.ManifestGrant{
		AgentNamespace:        scope.AgentNamespace,
		ResourceCreate:        granted[rbac.PermissionClusterResourceCreate],
		ResourceUpdate:        granted[rbac.PermissionClusterResourceUpdate],
		ResourceDelete:        granted[rbac.PermissionClusterResourceDelete],
		NamespaceManage:       granted[rbac.PermissionClusterNamespaceManage],
		NodeManage:            granted[rbac.PermissionClusterNodeManage],
		SecretRead:            granted[rbac.PermissionClusterSecretRead],
		SecretManage:          granted[rbac.PermissionClusterSecretManage],
		RBACManage:            granted[rbac.PermissionClusterRBACManage],
		SystemNamespaceManage: granted[rbac.PermissionClusterSystemNamespaceManage],
		AgentNamespaceManage:  granted[rbac.PermissionClusterAgentNamespaceManage],
	}
	return catalogue.dependencies.ManifestAccess(grant), scope, nil
}

func manifestResultSensitive(result kubernetesmanifest.Result) bool {
	for _, document := range result.Documents {
		switch document.Requirement {
		case kubernetesresource.ManifestRequirementRBACManage,
			kubernetesresource.ManifestRequirementSystemNamespaceManage,
			kubernetesresource.ManifestRequirementAgentNamespaceManage:
			return true
		}
	}
	return false
}

func manifestResultTarget(result kubernetesmanifest.Result) *aisession.Target {
	if len(result.Documents) != 1 {
		return nil
	}
	document := result.Documents[0]
	return &aisession.Target{
		Namespace: document.Namespace,
		GVK:       groupVersionKind(document.APIVersion, document.Kind),
		Name:      document.Name,
	}
}

func (catalogue *Catalogue) manifestToolResult(
	result kubernetesmanifest.Result,
	previewID string,
	target *aisession.Target,
) airuntime.ToolResult {
	documents := make([]map[string]any, 0, len(result.Documents))
	evidence := make([]aisession.Evidence, 0, len(result.Documents))
	auditTargets := make([]airuntime.ToolAuditTarget, 0, len(result.Documents))
	for _, document := range result.Documents {
		entry := map[string]any{
			"index":         document.Index,
			"api_version":   document.APIVersion,
			"kind":          document.Kind,
			"namespace":     document.Namespace,
			"name":          document.Name,
			"action":        document.Action,
			"status":        document.Status,
			"permission":    document.Requirement,
			"previewed":     document.Previewed,
			"changed_paths": changedPaths(document.Before, document.After, document.Action),
		}
		if document.Err != nil {
			entry["error"] = document.Err.Error()
		}
		documents = append(documents, entry)
		auditResult := "succeeded"
		if !result.Executable() {
			auditResult = "failed"
		}
		switch document.Status {
		case kubernetesmanifest.StatusRefused:
			auditResult = "denied"
		case kubernetesmanifest.StatusInvalid, kubernetesmanifest.StatusFailed,
			kubernetesmanifest.StatusNotAttempted:
			auditResult = "failed"
		}
		missing := ""
		if auditResult == "denied" {
			missing = manifestRequirementPermission(document.Requirement)
		}
		auditTargets = append(auditTargets, airuntime.ToolAuditTarget{
			Target: aisession.Target{
				Namespace: document.Namespace,
				GVK:       groupVersionKind(document.APIVersion, document.Kind),
				Name:      document.Name,
			},
			Result: auditResult, MissingPermission: missing,
		})
		if document.UID != "" && (document.Before != nil || !result.DryRun) {
			evidence = append(evidence, aisession.Evidence{
				Kind:            aisession.EvidenceResource,
				Namespace:       document.Namespace,
				GVK:             groupVersionKind(document.APIVersion, document.Kind),
				Name:            document.Name,
				ResourceVersion: document.ResourceVersion,
			})
		}
	}
	payload := map[string]any{
		"dry_run":         result.DryRun,
		"allowed":         result.Allowed,
		"valid":           result.Valid,
		"failed":          result.Failed,
		"catalog_partial": result.CatalogPartial,
		"documents":       documents,
	}
	if previewID != "" {
		payload["preview_id"] = previewID
		payload["expires_in_seconds"] = int(catalogue.config.ManifestPreviewTTL.Seconds())
	}
	verb := "Manifest 操作完成"
	if result.DryRun {
		verb = "Manifest 已完成服务端 DryRun；集群状态未改变"
	}
	failed := !result.Executable() || result.Failed
	return airuntime.ToolResult{
		Text:         verb + "。\n" + catalogue.encode(payload),
		Evidence:     evidence,
		Target:       target,
		AuditTargets: auditTargets,
		Failed:       failed,
		Denied:       !result.Allowed,
	}
}

func manifestRequirementPermission(requirement kubernetesresource.ManifestRequirement) string {
	switch requirement {
	case kubernetesresource.ManifestRequirementResourceCreate:
		return string(rbac.PermissionClusterResourceCreate)
	case kubernetesresource.ManifestRequirementResourceUpdate:
		return string(rbac.PermissionClusterResourceUpdate)
	case kubernetesresource.ManifestRequirementResourceDelete:
		return string(rbac.PermissionClusterResourceDelete)
	case kubernetesresource.ManifestRequirementNamespaceManage:
		return string(rbac.PermissionClusterNamespaceManage)
	case kubernetesresource.ManifestRequirementNodeManage:
		return string(rbac.PermissionClusterNodeManage)
	case kubernetesresource.ManifestRequirementSecretManage:
		return string(rbac.PermissionClusterSecretManage)
	case kubernetesresource.ManifestRequirementRBACManage:
		return string(rbac.PermissionClusterRBACManage)
	case kubernetesresource.ManifestRequirementSystemNamespaceManage:
		return string(rbac.PermissionClusterSystemNamespaceManage)
	case kubernetesresource.ManifestRequirementAgentNamespaceManage:
		return string(rbac.PermissionClusterAgentNamespaceManage)
	default:
		return ""
	}
}

func changedPaths(before, after map[string]any, action kubernetesmanifest.Action) []string {
	switch action {
	case kubernetesmanifest.ActionCreate, kubernetesmanifest.ActionDelete:
		return []string{"$"}
	case kubernetesmanifest.ActionAbsent:
		return []string{}
	}
	paths := make([]string, 0, 16)
	diffValue("", sanitizedDiffObject(before), sanitizedDiffObject(after), &paths)
	sort.Strings(paths)
	if len(paths) > 64 {
		paths = append(paths[:64], "…")
	}
	return paths
}

func sanitizedDiffObject(object map[string]any) map[string]any {
	if object == nil {
		return nil
	}
	encoded, _ := json.Marshal(object)
	copy := map[string]any{}
	_ = json.Unmarshal(encoded, &copy)
	delete(copy, "status")
	if metadata, ok := copy["metadata"].(map[string]any); ok {
		for _, key := range []string{"managedFields", "resourceVersion", "uid", "generation", "creationTimestamp"} {
			delete(metadata, key)
		}
	}
	return copy
}

func diffValue(path string, before, after any, paths *[]string) {
	if len(*paths) > 64 {
		return
	}
	beforeMap, beforeIsMap := before.(map[string]any)
	afterMap, afterIsMap := after.(map[string]any)
	if beforeIsMap && afterIsMap {
		keys := make(map[string]struct{}, len(beforeMap)+len(afterMap))
		for key := range beforeMap {
			keys[key] = struct{}{}
		}
		for key := range afterMap {
			keys[key] = struct{}{}
		}
		for key := range keys {
			next := key
			if path != "" {
				next = path + "." + key
			}
			diffValue(next, beforeMap[key], afterMap[key], paths)
		}
		return
	}
	if !valuesEqual(before, after) {
		if path == "" {
			path = "$"
		}
		*paths = append(*paths, path)
	}
}

func valuesEqual(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func newPreviewID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "manifest_" + hex.EncodeToString(value), nil
}

func (catalogue *Catalogue) storePreview(id string, preview *writePreview) {
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	now := time.Now()
	for key, item := range catalogue.previews {
		if now.After(item.expiresAt) {
			delete(catalogue.previews, key)
		}
	}
	if len(catalogue.previews) >= 256 {
		var oldestKey string
		var oldest time.Time
		for key, item := range catalogue.previews {
			if oldestKey == "" || item.expiresAt.Before(oldest) {
				oldestKey, oldest = key, item.expiresAt
			}
		}
		delete(catalogue.previews, oldestKey)
	}
	catalogue.previews[id] = preview
}

func (catalogue *Catalogue) reservePreview(
	id string,
	invocation airuntime.ToolInvocation,
	operation kubernetesmanifest.Operation,
) (*writePreview, *airuntime.ToolResult, error) {
	if !strings.HasPrefix(id, "manifest_") {
		return nil, nil, fmt.Errorf("%w: preview_id 无效", airuntime.ErrInvalidInput)
	}
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	preview := catalogue.previews[id]
	if preview == nil {
		return nil, nil, fmt.Errorf("%w: 预检不存在、已过期或不属于当前用户和 Cluster", airuntime.ErrInvalidInput)
	}
	if time.Now().After(preview.expiresAt) {
		delete(catalogue.previews, id)
		return nil, nil, fmt.Errorf("%w: 预检不存在、已过期或不属于当前用户和 Cluster", airuntime.ErrInvalidInput)
	}
	if preview.owner != invocation.UserID || preview.clusterID != invocation.ClusterID ||
		preview.operation != operation {
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

func (catalogue *Catalogue) releasePreview(id string, succeeded bool) {
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	if preview := catalogue.previews[id]; preview != nil {
		preview.executing = false
		if succeeded {
			preview.expiresAt = time.Now().Add(catalogue.config.ManifestPreviewTTL)
		}
	}
}

func (catalogue *Catalogue) completePreview(id string, result airuntime.ToolResult) {
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	if preview := catalogue.previews[id]; preview != nil {
		copy := result
		preview.result = &copy
	}
}

func (catalogue *Catalogue) previewTarget(arguments json.RawMessage) *aisession.Target {
	var reference previewReference
	if decode(arguments, &reference) != nil {
		return nil
	}
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	if preview := catalogue.previews[reference.PreviewID]; preview != nil &&
		preview.target != nil && time.Now().Before(preview.expiresAt) {
		copy := *preview.target
		return &copy
	}
	if preview := catalogue.rollbacks[reference.PreviewID]; preview != nil &&
		preview.target != nil && time.Now().Before(preview.expiresAt) {
		copy := *preview.target
		return &copy
	}
	return nil
}

func (catalogue *Catalogue) previewSensitive(arguments json.RawMessage) bool {
	var reference previewReference
	if decode(arguments, &reference) != nil {
		return false
	}
	catalogue.mu.Lock()
	defer catalogue.mu.Unlock()
	preview := catalogue.previews[reference.PreviewID]
	if preview != nil && time.Now().Before(preview.expiresAt) {
		return preview.sensitive
	}
	rollback := catalogue.rollbacks[reference.PreviewID]
	return rollback != nil && time.Now().Before(rollback.expiresAt) && rollback.sensitive
}
