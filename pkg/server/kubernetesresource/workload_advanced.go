package kubernetesresource

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
)

const (
	maxWorkloadContainerPorts               = 100
	maxWorkloadAffinityTerms                = 50
	maxWorkloadSelectorRequirements         = 50
	maxWorkloadSelectorValues               = 50
	maxWorkloadAffinityNamespaces           = 50
	maxWorkloadTopologySpreadConstraints    = 50
	maxWorkloadTopologySpreadMatchLabelKeys = 50
)

// WorkloadContainerPort is the service-facing subset of corev1.ContainerPort.
// Host ports and host IPs stay YAML-only: allocating a node port is a materially
// different security and scheduling decision from documenting a container port.
type WorkloadContainerPort struct {
	Name          string `json:"name,omitempty"`
	ContainerPort int32  `json:"container_port"`
	Protocol      string `json:"protocol,omitempty"`
}

type WorkloadLabelSelector struct {
	MatchLabels      map[string]string             `json:"match_labels,omitempty"`
	MatchExpressions []WorkloadSelectorRequirement `json:"match_expressions,omitempty"`
}

type WorkloadNodeSelectorRequirement struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
}

type WorkloadNodeSelectorTerm struct {
	MatchExpressions []WorkloadNodeSelectorRequirement `json:"match_expressions,omitempty"`
	MatchFields      []WorkloadNodeSelectorRequirement `json:"match_fields,omitempty"`
}

type WorkloadPreferredNodeSelectorTerm struct {
	Weight     int32                    `json:"weight"`
	Preference WorkloadNodeSelectorTerm `json:"preference"`
}

type WorkloadNodeAffinity struct {
	Required  []WorkloadNodeSelectorTerm          `json:"required,omitempty"`
	Preferred []WorkloadPreferredNodeSelectorTerm `json:"preferred,omitempty"`
}

type WorkloadPodAffinityTerm struct {
	LabelSelector     *WorkloadLabelSelector `json:"label_selector,omitempty"`
	NamespaceSelector *WorkloadLabelSelector `json:"namespace_selector,omitempty"`
	Namespaces        []string               `json:"namespaces,omitempty"`
	TopologyKey       string                 `json:"topology_key"`
	MatchLabelKeys    []string               `json:"match_label_keys,omitempty"`
	MismatchLabelKeys []string               `json:"mismatch_label_keys,omitempty"`
}

type WorkloadWeightedPodAffinityTerm struct {
	Weight  int32                   `json:"weight"`
	PodTerm WorkloadPodAffinityTerm `json:"pod_term"`
}

type WorkloadPodAffinity struct {
	Required  []WorkloadPodAffinityTerm         `json:"required,omitempty"`
	Preferred []WorkloadWeightedPodAffinityTerm `json:"preferred,omitempty"`
}

type WorkloadAffinity struct {
	NodeAffinity    *WorkloadNodeAffinity `json:"node_affinity,omitempty"`
	PodAffinity     *WorkloadPodAffinity  `json:"pod_affinity,omitempty"`
	PodAntiAffinity *WorkloadPodAffinity  `json:"pod_anti_affinity,omitempty"`
}

type WorkloadTopologySpreadConstraint struct {
	MaxSkew            int32                  `json:"max_skew"`
	TopologyKey        string                 `json:"topology_key"`
	WhenUnsatisfiable  string                 `json:"when_unsatisfiable"`
	LabelSelector      *WorkloadLabelSelector `json:"label_selector,omitempty"`
	MinDomains         *int32                 `json:"min_domains,omitempty"`
	NodeAffinityPolicy string                 `json:"node_affinity_policy,omitempty"`
	NodeTaintsPolicy   string                 `json:"node_taints_policy,omitempty"`
	MatchLabelKeys     []string               `json:"match_label_keys,omitempty"`
}

