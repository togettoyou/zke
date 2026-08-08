package kubernetesresource

import (
	"context"
	"encoding/json"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

// PolicyResource is one of the five objects that constrain what a Namespace or
// a Cluster lets workloads do: how much they may consume, what a container's
// limits default to, which traffic reaches them, how many may be disrupted at
// once, and how they rank against each other under contention.
type PolicyResource string

const (
	PolicyResourceQuotas       PolicyResource = "resourcequotas"
	PolicyLimitRanges          PolicyResource = "limitranges"
	PolicyNetworkPolicies      PolicyResource = "networkpolicies"
	PolicyDisruptionBudgets    PolicyResource = "poddisruptionbudgets"
	PolicyPriorityClasses      PolicyResource = "priorityclasses"
	maxPolicyAnnotations                      = 256 * 1024
	maxPolicyQuantities                       = 64
	maxPolicyLimitRangeItems                  = 32
	maxPolicyNetworkRules                     = 64
	maxPolicyNetworkPeers                     = 64
	maxPolicyNetworkPorts                     = 64
	maxPolicySelectorValues                   = 64
	maxPolicyScopeSelectors                   = 16
	maxPolicyDescriptionLength                = 1024
	// Kubernetes reserves everything above this for system-critical classes and
	// rejects a user-defined PriorityClass that reaches into that range.
	maxPolicyPriorityValue = 1_000_000_000
)

var policyResourceIdentities = map[PolicyResource]ResourceIdentity{
	PolicyResourceQuotas:    {Version: "v1", Resource: "resourcequotas"},
	PolicyLimitRanges:       {Version: "v1", Resource: "limitranges"},
	PolicyNetworkPolicies:   {Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"},
	PolicyDisruptionBudgets: {Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"},
	PolicyPriorityClasses:   {Group: "scheduling.k8s.io", Version: "v1", Resource: "priorityclasses"},
}

type ListPolicyResourcesInput struct {
	ClusterID     string
	Namespace     string
	Resource      PolicyResource
	Limit         int64
	ContinueToken string
	LabelSelector string
	FieldSelector string
}

type PolicyResourcePage struct {
	Resources          []PolicyResourceSummary `json:"resources"`
	ContinueToken      string                  `json:"continue_token"`
	ResourceVersion    string                  `json:"resource_version"`
	RemainingItemCount *int64                  `json:"remaining_item_count"`
}

type PolicyResourceSummary struct {
	Resource          PolicyResource           `json:"resource"`
	APIVersion        string                   `json:"api_version"`
	Kind              string                   `json:"kind"`
	Namespace         string                   `json:"namespace"`
	Name              string                   `json:"name"`
	UID               string                   `json:"uid"`
	ResourceVersion   string                   `json:"resource_version"`
	CreationTimestamp time.Time                `json:"creation_timestamp"`
	Labels            map[string]string        `json:"labels"`
	ResourceQuota     *ResourceQuotaSummary    `json:"resource_quota,omitempty"`
	LimitRange        *LimitRangeSummary       `json:"limit_range,omitempty"`
	NetworkPolicy     *NetworkPolicySummary    `json:"network_policy,omitempty"`
	DisruptionBudget  *DisruptionBudgetSummary `json:"disruption_budget,omitempty"`
	PriorityClass     *PriorityClassSummary    `json:"priority_class,omitempty"`
}

// PolicyResourceDetail has no ResourceQuota member: everything a quota detail
// used to add — its scopeSelector — is now part of the summary, because reading
// `used` against `hard` without knowing what the quota counts is misreading it,
// and a list does exactly that reading.
type PolicyResourceDetail struct {
	PolicyResourceSummary
	Annotations            map[string]string       `json:"annotations"`
	LimitRangeDetail       *LimitRangeDetail       `json:"limit_range_detail,omitempty"`
	NetworkPolicyDetail    *NetworkPolicyDetail    `json:"network_policy_detail,omitempty"`
	DisruptionBudgetDetail *DisruptionBudgetDetail `json:"disruption_budget_detail,omitempty"`
}

// ResourceQuotaSummary carries `hard` next to `used` because a quota is only
// ever read to answer "how much is left"; the two halves separated would make
// the list a lookup table rather than an answer.
//
// `scopes` and `scope_selector` travel with them for the same reason: both
// narrow which objects the quota counts, so a caller reading `used` against
// `hard` without them would read a subset of a Namespace as the whole of it.
type ResourceQuotaSummary struct {
	Hard          map[string]string                `json:"hard"`
	Used          map[string]string                `json:"used"`
	Scopes        []string                         `json:"scopes"`
	ScopeSelector []PolicyScopeSelectorRequirement `json:"scope_selector"`
}

type PolicyScopeSelectorRequirement struct {
	ScopeName string   `json:"scope_name"`
	Operator  string   `json:"operator"`
	Values    []string `json:"values"`
}

type LimitRangeSummary struct {
	Types     []string `json:"types"`
	ItemCount int      `json:"item_count"`
}

// LimitRangeItem is both the read view and the write input: a LimitRange has no
// status, so the object the operator sees is exactly the object they submit.
type LimitRangeItem struct {
	Type                 string            `json:"type"`
	Max                  map[string]string `json:"max"`
	Min                  map[string]string `json:"min"`
	Default              map[string]string `json:"default"`
	DefaultRequest       map[string]string `json:"default_request"`
	MaxLimitRequestRatio map[string]string `json:"max_limit_request_ratio"`
}

type LimitRangeDetail struct {
	Items []LimitRangeItem `json:"items"`
}

type NetworkPolicySummary struct {
	PodSelector  *WorkloadSelector `json:"pod_selector,omitempty"`
	PolicyTypes  []string          `json:"policy_types"`
	IngressRules int               `json:"ingress_rules"`
	EgressRules  int               `json:"egress_rules"`
}

type NetworkPolicyIPBlock struct {
	CIDR   string   `json:"cidr"`
	Except []string `json:"except"`
}

type NetworkPolicyPeer struct {
	PodSelector       *WorkloadSelector     `json:"pod_selector,omitempty"`
	NamespaceSelector *WorkloadSelector     `json:"namespace_selector,omitempty"`
	IPBlock           *NetworkPolicyIPBlock `json:"ip_block,omitempty"`
}

type NetworkPolicyPort struct {
	Protocol string `json:"protocol"`
	// A port number or an IANA service name, as Kubernetes accepts either.
	Port    string `json:"port"`
	EndPort *int32 `json:"end_port,omitempty"`
}

type NetworkPolicyRule struct {
	Peers []NetworkPolicyPeer `json:"peers"`
	Ports []NetworkPolicyPort `json:"ports"`
}

type NetworkPolicyDetail struct {
	Ingress []NetworkPolicyRule `json:"ingress"`
	Egress  []NetworkPolicyRule `json:"egress"`
}

type DisruptionBudgetSummary struct {
	Selector           *WorkloadSelector `json:"selector,omitempty"`
	MinAvailable       string            `json:"min_available"`
	MaxUnavailable     string            `json:"max_unavailable"`
	CurrentHealthy     int32             `json:"current_healthy"`
	DesiredHealthy     int32             `json:"desired_healthy"`
	DisruptionsAllowed int32             `json:"disruptions_allowed"`
	ExpectedPods       int32             `json:"expected_pods"`
}

type PolicyCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastTransitionTime time.Time `json:"last_transition_time"`
}

type DisruptionBudgetDetail struct {
	UnhealthyPodEvictionPolicy string            `json:"unhealthy_pod_eviction_policy"`
	Conditions                 []PolicyCondition `json:"conditions"`
}

type PriorityClassSummary struct {
	Value            int32  `json:"value"`
	GlobalDefault    bool   `json:"global_default"`
	PreemptionPolicy string `json:"preemption_policy"`
	Description      string `json:"description"`
}

type ResourceQuotaSpecInput struct {
	Hard          map[string]string                `json:"hard"`
	Scopes        []string                         `json:"scopes"`
	ScopeSelector []PolicyScopeSelectorRequirement `json:"scope_selector"`
}

type LimitRangeSpecInput struct {
	Items []LimitRangeItem `json:"items"`
}

type NetworkPolicySpecInput struct {
	PodSelector *WorkloadSelector   `json:"pod_selector"`
	PolicyTypes []string            `json:"policy_types"`
	Ingress     []NetworkPolicyRule `json:"ingress"`
	Egress      []NetworkPolicyRule `json:"egress"`
}

type DisruptionBudgetSpecInput struct {
	Selector                   *WorkloadSelector `json:"selector"`
	MinAvailable               string            `json:"min_available"`
	MaxUnavailable             string            `json:"max_unavailable"`
	UnhealthyPodEvictionPolicy string            `json:"unhealthy_pod_eviction_policy"`
}

type PriorityClassSpecInput struct {
	Value            int32  `json:"value"`
	GlobalDefault    bool   `json:"global_default"`
	PreemptionPolicy string `json:"preemption_policy"`
	Description      string `json:"description"`
}

// PriorityClassUpdateSpec is narrower than its create input on purpose:
// Kubernetes forbids changing a PriorityClass's value after creation, and
// preemptionPolicy only takes effect at scheduling time for Pods admitted
// afterwards. Offering either as an editable field would be offering an edit
// the API server refuses or the cluster ignores.
type PriorityClassUpdateSpec struct {
	Description   string `json:"description"`
	GlobalDefault *bool  `json:"global_default"`
}

type PolicyCreateSpec struct {
	ResourceQuota    *ResourceQuotaSpecInput    `json:"resource_quota"`
	LimitRange       *LimitRangeSpecInput       `json:"limit_range"`
	NetworkPolicy    *NetworkPolicySpecInput    `json:"network_policy"`
	DisruptionBudget *DisruptionBudgetSpecInput `json:"disruption_budget"`
	PriorityClass    *PriorityClassSpecInput    `json:"priority_class"`
}

type PolicyUpdateSpec struct {
	ResourceQuota    *ResourceQuotaSpecInput    `json:"resource_quota"`
	LimitRange       *LimitRangeSpecInput       `json:"limit_range"`
	NetworkPolicy    *NetworkPolicySpecInput    `json:"network_policy"`
	DisruptionBudget *DisruptionBudgetSpecInput `json:"disruption_budget"`
	PriorityClass    *PriorityClassUpdateSpec   `json:"priority_class"`
}

type CreatePolicyResourceInput struct {
	ClusterID      string
	Namespace      string
	Resource       PolicyResource
	Name           string
	Labels         map[string]string
	Annotations    map[string]string
	Spec           PolicyCreateSpec
	DryRun         bool
	Confirm        bool
	IdempotencyKey string
}

type UpdatePolicyResourceInput struct {
	ClusterID       string
	Namespace       string
	Resource        PolicyResource
	Name            string
	UID             string
	ResourceVersion string
	Spec            PolicyUpdateSpec
	DryRun          bool
	Confirm         bool
	IdempotencyKey  string
}

type DeletePolicyResourceInput struct {
	ClusterID       string
	Namespace       string
	Resource        PolicyResource
	Name            string
	UID             string
	ResourceVersion string
	DryRun          bool
	Confirm         bool
	IdempotencyKey  string
}

func ParsePolicyResource(value string) (PolicyResource, bool) {
	resourceName := PolicyResource(value)
	_, exists := policyResourceIdentities[resourceName]
	return resourceName, exists
}

func PolicyResourceIdentity(resourceName PolicyResource) (ResourceIdentity, bool) {
	identity, exists := policyResourceIdentities[resourceName]
	return identity, exists
}

func (service *Service) ListPolicyResources(
	ctx context.Context,
	input ListPolicyResourcesInput,
) (PolicyResourcePage, error) {
	identity, ok := policyResourceIdentities[input.Resource]
	if !ok || !validPolicyScope(input.Resource, input.Namespace) {
		return PolicyResourcePage{}, ErrInvalidInput
	}
	page, err := service.ListResources(ctx, ListResourcesInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace,
		Limit: input.Limit, ContinueToken: input.ContinueToken,
		LabelSelector: input.LabelSelector, FieldSelector: input.FieldSelector,
	})
	if err != nil {
		return PolicyResourcePage{}, err
	}
	result := PolicyResourcePage{
		Resources: make([]PolicyResourceSummary, 0, len(page.Items)), ContinueToken: page.ContinueToken,
		ResourceVersion: page.ResourceVersion, RemainingItemCount: page.RemainingItemCount,
	}
	for _, item := range page.Items {
		detail, err := policyResourceDetail(item, input.Resource, input.Namespace, "")
		if err != nil {
			return PolicyResourcePage{}, err
		}
		result.Resources = append(result.Resources, detail.PolicyResourceSummary)
	}
	return result, nil
}

func (service *Service) GetPolicyResource(
	ctx context.Context,
	clusterID, namespace string,
	resourceName PolicyResource,
	name string,
) (PolicyResourceDetail, error) {
	identity, ok := policyResourceIdentities[resourceName]
	if !ok || !validPolicyScope(resourceName, namespace) || len(k8svalidation.IsDNS1123Subdomain(name)) != 0 {
		return PolicyResourceDetail{}, ErrInvalidInput
	}
	object, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: clusterID, Resource: identity, Namespace: namespace, Name: name,
	})
	if err != nil {
		return PolicyResourceDetail{}, err
	}
	return policyResourceDetail(object, resourceName, namespace, name)
}

