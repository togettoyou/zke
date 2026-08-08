package kubernetesresource

import (
	"context"
	"errors"
	"maps"
	"sort"
	"strings"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

var (
	verticalPodAutoscalerIdentity = ResourceIdentity{
		Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers",
	}
	kedaScaledObjectIdentity = ResourceIdentity{
		Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects",
	}
)

const (
	maxAutoscalingExtensionEntries = 64
	maxAutoscalingExtensionBytes   = 256 * 1024
	maxKEDATriggers                = 16
	maxTrendPoints                 = 240
	maxTrendSeries                 = 2000
)

type AutoscalingTarget struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

type AutoscalingExtensionCondition struct {
	Type               string     `json:"type"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason"`
	Message            string     `json:"message"`
	LastTransitionTime *time.Time `json:"last_transition_time,omitempty"`
}

type AutoscalingExtensionListInput struct {
	ClusterID     string
	Namespace     string
	Limit         int64
	ContinueToken string
	LabelSelector string
	FieldSelector string
}

type VPAContainerPolicy struct {
	ContainerName       string            `json:"container_name"`
	Mode                string            `json:"mode"`
	MinAllowed          map[string]string `json:"min_allowed"`
	MaxAllowed          map[string]string `json:"max_allowed"`
	ControlledResources []string          `json:"controlled_resources"`
	ControlledValues    string            `json:"controlled_values"`
}

type VPASpec struct {
	Target            AutoscalingTarget    `json:"target"`
	UpdateMode        string               `json:"update_mode"`
	ContainerPolicies []VPAContainerPolicy `json:"container_policies"`
}

type VPARecommendation struct {
	ContainerName  string            `json:"container_name"`
	Target         map[string]string `json:"target"`
	LowerBound     map[string]string `json:"lower_bound"`
	UpperBound     map[string]string `json:"upper_bound"`
	UncappedTarget map[string]string `json:"uncapped_target"`
}

type VPASummary struct {
	Namespace           string                          `json:"namespace"`
	Name                string                          `json:"name"`
	UID                 string                          `json:"uid"`
	ResourceVersion     string                          `json:"resource_version"`
	Generation          int64                           `json:"generation"`
	ObservedGeneration  int64                           `json:"observed_generation"`
	CreationTimestamp   time.Time                       `json:"creation_timestamp"`
	Labels              map[string]string               `json:"labels"`
	Target              AutoscalingTarget               `json:"target"`
	UpdateMode          string                          `json:"update_mode"`
	RecommendationCount int                             `json:"recommendation_count"`
	Conditions          []AutoscalingExtensionCondition `json:"conditions"`
}

type VPADetail struct {
	VPASummary
	Annotations       map[string]string    `json:"annotations"`
	ContainerPolicies []VPAContainerPolicy `json:"container_policies"`
	Recommendations   []VPARecommendation  `json:"recommendations"`
}

type VPAPage struct {
	Available          bool         `json:"available"`
	UnavailableReason  string       `json:"unavailable_reason,omitempty"`
	Autoscalers        []VPASummary `json:"autoscalers"`
	ContinueToken      string       `json:"continue_token"`
	ResourceVersion    string       `json:"resource_version"`
	RemainingItemCount *int64       `json:"remaining_item_count"`
}

type KEDATrigger struct {
	Type                  string            `json:"type"`
	Name                  string            `json:"name"`
	UseCachedMetrics      bool              `json:"use_cached_metrics"`
	Metadata              map[string]string `json:"metadata"`
	RedactedMetadataKeys  []string          `json:"redacted_metadata_keys"`
	AuthenticationRefName string            `json:"authentication_ref_name"`
}

type KEDAScaledObjectSpec struct {
	Target          AutoscalingTarget `json:"target"`
	PollingInterval int64             `json:"polling_interval"`
	CooldownPeriod  int64             `json:"cooldown_period"`
	MinReplicas     int32             `json:"min_replicas"`
	MaxReplicas     int32             `json:"max_replicas"`
	Triggers        []KEDATrigger     `json:"triggers"`
}

type KEDAScaledObjectSummary struct {
	Namespace         string            `json:"namespace"`
	Name              string            `json:"name"`
	UID               string            `json:"uid"`
	ResourceVersion   string            `json:"resource_version"`
	Generation        int64             `json:"generation"`
	CreationTimestamp time.Time         `json:"creation_timestamp"`
	Labels            map[string]string `json:"labels"`
	Target            AutoscalingTarget `json:"target"`
	MinReplicas       int32             `json:"min_replicas"`
	MaxReplicas       int32             `json:"max_replicas"`
	TriggerCount      int               `json:"trigger_count"`
	Ready             bool              `json:"ready"`
	Active            bool              `json:"active"`
	Fallback          bool              `json:"fallback"`
	Paused            bool              `json:"paused"`
}

type KEDAScaledObjectDetail struct {
	KEDAScaledObjectSummary
	Annotations         map[string]string               `json:"annotations"`
	PollingInterval     int64                           `json:"polling_interval"`
	CooldownPeriod      int64                           `json:"cooldown_period"`
	Triggers            []KEDATrigger                   `json:"triggers"`
	Conditions          []AutoscalingExtensionCondition `json:"conditions"`
	ExternalMetricNames []string                        `json:"external_metric_names"`
	HPAName             string                          `json:"hpa_name"`
}

type KEDAScaledObjectPage struct {
	Available          bool                      `json:"available"`
	UnavailableReason  string                    `json:"unavailable_reason,omitempty"`
	ScaledObjects      []KEDAScaledObjectSummary `json:"scaled_objects"`
	ContinueToken      string                    `json:"continue_token"`
	ResourceVersion    string                    `json:"resource_version"`
	RemainingItemCount *int64                    `json:"remaining_item_count"`
}

type CreateVPAInput struct {
	ClusterID, Namespace, Name string
	Labels, Annotations        map[string]string
	Spec                       VPASpec
	DryRun, Confirm            bool
	IdempotencyKey             string
}

type UpdateVPAInput struct {
	ClusterID, Namespace, Name, UID, ResourceVersion string
	Spec                                             VPASpec
	DryRun, Confirm                                  bool
	IdempotencyKey                                   string
}

type CreateKEDAScaledObjectInput struct {
	ClusterID, Namespace, Name string
	Labels, Annotations        map[string]string
	Spec                       KEDAScaledObjectSpec
	DryRun, Confirm            bool
	IdempotencyKey             string
}

type UpdateKEDAScaledObjectInput struct {
	ClusterID, Namespace, Name, UID, ResourceVersion string
	Spec                                             KEDAScaledObjectSpec
	DryRun, Confirm                                  bool
	IdempotencyKey                                   string
}

type DeleteAutoscalingExtensionInput struct {
	ClusterID, Namespace, Name, UID, ResourceVersion string
	DryRun, Confirm                                  bool
	IdempotencyKey                                   string
}

func VerticalPodAutoscalerResourceIdentity() ResourceIdentity { return verticalPodAutoscalerIdentity }
func KEDAScaledObjectResourceIdentity() ResourceIdentity      { return kedaScaledObjectIdentity }

func (service *Service) ListVerticalPodAutoscalers(ctx context.Context, input AutoscalingExtensionListInput) (VPAPage, error) {
	page, err := service.listAutoscalingExtension(ctx, input, verticalPodAutoscalerIdentity)
	if errors.Is(err, ErrResourceNotEnabled) || errors.Is(err, ErrResourceNotFound) {
		return VPAPage{Available: false, UnavailableReason: "not_installed", Autoscalers: []VPASummary{}}, nil
	}
	if err != nil {
		return VPAPage{}, err
	}
	result := VPAPage{Available: true, Autoscalers: make([]VPASummary, 0, len(page.Items)), ContinueToken: page.ContinueToken, ResourceVersion: page.ResourceVersion, RemainingItemCount: page.RemainingItemCount}
	for _, object := range page.Items {
		detail, parseErr := vpaDetail(object, input.Namespace, "")
		if parseErr != nil {
			return VPAPage{}, parseErr
		}
		result.Autoscalers = append(result.Autoscalers, detail.VPASummary)
	}
	return result, nil
}

func (service *Service) ListKEDAScaledObjects(ctx context.Context, input AutoscalingExtensionListInput) (KEDAScaledObjectPage, error) {
	page, err := service.listAutoscalingExtension(ctx, input, kedaScaledObjectIdentity)
	if errors.Is(err, ErrResourceNotEnabled) || errors.Is(err, ErrResourceNotFound) {
		return KEDAScaledObjectPage{Available: false, UnavailableReason: "not_installed", ScaledObjects: []KEDAScaledObjectSummary{}}, nil
	}
	if err != nil {
		return KEDAScaledObjectPage{}, err
	}
	result := KEDAScaledObjectPage{Available: true, ScaledObjects: make([]KEDAScaledObjectSummary, 0, len(page.Items)), ContinueToken: page.ContinueToken, ResourceVersion: page.ResourceVersion, RemainingItemCount: page.RemainingItemCount}
	for _, object := range page.Items {
		detail, parseErr := kedaDetail(object, input.Namespace, "")
		if parseErr != nil {
			return KEDAScaledObjectPage{}, parseErr
		}
		result.ScaledObjects = append(result.ScaledObjects, detail.KEDAScaledObjectSummary)
	}
	return result, nil
}

func (service *Service) listAutoscalingExtension(ctx context.Context, input AutoscalingExtensionListInput, identity ResourceIdentity) (ResourcePage, error) {
	if !validAutoscalingExtensionNamespace(input.Namespace) {
		return ResourcePage{}, ErrInvalidInput
	}
	return service.ListResources(ctx, ListResourcesInput{ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Limit: input.Limit, ContinueToken: input.ContinueToken, LabelSelector: input.LabelSelector, FieldSelector: input.FieldSelector})
}

func (service *Service) GetVerticalPodAutoscaler(ctx context.Context, clusterID, namespace, name string) (VPADetail, error) {
	object, err := service.getAutoscalingExtension(ctx, clusterID, namespace, name, verticalPodAutoscalerIdentity)
	if err != nil {
		return VPADetail{}, err
	}
	return vpaDetail(object, namespace, name)
}

func (service *Service) GetKEDAScaledObject(ctx context.Context, clusterID, namespace, name string) (KEDAScaledObjectDetail, error) {
	object, err := service.getAutoscalingExtension(ctx, clusterID, namespace, name, kedaScaledObjectIdentity)
	if err != nil {
		return KEDAScaledObjectDetail{}, err
	}
	return kedaDetail(object, namespace, name)
}

func (service *Service) getAutoscalingExtension(ctx context.Context, clusterID, namespace, name string, identity ResourceIdentity) (map[string]any, error) {
	if !validAutoscalingExtensionIdentity(namespace, name) {
		return nil, ErrInvalidInput
	}
	return service.GetResource(ctx, GetResourceInput{ClusterID: clusterID, Resource: identity, Namespace: namespace, Name: name})
}

func (service *Service) CreateVerticalPodAutoscaler(ctx context.Context, input CreateVPAInput) (VPADetail, error) {
	spec, err := vpaKubernetesSpec(input.Spec)
	if err != nil || !validAutoscalingExtensionMetadata(input.Namespace, input.Name, input.Labels, input.Annotations) {
		return VPADetail{}, ErrInvalidInput
	}
	object := extensionObject("autoscaling.k8s.io/v1", "VerticalPodAutoscaler", input.Namespace, input.Name, input.Labels, input.Annotations, spec)
	created, err := service.CreateResource(ctx, CreateResourceInput{ClusterID: input.ClusterID, Resource: verticalPodAutoscalerIdentity, Namespace: input.Namespace, Object: object, Options: MutationOptions{DryRun: input.DryRun}, Confirm: input.Confirm, IdempotencyKey: input.IdempotencyKey})
	if err != nil {
		return VPADetail{}, err
	}
	return vpaDetail(created, input.Namespace, input.Name)
}

func (service *Service) UpdateVerticalPodAutoscaler(ctx context.Context, input UpdateVPAInput) (VPADetail, error) {
	spec, err := vpaKubernetesSpec(input.Spec)
	if err != nil {
		return VPADetail{}, err
	}
	updated, err := service.updateAutoscalingExtension(ctx, input.ClusterID, input.Namespace, input.Name, input.UID, input.ResourceVersion, verticalPodAutoscalerIdentity, spec, input.DryRun, input.Confirm, input.IdempotencyKey)
	if err != nil {
		return VPADetail{}, err
	}
	return vpaDetail(updated, input.Namespace, input.Name)
}

func (service *Service) CreateKEDAScaledObject(ctx context.Context, input CreateKEDAScaledObjectInput) (KEDAScaledObjectDetail, error) {
	spec, err := kedaKubernetesSpec(input.Spec)
	if err != nil || !validAutoscalingExtensionMetadata(input.Namespace, input.Name, input.Labels, input.Annotations) {
		return KEDAScaledObjectDetail{}, ErrInvalidInput
	}
	object := extensionObject("keda.sh/v1alpha1", "ScaledObject", input.Namespace, input.Name, input.Labels, input.Annotations, spec)
	created, err := service.CreateResource(ctx, CreateResourceInput{ClusterID: input.ClusterID, Resource: kedaScaledObjectIdentity, Namespace: input.Namespace, Object: object, Options: MutationOptions{DryRun: input.DryRun}, Confirm: input.Confirm, IdempotencyKey: input.IdempotencyKey})
	if err != nil {
		return KEDAScaledObjectDetail{}, err
	}
	return kedaDetail(created, input.Namespace, input.Name)
}

func (service *Service) UpdateKEDAScaledObject(ctx context.Context, input UpdateKEDAScaledObjectInput) (KEDAScaledObjectDetail, error) {
	spec, err := kedaKubernetesSpec(input.Spec)
	if err != nil {
		return KEDAScaledObjectDetail{}, err
	}
	updated, err := service.updateAutoscalingExtension(ctx, input.ClusterID, input.Namespace, input.Name, input.UID, input.ResourceVersion, kedaScaledObjectIdentity, spec, input.DryRun, input.Confirm, input.IdempotencyKey)
	if err != nil {
		return KEDAScaledObjectDetail{}, err
	}
	return kedaDetail(updated, input.Namespace, input.Name)
}

func (service *Service) updateAutoscalingExtension(ctx context.Context, clusterID, namespace, name, uid, resourceVersion string, identity ResourceIdentity, spec map[string]any, dryRun, confirm bool, idempotencyKey string) (map[string]any, error) {
	if !validAutoscalingExtensionMutationIdentity(namespace, name, uid, resourceVersion) {
		return nil, ErrInvalidInput
	}
	existing, err := service.GetResource(ctx, GetResourceInput{ClusterID: clusterID, Resource: identity, Namespace: namespace, Name: name})
	if err != nil {
		return nil, err
	}
	object := &unstructured.Unstructured{Object: existing}
	if string(object.GetUID()) != uid || object.GetResourceVersion() != resourceVersion {
		return nil, ErrUpstreamConflict
	}
	if err := unstructured.SetNestedMap(object.Object, spec, "spec"); err != nil {
		return nil, ErrInvalidResponse
	}
	return service.UpdateResource(ctx, UpdateResourceInput{ClusterID: clusterID, Resource: identity, Namespace: namespace, Name: name, Object: object.Object, Options: MutationOptions{DryRun: dryRun}, Confirm: confirm, IdempotencyKey: idempotencyKey})
}

func (service *Service) DeleteVerticalPodAutoscaler(ctx context.Context, input DeleteAutoscalingExtensionInput) error {
	return service.deleteAutoscalingExtension(ctx, input, verticalPodAutoscalerIdentity)
}

func (service *Service) DeleteKEDAScaledObject(ctx context.Context, input DeleteAutoscalingExtensionInput) error {
	return service.deleteAutoscalingExtension(ctx, input, kedaScaledObjectIdentity)
}

func (service *Service) deleteAutoscalingExtension(ctx context.Context, input DeleteAutoscalingExtensionInput, identity ResourceIdentity) error {
	if !validAutoscalingExtensionMutationIdentity(input.Namespace, input.Name, input.UID, input.ResourceVersion) {
		return ErrInvalidInput
	}
	return service.DeleteResource(ctx, DeleteResourceInput{ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Name: input.Name, DryRun: input.DryRun, Confirm: input.Confirm, Preconditions: DeletePreconditions{UID: input.UID, ResourceVersion: input.ResourceVersion}, Propagation: agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND, IdempotencyKey: input.IdempotencyKey})
}

func extensionObject(apiVersion, kind, namespace, name string, labels, annotations map[string]string, spec map[string]any) map[string]any {
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "spec": spec}}
	object.SetName(name)
	object.SetNamespace(namespace)
	object.SetLabels(maps.Clone(labels))
	object.SetAnnotations(maps.Clone(annotations))
	return object.Object
}

