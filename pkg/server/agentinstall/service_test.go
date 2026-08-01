package agentinstall

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/togettoyou/zke/pkg/server/enrollment"
)

func TestRenderManifestCreatesBootstrapResourcesWithoutIdentitySecretOrPV(t *testing.T) {
	t.Parallel()

	manifest, err := renderManifest(
		Config{
			PublicHTTPURL:                "https://zke.example.com",
			PublicQUICAddress:            "zke.example.com:8443",
			Image:                        "registry.example.com/zke-agent:test",
			Namespace:                    "zke-system",
			ImagePullPolicy:              corev1.PullIfNotPresent,
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
	if strings.Contains(output, "persistentVolumeClaim") ||
		strings.Contains(output, "kind: Service\n") {
		t.Fatal("outbound-only Agent manifest contains a Service or PVC")
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
}

func TestShellQuoteProtectsInstallCommandValues(t *testing.T) {
	t.Parallel()

	if quoted := shellQuote("a'b"); quoted != `'a'"'"'b'` {
		t.Fatalf("shellQuote() = %q", quoted)
	}
}

func TestRenderManifestGrantsOnlyEnabledWorkloadResources(t *testing.T) {
	t.Parallel()

	manifest, err := renderManifest(
		Config{
			PublicHTTPURL:            "https://zke.example.com",
			PublicQUICAddress:        "zke.example.com:8443",
			Image:                    "registry.example.com/zke-agent:test",
			Namespace:                "zke-system",
			ImagePullPolicy:          corev1.PullIfNotPresent,
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
	var clusterRole *rbacv1.ClusterRole
	for {
		var object unstructured.Unstructured
		if err := decoder.Decode(&object); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if object.GetKind() != "ClusterRole" ||
			object.GetName() != ServiceAccountName {
			continue
		}
		clusterRole = &rbacv1.ClusterRole{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(
			object.Object,
			clusterRole,
		); err != nil {
			t.Fatal(err)
		}
	}
	if clusterRole == nil {
		t.Fatal("manifest has no ZKE Agent ClusterRole")
	}

	workloadVerbs := []string{"get", "list", "create", "update", "patch", "delete"}
	assertPolicyRule(t, clusterRole.Rules, "apps", []string{
		"deployments", "statefulsets", "daemonsets",
	}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "batch", []string{
		"jobs", "cronjobs",
	}, workloadVerbs)
	assertPolicyRule(t, clusterRole.Rules, "", []string{"nodes"}, []string{
		"get", "list", "update", "patch",
	})
	assertPolicyRule(t, clusterRole.Rules, "", []string{"namespaces"}, []string{
		"get", "list", "create", "update", "delete",
	})
	assertPolicyRule(t, clusterRole.Rules, "", []string{"pods"}, []string{
		"get", "list", "update", "delete",
	})
	assertPolicyRule(t, clusterRole.Rules, "", []string{"pods/log"}, []string{"get"})
	assertPolicyRule(t, clusterRole.Rules, "", []string{"events"}, []string{"get", "list", "watch"})
	for _, rule := range clusterRole.Rules {
		for _, resource := range rule.Resources {
			if resource == "*" ||
				(strings.Contains(resource, "/") && resource != "pods/log") {
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