func (service *Service) CreatePolicyResource(
	ctx context.Context,
	input CreatePolicyResourceInput,
) (PolicyResourceDetail, error) {
	identity, ok := policyResourceIdentities[input.Resource]
	if !ok {
		return PolicyResourceDetail{}, ErrInvalidInput
	}
	object, err := createPolicyObject(input)
	if err != nil {
		return PolicyResourceDetail{}, err
	}
	result, err := service.CreateResource(ctx, CreateResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Object: object,
		Options: MutationOptions{DryRun: input.DryRun}, Confirm: input.Confirm,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return PolicyResourceDetail{}, err
	}
	return policyResourceDetail(result, input.Resource, input.Namespace, input.Name)
}

// UpdatePolicyResource replaces the managed part of one object's spec.
//
// The object is read back first and its UID and resourceVersion are compared
// with the ones the caller edited: a policy decides what an entire Namespace is
// allowed to do, so an update built on a stale read must be refused rather than
// applied over whatever changed in between.
func (service *Service) UpdatePolicyResource(
	ctx context.Context,
	input UpdatePolicyResourceInput,
) (PolicyResourceDetail, error) {
	identity, ok := policyResourceIdentities[input.Resource]
	if !ok || !validPolicyMutationIdentity(input.Resource, input.Namespace, input.Name, input.UID, input.ResourceVersion) {
		return PolicyResourceDetail{}, ErrInvalidInput
	}
	existing, err := service.GetResource(ctx, GetResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Name: input.Name,
	})
	if err != nil {
		return PolicyResourceDetail{}, err
	}
	current := &unstructured.Unstructured{Object: existing}
	if string(current.GetUID()) != input.UID || current.GetResourceVersion() != input.ResourceVersion {
		return PolicyResourceDetail{}, ErrUpstreamConflict
	}
	updated, err := updatePolicyObject(existing, input)
	if err != nil {
		return PolicyResourceDetail{}, err
	}
	result, err := service.UpdateResource(ctx, UpdateResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Name: input.Name,
		Object: updated, Options: MutationOptions{DryRun: input.DryRun}, Confirm: input.Confirm,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return PolicyResourceDetail{}, err
	}
	return policyResourceDetail(result, input.Resource, input.Namespace, input.Name)
}

