package agentinstall

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/togettoyou/zke/pkg/server/enrollment"
	"github.com/togettoyou/zke/pkg/server/store"
)

func TestRenderManifestCreatesBootstrapResourcesWithoutIdentitySecretOrPV(t *testing.T) {
	t.Parallel()

	manifest, err := renderManifest(
		manifestConfig{
			PublicHTTPURL:     "https://zke.example.com",
			PublicQUICAddress: "zke.example.com:8443",
			Workload: store.WorkloadSettings{
				Image: "registry.example.com/zke-agent:test", ImagePullPolicy: "IfNotPresent",
				CPURequest: "50m", MemoryLimit: "512Mi",
			},
			Namespace:                    "zke-system",
			ListenerCACertificatePEM:     []byte("listener-ca"),
			RegistrationCACertificatePEM: []byte("registration-ca"),
		},
		enrollment.ManifestEnrollment{
			ID:          "00000000-0000-0000-0000-000000000001",
			ProjectID:   "10000000-0000-0000-0000-000000000001",
			ClusterName: "test-cluster",
			ExpiresAt:   time.Now().Add(time.Minute),
		},
		"temporary-token",
	)
	if err != nil {
		t.Fatal(err)
	}
	output := string(manifest)
	for _, required := range []string{
		"kind: Namespace",
		"name: zke-agent-enrollment",
		"name: zke-agent-trust",
		"name: zke-agent-config",
		"kind: ServiceAccount",
		"kind: Role",
		"kind: RoleBinding",
		"kind: ClusterRole",
		"kind: ClusterRoleBinding",
		"kind: Deployment",
		"server_url: https://zke.example.com",
		"server_address: zke.example.com:8443",
		"agent-listener-ca.crt",
		"registration-ca.crt",
		"- zke-agent-enrollment",
		"- zke-agent-trust",
		"- nodes",
		"- namespaces",
		"- pods",
		"- deployments",
		"- statefulsets",
		"- daemonsets",
		"- jobs",
		"- cronjobs",
		"- apps",
		"- batch",
		"- get",
		"- list",
		"- create",
		"- update",
		"- delete",
		"- patch",
	} {
		if !strings.Contains(output, required) {
			t.Errorf("manifest is missing %q", required)
		}
	}
	if count := strings.Count(output, "kind: Secret\n"); count != 2 {
		t.Fatalf("manifest Secret document count = %d, want enrollment and trust only", count)
	}
	if strings.Contains(output, "persistentVolumeClaim") {
		t.Fatal("outbound-only Agent manifest contains a PVC")
	}
	// The one Service is the in-Cluster metrics ingest endpoint: it is a
	// ClusterIP for a collector running beside the Agent, not an inbound path
	// from outside the Cluster.
	if count := strings.Count(output, "kind: Service\n"); count != 1 {
		t.Fatalf("manifest Service document count = %d, want the ingest endpoint only", count)
	}
	if strings.Contains(output, "type: NodePort") ||
		strings.Contains(output, "type: LoadBalancer") ||
		strings.Contains(output, "kind: Ingress\n") {
		t.Fatal("Agent manifest exposes an inbound path from outside the Cluster")
	}
	if strings.Contains(output, "optional: true") {
		t.Fatal("retained Enrollment Secret was rendered as optional")
	}
	if strings.Contains(output, "token_file:") {
		t.Fatal("manifest configures a local Enrollment Token file")
	}
	if strings.Contains(output, "/var/run/secrets/zke-") {
		t.Fatal("manifest mounts Agent credentials as local files")
	}
	// The budget is what the enrollment snapshot froze, not a constant in this
	// renderer: the operator sets it in platform settings and applies the file
	// in their own Cluster, so a value that never reached the container would
	// be a setting that silently does nothing.
	for _, required := range []string{"cpu: 50m", "memory: 512Mi"} {
		if !strings.Contains(output, required) {
			t.Errorf("Agent container is missing resource entry %q", required)
		}
	}
	// The two the snapshot left empty stay off the container: Kubernetes has no
	// other spelling for "no limit".
	if strings.Contains(output, "cpu: \"0\"") || strings.Contains(output, "memory: \"0\"") {
		t.Error("an unset quantity was rendered as zero rather than left off")
	}
}

