package aitools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/rbac"
)

type recordingWorkloadWriter struct {
	inputs []kubernetesresource.ScaleWorkloadInput
	result kubernetesresource.WorkloadDetail
	err    error
}

type recordingRevisionWriter struct {
	rollbackInputs []kubernetesresource.RollbackWorkloadInput
	rollbackResult kubernetesresource.WorkloadDetail
	page           kubernetesresource.WorkloadRevisionPage
	err            error
}

func (writer *recordingRevisionWriter) ListWorkloadRevisions(
	context.Context,
	kubernetesresource.ListWorkloadRevisionsInput,
) (kubernetesresource.WorkloadRevisionPage, error) {
	return writer.page, nil
}

func (writer *recordingRevisionWriter) RollbackWorkload(
	_ context.Context,
	input kubernetesresource.RollbackWorkloadInput,
) (kubernetesresource.WorkloadDetail, error) {
	writer.rollbackInputs = append(writer.rollbackInputs, input)
	return writer.rollbackResult, writer.err
}

type staticScopeResolver struct {
	scope rbac.ResolvedScope
	err   error
}

func (resolver staticScopeResolver) ResolveClusterScope(
	context.Context,
	string,
) (rbac.ResolvedScope, error) {
	return resolver.scope, resolver.err
}

func (resolver staticScopeResolver) AuthorizeResolvedCluster(
	context.Context,
	string,
	rbac.Permission,
	rbac.ResolvedScope,
) error {
	return resolver.err
}

func ordinaryScope() staticScopeResolver {
	return staticScopeResolver{scope: rbac.ResolvedScope{
		TenantID:       "6f1f4a2c-f69c-43a3-b2d1-26d352e74ce8",
		ProjectID:      "2e9fdca3-acde-43a5-aed9-977f58f780e3",
		AgentNamespace: "zke-system",
	}}
}

func (writer *recordingWorkloadWriter) ScaleWorkload(
	_ context.Context,
	input kubernetesresource.ScaleWorkloadInput,
) (kubernetesresource.WorkloadDetail, error) {
	writer.inputs = append(writer.inputs, input)
	return writer.result, writer.err
}

func workloadScaleResult() kubernetesresource.WorkloadDetail {
	return kubernetesresource.WorkloadDetail{WorkloadSummary: kubernetesresource.WorkloadSummary{
		Resource:   kubernetesresource.WorkloadDeployments,
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: "team-a", Name: "web",
		UID: "uid-web", ResourceVersion: "43", Generation: 8, ObservedGeneration: 7,
		Status: "progressing",
		Replicas: &kubernetesresource.WorkloadReplicaStatus{
			Desired: 3, Current: 2, Ready: 2, Available: 2, Unavailable: 1,
		},
	}}
}

func TestWorkloadScaleToolsDeclareTheApprovalBoundary(t *testing.T) {
	t.Parallel()
	catalogue := New(Dependencies{
		Workloads: &recordingWorkloadWriter{}, Scopes: ordinaryScope(),
	}, Config{})
	specs := catalogue.Specs()
	if len(specs) != 2 {
		t.Fatalf("Specs() = %+v", specs)
	}
	preview, apply := specs[0], specs[1]
	if preview.Name != toolPreviewWorkloadScale || preview.Mutating {
		t.Fatalf("preview spec = %+v", preview)
	}
	if apply.Name != toolScaleWorkload || !apply.Mutating || !apply.Sensitive {
		t.Fatalf("apply spec = %+v", apply)
	}
	for _, spec := range specs {
		if len(spec.Permissions) != 0 || !slices.Equal(spec.ConditionalPermissions, []rbac.Permission{
			rbac.PermissionClusterResourceUpdate,
			rbac.PermissionClusterSystemNamespaceManage,
			rbac.PermissionClusterAgentNamespaceManage,
		}) {
			t.Fatalf("%s permissions = %+v conditional=%+v", spec.Name, spec.Permissions, spec.ConditionalPermissions)
		}
		if !strings.Contains(string(spec.Schema), `"additionalProperties":false`) {
			t.Fatalf("%s schema accepts undeclared fields: %s", spec.Name, spec.Schema)
		}
	}
}