func vpaKubernetesSpec(input VPASpec) (map[string]any, error) {
	if !validAutoscalingTarget(input.Target, true) || !validVPAUpdateMode(input.UpdateMode) || len(input.ContainerPolicies) > maxAutoscalingExtensionEntries {
		return nil, ErrInvalidInput
	}
	policies := make([]any, 0, len(input.ContainerPolicies))
	seen := map[string]struct{}{}
	for _, policy := range input.ContainerPolicies {
		if _, exists := seen[policy.ContainerName]; exists || !validVPAContainerPolicy(policy) {
			return nil, ErrInvalidInput
		}
		seen[policy.ContainerName] = struct{}{}
		item := map[string]any{"containerName": policy.ContainerName}
		if policy.Mode != "" {
			item["mode"] = policy.Mode
		}
		if len(policy.MinAllowed) > 0 {
			item["minAllowed"] = stringMapAny(policy.MinAllowed)
		}
		if len(policy.MaxAllowed) > 0 {
			item["maxAllowed"] = stringMapAny(policy.MaxAllowed)
		}
		if len(policy.ControlledResources) > 0 {
			item["controlledResources"] = stringSliceAny(policy.ControlledResources)
		}
		if policy.ControlledValues != "" {
			item["controlledValues"] = policy.ControlledValues
		}
		policies = append(policies, item)
	}
	spec := map[string]any{"targetRef": map[string]any{"apiVersion": input.Target.APIVersion, "kind": input.Target.Kind, "name": input.Target.Name}}
	if input.UpdateMode != "" {
		spec["updatePolicy"] = map[string]any{"updateMode": input.UpdateMode}
	}
	if len(policies) > 0 {
		spec["resourcePolicy"] = map[string]any{"containerPolicies": policies}
	}
	return spec, nil
}

