package agent

import (
	"context"
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/observability"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func bundleRequest() *agentv1.MetricsCollectorRequest {
	request := installRequest()
	request.KubeStateMetrics = &agentv1.MetricsCollectorComponent{
		Image:           "registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.1",
		ImagePullPolicy: "IfNotPresent",
		CpuRequest:      "20m",
		MemoryRequest:   "128Mi",
	}
	request.NodeExporter = &agentv1.MetricsCollectorComponent{
		Image:           "quay.io/prometheus/node-exporter:v1.12.1",
		ImagePullPolicy: "IfNotPresent",
		CpuRequest:      "10m",
		MemoryRequest:   "32Mi",
	}
	return request
}

func componentByName(
	state *agentv1.MetricsCollectorState,
	name string,
) *agentv1.MetricsComponentState {
	for _, component := range state.GetComponents() {
		if component.GetComponent() == name {
			return component
		}
	}
	return nil
}

func TestBundleInstallCreatesAllThreeScrapeTargets(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	response, err := collectorHandler(client)(bundleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("install = %+v", response)
	}

	ctx := context.Background()
	if _, err := client.AppsV1().Deployments(collectorNamespace).Get(
		ctx, observability.KubeStateName, metav1.GetOptions{},
	); err != nil {
		t.Fatalf("kube-state-metrics Deployment: %v", err)
	}
	if _, err := client.CoreV1().Services(collectorNamespace).Get(
		ctx, observability.KubeStateName, metav1.GetOptions{},
	); err != nil {
		t.Fatalf("kube-state-metrics Service: %v", err)
	}
	if _, err := client.AppsV1().DaemonSets(collectorNamespace).Get(
		ctx, observability.NodeExporterName, metav1.GetOptions{},
	); err != nil {
		t.Fatalf("node-exporter DaemonSet: %v", err)
	}

	// Every component reports itself, so a half-installed pipeline cannot look
	// healthy behind one aggregate flag.
	for _, name := range observability.Components() {
		component := componentByName(response.GetState(), name)
		if component == nil {
			t.Fatalf("%s is missing from the reported state", name)
		}
		if !component.GetInstalled() {
			t.Fatalf("%s reported as not installed after a successful install", name)
		}
	}
}

// The object exporter reports object metadata, and it is installed with a
// permission set the Agent could make much wider. Secrets in particular are
// something the Agent holds and the exporter has no reason to see.
func TestKubeStateMetricsRoleIsReadOnlyAndExcludesSecrets(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	if _, err := collectorHandler(client)(bundleRequest()); err != nil {
		t.Fatal(err)
	}
	role, err := client.RbacV1().ClusterRoles().Get(
		context.Background(), observability.KubeStateName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			if resource == "secrets" || resource == "configmaps" {
				t.Fatalf("kube-state-metrics may read %q", resource)
			}
		}
		for _, verb := range rule.Verbs {
			if verb != "list" && verb != "watch" {
				t.Fatalf("kube-state-metrics holds verb %q; only list and watch are needed", verb)
			}
		}
	}
	assertNoWriteAnywhere(t, role)

	deployment, err := client.AppsV1().Deployments(collectorNamespace).Get(
		context.Background(), observability.KubeStateName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(deployment.Spec.Template.Spec.Containers[0].Args, " ")
	// The allow list is the other half of the same decision: the ClusterRole
	// bounds what it may watch, this bounds what it may report.
	if !strings.Contains(args, "--metric-allowlist=") ||
		!strings.Contains(args, "kube_node_status_allocatable") ||
		!strings.Contains(args, "kube_replicaset_owner") {
		t.Fatalf("kube-state-metrics args do not carry the allow list: %s", args)
	}
	if strings.Contains(args, "secret") {
		t.Fatalf("kube-state-metrics args mention secrets: %s", args)
	}
}

func assertNoWriteAnywhere(t *testing.T, role *rbacv1.ClusterRole) {
	t.Helper()
	for _, rule := range role.Rules {
		for _, verb := range rule.Verbs {
			switch verb {
			case "create", "update", "patch", "delete", "deletecollection", "*":
				t.Fatalf("kube-state-metrics may %s objects", verb)
			}
		}
	}
}

