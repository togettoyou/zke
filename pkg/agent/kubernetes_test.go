package agent

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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

func TestLoadEnrollmentTokenAndTrustFromSecrets(t *testing.T) {
	t.Parallel()

	token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	client := fake.NewClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "zke-system",
				Name:      enrollmentSecretName,
			},
			Data: map[string][]byte{enrollmentTokenKey: []byte(token)},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "zke-system",
				Name:      trustSecretName,
			},
			Data: map[string][]byte{
				listenerCAKey:     []byte("listener-ca"),
				registrationCAKey: []byte("registration-ca"),
			},
		},
	)
	loadedToken, err := loadEnrollmentToken(
		context.Background(),
		client,
		"zke-system",
	)
	if err != nil {
		t.Fatal(err)
	}
	if loadedToken != token {
		t.Fatalf("loaded token = %q, want Secret token", loadedToken)
	}
	cfg := Config{IdentityNamespace: "zke-system"}
	if err := loadTrust(context.Background(), client, &cfg); err != nil {
		t.Fatal(err)
	}
	if string(cfg.Connection.CACertificatePEM) != "listener-ca" ||
		string(cfg.Registration.CACertificatePEM) != "registration-ca" {
		t.Fatalf("unexpected Secret trust configuration: %+v", cfg)
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
