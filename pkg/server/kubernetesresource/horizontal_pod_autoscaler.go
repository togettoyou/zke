package kubernetesresource

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

var horizontalPodAutoscalerIdentity = ResourceIdentity{
	Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers",
}

const (
	maxHPAMetrics     = 16
	maxHPAPolicies    = 16
	maxHPAReplicas    = 1_000_000
	maxHPAAnnotations = 256 * 1024
)

type ListHorizontalPodAutoscalersInput struct {
	ClusterID     string
	Namespace     string
	Limit         int64
	ContinueToken string
	LabelSelector string
	FieldSelector string
}

type HorizontalPodAutoscalerPage struct {
	Autoscalers        []HorizontalPodAutoscalerSummary `json:"autoscalers"`
	ContinueToken      string                           `json:"continue_token"`
	ResourceVersion    string                           `json:"resource_version"`
	RemainingItemCount *int64                           `json:"remaining_item_count"`
}

type HPAScaleTarget struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

type HorizontalPodAutoscalerSummary struct {
	Namespace          string            `json:"namespace"`
	Name               string            `json:"name"`
	UID                string            `json:"uid"`
	ResourceVersion    string            `json:"resource_version"`
	Generation         int64             `json:"generation"`
	ObservedGeneration *int64            `json:"observed_generation"`
	CreationTimestamp  time.Time         `json:"creation_timestamp"`
	Labels             map[string]string `json:"labels"`
	Target             HPAScaleTarget    `json:"target"`
	MinReplicas        int32             `json:"min_replicas"`
	MaxReplicas        int32             `json:"max_replicas"`
	CurrentReplicas    int32             `json:"current_replicas"`
	DesiredReplicas    int32             `json:"desired_replicas"`
	MetricCount        int               `json:"metric_count"`
	AbleToScale        bool              `json:"able_to_scale"`
	ScalingActive      bool              `json:"scaling_active"`
	ScalingLimited     bool              `json:"scaling_limited"`
	LastScaleTime      *time.Time        `json:"last_scale_time"`
}

type HorizontalPodAutoscalerDetail struct {
	HorizontalPodAutoscalerSummary
	Annotations    map[string]string `json:"annotations"`
	Metrics        []HPAMetricView   `json:"metrics"`
	CurrentMetrics []HPAMetricView   `json:"current_metrics"`
	Behavior       *HPABehavior      `json:"behavior,omitempty"`
	Conditions     []HPACondition    `json:"conditions"`
}

type HPACondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastTransitionTime time.Time `json:"last_transition_time"`
}

type HPAMetricTarget struct {
	Type               string `json:"type"`
	AverageUtilization *int32 `json:"average_utilization,omitempty"`
	AverageValue       string `json:"average_value"`
	Value              string `json:"value"`
}

type HPAMetricIdentifier struct {
	Name     string            `json:"name"`
	Selector *WorkloadSelector `json:"selector,omitempty"`
}

type HPAMetricView struct {
	Type            string               `json:"type"`
	Name            string               `json:"name"`
	Container       string               `json:"container"`
	Target          HPAMetricTarget      `json:"target"`
	Current         HPAMetricTarget      `json:"current"`
	DescribedObject *HPAScaleTarget      `json:"described_object,omitempty"`
	Metric          *HPAMetricIdentifier `json:"metric,omitempty"`
}

type HPAResourceMetricSpec struct {
	Name      string          `json:"name"`
	Container string          `json:"container"`
	Target    HPAMetricTarget `json:"target"`
}

type HPAMetricSpec struct {
	Type              string                 `json:"type"`
	Resource          *HPAResourceMetricSpec `json:"resource,omitempty"`
	ContainerResource *HPAResourceMetricSpec `json:"container_resource,omitempty"`
}

type HPAScalingPolicy struct {
	Type          string `json:"type"`
	Value         int32  `json:"value"`
	PeriodSeconds int32  `json:"period_seconds"`
}

type HPAScalingRules struct {
	StabilizationWindowSeconds *int32             `json:"stabilization_window_seconds,omitempty"`
	SelectPolicy               string             `json:"select_policy"`
	Policies                   []HPAScalingPolicy `json:"policies"`
}