func TestNodeExporterRunsOnEveryNodeWithoutAnAPIToken(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	if _, err := collectorHandler(client)(bundleRequest()); err != nil {
		t.Fatal(err)
	}
	daemonSet, err := client.AppsV1().DaemonSets(collectorNamespace).Get(
		context.Background(), observability.NodeExporterName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	spec := daemonSet.Spec.Template.Spec
	if !spec.HostNetwork {
		t.Fatal("node-exporter is not on the host network, so its numbers describe a Pod sandbox")
	}
	if spec.DNSPolicy != corev1.DNSClusterFirstWithHostNet {
		t.Fatalf("DNS policy = %s", spec.DNSPolicy)
	}
	// A Node kept out of the disk view because it carries a taint is a gap
	// nobody would think to look for.
	if len(spec.Tolerations) != 1 || spec.Tolerations[0].Operator != corev1.TolerationOpExists {
		t.Fatalf("tolerations = %+v", spec.Tolerations)
	}
	// It never talks to the API server, so it is given no token to lose.
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Fatal("node-exporter mounts a ServiceAccount token it has no use for")
	}
	args := strings.Join(spec.Containers[0].Args, " ")
	if !strings.Contains(args, "--collector.disable-defaults") {
		t.Fatalf("node-exporter runs with its default collectors: %s", args)
	}
	if !strings.Contains(args, "--collector.filesystem.mount-points-exclude=") {
		t.Fatalf("node-exporter would report a filesystem series per Pod per Node: %s", args)
	}
}

// The scrape configuration and the installed objects are one decision. A job
// for something that was never installed fails every interval, which reads as a
// broken Cluster rather than as a target nobody asked for.
func TestScrapeConfigCoversExactlyTheInstalledTargets(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		request *agentv1.MetricsCollectorRequest
		jobs    []string
		absent  []string
	}{
		"whole bundle": {
			request: bundleRequest(),
			jobs: []string{
				"kubelet-resource",
				"kubelet-cadvisor",
				"kubelet-volume",
				"kube-state-metrics",
				"node-exporter",
			},
		},
		"collector alone, as an older Server asks": {
			request: installRequest(),
			// The kubelet endpoints are not optional: they are one target, and
			// every install reads all three of them.
			jobs:   []string{"kubelet-resource", "kubelet-cadvisor", "kubelet-volume"},
			absent: []string{"kube-state-metrics", "node-exporter"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := fake.NewClientset()
			if _, err := collectorHandler(client)(testCase.request); err != nil {
				t.Fatal(err)
			}
			configMap, err := client.CoreV1().ConfigMaps(collectorNamespace).Get(
				context.Background(), observability.CollectorConfigMapName, metav1.GetOptions{},
			)
			if err != nil {
				t.Fatal(err)
			}
			config := configMap.Data[observability.CollectorConfigKey]
			for _, job := range testCase.jobs {
				if !strings.Contains(config, "job_name: "+job) {
					t.Fatalf("scrape configuration is missing job %q:\n%s", job, config)
				}
			}
			for _, job := range testCase.absent {
				if strings.Contains(config, "job_name: "+job) {
					t.Fatalf("scrape configuration names uninstalled job %q:\n%s", job, config)
				}
			}
			// The scope labels are dropped in every job, not only the first: the
			// Server replaces them anyway, and a job that forgot would ship a
			// Cluster's own idea of its identity.
			if strings.Count(config, "regex: ^zke_.*$") != len(testCase.jobs) {
				t.Fatalf("not every job drops reserved scope labels:\n%s", config)
			}
			// The two large kubelet endpoints are taken through an allow list.
			// Without it a single install multiplies what every Cluster ships
			// into storage they all share, which is the failure this pipeline
			// is built to avoid rather than to discover in production.
			for _, family := range []string{
				"container_cpu_cfs_throttled_periods_total",
				"kubelet_volume_stats_used_bytes",
			} {
				if !strings.Contains(config, family) {
					t.Fatalf("scrape configuration does not keep %q:\n%s", family, config)
				}
			}
			if strings.Count(config, "action: keep") != 2 {
				t.Fatalf("cAdvisor and volume statistics must be filtered:\n%s", config)
			}
			// Container state reasons are filtered where they are produced. A
			// query-side selector would have paid for every reason the exporter
			// knows, per container, in storage every Cluster shares.
			if strings.Contains(config, "job_name: kube-state-metrics") &&
				!strings.Contains(config, "__tmp_zke_keep") {
				t.Fatalf("container state reasons are not filtered at the scrape:\n%s", config)
			}
			if strings.Contains(config, "ContainerCannotRun") {
				t.Fatalf("scrape keeps a reason no query reads:\n%s", config)
			}
		})
	}
}