func validVPAContainerPolicy(policy VPAContainerPolicy) bool {
	if policy.ContainerName != "*" && len(k8svalidation.IsDNS1123Label(policy.ContainerName)) != 0 || policy.Mode != "" && policy.Mode != "Auto" && policy.Mode != "Off" || policy.ControlledValues != "" && policy.ControlledValues != "RequestsOnly" && policy.ControlledValues != "RequestsAndLimits" {
		return false
	}
	if len(policy.ControlledResources) > 2 {
		return false
	}
	for _, name := range policy.ControlledResources {
		if name != "cpu" && name != "memory" {
			return false
		}
	}
	for _, name := range []string{"cpu", "memory"} {
		minimum, minExists := policy.MinAllowed[name]
		maximum, maxExists := policy.MaxAllowed[name]
		if minExists && !positiveQuantity(minimum) || maxExists && !positiveQuantity(maximum) {
			return false
		}
		if minExists && maxExists {
			minQ, _ := resource.ParseQuantity(minimum)
			maxQ, _ := resource.ParseQuantity(maximum)
			if minQ.Cmp(maxQ) > 0 {
				return false
			}
		}
	}
	for key := range policy.MinAllowed {
		if key != "cpu" && key != "memory" {
			return false
		}
	}
	for key := range policy.MaxAllowed {
		if key != "cpu" && key != "memory" {
			return false
		}
	}
	return true
}