// A Deployment carrying no budget at all is what the Agent has always installed
// with, and it must stay renderable: an empty resources block is the operator
// deferring to their Namespace's LimitRange, not a value the Server fills in.
func TestRenderManifestOmitsAnEmptyAgentBudget(t *testing.T) {
	t.Parallel()

	manifest, err := renderManifest(
		manifestConfig{
			PublicHTTPURL:     "https://zke.example.com",
			PublicQUICAddress: "zke.example.com:8443",
			Workload: store.WorkloadSettings{
				Image: "registry.example.com/zke-agent:test", ImagePullPolicy: "IfNotPresent",
			},
			Namespace:                "zke-system",
			ListenerCACertificatePEM: []byte("listener-ca"),
		},
		enrollment.ManifestEnrollment{
			ID:          "00000000-0000-0000-0000-000000000001",
			ProjectID:   "10000000-0000-0000-0000-000000000001",
			ClusterName: "test-cluster",
			ExpiresAt:   time.Now().Add(time.Minute),
		},
		"temporary-token",
	)
	if err != nil {
		t.Fatal(err)
	}
	// Asserted on the quantity keys rather than on "resources:", which every
	// RBAC rule in this manifest also uses for something unrelated.
	output := string(manifest)
	if strings.Contains(output, "cpu:") || strings.Contains(output, "memory:") {
		t.Error("an empty budget put quantities on the Agent container")
	}
}

// A quantity Kubernetes would refuse must fail here, where the message names
// the platform setting, rather than in the operator's Cluster hours later.
func TestRenderManifestRejectsAnUnusableAgentQuantity(t *testing.T) {
	t.Parallel()

	_, err := renderManifest(
		manifestConfig{
			PublicHTTPURL:     "https://zke.example.com",
			PublicQUICAddress: "zke.example.com:8443",
			Workload: store.WorkloadSettings{
				Image: "registry.example.com/zke-agent:test", ImagePullPolicy: "IfNotPresent",
				CPURequest: "half a core",
			},
			Namespace:                "zke-system",
			ListenerCACertificatePEM: []byte("listener-ca"),
		},
		enrollment.ManifestEnrollment{
			ID:          "00000000-0000-0000-0000-000000000001",
			ProjectID:   "10000000-0000-0000-0000-000000000001",
			ClusterName: "test-cluster",
			ExpiresAt:   time.Now().Add(time.Minute),
		},
		"temporary-token",
	)
	if err == nil {
		t.Fatal("renderManifest() accepted a quantity Kubernetes would refuse")
	}
}