func TestPreviewWorkloadScaleUsesServerSideDryRun(t *testing.T) {
	t.Parallel()
	writer := &recordingWorkloadWriter{result: workloadScaleResult()}
	catalogue := New(Dependencies{Workloads: writer, Scopes: ordinaryScope()}, Config{})
	result, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolPreviewWorkloadScale, ClusterID: testClusterID, UserID: testUserID,
		Arguments:      json.RawMessage(`{"kind":"Deployment","namespace":"team-a","name":"web","replicas":3}`),
		IdempotencyKey: "aiops:preview",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(writer.inputs) != 1 {
		t.Fatalf("ScaleWorkload inputs = %+v", writer.inputs)
	}
	input := writer.inputs[0]
	if !input.DryRun || input.Confirm || input.Replicas != 3 ||
		input.Resource != kubernetesresource.WorkloadDeployments {
		t.Fatalf("preview input = %+v", input)
	}
	if input.IdempotencyKey != "aiops:preview:dryrun" {
		t.Fatalf("preview idempotency key = %q", input.IdempotencyKey)
	}
	if !strings.Contains(result.Text, "DryRun") || !strings.Contains(result.Text, `"dry_run": true`) {
		t.Fatalf("preview result = %q", result.Text)
	}
	if result.Target == nil || result.Target.Cluster != "" || result.Target.Name != "web" {
		t.Fatalf("preview target = %+v", result.Target)
	}
}

func TestScaleWorkloadConfirmsAndForwardsRuntimeIdempotency(t *testing.T) {
	t.Parallel()
	writer := &recordingWorkloadWriter{result: workloadScaleResult()}
	catalogue := New(Dependencies{Workloads: writer, Scopes: ordinaryScope()}, Config{})
	result, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolScaleWorkload, ClusterID: testClusterID, UserID: testUserID,
		Arguments:      json.RawMessage(`{"kind":"StatefulSet","namespace":"team-a","name":"db","replicas":4}`),
		IdempotencyKey: "aiops:stable-call",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(writer.inputs) != 2 {
		t.Fatalf("write calls = %+v, want DryRun then execution", writer.inputs)
	}
	preview, input := writer.inputs[0], writer.inputs[1]
	if !preview.DryRun || preview.Confirm || preview.IdempotencyKey != "aiops:stable-call:dryrun" {
		t.Fatalf("write preview input = %+v", preview)
	}
	if input.DryRun || !input.Confirm || input.Replicas != 4 ||
		input.Resource != kubernetesresource.WorkloadStatefulSets ||
		input.IdempotencyKey != "aiops:stable-call" {
		t.Fatalf("write input = %+v", input)
	}
	if !strings.Contains(result.Text, "实际伸缩") {
		t.Fatalf("write result = %q", result.Text)
	}
}

func TestWorkloadScaleRefusesUnsupportedTargetsBeforeTheService(t *testing.T) {
	t.Parallel()
	writer := &recordingWorkloadWriter{}
	catalogue := New(Dependencies{Workloads: writer, Scopes: ordinaryScope()}, Config{})
	_, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolScaleWorkload, ClusterID: testClusterID,
		Arguments: json.RawMessage(`{"kind":"DaemonSet","namespace":"default","name":"agents","replicas":3}`),
	})
	if err == nil {
		t.Fatal("DaemonSet scaling was accepted")
	}
	if len(writer.inputs) != 0 {
		t.Fatalf("unsupported target reached service: %+v", writer.inputs)
	}
}

func TestWorkloadScaleUsesProtectedNamespacePermissions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		namespace  string
		permission rbac.Permission
	}{
		{namespace: "kube-system", permission: rbac.PermissionClusterSystemNamespaceManage},
		{namespace: "zke-system", permission: rbac.PermissionClusterAgentNamespaceManage},
	} {
		t.Run(test.namespace, func(t *testing.T) {
			result := workloadScaleResult()
			result.Namespace = test.namespace
			writer := &recordingWorkloadWriter{result: result}
			catalogue := New(Dependencies{
				Workloads: writer,
				Scopes: permissionScope{
					staticScopeResolver: ordinaryScope(),
					allowed:             map[rbac.Permission]bool{test.permission: true},
				},
			}, Config{})
			allowed, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
				Name: toolPreviewWorkloadScale, ClusterID: testClusterID, UserID: testUserID,
				Arguments: json.RawMessage(`{"kind":"Deployment","namespace":"` +
					test.namespace + `","name":"web","replicas":3}`),
			})
			if err != nil || allowed.Failed || len(writer.inputs) != 1 {
				t.Fatalf("namespace %q result=%+v error=%v inputs=%+v", test.namespace, allowed, err, writer.inputs)
			}

			deniedWriter := &recordingWorkloadWriter{}
			deniedCatalogue := New(Dependencies{
				Workloads: deniedWriter,
				Scopes:    permissionScope{staticScopeResolver: ordinaryScope(), allowed: map[rbac.Permission]bool{}},
			}, Config{})
			denied, err := deniedCatalogue.Invoke(context.Background(), airuntime.ToolInvocation{
				Name: toolPreviewWorkloadScale, ClusterID: testClusterID, UserID: testUserID,
				Arguments: json.RawMessage(`{"kind":"Deployment","namespace":"` +
					test.namespace + `","name":"web","replicas":3}`),
			})
			if err != nil || !denied.Denied || len(deniedWriter.inputs) != 0 ||
				len(denied.AuditTargets) != 1 ||
				denied.AuditTargets[0].MissingPermission != string(test.permission) {
				t.Fatalf("namespace %q denied=%+v error=%v inputs=%+v", test.namespace, denied, err, deniedWriter.inputs)
			}
		})
	}
}