func kedaKubernetesSpec(input KEDAScaledObjectSpec) (map[string]any, error) {
	if !validAutoscalingTarget(input.Target, false) || input.PollingInterval < 1 || input.PollingInterval > 3600 || input.CooldownPeriod < 0 || input.CooldownPeriod > 86400 || input.MinReplicas < 0 || input.MaxReplicas < 1 || input.MaxReplicas > maxHPAReplicas || input.MinReplicas > input.MaxReplicas || len(input.Triggers) == 0 || len(input.Triggers) > maxKEDATriggers {
		return nil, ErrInvalidInput
	}
	triggers := make([]any, 0, len(input.Triggers))
	for _, trigger := range input.Triggers {
		if !validKEDATrigger(trigger) {
			return nil, ErrInvalidInput
		}
		item := map[string]any{"type": trigger.Type, "metadata": stringMapAny(trigger.Metadata)}
		if trigger.Name != "" {
			item["name"] = trigger.Name
		}
		if trigger.UseCachedMetrics {
			item["useCachedMetrics"] = true
		}
		if trigger.AuthenticationRefName != "" {
			item["authenticationRef"] = map[string]any{"name": trigger.AuthenticationRefName, "kind": "TriggerAuthentication"}
		}
		triggers = append(triggers, item)
	}
	return map[string]any{
		"scaleTargetRef":  map[string]any{"apiVersion": input.Target.APIVersion, "kind": input.Target.Kind, "name": input.Target.Name},
		"pollingInterval": input.PollingInterval, "cooldownPeriod": input.CooldownPeriod,
		"minReplicaCount": int64(input.MinReplicas), "maxReplicaCount": int64(input.MaxReplicas), "triggers": triggers,
	}, nil
}

