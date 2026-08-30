package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/kubernetesdescribe"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

type changeReaderFake struct {
	inputs []audit.QueryInput
	now    time.Time
}

func (fake *changeReaderFake) Query(
	_ context.Context, input audit.QueryInput,
) (audit.QueryResult, error) {
	fake.inputs = append(fake.inputs, input)
	if len(input.Actions) == 1 && input.Actions[0] == auditaction.AIToolInvoke {
		return audit.QueryResult{
			Events: []audit.Event{{
				ID: "00000000-0000-4000-8000-000000000002", ActorType: "user",
				ActorUserName: "operator", Action: auditaction.AIToolInvoke,
				TargetType: "ai_session", TargetName: "scale_workload", Result: "succeeded",
				RequestID: "aiops:session:1:2", CreatedAt: fake.now.Add(-time.Minute),
				Detail: map[string]string{
					"mutating": "true", "tool": "scale_workload", "gvk": "apps/v1/Deployment",
					"namespace": "web", "resource_name": "api",
				},
			}},
			Page: pagination.Result{Total: 1},
		}, nil
	}
	return audit.QueryResult{
		Events: []audit.Event{{
			ID: "00000000-0000-4000-8000-000000000001", ActorType: "user",
			ActorUserName: "operator", Action: auditaction.KubernetesResourcePatch,
			TargetType: "kubernetes_resource", TargetName: "apps/v1/Deployment web/api",
			Result: "succeeded", RequestID: "request-1", CreatedAt: fake.now.Add(-2 * time.Minute),
		}},
		Page: pagination.Result{Total: 1},
	}, nil
}

func TestListClusterChangesMergesOrdinaryAndAIOpsMutations(t *testing.T) {
	t.Parallel()
	fake := &changeReaderFake{now: time.Now().UTC()}
	catalogue := New(Dependencies{Changes: fake}, Config{})
	result, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolListClusterChanges, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{"minutes":60,"limit":10}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.inputs) != 2 {
		t.Fatalf("audit queries = %d, want 2", len(fake.inputs))
	}
	if fake.inputs[0].ClusterID != testClusterID || fake.inputs[0].UserID != testUserID ||
		fake.inputs[0].Result != "succeeded" || len(fake.inputs[0].Actions) == 0 {
		t.Fatalf("ordinary change query = %+v", fake.inputs[0])
	}
	if fake.inputs[1].DetailContains["mutating"] != "true" {
		t.Fatalf("AIOps change query = %+v", fake.inputs[1])
	}
	var decoded struct {
		Total   int `json:"total"`
		Changes []struct {
			Action string `json:"action"`
			Tool   string `json:"tool"`
			Target string `json:"target"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(result.Text), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Total != 2 || len(decoded.Changes) != 2 ||
		decoded.Changes[0].Tool != "scale_workload" ||
		decoded.Changes[0].Target != "apps/v1/Deployment web/api" ||
		decoded.Changes[1].Action != auditaction.KubernetesResourcePatch {
		t.Fatalf("timeline = %+v", decoded)
	}
}

type verifyResourcesFake struct{ ResourceReader }

func (verifyResourcesFake) DiscoverResources(
	context.Context, string,
) (kubernetescatalog.Catalog, error) {
	return kubernetescatalog.Catalog{Resources: []kubernetescatalog.Resource{{
		Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment", Namespaced: true,
	}}}, nil
}

type verifyDescribeFake struct {
	DescribeReader
	result kubernetesdescribe.Result
	input  kubernetesdescribe.ResourceInput
}

func (fake *verifyDescribeFake) DescribeResource(
	_ context.Context, input kubernetesdescribe.ResourceInput,
) (kubernetesdescribe.Result, error) {
	fake.input = input
	return fake.result, nil
}

func TestVerifyResourceChangeReportsUnconvergedWorkloadAndRecentWarning(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	replicas := &kubernetesresource.WorkloadReplicaStatus{
		Desired: 3, Current: 3, Updated: 2, Ready: 2, Available: 2, Unavailable: 1,
	}
	describe := &verifyDescribeFake{result: kubernetesdescribe.Result{
		Target: kubernetesdescribe.Target{
			APIVersion: "apps/v1", Kind: "Deployment", Namespace: "web", Name: "api",
			ResourceVersion: "42",
		},
		Family: kubernetesdescribe.FamilyWorkload,
		Workload: &kubernetesresource.WorkloadDetail{WorkloadSummary: kubernetesresource.WorkloadSummary{
			APIVersion: "apps/v1", Kind: "Deployment", Namespace: "web", Name: "api",
			Generation: 7, ObservedGeneration: 6, Status: "progressing", Replicas: replicas,
		}},
		Events: kubernetesdescribe.Events{Items: []kubernetesdescribe.Event{{
			Type: "Warning", Reason: "FailedCreate", Message: "quota exceeded",
			LastSeen:  timePointer(now.Add(-time.Minute)),
			Regarding: kubernetesdescribe.EventSubject{Kind: "ReplicaSet", Name: "api-7"},
		}}},
	}}
	catalogue := New(Dependencies{Resources: verifyResourcesFake{}, Describe: describe}, Config{})
	result, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolVerifyResourceChange, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{"api_version":"apps/v1","kind":"Deployment","namespace":"web","name":"api","changed_at":"` +
			now.Add(-10*time.Minute).Format(time.RFC3339) + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Status  string   `json:"status"`
		Reasons []string `json:"reasons"`
		Rollout struct {
			Generation         int64 `json:"generation"`
			ObservedGeneration int64 `json:"observed_generation"`
		} `json:"rollout"`
	}
	if err := json.Unmarshal([]byte(result.Text), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Status != "warning" || decoded.Rollout.Generation != 7 ||
		decoded.Rollout.ObservedGeneration != 6 ||
		!containsString(decoded.Reasons, "generation_not_observed") ||
		!containsString(decoded.Reasons, "replicas_not_converged") ||
		!containsString(decoded.Reasons, "warning_events_after_change") {
		t.Fatalf("verification = %+v", decoded)
	}
	if describe.input.ClusterID != testClusterID || describe.input.Namespace != "web" ||
		describe.input.Name != "api" {
		t.Fatalf("describe input = %+v", describe.input)
	}
	if len(result.Evidence) != 2 || result.Evidence[0].Kind != aisession.EvidenceResource ||
		result.Evidence[1].Kind != aisession.EvidenceEvent {
		t.Fatalf("evidence = %+v", result.Evidence)
	}
}

func TestChangeVerificationDoesNotPassGenericOrTooFreshEvidence(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	for _, testCase := range []struct {
		name      string
		family    string
		changedAt time.Time
		reason    string
	}{
		{name: "generic", family: kubernetesdescribe.FamilyGeneric, changedAt: now.Add(-5 * time.Minute), reason: "generic_resource_has_no_health_rules"},
		{name: "too fresh", family: kubernetesdescribe.FamilyWorkload, changedAt: now.Add(-10 * time.Second), reason: "observation_window_too_short"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			digest, _ := changeVerificationDigest(kubernetesdescribe.Result{
				Target: kubernetesdescribe.Target{APIVersion: "example.io/v1", Kind: "Widget", Name: "one"},
				Family: testCase.family, Events: kubernetesdescribe.Events{Items: []kubernetesdescribe.Event{}},
			}, testCase.changedAt, now)
			if digest["status"] != "inconclusive" ||
				!containsString(digest["reasons"].([]string), testCase.reason) {
				t.Fatalf("digest = %+v", digest)
			}
		})
	}
}

