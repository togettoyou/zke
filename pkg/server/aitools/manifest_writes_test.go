package aitools

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/kubernetesmanifest"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

type recordingManifestWriter struct {
	inputs []kubernetesmanifest.Input
	result kubernetesmanifest.Result
}

func (writer *recordingManifestWriter) Execute(
	_ context.Context,
	_ kubernetesmanifest.ResourceAccess,
	input kubernetesmanifest.Input,
) (kubernetesmanifest.Result, error) {
	writer.inputs = append(writer.inputs, input)
	result := writer.result
	result.DryRun = input.DryRun
	return result, nil
}

type permissionScope struct {
	staticScopeResolver
	allowed map[rbac.Permission]bool
}

func (scope permissionScope) AuthorizeResolvedCluster(
	_ context.Context,
	_ string,
	permission rbac.Permission,
	_ rbac.ResolvedScope,
) error {
	if scope.allowed[permission] {
		return nil
	}
	return rbac.ErrDenied
}

func manifestPreviewResult(requirement kubernetesresource.ManifestRequirement) kubernetesmanifest.Result {
	return kubernetesmanifest.Result{
		Allowed: true, Valid: true,
		Documents: []kubernetesmanifest.Document{{
			Index: 0, APIVersion: "apps/v1", Kind: "Deployment",
			Namespace: "team-a", Name: "web",
			Action:      kubernetesmanifest.ActionUpdate,
			Status:      kubernetesmanifest.StatusPlanned,
			Requirement: requirement, Previewed: true,
			UID: "uid-web", ResourceVersion: "8",
			Before: map[string]any{"spec": map[string]any{"replicas": float64(2)}},
			After:  map[string]any{"spec": map[string]any{"replicas": float64(3)}},
		}},
	}
}

func manifestCatalogue(writer *recordingManifestWriter, scope ClusterScopeResolver) *Catalogue {
	return New(Dependencies{
		Manifests:      writer,
		ManifestAccess: func(kubernetesresource.ManifestGrant) kubernetesmanifest.ResourceAccess { return nil },
		Scopes:         scope,
	}, Config{})
}

func TestManifestApplyBindsExecutionToAuthorizedPreview(t *testing.T) {
	t.Parallel()
	writer := &recordingManifestWriter{result: manifestPreviewResult(
		kubernetesresource.ManifestRequirementResourceUpdate,
	)}
	scope := permissionScope{
		staticScopeResolver: ordinaryScope(),
		allowed:             map[rbac.Permission]bool{rbac.PermissionClusterResourceUpdate: true},
	}
	catalogue := manifestCatalogue(writer, scope)
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: team-a\n"
	preview, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolPreviewManifestApply, ClusterID: testClusterID, UserID: testUserID,
		Arguments:      json.RawMessage(`{"manifest":` + quoteJSON(manifest) + `}`),
		IdempotencyKey: "aiops:preview",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := regexp.MustCompile(`manifest_[0-9a-f]+`).FindString(preview.Text)
	if id == "" || !strings.Contains(preview.Text, `"spec.replicas"`) {
		t.Fatalf("preview = %q", preview.Text)
	}
	_, err = catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolApplyManifest, ClusterID: testClusterID,
		UserID:         "11111111-1111-4111-8111-111111111111",
		Arguments:      json.RawMessage(`{"preview_id":"` + id + `"}`),
		IdempotencyKey: "aiops:other-user",
	})
	if err == nil || len(writer.inputs) != 1 {
		t.Fatalf("foreign preview err=%v inputs=%+v", err, writer.inputs)
	}
	result, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolApplyManifest, ClusterID: testClusterID, UserID: testUserID,
		Arguments:      json.RawMessage(`{"preview_id":"` + id + `"}`),
		IdempotencyKey: "aiops:execute",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed || len(writer.inputs) != 3 {
		t.Fatalf("result=%+v inputs=%+v", result, writer.inputs)
	}
	if !writer.inputs[0].DryRun || !writer.inputs[1].DryRun || writer.inputs[2].DryRun ||
		!writer.inputs[2].Confirm || writer.inputs[2].IdempotencyKey != "aiops:execute" {
		t.Fatalf("manifest sequence = %+v", writer.inputs)
	}
	if string(writer.inputs[2].Manifest) != manifest {
		t.Fatalf("executed manifest changed: %q", writer.inputs[2].Manifest)
	}
	// A successful retry of the same preview is answered from the cache and does
	// not create another Agent write under a new runtime call key.
	_, err = catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolApplyManifest, ClusterID: testClusterID, UserID: testUserID,
		Arguments:      json.RawMessage(`{"preview_id":"` + id + `"}`),
		IdempotencyKey: "aiops:different-call",
	})
	if err != nil || len(writer.inputs) != 3 {
		t.Fatalf("retry err=%v inputs=%+v", err, writer.inputs)
	}
}

func TestManifestPreviewRefusesSecretsBeforeService(t *testing.T) {
	t.Parallel()
	writer := &recordingManifestWriter{}
	catalogue := manifestCatalogue(writer, permissionScope{
		staticScopeResolver: ordinaryScope(), allowed: map[rbac.Permission]bool{},
	})
	_, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolPreviewManifestApply, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{"manifest":"apiVersion: v1\nkind: Secret\nmetadata:\n  name: token\nstringData:\n  value: cleartext\n"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "Secret 专用入口") {
		t.Fatalf("error = %v", err)
	}
	if len(writer.inputs) != 0 {
		t.Fatalf("Secret reached manifest service: %+v", writer.inputs)
	}
}

func TestManifestDocumentPermissionDenialIsStructured(t *testing.T) {
	t.Parallel()
	result := manifestPreviewResult(kubernetesresource.ManifestRequirementRBACManage)
	result.Allowed = false
	result.Documents[0].Status = kubernetesmanifest.StatusRefused
	result.Documents[0].Err = kubernetesresource.ErrManifestForbidden
	result.Documents = append(result.Documents, kubernetesmanifest.Document{
		Index: 1, APIVersion: "v1", Kind: "ConfigMap", Namespace: "team-a", Name: "config",
		Action: kubernetesmanifest.ActionUpdate, Status: kubernetesmanifest.StatusPlanned,
		Requirement: kubernetesresource.ManifestRequirementResourceUpdate,
	})
	writer := &recordingManifestWriter{result: result}
	catalogue := manifestCatalogue(writer, permissionScope{
		staticScopeResolver: ordinaryScope(), allowed: map[rbac.Permission]bool{},
	})
	toolResult, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolPreviewManifestApply, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{"manifest":"apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata:\n  name: reader\n  namespace: team-a\nrules: []\n"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !toolResult.Failed || !toolResult.Denied || !strings.Contains(toolResult.Text, "rbac_manage") {
		t.Fatalf("result = %+v", toolResult)
	}
	if len(toolResult.AuditTargets) != 2 || toolResult.AuditTargets[0].Result != "denied" ||
		toolResult.AuditTargets[0].MissingPermission != string(rbac.PermissionClusterRBACManage) {
		t.Fatalf("audit targets = %+v", toolResult.AuditTargets)
	}
	if toolResult.AuditTargets[1].Result != "failed" {
		t.Fatalf("unexecuted permitted document audit = %+v", toolResult.AuditTargets[1])
	}
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
