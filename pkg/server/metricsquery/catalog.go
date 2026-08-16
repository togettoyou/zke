package metricsquery

import (
	"fmt"
	"strings"

	"github.com/togettoyou/zke/pkg/shared/observability"
)

// Kind separates a query that returns a curve from one that returns a single
// current value.
type Kind string

const (
	KindRange   Kind = "range"
	KindInstant Kind = "instant"
)

// Unit is the unit the Server converts a query's values into before returning
// them, so no client has to know what the underlying metric counts in.
type Unit string

const (
	UnitMillicores     Unit = "millicores"
	UnitBytes          Unit = "bytes"
	UnitBytesPerSecond Unit = "bytes_per_second"
	UnitCount          Unit = "count"
	UnitRatio          Unit = "ratio"
)

// Definition is one query the Console may ask for.
//
// The catalogue exists instead of accepting PromQL from the browser. Two
// reasons, and the second is the load-bearing one: an expression from outside
// would have to be parsed and rewritten before a scope filter could be
// injected, and any flaw in that rewriting is a cross-tenant data leak; and
// the cost of an arbitrary expression cannot be predicted, so one query could
// exhaust the storage a single-instance Server depends on. Here the filter is
// part of the template, and every template's shape is known before it runs.
type Definition struct {
	Name  string
	Title string
	Kind  Kind
	Unit  Unit
	// Dimensions are the labels each series carries besides the Cluster
	// identity, in the order a client should display them.
	Dimensions        []string
	RequiresNamespace bool
	SupportsTop       bool
	// RequiresTop marks a query whose natural answer is unbounded. Pod level is
	// the first such dimension: a Cluster has orders of magnitude more Pods than
	// Nodes, and "every Pod" is neither renderable nor a question anyone asks.
	// Demanding Top N in the request keeps that bound in the contract instead of
	// leaving it to whatever the series ceiling happens to cut off.
	RequiresTop bool
	// SupportsNamespace marks a query whose expression can carry a Namespace
	// filter. Asking for one elsewhere is refused rather than ignored, so a
	// caller never believes it narrowed an answer that it did not.
	SupportsNamespace bool
	// RequiresComponent names the scrape target this query reads, when it is not
	// the kubelet every install configures. A Cluster without that component
	// returns nothing here, and "nothing" on its own is indistinguishable from an
	// idle Cluster — so the client is told which target the answer depends on
	// rather than left to present an empty chart.
	RequiresComponent string
	// build receives an already-validated label matcher and parameters. It
	// never sees raw client input.
	build func(matcher string, params buildParams) string
}

type buildParams struct {
	Namespace string
	Top       int
	Window    string
}