type HPABehavior struct {
	ScaleUp   *HPAScalingRules `json:"scale_up,omitempty"`
	ScaleDown *HPAScalingRules `json:"scale_down,omitempty"`
}

type HorizontalPodAutoscalerSpec struct {
	Target      HPAScaleTarget  `json:"target"`
	MinReplicas *int32          `json:"min_replicas,omitempty"`
	MaxReplicas int32           `json:"max_replicas"`
	Metrics     []HPAMetricSpec `json:"metrics"`
	Behavior    *HPABehavior    `json:"behavior,omitempty"`
}

type CreateHorizontalPodAutoscalerInput struct {
	ClusterID      string
	Namespace      string
	Name           string
	Labels         map[string]string
	Annotations    map[string]string
	Spec           HorizontalPodAutoscalerSpec
	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

type UpdateHorizontalPodAutoscalerInput struct {
	ClusterID       string
	Namespace       string
	Name            string
	UID             string
	ResourceVersion string
	Spec            HorizontalPodAutoscalerSpec
	DryRun          bool
	Confirm         bool
	IdempotencyKey  string
}

type DeleteHorizontalPodAutoscalerInput struct {
	ClusterID       string
	Namespace       string
	Name            string
	UID             string
	ResourceVersion string
	DryRun          bool
	Confirm         bool
	IdempotencyKey  string
}

func HorizontalPodAutoscalerResourceIdentity() ResourceIdentity {
	return horizontalPodAutoscalerIdentity
}

func (service *Service) ListHorizontalPodAutoscalers(
	ctx context.Context,
	input ListHorizontalPodAutoscalersInput,
) (HorizontalPodAutoscalerPage, error) {
	if !validHPANamespace(input.Namespace) {
		return HorizontalPodAutoscalerPage{}, ErrInvalidInput
	}
	page, err := service.ListResources(ctx, ListResourcesInput{
		ClusterID: input.ClusterID, Resource: horizontalPodAutoscalerIdentity, Namespace: input.Namespace,
		Limit: input.Limit, ContinueToken: input.ContinueToken,
		LabelSelector: input.LabelSelector, FieldSelector: input.FieldSelector,
	})
	if err != nil {
		return HorizontalPodAutoscalerPage{}, err
	}
	result := HorizontalPodAutoscalerPage{
		Autoscalers:   make([]HorizontalPodAutoscalerSummary, 0, len(page.Items)),
		ContinueToken: page.ContinueToken, ResourceVersion: page.ResourceVersion,
		RemainingItemCount: page.RemainingItemCount,
	}
	for _, item := range page.Items {
		detail, err := horizontalPodAutoscalerDetail(item, input.Namespace, "")
		if err != nil {
			return HorizontalPodAutoscalerPage{}, err
		}
		result.Autoscalers = append(result.Autoscalers, detail.HorizontalPodAutoscalerSummary)
	}
	return result, nil
}

func (service *Service) GetHorizontalPodAutoscaler(
	ctx context.Context,
	clusterID, namespace, name string,
) (HorizontalPodAutoscalerDetail, error) {
	if !validHPAIdentity(namespace, name) {
		return HorizontalPodAutoscalerDetail{}, ErrInvalidInput
	}
	object, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: clusterID, Resource: horizontalPodAutoscalerIdentity, Namespace: namespace, Name: name,
	})
	if err != nil {
		return HorizontalPodAutoscalerDetail{}, err
	}
	return horizontalPodAutoscalerDetail(object, namespace, name)
}