func validWorkloadContainerPorts(ports []WorkloadContainerPort) bool {
	if len(ports) > maxWorkloadContainerPorts {
		return false
	}
	names := make(map[string]struct{}, len(ports))
	for _, port := range ports {
		if port.ContainerPort < 1 || port.ContainerPort > 65535 {
			return false
		}
		if port.Protocol != "" &&
			port.Protocol != string(corev1.ProtocolTCP) &&
			port.Protocol != string(corev1.ProtocolUDP) &&
			port.Protocol != string(corev1.ProtocolSCTP) {
			return false
		}
		if port.Name == "" {
			continue
		}
		if len(k8svalidation.IsValidPortName(port.Name)) != 0 {
			return false
		}
		if _, exists := names[port.Name]; exists {
			return false
		}
		names[port.Name] = struct{}{}
	}
	return true
}

func validWorkloadAffinity(affinity *WorkloadAffinity) bool {
	if affinity == nil {
		return true
	}
	return validWorkloadNodeAffinity(affinity.NodeAffinity) &&
		validWorkloadPodAffinity(affinity.PodAffinity) &&
		validWorkloadPodAffinity(affinity.PodAntiAffinity)
}

func validWorkloadNodeAffinity(affinity *WorkloadNodeAffinity) bool {
	if affinity == nil {
		return true
	}
	if len(affinity.Required) > maxWorkloadAffinityTerms ||
		len(affinity.Preferred) > maxWorkloadAffinityTerms {
		return false
	}
	for _, term := range affinity.Required {
		if !validWorkloadNodeSelectorTerm(term) {
			return false
		}
	}
	for _, term := range affinity.Preferred {
		if term.Weight < 1 || term.Weight > 100 || !validWorkloadNodeSelectorTerm(term.Preference) {
			return false
		}
	}
	converted := workloadNodeAffinitySpec(affinity)
	if converted.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		if _, err := nodeaffinity.NewNodeSelector(converted.RequiredDuringSchedulingIgnoredDuringExecution); err != nil {
			return false
		}
	}
	if _, err := nodeaffinity.NewPreferredSchedulingTerms(converted.PreferredDuringSchedulingIgnoredDuringExecution); err != nil {
		return false
	}
	return true
}

