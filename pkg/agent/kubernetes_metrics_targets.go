package agent

import (
	"context"
	"strconv"
	"strings"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/observability"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

// The scrape targets installed beside the collector.
//
// They are here rather than left to the operator because the collector is
// useless without the first of them: kubelet reports what a Node and a Pod are
// using, and nothing else in the pipeline reports what they were given. A usage
// curve with no denominator cannot answer any capacity question, so the
// denominator is installed with the thing that measures usage.
//
// What each one is allowed to report is pinned here, not discovered. Every
// metric family a Cluster ships costs cardinality in storage every other
// Cluster shares, so both components run with an explicit allow list and the
// query catalogue is built from exactly that list.

// kubeStateResources are the object kinds kube-state-metrics watches.
//
// Secrets are deliberately absent, and so is everything else not named here:
// kube-state-metrics reports object metadata, and a component whose job is to
// count Pods has no reason to hold a list of every Secret in the Cluster. The
// Agent could grant it — it holds that permission itself — which is exactly why
// the restriction has to be written down rather than assumed.
const kubeStateResources = "nodes,pods,namespaces,resourcequotas,deployments," +
	"statefulsets,daemonsets,replicasets,jobs,cronjobs,persistentvolumeclaims," +
	"persistentvolumes"

// kubeStateMetricFamilies is what kube-state-metrics is allowed to expose.
//
// The list and the Server's query catalogue are one decision: a family here
// that no query reads is pure cardinality, and a query reading a family that is
// not here returns nothing with no explanation.
var kubeStateMetricFamilies = []string{
	// Capacity — the denominator every utilisation query divides by.
	"kube_node_status_allocatable",
	"kube_node_status_capacity",
	"kube_node_status_condition",
	// A cordoned Node is not a failed one, so it appears in no condition and in
	// no usage curve — it simply stops receiving work, and the Cluster loses
	// that much capacity without anything saying so.
	"kube_node_spec_unschedulable",
	// What workloads asked for, against what they use.
	"kube_pod_container_resource_requests",
	"kube_pod_container_resource_limits",
	// Ownership, so Pod-level usage can be rolled up to the workload that owns
	// it. Both are needed: a Deployment owns a ReplicaSet, which owns the Pods.
	"kube_pod_owner",
	"kube_replicaset_owner",
	// Placement, so Pods can be counted per Node against that Node's Pod
	// capacity. It is the third capacity a Node runs out of, and the only one
	// nothing else here reports: no other family carries both the Pod and the
	// Node it landed on. One series per Pod, the same order as kube_pod_owner.
	"kube_pod_info",
	// Workload and Pod health over time, which the live resource views can only
	// show as "now".
	"kube_pod_status_phase",
	"kube_pod_container_status_restarts_total",
	// Running is not serving. A Pod whose readiness probe fails stays in the
	// Running phase and is removed from every Service behind it, which is the
	// difference between a workload that is up and one that answers. Only the
	// `true` series is kept — the other two conditions are its complement
	// against the Pod count, at three times the series.
	"kube_pod_status_ready",
	// A Pod the scheduler could not place. It is Pending like a Pod that is
	// merely still starting, and the two are the same phase with completely
	// different causes: one resolves itself, the other waits for capacity,
	// a toleration or a volume that may never arrive.
	"kube_pod_status_unschedulable",
	// Why a container is not running. A restart count says something happened;
	// these two say what it was — OOMKilled and CrashLoopBackOff are different
	// faults with different fixes, and a curve that only counts restarts sends
	// the operator to read logs to find out which one they are looking at.
	"kube_pod_container_status_waiting_reason",
	"kube_pod_container_status_last_terminated_reason",
	// Namespace quotas, which are the limit a workload hits before the Cluster
	// runs out of anything. A Namespace refused at 100% of its quota looks
	// identical to a healthy one in every usage curve here.
	"kube_resourcequota",
	"kube_deployment_status_replicas",
	"kube_deployment_status_replicas_available",
	"kube_statefulset_status_replicas",
	"kube_statefulset_status_replicas_ready",
	"kube_daemonset_status_desired_number_scheduled",
	"kube_daemonset_status_number_ready",
	// Batch work, which the replica families above deliberately exclude: a Job
	// that has finished is not a workload missing replicas. A failing nightly
	// Job is invisible in every other family here — its Pods are gone by the
	// time anybody looks, and the object itself reports the failure.
	"kube_job_status_active",
	"kube_job_status_failed",
	"kube_job_status_succeeded",
	// Storage objects, which the kubelet's volume statistics cannot report: it
	// measures the volumes it has mounted, so a claim no Pod could bind and a
	// volume released without being reclaimed are precisely the ones missing
	// from that view.
	"kube_persistentvolumeclaim_status_phase",
	"kube_persistentvolume_status_phase",
}

// nodeExporterCollectors are the only collectors enabled.
//
// node-exporter ships around forty by default, most of which describe hardware
// nobody asks ZKE about, and each one multiplies its series by the number of
// Nodes. Defaults are switched off and these are switched back on, so adding
// one later is a visible decision rather than a version bump.
var nodeExporterCollectors = []string{
	"cpu",
	"meminfo",
	"filesystem",
	"diskstats",
	"netdev",
	"loadavg",
	// The kernel's own counters for what the machine is doing between its
	// workloads: context switches, interrupts, the run queue and the blocked
	// queue, and the boot time an unexpected reboot is only visible against.
	"stat",
	// Paging and the kernel OOM killer. Its default field selector is already
	// narrow — page faults, swap pages and `oom_kill` — and those are exactly
	// the events that explain a Node whose memory looks unremarkable while
	// everything on it is slow.
	"vmstat",
	// Sockets, which no byte counter reports: a Node out of ephemeral ports or
	// over its TCP memory limit refuses new connections while its throughput
	// stays flat.
	"sockstat",
	// The process-wide file descriptor table. Exhausting it fails every accept
	// and every open on the Node at once.
	"filefd",
	// Clock offset and synchronisation state. A Node whose clock has drifted
	// produces certificate errors, out-of-order logs and metrics that arrive
	// outside the ingest window — all of which are read as faults elsewhere.
	"timex",
	// Saturation that never appears in a throughput or utilisation curve. A
	// Node whose conntrack table is full, or whose TCP connections are being
	// retransmitted, serves traffic that simply times out while every byte
	// counter on it looks ordinary.
	"netstat",
	"conntrack",
	// Pressure stall information: how long tasks actually waited for CPU, memory
	// or I/O. It needs a kernel new enough to expose /proc/pressure; on an older
	// one the collector reports nothing and the rest of the exporter is
	// unaffected.
	"pressure",
}

// nodeExporterMountPointsExclude keeps Kubernetes' own mounts out of the
// filesystem view.
//
// Every Pod's volumes are mounts on its Node, and so is a shared memory segment
// for every container, so a Cluster of a few thousand Pods would otherwise
// report a filesystem series per Pod per Node — the exact per-Pod cardinality
// this pipeline is built to avoid.
//
// Two of the three branches deliberately match a path fragment rather than a
// prefix. The first branch is upstream's, and it assumes the kubelet keeps its
// state in /var/lib/kubelet and the runtime in /var/lib/docker; a Docker Desktop
// Node keeps both under /mnt/docker-desktop-disk/data and k3s keeps the kubelet
// under /var/lib/rancher, so on those Nodes a prefix matches nothing and every
// Pod comes back. The second branch is where a Pod's volumes and a CSI mount
// live under any kubelet root, and the third is the per-container shared memory
// mount, which ends in /shm under every runtime. The Node's own /dev/shm is
// already covered by the /dev branch.
const nodeExporterMountPointsExclude = `^/(dev|proc|sys|run/credentials/.+|` +
	`var/lib/kubelet/.+|var/lib/docker/.+)($|/)` +
	`|/kubelet/(pods|plugins)/` +
	`|/shm$`

// nodeExporterNetstatFields bounds the one collector above that is otherwise
// unbounded: netstat exposes the whole of /proc/net/snmp, around a hundred
// series per Node, for the nine counters the catalogue reads.
//
// The four UDP counters are here because cluster DNS runs on UDP: a receive
// buffer overflow on the Node is a resolver timeout in every Pod on it, and it
// appears in no TCP counter and in no throughput curve.
//
// Deliberately not enabled, so that turning one on later is a visible decision:
// `softnet` and `schedstat` report per CPU, which multiplies their series by
// the core count of every Node; `hwmon`, `thermal_zone` and `power_supply`
// describe hardware that a virtualised Node does not have and that a bare metal
// one exposes under sensor names nothing here could query portably.
const nodeExporterNetstatFields = `^(Tcp_RetransSegs|Tcp_OutSegs|Tcp_CurrEstab|` +
	`TcpExt_ListenDrops|TcpExt_ListenOverflows|` +
	`Udp_InErrors|Udp_NoPorts|Udp_RcvbufErrors|Udp_SndbufErrors)$`

// nodeExporterUnavailable marks the one component a Cluster may legitimately
// refuse. It needs the host's network namespace and host paths, which a
// Namespace running under a `baseline` or `restricted` Pod Security admission
// level rejects. That is the Cluster's policy, not a fault, so it is reported
// and the rest of the install stands.
const nodeExporterUnavailable = "NodeExporterRejected"

// applyKubeStateMetrics installs the object metrics exporter. Its failure is
// the whole install's failure: every utilisation, request and workload query
// reads it, so a pipeline without it silently answers half the catalogue with
// nothing.
func applyKubeStateMetrics(
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
	component *agentv1.MetricsCollectorComponent,
) error {
	labels := observability.ComponentLabels(observability.ComponentKubeState)
	name := observability.KubeStateName

	if err := applyCollectorObject(
		ctx,
		"kube-state-metrics ServiceAccount",
		func() (map[string]string, error) {
			existing, err := client.CoreV1().ServiceAccounts(namespace).Get(
				ctx, name, metav1.GetOptions{},
			)
			if err != nil {
				return nil, err
			}
			return existing.Labels, nil
		},
		func() error {
			_, err := client.CoreV1().ServiceAccounts(namespace).Create(ctx, &corev1.ServiceAccount{
				ObjectMeta:                   metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
				AutomountServiceAccountToken: pointerTo(true),
			}, metav1.CreateOptions{})
			return err
		},
		func() error {
			_, err := client.CoreV1().ServiceAccounts(namespace).Update(ctx, &corev1.ServiceAccount{
				ObjectMeta:                   metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
				AutomountServiceAccountToken: pointerTo(true),
			}, metav1.UpdateOptions{})
			return err
		},
	); err != nil {
		return err
	}

	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		// Read only, and only the kinds kubeStateResources names. list and watch
		// without get: kube-state-metrics builds informers, it never fetches a
		// single object by name.
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{
					"nodes", "pods", "namespaces", "resourcequotas",
					"persistentvolumeclaims", "persistentvolumes",
				},
				Verbs: []string{"list", "watch"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"deployments", "statefulsets", "daemonsets", "replicasets"},
				Verbs:     []string{"list", "watch"},
			},
			{
				APIGroups: []string{"batch"},
				Resources: []string{"jobs", "cronjobs"},
				Verbs:     []string{"list", "watch"},
			},
		},
	}
	if err := applyCollectorObject(
		ctx,
		"kube-state-metrics ClusterRole",
		func() (map[string]string, error) {
			existing, err := client.RbacV1().ClusterRoles().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return existing.Labels, nil
		},
		func() error {
			_, err := client.RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{})
			return err
		},
		func() error {
			_, err := client.RbacV1().ClusterRoles().Update(ctx, clusterRole, metav1.UpdateOptions{})
			return err
		},
	); err != nil {
		return err
	}

	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Subjects: []rbacv1.Subject{{
			Kind: "ServiceAccount", Name: name, Namespace: namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: name,
		},
	}
	if err := applyCollectorObject(
		ctx,
		"kube-state-metrics ClusterRoleBinding",
		func() (map[string]string, error) {
			existing, err := client.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return existing.Labels, nil
		},
		func() error {
			_, err := client.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{})
			return err
		},
		func() error {
			// RoleRef is immutable, so a binding pointing elsewhere is replaced.
			if err := client.RbacV1().ClusterRoleBindings().Delete(
				ctx, name, metav1.DeleteOptions{},
			); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			_, err := client.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{})
			return err
		},
	); err != nil {
		return err
	}

	// A Service rather than Pod discovery: the collector then reaches it by a
	// name that does not change, and its ClusterRole needs no permission to list
	// Pods just to find one address.
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: map[string]string{"app.kubernetes.io/name": name},
			Ports: []corev1.ServicePort{{
				Name:       "metrics",
				Port:       observability.KubeStatePort,
				TargetPort: intstr.FromInt32(observability.KubeStatePort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
	if err := applyCollectorObject(
		ctx,
		"kube-state-metrics Service",
		func() (map[string]string, error) {
			existing, err := client.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return existing.Labels, nil
		},
		func() error {
			_, err := client.CoreV1().Services(namespace).Create(ctx, service, metav1.CreateOptions{})
			return err
		},
		func() error {
			existing, err := client.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			// ClusterIP is immutable once assigned, so the existing one is kept
			// and only the parts an install decides are written back.
			existing.Labels = labels
			existing.Spec.Selector = service.Spec.Selector
			existing.Spec.Ports = service.Spec.Ports
			_, err = client.CoreV1().Services(namespace).Update(ctx, existing, metav1.UpdateOptions{})
			return err
		},
	); err != nil {
		return err
	}

	resources, err := componentResources(component)
	if err != nil {
		return err
	}
	replicas := int32(1)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			// Two of them would report every object twice.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/name": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName:           name,
					AutomountServiceAccountToken: pointerTo(true),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: pointerTo(true),
						RunAsUser:    pointerTo(int64(65534)),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{{
						Name:            observability.KubeStateContainerName,
						Image:           component.GetImage(),
						ImagePullPolicy: corev1.PullPolicy(component.GetImagePullPolicy()),
						Args: []string{
							"--port=" + strconv.Itoa(observability.KubeStatePort),
							"--resources=" + kubeStateResources,
							"--metric-allowlist=" + strings.Join(kubeStateMetricFamilies, ","),
						},
						Ports: []corev1.ContainerPort{{
							Name:          "metrics",
							ContainerPort: observability.KubeStatePort,
							Protocol:      corev1.ProtocolTCP,
						}},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: pointerTo(false),
							ReadOnlyRootFilesystem:   pointerTo(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						Resources: resources,
					}},
				},
			},
		},
	}
	return applyCollectorObject(
		ctx,
		"kube-state-metrics Deployment",
		func() (map[string]string, error) {
			existing, err := client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return existing.Labels, nil
		},
		func() error {
			_, err := client.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{})
			return err
		},
		func() error {
			_, err := client.AppsV1().Deployments(namespace).Update(ctx, deployment, metav1.UpdateOptions{})
			return err
		},
	)
}