func (service *Service) DeletePolicyResource(ctx context.Context, input DeletePolicyResourceInput) error {
	identity, ok := policyResourceIdentities[input.Resource]
	if !ok || !validPolicyMutationIdentity(input.Resource, input.Namespace, input.Name, input.UID, input.ResourceVersion) {
		return ErrInvalidInput
	}
	return service.DeleteResource(ctx, DeleteResourceInput{
		ClusterID: input.ClusterID, Resource: identity, Namespace: input.Namespace, Name: input.Name,
		DryRun: input.DryRun, Confirm: input.Confirm,
		Preconditions:  DeletePreconditions{UID: input.UID, ResourceVersion: input.ResourceVersion},
		Propagation:    agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND,
		IdempotencyKey: input.IdempotencyKey,
	})
}

// validPolicyScope keeps each kind on the route family that matches its scope.
// PriorityClass ranks Pods across the whole Cluster and has no Namespace; the
// other four constrain exactly one.
func validPolicyScope(resourceName PolicyResource, namespace string) bool {
	if resourceName == PolicyPriorityClasses {
		return namespace == ""
	}
	return len(k8svalidation.IsDNS1123Label(namespace)) == 0
}

func validPolicyMutationIdentity(resourceName PolicyResource, namespace, name, uid, resourceVersion string) bool {
	return validPolicyScope(resourceName, namespace) && len(k8svalidation.IsDNS1123Subdomain(name)) == 0 &&
		strings.TrimSpace(uid) != "" && len(uid) <= 128 &&
		strings.TrimSpace(resourceVersion) != "" && len(resourceVersion) <= 256
}

func validPolicyMetadata(resourceName PolicyResource, namespace, name string, labels, annotations map[string]string) bool {
	if !validPolicyScope(resourceName, namespace) || len(k8svalidation.IsDNS1123Subdomain(name)) != 0 ||
		!validNamespaceLabels(labels) {
		return false
	}
	total := 0
	for key, value := range annotations {
		if len(k8svalidation.IsQualifiedName(key)) != 0 {
			return false
		}
		total += len(key) + len(value)
		if total > maxPolicyAnnotations {
			return false
		}
	}
	return true
}

func createPolicyObject(input CreatePolicyResourceInput) (map[string]any, error) {
	if !validPolicyMetadata(input.Resource, input.Namespace, input.Name, input.Labels, input.Annotations) ||
		!exactlyOnePolicySpec(input.Resource, input.Spec) {
		return nil, ErrInvalidInput
	}
	metadata := metav1.ObjectMeta{
		Name: input.Name, Namespace: input.Namespace,
		Labels: maps.Clone(input.Labels), Annotations: maps.Clone(input.Annotations),
	}
	var object any
	switch input.Resource {
	case PolicyResourceQuotas:
		spec, err := resourceQuotaKubernetesSpec(*input.Spec.ResourceQuota)
		if err != nil {
			return nil, err
		}
		object = &corev1.ResourceQuota{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ResourceQuota"}, ObjectMeta: metadata, Spec: spec,
		}
	case PolicyLimitRanges:
		spec, err := limitRangeKubernetesSpec(*input.Spec.LimitRange)
		if err != nil {
			return nil, err
		}
		object = &corev1.LimitRange{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "LimitRange"}, ObjectMeta: metadata, Spec: spec,
		}
	case PolicyNetworkPolicies:
		spec, err := networkPolicyKubernetesSpec(*input.Spec.NetworkPolicy)
		if err != nil {
			return nil, err
		}
		object = &networkingv1.NetworkPolicy{
			TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
			ObjectMeta: metadata, Spec: spec,
		}
	case PolicyDisruptionBudgets:
		spec, err := disruptionBudgetKubernetesSpec(*input.Spec.DisruptionBudget)
		if err != nil {
			return nil, err
		}
		object = &policyv1.PodDisruptionBudget{
			TypeMeta:   metav1.TypeMeta{APIVersion: "policy/v1", Kind: "PodDisruptionBudget"},
			ObjectMeta: metadata, Spec: spec,
		}
	case PolicyPriorityClasses:
		class, err := priorityClassKubernetesObject(metadata, *input.Spec.PriorityClass)
		if err != nil {
			return nil, err
		}
		object = class
	default:
		return nil, ErrInvalidInput
	}
	return typedPolicyObject(object, ErrInvalidInput)
}