func validKEDATrigger(trigger KEDATrigger) bool {
	if len(k8svalidation.IsDNS1123Label(trigger.Type)) != 0 || trigger.Name != "" && len(k8svalidation.IsDNS1123Label(trigger.Name)) != 0 || trigger.AuthenticationRefName != "" && len(k8svalidation.IsDNS1123Subdomain(trigger.AuthenticationRefName)) != 0 || len(trigger.Metadata) == 0 || len(trigger.Metadata) > maxAutoscalingExtensionEntries {
		return false
	}
	total := 0
	for key, value := range trigger.Metadata {
		if key == "" || strings.TrimSpace(key) != key || strings.TrimSpace(value) != value || sensitiveKEDAMetadataKey(key) {
			return false
		}
		total += len(key) + len(value)
	}
	return total <= maxAutoscalingExtensionBytes
}

func vpaDetail(object map[string]any, namespace, name string) (VPADetail, error) {
	value := &unstructured.Unstructured{Object: object}
	if value.GetAPIVersion() != "autoscaling.k8s.io/v1" || value.GetKind() != "VerticalPodAutoscaler" || value.GetNamespace() != namespace || value.GetName() == "" || name != "" && value.GetName() != name {
		return VPADetail{}, ErrInvalidResponse
	}
	target := nestedTarget(object, "spec", "targetRef")
	updateMode, _, _ := unstructured.NestedString(object, "spec", "updatePolicy", "updateMode")
	policies := parseVPAContainerPolicies(object)
	recommendations := parseVPARecommendations(object)
	conditions := parseExtensionConditions(object)
	observed, _, _ := unstructured.NestedInt64(object, "status", "observedGeneration")
	return VPADetail{VPASummary: VPASummary{Namespace: namespace, Name: value.GetName(), UID: string(value.GetUID()), ResourceVersion: value.GetResourceVersion(), Generation: value.GetGeneration(), ObservedGeneration: observed, CreationTimestamp: value.GetCreationTimestamp().Time, Labels: normalizedStringMap(value.GetLabels()), Target: target, UpdateMode: updateMode, RecommendationCount: len(recommendations), Conditions: conditions}, Annotations: normalizedStringMap(value.GetAnnotations()), ContainerPolicies: policies, Recommendations: recommendations}, nil
}