// applyNodeExporter installs the node metrics exporter and reports why it could
// not, rather than failing the install.
//
// It is the only component that needs the host: its numbers come from /proc,
// /sys and the root filesystem, and there is no version of it that reads disk
// or network without them. A Namespace under a restrictive Pod Security level
// refuses exactly that, and ZKE does not relabel somebody's Namespace to get
// around it — changing a Cluster's security posture is not a side effect an
// install is allowed to have.
func applyNodeExporter(
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
	component *agentv1.MetricsCollectorComponent,
) (string, string) {
	labels := observability.ComponentLabels(observability.ComponentNodeExporter)
	name := observability.NodeExporterName

	if err := applyCollectorObject(
		ctx,
		"node-exporter ServiceAccount",
		func() (map[string]string, error) {
			existing, err := client.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return existing.Labels, nil
		},
		func() error {
			_, err := client.CoreV1().ServiceAccounts(namespace).Create(ctx, &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
				// It never talks to the API server: the collector discovers Nodes
				// and scrapes it directly, so it needs no token at all.
				AutomountServiceAccountToken: pointerTo(false),
			}, metav1.CreateOptions{})
			return err
		},
		func() error {
			_, err := client.CoreV1().ServiceAccounts(namespace).Update(ctx, &corev1.ServiceAccount{
				ObjectMeta:                   metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
				AutomountServiceAccountToken: pointerTo(false),
			}, metav1.UpdateOptions{})
			return err
		},
	); err != nil {
		return nodeExporterUnavailable, nodeExporterFailureMessage(err)
	}

	resources, err := componentResources(component)
	if err != nil {
		return nodeExporterUnavailable, "node metrics exporter resource budget is not a valid Kubernetes quantity"
	}
	daemonSet := renderNodeExporterDaemonSet(namespace, name, labels, component, resources)
	if err := applyCollectorObject(
		ctx,
		"node-exporter DaemonSet",
		func() (map[string]string, error) {
			existing, err := client.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return existing.Labels, nil
		},
		func() error {
			_, err := client.AppsV1().DaemonSets(namespace).Create(ctx, daemonSet, metav1.CreateOptions{})
			return err
		},
		func() error {
			_, err := client.AppsV1().DaemonSets(namespace).Update(ctx, daemonSet, metav1.UpdateOptions{})
			return err
		},
	); err != nil {
		return nodeExporterUnavailable, nodeExporterFailureMessage(err)
	}
	return "", ""
}

