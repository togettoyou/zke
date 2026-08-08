package kubernetesresource

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestWorkloadAdvancedSchedulingViewRoundTripsEveryModeledField(t *testing.T) {
	t.Parallel()

	affinity := &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key: "node.kubernetes.io/instance-type", Operator: corev1.NodeSelectorOpIn, Values: []string{"gpu"},
					}},
					MatchFields: []corev1.NodeSelectorRequirement{{
						Key: "metadata.name", Operator: corev1.NodeSelectorOpNotIn, Values: []string{"maintenance"},
					}},
				}},
			},
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{
				Weight: 25,
				Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key: "disk", Operator: corev1.NodeSelectorOpExists,
				}}},
			}},
		},
		PodAffinity: &corev1.PodAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
				NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key: "team", Operator: metav1.LabelSelectorOpIn, Values: []string{"platform"},
				}}},
				TopologyKey:    "topology.kubernetes.io/zone",
				MatchLabelKeys: []string{"pod-template-hash"},
			}},
		},
		PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
				Weight: 90,
				PodAffinityTerm: corev1.PodAffinityTerm{
					LabelSelector: &metav1.LabelSelector{}, Namespaces: []string{"default"},
					TopologyKey: "kubernetes.io/hostname", MismatchLabelKeys: []string{"tenant"},
				},
			}},
		},
	}
	if roundTrip := workloadAffinitySpec(workloadAffinityView(affinity)); !apiequality.Semantic.DeepEqual(roundTrip, affinity) {
		t.Fatalf("affinity round trip changed fields:\n got: %#v\nwant: %#v", roundTrip, affinity)
	}

	honor := corev1.NodeInclusionPolicyHonor
	ignore := corev1.NodeInclusionPolicyIgnore
	minDomains := int32(2)
	topology := []corev1.TopologySpreadConstraint{{
		MaxSkew: 1, TopologyKey: "topology.kubernetes.io/zone",
		WhenUnsatisfiable: corev1.DoNotSchedule,
		LabelSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"api"},
		}}},
		MinDomains: &minDomains, NodeAffinityPolicy: &honor, NodeTaintsPolicy: &ignore,
		MatchLabelKeys: []string{"pod-template-hash"},
	}}
	if roundTrip := workloadTopologySpreadSpec(workloadTopologySpreadView(topology)); !apiequality.Semantic.DeepEqual(roundTrip, topology) {
		t.Fatalf("topology spread round trip changed fields:\n got: %#v\nwant: %#v", roundTrip, topology)
	}
}