// An older Server sends no component configuration at all. The Agent must then
// install what that Server asked for and report the other two as absent — not
// invent entries for workloads that are not in the Cluster.
func TestCollectorAloneReportsTheOtherTargetsAsNotInstalled(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	response, err := collectorHandler(client)(installRequest())
	if err != nil {
		t.Fatal(err)
	}
	collector := componentByName(response.GetState(), observability.ComponentCollector)
	if collector == nil || !collector.GetInstalled() {
		t.Fatalf("collector = %+v", collector)
	}
	for _, name := range []string{
		observability.ComponentKubeState,
		observability.ComponentNodeExporter,
	} {
		component := componentByName(response.GetState(), name)
		if component == nil {
			t.Fatalf("%s is missing from the reported state", name)
		}
		if component.GetInstalled() {
			t.Fatalf("%s reported as installed but was never requested", name)
		}
		// Nobody asked for it, so there is nothing for an operator to fix.
		if component.GetUnavailableReason() != "" {
			t.Fatalf("%s carries reason %q for a component nobody requested",
				name, component.GetUnavailableReason())
		}
	}
}

// The one component a Cluster may legitimately refuse. It needs host namespaces
// and host paths, which a restrictive Pod Security level rejects — and that must
// not take the rest of the pipeline down with it.
func TestNodeExporterRefusalDoesNotFailTheInstall(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	client.PrependReactor(
		"create",
		"daemonsets",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(
				schema.GroupResource{Group: "apps", Resource: "daemonsets"},
				observability.NodeExporterName,
				nil,
			)
		},
	)
	response, err := collectorHandler(client)(bundleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("a refused node exporter failed the whole install: %+v", response)
	}
	if collector := componentByName(
		response.GetState(), observability.ComponentCollector,
	); collector == nil || !collector.GetInstalled() {
		t.Fatalf("collector = %+v", collector)
	}
	nodeExporter := componentByName(response.GetState(), observability.ComponentNodeExporter)
	if nodeExporter == nil || nodeExporter.GetInstalled() {
		t.Fatalf("node-exporter = %+v", nodeExporter)
	}
	if nodeExporter.GetUnavailableReason() != nodeExporterUnavailable ||
		nodeExporter.GetUnavailableMessage() == "" {
		t.Fatalf("a refused component must explain itself: %+v", nodeExporter)
	}

	configMap, err := client.CoreV1().ConfigMaps(collectorNamespace).Get(
		context.Background(), observability.CollectorConfigMapName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(configMap.Data[observability.CollectorConfigKey], "job_name: node-exporter") {
		t.Fatal("the collector was told to scrape a target the Cluster refused")
	}

	// The reason has to outlive the install. Without it a later status read shows
	// "not installed" for a component the Cluster refused, and the operator would
	// reinstall just to see why.
	status, err := collectorHandler(client)(&agentv1.MetricsCollectorRequest{
		Action: agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_STATUS,
	})
	if err != nil {
		t.Fatal(err)
	}
	later := componentByName(status.GetState(), observability.ComponentNodeExporter)
	if later == nil || later.GetUnavailableReason() != nodeExporterUnavailable {
		t.Fatalf("the refusal did not survive the install: %+v", later)
	}
}

