package kubernetesdescribe

import (
	"errors"
	"testing"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

func TestDescribeHorizontalPodAutoscalerReportsControllerConditionsAndTarget(t *testing.T) {
	t.Parallel()

	observed := int64(6)
	access := &fakeResourceAccess{
		autoscaler: kubernetesresource.HorizontalPodAutoscalerDetail{
			HorizontalPodAutoscalerSummary: kubernetesresource.HorizontalPodAutoscalerSummary{
				Namespace: "models", Name: "inference", UID: "hpa-uid",
				ResourceVersion: "41", Generation: 7, ObservedGeneration: &observed,
				Target: kubernetesresource.HPAScaleTarget{
					APIVersion: "apps/v1", Kind: "Deployment", Name: "inference",
				},
			},
			Conditions: []kubernetesresource.HPACondition{
				{Type: "AbleToScale", Status: "False", Reason: "FailedGetScale", Message: "the target deployment was not found"},
				{Type: "ScalingActive", Status: "False", Reason: "FailedGetResourceMetric", Message: "did not receive metrics"},
				{Type: "ScalingLimited", Status: "True", Reason: "TooManyReplicas", Message: "desired count is above the maximum"},
			},
		},
		workload: kubernetesresource.WorkloadDetail{
			WorkloadSummary: kubernetesresource.WorkloadSummary{
				Resource:   kubernetesresource.WorkloadDeployments,
				APIVersion: "apps/v1", Kind: "Deployment", Namespace: "models",
				Name: "inference", UID: "deployment-uid", Status: "progressing",
				Replicas: &kubernetesresource.WorkloadReplicaStatus{Desired: 4, Ready: 2},
			},
		},
	}
	events := &fakeEventSource{}
	result, err := NewService(access, events, Config{}).DescribeHorizontalPodAutoscaler(
		t.Context(),
		HorizontalPodAutoscalerInput{ClusterID: testClusterID, Namespace: "models", Name: "inference"},
	)
	if err != nil {
		t.Fatalf("describe HPA: %v", err)
	}
	if result.Family != FamilyAutoscaling || result.Autoscaler == nil {
		t.Fatalf("unexpected HPA projection: %+v", result)
	}
	if access.workloadResource != kubernetesresource.WorkloadDeployments || access.workloadName != "inference" {
		t.Fatalf("unexpected target read: resource=%q name=%q", access.workloadResource, access.workloadName)
	}
	if result.AutoscalerTarget == nil || result.AutoscalerTarget.Status != "2/4" || result.AutoscalerTarget.Ready {
		t.Fatalf("unexpected target summary: %+v", result.AutoscalerTarget)
	}
	want := []string{
		FindingHPAStatusStale,
		FindingHPAUnableToScale,
		FindingHPAMetricsUnavailable,
		FindingHPAScalingLimited,
	}
	if len(result.Findings) != len(want) {
		t.Fatalf("unexpected findings: %+v", result.Findings)
	}
	for index, code := range want {
		if result.Findings[index].Code != code {
			t.Fatalf("finding %d = %q, want %q", index, result.Findings[index].Code, code)
		}
	}
	if !events.called || events.input.ResourceUID != "hpa-uid" || events.input.Namespace != "models" {
		t.Fatalf("events were not scoped to the HPA: %+v", events.input)
	}
}

func TestDescribeHorizontalPodAutoscalerDegradesKnownTargetRead(t *testing.T) {
	t.Parallel()

	observed := int64(1)
	access := &fakeResourceAccess{
		autoscaler:  healthyTestAutoscaler(&observed),
		workloadErr: errors.New("agent unavailable"),
	}
	result, err := NewService(access, &fakeEventSource{}, Config{}).DescribeHorizontalPodAutoscaler(
		t.Context(),
		HorizontalPodAutoscalerInput{ClusterID: testClusterID, Namespace: "models", Name: "inference"},
	)
	if err != nil {
		t.Fatalf("describe HPA: %v", err)
	}
	if result.AutoscalerTarget != nil || len(result.DegradedSections) != 1 || result.DegradedSections[0] != "autoscaler.target" {
		t.Fatalf("target failure was not explicit: %+v", result)
	}
}

func TestDescribeHorizontalPodAutoscalerLeavesCustomTargetToConditions(t *testing.T) {
	t.Parallel()

	observed := int64(1)
	autoscaler := healthyTestAutoscaler(&observed)
	autoscaler.Target = kubernetesresource.HPAScaleTarget{
		APIVersion: "example.io/v1", Kind: "WorkerPool", Name: "inference",
	}
	access := &fakeResourceAccess{autoscaler: autoscaler, workloadErr: errors.New("must not be called")}
	result, err := NewService(access, &fakeEventSource{}, Config{}).DescribeHorizontalPodAutoscaler(
		t.Context(),
		HorizontalPodAutoscalerInput{ClusterID: testClusterID, Namespace: "models", Name: "inference"},
	)
	if err != nil {
		t.Fatalf("describe HPA: %v", err)
	}
	if result.AutoscalerTarget != nil || len(result.DegradedSections) != 0 || access.workloadName != "" {
		t.Fatalf("custom target should remain unfetched: %+v", result)
	}
}

func healthyTestAutoscaler(observed *int64) kubernetesresource.HorizontalPodAutoscalerDetail {
	return kubernetesresource.HorizontalPodAutoscalerDetail{
		HorizontalPodAutoscalerSummary: kubernetesresource.HorizontalPodAutoscalerSummary{
			Namespace: "models", Name: "inference", UID: "hpa-uid",
			ResourceVersion: "40", Generation: 1, ObservedGeneration: observed,
			Target: kubernetesresource.HPAScaleTarget{
				APIVersion: "apps/v1", Kind: "StatefulSet", Name: "inference",
			},
		},
		Conditions: []kubernetesresource.HPACondition{
			{Type: "AbleToScale", Status: "True"},
			{Type: "ScalingActive", Status: "True"},
			{Type: "ScalingLimited", Status: "False"},
		},
	}
}
