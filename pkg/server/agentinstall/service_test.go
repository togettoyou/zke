package agentinstall

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

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
		"- get",
		"- list",
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
