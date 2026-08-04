package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestCreatePolicyObjectCoversEveryPolicyKind(t *testing.T) {
	t.Parallel()

	quotaObject, err := createPolicyObject(CreatePolicyResourceInput{
		Resource: PolicyResourceQuotas, Namespace: "team-a", Name: "compute",
		Spec: PolicyCreateSpec{ResourceQuota: &ResourceQuotaSpecInput{
			Hard:   map[string]string{"requests.cpu": "10", "limits.memory": "20Gi", "count/pods": "50"},
			Scopes: []string{"NotBestEffort"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var quota corev1.ResourceQuota
	if runtime.DefaultUnstructuredConverter.FromUnstructured(quotaObject, &quota) != nil ||
		quota.Spec.Hard.Cpu() == nil || quota.Spec.Hard["requests.cpu"] != resource.MustParse("10") ||
		len(quota.Spec.Scopes) != 1 || quota.Spec.Scopes[0] != corev1.ResourceQuotaScopeNotBestEffort {
		t.Fatalf("unexpected ResourceQuota: %+v", quota)
	}

	limitObject, err := createPolicyObject(CreatePolicyResourceInput{
		Resource: PolicyLimitRanges, Namespace: "team-a", Name: "defaults",
		Spec: PolicyCreateSpec{LimitRange: &LimitRangeSpecInput{Items: []LimitRangeItem{{
			Type: "Container", Default: map[string]string{"cpu": "500m"}, DefaultRequest: map[string]string{"cpu": "100m"},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var limitRange corev1.LimitRange
	if runtime.DefaultUnstructuredConverter.FromUnstructured(limitObject, &limitRange) != nil ||
		len(limitRange.Spec.Limits) != 1 || limitRange.Spec.Limits[0].Type != corev1.LimitTypeContainer ||
		limitRange.Spec.Limits[0].Default.Cpu().String() != "500m" {
		t.Fatalf("unexpected LimitRange: %+v", limitRange)
	}

	networkObject, err := createPolicyObject(CreatePolicyResourceInput{
		Resource: PolicyNetworkPolicies, Namespace: "team-a", Name: "api-ingress",
		Spec: PolicyCreateSpec{NetworkPolicy: &NetworkPolicySpecInput{
			PodSelector: &WorkloadSelector{MatchLabels: map[string]string{"app": "api"}},
			PolicyTypes: []string{"Ingress"},
			Ingress: []NetworkPolicyRule{{
				Peers: []NetworkPolicyPeer{
					{PodSelector: &WorkloadSelector{MatchLabels: map[string]string{"app": "web"}}},
					{IPBlock: &NetworkPolicyIPBlock{CIDR: "10.0.0.0/8", Except: []string{"10.1.0.0/16"}}},
				},
				Ports: []NetworkPolicyPort{{Protocol: "TCP", Port: "8080"}},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var network networkingv1.NetworkPolicy
	if runtime.DefaultUnstructuredConverter.FromUnstructured(networkObject, &network) != nil ||
		len(network.Spec.Ingress) != 1 || len(network.Spec.Ingress[0].From) != 2 ||
		network.Spec.Ingress[0].From[1].IPBlock == nil || network.Spec.Ingress[0].Ports[0].Port.IntValue() != 8080 {
		t.Fatalf("unexpected NetworkPolicy: %+v", network)
	}

	budgetObject, err := createPolicyObject(CreatePolicyResourceInput{
		Resource: PolicyDisruptionBudgets, Namespace: "team-a", Name: "api",
		Spec: PolicyCreateSpec{DisruptionBudget: &DisruptionBudgetSpecInput{
			Selector: &WorkloadSelector{MatchLabels: map[string]string{"app": "api"}}, MinAvailable: "50%",
			UnhealthyPodEvictionPolicy: "AlwaysAllow",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var budget policyv1.PodDisruptionBudget
	if runtime.DefaultUnstructuredConverter.FromUnstructured(budgetObject, &budget) != nil ||
		budget.Spec.MinAvailable == nil || budget.Spec.MinAvailable.StrVal != "50%" ||
		budget.Spec.MaxUnavailable != nil || budget.Spec.UnhealthyPodEvictionPolicy == nil {
		t.Fatalf("unexpected PodDisruptionBudget: %+v", budget)
	}

	classObject, err := createPolicyObject(CreatePolicyResourceInput{
		Resource: PolicyPriorityClasses, Name: "training-high",
		Spec: PolicyCreateSpec{PriorityClass: &PriorityClassSpecInput{
			Value: 100000, Description: "训练作业", PreemptionPolicy: "Never",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var class schedulingv1.PriorityClass
	if runtime.DefaultUnstructuredConverter.FromUnstructured(classObject, &class) != nil ||
		class.Value != 100000 || class.GlobalDefault || class.PreemptionPolicy == nil ||
		*class.PreemptionPolicy != corev1.PreemptNever {
		t.Fatalf("unexpected PriorityClass: %+v", class)
	}
}

func TestCreatePolicyObjectRejectsMismatchedAndUnusableSpecs(t *testing.T) {
	t.Parallel()

	inputs := map[string]CreatePolicyResourceInput{
		"spec block for another kind": {
			Resource: PolicyResourceQuotas, Namespace: "team-a", Name: "compute",
			Spec: PolicyCreateSpec{LimitRange: &LimitRangeSpecInput{Items: []LimitRangeItem{{Type: "Container", Max: map[string]string{"cpu": "1"}}}}},
		},
		"two spec blocks at once": {
			Resource: PolicyResourceQuotas, Namespace: "team-a", Name: "compute",
			Spec: PolicyCreateSpec{
				ResourceQuota: &ResourceQuotaSpecInput{Hard: map[string]string{"requests.cpu": "1"}},
				LimitRange:    &LimitRangeSpecInput{Items: []LimitRangeItem{{Type: "Container", Max: map[string]string{"cpu": "1"}}}},
			},
		},
		"cluster kind in a namespace": {
			Resource: PolicyPriorityClasses, Namespace: "team-a", Name: "high",
			Spec: PolicyCreateSpec{PriorityClass: &PriorityClassSpecInput{Value: 100}},
		},
		"namespaced kind without a namespace": {
			Resource: PolicyLimitRanges, Name: "defaults",
			Spec: PolicyCreateSpec{LimitRange: &LimitRangeSpecInput{Items: []LimitRangeItem{{Type: "Container", Max: map[string]string{"cpu": "1"}}}}},
		},
		"quota with an unparsable quantity": {
			Resource: PolicyResourceQuotas, Namespace: "team-a", Name: "compute",
			Spec: PolicyCreateSpec{ResourceQuota: &ResourceQuotaSpecInput{Hard: map[string]string{"requests.cpu": "ten"}}},
		},
		"limit range item that constrains nothing": {
			Resource: PolicyLimitRanges, Namespace: "team-a", Name: "defaults",
			Spec: PolicyCreateSpec{LimitRange: &LimitRangeSpecInput{Items: []LimitRangeItem{{Type: "Container"}}}},
		},
		"rules under an undeclared policy type": {
			Resource: PolicyNetworkPolicies, Namespace: "team-a", Name: "api",
			Spec: PolicyCreateSpec{NetworkPolicy: &NetworkPolicySpecInput{
				PolicyTypes: []string{"Ingress"},
				Egress:      []NetworkPolicyRule{{Ports: []NetworkPolicyPort{{Port: "53", Protocol: "UDP"}}}},
			}},
		},
		"peer mixing ipBlock with a selector": {
			Resource: PolicyNetworkPolicies, Namespace: "team-a", Name: "api",
			Spec: PolicyCreateSpec{NetworkPolicy: &NetworkPolicySpecInput{
				PolicyTypes: []string{"Ingress"},
				Ingress: []NetworkPolicyRule{{Peers: []NetworkPolicyPeer{{
					IPBlock:     &NetworkPolicyIPBlock{CIDR: "10.0.0.0/8"},
					PodSelector: &WorkloadSelector{MatchLabels: map[string]string{"app": "web"}},
				}}}},
			}},
		},
		"except outside its own block": {
			Resource: PolicyNetworkPolicies, Namespace: "team-a", Name: "api",
			Spec: PolicyCreateSpec{NetworkPolicy: &NetworkPolicySpecInput{
				PolicyTypes: []string{"Ingress"},
				Ingress: []NetworkPolicyRule{{Peers: []NetworkPolicyPeer{{
					IPBlock: &NetworkPolicyIPBlock{CIDR: "10.0.0.0/8", Except: []string{"192.168.0.0/16"}},
				}}}},
			}},
		},
		"budget setting both bounds": {
			Resource: PolicyDisruptionBudgets, Namespace: "team-a", Name: "api",
			Spec: PolicyCreateSpec{DisruptionBudget: &DisruptionBudgetSpecInput{
				Selector:     &WorkloadSelector{MatchLabels: map[string]string{"app": "api"}},
				MinAvailable: "1", MaxUnavailable: "1",
			}},
		},
		"budget without a selector": {
			Resource: PolicyDisruptionBudgets, Namespace: "team-a", Name: "api",
			Spec: PolicyCreateSpec{DisruptionBudget: &DisruptionBudgetSpecInput{MinAvailable: "1"}},
		},
		"priority above the user-definable ceiling": {
			Resource: PolicyPriorityClasses, Name: "system-ish",
			Spec: PolicyCreateSpec{PriorityClass: &PriorityClassSpecInput{Value: 2_000_000_000}},
		},
	}
	for name, input := range inputs {
		if _, err := createPolicyObject(input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestPolicyResourceDetailsReportEnforcedStateAndEmptyCollections(t *testing.T) {
	t.Parallel()

	quota := &corev1.ResourceQuota{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ResourceQuota"},
		ObjectMeta: metav1.ObjectMeta{Name: "compute", Namespace: "team-a", UID: types.UID("quota-uid"), ResourceVersion: "4"},
		Spec:       corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{"requests.cpu": resource.MustParse("20")}},
		Status: corev1.ResourceQuotaStatus{
			Hard: corev1.ResourceList{"requests.cpu": resource.MustParse("10")},
			Used: corev1.ResourceList{"requests.cpu": resource.MustParse("3")},
		},
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(quota)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := policyResourceDetail(object, PolicyResourceQuotas, "team-a", "compute")
	if err != nil {
		t.Fatal(err)
	}
	// The status is what the cluster is enforcing; the spec may have moved on.
	// The scope selector is part of the summary, so a list carries it too.
	if detail.ResourceQuota == nil || detail.ResourceQuota.Hard["requests.cpu"] != "10" ||
		detail.ResourceQuota.Used["requests.cpu"] != "3" || detail.ResourceQuota.ScopeSelector == nil {
		t.Fatalf("unexpected ResourceQuota detail: %+v", detail)
	}

	budget := &policyv1.PodDisruptionBudget{
		TypeMeta:   metav1.TypeMeta{APIVersion: "policy/v1", Kind: "PodDisruptionBudget"},
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team-a", UID: types.UID("pdb-uid"), ResourceVersion: "7"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: ptrIntOrString(intstr.FromString("25%")),
			Selector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
		},
		Status: policyv1.PodDisruptionBudgetStatus{CurrentHealthy: 3, DesiredHealthy: 2, DisruptionsAllowed: 1, ExpectedPods: 3},
	}
	budgetObject, err := runtime.DefaultUnstructuredConverter.ToUnstructured(budget)
	if err != nil {
		t.Fatal(err)
	}
	budgetDetail, err := policyResourceDetail(budgetObject, PolicyDisruptionBudgets, "team-a", "api")
	if err != nil {
		t.Fatal(err)
	}
	if budgetDetail.DisruptionBudget == nil || budgetDetail.DisruptionBudget.MaxUnavailable != "25%" ||
		budgetDetail.DisruptionBudget.MinAvailable != "" || budgetDetail.DisruptionBudget.DisruptionsAllowed != 1 {
		t.Fatalf("unexpected PodDisruptionBudget detail: %+v", budgetDetail)
	}
	body, err := json.Marshal(budgetDetail)
	if err != nil {
		t.Fatal(err)
	}
	for _, unexpected := range []string{`"labels":null`, `"annotations":null`, `"conditions":null`} {
		if strings.Contains(string(body), unexpected) {
			t.Fatalf("policy detail contains %s: %s", unexpected, body)
		}
	}
}

func TestPriorityClassUpdateKeepsValueAndRewritesOnlyMutableFields(t *testing.T) {
	t.Parallel()

	class := &schedulingv1.PriorityClass{
		TypeMeta:      metav1.TypeMeta{APIVersion: "scheduling.k8s.io/v1", Kind: "PriorityClass"},
		ObjectMeta:    metav1.ObjectMeta{Name: "training-high", UID: types.UID("class-uid"), ResourceVersion: "3"},
		Value:         100000,
		GlobalDefault: false,
		Description:   "旧描述",
	}
	existing, err := runtime.DefaultUnstructuredConverter.ToUnstructured(class)
	if err != nil {
		t.Fatal(err)
	}
	globalDefault := true
	updated, err := updatePolicyObject(existing, UpdatePolicyResourceInput{
		Resource: PolicyPriorityClasses, Name: "training-high",
		Spec: PolicyUpdateSpec{PriorityClass: &PriorityClassUpdateSpec{Description: "新描述", GlobalDefault: &globalDefault}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var result schedulingv1.PriorityClass
	if runtime.DefaultUnstructuredConverter.FromUnstructured(updated, &result) != nil ||
		result.Value != 100000 || result.Description != "新描述" || !result.GlobalDefault {
		t.Fatalf("unexpected updated PriorityClass: %+v", result)
	}
}

func TestDisruptionBudgetUpdateKeepsTheSelectorItProtects(t *testing.T) {
	t.Parallel()

	budget := &policyv1.PodDisruptionBudget{
		TypeMeta:   metav1.TypeMeta{APIVersion: "policy/v1", Kind: "PodDisruptionBudget"},
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team-a", UID: types.UID("pdb-uid"), ResourceVersion: "7"},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: ptrIntOrString(intstr.FromInt32(1)),
			Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
		},
	}
	existing, err := runtime.DefaultUnstructuredConverter.ToUnstructured(budget)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := updatePolicyObject(existing, UpdatePolicyResourceInput{
		Resource: PolicyDisruptionBudgets, Namespace: "team-a", Name: "api",
		Spec: PolicyUpdateSpec{DisruptionBudget: &DisruptionBudgetSpecInput{
			// A selector offered here must not take effect.
			Selector: &WorkloadSelector{MatchLabels: map[string]string{"app": "other"}}, MaxUnavailable: "2",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var result policyv1.PodDisruptionBudget
	if runtime.DefaultUnstructuredConverter.FromUnstructured(updated, &result) != nil ||
		result.Spec.Selector.MatchLabels["app"] != "api" || result.Spec.MinAvailable != nil ||
		result.Spec.MaxUnavailable == nil || result.Spec.MaxUnavailable.IntValue() != 2 {
		t.Fatalf("unexpected updated PodDisruptionBudget: %+v", result)
	}
}

func TestUpdatePolicyResourceRejectsStaleIdentityBeforeMutation(t *testing.T) {
	t.Parallel()

	quota := &corev1.ResourceQuota{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ResourceQuota"},
		ObjectMeta: metav1.ObjectMeta{Name: "compute", Namespace: "team-a", UID: types.UID("current-uid"), ResourceVersion: "9"},
	}
	requester := &fakeResourceRequester{
		handle: func(_ context.Context, _ string, request *agentv1.ResourceRequest, responseBody io.Writer) (*agentv1.ResourceResponse, error) {
			if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_GET {
				t.Fatalf("unexpected request: %+v", request)
			}
			return writeKubernetesObject(t, responseBody, quota), nil
		},
		mutate: func(context.Context, string, *agentv1.ResourceRequest, io.Reader, io.Writer, string) (*agentv1.ResourceResponse, error) {
			t.Fatal("stale update reached mutation transport")
			return nil, nil
		},
	}
	_, err := NewService(requester).UpdatePolicyResource(context.Background(), UpdatePolicyResourceInput{
		ClusterID: testClusterID, Namespace: "team-a", Resource: PolicyResourceQuotas, Name: "compute",
		UID: "stale-uid", ResourceVersion: "9",
		Spec:    PolicyUpdateSpec{ResourceQuota: &ResourceQuotaSpecInput{Hard: map[string]string{"requests.cpu": "10"}}},
		Confirm: true, IdempotencyKey: "policy-update-0001",
	})
	if !errors.Is(err, ErrUpstreamConflict) {
		t.Fatalf("error = %v, want ErrUpstreamConflict", err)
	}
}

func ptrIntOrString(value intstr.IntOrString) *intstr.IntOrString {
	return &value
}