func validWorkloadNodeSelectorTerm(term WorkloadNodeSelectorTerm) bool {
	if len(term.MatchExpressions)+len(term.MatchFields) == 0 ||
		len(term.MatchExpressions) > maxWorkloadSelectorRequirements ||
		len(term.MatchFields) > maxWorkloadSelectorRequirements {
		return false
	}
	requirements := append([]WorkloadNodeSelectorRequirement{}, term.MatchExpressions...)
	requirements = append(requirements, term.MatchFields...)
	for _, requirement := range requirements {
		if len(k8svalidation.IsQualifiedName(requirement.Key)) != 0 ||
			len(requirement.Values) > maxWorkloadSelectorValues {
			return false
		}
		switch corev1.NodeSelectorOperator(requirement.Operator) {
		case corev1.NodeSelectorOpIn, corev1.NodeSelectorOpNotIn:
			if len(requirement.Values) == 0 {
				return false
			}
		case corev1.NodeSelectorOpExists, corev1.NodeSelectorOpDoesNotExist:
			if len(requirement.Values) != 0 {
				return false
			}
		case corev1.NodeSelectorOpGt, corev1.NodeSelectorOpLt:
			if len(requirement.Values) != 1 {
				return false
			}
			if _, err := strconv.ParseInt(requirement.Values[0], 10, 64); err != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validWorkloadPodAffinity(affinity *WorkloadPodAffinity) bool {
	if affinity == nil {
		return true
	}
	if len(affinity.Required) > maxWorkloadAffinityTerms ||
		len(affinity.Preferred) > maxWorkloadAffinityTerms {
		return false
	}
	for _, term := range affinity.Required {
		if !validWorkloadPodAffinityTerm(term) {
			return false
		}
	}
	for _, term := range affinity.Preferred {
		if term.Weight < 1 || term.Weight > 100 || !validWorkloadPodAffinityTerm(term.PodTerm) {
			return false
		}
	}
	return true
}

func validWorkloadPodAffinityTerm(term WorkloadPodAffinityTerm) bool {
	if len(k8svalidation.IsQualifiedName(term.TopologyKey)) != 0 ||
		len(term.Namespaces) > maxWorkloadAffinityNamespaces ||
		!validWorkloadLabelSelector(term.LabelSelector) ||
		!validWorkloadLabelSelector(term.NamespaceSelector) ||
		!validWorkloadQualifiedKeys(term.MatchLabelKeys) ||
		!validWorkloadQualifiedKeys(term.MismatchLabelKeys) ||
		((len(term.MatchLabelKeys) > 0 || len(term.MismatchLabelKeys) > 0) && term.LabelSelector == nil) {
		return false
	}
	selectorKeys := workloadLabelSelectorKeys(term.LabelSelector)
	for _, key := range append(append([]string{}, term.MatchLabelKeys...), term.MismatchLabelKeys...) {
		if _, exists := selectorKeys[key]; exists {
			return false
		}
	}
	matchKeys := make(map[string]struct{}, len(term.MatchLabelKeys))
	for _, key := range term.MatchLabelKeys {
		matchKeys[key] = struct{}{}
	}
	for _, key := range term.MismatchLabelKeys {
		if _, exists := matchKeys[key]; exists {
			return false
		}
	}
	seen := make(map[string]struct{}, len(term.Namespaces))
	for _, namespace := range term.Namespaces {
		if len(k8svalidation.IsDNS1123Label(namespace)) != 0 {
			return false
		}
		if _, exists := seen[namespace]; exists {
			return false
		}
		seen[namespace] = struct{}{}
	}
	return true
}

func validWorkloadLabelSelector(selector *WorkloadLabelSelector) bool {
	if selector == nil {
		return true
	}
	if len(selector.MatchLabels) > maxWorkloadSelectorRequirements ||
		len(selector.MatchExpressions) > maxWorkloadSelectorRequirements ||
		!validNamespaceLabels(selector.MatchLabels) {
		return false
	}
	converted := metav1.LabelSelector{MatchLabels: selector.MatchLabels}
	for _, requirement := range selector.MatchExpressions {
		if len(requirement.Values) > maxWorkloadSelectorValues {
			return false
		}
		converted.MatchExpressions = append(converted.MatchExpressions, metav1.LabelSelectorRequirement{
			Key: requirement.Key, Operator: metav1.LabelSelectorOperator(requirement.Operator), Values: requirement.Values,
		})
	}
	_, err := metav1.LabelSelectorAsSelector(&converted)
	return err == nil
}

func validWorkloadQualifiedKeys(values []string) bool {
	if len(values) > maxWorkloadSelectorValues {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if len(k8svalidation.IsQualifiedName(value)) != 0 {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func workloadLabelSelectorKeys(selector *WorkloadLabelSelector) map[string]struct{} {
	result := map[string]struct{}{}
	if selector == nil {
		return result
	}
	for key := range selector.MatchLabels {
		result[key] = struct{}{}
	}
	for _, requirement := range selector.MatchExpressions {
		result[requirement.Key] = struct{}{}
	}
	return result
}

func validWorkloadTopologySpread(values []WorkloadTopologySpreadConstraint) bool {
	if len(values) > maxWorkloadTopologySpreadConstraints {
		return false
	}
	for _, value := range values {
		if value.MaxSkew < 1 ||
			len(k8svalidation.IsQualifiedName(value.TopologyKey)) != 0 ||
			(value.WhenUnsatisfiable != string(corev1.DoNotSchedule) &&
				value.WhenUnsatisfiable != string(corev1.ScheduleAnyway)) ||
			!validWorkloadLabelSelector(value.LabelSelector) ||
			(value.MinDomains != nil && (*value.MinDomains < 1 || value.WhenUnsatisfiable != string(corev1.DoNotSchedule))) ||
			!validWorkloadNodeInclusionPolicy(value.NodeAffinityPolicy) ||
			!validWorkloadNodeInclusionPolicy(value.NodeTaintsPolicy) ||
			len(value.MatchLabelKeys) > maxWorkloadTopologySpreadMatchLabelKeys {
			return false
		}
		seen := make(map[string]struct{}, len(value.MatchLabelKeys))
		for _, key := range value.MatchLabelKeys {
			if len(k8svalidation.IsQualifiedName(key)) != 0 {
				return false
			}
			if _, exists := seen[key]; exists {
				return false
			}
			seen[key] = struct{}{}
			if _, exists := workloadLabelSelectorKeys(value.LabelSelector)[key]; exists {
				return false
			}
		}
	}
	return true
}

func validWorkloadNodeInclusionPolicy(value string) bool {
	return value == "" || value == string(corev1.NodeInclusionPolicyHonor) ||
		value == string(corev1.NodeInclusionPolicyIgnore)
}

func workloadContainerPortSpec(values []WorkloadContainerPort) []corev1.ContainerPort {
	result := make([]corev1.ContainerPort, 0, len(values))
	for _, value := range values {
		protocol := corev1.Protocol(value.Protocol)
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		result = append(result, corev1.ContainerPort{Name: value.Name, ContainerPort: value.ContainerPort, Protocol: protocol})
	}
	return result
}

func workloadContainerPortView(values []corev1.ContainerPort) []WorkloadContainerPort {
	result := make([]WorkloadContainerPort, 0, len(values))
	for _, value := range values {
		result = append(result, WorkloadContainerPort{Name: value.Name, ContainerPort: value.ContainerPort, Protocol: string(value.Protocol)})
	}
	return result
}

func workloadAffinitySpec(value *WorkloadAffinity) *corev1.Affinity {
	if value == nil {
		return nil
	}
	result := &corev1.Affinity{
		NodeAffinity:    workloadNodeAffinitySpec(value.NodeAffinity),
		PodAffinity:     workloadPodAffinitySpec(value.PodAffinity),
		PodAntiAffinity: workloadPodAntiAffinitySpec(value.PodAntiAffinity),
	}
	if result.NodeAffinity == nil && result.PodAffinity == nil && result.PodAntiAffinity == nil {
		return nil
	}
	return result
}

func workloadPodAntiAffinitySpec(value *WorkloadPodAffinity) *corev1.PodAntiAffinity {
	converted := workloadPodAffinitySpec(value)
	if converted == nil {
		return nil
	}
	return &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution:  converted.RequiredDuringSchedulingIgnoredDuringExecution,
		PreferredDuringSchedulingIgnoredDuringExecution: converted.PreferredDuringSchedulingIgnoredDuringExecution,
	}
}

func workloadNodeAffinitySpec(value *WorkloadNodeAffinity) *corev1.NodeAffinity {
	if value == nil {
		return nil
	}
	result := &corev1.NodeAffinity{}
	if len(value.Required) > 0 {
		result.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{}
		for _, term := range value.Required {
			result.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = append(
				result.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms,
				workloadNodeSelectorTermSpec(term),
			)
		}
	}
	for _, term := range value.Preferred {
		result.PreferredDuringSchedulingIgnoredDuringExecution = append(result.PreferredDuringSchedulingIgnoredDuringExecution, corev1.PreferredSchedulingTerm{
			Weight: term.Weight, Preference: workloadNodeSelectorTermSpec(term.Preference),
		})
	}
	return result
}

func workloadNodeSelectorTermSpec(value WorkloadNodeSelectorTerm) corev1.NodeSelectorTerm {
	result := corev1.NodeSelectorTerm{}
	for _, requirement := range value.MatchExpressions {
		result.MatchExpressions = append(result.MatchExpressions, corev1.NodeSelectorRequirement{
			Key: requirement.Key, Operator: corev1.NodeSelectorOperator(requirement.Operator), Values: append([]string(nil), requirement.Values...),
		})
	}
	for _, requirement := range value.MatchFields {
		result.MatchFields = append(result.MatchFields, corev1.NodeSelectorRequirement{
			Key: requirement.Key, Operator: corev1.NodeSelectorOperator(requirement.Operator), Values: append([]string(nil), requirement.Values...),
		})
	}
	return result
}

func workloadPodAffinitySpec(value *WorkloadPodAffinity) *corev1.PodAffinity {
	if value == nil {
		return nil
	}
	result := &corev1.PodAffinity{}
	for _, term := range value.Required {
		result.RequiredDuringSchedulingIgnoredDuringExecution = append(result.RequiredDuringSchedulingIgnoredDuringExecution, workloadPodAffinityTermSpec(term))
	}
	for _, term := range value.Preferred {
		result.PreferredDuringSchedulingIgnoredDuringExecution = append(result.PreferredDuringSchedulingIgnoredDuringExecution, corev1.WeightedPodAffinityTerm{
			Weight: term.Weight, PodAffinityTerm: workloadPodAffinityTermSpec(term.PodTerm),
		})
	}
	return result
}

func workloadPodAffinityTermSpec(value WorkloadPodAffinityTerm) corev1.PodAffinityTerm {
	return corev1.PodAffinityTerm{
		LabelSelector:     workloadLabelSelectorSpec(value.LabelSelector),
		NamespaceSelector: workloadLabelSelectorSpec(value.NamespaceSelector),
		Namespaces:        append([]string(nil), value.Namespaces...),
		TopologyKey:       value.TopologyKey,
		MatchLabelKeys:    append([]string(nil), value.MatchLabelKeys...),
		MismatchLabelKeys: append([]string(nil), value.MismatchLabelKeys...),
	}
}

func workloadLabelSelectorSpec(value *WorkloadLabelSelector) *metav1.LabelSelector {
	if value == nil {
		return nil
	}
	result := &metav1.LabelSelector{MatchLabels: cloneMap(value.MatchLabels)}
	for _, requirement := range value.MatchExpressions {
		result.MatchExpressions = append(result.MatchExpressions, metav1.LabelSelectorRequirement{
			Key: requirement.Key, Operator: metav1.LabelSelectorOperator(requirement.Operator), Values: append([]string(nil), requirement.Values...),
		})
	}
	return result
}

func workloadTopologySpreadSpec(values []WorkloadTopologySpreadConstraint) []corev1.TopologySpreadConstraint {
	result := make([]corev1.TopologySpreadConstraint, 0, len(values))
	for _, value := range values {
		converted := corev1.TopologySpreadConstraint{
			MaxSkew: value.MaxSkew, TopologyKey: value.TopologyKey,
			WhenUnsatisfiable: corev1.UnsatisfiableConstraintAction(value.WhenUnsatisfiable),
			LabelSelector:     workloadLabelSelectorSpec(value.LabelSelector), MinDomains: copyPointer(value.MinDomains),
			MatchLabelKeys: append([]string(nil), value.MatchLabelKeys...),
		}
		if value.NodeAffinityPolicy != "" {
			policy := corev1.NodeInclusionPolicy(value.NodeAffinityPolicy)
			converted.NodeAffinityPolicy = &policy
		}
		if value.NodeTaintsPolicy != "" {
			policy := corev1.NodeInclusionPolicy(value.NodeTaintsPolicy)
			converted.NodeTaintsPolicy = &policy
		}
		result = append(result, converted)
	}
	return result
}

func workloadAffinityView(value *corev1.Affinity) *WorkloadAffinity {
	if value == nil {
		return nil
	}
	return &WorkloadAffinity{
		NodeAffinity:    workloadNodeAffinityView(value.NodeAffinity),
		PodAffinity:     workloadPodAffinityView(value.PodAffinity),
		PodAntiAffinity: workloadPodAntiAffinityView(value.PodAntiAffinity),
	}
}

func workloadPodAntiAffinityView(value *corev1.PodAntiAffinity) *WorkloadPodAffinity {
	if value == nil {
		return nil
	}
	return workloadPodAffinityView(&corev1.PodAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution:  value.RequiredDuringSchedulingIgnoredDuringExecution,
		PreferredDuringSchedulingIgnoredDuringExecution: value.PreferredDuringSchedulingIgnoredDuringExecution,
	})
}

func workloadNodeAffinityView(value *corev1.NodeAffinity) *WorkloadNodeAffinity {
	if value == nil {
		return nil
	}
	result := &WorkloadNodeAffinity{}
	if value.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		for _, term := range value.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			result.Required = append(result.Required, workloadNodeSelectorTermView(term))
		}
	}
	for _, term := range value.PreferredDuringSchedulingIgnoredDuringExecution {
		result.Preferred = append(result.Preferred, WorkloadPreferredNodeSelectorTerm{Weight: term.Weight, Preference: workloadNodeSelectorTermView(term.Preference)})
	}
	return result
}

func workloadNodeSelectorTermView(value corev1.NodeSelectorTerm) WorkloadNodeSelectorTerm {
	result := WorkloadNodeSelectorTerm{}
	for _, requirement := range value.MatchExpressions {
		result.MatchExpressions = append(result.MatchExpressions, WorkloadNodeSelectorRequirement{
			Key: requirement.Key, Operator: string(requirement.Operator), Values: append([]string{}, requirement.Values...),
		})
	}
	for _, requirement := range value.MatchFields {
		result.MatchFields = append(result.MatchFields, WorkloadNodeSelectorRequirement{
			Key: requirement.Key, Operator: string(requirement.Operator), Values: append([]string{}, requirement.Values...),
		})
	}
	return result
}

func workloadPodAffinityView(value *corev1.PodAffinity) *WorkloadPodAffinity {
	if value == nil {
		return nil
	}
	result := &WorkloadPodAffinity{}
	for _, term := range value.RequiredDuringSchedulingIgnoredDuringExecution {
		result.Required = append(result.Required, workloadPodAffinityTermView(term))
	}
	for _, term := range value.PreferredDuringSchedulingIgnoredDuringExecution {
		result.Preferred = append(result.Preferred, WorkloadWeightedPodAffinityTerm{Weight: term.Weight, PodTerm: workloadPodAffinityTermView(term.PodAffinityTerm)})
	}
	return result
}

func workloadPodAffinityTermView(value corev1.PodAffinityTerm) WorkloadPodAffinityTerm {
	return WorkloadPodAffinityTerm{
		LabelSelector:     workloadLabelSelectorView(value.LabelSelector),
		NamespaceSelector: workloadLabelSelectorView(value.NamespaceSelector),
		Namespaces:        append([]string(nil), value.Namespaces...),
		TopologyKey:       value.TopologyKey,
		MatchLabelKeys:    append([]string(nil), value.MatchLabelKeys...),
		MismatchLabelKeys: append([]string(nil), value.MismatchLabelKeys...),
	}
}

func workloadLabelSelectorView(value *metav1.LabelSelector) *WorkloadLabelSelector {
	if value == nil {
		return nil
	}
	result := &WorkloadLabelSelector{MatchLabels: cloneMap(value.MatchLabels)}
	for _, requirement := range value.MatchExpressions {
		result.MatchExpressions = append(result.MatchExpressions, WorkloadSelectorRequirement{
			Key: requirement.Key, Operator: string(requirement.Operator), Values: append([]string{}, requirement.Values...),
		})
	}
	return result
}

func workloadTopologySpreadView(values []corev1.TopologySpreadConstraint) []WorkloadTopologySpreadConstraint {
	result := make([]WorkloadTopologySpreadConstraint, 0, len(values))
	for _, value := range values {
		converted := WorkloadTopologySpreadConstraint{
			MaxSkew: value.MaxSkew, TopologyKey: value.TopologyKey,
			WhenUnsatisfiable: string(value.WhenUnsatisfiable), LabelSelector: workloadLabelSelectorView(value.LabelSelector),
			MinDomains: copyPointer(value.MinDomains), MatchLabelKeys: append([]string(nil), value.MatchLabelKeys...),
		}
		if value.NodeAffinityPolicy != nil {
			converted.NodeAffinityPolicy = string(*value.NodeAffinityPolicy)
		}
		if value.NodeTaintsPolicy != nil {
			converted.NodeTaintsPolicy = string(*value.NodeTaintsPolicy)
		}
		result = append(result, converted)
	}
	return result
}