func renderNodeExporterDaemonSet(
	namespace string,
	name string,
	labels map[string]string,
	component *agentv1.MetricsCollectorComponent,
	resources corev1.ResourceRequirements,
) *appsv1.DaemonSet {
	args := []string{
		"--path.procfs=/host/proc",
		"--path.sysfs=/host/sys",
		"--path.rootfs=/host/root",
		"--web.listen-address=:" + strconv.Itoa(observability.NodeExporterPort),
		"--collector.disable-defaults",
		"--collector.filesystem.mount-points-exclude=" + nodeExporterMountPointsExclude,
		"--collector.filesystem.fs-types-exclude=" +
			`^(autofs|binfmt_misc|bpf|cgroup2?|configfs|debugfs|devpts|devtmpfs|` +
			`fusectl|hugetlbfs|iso9660|mqueue|nsfs|overlay|proc|procfs|pstore|` +
			`rpc_pipefs|securityfs|selinuxfs|squashfs|sysfs|tracefs)$`,
		// Virtual interfaces: one per Pod on every Node, and none of them is the
		// Node's network.
		"--collector.netdev.device-exclude=" +
			`^(veth|cali|cni|docker|flannel|lxc|tunl|nodelocaldns).*|^lo$`,
		"--collector.diskstats.device-exclude=" +
			`^(ram|loop|fd|dm-|sr|nbd)\d+$`,
		"--collector.netstat.fields=" + nodeExporterNetstatFields,
	}
	for _, collector := range nodeExporterCollectors {
		args = append(args, "--collector."+collector)
	}
	hostPathDirectory := corev1.HostPathDirectory
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app.kubernetes.io/name": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName:           name,
					AutomountServiceAccountToken: pointerTo(false),
					// The host's network, so the numbers describe the Node rather
					// than a Pod sandbox, and so the collector can reach it at the
					// Node's own address without a Service per Node.
					HostNetwork: true,
					DNSPolicy:   corev1.DNSClusterFirstWithHostNet,
					// Every Node, including the ones a Cluster keeps for its own
					// control plane: a Node missing from the disk view because it
					// carries a taint is a gap nobody would think to look for.
					Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: pointerTo(true),
						RunAsUser:    pointerTo(int64(65534)),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{{
						Name:            observability.NodeExporterContainerName,
						Image:           component.GetImage(),
						ImagePullPolicy: corev1.PullPolicy(component.GetImagePullPolicy()),
						Args:            args,
						Ports: []corev1.ContainerPort{{
							Name:          "metrics",
							ContainerPort: observability.NodeExporterPort,
							HostPort:      observability.NodeExporterPort,
							Protocol:      corev1.ProtocolTCP,
						}},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: pointerTo(false),
							ReadOnlyRootFilesystem:   pointerTo(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						Resources: resources,
						VolumeMounts: []corev1.VolumeMount{
							{Name: "proc", MountPath: "/host/proc", ReadOnly: true},
							{Name: "sys", MountPath: "/host/sys", ReadOnly: true},
							// The host's root, and deliberately without mount
							// propagation.
							//
							// `HostToContainer` is what the exporter's own
							// packaging uses, and it is what lets a filesystem
							// mounted on the Node after this Pod started appear
							// inside it. It also makes the whole component
							// refuse to start on a Node whose own `/` is a
							// private mount: the Docker runtime validates the
							// source's propagation before it creates the
							// container and answers `ContainerCannotRun`, so
							// nothing runs and no log is ever written — which is
							// the state a Docker Desktop Cluster arrives in out
							// of the box.
							//
							// Without it a hostPath is still a recursive bind,
							// so every mount that existed when the Pod started
							// is visible and measured. What is lost is the Node
							// whose disks changed underneath a running Pod, and
							// the two places that would most often happen are
							// already excluded from what this exporter reports:
							// PersistentVolume mounts live under
							// /var/lib/kubelet and the runtime's own layers
							// under /var/lib/docker. The residual case — an
							// operator mounting a new filesystem on the Node —
							// corrects itself the next time this Pod is
							// recreated.
							{Name: "root", MountPath: "/host/root", ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "proc", VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{Path: "/proc", Type: &hostPathDirectory},
						}},
						{Name: "sys", VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{Path: "/sys", Type: &hostPathDirectory},
						}},
						{Name: "root", VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{Path: "/", Type: &hostPathDirectory},
						}},
					},
				},
			},
		},
	}
}

// nodeExporterFailureMessage keeps the Cluster's own words out of the answer
// while still separating the case an operator can do something about.
func nodeExporterFailureMessage(err error) string {
	if apierrors.IsForbidden(err) || apierrors.IsInvalid(err) {
		return "the Cluster refused the node metrics exporter; it needs host network and host path access, which a Namespace under a baseline or restricted Pod Security level does not allow"
	}
	return "the node metrics exporter could not be installed in this Cluster"
}