func updatePolicyObject(existing map[string]any, input UpdatePolicyResourceInput) (map[string]any, error) {
	if !exactlyOnePolicyUpdateSpec(input.Resource, input.Spec) {
		return nil, ErrInvalidInput
	}
	body, err := json.Marshal(existing)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	switch input.Resource {
	case PolicyResourceQuotas:
		var object corev1.ResourceQuota
		if json.Unmarshal(body, &object) != nil ||
			!validTypedPolicyIdentity(&object.ObjectMeta, object.APIVersion, object.Kind, input.Namespace, input.Name, "v1", "ResourceQuota") {
			return nil, ErrInvalidResponse
		}
		spec, err := resourceQuotaKubernetesSpec(*input.Spec.ResourceQuota)
		if err != nil {
			return nil, err
		}
		object.Spec = spec
		return typedPolicyObject(&object, ErrInvalidResponse)
	case PolicyLimitRanges:
		var object corev1.LimitRange
		if json.Unmarshal(body, &object) != nil ||
			!validTypedPolicyIdentity(&object.ObjectMeta, object.APIVersion, object.Kind, input.Namespace, input.Name, "v1", "LimitRange") {
			return nil, ErrInvalidResponse
		}
		spec, err := limitRangeKubernetesSpec(*input.Spec.LimitRange)
		if err != nil {
			return nil, err
		}
		object.Spec = spec
		return typedPolicyObject(&object, ErrInvalidResponse)
	case PolicyNetworkPolicies:
		var object networkingv1.NetworkPolicy
		if json.Unmarshal(body, &object) != nil ||
			!validTypedPolicyIdentity(&object.ObjectMeta, object.APIVersion, object.Kind, input.Namespace, input.Name, "networking.k8s.io/v1", "NetworkPolicy") {
			return nil, ErrInvalidResponse
		}
		spec, err := networkPolicyKubernetesSpec(*input.Spec.NetworkPolicy)
		if err != nil {
			return nil, err
		}
		object.Spec = spec
		return typedPolicyObject(&object, ErrInvalidResponse)
	case PolicyDisruptionBudgets:
		var object policyv1.PodDisruptionBudget
		if json.Unmarshal(body, &object) != nil ||
			!validTypedPolicyIdentity(&object.ObjectMeta, object.APIVersion, object.Kind, input.Namespace, input.Name, "policy/v1", "PodDisruptionBudget") {
			return nil, ErrInvalidResponse
		}
		// The selector is deliberately left as it is: repointing a budget at a
		// different set of Pods silently unprotects the ones it used to cover,
		// which is a different operation from adjusting the budget itself.
		budget, err := disruptionBudgetValues(input.Spec.DisruptionBudget.MinAvailable, input.Spec.DisruptionBudget.MaxUnavailable)
		if err != nil {
			return nil, err
		}
		policy, err := unhealthyPodEvictionPolicy(input.Spec.DisruptionBudget.UnhealthyPodEvictionPolicy)
		if err != nil {
			return nil, err
		}
		object.Spec.MinAvailable, object.Spec.MaxUnavailable = budget.min, budget.max
		object.Spec.UnhealthyPodEvictionPolicy = policy
		return typedPolicyObject(&object, ErrInvalidResponse)
	case PolicyPriorityClasses:
		var object schedulingv1.PriorityClass
		if json.Unmarshal(body, &object) != nil ||
			!validTypedPolicyIdentity(&object.ObjectMeta, object.APIVersion, object.Kind, input.Namespace, input.Name, "scheduling.k8s.io/v1", "PriorityClass") {
			return nil, ErrInvalidResponse
		}
		if len(input.Spec.PriorityClass.Description) > maxPolicyDescriptionLength {
			return nil, ErrInvalidInput
		}
		object.Description = input.Spec.PriorityClass.Description
		object.GlobalDefault = *input.Spec.PriorityClass.GlobalDefault
		return typedPolicyObject(&object, ErrInvalidResponse)
	default:
		return nil, ErrInvalidInput
	}
}

// exactlyOnePolicySpec holds a create request to the one spec block its kind
// understands. A request carrying two blocks is ambiguous about which object it
// means, and one carrying none is not a create request at all.
func exactlyOnePolicySpec(resourceName PolicyResource, spec PolicyCreateSpec) bool {
	present := 0
	for _, block := range []bool{
		spec.ResourceQuota != nil, spec.LimitRange != nil, spec.NetworkPolicy != nil,
		spec.DisruptionBudget != nil, spec.PriorityClass != nil,
	} {
		if block {
			present++
		}
	}
	if present != 1 {
		return false
	}
	switch resourceName {
	case PolicyResourceQuotas:
		return spec.ResourceQuota != nil
	case PolicyLimitRanges:
		return spec.LimitRange != nil
	case PolicyNetworkPolicies:
		return spec.NetworkPolicy != nil
	case PolicyDisruptionBudgets:
		return spec.DisruptionBudget != nil
	case PolicyPriorityClasses:
		return spec.PriorityClass != nil
	default:
		return false
	}
}

func exactlyOnePolicyUpdateSpec(resourceName PolicyResource, spec PolicyUpdateSpec) bool {
	present := 0
	for _, block := range []bool{
		spec.ResourceQuota != nil, spec.LimitRange != nil, spec.NetworkPolicy != nil,
		spec.DisruptionBudget != nil, spec.PriorityClass != nil,
	} {
		if block {
			present++
		}
	}
	if present != 1 {
		return false
	}
	switch resourceName {
	case PolicyResourceQuotas:
		return spec.ResourceQuota != nil
	case PolicyLimitRanges:
		return spec.LimitRange != nil
	case PolicyNetworkPolicies:
		return spec.NetworkPolicy != nil
	case PolicyDisruptionBudgets:
		return spec.DisruptionBudget != nil
	case PolicyPriorityClasses:
		return spec.PriorityClass != nil && spec.PriorityClass.GlobalDefault != nil
	default:
		return false
	}
}

func resourceQuotaKubernetesSpec(input ResourceQuotaSpecInput) (corev1.ResourceQuotaSpec, error) {
	hard, err := policyResourceList(input.Hard)
	if err != nil {
		return corev1.ResourceQuotaSpec{}, err
	}
	if len(hard) == 0 {
		return corev1.ResourceQuotaSpec{}, ErrInvalidInput
	}
	scopes := make([]corev1.ResourceQuotaScope, 0, len(input.Scopes))
	seen := map[string]struct{}{}
	for _, scope := range input.Scopes {
		if !validResourceQuotaScope(scope) {
			return corev1.ResourceQuotaSpec{}, ErrInvalidInput
		}
		if _, exists := seen[scope]; exists {
			return corev1.ResourceQuotaSpec{}, ErrInvalidInput
		}
		seen[scope] = struct{}{}
		scopes = append(scopes, corev1.ResourceQuotaScope(scope))
	}
	selector, err := resourceQuotaScopeSelector(input.ScopeSelector)
	if err != nil {
		return corev1.ResourceQuotaSpec{}, err
	}
	return corev1.ResourceQuotaSpec{Hard: hard, Scopes: scopes, ScopeSelector: selector}, nil
}

func resourceQuotaScopeSelector(input []PolicyScopeSelectorRequirement) (*corev1.ScopeSelector, error) {
	if len(input) == 0 {
		return nil, nil
	}
	if len(input) > maxPolicyScopeSelectors {
		return nil, ErrInvalidInput
	}
	result := &corev1.ScopeSelector{MatchExpressions: make([]corev1.ScopedResourceSelectorRequirement, 0, len(input))}
	for _, requirement := range input {
		if !validResourceQuotaScope(requirement.ScopeName) || len(requirement.Values) > maxPolicySelectorValues {
			return nil, ErrInvalidInput
		}
		switch corev1.ScopeSelectorOperator(requirement.Operator) {
		case corev1.ScopeSelectorOpIn, corev1.ScopeSelectorOpNotIn:
			if len(requirement.Values) == 0 {
				return nil, ErrInvalidInput
			}
		case corev1.ScopeSelectorOpExists, corev1.ScopeSelectorOpDoesNotExist:
			if len(requirement.Values) != 0 {
				return nil, ErrInvalidInput
			}
		default:
			return nil, ErrInvalidInput
		}
		result.MatchExpressions = append(result.MatchExpressions, corev1.ScopedResourceSelectorRequirement{
			ScopeName: corev1.ResourceQuotaScope(requirement.ScopeName),
			Operator:  corev1.ScopeSelectorOperator(requirement.Operator),
			Values:    slices.Clone(requirement.Values),
		})
	}
	return result, nil
}

