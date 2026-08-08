package kubernetesdescribe

import (
	"testing"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

func TestDescribeResourceQuotaReportsQuantityAwareExhaustion(t *testing.T) {
	t.Parallel()

	access := &fakeResourceAccess{policy: kubernetesresource.PolicyResourceDetail{
		PolicyResourceSummary: kubernetesresource.PolicyResourceSummary{
			Resource:   kubernetesresource.PolicyResourceQuotas,
			APIVersion: "v1", Kind: "ResourceQuota", Namespace: "models",
			Name: "compute", UID: "quota-uid", ResourceVersion: "22",
			ResourceQuota: &kubernetesresource.ResourceQuotaSummary{
				Hard: map[string]string{"requests.cpu": "1", "pods": "10", "limits.memory": "2Gi"},
				Used: map[string]string{"requests.cpu": "1000m", "pods": "4", "limits.memory": "3Gi"},
			},
		},
	}}
	events := &fakeEventSource{}
	result, err := NewService(access, events, Config{}).DescribePolicy(
		t.Context(),
		PolicyInput{
			ClusterID: testClusterID, Namespace: "models",
			Resource: kubernetesresource.PolicyResourceQuotas, Name: "compute",
		},
	)
	if err != nil {
		t.Fatalf("describe ResourceQuota: %v", err)
	}
	if result.Family != FamilyPolicy || result.Policy == nil || result.PolicyStatus == nil {
		t.Fatalf("unexpected policy projection: %+v", result)
	}
	if len(result.PolicyStatus.QuotaUsage) != 3 {
		t.Fatalf("unexpected quota usage: %+v", result.PolicyStatus.QuotaUsage)
	}
	exhausted := map[string]bool{}
	for _, usage := range result.PolicyStatus.QuotaUsage {
		exhausted[usage.Resource] = usage.Exhausted
	}
	if !exhausted["requests.cpu"] || !exhausted["limits.memory"] || exhausted["pods"] {
		t.Fatalf("quantity comparison was wrong: %+v", result.PolicyStatus.QuotaUsage)
	}
	if len(result.Findings) != 1 || result.Findings[0].Code != FindingResourceQuotaExhausted ||
		len(result.Findings[0].Evidence) != 2 {
		t.Fatalf("unexpected quota finding: %+v", result.Findings)
	}
	if !events.called || events.input.ResourceUID != "quota-uid" {
		t.Fatalf("events were not scoped to the quota: %+v", events.input)
	}
}

func TestDescribePodDisruptionBudgetUsesControllerCondition(t *testing.T) {
	t.Parallel()

	access := &fakeResourceAccess{policy: kubernetesresource.PolicyResourceDetail{
		PolicyResourceSummary: kubernetesresource.PolicyResourceSummary{
			Resource:   kubernetesresource.PolicyDisruptionBudgets,
			APIVersion: "policy/v1", Kind: "PodDisruptionBudget", Namespace: "models",
			Name: "inference", UID: "pdb-uid", ResourceVersion: "24",
			DisruptionBudget: &kubernetesresource.DisruptionBudgetSummary{
				CurrentHealthy: 2, DesiredHealthy: 3, DisruptionsAllowed: 0, ExpectedPods: 3,
			},
		},
		DisruptionBudgetDetail: &kubernetesresource.DisruptionBudgetDetail{
			Conditions: []kubernetesresource.PolicyCondition{{
				Type: "DisruptionAllowed", Status: "False", Reason: "InsufficientPods",
				Message: "not enough healthy pods",
			}},
		},
	}}
	result, err := NewService(access, &fakeEventSource{}, Config{}).DescribePolicy(
		t.Context(),
		PolicyInput{
			ClusterID: testClusterID, Namespace: "models",
			Resource: kubernetesresource.PolicyDisruptionBudgets, Name: "inference",
		},
	)
	if err != nil {
		t.Fatalf("describe PodDisruptionBudget: %v", err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Code != FindingPDBNoDisruptionsAllowed ||
		result.Findings[0].Reason != "InsufficientPods" {
		t.Fatalf("unexpected disruption finding: %+v", result.Findings)
	}
}

func TestDescribePolicyRejectsTypesWithoutDiagnosticStatus(t *testing.T) {
	t.Parallel()

	_, err := NewService(&fakeResourceAccess{}, &fakeEventSource{}, Config{}).DescribePolicy(
		t.Context(),
		PolicyInput{
			ClusterID: testClusterID, Namespace: "models",
			Resource: kubernetesresource.PolicyNetworkPolicies, Name: "default-deny",
		},
	)
	if err != ErrInvalidInput {
		t.Fatalf("expected invalid input, got %v", err)
	}
}