// Every query here is expressed over metrics one of the three installed scrape
// targets actually reports, and RequiresComponent names which. The catalogue
// and the generated scrape configuration are one decision: a query reading a
// family nothing collects returns nothing with no explanation, and a family
// nothing queries is pure cardinality in storage every Cluster shares. Adding a
// target means adding queries deliberately, not discovering them.
func catalog() []Definition {
	return []Definition{
		{
			Name:  "cluster_cpu_usage",
			Title: "集群 CPU 用量",
			Kind:  KindRange,
			Unit:  UnitMillicores,
			build: func(matcher string, params buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) (rate(node_cpu_usage_seconds_total{%s}[%s])) * 1000`,
					matcher,
					params.Window,
				)
			},
		},
		{
			Name:  "cluster_memory_usage",
			Title: "集群内存用量",
			Kind:  KindRange,
			Unit:  UnitBytes,
			build: func(matcher string, _ buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) (node_memory_working_set_bytes{%s})`,
					matcher,
				)
			},
		},
		{
			Name:        "node_cpu_usage",
			Title:       "节点 CPU 用量",
			Kind:        KindRange,
			Unit:        UnitMillicores,
			Dimensions:  []string{"node"},
			SupportsTop: true,
			build: func(matcher string, params buildParams) string {
				expression := fmt.Sprintf(
					`sum by (zke_cluster_id, node) (rate(node_cpu_usage_seconds_total{%s}[%s])) * 1000`,
					matcher,
					params.Window,
				)
				return topk(expression, params.Top)
			},
		},
		{
			Name:        "node_memory_usage",
			Title:       "节点内存用量",
			Kind:        KindRange,
			Unit:        UnitBytes,
			Dimensions:  []string{"node"},
			SupportsTop: true,
			build: func(matcher string, params buildParams) string {
				expression := fmt.Sprintf(
					`sum by (zke_cluster_id, node) (node_memory_working_set_bytes{%s})`,
					matcher,
				)
				return topk(expression, params.Top)
			},
		},
		{
			Name:              "namespace_cpu_usage",
			Title:             "命名空间 CPU 用量",
			Kind:              KindRange,
			Unit:              UnitMillicores,
			Dimensions:        []string{"namespace"},
			SupportsTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				expression := fmt.Sprintf(
					`sum by (zke_cluster_id, namespace) (rate(pod_cpu_usage_seconds_total{%s}[%s])) * 1000`,
					namespaceSelector(matcher, params.Namespace),
					params.Window,
				)
				return topk(expression, params.Top)
			},
		},
		{
			Name:              "namespace_memory_usage",
			Title:             "命名空间内存用量",
			Kind:              KindRange,
			Unit:              UnitBytes,
			Dimensions:        []string{"namespace"},
			SupportsTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				expression := fmt.Sprintf(
					`sum by (zke_cluster_id, namespace) (pod_memory_working_set_bytes{%s})`,
					namespaceSelector(matcher, params.Namespace),
				)
				return topk(expression, params.Top)
			},
		},
		{
			// Pod level comes from the same kubelet endpoint the Cluster and Node
			// views already read, so it needs no additional scrape target and adds
			// no cardinality to what a Cluster ships. Only the answer is larger,
			// which is what RequiresTop bounds.
			Name:              "pod_cpu_usage",
			Title:             "Pod CPU 用量",
			Kind:              KindRange,
			Unit:              UnitMillicores,
			Dimensions:        []string{"namespace", "pod"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				expression := fmt.Sprintf(
					`sum by (zke_cluster_id, namespace, pod) (rate(pod_cpu_usage_seconds_total{%s}[%s])) * 1000`,
					namespaceSelector(matcher, params.Namespace),
					params.Window,
				)
				return topk(expression, params.Top)
			},
		},
		{
			Name:              "pod_memory_usage",
			Title:             "Pod 内存用量",
			Kind:              KindRange,
			Unit:              UnitBytes,
			Dimensions:        []string{"namespace", "pod"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				expression := fmt.Sprintf(
					`sum by (zke_cluster_id, namespace, pod) (pod_memory_working_set_bytes{%s})`,
					namespaceSelector(matcher, params.Namespace),
				)
				return topk(expression, params.Top)
			},
		},
		// Utilisation. Usage divided by what the Node was given — the number every
		// capacity question actually asks, and the one the kubelet endpoint alone
		// cannot answer, because it reports what is used and never what exists.
		{
			Name:              "cluster_cpu_utilization",
			Title:             "集群 CPU 利用率",
			Kind:              KindRange,
			Unit:              UnitRatio,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) (rate(node_cpu_usage_seconds_total{%s}[%s]))`+
						` / on (zke_cluster_id) `+
						`sum by (zke_cluster_id) (kube_node_status_allocatable{%s,resource="cpu"})`,
					matcher,
					params.Window,
					matcher,
				)
			},
		},
		{
			Name:              "cluster_memory_utilization",
			Title:             "集群内存利用率",
			Kind:              KindRange,
			Unit:              UnitRatio,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, _ buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) (node_memory_working_set_bytes{%s})`+
						` / on (zke_cluster_id) `+
						`sum by (zke_cluster_id) (kube_node_status_allocatable{%s,resource="memory"})`,
					matcher,
					matcher,
				)
			},
		},
		{
			Name:              "node_cpu_utilization",
			Title:             "节点 CPU 利用率",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"node"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, node) (rate(node_cpu_usage_seconds_total{%s}[%s]))`+
						` / on (zke_cluster_id, node) `+
						`sum by (zke_cluster_id, node) (kube_node_status_allocatable{%s,resource="cpu"})`,
					matcher,
					params.Window,
					matcher,
				), params.Top)
			},
		},
		{
			Name:              "node_memory_utilization",
			Title:             "节点内存利用率",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"node"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, node) (node_memory_working_set_bytes{%s})`+
						` / on (zke_cluster_id, node) `+
						`sum by (zke_cluster_id, node) (kube_node_status_allocatable{%s,resource="memory"})`,
					matcher,
					matcher,
				), params.Top)
			},
		},
		// What workloads asked for, against what they use. Requests are what the
		// scheduler reserved and nobody can reclaim, so a Namespace far above its
		// usage is the most common form of wasted capacity in a Cluster.
		{
			Name:              "namespace_cpu_requests",
			Title:             "命名空间 CPU 申请量",
			Kind:              KindRange,
			Unit:              UnitMillicores,
			Dimensions:        []string{"namespace"},
			SupportsTop:       true,
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, namespace) `+
						`(kube_pod_container_resource_requests{%s,resource="cpu"}) * 1000`,
					namespaceSelector(matcher, params.Namespace),
				), params.Top)
			},
		},
		{
			Name:              "namespace_memory_requests",
			Title:             "命名空间内存申请量",
			Kind:              KindRange,
			Unit:              UnitBytes,
			Dimensions:        []string{"namespace"},
			SupportsTop:       true,
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, namespace) `+
						`(kube_pod_container_resource_requests{%s,resource="memory"})`,
					namespaceSelector(matcher, params.Namespace),
				), params.Top)
			},
		},
		{
			Name:              "namespace_cpu_limits",
			Title:             "命名空间 CPU 限制量",
			Kind:              KindRange,
			Unit:              UnitMillicores,
			Dimensions:        []string{"namespace"},
			SupportsTop:       true,
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, namespace) `+
						`(kube_pod_container_resource_limits{%s,resource="cpu"}) * 1000`,
					namespaceSelector(matcher, params.Namespace),
				), params.Top)
			},
		},
		{
			Name:              "namespace_memory_limits",
			Title:             "命名空间内存限制量",
			Kind:              KindRange,
			Unit:              UnitBytes,
			Dimensions:        []string{"namespace"},
			SupportsTop:       true,
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, namespace) `+
						`(kube_pod_container_resource_limits{%s,resource="memory"})`,
					namespaceSelector(matcher, params.Namespace),
				), params.Top)
			},
		},
		// Workload level. Pod usage rolled up through the controller that owns the
		// Pod, which is the unit an operator actually deploys and scales.
		{
			Name:              "workload_cpu_usage",
			Title:             "工作负载 CPU 用量",
			Kind:              KindRange,
			Unit:              UnitMillicores,
			Dimensions:        []string{"namespace", "workload_kind", "workload"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				selector := namespaceSelector(matcher, params.Namespace)
				usage := fmt.Sprintf(
					`sum by (zke_cluster_id, namespace, pod) `+
						`(rate(pod_cpu_usage_seconds_total{%s}[%s])) * 1000`,
					selector,
					params.Window,
				)
				return topk(workloadRollup(usage, selector), params.Top)
			},
		},
		{
			Name:              "workload_memory_usage",
			Title:             "工作负载内存用量",
			Kind:              KindRange,
			Unit:              UnitBytes,
			Dimensions:        []string{"namespace", "workload_kind", "workload"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				selector := namespaceSelector(matcher, params.Namespace)
				usage := fmt.Sprintf(
					`sum by (zke_cluster_id, namespace, pod) `+
						`(pod_memory_working_set_bytes{%s})`,
					selector,
				)
				return topk(workloadRollup(usage, selector), params.Top)
			},
		},
		{
			Name:              "pod_restarts",
			Title:             "Pod 重启次数",
			Kind:              KindRange,
			Unit:              UnitCount,
			Dimensions:        []string{"namespace", "pod"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				// increase() over the window rather than the raw counter: the
				// counter's absolute value is dominated by however long the Pod
				// has existed, which says nothing about whether it is restarting
				// now.
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, namespace, pod) `+
						`(increase(kube_pod_container_status_restarts_total{%s}[%s]))`,
					namespaceSelector(matcher, params.Namespace),
					params.Window,
				), params.Top)
			},
		},
		// Node disk and network. Nothing else in the pipeline reports them: the
		// kubelet resource endpoint covers CPU and memory and stops there.
		{
			Name:              "node_filesystem_utilization",
			Title:             "节点文件系统使用率",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"node", "mountpoint"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`1 - (sum by (zke_cluster_id, node, mountpoint) `+
						`(node_filesystem_avail_bytes{%s})`+
						` / on (zke_cluster_id, node, mountpoint) `+
						`sum by (zke_cluster_id, node, mountpoint) `+
						`(node_filesystem_size_bytes{%s}))`,
					matcher,
					matcher,
				), params.Top)
			},
		},
		{
			Name:              "node_network_receive",
			Title:             "节点网络接收",
			Kind:              KindRange,
			Unit:              UnitBytesPerSecond,
			Dimensions:        []string{"node", "device"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, node, device) `+
						`(rate(node_network_receive_bytes_total{%s}[%s]))`,
					matcher,
					params.Window,
				), params.Top)
			},
		},
		{
			Name:              "node_network_transmit",
			Title:             "节点网络发送",
			Kind:              KindRange,
			Unit:              UnitBytesPerSecond,
			Dimensions:        []string{"node", "device"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, node, device) `+
						`(rate(node_network_transmit_bytes_total{%s}[%s]))`,
					matcher,
					params.Window,
				), params.Top)
			},
		},
		{
			Name:              "node_disk_read",
			Title:             "节点磁盘读取",
			Kind:              KindRange,
			Unit:              UnitBytesPerSecond,
			Dimensions:        []string{"node", "device"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, node, device) `+
						`(rate(node_disk_read_bytes_total{%s}[%s]))`,
					matcher,
					params.Window,
				), params.Top)
			},
		},
		{
			Name:              "node_disk_write",
			Title:             "节点磁盘写入",
			Kind:              KindRange,
			Unit:              UnitBytesPerSecond,
			Dimensions:        []string{"node", "device"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, node, device) `+
						`(rate(node_disk_written_bytes_total{%s}[%s]))`,
					matcher,
					params.Window,
				), params.Top)
			},
		},
		{
			Name:  "collection_health",
			Title: "采集健康度",
			Kind:  KindInstant,
			Unit:  UnitRatio,
			build: func(matcher string, _ buildParams) string {
				// The fraction of this Cluster's scrape targets that answered
				// their last scrape. A Cluster missing from the answer entirely
				// has sent nothing recently, which the caller must show as
				// "no data" rather than as zero.
				return fmt.Sprintf(
					`avg by (zke_cluster_id) (up{%s})`,
					matcher,
				)
			},
		},
	}
}

// workloadRollup sums a per-Pod expression by the controller that owns each Pod.
//
// Two levels, because Kubernetes has two. A StatefulSet, DaemonSet or Job owns
// its Pods directly, so its name is already on `kube_pod_owner`. A Deployment
// does not: it owns a ReplicaSet which owns the Pods, so `kube_pod_owner` names
// the ReplicaSet and the Deployment has to be found through
// `kube_replicaset_owner`. Reporting the ReplicaSet instead would show an
// operator a different workload after every rollout of the same Deployment.
//
// The mapping vector is reduced with `max by` before the join. Each Pod has
// exactly one owner series today, but a join whose right side is not provably
// unique fails the whole query at evaluation time rather than returning a
// slightly wrong number — and this template runs on data from Clusters this
// Server does not control.
func workloadRollup(usage string, selector string) string {
	// Deployments: rename kube_pod_owner's owner_name to `replicaset` so it can
	// be matched against kube_replicaset_owner, then carry that ReplicaSet's own
	// owner down as the workload.
	viaReplicaSet := fmt.Sprintf(
		`label_replace(label_replace(`+
			`label_replace(kube_pod_owner{%s,owner_kind="ReplicaSet"}, `+
			`"replicaset", "$1", "owner_name", "(.*)")`+
			` * on (zke_cluster_id, namespace, replicaset) group_left(deployment) `+
			`label_replace(kube_replicaset_owner{%s}, "deployment", "$1", "owner_name", "(.*)")`+
			`, "workload", "$1", "deployment", "(.*)")`+
			`, "workload_kind", "Deployment", "namespace", ".*")`,
		selector,
		selector,
	)
	// Everything else: the owner on the Pod is already the workload. Pods with
	// no owner at all are excluded — a bare Pod is not a workload, and grouping
	// them under an empty name would produce one meaningless series per
	// Namespace.
	direct := fmt.Sprintf(
		`label_replace(label_replace(`+
			`kube_pod_owner{%s,owner_kind!="ReplicaSet",owner_kind!=""}`+
			`, "workload", "$1", "owner_name", "(.*)")`+
			`, "workload_kind", "$1", "owner_kind", "(.*)")`,
		selector,
	)
	owners := fmt.Sprintf(
		`max by (zke_cluster_id, namespace, pod, workload, workload_kind) ((%s) or (%s))`,
		viaReplicaSet,
		direct,
	)
	return fmt.Sprintf(
		`sum by (zke_cluster_id, namespace, workload_kind, workload) (`+
			`(%s) * on (zke_cluster_id, namespace, pod) `+
			`group_left(workload, workload_kind) (%s))`,
		usage,
		owners,
	)
}

// namespaceSelector narrows a matcher to one Namespace. The value is a
// validated DNS label by the time it arrives, so it cannot close the selector
// and open something else.
func namespaceSelector(matcher string, namespace string) string {
	if namespace == "" {
		return matcher
	}
	return fmt.Sprintf(`%s,namespace="%s"`, matcher, namespace)
}

func topk(expression string, top int) string {
	if top <= 0 {
		return expression
	}
	return fmt.Sprintf("topk(%d, %s)", top, expression)
}

// Catalog reports the queries this Server offers.
func Catalog() []Definition {
	return catalog()
}

func lookup(name string) (Definition, bool) {
	for _, definition := range catalog() {
		if definition.Name == strings.TrimSpace(name) {
			return definition, true
		}
	}
	return Definition{}, false
}