func TestPreviewWorkloadRollbackReportsCurrentRevision(t *testing.T) {
	t.Parallel()
	writer := &recordingRevisionWriter{err: kubernetesresource.ErrWorkloadRevisionUnchanged}
	catalogue := New(Dependencies{
		Revisions: writer,
		Resources: &stubResources{pod: map[string]any{}},
		Scopes: permissionScope{
			staticScopeResolver: ordinaryScope(),
			allowed:             map[rbac.Permission]bool{rbac.PermissionClusterAgentNamespaceManage: true},
		},
	}, Config{})
	result, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolPreviewWorkloadRollback, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{"kind":"Deployment","namespace":"zke-system","name":"metrics","revision":1,"uid":"uid","resource_version":"8"}`),
	})
	if err != nil || !result.Failed || result.Denied || !strings.Contains(result.Text, "current=false") {
		t.Fatalf("current revision result=%+v error=%v", result, err)
	}
}

func TestWorkloadScaleAllowsDefaultNamespace(t *testing.T) {
	t.Parallel()
	result := workloadScaleResult()
	result.Namespace = "default"
	writer := &recordingWorkloadWriter{result: result}
	catalogue := New(Dependencies{Workloads: writer, Scopes: ordinaryScope()}, Config{})
	_, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolScaleWorkload, ClusterID: testClusterID, UserID: testUserID,
		Arguments:      json.RawMessage(`{"kind":"Deployment","namespace":"default","name":"web","replicas":3}`),
		IdempotencyKey: "aiops:default-scale",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(writer.inputs) != 2 || writer.inputs[1].Namespace != "default" {
		t.Fatalf("scale inputs = %+v", writer.inputs)
	}
}

func TestWorkloadRollbackUsesBoundPreviewAndRechecksBeforeCommit(t *testing.T) {
	t.Parallel()
	writer := &recordingRevisionWriter{rollbackResult: workloadScaleResult()}
	catalogue := New(Dependencies{
		Revisions: writer,
		Resources: &stubResources{pod: map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": "web", "namespace": "team-a", "uid": "uid-web", "resourceVersion": "8"},
		}},
		Scopes: ordinaryScope(),
	}, Config{})
	arguments := json.RawMessage(`{"kind":"Deployment","namespace":"team-a","name":"web","revision":2,"uid":"uid-web","resource_version":"8"}`)
	preview, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolPreviewWorkloadRollback, ClusterID: testClusterID, UserID: testUserID,
		Arguments: arguments, IdempotencyKey: "aiops:preview",
	})
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(preview.Text, "rollback_")
	if start < 0 {
		t.Fatalf("preview = %q", preview.Text)
	}
	end := start
	for end < len(preview.Text) && ((preview.Text[end] >= 'a' && preview.Text[end] <= 'z') ||
		(preview.Text[end] >= '0' && preview.Text[end] <= '9') || preview.Text[end] == '_') {
		end++
	}
	id := preview.Text[start:end]
	_, err = catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolRollbackWorkload, ClusterID: testClusterID, UserID: testUserID,
		Arguments:      json.RawMessage(`{"preview_id":"` + id + `"}`),
		IdempotencyKey: "aiops:execute",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(writer.rollbackInputs) != 3 {
		t.Fatalf("rollback sequence = %+v", writer.rollbackInputs)
	}
	if !writer.rollbackInputs[0].DryRun || !writer.rollbackInputs[1].DryRun ||
		writer.rollbackInputs[2].DryRun || !writer.rollbackInputs[2].Confirm ||
		writer.rollbackInputs[2].IdempotencyKey != "aiops:execute" {
		t.Fatalf("rollback sequence = %+v", writer.rollbackInputs)
	}
}

func TestWorkloadRollbackUsesProtectedNamespacePermissionAndBecomesSensitive(t *testing.T) {
	t.Parallel()
	writer := &recordingRevisionWriter{rollbackResult: kubernetesresource.WorkloadDetail{
		WorkloadSummary: kubernetesresource.WorkloadSummary{
			APIVersion: "apps/v1", Kind: "Deployment", Namespace: "kube-system",
			Name: "web", UID: "uid-web", ResourceVersion: "8",
		},
	}}
	scope := permissionScope{
		staticScopeResolver: ordinaryScope(),
		allowed: map[rbac.Permission]bool{
			rbac.PermissionClusterSystemNamespaceManage: true,
		},
	}
	catalogue := New(Dependencies{
		Revisions: writer,
		Resources: &stubResources{pod: map[string]any{}},
		Scopes:    scope,
	}, Config{})
	preview, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolPreviewWorkloadRollback, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{"kind":"Deployment","namespace":"kube-system","name":"web","revision":2,"uid":"uid-web","resource_version":"8"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(preview.Text, "rollback_")
	if start < 0 {
		t.Fatalf("preview = %q", preview.Text)
	}
	end := start
	for end < len(preview.Text) && ((preview.Text[end] >= 'a' && preview.Text[end] <= 'z') ||
		(preview.Text[end] >= '0' && preview.Text[end] <= '9') || preview.Text[end] == '_') {
		end++
	}
	id := preview.Text[start:end]
	var rollbackSpec airuntime.ToolSpec
	for _, spec := range catalogue.Specs() {
		if spec.Name == toolRollbackWorkload {
			rollbackSpec = spec
			break
		}
	}
	if id == "" || rollbackSpec.SensitiveWhen == nil ||
		!rollbackSpec.SensitiveWhen(json.RawMessage(`{"preview_id":"`+id+`"}`)) {
		t.Fatalf("preview=%q spec=%+v", preview.Text, rollbackSpec)
	}

	deniedWriter := &recordingRevisionWriter{}
	deniedCatalogue := New(Dependencies{
		Revisions: deniedWriter, Resources: &stubResources{pod: map[string]any{}},
		Scopes: permissionScope{staticScopeResolver: ordinaryScope(), allowed: map[rbac.Permission]bool{}},
	}, Config{})
	denied, err := deniedCatalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolPreviewWorkloadRollback, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{"kind":"Deployment","namespace":"kube-system","name":"web","revision":2,"uid":"uid-web","resource_version":"8"}`),
	})
	if err != nil || !denied.Denied || len(deniedWriter.rollbackInputs) != 0 {
		t.Fatalf("denied=%+v err=%v inputs=%+v", denied, err, deniedWriter.rollbackInputs)
	}
	if len(denied.AuditTargets) != 1 ||
		denied.AuditTargets[0].MissingPermission != string(rbac.PermissionClusterSystemNamespaceManage) {
		t.Fatalf("denied audit targets = %+v", denied.AuditTargets)
	}
}