func limitRangeKubernetesSpec(input LimitRangeSpecInput) (corev1.LimitRangeSpec, error) {
	if len(input.Items) == 0 || len(input.Items) > maxPolicyLimitRangeItems {
		return corev1.LimitRangeSpec{}, ErrInvalidInput
	}
	result := corev1.LimitRangeSpec{Limits: make([]corev1.LimitRangeItem, 0, len(input.Items))}
	seen := map[string]struct{}{}
	for _, item := range input.Items {
		if !validLimitRangeType(item.Type) {
			return corev1.LimitRangeSpec{}, ErrInvalidInput
		}
		if _, exists := seen[item.Type]; exists {
			return corev1.LimitRangeSpec{}, ErrInvalidInput
		}
		seen[item.Type] = struct{}{}
		converted := corev1.LimitRangeItem{Type: corev1.LimitType(item.Type)}
		for target, values := range map[*corev1.ResourceList]map[string]string{
			&converted.Max: item.Max, &converted.Min: item.Min, &converted.Default: item.Default,
			&converted.DefaultRequest: item.DefaultRequest, &converted.MaxLimitRequestRatio: item.MaxLimitRequestRatio,
		} {
			list, err := policyResourceList(values)
			if err != nil {
				return corev1.LimitRangeSpec{}, err
			}
			*target = list
		}
		if len(converted.Max)+len(converted.Min)+len(converted.Default)+
			len(converted.DefaultRequest)+len(converted.MaxLimitRequestRatio) == 0 {
			return corev1.LimitRangeSpec{}, ErrInvalidInput
		}
		result.Limits = append(result.Limits, converted)
	}
	return result, nil
}

func networkPolicyKubernetesSpec(input NetworkPolicySpecInput) (networkingv1.NetworkPolicySpec, error) {
	selector, err := policyLabelSelector(input.PodSelector)
	if err != nil {
		return networkingv1.NetworkPolicySpec{}, err
	}
	// An empty podSelector is the documented way to write "every Pod in this
	// Namespace", so an absent selector is kept as the empty one rather than
	// rejected.
	spec := networkingv1.NetworkPolicySpec{}
	if selector != nil {
		spec.PodSelector = *selector
	}
	types := make([]networkingv1.PolicyType, 0, len(input.PolicyTypes))
	seen := map[string]struct{}{}
	for _, policyType := range input.PolicyTypes {
		if policyType != string(networkingv1.PolicyTypeIngress) && policyType != string(networkingv1.PolicyTypeEgress) {
			return networkingv1.NetworkPolicySpec{}, ErrInvalidInput
		}
		if _, exists := seen[policyType]; exists {
			return networkingv1.NetworkPolicySpec{}, ErrInvalidInput
		}
		seen[policyType] = struct{}{}
		types = append(types, networkingv1.PolicyType(policyType))
	}
	if len(types) == 0 {
		return networkingv1.NetworkPolicySpec{}, ErrInvalidInput
	}
	spec.PolicyTypes = types
	if len(input.Ingress) > maxPolicyNetworkRules || len(input.Egress) > maxPolicyNetworkRules {
		return networkingv1.NetworkPolicySpec{}, ErrInvalidInput
	}
	for _, rule := range input.Ingress {
		peers, ports, err := networkPolicyRuleParts(rule)
		if err != nil {
			return networkingv1.NetworkPolicySpec{}, err
		}
		spec.Ingress = append(spec.Ingress, networkingv1.NetworkPolicyIngressRule{From: peers, Ports: ports})
	}
	for _, rule := range input.Egress {
		peers, ports, err := networkPolicyRuleParts(rule)
		if err != nil {
			return networkingv1.NetworkPolicySpec{}, err
		}
		spec.Egress = append(spec.Egress, networkingv1.NetworkPolicyEgressRule{To: peers, Ports: ports})
	}
	// Rules under a type the policy does not declare are silently ignored by
	// Kubernetes, which is exactly the kind of policy that looks applied and is
	// not.
	if len(spec.Ingress) > 0 && !slices.Contains(types, networkingv1.PolicyTypeIngress) ||
		len(spec.Egress) > 0 && !slices.Contains(types, networkingv1.PolicyTypeEgress) {
		return networkingv1.NetworkPolicySpec{}, ErrInvalidInput
	}
	return spec, nil
}

func networkPolicyRuleParts(rule NetworkPolicyRule) ([]networkingv1.NetworkPolicyPeer, []networkingv1.NetworkPolicyPort, error) {
	if len(rule.Peers) > maxPolicyNetworkPeers || len(rule.Ports) > maxPolicyNetworkPorts {
		return nil, nil, ErrInvalidInput
	}
	peers := make([]networkingv1.NetworkPolicyPeer, 0, len(rule.Peers))
	for _, peer := range rule.Peers {
		converted, err := networkPolicyKubernetesPeer(peer)
		if err != nil {
			return nil, nil, err
		}
		peers = append(peers, converted)
	}
	ports := make([]networkingv1.NetworkPolicyPort, 0, len(rule.Ports))
	for _, port := range rule.Ports {
		converted, err := networkPolicyKubernetesPort(port)
		if err != nil {
			return nil, nil, err
		}
		ports = append(ports, converted)
	}
	return peers, ports, nil
}

func networkPolicyKubernetesPeer(peer NetworkPolicyPeer) (networkingv1.NetworkPolicyPeer, error) {
	// Kubernetes treats an ipBlock peer as mutually exclusive with the two
	// selectors, and a peer with nothing set matches nothing at all.
	if peer.IPBlock != nil && (peer.PodSelector != nil || peer.NamespaceSelector != nil) ||
		peer.IPBlock == nil && peer.PodSelector == nil && peer.NamespaceSelector == nil {
		return networkingv1.NetworkPolicyPeer{}, ErrInvalidInput
	}
	if peer.IPBlock != nil {
		block, err := networkPolicyIPBlock(*peer.IPBlock)
		if err != nil {
			return networkingv1.NetworkPolicyPeer{}, err
		}
		return networkingv1.NetworkPolicyPeer{IPBlock: block}, nil
	}
	podSelector, err := policyLabelSelector(peer.PodSelector)
	if err != nil {
		return networkingv1.NetworkPolicyPeer{}, err
	}
	namespaceSelector, err := policyLabelSelector(peer.NamespaceSelector)
	if err != nil {
		return networkingv1.NetworkPolicyPeer{}, err
	}
	return networkingv1.NetworkPolicyPeer{PodSelector: podSelector, NamespaceSelector: namespaceSelector}, nil
}

