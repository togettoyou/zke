package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadKubernetesConfigUsesExplicitFile(t *testing.T) {
	t.Parallel()

	path := writeTestKubeconfig(t, "https://explicit.example.invalid")
	config, err := loadKubernetesConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "https://explicit.example.invalid" {
		t.Fatalf("Kubernetes host = %q, want explicit kubeconfig host", config.Host)
	}
}

func TestLoadKubernetesConfigFallsBackToDefaultFileOutsideCluster(t *testing.T) {
	path := writeTestKubeconfig(t, "https://default.example.invalid")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	t.Setenv("KUBECONFIG", path)

	config, err := loadKubernetesConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "https://default.example.invalid" {
		t.Fatalf("Kubernetes host = %q, want default kubeconfig host", config.Host)
	}
}

func writeTestKubeconfig(t *testing.T, server string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	content := []byte(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: ` + server + `
    insecure-skip-tls-verify: true
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
users:
- name: test
  user:
    token: test-token
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