func (service *Service) CreateHorizontalPodAutoscaler(
	ctx context.Context,
	input CreateHorizontalPodAutoscalerInput,
) (HorizontalPodAutoscalerDetail, error) {
	object, err := createHorizontalPodAutoscalerObject(input)
	if err != nil {
		return HorizontalPodAutoscalerDetail{}, err
	}
	result, err := service.CreateResource(ctx, CreateResourceInput{
		ClusterID: input.ClusterID, Resource: horizontalPodAutoscalerIdentity, Namespace: input.Namespace,
		Object: object, Options: MutationOptions{DryRun: input.DryRun}, Confirm: input.Confirm,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return HorizontalPodAutoscalerDetail{}, err
	}
	return horizontalPodAutoscalerDetail(result, input.Namespace, input.Name)
}

func (service *Service) UpdateHorizontalPodAutoscaler(
	ctx context.Context,
	input UpdateHorizontalPodAutoscalerInput,
) (HorizontalPodAutoscalerDetail, error) {
	if !validHPAMutationIdentity(input.Namespace, input.Name, input.UID, input.ResourceVersion) {
		return HorizontalPodAutoscalerDetail{}, ErrInvalidInput
	}
	existing, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: input.ClusterID, Resource: horizontalPodAutoscalerIdentity,
		Namespace: input.Namespace, Name: input.Name,
	})
	if err != nil {
		return HorizontalPodAutoscalerDetail{}, err
	}
	current := &unstructured.Unstructured{Object: existing}
	if string(current.GetUID()) != input.UID || current.GetResourceVersion() != input.ResourceVersion {
		return HorizontalPodAutoscalerDetail{}, ErrUpstreamConflict
	}
	updated, err := updateHorizontalPodAutoscalerObject(existing, input)
	if err != nil {
		return HorizontalPodAutoscalerDetail{}, err
	}
	result, err := service.UpdateResource(ctx, UpdateResourceInput{
		ClusterID: input.ClusterID, Resource: horizontalPodAutoscalerIdentity,
		Namespace: input.Namespace, Name: input.Name, Object: updated,
		Options: MutationOptions{DryRun: input.DryRun}, Confirm: input.Confirm,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return HorizontalPodAutoscalerDetail{}, err
	}
	return horizontalPodAutoscalerDetail(result, input.Namespace, input.Name)
}

func (service *Service) DeleteHorizontalPodAutoscaler(ctx context.Context, input DeleteHorizontalPodAutoscalerInput) error {
	if !validHPAMutationIdentity(input.Namespace, input.Name, input.UID, input.ResourceVersion) {
		return ErrInvalidInput
	}
	return service.DeleteResource(ctx, DeleteResourceInput{
		ClusterID: input.ClusterID, Resource: horizontalPodAutoscalerIdentity,
		Namespace: input.Namespace, Name: input.Name, DryRun: input.DryRun, Confirm: input.Confirm,
		Preconditions:  DeletePreconditions{UID: input.UID, ResourceVersion: input.ResourceVersion},
		Propagation:    agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND,
		IdempotencyKey: input.IdempotencyKey,
	})
}

func createHorizontalPodAutoscalerObject(input CreateHorizontalPodAutoscalerInput) (map[string]any, error) {
	if !validHPAMetadata(input.Namespace, input.Name, input.Labels, input.Annotations) {
		return nil, ErrInvalidInput
	}
	spec, err := horizontalPodAutoscalerKubernetesSpec(input.Spec)
	if err != nil {
		return nil, err
	}
	object := &autoscalingv2.HorizontalPodAutoscaler{
		TypeMeta: metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"},
		ObjectMeta: metav1.ObjectMeta{
			Name: input.Name, Namespace: input.Namespace,
			Labels: maps.Clone(input.Labels), Annotations: maps.Clone(input.Annotations),
		},
		Spec: spec,
	}
	return typedHPAObject(object, ErrInvalidInput)
}

func updateHorizontalPodAutoscalerObject(existing map[string]any, input UpdateHorizontalPodAutoscalerInput) (map[string]any, error) {
	body, err := json.Marshal(existing)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	var object autoscalingv2.HorizontalPodAutoscaler
	if json.Unmarshal(body, &object) != nil || !validTypedHPAIdentity(object, input.Namespace, input.Name) {
		return nil, ErrInvalidResponse
	}
	spec, err := horizontalPodAutoscalerKubernetesSpec(input.Spec)
	if err != nil {
		return nil, err
	}
	object.Spec = spec
	return typedHPAObject(&object, ErrInvalidResponse)
}