func networkPolicyIPBlock(input NetworkPolicyIPBlock) (*networkingv1.IPBlock, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(input.CIDR))
	if err != nil || prefix.Masked() != prefix {
		return nil, ErrInvalidInput
	}
	if len(input.Except) > maxPolicySelectorValues {
		return nil, ErrInvalidInput
	}
	for _, except := range input.Except {
		exception, err := netip.ParsePrefix(strings.TrimSpace(except))
		// An exception outside the block it belongs to is rejected by Kubernetes
		// too; catching it here keeps the error a request error.
		if err != nil || exception.Masked() != exception || exception.Addr().Is4() != prefix.Addr().Is4() ||
			exception.Bits() < prefix.Bits() || !prefix.Contains(exception.Addr()) {
			return nil, ErrInvalidInput
		}
	}
	return &networkingv1.IPBlock{CIDR: strings.TrimSpace(input.CIDR), Except: trimmedStrings(input.Except)}, nil
}

func networkPolicyKubernetesPort(input NetworkPolicyPort) (networkingv1.NetworkPolicyPort, error) {
	result := networkingv1.NetworkPolicyPort{}
	if input.Protocol != "" {
		switch corev1.Protocol(input.Protocol) {
		case corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolSCTP:
			protocol := corev1.Protocol(input.Protocol)
			result.Protocol = &protocol
		default:
			return networkingv1.NetworkPolicyPort{}, ErrInvalidInput
		}
	}
	value := strings.TrimSpace(input.Port)
	if value == "" {
		// No port at all means every port, and a range needs a start to run from.
		if input.EndPort != nil {
			return networkingv1.NetworkPolicyPort{}, ErrInvalidInput
		}
		return result, nil
	}
	if number, err := strconv.ParseInt(value, 10, 32); err == nil {
		if number < 1 || number > 65535 {
			return networkingv1.NetworkPolicyPort{}, ErrInvalidInput
		}
		port := intstr.FromInt32(int32(number))
		result.Port = &port
		if input.EndPort != nil {
			if int64(*input.EndPort) < number || *input.EndPort > 65535 {
				return networkingv1.NetworkPolicyPort{}, ErrInvalidInput
			}
			endPort := *input.EndPort
			result.EndPort = &endPort
		}
		return result, nil
	}
	// A named port names one container port, so a range from it is meaningless.
	if len(k8svalidation.IsValidPortName(value)) != 0 || input.EndPort != nil {
		return networkingv1.NetworkPolicyPort{}, ErrInvalidInput
	}
	port := intstr.FromString(value)
	result.Port = &port
	return result, nil
}

type disruptionBudgetPair struct {
	min *intstr.IntOrString
	max *intstr.IntOrString
}

func disruptionBudgetValues(minAvailable, maxUnavailable string) (disruptionBudgetPair, error) {
	minValue, maxValue := strings.TrimSpace(minAvailable), strings.TrimSpace(maxUnavailable)
	// Kubernetes rejects a budget that sets both, and one that sets neither
	// protects nothing while looking like protection.
	if (minValue == "") == (maxValue == "") {
		return disruptionBudgetPair{}, ErrInvalidInput
	}
	if minValue != "" {
		parsed, err := policyIntOrPercent(minValue)
		if err != nil {
			return disruptionBudgetPair{}, err
		}
		return disruptionBudgetPair{min: parsed}, nil
	}
	parsed, err := policyIntOrPercent(maxValue)
	if err != nil {
		return disruptionBudgetPair{}, err
	}
	return disruptionBudgetPair{max: parsed}, nil
}

func unhealthyPodEvictionPolicy(value string) (*policyv1.UnhealthyPodEvictionPolicyType, error) {
	if value == "" {
		return nil, nil
	}
	switch policyv1.UnhealthyPodEvictionPolicyType(value) {
	case policyv1.IfHealthyBudget, policyv1.AlwaysAllow:
		policy := policyv1.UnhealthyPodEvictionPolicyType(value)
		return &policy, nil
	default:
		return nil, ErrInvalidInput
	}
}

func disruptionBudgetKubernetesSpec(input DisruptionBudgetSpecInput) (policyv1.PodDisruptionBudgetSpec, error) {
	budget, err := disruptionBudgetValues(input.MinAvailable, input.MaxUnavailable)
	if err != nil {
		return policyv1.PodDisruptionBudgetSpec{}, err
	}
	selector, err := policyLabelSelector(input.Selector)
	if err != nil || selector == nil {
		return policyv1.PodDisruptionBudgetSpec{}, ErrInvalidInput
	}
	policy, err := unhealthyPodEvictionPolicy(input.UnhealthyPodEvictionPolicy)
	if err != nil {
		return policyv1.PodDisruptionBudgetSpec{}, err
	}
	return policyv1.PodDisruptionBudgetSpec{
		MinAvailable: budget.min, MaxUnavailable: budget.max, Selector: selector,
		UnhealthyPodEvictionPolicy: policy,
	}, nil
}

func priorityClassKubernetesObject(metadata metav1.ObjectMeta, input PriorityClassSpecInput) (*schedulingv1.PriorityClass, error) {
	if input.Value > maxPolicyPriorityValue || len(input.Description) > maxPolicyDescriptionLength {
		return nil, ErrInvalidInput
	}
	result := &schedulingv1.PriorityClass{
		TypeMeta:   metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1", Kind: "PriorityClass"},
		ObjectMeta: metadata, Value: input.Value, GlobalDefault: input.GlobalDefault,
		Description: input.Description,
	}
	if input.PreemptionPolicy != "" {
		switch corev1.PreemptionPolicy(input.PreemptionPolicy) {
		case corev1.PreemptLowerPriority, corev1.PreemptNever:
			policy := corev1.PreemptionPolicy(input.PreemptionPolicy)
			result.PreemptionPolicy = &policy
		default:
			return nil, ErrInvalidInput
		}
	}
	return result, nil
}