func TestWorkloadRollbackTreatsDefaultAsOrdinaryNamespace(t *testing.T) {
	t.Parallel()
	writer := &recordingRevisionWriter{rollbackResult: kubernetesresource.WorkloadDetail{
		WorkloadSummary: kubernetesresource.WorkloadSummary{
			APIVersion: "apps/v1", Kind: "Deployment", Namespace: "default",
			Name: "web", UID: "uid-web", ResourceVersion: "8",
		},
	}}
	catalogue := New(Dependencies{
		Revisions: writer, Resources: &stubResources{pod: map[string]any{}},
		Scopes: permissionScope{
			staticScopeResolver: ordinaryScope(),
			allowed:             map[rbac.Permission]bool{rbac.PermissionClusterResourceUpdate: true},
		},
	}, Config{})
	preview, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolPreviewWorkloadRollback, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{"kind":"Deployment","namespace":"default","name":"web","revision":2,"uid":"uid-web","resource_version":"8"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(preview.Text, "rollback_")
	if start < 0 {
		t.Fatalf("preview = %q", preview.Text)
	}
	end := start
	for end < len(preview.Text) && ((preview.Text[end] >= 'a' && preview.Text[end] <= 'z') ||
		(preview.Text[end] >= '0' && preview.Text[end] <= '9') || preview.Text[end] == '_') {
		end++
	}
	id := preview.Text[start:end]
	for _, spec := range catalogue.Specs() {
		if spec.Name == toolRollbackWorkload && spec.SensitiveWhen(json.RawMessage(`{"preview_id":"`+id+`"}`)) {
			t.Fatal("default Namespace rollback was marked sensitive")
		}
	}
}
