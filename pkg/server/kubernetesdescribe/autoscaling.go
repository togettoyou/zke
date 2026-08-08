package kubernetesdescribe

import (
	"context"
	"fmt"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

type HorizontalPodAutoscalerInput struct {
	ClusterID string
	Namespace string
	Name      string
}

// DescribeHorizontalPodAutoscaler joins the HPA controller status with the
// scale target it manages. The target read is deliberately limited to the
// apps/v1 workload kinds supported by the typed HPA editor; custom scale
// targets remain valid and are described from the HPA's own Conditions.
func (service *Service) DescribeHorizontalPodAutoscaler(
	ctx context.Context,
	input HorizontalPodAutoscalerInput,
) (Result, error) {
	if service == nil || service.resources == nil {
		return Result{}, ErrInvalidInput
	}
	autoscaler, err := service.resources.GetHorizontalPodAutoscaler(
		ctx, input.ClusterID, input.Namespace, input.Name,
	)
	if err != nil {
		return Result{}, err
	}
	if autoscaler.UID == "" {
		return Result{}, kubernetesresource.ErrInvalidResponse
	}
	result := Result{
		Target: Target{
			APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler",
			Namespace: autoscaler.Namespace, Name: autoscaler.Name,
			UID: autoscaler.UID, ResourceVersion: autoscaler.ResourceVersion,
		},
		Family: FamilyAutoscaling, Autoscaler: &autoscaler,
		Findings: hpaFindings(autoscaler), DegradedSections: []string{},
	}

	if resource, known := hpaWorkloadTarget(autoscaler.Target); known {
		workload, targetErr := service.resources.GetWorkload(
			ctx, input.ClusterID, input.Namespace, resource, autoscaler.Target.Name,
		)
		if targetErr != nil {
			result.DegradedSections = append(result.DegradedSections, "autoscaler.target")
		} else {
			findings := workloadFindings(workload, nil)
			result.AutoscalerTarget = &RelatedObject{
				Kind: workload.Kind, Name: workload.Name, UID: workload.UID,
				Namespace: workload.Namespace, Status: hpaTargetStatus(workload),
				Ready: len(findings) == 0 && hpaTargetReady(workload), Findings: findings,
			}
		}
	}

	result.Events, _ = service.objectEvents(ctx, input.ClusterID, result.Target)
	if result.Events.Omitted == EventsOmittedUnavailable {
		result.DegradedSections = append(result.DegradedSections, "events")
	}
	return result, nil
}

func hpaWorkloadTarget(
	target kubernetesresource.HPAScaleTarget,
) (kubernetesresource.WorkloadResource, bool) {
	if target.APIVersion != "apps/v1" {
		return "", false
	}
	switch target.Kind {
	case "Deployment":
		return kubernetesresource.WorkloadDeployments, true
	case "StatefulSet":
		return kubernetesresource.WorkloadStatefulSets, true
	default:
		return "", false
	}
}

func hpaTargetStatus(workload kubernetesresource.WorkloadDetail) string {
	if workload.Replicas == nil {
		return workload.Status
	}
	return fmt.Sprintf("%d/%d", workload.Replicas.Ready, workload.Replicas.Desired)
}

func hpaTargetReady(workload kubernetesresource.WorkloadDetail) bool {
	if workload.Replicas == nil {
		return workload.Status == "complete" || workload.Status == "scheduled"
	}
	return workload.Replicas.Ready >= workload.Replicas.Desired
}