func TestBundleUninstallRemovesEveryComponent(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	handler := collectorHandler(client)
	if _, err := handler(bundleRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := handler(&agentv1.MetricsCollectorRequest{
		Action: agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_UNINSTALL,
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	// An exporter left behind would keep consuming a Node's memory for data
	// nothing reads.
	if _, err := client.AppsV1().DaemonSets(collectorNamespace).Get(
		ctx, observability.NodeExporterName, metav1.GetOptions{},
	); !apierrors.IsNotFound(err) {
		t.Fatalf("node-exporter DaemonSet survived the uninstall: %v", err)
	}
	if _, err := client.AppsV1().Deployments(collectorNamespace).Get(
		ctx, observability.KubeStateName, metav1.GetOptions{},
	); !apierrors.IsNotFound(err) {
		t.Fatalf("kube-state-metrics Deployment survived the uninstall: %v", err)
	}
	if _, err := client.CoreV1().Services(collectorNamespace).Get(
		ctx, observability.KubeStateName, metav1.GetOptions{},
	); !apierrors.IsNotFound(err) {
		t.Fatalf("kube-state-metrics Service survived the uninstall: %v", err)
	}
	if _, err := client.RbacV1().ClusterRoles().Get(
		ctx, observability.KubeStateName, metav1.GetOptions{},
	); !apierrors.IsNotFound(err) {
		t.Fatalf("kube-state-metrics ClusterRole survived the uninstall: %v", err)
	}
	if _, err := client.RbacV1().ClusterRoleBindings().Get(
		ctx, observability.KubeStateName, metav1.GetOptions{},
	); !apierrors.IsNotFound(err) {
		t.Fatalf("kube-state-metrics ClusterRoleBinding survived the uninstall: %v", err)
	}
	if _, err := client.CoreV1().Secrets(collectorNamespace).Get(
		ctx, observability.IngestSecretName, metav1.GetOptions{},
	); !apierrors.IsNotFound(err) {
		t.Fatalf("the ingest credential survived the uninstall: %v", err)
	}
}

// The collector starts pushing seconds after an install. If the endpoint only
// learned about the credential on its next poll, every one of those writes
// would be answered 401 — and vmagent backs off exponentially on top of that,
// so a working install looks broken for well over a minute. Worse, the collector
// status reads the Secret straight from the API server, so the Console reports
// the credential as ready throughout.
func TestInstallMakesTheCredentialUsableImmediately(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	handler, credentials := collectorHandlerWithCredentials(client)

	// Nothing is accepted before an install: there is no credential to present.
	if credentials.authorize("Bearer anything") {
		t.Fatal("the endpoint authorized a caller before any credential existed")
	}

	if _, err := handler(bundleRequest()); err != nil {
		t.Fatal(err)
	}
	secret, err := client.CoreV1().Secrets(collectorNamespace).Get(
		context.Background(), observability.IngestSecretName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	token := string(secret.Data[observability.IngestTokenKey])
	if !credentials.authorize("Bearer " + token) {
		t.Fatal("the endpoint would answer 401 to the collector this install just configured")
	}

	// And it stops being accepted with the uninstall that deleted it. The
	// credential is deleted last so no usable token outlives the collector; that
	// only holds if the endpoint forgets it too.
	if _, err := handler(&agentv1.MetricsCollectorRequest{
		Action: agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_UNINSTALL,
	}); err != nil {
		t.Fatal(err)
	}
	if credentials.authorize("Bearer " + token) {
		t.Fatal("a deleted credential is still accepted by the endpoint")
	}
}

// A Cluster may already run its own kube-state-metrics under the same name.
// Adopting it would let an install overwrite something this Agent never created.
func TestBundleRefusesTargetsItDoesNotOwn(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset()
	if _, err := client.CoreV1().Services(collectorNamespace).Create(
		context.Background(),
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{
			Name:      observability.KubeStateName,
			Namespace: collectorNamespace,
		}},
		metav1.CreateOptions{},
	); err != nil {
		t.Fatal(err)
	}
	response, err := collectorHandler(client)(bundleRequest())
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() == agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatal("the install adopted a Service it did not create")
	}
}
