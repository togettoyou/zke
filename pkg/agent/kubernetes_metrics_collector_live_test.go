package agent

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/observability"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// TestLiveMetricsCollectorInstallAndUninstall is opt-in because it creates and
// deletes real objects in the currently selected cluster.
//
// A fake clientset accepts any object; only an API server decides whether the
// Deployment, the RBAC and the mounts this Agent writes are actually valid. It
// runs in its own Namespace so a failure cannot touch a real Agent's.
//
//	ZKE_LIVE_KUBERNETES_E2E=1 go test ./pkg/agent -run LiveMetricsCollector
func TestLiveMetricsCollectorInstallAndUninstall(t *testing.T) {
	if os.Getenv("ZKE_LIVE_KUBERNETES_E2E") != "1" {
		t.Skip("set ZKE_LIVE_KUBERNETES_E2E=1 to use the current kubeconfig")
	}
	config, err := loadKubernetesConfig("")
	if err != nil {
		t.Fatal(err)
	}
	config.Timeout = 30 * time.Second
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// The collector's ClusterRole and ClusterRoleBinding are cluster-scoped and
	// their names are fixed by the product, so this test cannot give itself
	// private ones. If a real collector is already installed here, running
	// would delete its authorization on the way out — skip instead.
	for _, name := range []string{observability.CollectorName, observability.KubeStateName} {
		if _, err := client.RbacV1().ClusterRoles().Get(
			ctx, name, metav1.GetOptions{},
		); err == nil {
			t.Skip("a metrics collector is already installed in this cluster")
		} else if !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
	}

	namespace := "zke-collector-e2e-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	if _, err := client.CoreV1().Namespaces().Create(
		ctx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		metav1.CreateOptions{},
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancelCleanup := context.WithTimeout(context.Background(), time.Minute)
		defer cancelCleanup()
		_ = client.CoreV1().Namespaces().Delete(cleanup, namespace, metav1.DeleteOptions{})
		// Cluster-scoped objects do not go with the Namespace.
		for _, name := range []string{observability.CollectorName, observability.KubeStateName} {
			_ = client.RbacV1().ClusterRoleBindings().Delete(
				cleanup, name, metav1.DeleteOptions{})
			_ = client.RbacV1().ClusterRoles().Delete(
				cleanup, name, metav1.DeleteOptions{})
		}
	})

	// The test runs outside the Cluster, so it advertises an address the way a
	// developer's Agent does. Whether that address is reachable is not what
	// this test is about; whether the API server accepts what gets written is.
	handler := newKubernetesMetricsCollectorHandler(client, collectorPlacement{
		Namespace:     namespace,
		AdvertisedURL: "http://host.docker.internal:8429",
	})
	response, err := handler(ctx, &agentv1.MetricsCollectorRequest{
		Action:             agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_INSTALL,
		Image:              "victoriametrics/vmagent:v1.149.0",
		ImagePullPolicy:    "IfNotPresent",
		ScrapeInterval:     "30s",
		BufferSize:         "512Mi",
		KubeletMetricsPort: 10250,
		KubeStateMetrics: &agentv1.MetricsCollectorComponent{
			Image:           "registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.1",
			ImagePullPolicy: "IfNotPresent",
			CpuRequest:      "20m",
			MemoryRequest:   "128Mi",
		},
		NodeExporter: &agentv1.MetricsCollectorComponent{
			Image:           "quay.io/prometheus/node-exporter:v1.12.1",
			ImagePullPolicy: "IfNotPresent",
			CpuRequest:      "10m",
			MemoryRequest:   "32Mi",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("install rejected by the cluster: %+v", response)
	}
	if !response.GetState().GetInstalled() || !response.GetState().GetCredentialReady() {
		t.Fatalf("install state = %+v", response.GetState())
	}
	// The object exporter has to be accepted too. Its ClusterRole is the part a
	// fake client cannot check: Kubernetes refuses to create a ClusterRole
	// granting a permission its creator does not itself hold, so this is where a
	// missing Agent permission shows up. The node exporter is allowed to be
	// refused — that is a Pod Security decision belonging to the Cluster — so it
	// is reported rather than asserted.
	for _, component := range response.GetState().GetComponents() {
		if component.GetComponent() == observability.ComponentNodeExporter &&
			!component.GetInstalled() {
			t.Logf(
				"node-exporter was refused by this cluster (%s); the rest of the install stands",
				component.GetUnavailableReason(),
			)
			continue
		}
		if !component.GetInstalled() {
			t.Fatalf("%s was not installed: %+v", component.GetComponent(), component)
		}
	}

	// The Deployment has to be accepted and become available. A spec the API
	// server takes but the scheduler or kubelet refuses would otherwise look
	// like a successful install.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		deployment, err := client.AppsV1().Deployments(namespace).Get(
			ctx, observability.CollectorName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if deployment.Status.ReadyReplicas >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"collector never became ready: %d/%d, conditions %+v",
				deployment.Status.ReadyReplicas,
				deployment.Status.Replicas,
				deployment.Status.Conditions,
			)
		}
		time.Sleep(3 * time.Second)
	}

	removal, err := handler(ctx, &agentv1.MetricsCollectorRequest{
		Action: agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_UNINSTALL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if removal.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		removal.GetState().GetInstalled() {
		t.Fatalf("uninstall response = %+v", removal)
	}
	if _, err := client.CoreV1().Secrets(namespace).Get(
		ctx, observability.IngestSecretName, metav1.GetOptions{},
	); !apierrors.IsNotFound(err) {
		t.Fatalf("ingest credential survived uninstall: %v", err)
	}
}
