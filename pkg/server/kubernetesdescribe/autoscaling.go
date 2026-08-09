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

func (service *Service) describeVerticalPodAutoscaler(
	ctx context.Context,
	input ResourceInput,
) (Result, error) {
	autoscaler, err := service.resources.GetVerticalPodAutoscaler(
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
			APIVersion: "autoscaling.k8s.io/v1", Kind: "VerticalPodAutoscaler",
			Namespace: autoscaler.Namespace, Name: autoscaler.Name,
			UID: autoscaler.UID, ResourceVersion: autoscaler.ResourceVersion,
		},
		Family: FamilyAutoscaling, VerticalPodAutoscaler: &autoscaler,
		Findings: vpaFindings(autoscaler), DegradedSections: []string{},
	}
	service.addAutoscalingTarget(ctx, input.ClusterID, autoscaler.Namespace, autoscaler.Target, true, &result)
	result.Events, _ = service.objectEvents(ctx, input.ClusterID, result.Target)
	if result.Events.Omitted == EventsOmittedUnavailable {
		result.DegradedSections = append(result.DegradedSections, "events")
	}
	return result, nil
}

func (service *Service) describeKEDAScaledObject(
	ctx context.Context,
	input ResourceInput,
) (Result, error) {
	autoscaler, err := service.resources.GetKEDAScaledObject(
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
			APIVersion: "keda.sh/v1alpha1", Kind: "ScaledObject",
			Namespace: autoscaler.Namespace, Name: autoscaler.Name,
			UID: autoscaler.UID, ResourceVersion: autoscaler.ResourceVersion,
		},
		Family: FamilyAutoscaling, KEDAScaledObject: &autoscaler,
		Findings: kedaFindings(autoscaler), DegradedSections: []string{},
	}
	service.addAutoscalingTarget(ctx, input.ClusterID, autoscaler.Namespace, autoscaler.Target, false, &result)
	result.Events, _ = service.objectEvents(ctx, input.ClusterID, result.Target)
	if result.Events.Omitted == EventsOmittedUnavailable {
		result.DegradedSections = append(result.DegradedSections, "events")
	}
	return result, nil
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

	service.addAutoscalingTarget(ctx, input.ClusterID, input.Namespace, kubernetesresource.AutoscalingTarget{
		APIVersion: autoscaler.Target.APIVersion,
		Kind:       autoscaler.Target.Kind,
		Name:       autoscaler.Target.Name,
	}, false, &result)

	result.Events, _ = service.objectEvents(ctx, input.ClusterID, result.Target)
	if result.Events.Omitted == EventsOmittedUnavailable {
		result.DegradedSections = append(result.DegradedSections, "events")
	}
	return result, nil
}

func (service *Service) addAutoscalingTarget(
	ctx context.Context,
	clusterID string,
	namespace string,
	target kubernetesresource.AutoscalingTarget,
	allowDaemonSet bool,
	result *Result,
) {
	resource, known := autoscalingWorkloadTarget(target, allowDaemonSet)
	if !known {
		return
	}
	workload, err := service.resources.GetWorkload(
		ctx, clusterID, namespace, resource, target.Name,
	)
	if err != nil {
		result.DegradedSections = append(result.DegradedSections, "autoscaler.target")
		return
	}
	findings := workloadFindings(workload, nil)
	result.AutoscalerTarget = &RelatedObject{
		Kind: workload.Kind, Name: workload.Name, UID: workload.UID,
		Namespace: workload.Namespace, Status: hpaTargetStatus(workload),
		Ready: len(findings) == 0 && hpaTargetReady(workload), Findings: findings,
	}
}

func autoscalingWorkloadTarget(
	target kubernetesresource.AutoscalingTarget,
	allowDaemonSet bool,
) (kubernetesresource.WorkloadResource, bool) {
	if target.APIVersion != "apps/v1" {
		return "", false
	}
	switch target.Kind {
	case "Deployment":
		return kubernetesresource.WorkloadDeployments, true
	case "StatefulSet":
		return kubernetesresource.WorkloadStatefulSets, true
	case "DaemonSet":
		return kubernetesresource.WorkloadDaemonSets, allowDaemonSet
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