func horizontalPodAutoscalerKubernetesSpec(input HorizontalPodAutoscalerSpec) (autoscalingv2.HorizontalPodAutoscalerSpec, error) {
	if !validHPATarget(input.Target) || input.MaxReplicas < 1 || input.MaxReplicas > maxHPAReplicas ||
		input.MinReplicas != nil && (*input.MinReplicas < 1 || *input.MinReplicas > input.MaxReplicas) ||
		len(input.Metrics) > maxHPAMetrics {
		return autoscalingv2.HorizontalPodAutoscalerSpec{}, ErrInvalidInput
	}
	metrics := make([]autoscalingv2.MetricSpec, 0, len(input.Metrics))
	for _, metric := range input.Metrics {
		converted, err := hpaKubernetesMetric(metric)
		if err != nil {
			return autoscalingv2.HorizontalPodAutoscalerSpec{}, err
		}
		metrics = append(metrics, converted)
	}
	behavior, err := hpaKubernetesBehavior(input.Behavior)
	if err != nil {
		return autoscalingv2.HorizontalPodAutoscalerSpec{}, err
	}
	return autoscalingv2.HorizontalPodAutoscalerSpec{
		ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
			APIVersion: input.Target.APIVersion, Kind: input.Target.Kind, Name: input.Target.Name,
		},
		MinReplicas: cloneInt32Pointer(input.MinReplicas), MaxReplicas: input.MaxReplicas,
		Metrics: metrics, Behavior: behavior,
	}, nil
}

func hpaKubernetesMetric(input HPAMetricSpec) (autoscalingv2.MetricSpec, error) {
	if input.Type == "Resource" && input.Resource != nil && input.ContainerResource == nil {
		metric, err := hpaKubernetesResourceMetric(*input.Resource, false)
		if err != nil {
			return autoscalingv2.MetricSpec{}, err
		}
		return autoscalingv2.MetricSpec{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{Name: corev1.ResourceName(metric.Name), Target: metric.Target}}, nil
	}
	if input.Type == "ContainerResource" && input.Resource == nil && input.ContainerResource != nil {
		metric, err := hpaKubernetesResourceMetric(*input.ContainerResource, true)
		if err != nil {
			return autoscalingv2.MetricSpec{}, err
		}
		return autoscalingv2.MetricSpec{Type: autoscalingv2.ContainerResourceMetricSourceType, ContainerResource: &autoscalingv2.ContainerResourceMetricSource{Name: corev1.ResourceName(metric.Name), Container: metric.Container, Target: metric.Target}}, nil
	}
	return autoscalingv2.MetricSpec{}, ErrInvalidInput
}

type hpaConvertedResourceMetric struct {
	Name      string
	Container string
	Target    autoscalingv2.MetricTarget
}

func hpaKubernetesResourceMetric(input HPAResourceMetricSpec, requireContainer bool) (hpaConvertedResourceMetric, error) {
	if len(k8svalidation.IsQualifiedName(input.Name)) != 0 || requireContainer != (input.Container != "") ||
		input.Container != "" && len(k8svalidation.IsDNS1123Label(input.Container)) != 0 {
		return hpaConvertedResourceMetric{}, ErrInvalidInput
	}
	target, err := hpaKubernetesMetricTarget(input.Target, false)
	if err != nil {
		return hpaConvertedResourceMetric{}, err
	}
	return hpaConvertedResourceMetric{Name: input.Name, Container: input.Container, Target: target}, nil
}

func hpaKubernetesMetricTarget(input HPAMetricTarget, allowValue bool) (autoscalingv2.MetricTarget, error) {
	result := autoscalingv2.MetricTarget{Type: autoscalingv2.MetricTargetType(input.Type)}
	switch input.Type {
	case "Utilization":
		if input.AverageUtilization == nil || *input.AverageUtilization < 1 || input.AverageValue != "" || input.Value != "" {
			return autoscalingv2.MetricTarget{}, ErrInvalidInput
		}
		result.AverageUtilization = cloneInt32Pointer(input.AverageUtilization)
	case "AverageValue":
		if input.AverageUtilization != nil || input.Value != "" {
			return autoscalingv2.MetricTarget{}, ErrInvalidInput
		}
		quantity, err := resource.ParseQuantity(input.AverageValue)
		if err != nil || quantity.Sign() <= 0 {
			return autoscalingv2.MetricTarget{}, ErrInvalidInput
		}
		result.AverageValue = &quantity
	case "Value":
		if !allowValue || input.AverageUtilization != nil || input.AverageValue != "" {
			return autoscalingv2.MetricTarget{}, ErrInvalidInput
		}
		quantity, err := resource.ParseQuantity(input.Value)
		if err != nil || quantity.Sign() <= 0 {
			return autoscalingv2.MetricTarget{}, ErrInvalidInput
		}
		result.Value = &quantity
	default:
		return autoscalingv2.MetricTarget{}, ErrInvalidInput
	}
	return result, nil
}