func kedaDetail(object map[string]any, namespace, name string) (KEDAScaledObjectDetail, error) {
	value := &unstructured.Unstructured{Object: object}
	if value.GetAPIVersion() != "keda.sh/v1alpha1" || value.GetKind() != "ScaledObject" || value.GetNamespace() != namespace || value.GetName() == "" || name != "" && value.GetName() != name {
		return KEDAScaledObjectDetail{}, ErrInvalidResponse
	}
	target := nestedTarget(object, "spec", "scaleTargetRef")
	polling, _, _ := unstructured.NestedInt64(object, "spec", "pollingInterval")
	if polling == 0 {
		polling = 30
	}
	cooldown, cooldownFound, _ := unstructured.NestedInt64(object, "spec", "cooldownPeriod")
	if !cooldownFound {
		cooldown = 300
	}
	minimum, _, _ := unstructured.NestedInt64(object, "spec", "minReplicaCount")
	maximum, found, _ := unstructured.NestedInt64(object, "spec", "maxReplicaCount")
	if !found {
		maximum = 100
	}
	triggers := parseKEDATriggers(object)
	conditions := parseExtensionConditions(object)
	ready, active, fallback, paused := conditionTrue(conditions, "Ready"), conditionTrue(conditions, "Active"), conditionTrue(conditions, "Fallback"), conditionTrue(conditions, "Paused")
	metricNames, _, _ := unstructured.NestedStringSlice(object, "status", "externalMetricNames")
	hpaName, _, _ := unstructured.NestedString(object, "status", "hpaName")
	return KEDAScaledObjectDetail{KEDAScaledObjectSummary: KEDAScaledObjectSummary{Namespace: namespace, Name: value.GetName(), UID: string(value.GetUID()), ResourceVersion: value.GetResourceVersion(), Generation: value.GetGeneration(), CreationTimestamp: value.GetCreationTimestamp().Time, Labels: normalizedStringMap(value.GetLabels()), Target: target, MinReplicas: int32(minimum), MaxReplicas: int32(maximum), TriggerCount: len(triggers), Ready: ready, Active: active, Fallback: fallback, Paused: paused}, Annotations: normalizedStringMap(value.GetAnnotations()), PollingInterval: polling, CooldownPeriod: cooldown, Triggers: triggers, Conditions: conditions, ExternalMetricNames: metricNames, HPAName: hpaName}, nil
}

func parseVPAContainerPolicies(object map[string]any) []VPAContainerPolicy {
	items, _, _ := unstructured.NestedSlice(object, "spec", "resourcePolicy", "containerPolicies")
	result := make([]VPAContainerPolicy, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(item, "containerName")
		mode, _, _ := unstructured.NestedString(item, "mode")
		controlledValues, _, _ := unstructured.NestedString(item, "controlledValues")
		controlled, _, _ := unstructured.NestedStringSlice(item, "controlledResources")
		result = append(result, VPAContainerPolicy{ContainerName: name, Mode: mode, MinAllowed: nestedStringMap(item, "minAllowed"), MaxAllowed: nestedStringMap(item, "maxAllowed"), ControlledResources: controlled, ControlledValues: controlledValues})
	}
	return result
}