func TestChangeVerificationAcceptsCompletedJobPod(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	digest, _ := changeVerificationDigest(kubernetesdescribe.Result{
		Target: kubernetesdescribe.Target{APIVersion: "batch/v1", Kind: "Job", Namespace: "jobs", Name: "backup"},
		Family: kubernetesdescribe.FamilyWorkload,
		Workload: &kubernetesresource.WorkloadDetail{WorkloadSummary: kubernetesresource.WorkloadSummary{
			APIVersion: "batch/v1", Kind: "Job", Namespace: "jobs", Name: "backup",
			Generation: 1, ObservedGeneration: 1, Status: "Complete",
		}},
		Related: &kubernetesdescribe.Related{Pods: []kubernetesdescribe.RelatedObject{{
			Kind: "Pod", Name: "backup-abc", Namespace: "jobs", Status: "Succeeded", Ready: false,
		}}},
		Events: kubernetesdescribe.Events{Items: []kubernetesdescribe.Event{}},
	}, now.Add(-5*time.Minute), now)
	if digest["status"] != "passed" || digest["unready_related_objects"] != 0 {
		t.Fatalf("digest = %+v", digest)
	}
}

func TestVerifyResourceChangeRejectsInvalidTime(t *testing.T) {
	t.Parallel()
	catalogue := New(Dependencies{Resources: verifyResourcesFake{}, Describe: &verifyDescribeFake{}}, Config{})
	_, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolVerifyResourceChange, ClusterID: testClusterID, UserID: testUserID,
		Arguments: json.RawMessage(`{"api_version":"apps/v1","kind":"Deployment","namespace":"web","name":"api","changed_at":"yesterday"}`),
	})
	if !errors.Is(err, airuntime.ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
}

func TestChangeToolsDeclareTheirReadPermissions(t *testing.T) {
	t.Parallel()
	catalogue := New(Dependencies{
		Changes: &changeReaderFake{}, Resources: verifyResourcesFake{}, Describe: &verifyDescribeFake{},
	}, Config{})
	specs := catalogue.Specs()
	timeline, ok := changeToolSpec(specs, toolListClusterChanges)
	if !ok || len(timeline.Permissions) != 1 || timeline.Permissions[0] != rbac.PermissionAuditRead {
		t.Fatalf("timeline spec = %+v", timeline)
	}
	verify, ok := changeToolSpec(specs, toolVerifyResourceChange)
	if !ok || len(verify.Permissions) != 2 || verify.Mutating || verify.Sensitive {
		t.Fatalf("verification spec = %+v", verify)
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func changeToolSpec(specs []airuntime.ToolSpec, name string) (airuntime.ToolSpec, bool) {
	for _, spec := range specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return airuntime.ToolSpec{}, false
}