func hpaKubernetesBehavior(input *HPABehavior) (*autoscalingv2.HorizontalPodAutoscalerBehavior, error) {
	if input == nil {
		return nil, nil
	}
	scaleUp, err := hpaKubernetesScalingRules(input.ScaleUp)
	if err != nil {
		return nil, err
	}
	scaleDown, err := hpaKubernetesScalingRules(input.ScaleDown)
	if err != nil {
		return nil, err
	}
	return &autoscalingv2.HorizontalPodAutoscalerBehavior{ScaleUp: scaleUp, ScaleDown: scaleDown}, nil
}

func hpaKubernetesScalingRules(input *HPAScalingRules) (*autoscalingv2.HPAScalingRules, error) {
	if input == nil {
		return nil, nil
	}
	if input.StabilizationWindowSeconds != nil && (*input.StabilizationWindowSeconds < 0 || *input.StabilizationWindowSeconds > 3600) ||
		input.SelectPolicy != "" && input.SelectPolicy != "Max" && input.SelectPolicy != "Min" && input.SelectPolicy != "Disabled" ||
		len(input.Policies) == 0 || len(input.Policies) > maxHPAPolicies {
		return nil, ErrInvalidInput
	}
	policies := make([]autoscalingv2.HPAScalingPolicy, 0, len(input.Policies))
	for _, policy := range input.Policies {
		if policy.Type != "Pods" && policy.Type != "Percent" || policy.Value <= 0 || policy.PeriodSeconds <= 0 || policy.PeriodSeconds > 1800 {
			return nil, ErrInvalidInput
		}
		policies = append(policies, autoscalingv2.HPAScalingPolicy{
			Type: autoscalingv2.HPAScalingPolicyType(policy.Type), Value: policy.Value, PeriodSeconds: policy.PeriodSeconds,
		})
	}
	result := &autoscalingv2.HPAScalingRules{
		StabilizationWindowSeconds: cloneInt32Pointer(input.StabilizationWindowSeconds), Policies: policies,
	}
	if input.SelectPolicy != "" {
		value := autoscalingv2.ScalingPolicySelect(input.SelectPolicy)
		result.SelectPolicy = &value
	}
	return result, nil
}

func horizontalPodAutoscalerDetail(object map[string]any, namespace, name string) (HorizontalPodAutoscalerDetail, error) {
	body, err := json.Marshal(object)
	if err != nil {
		return HorizontalPodAutoscalerDetail{}, ErrInvalidResponse
	}
	var value autoscalingv2.HorizontalPodAutoscaler
	if json.Unmarshal(body, &value) != nil || !validTypedHPAIdentity(value, namespace, name) {
		return HorizontalPodAutoscalerDetail{}, ErrInvalidResponse
	}
	conditions := make([]HPACondition, 0, len(value.Status.Conditions))
	for _, condition := range value.Status.Conditions {
		conditions = append(conditions, HPACondition{
			Type: string(condition.Type), Status: string(condition.Status), Reason: condition.Reason,
			Message: condition.Message, LastTransitionTime: condition.LastTransitionTime.Time,
		})
	}
	return HorizontalPodAutoscalerDetail{
		HorizontalPodAutoscalerSummary: hpaSummary(value),
		Annotations:                    normalizedStringMap(value.Annotations), Metrics: hpaMetricSpecViews(value.Spec.Metrics),
		CurrentMetrics: hpaMetricStatusViews(value.Status.CurrentMetrics), Behavior: hpaBehaviorView(value.Spec.Behavior),
		Conditions: conditions,
	}, nil
}