func parseVPARecommendations(object map[string]any) []VPARecommendation {
	items, _, _ := unstructured.NestedSlice(object, "status", "recommendation", "containerRecommendations")
	result := make([]VPARecommendation, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _, _ := unstructured.NestedString(item, "containerName")
		result = append(result, VPARecommendation{ContainerName: name, Target: nestedStringMap(item, "target"), LowerBound: nestedStringMap(item, "lowerBound"), UpperBound: nestedStringMap(item, "upperBound"), UncappedTarget: nestedStringMap(item, "uncappedTarget")})
	}
	return result
}

func parseKEDATriggers(object map[string]any) []KEDATrigger {
	items, _, _ := unstructured.NestedSlice(object, "spec", "triggers")
	result := make([]KEDATrigger, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typeName, _, _ := unstructured.NestedString(item, "type")
		name, _, _ := unstructured.NestedString(item, "name")
		cached, _, _ := unstructured.NestedBool(item, "useCachedMetrics")
		authName, _, _ := unstructured.NestedString(item, "authenticationRef", "name")
		metadata := nestedStringMap(item, "metadata")
		redacted := make([]string, 0)
		for key := range metadata {
			if sensitiveKEDAMetadataKey(key) {
				metadata[key] = "[redacted]"
				redacted = append(redacted, key)
			}
		}
		sort.Strings(redacted)
		result = append(result, KEDATrigger{Type: typeName, Name: name, UseCachedMetrics: cached, Metadata: metadata, RedactedMetadataKeys: redacted, AuthenticationRefName: authName})
	}
	return result
}

func parseExtensionConditions(object map[string]any) []AutoscalingExtensionCondition {
	items, _, _ := unstructured.NestedSlice(object, "status", "conditions")
	result := make([]AutoscalingExtensionCondition, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		condition := AutoscalingExtensionCondition{}
		condition.Type, _, _ = unstructured.NestedString(item, "type")
		condition.Status, _, _ = unstructured.NestedString(item, "status")
		condition.Reason, _, _ = unstructured.NestedString(item, "reason")
		condition.Message, _, _ = unstructured.NestedString(item, "message")
		timestamp, _, _ := unstructured.NestedString(item, "lastTransitionTime")
		if parsed, err := time.Parse(time.RFC3339, timestamp); err == nil {
			condition.LastTransitionTime = &parsed
		}
		result = append(result, condition)
	}
	return result
}

func nestedTarget(object map[string]any, fields ...string) AutoscalingTarget {
	item, _, _ := unstructured.NestedMap(object, fields...)
	apiVersion, _, _ := unstructured.NestedString(item, "apiVersion")
	kind, _, _ := unstructured.NestedString(item, "kind")
	name, _, _ := unstructured.NestedString(item, "name")
	return AutoscalingTarget{APIVersion: apiVersion, Kind: kind, Name: name}
}
func nestedStringMap(object map[string]any, fields ...string) map[string]string {
	item, _, _ := unstructured.NestedStringMap(object, fields...)
	return normalizedStringMap(item)
}
func stringMapAny(input map[string]string) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
func stringSliceAny(input []string) []any {
	result := make([]any, len(input))
	for index, value := range input {
		result[index] = value
	}
	return result
}
func conditionTrue(conditions []AutoscalingExtensionCondition, name string) bool {
	for _, condition := range conditions {
		if condition.Type == name {
			return condition.Status == "True"
		}
	}
	return false
}
func positiveQuantity(value string) bool {
	quantity, err := resource.ParseQuantity(value)
	return err == nil && quantity.Sign() > 0
}
func validVPAUpdateMode(value string) bool {
	return value == "" || value == "Off" || value == "Initial" || value == "Recreate" || value == "InPlaceOrRecreate" || value == "InPlace"
}
func validAutoscalingTarget(value AutoscalingTarget, allowDaemonSet bool) bool {
	if value.APIVersion != "apps/v1" || len(k8svalidation.IsDNS1123Subdomain(value.Name)) != 0 {
		return false
	}
	return value.Kind == "Deployment" || value.Kind == "StatefulSet" || allowDaemonSet && value.Kind == "DaemonSet"
}
func validAutoscalingExtensionNamespace(value string) bool {
	return len(k8svalidation.IsDNS1123Label(value)) == 0
}
func validAutoscalingExtensionIdentity(namespace, name string) bool {
	return validAutoscalingExtensionNamespace(namespace) && len(k8svalidation.IsDNS1123Subdomain(name)) == 0
}
func validAutoscalingExtensionMutationIdentity(namespace, name, uid, rv string) bool {
	return validAutoscalingExtensionIdentity(namespace, name) && uid != "" && strings.TrimSpace(uid) == uid && len(uid) <= 256 && rv != "" && strings.TrimSpace(rv) == rv && len(rv) <= 256
}
func validAutoscalingExtensionMetadata(namespace, name string, labels, annotations map[string]string) bool {
	if !validAutoscalingExtensionIdentity(namespace, name) || len(labels) > maxAutoscalingExtensionEntries || len(annotations) > maxAutoscalingExtensionEntries {
		return false
	}
	total := 0
	for key, value := range labels {
		if len(k8svalidation.IsQualifiedName(key)) != 0 || len(k8svalidation.IsValidLabelValue(value)) != 0 {
			return false
		}
		total += len(key) + len(value)
	}
	for key, value := range annotations {
		if len(k8svalidation.IsQualifiedName(key)) != 0 {
			return false
		}
		total += len(key) + len(value)
	}
	return total <= maxAutoscalingExtensionBytes
}
func sensitiveKEDAMetadataKey(key string) bool {
	lower := strings.ToLower(key)
	for _, fragment := range []string{"password", "passwd", "token", "secret", "apikey", "api_key", "accountkey", "account_key", "connectionstring", "connection_string"} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return lower == "sas" || strings.HasSuffix(lower, "sastoken") || strings.HasSuffix(lower, "sas_token")
}