func TestRenderManifestGrantsOnlyEnabledClusterResources(t *testing.T) {
	t.Parallel()

	manifest, err := renderManifest(
		manifestConfig{
			PublicHTTPURL:     "https://zke.example.com",
			PublicQUICAddress: "zke.example.com:8443",
			Workload: store.WorkloadSettings{
				Image: "registry.example.com/zke-agent:test", ImagePullPolicy: "IfNotPresent",
			},
			Namespace:                "zke-system",
			ListenerCACertificatePEM: []byte("listener-ca"),
		},
		enrollment.ManifestEnrollment{
			ID:          "00000000-0000-0000-0000-000000000001",
			ProjectID:   "10000000-0000-0000-0000-000000000001",
			ClusterName: "test-cluster",
			ExpiresAt:   time.Now().Add(time.Minute),
		},
		"temporary-token",
	)
	if err != nil {
		t.Fatal(err)
	}

	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	// Two ClusterRoles: the one the Agent is bound to, which carries no rules
	// and aggregates, and the base one that carries what ZKE itself grants.
	var clusterRole *rbacv1.ClusterRole
	var aggregateClusterRole *rbacv1.ClusterRole
	for {
		var object unstructured.Unstructured
		if err := decoder.Decode(&object); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if object.GetKind() != "ClusterRole" {
			continue
		}
		decoded := &rbacv1.ClusterRole{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(
			object.Object,
			decoded,
		); err != nil {
			t.Fatal(err)
		}
		switch object.GetName() {
		case BaseClusterRoleName:
			clusterRole = decoded
		case ServiceAccountName:
			aggregateClusterRole = decoded
		}
	}
	if clusterRole == nil {
		t.Fatal("manifest has no ZKE Agent base ClusterRole")
	}
	if aggregateClusterRole == nil {
		t.Fatal("manifest has no ZKE Agent aggregate ClusterRole")
	}
	// The bound role must aggregate and must carry nothing itself: rules
	// written on it would be overwritten by the aggregation controller, so a
	// permission put there would disappear without anyone noticing.
	if len(aggregateClusterRole.Rules) != 0 {
		t.Errorf("aggregate ClusterRole carries %d own rules", len(aggregateClusterRole.Rules))
	}
	if aggregateClusterRole.AggregationRule == nil ||
		len(aggregateClusterRole.AggregationRule.ClusterRoleSelectors) != 1 ||
		aggregateClusterRole.AggregationRule.ClusterRoleSelectors[0].MatchLabels[AggregationLabel] != "true" {
		t.Fatal("aggregate ClusterRole does not select the aggregation label")
	}
	if clusterRole.Labels[AggregationLabel] != "true" {
		t.Error("base ClusterRole is not labelled for aggregation")
	}

	workloadVerbs := []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	assertPolicyRule(t, clusterRole.Rules, "apps", []string{
		"deployments", "statefulsets", "daemonsets",
	}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "apps", []string{
		"replicasets", "controllerrevisions",
	}, []string{"get", "list", "watch"})
	assertPolicyRule(t, clusterRole.Rules, "batch", []string{
		"jobs", "cronjobs",
	}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "", []string{"services"}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "", []string{"configmaps"}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "", []string{
		"persistentvolumes", "persistentvolumeclaims",
	}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "storage.k8s.io", []string{"storageclasses"}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "autoscaling", []string{"horizontalpodautoscalers"}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "autoscaling.k8s.io", []string{"verticalpodautoscalers"}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "keda.sh", []string{"scaledobjects"}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "", []string{"serviceaccounts"}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "rbac.authorization.k8s.io", []string{
		"roles", "clusterroles", "rolebindings", "clusterrolebindings",
	}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "", []string{"resourcequotas", "limitranges"}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "policy", []string{"poddisruptionbudgets"}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "scheduling.k8s.io", []string{"priorityclasses"}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "apiextensions.k8s.io", []string{
		"customresourcedefinitions",
	}, []string{"get", "list", "watch"})
	assertPolicyRule(t, clusterRole.Rules, "metrics.k8s.io", []string{
		"nodes", "pods",
	}, []string{"get", "list"})
	assertPolicyRule(t, clusterRole.Rules, "networking.k8s.io", []string{"ingresses", "networkpolicies"}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "gateway.networking.k8s.io",
		[]string{"gateways", "httproutes", "grpcroutes", "tlsroutes", "tcproutes", "udproutes"}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "", []string{"nodes"}, []string{
		"get", "list", "watch", "update", "patch",
	})
	assertPolicyRule(t, clusterRole.Rules, "", []string{"namespaces"}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "", []string{"pods"}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "", []string{"pods/log"}, []string{"get"})
	assertPolicyRule(t, clusterRole.Rules, "", []string{"pods/exec"}, []string{"get", "create"})
	assertPolicyRule(t, clusterRole.Rules, "", []string{"pods/portforward"}, []string{"get", "create"})
	assertPolicyRule(t, clusterRole.Rules, "", []string{"pods/eviction"}, []string{"create"})
	assertPolicyRule(t, clusterRole.Rules, "", []string{"events"}, []string{"get", "list", "watch"})
	// Secrets include watch so the Agent may delegate the terminal's read-only
	// watch without `escalate`; no `deletecollection` or Subresource is granted.
	// The Agent still refuses a Secret request that does not come from its typed
	// API and any request touching its own namespace.
	assertPolicyRule(t, clusterRole.Rules, "", []string{"secrets"}, workloadVerbs)
	for _, rule := range clusterRole.Rules {
		for _, resource := range rule.Resources {
			if resource == "*" ||
				(strings.Contains(resource, "/") &&
					resource != "pods/log" &&
					resource != "pods/exec" &&
					resource != "pods/portforward" &&
					resource != "pods/eviction" &&
					// Held only so the Agent may grant it to the metrics
					// collector: Kubernetes refuses to create a ClusterRole
					// carrying a permission its creator does not have.
					resource != "nodes/metrics") {
				t.Fatalf("ClusterRole grants wildcard or Subresource access: %+v", rule)
			}
		}
	}
}

func assertPolicyRule(
	t *testing.T,
	rules []rbacv1.PolicyRule,
	apiGroup string,
	resources []string,
	verbs []string,
) {
	t.Helper()
	for _, rule := range rules {
		if slices.Equal(rule.APIGroups, []string{apiGroup}) &&
			slices.Equal(rule.Resources, resources) &&
			slices.Equal(rule.Verbs, verbs) {
			return
		}
	}
	t.Fatalf(
		"ClusterRole has no exact rule group=%q resources=%v verbs=%v: %+v",
		apiGroup,
		resources,
		verbs,
		rules,
	)
}