func hpaSummary(value autoscalingv2.HorizontalPodAutoscaler) HorizontalPodAutoscalerSummary {
	minReplicas := int32(1)
	if value.Spec.MinReplicas != nil {
		minReplicas = *value.Spec.MinReplicas
	}
	return HorizontalPodAutoscalerSummary{
		Namespace: value.Namespace, Name: value.Name, UID: string(value.UID), ResourceVersion: value.ResourceVersion,
		Generation: value.Generation, ObservedGeneration: cloneInt64Pointer(value.Status.ObservedGeneration),
		CreationTimestamp: value.CreationTimestamp.Time, Labels: normalizedStringMap(value.Labels),
		Target:      HPAScaleTarget{APIVersion: value.Spec.ScaleTargetRef.APIVersion, Kind: value.Spec.ScaleTargetRef.Kind, Name: value.Spec.ScaleTargetRef.Name},
		MinReplicas: minReplicas, MaxReplicas: value.Spec.MaxReplicas,
		CurrentReplicas: value.Status.CurrentReplicas, DesiredReplicas: value.Status.DesiredReplicas,
		MetricCount: len(value.Spec.Metrics), AbleToScale: hpaConditionTrue(value.Status.Conditions, autoscalingv2.AbleToScale),
		ScalingActive:  hpaConditionTrue(value.Status.Conditions, autoscalingv2.ScalingActive),
		ScalingLimited: hpaConditionTrue(value.Status.Conditions, autoscalingv2.ScalingLimited),
		LastScaleTime:  optionalTimePointer(value.Status.LastScaleTime),
	}
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func hpaConditionTrue(conditions []autoscalingv2.HorizontalPodAutoscalerCondition, conditionType autoscalingv2.HorizontalPodAutoscalerConditionType) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func hpaMetricSpecViews(values []autoscalingv2.MetricSpec) []HPAMetricView {
	result := make([]HPAMetricView, 0, len(values))
	for _, value := range values {
		view := HPAMetricView{Type: string(value.Type)}
		switch {
		case value.Resource != nil:
			view.Name, view.Target = string(value.Resource.Name), hpaMetricTargetView(value.Resource.Target)
		case value.ContainerResource != nil:
			view.Name, view.Container = string(value.ContainerResource.Name), value.ContainerResource.Container
			view.Target = hpaMetricTargetView(value.ContainerResource.Target)
		case value.Pods != nil:
			view.Metric, view.Target = hpaMetricIdentifierView(value.Pods.Metric), hpaMetricTargetView(value.Pods.Target)
		case value.Object != nil:
			view.Metric, view.Target = hpaMetricIdentifierView(value.Object.Metric), hpaMetricTargetView(value.Object.Target)
			view.DescribedObject = &HPAScaleTarget{APIVersion: value.Object.DescribedObject.APIVersion, Kind: value.Object.DescribedObject.Kind, Name: value.Object.DescribedObject.Name}
		case value.External != nil:
			view.Metric, view.Target = hpaMetricIdentifierView(value.External.Metric), hpaMetricTargetView(value.External.Target)
		}
		result = append(result, view)
	}
	return result
}

func hpaMetricStatusViews(values []autoscalingv2.MetricStatus) []HPAMetricView {
	result := make([]HPAMetricView, 0, len(values))
	for _, value := range values {
		view := HPAMetricView{Type: string(value.Type)}
		switch {
		case value.Resource != nil:
			view.Name, view.Current = string(value.Resource.Name), hpaCurrentMetricTargetView(value.Resource.Current)
		case value.ContainerResource != nil:
			view.Name, view.Container = string(value.ContainerResource.Name), value.ContainerResource.Container
			view.Current = hpaCurrentMetricTargetView(value.ContainerResource.Current)
		case value.Pods != nil:
			view.Metric, view.Current = hpaMetricIdentifierView(value.Pods.Metric), hpaCurrentMetricTargetView(value.Pods.Current)
		case value.Object != nil:
			view.Metric, view.Current = hpaMetricIdentifierView(value.Object.Metric), hpaCurrentMetricTargetView(value.Object.Current)
			view.DescribedObject = &HPAScaleTarget{APIVersion: value.Object.DescribedObject.APIVersion, Kind: value.Object.DescribedObject.Kind, Name: value.Object.DescribedObject.Name}
		case value.External != nil:
			view.Metric, view.Current = hpaMetricIdentifierView(value.External.Metric), hpaCurrentMetricTargetView(value.External.Current)
		}
		result = append(result, view)
	}
	return result
}

func hpaMetricTargetView(value autoscalingv2.MetricTarget) HPAMetricTarget {
	result := HPAMetricTarget{Type: string(value.Type), AverageUtilization: cloneInt32Pointer(value.AverageUtilization)}
	if value.AverageValue != nil {
		result.AverageValue = value.AverageValue.String()
	}
	if value.Value != nil {
		result.Value = value.Value.String()
	}
	return result
}

func hpaCurrentMetricTargetView(value autoscalingv2.MetricValueStatus) HPAMetricTarget {
	result := HPAMetricTarget{AverageUtilization: cloneInt32Pointer(value.AverageUtilization)}
	if value.AverageValue != nil {
		result.Type, result.AverageValue = "AverageValue", value.AverageValue.String()
	}
	if value.Value != nil {
		result.Type, result.Value = "Value", value.Value.String()
	}
	if value.AverageUtilization != nil {
		result.Type = "Utilization"
	}
	return result
}

func hpaMetricIdentifierView(value autoscalingv2.MetricIdentifier) *HPAMetricIdentifier {
	return &HPAMetricIdentifier{Name: value.Name, Selector: workloadSelector(value.Selector)}
}

func hpaBehaviorView(value *autoscalingv2.HorizontalPodAutoscalerBehavior) *HPABehavior {
	if value == nil {
		return nil
	}
	return &HPABehavior{ScaleUp: hpaScalingRulesView(value.ScaleUp), ScaleDown: hpaScalingRulesView(value.ScaleDown)}
}

func hpaScalingRulesView(value *autoscalingv2.HPAScalingRules) *HPAScalingRules {
	if value == nil {
		return nil
	}
	result := &HPAScalingRules{
		StabilizationWindowSeconds: cloneInt32Pointer(value.StabilizationWindowSeconds), Policies: make([]HPAScalingPolicy, 0, len(value.Policies)),
	}
	if value.SelectPolicy != nil {
		result.SelectPolicy = string(*value.SelectPolicy)
	}
	for _, policy := range value.Policies {
		result.Policies = append(result.Policies, HPAScalingPolicy{Type: string(policy.Type), Value: policy.Value, PeriodSeconds: policy.PeriodSeconds})
	}
	return result
}

func validHPAIdentity(namespace, name string) bool {
	return validHPANamespace(namespace) && len(k8svalidation.IsDNS1123Subdomain(name)) == 0
}

func validHPANamespace(namespace string) bool {
	return len(k8svalidation.IsDNS1123Label(namespace)) == 0
}

func validHPAMutationIdentity(namespace, name, uid, resourceVersion string) bool {
	return validHPAIdentity(namespace, name) && strings.TrimSpace(uid) != "" && len(uid) <= 128 &&
		strings.TrimSpace(resourceVersion) != "" && len(resourceVersion) <= 256
}

func validHPAMetadata(namespace, name string, labels, annotations map[string]string) bool {
	if !validHPAIdentity(namespace, name) || !validNamespaceLabels(labels) {
		return false
	}
	total := 0
	for key, value := range annotations {
		if len(k8svalidation.IsQualifiedName(key)) != 0 {
			return false
		}
		total += len(key) + len(value)
		if total > maxHPAAnnotations {
			return false
		}
	}
	return true
}

func validHPATarget(value HPAScaleTarget) bool {
	return value.APIVersion == "apps/v1" && (value.Kind == "Deployment" || value.Kind == "StatefulSet") &&
		len(k8svalidation.IsDNS1123Subdomain(value.Name)) == 0
}

func validTypedHPAIdentity(value autoscalingv2.HorizontalPodAutoscaler, namespace, name string) bool {
	return value.APIVersion == "autoscaling/v2" && value.Kind == "HorizontalPodAutoscaler" &&
		value.Namespace == namespace && value.Name != "" && (name == "" || value.Name == name)
}

func typedHPAObject(object *autoscalingv2.HorizontalPodAutoscaler, failure error) (map[string]any, error) {
	body, err := json.Marshal(object)
	if err != nil {
		return nil, failure
	}
	var result unstructured.Unstructured
	if result.UnmarshalJSON(body) != nil {
		return nil, failure
	}
	return result.Object, nil
}