type HPAMetricTrendPoint struct {
	Timestamp       time.Time       `json:"timestamp"`
	CurrentReplicas int32           `json:"current_replicas"`
	DesiredReplicas int32           `json:"desired_replicas"`
	Metrics         []HPAMetricView `json:"metrics"`
}

type HPAMetricTrend struct {
	UID           string                `json:"uid"`
	Name          string                `json:"name"`
	Namespace     string                `json:"namespace"`
	WindowSeconds int64                 `json:"window_seconds"`
	Points        []HPAMetricTrendPoint `json:"points"`
}

func (service *Service) GetHorizontalPodAutoscalerMetricTrend(ctx context.Context, clusterID, namespace, name string) (HPAMetricTrend, error) {
	detail, err := service.GetHorizontalPodAutoscaler(ctx, clusterID, namespace, name)
	if err != nil {
		return HPAMetricTrend{}, err
	}
	now := time.Now().UTC()
	key := clusterID + "\x00" + namespace + "\x00" + detail.UID
	point := HPAMetricTrendPoint{Timestamp: now, CurrentReplicas: detail.CurrentReplicas, DesiredReplicas: detail.DesiredReplicas, Metrics: detail.CurrentMetrics}
	service.trendMutex.Lock()
	defer service.trendMutex.Unlock()
	if service.hpaTrends == nil {
		service.hpaTrends = make(map[string][]HPAMetricTrendPoint)
	}
	cutoff := now.Add(-time.Hour)
	oldestKey, oldestTime := "", now
	for seriesKey, series := range service.hpaTrends {
		if len(series) == 0 || !series[len(series)-1].Timestamp.After(cutoff) {
			delete(service.hpaTrends, seriesKey)
			continue
		}
		if series[len(series)-1].Timestamp.Before(oldestTime) {
			oldestKey, oldestTime = seriesKey, series[len(series)-1].Timestamp
		}
	}
	if _, exists := service.hpaTrends[key]; !exists && len(service.hpaTrends) >= maxTrendSeries && oldestKey != "" {
		delete(service.hpaTrends, oldestKey)
	}
	points := service.hpaTrends[key]
	kept := points[:0]
	for _, existing := range points {
		if existing.Timestamp.After(cutoff) {
			kept = append(kept, existing)
		}
	}
	if len(kept) == 0 || now.Sub(kept[len(kept)-1].Timestamp) >= 5*time.Second {
		kept = append(kept, point)
	}
	if len(kept) > maxTrendPoints {
		kept = kept[len(kept)-maxTrendPoints:]
	}
	service.hpaTrends[key] = kept
	return HPAMetricTrend{UID: detail.UID, Name: name, Namespace: namespace, WindowSeconds: 3600, Points: append([]HPAMetricTrendPoint(nil), kept...)}, nil
}
