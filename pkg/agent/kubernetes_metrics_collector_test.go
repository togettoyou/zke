package agent

import (
	"context"
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/observability"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

const collectorNamespace = "zke-system"

func installRequest() *agentv1.MetricsCollectorRequest {
	return &agentv1.MetricsCollectorRequest{
		Action:             agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_INSTALL,
		Image:              "victoriametrics/vmagent:v1.149.0",
		ImagePullPolicy:    "IfNotPresent",
		ScrapeInterval:     "30s",
		BufferSize:         "1Gi",
		KubeletMetricsPort: 10250,
	}
}

func collectorHandler(client kubernetes.Interface) func(
	*agentv1.MetricsCollectorRequest,
) (*agentv1.MetricsCollectorResponse, error) {
	handler, _ := collectorHandlerWithCredentials(client)
	return handler
}

// collectorHandlerWithCredentials also hands back the token cache the endpoint
// authorizes against, which is the same object the install path refreshes.
func collectorHandlerWithCredentials(client kubernetes.Interface) (
	func(*agentv1.MetricsCollectorRequest) (*agentv1.MetricsCollectorResponse, error),
	*metricsIngestTokens,
) {
	credentials := newMetricsIngestTokens(client, collectorNamespace)
	handler := newKubernetesMetricsCollectorHandler(client, collectorPlacement{
		Namespace: collectorNamespace,
		InCluster: true,
	}, credentials)
	return func(request *agentv1.MetricsCollectorRequest) (*agentv1.MetricsCollectorResponse, error) {
		return handler(context.Background(), request)
	}, credentials
}

func TestCollectorInstallCreatesEverythingItNeedsAndNothingElse(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	response, err := collectorHandler(client)(installRequest())
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("install response = %+v", response)
	}
	state := response.GetState()
	if !state.GetInstalled() || !state.GetCredentialReady() ||
		state.GetNamespace() != collectorNamespace {
		t.Fatalf("state = %+v", state)
	}

	ctx := context.Background()
	deployment, err := client.AppsV1().Deployments(collectorNamespace).Get(
		ctx, observability.CollectorName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	container := deployment.Spec.Template.Spec.Containers[0]
	if container.Image != "victoriametrics/vmagent:v1.149.0" {
		t.Fatalf("collector image = %q", container.Image)
	}
	// The disk buffer is configured as a Kubernetes quantity because it also
	// sizes the volume, but vmagent does not parse that spelling: it refuses
	// "1Gi" and needs plain bytes.
	if !containsArgument(container.Args, "-remoteWrite.maxDiskUsagePerURL=1073741824") {
		t.Fatalf("collector buffer flag = %v", container.Args)
	}
	// The remote write target is the Agent beside it, reached through the
	// in-Cluster Service. Anything else would mean metrics leaving the Cluster
	// on a path the Agent connection does not own.
	wantURL := "-remoteWrite.url=http://" + observability.IngestServiceName +
		"." + collectorNamespace + ".svc:8429/api/v1/write"
	if !containsArgument(container.Args, wantURL) {
		t.Fatalf("collector args = %v", container.Args)
	}

	// The credential is created by the Agent, in the Cluster, and is a real
	// value rather than a placeholder.
	secret, err := client.CoreV1().Secrets(collectorNamespace).Get(
		ctx, observability.IngestSecretName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(secret.Data[observability.IngestTokenKey]) < minMetricsIngestTokenLength {
		t.Fatalf("ingest token length = %d", len(secret.Data[observability.IngestTokenKey]))
	}

	role, err := client.RbacV1().ClusterRoles().Get(
		ctx, observability.CollectorName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			switch resource {
			// The Node endpoints, plus the discovery metadata Kubernetes
			// service discovery reads for annotated targets — EndpointSlices
			// rather than the deprecated v1 Endpoints. Secrets and ConfigMaps
			// stay out: a scrape annotation must never become a way to read
			// Cluster credentials.
			case "nodes", "nodes/metrics", "services", "endpointslices", "pods":
			default:
				t.Fatalf("collector ClusterRole grants %q", resource)
			}
		}
		for _, verb := range rule.Verbs {
			if verb != "get" && verb != "list" && verb != "watch" {
				t.Fatalf("collector ClusterRole grants verb %q", verb)
			}
		}
	}
}

func TestCollectorInstallIsIdempotentAndKeepsTheCredential(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	install := collectorHandler(client)
	if _, err := install(installRequest()); err != nil {
		t.Fatal(err)
	}
	first, err := client.CoreV1().Secrets(collectorNamespace).Get(
		context.Background(), observability.IngestSecretName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := install(installRequest()); err != nil {
		t.Fatal(err)
	}
	second, err := client.CoreV1().Secrets(collectorNamespace).Get(
		context.Background(), observability.IngestSecretName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Re-installing must not rotate the credential: the running collector has
	// the old one mounted, and replacing it would break collection until the
	// Pod happened to notice.
	if string(first.Data[observability.IngestTokenKey]) !=
		string(second.Data[observability.IngestTokenKey]) {
		t.Fatal("re-installing replaced the ingest credential")
	}
}

func TestCollectorRefusesObjectsItDoesNotOwn(t *testing.T) {
	t.Parallel()

	// A Cluster already running its own vmagent under the same name. Adopting
	// it would let an install overwrite something ZKE never created.
	client := fake.NewClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      observability.CollectorName,
			Namespace: collectorNamespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "helm"},
		},
	})
	handler := collectorHandler(client)
	response, err := handler(installRequest())
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_CONFLICT {
		t.Fatalf("install response = %+v", response)
	}
	status, err := handler(&agentv1.MetricsCollectorRequest{
		Action: agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_STATUS,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reported as not installed: it is not ZKE's collector, and saying it is
	// would offer an uninstall that deletes a stranger's workload.
	if status.GetState().GetInstalled() {
		t.Fatal("a foreign Deployment was reported as the ZKE collector")
	}
	removal, err := handler(&agentv1.MetricsCollectorRequest{
		Action: agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_UNINSTALL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if removal.GetResult() != agentv1.ResultCode_RESULT_CODE_CONFLICT {
		t.Fatalf("uninstall response = %+v", removal)
	}
	if _, err := client.AppsV1().Deployments(collectorNamespace).Get(
		context.Background(), observability.CollectorName, metav1.GetOptions{},
	); err != nil {
		t.Fatal("uninstall deleted a Deployment ZKE does not manage")
	}
}

func TestCollectorUninstallRemovesTheCredentialLast(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	handler := collectorHandler(client)
	if _, err := handler(installRequest()); err != nil {
		t.Fatal(err)
	}
	response, err := handler(&agentv1.MetricsCollectorRequest{
		Action: agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_UNINSTALL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("uninstall response = %+v", response)
	}
	state := response.GetState()
	if state.GetInstalled() || state.GetCredentialReady() {
		t.Fatalf("state after uninstall = %+v", state)
	}
	ctx := context.Background()
	for _, exists := range []func() error{
		func() error {
			_, err := client.AppsV1().Deployments(collectorNamespace).Get(
				ctx, observability.CollectorName, metav1.GetOptions{})
			return err
		},
		func() error {
			_, err := client.CoreV1().ConfigMaps(collectorNamespace).Get(
				ctx, observability.CollectorConfigMapName, metav1.GetOptions{})
			return err
		},
		func() error {
			_, err := client.RbacV1().ClusterRoles().Get(
				ctx, observability.CollectorName, metav1.GetOptions{})
			return err
		},
		func() error {
			_, err := client.RbacV1().ClusterRoleBindings().Get(
				ctx, observability.CollectorName, metav1.GetOptions{})
			return err
		},
		func() error {
			_, err := client.CoreV1().ServiceAccounts(collectorNamespace).Get(
				ctx, observability.CollectorName, metav1.GetOptions{})
			return err
		},
		func() error {
			// Leaving this behind would keep a usable token in the Cluster for
			// a collector that no longer exists.
			_, err := client.CoreV1().Secrets(collectorNamespace).Get(
				ctx, observability.IngestSecretName, metav1.GetOptions{})
			return err
		},
	} {
		if err := exists(); err == nil {
			t.Fatal("uninstall left a collector object behind")
		}
	}
}

func TestCollectorUninstallOnAnEmptyClusterSucceeds(t *testing.T) {
	t.Parallel()

	response, err := collectorHandler(fake.NewClientset())(
		&agentv1.MetricsCollectorRequest{
			Action: agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_UNINSTALL,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Removing what is not there is the state the caller asked for, not a
	// failure to report.
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		response.GetState().GetInstalled() {
		t.Fatalf("uninstall response = %+v", response)
	}
}

func TestCollectorScrapeConfigDropsReservedScopeLabels(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	if _, err := collectorHandler(client)(installRequest()); err != nil {
		t.Fatal(err)
	}
	configMap, err := client.CoreV1().ConfigMaps(collectorNamespace).Get(
		context.Background(), observability.CollectorConfigMapName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	config := configMap.Data[observability.CollectorConfigKey]
	if !strings.Contains(config, "labeldrop") ||
		!strings.Contains(config, "^zke_.*$") {
		t.Fatalf("scrape config does not drop reserved labels:\n%s", config)
	}
	if !strings.Contains(config, "scrape_interval: 30s") ||
		!strings.Contains(config, ":10250") {
		t.Fatalf("scrape config ignored the requested settings:\n%s", config)
	}
}

func TestCollectorRejectsRequestsTheServerShouldNotSend(t *testing.T) {
	t.Parallel()

	handler := collectorHandler(fake.NewClientset())
	broken := installRequest()
	broken.Image = "vmagent with spaces"
	response, err := handler(broken)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT {
		t.Fatalf("response = %+v", response)
	}
}

// The collector runs in somebody else's Cluster, so the budget the Server
// names is the budget the container gets — including a limit, which it used to
// have none of.
func TestCollectorInstallAppliesTheRequestedResourceBudget(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	request := installRequest()
	request.CpuRequest = "100m"
	request.MemoryRequest = "256Mi"
	request.CpuLimit = "1"
	request.MemoryLimit = "1Gi"
	if _, err := collectorHandler(client)(request); err != nil {
		t.Fatal(err)
	}
	container := collectorContainer(t, client)
	for _, want := range []struct {
		list  corev1.ResourceList
		name  corev1.ResourceName
		value string
	}{
		{container.Resources.Requests, corev1.ResourceCPU, "100m"},
		{container.Resources.Requests, corev1.ResourceMemory, "256Mi"},
		{container.Resources.Limits, corev1.ResourceCPU, "1"},
		{container.Resources.Limits, corev1.ResourceMemory, "1Gi"},
	} {
		quantity, ok := want.list[want.name]
		if !ok || quantity.String() != want.value {
			t.Fatalf("%s = %v, want %s", want.name, quantity, want.value)
		}
	}
}

// Kubernetes has no spelling for "no limit" other than leaving the entry off
// the container, so an empty quantity must produce an absent key rather than a
// zero one — a zero CPU limit would stop the collector from getting any.
func TestCollectorInstallLeavesEmptyQuantitiesOffTheContainer(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	request := installRequest()
	request.CpuRequest = "100m"
	request.MemoryRequest = "256Mi"
	if _, err := collectorHandler(client)(request); err != nil {
		t.Fatal(err)
	}
	container := collectorContainer(t, client)
	if len(container.Resources.Limits) != 0 {
		t.Fatalf("limits = %v, want none", container.Resources.Limits)
	}
	if len(container.Resources.Requests) != 2 {
		t.Fatalf("requests = %v", container.Resources.Requests)
	}
}

// A Server too old to know these fields names none of them. That must install
// the collector it always did rather than an unbounded one with no requests at
// all, which is what a literal reading of "nothing was asked for" would give.
func TestCollectorInstallFallsBackWhenTheServerNamesNoBudget(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	if _, err := collectorHandler(client)(installRequest()); err != nil {
		t.Fatal(err)
	}
	container := collectorContainer(t, client)
	if len(container.Resources.Limits) != 0 {
		t.Fatalf("limits = %v, want none", container.Resources.Limits)
	}
	cpu := container.Resources.Requests[corev1.ResourceCPU]
	memory := container.Resources.Requests[corev1.ResourceMemory]
	if cpu.String() != legacyCollectorCPURequest ||
		memory.String() != legacyCollectorMemoryRequest {
		t.Fatalf("requests = %v", container.Resources.Requests)
	}
}

func collectorContainer(t *testing.T, client kubernetes.Interface) corev1.Container {
	t.Helper()
	deployment, err := client.AppsV1().Deployments(collectorNamespace).Get(
		context.Background(), observability.CollectorName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return deployment.Spec.Template.Spec.Containers[0]
}

func TestCollectorStatusReportsAForeignSecretAsUnusable(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      observability.IngestSecretName,
			Namespace: collectorNamespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "helm"},
		},
		Data: map[string][]byte{observability.IngestTokenKey: []byte("short")},
	})
	response, err := collectorHandler(client)(installRequest())
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_INTERNAL &&
		response.GetResult() != agentv1.ResultCode_RESULT_CODE_CONFLICT {
		t.Fatalf("install over a foreign Secret response = %+v", response)
	}
}

// An Agent that does not run as a Pod has no Endpoint behind its Service, so
// the Service address would send the collector nowhere. That is the normal
// local-development shape, and it has to be answered with an explanation
// rather than with a collector that retries forever.
func TestCollectorRefusesToInstallWhenItCannotBeReached(t *testing.T) {
	t.Parallel()

	outsideClient := fake.NewClientset()
	outside := newKubernetesMetricsCollectorHandler(outsideClient, collectorPlacement{
		Namespace: collectorNamespace,
	}, newMetricsIngestTokens(outsideClient, collectorNamespace))
	response, err := outside(context.Background(), installRequest())
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_UNAVAILABLE ||
		response.GetReason() != "CollectorIngestAddressUnknown" {
		t.Fatalf("response = %+v", response)
	}

	// With an advertised address the same Agent can install, and the collector
	// is told to write there instead of to the Service.
	client := fake.NewClientset()
	advertised := newKubernetesMetricsCollectorHandler(client, collectorPlacement{
		Namespace:     collectorNamespace,
		AdvertisedURL: "http://host.docker.internal:8429",
	}, newMetricsIngestTokens(client, collectorNamespace))
	if _, err := advertised(context.Background(), installRequest()); err != nil {
		t.Fatal(err)
	}
	deployment, err := client.AppsV1().Deployments(collectorNamespace).Get(
		context.Background(), observability.CollectorName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !containsArgument(
		deployment.Spec.Template.Spec.Containers[0].Args,
		"-remoteWrite.url=http://host.docker.internal:8429/api/v1/write",
	) {
		t.Fatalf("collector args = %v", deployment.Spec.Template.Spec.Containers[0].Args)
	}
}

func containsArgument(args []string, want string) bool {
	for _, argument := range args {
		if argument == want {
			return true
		}
	}
	return false
}