func policyResourceDetail(object map[string]any, resourceName PolicyResource, namespace, name string) (PolicyResourceDetail, error) {
	body, err := json.Marshal(object)
	if err != nil {
		return PolicyResourceDetail{}, ErrInvalidResponse
	}
	switch resourceName {
	case PolicyResourceQuotas:
		var value corev1.ResourceQuota
		if json.Unmarshal(body, &value) != nil ||
			!validTypedPolicyIdentity(&value.ObjectMeta, value.APIVersion, value.Kind, namespace, name, "v1", "ResourceQuota") {
			return PolicyResourceDetail{}, ErrInvalidResponse
		}
		return resourceQuotaResourceDetail(&value), nil
	case PolicyLimitRanges:
		var value corev1.LimitRange
		if json.Unmarshal(body, &value) != nil ||
			!validTypedPolicyIdentity(&value.ObjectMeta, value.APIVersion, value.Kind, namespace, name, "v1", "LimitRange") {
			return PolicyResourceDetail{}, ErrInvalidResponse
		}
		return limitRangeResourceDetail(&value), nil
	case PolicyNetworkPolicies:
		var value networkingv1.NetworkPolicy
		if json.Unmarshal(body, &value) != nil ||
			!validTypedPolicyIdentity(&value.ObjectMeta, value.APIVersion, value.Kind, namespace, name, "networking.k8s.io/v1", "NetworkPolicy") {
			return PolicyResourceDetail{}, ErrInvalidResponse
		}
		return networkPolicyResourceDetail(&value), nil
	case PolicyDisruptionBudgets:
		var value policyv1.PodDisruptionBudget
		if json.Unmarshal(body, &value) != nil ||
			!validTypedPolicyIdentity(&value.ObjectMeta, value.APIVersion, value.Kind, namespace, name, "policy/v1", "PodDisruptionBudget") {
			return PolicyResourceDetail{}, ErrInvalidResponse
		}
		return disruptionBudgetResourceDetail(&value), nil
	case PolicyPriorityClasses:
		var value schedulingv1.PriorityClass
		if json.Unmarshal(body, &value) != nil ||
			!validTypedPolicyIdentity(&value.ObjectMeta, value.APIVersion, value.Kind, namespace, name, "scheduling.k8s.io/v1", "PriorityClass") {
			return PolicyResourceDetail{}, ErrInvalidResponse
		}
		return priorityClassResourceDetail(&value), nil
	default:
		return PolicyResourceDetail{}, ErrInvalidInput
	}
}

func resourceQuotaResourceDetail(value *corev1.ResourceQuota) PolicyResourceDetail {
	scopes := make([]string, 0, len(value.Spec.Scopes))
	for _, scope := range value.Spec.Scopes {
		scopes = append(scopes, string(scope))
	}
	// `hard` is read from the status when Kubernetes has published one: that is
	// the quota actually being enforced, which is what an operator is asking
	// about when the spec was only just changed.
	hard := value.Status.Hard
	if len(hard) == 0 {
		hard = value.Spec.Hard
	}
	requirements := make([]PolicyScopeSelectorRequirement, 0)
	if value.Spec.ScopeSelector != nil {
		for _, requirement := range value.Spec.ScopeSelector.MatchExpressions {
			requirements = append(requirements, PolicyScopeSelectorRequirement{
				ScopeName: string(requirement.ScopeName), Operator: string(requirement.Operator),
				Values: normalizedStrings(requirement.Values),
			})
		}
	}
	summary := ResourceQuotaSummary{
		Hard: policyQuantityMap(hard), Used: policyQuantityMap(value.Status.Used), Scopes: scopes,
		ScopeSelector: requirements,
	}
	return PolicyResourceDetail{
		PolicyResourceSummary: policySummary(PolicyResourceQuotas, value.TypeMeta, value.ObjectMeta, func(s *PolicyResourceSummary) {
			s.ResourceQuota = &summary
		}),
		Annotations: normalizedStringMap(value.Annotations),
	}
}

func limitRangeResourceDetail(value *corev1.LimitRange) PolicyResourceDetail {
	items := make([]LimitRangeItem, 0, len(value.Spec.Limits))
	types := make([]string, 0, len(value.Spec.Limits))
	for _, item := range value.Spec.Limits {
		types = append(types, string(item.Type))
		items = append(items, LimitRangeItem{
			Type: string(item.Type), Max: policyQuantityMap(item.Max), Min: policyQuantityMap(item.Min),
			Default: policyQuantityMap(item.Default), DefaultRequest: policyQuantityMap(item.DefaultRequest),
			MaxLimitRequestRatio: policyQuantityMap(item.MaxLimitRequestRatio),
		})
	}
	summary := LimitRangeSummary{Types: types, ItemCount: len(items)}
	return PolicyResourceDetail{
		PolicyResourceSummary: policySummary(PolicyLimitRanges, value.TypeMeta, value.ObjectMeta, func(s *PolicyResourceSummary) {
			s.LimitRange = &summary
		}),
		Annotations:      normalizedStringMap(value.Annotations),
		LimitRangeDetail: &LimitRangeDetail{Items: items},
	}
}

func networkPolicyResourceDetail(value *networkingv1.NetworkPolicy) PolicyResourceDetail {
	types := make([]string, 0, len(value.Spec.PolicyTypes))
	for _, policyType := range value.Spec.PolicyTypes {
		types = append(types, string(policyType))
	}
	summary := NetworkPolicySummary{
		PodSelector: policySelectorView(&value.Spec.PodSelector), PolicyTypes: types,
		IngressRules: len(value.Spec.Ingress), EgressRules: len(value.Spec.Egress),
	}
	ingress := make([]NetworkPolicyRule, 0, len(value.Spec.Ingress))
	for _, rule := range value.Spec.Ingress {
		ingress = append(ingress, NetworkPolicyRule{
			Peers: networkPolicyPeerViews(rule.From), Ports: networkPolicyPortViews(rule.Ports),
		})
	}
	egress := make([]NetworkPolicyRule, 0, len(value.Spec.Egress))
	for _, rule := range value.Spec.Egress {
		egress = append(egress, NetworkPolicyRule{
			Peers: networkPolicyPeerViews(rule.To), Ports: networkPolicyPortViews(rule.Ports),
		})
	}
	return PolicyResourceDetail{
		PolicyResourceSummary: policySummary(PolicyNetworkPolicies, value.TypeMeta, value.ObjectMeta, func(s *PolicyResourceSummary) {
			s.NetworkPolicy = &summary
		}),
		Annotations:         normalizedStringMap(value.Annotations),
		NetworkPolicyDetail: &NetworkPolicyDetail{Ingress: ingress, Egress: egress},
	}
}

func disruptionBudgetResourceDetail(value *policyv1.PodDisruptionBudget) PolicyResourceDetail {
	summary := DisruptionBudgetSummary{
		Selector: policySelectorView(value.Spec.Selector), MinAvailable: intOrStringValue(value.Spec.MinAvailable),
		MaxUnavailable: intOrStringValue(value.Spec.MaxUnavailable), CurrentHealthy: value.Status.CurrentHealthy,
		DesiredHealthy: value.Status.DesiredHealthy, DisruptionsAllowed: value.Status.DisruptionsAllowed,
		ExpectedPods: value.Status.ExpectedPods,
	}
	conditions := make([]PolicyCondition, 0, len(value.Status.Conditions))
	for _, condition := range value.Status.Conditions {
		conditions = append(conditions, PolicyCondition{
			Type: condition.Type, Status: string(condition.Status), Reason: condition.Reason,
			Message: condition.Message, LastTransitionTime: condition.LastTransitionTime.Time,
		})
	}
	policy := ""
	if value.Spec.UnhealthyPodEvictionPolicy != nil {
		policy = string(*value.Spec.UnhealthyPodEvictionPolicy)
	}
	return PolicyResourceDetail{
		PolicyResourceSummary: policySummary(PolicyDisruptionBudgets, value.TypeMeta, value.ObjectMeta, func(s *PolicyResourceSummary) {
			s.DisruptionBudget = &summary
		}),
		Annotations:            normalizedStringMap(value.Annotations),
		DisruptionBudgetDetail: &DisruptionBudgetDetail{UnhealthyPodEvictionPolicy: policy, Conditions: conditions},
	}
}

func priorityClassResourceDetail(value *schedulingv1.PriorityClass) PolicyResourceDetail {
	preemption := ""
	if value.PreemptionPolicy != nil {
		preemption = string(*value.PreemptionPolicy)
	}
	summary := PriorityClassSummary{
		Value: value.Value, GlobalDefault: value.GlobalDefault,
		PreemptionPolicy: preemption, Description: value.Description,
	}
	return PolicyResourceDetail{
		PolicyResourceSummary: policySummary(PolicyPriorityClasses, value.TypeMeta, value.ObjectMeta, func(s *PolicyResourceSummary) {
			s.PriorityClass = &summary
		}),
		Annotations: normalizedStringMap(value.Annotations),
	}
}

func policySummary(
	resourceName PolicyResource,
	typeMeta metav1.TypeMeta,
	metadata metav1.ObjectMeta,
	attach func(*PolicyResourceSummary),
) PolicyResourceSummary {
	summary := PolicyResourceSummary{
		Resource: resourceName, APIVersion: typeMeta.APIVersion, Kind: typeMeta.Kind,
		Namespace: metadata.Namespace, Name: metadata.Name, UID: string(metadata.UID),
		ResourceVersion: metadata.ResourceVersion, CreationTimestamp: metadata.CreationTimestamp.Time,
		Labels: normalizedStringMap(metadata.Labels),
	}
	attach(&summary)
	return summary
}

func networkPolicyPeerViews(values []networkingv1.NetworkPolicyPeer) []NetworkPolicyPeer {
	result := make([]NetworkPolicyPeer, 0, len(values))
	for _, peer := range values {
		view := NetworkPolicyPeer{
			PodSelector: policySelectorView(peer.PodSelector), NamespaceSelector: policySelectorView(peer.NamespaceSelector),
		}
		if peer.IPBlock != nil {
			view.IPBlock = &NetworkPolicyIPBlock{CIDR: peer.IPBlock.CIDR, Except: normalizedStrings(peer.IPBlock.Except)}
		}
		result = append(result, view)
	}
	return result
}

func networkPolicyPortViews(values []networkingv1.NetworkPolicyPort) []NetworkPolicyPort {
	result := make([]NetworkPolicyPort, 0, len(values))
	for _, port := range values {
		view := NetworkPolicyPort{Port: intOrStringValue(port.Port)}
		if port.Protocol != nil {
			view.Protocol = string(*port.Protocol)
		}
		if port.EndPort != nil {
			endPort := *port.EndPort
			view.EndPort = &endPort
		}
		result = append(result, view)
	}
	return result
}

// policyLabelSelector converts a selector and proves Kubernetes can compile it,
// so an unusable selector is a request error here rather than a rejection from
// the API server one round trip later.
func policyLabelSelector(input *WorkloadSelector) (*metav1.LabelSelector, error) {
	if input == nil {
		return nil, nil
	}
	result := gatewayLabelSelector(input)
	if _, err := metav1.LabelSelectorAsSelector(result); err != nil {
		return nil, ErrInvalidInput
	}
	return result, nil
}

func policySelectorView(value *metav1.LabelSelector) *WorkloadSelector {
	if value == nil {
		return nil
	}
	result := &WorkloadSelector{
		MatchLabels:      normalizedStringMap(value.MatchLabels),
		MatchExpressions: make([]WorkloadSelectorRequirement, 0, len(value.MatchExpressions)),
	}
	for _, expression := range value.MatchExpressions {
		result.MatchExpressions = append(result.MatchExpressions, WorkloadSelectorRequirement{
			Key: expression.Key, Operator: string(expression.Operator), Values: normalizedStrings(expression.Values),
		})
	}
	return result
}

// policyResourceList parses a resource name to quantity map. Quantities are
// carried as strings end to end so `100m` and `1Gi` reach Kubernetes written the
// way the operator wrote them.
func policyResourceList(values map[string]string) (corev1.ResourceList, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > maxPolicyQuantities {
		return nil, ErrInvalidInput
	}
	result := make(corev1.ResourceList, len(values))
	for key, value := range values {
		if len(k8svalidation.IsQualifiedName(key)) != 0 {
			return nil, ErrInvalidInput
		}
		quantity, err := resource.ParseQuantity(strings.TrimSpace(value))
		if err != nil || quantity.Sign() < 0 {
			return nil, ErrInvalidInput
		}
		result[corev1.ResourceName(key)] = quantity
	}
	return result, nil
}

func policyQuantityMap(values corev1.ResourceList) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[string(key)] = value.String()
	}
	return result
}

// policyIntOrPercent accepts the two forms Kubernetes accepts for a budget: a
// count of Pods, or a percentage of the selected set.
func policyIntOrPercent(value string) (*intstr.IntOrString, error) {
	if strings.HasSuffix(value, "%") {
		percentage, err := strconv.ParseInt(strings.TrimSuffix(value, "%"), 10, 32)
		if err != nil || percentage < 0 || percentage > 100 {
			return nil, ErrInvalidInput
		}
		result := intstr.FromString(value)
		return &result, nil
	}
	count, err := strconv.ParseInt(value, 10, 32)
	if err != nil || count < 0 || count > 1_000_000 {
		return nil, ErrInvalidInput
	}
	result := intstr.FromInt32(int32(count))
	return &result, nil
}

func intOrStringValue(value *intstr.IntOrString) string {
	if value == nil {
		return ""
	}
	return value.String()
}

func validResourceQuotaScope(value string) bool {
	switch corev1.ResourceQuotaScope(value) {
	case corev1.ResourceQuotaScopeTerminating, corev1.ResourceQuotaScopeNotTerminating,
		corev1.ResourceQuotaScopeBestEffort, corev1.ResourceQuotaScopeNotBestEffort,
		corev1.ResourceQuotaScopePriorityClass, corev1.ResourceQuotaScopeCrossNamespacePodAffinity:
		return true
	default:
		return false
	}
}

func validLimitRangeType(value string) bool {
	switch corev1.LimitType(value) {
	case corev1.LimitTypeContainer, corev1.LimitTypePod, corev1.LimitTypePersistentVolumeClaim:
		return true
	default:
		return false
	}
}

func typedPolicyObject(object any, failure error) (map[string]any, error) {
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

func validTypedPolicyIdentity(metadata *metav1.ObjectMeta, apiVersion, kind, namespace, name, expectedVersion, expectedKind string) bool {
	return apiVersion == expectedVersion && kind == expectedKind && metadata.Name != "" &&
		metadata.Namespace == namespace && (name == "" || metadata.Name == name)
}

func trimmedStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, strings.TrimSpace(value))
	}
	return result
}
