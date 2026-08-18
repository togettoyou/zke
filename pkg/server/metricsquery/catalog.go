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
	UnitOpsPerSecond   Unit = "ops_per_second"
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
		{
			// Container level, from the same kubelet endpoint as everything
			// above. A Pod is a group, not a process: one of its containers is
			// usually the one consuming, and a Pod-level curve cannot say which.
			Name:              "container_cpu_usage",
			Title:             "容器 CPU 用量",
			Kind:              KindRange,
			Unit:              UnitMillicores,
			Dimensions:        []string{"namespace", "pod", "container"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, namespace, pod, container) `+
						`(rate(container_cpu_usage_seconds_total{%s,container!=""}[%s])) * 1000`,
					namespaceSelector(matcher, params.Namespace),
					params.Window,
				), params.Top)
			},
		},
		{
			Name:              "container_memory_usage",
			Title:             "容器内存用量",
			Kind:              KindRange,
			Unit:              UnitBytes,
			Dimensions:        []string{"namespace", "pod", "container"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, namespace, pod, container) `+
						`(container_memory_working_set_bytes{%s,container!=""})`,
					namespaceSelector(matcher, params.Namespace),
				), params.Top)
			},
		},
		{
			// The one saturation signal no usage curve contains. A container
			// held at its CPU limit uses exactly what it was allowed and looks
			// perfectly healthy; the periods it spent waiting for the next
			// quota window are the only evidence that it is being slowed down.
			Name:              "container_cpu_throttling",
			Title:             "容器 CPU 限流比例",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"namespace", "pod", "container"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				selector := namespaceSelector(matcher, params.Namespace)
				// The denominator is guarded rather than divided blindly: a
				// container with no CPU limit reports no periods at all, and an
				// unguarded division would answer NaN for every one of them.
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, namespace, pod, container) `+
						`(rate(container_cpu_cfs_throttled_periods_total{%s,container!=""}[%s]))`+
						` / on (zke_cluster_id, namespace, pod, container) `+
						`(sum by (zke_cluster_id, namespace, pod, container) `+
						`(rate(container_cpu_cfs_periods_total{%s,container!=""}[%s])) > 0)`,
					selector,
					params.Window,
					selector,
					params.Window,
				), params.Top)
			},
		},
		{
			// Per-Pod network, which the Node-level device curves cannot
			// attribute. A saturated uplink is a Node fact; which workload
			// filled it is the question that follows, and it is answered here.
			Name:              "pod_network_receive",
			Title:             "Pod 网络接收",
			Kind:              KindRange,
			Unit:              UnitBytesPerSecond,
			Dimensions:        []string{"namespace", "pod"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, namespace, pod) `+
						`(rate(container_network_receive_bytes_total{%s,pod!=""}[%s]))`,
					namespaceSelector(matcher, params.Namespace),
					params.Window,
				), params.Top)
			},
		},
		{
			Name:              "pod_network_transmit",
			Title:             "Pod 网络发送",
			Kind:              KindRange,
			Unit:              UnitBytesPerSecond,
			Dimensions:        []string{"namespace", "pod"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, namespace, pod) `+
						`(rate(container_network_transmit_bytes_total{%s,pod!=""}[%s]))`,
					namespaceSelector(matcher, params.Namespace),
					params.Window,
				), params.Top)
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
		// Cluster capacity and commitment. Usage answers "what is running";
		// these answer "what is left", which is the question that decides whether
		// the next workload can be scheduled at all. Requests are the number the
		// scheduler actually enforces, so a Cluster can be fully committed while
		// every node sits idle — a state no usage curve shows.
		{
			Name:              "cluster_cpu_requests",
			Title:             "集群 CPU 申请量",
			Kind:              KindRange,
			Unit:              UnitMillicores,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, _ buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) `+
						`(kube_pod_container_resource_requests{%s,resource="cpu"}) * 1000`,
					matcher,
				)
			},
		},
		{
			Name:              "cluster_memory_requests",
			Title:             "集群内存申请量",
			Kind:              KindRange,
			Unit:              UnitBytes,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, _ buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) `+
						`(kube_pod_container_resource_requests{%s,resource="memory"})`,
					matcher,
				)
			},
		},
		{
			Name:              "cluster_cpu_limits",
			Title:             "集群 CPU 限制量",
			Kind:              KindRange,
			Unit:              UnitMillicores,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, _ buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) `+
						`(kube_pod_container_resource_limits{%s,resource="cpu"}) * 1000`,
					matcher,
				)
			},
		},
		{
			Name:              "cluster_memory_limits",
			Title:             "集群内存限制量",
			Kind:              KindRange,
			Unit:              UnitBytes,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, _ buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) `+
						`(kube_pod_container_resource_limits{%s,resource="memory"})`,
					matcher,
				)
			},
		},
		{
			Name:              "cluster_cpu_allocatable",
			Title:             "集群 CPU 可分配量",
			Kind:              KindRange,
			Unit:              UnitMillicores,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, _ buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) `+
						`(kube_node_status_allocatable{%s,resource="cpu"}) * 1000`,
					matcher,
				)
			},
		},
		{
			Name:              "cluster_memory_allocatable",
			Title:             "集群内存可分配量",
			Kind:              KindRange,
			Unit:              UnitBytes,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, _ buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) `+
						`(kube_node_status_allocatable{%s,resource="memory"})`,
					matcher,
				)
			},
		},
		{
			// Above 1 the Cluster has promised more than it has. That is not by
			// itself a fault — most Clusters run committed above their usage on
			// purpose — but it is the line past which a Node failure has nowhere
			// to reschedule to.
			Name:              "cluster_cpu_commitment",
			Title:             "集群 CPU 申请占比",
			Kind:              KindRange,
			Unit:              UnitRatio,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, _ buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) `+
						`(kube_pod_container_resource_requests{%s,resource="cpu"})`+
						` / on (zke_cluster_id) `+
						`sum by (zke_cluster_id) (kube_node_status_allocatable{%s,resource="cpu"})`,
					matcher,
					matcher,
				)
			},
		},
		{
			Name:              "cluster_memory_commitment",
			Title:             "集群内存申请占比",
			Kind:              KindRange,
			Unit:              UnitRatio,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, _ buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) `+
						`(kube_pod_container_resource_requests{%s,resource="memory"})`+
						` / on (zke_cluster_id) `+
						`sum by (zke_cluster_id) (kube_node_status_allocatable{%s,resource="memory"})`,
					matcher,
					matcher,
				)
			},
		},
		{
			// The denominator the load average has no unit without. A run queue
			// of 8 is idle on a 64-core Node and a crisis on a 2-core one, so
			// the core count is drawn beside it rather than left to the reader.
			Name:              "node_cpu_cores",
			Title:             "节点 CPU 核数",
			Kind:              KindRange,
			Unit:              UnitCount,
			Dimensions:        []string{"node"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`max by (zke_cluster_id, node) `+
						`(kube_node_status_allocatable{%s,resource="cpu"})`,
					matcher,
				), params.Top)
			},
		},
		// Node saturation. Utilisation says how much of a Node is in use; these
		// say whether it is keeping up. A Node at 60% CPU with a run queue of 40
		// is a Node whose workloads are waiting, and no usage curve shows that.
		{
			Name:              "node_load1",
			Title:             "节点 1 分钟负载",
			Kind:              KindRange,
			Unit:              UnitCount,
			Dimensions:        []string{"node"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				// max rather than sum: one exporter reports per Node, and a second
				// one during a rollout would otherwise double the number.
				return topk(fmt.Sprintf(
					`max by (zke_cluster_id, node) (node_load1{%s})`,
					matcher,
				), params.Top)
			},
		},
		{
			Name:              "node_cpu_iowait",
			Title:             "节点 CPU I/O 等待",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"node"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				// Divided by the Node's own core count, which is the number of
				// `idle` series it reports. Without the divisor the same storage
				// stall reads differently on a 4-core and a 64-core Node.
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, node) `+
						`(rate(node_cpu_seconds_total{%s,mode="iowait"}[%s]))`+
						` / on (zke_cluster_id, node) `+
						`count by (zke_cluster_id, node) `+
						`(node_cpu_seconds_total{%s,mode="idle"})`,
					matcher,
					params.Window,
					matcher,
				), params.Top)
			},
		},
		{
			Name:              "node_memory_available",
			Title:             "节点可用内存",
			Kind:              KindRange,
			Unit:              UnitBytes,
			Dimensions:        []string{"node"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				// The kernel's own estimate of what a new allocation could get,
				// which is not free memory: page cache is reclaimable and counts.
				return topk(fmt.Sprintf(
					`max by (zke_cluster_id, node) (node_memory_MemAvailable_bytes{%s})`,
					matcher,
				), params.Top)
			},
		},
		{
			// The table every connection has to fit in. A Node whose conntrack
			// table is full drops new connections while every byte counter on it
			// stays ordinary — the traffic that fails is the traffic that never
			// started.
			Name:              "node_conntrack_utilization",
			Title:             "节点连接跟踪表使用率",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"node"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`max by (zke_cluster_id, node) (node_nf_conntrack_entries{%s})`+
						` / on (zke_cluster_id, node) `+
						`(max by (zke_cluster_id, node) `+
						`(node_nf_conntrack_entries_limit{%s}) > 0)`,
					matcher,
					matcher,
				), params.Top)
			},
		},
		{
			// Retransmissions as a share of what was sent, not as a count: a
			// busy Node retransmits more segments than an idle one over a link
			// of the same quality, and only the ratio compares across Nodes.
			Name:              "node_tcp_retransmission",
			Title:             "节点 TCP 重传比例",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"node"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, node) `+
						`(rate(node_netstat_Tcp_RetransSegs{%s}[%s]))`+
						` / on (zke_cluster_id, node) `+
						`(sum by (zke_cluster_id, node) `+
						`(rate(node_netstat_Tcp_OutSegs{%s}[%s])) > 0)`,
					matcher,
					params.Window,
					matcher,
					params.Window,
				), params.Top)
			},
		},
		{
			// Connections the Node accepted nowhere. Both counters mean the same
			// thing to whoever was connecting — the handshake completed and the
			// listener never took it — so they are added rather than separated.
			Name:              "node_tcp_listen_drops",
			Title:             "节点连接队列丢弃",
			Kind:              KindRange,
			Unit:              UnitOpsPerSecond,
			Dimensions:        []string{"node"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				term := func(name string) string {
					return fmt.Sprintf(
						`sum by (zke_cluster_id, node) (rate(%s{%s}[%s]))`,
						name,
						matcher,
						params.Window,
					)
				}
				return topk(
					term("node_netstat_TcpExt_ListenDrops")+" + "+
						term("node_netstat_TcpExt_ListenOverflows"),
					params.Top,
				)
			},
		},
		{
			// Pressure stall information: the share of time at least one task
			// was waiting for the resource rather than running. It is the only
			// signal here that measures the delay itself instead of the level
			// something sits at — a Node can stall on memory reclaim while its
			// available memory looks unremarkable.
			//
			// The kernel must expose /proc/pressure. On an older one this
			// answers nothing, which the client presents as no data rather than
			// as no pressure.
			Name:              "node_pressure_cpu",
			Title:             "节点 CPU 压力",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"node"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				return topk(pressureStall(
					"node_pressure_cpu_waiting_seconds_total", matcher, params.Window,
				), params.Top)
			},
		},
		{
			Name:              "node_pressure_memory",
			Title:             "节点内存压力",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"node"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				return topk(pressureStall(
					"node_pressure_memory_waiting_seconds_total", matcher, params.Window,
				), params.Top)
			},
		},
		{
			Name:              "node_pressure_io",
			Title:             "节点 I/O 压力",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"node"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				return topk(pressureStall(
					"node_pressure_io_waiting_seconds_total", matcher, params.Window,
				), params.Top)
			},
		},
		{
			Name:              "node_pod_count",
			Title:             "节点 Pod 数量",
			Kind:              KindRange,
			Unit:              UnitCount,
			Dimensions:        []string{"node"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`count by (zke_cluster_id, node) (kube_pod_info{%s,node!=""})`,
					matcher,
				), params.Top)
			},
		},
		{
			// The third capacity nobody watches until it runs out. A Node with
			// spare CPU and memory still refuses Pods once it holds 110 of them,
			// and the failure reads as "no nodes available" with no number behind
			// it.
			Name:              "node_pod_utilization",
			Title:             "节点 Pod 密度",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"node"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`count by (zke_cluster_id, node) (kube_pod_info{%s,node!=""})`+
						` / on (zke_cluster_id, node) `+
						`sum by (zke_cluster_id, node) `+
						`(kube_node_status_capacity{%s,resource="pods"})`,
					matcher,
					matcher,
				), params.Top)
			},
		},
		{
			Name:              "namespace_pod_count",
			Title:             "命名空间 Pod 数量",
			Kind:              KindRange,
			Unit:              UnitCount,
			Dimensions:        []string{"namespace"},
			SupportsTop:       true,
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, namespace) `+
						`(kube_pod_status_phase{%s,phase="Running"})`,
					namespaceSelector(matcher, params.Namespace),
				), params.Top)
			},
		},
		{
			// The limit a Namespace reaches before the Cluster reaches any of
			// its own. A full quota refuses every new Pod while the Nodes behind
			// it sit half idle, and no usage curve in this application shows
			// that — the workloads that would have used the capacity were never
			// created.
			Name:              "namespace_quota_utilization",
			Title:             "命名空间配额使用率",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"namespace", "resource"},
			SupportsTop:       true,
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				selector := namespaceSelector(matcher, params.Namespace)
				used := fmt.Sprintf(
					`sum by (zke_cluster_id, namespace, resourcequota, resource) `+
						`(kube_resourcequota{%s,type="used"})`,
					selector,
				)
				hard := fmt.Sprintf(
					`sum by (zke_cluster_id, namespace, resourcequota, resource) `+
						`(kube_resourcequota{%s,type="hard"})`,
					selector,
				)
				// Per quota object first, then the worst one per resource: a
				// Namespace may carry several ResourceQuotas, and the one it is
				// about to hit is the answer, not their average.
				return topk(fmt.Sprintf(
					`max by (zke_cluster_id, namespace, resource) ((%s)`+
						` / on (zke_cluster_id, namespace, resourcequota, resource) ((%s) > 0))`,
					used,
					hard,
				), params.Top)
			},
		},
		// Kubernetes object state. Everything above measures consumption; these
		// measure whether the Cluster is doing what it was told. The live resource
		// views answer the same questions for "now" — only a series can say when it
		// started.
		{
			Name:              "cluster_pod_phase",
			Title:             "Pod 状态分布",
			Kind:              KindRange,
			Unit:              UnitCount,
			Dimensions:        []string{"phase"},
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id, phase) (kube_pod_status_phase{%s})`,
					namespaceSelector(matcher, params.Namespace),
				)
			},
		},
		{
			Name:              "cluster_node_readiness",
			Title:             "节点就绪状态",
			Kind:              KindRange,
			Unit:              UnitCount,
			Dimensions:        []string{"status"},
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, _ buildParams) string {
				// Every Node reports a series per status value, valued 1 for the
				// one it is in, so summing by status counts Nodes per state and
				// the three add up to the Cluster's Node count.
				return fmt.Sprintf(
					`sum by (zke_cluster_id, status) `+
						`(kube_node_status_condition{%s,condition="Ready"})`,
					matcher,
				)
			},
		},
		{
			Name:              "cluster_node_pressure",
			Title:             "节点压力状态",
			Kind:              KindRange,
			Unit:              UnitCount,
			Dimensions:        []string{"condition"},
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, _ buildParams) string {
				// Only the Nodes currently under each pressure. A flat zero line is
				// the answer an operator wants to see, and it is only readable as
				// an answer because the series exists.
				return fmt.Sprintf(
					`sum by (zke_cluster_id, condition) (kube_node_status_condition{%s,`+
						`condition=~"MemoryPressure|DiskPressure|PIDPressure",status="true"})`,
					matcher,
				)
			},
		},
		{
			Name:              "cluster_container_restarts",
			Title:             "集群容器重启次数",
			Kind:              KindRange,
			Unit:              UnitCount,
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id) `+
						`(increase(kube_pod_container_status_restarts_total{%s}[%s]))`,
					namespaceSelector(matcher, params.Namespace),
					params.Window,
				)
			},
		},
		{
			// What the containers that are not running are waiting for. The
			// restart curve above says something is wrong; this says whether it
			// is an image that cannot be pulled, a configuration that cannot be
			// resolved, or a process that keeps dying — three faults with three
			// different fixes.
			//
			// Restricted to the reasons an operator acts on. kube-state reports
			// a series for every reason it knows, most of them permanently zero,
			// and a chart of twenty flat lines answers nothing.
			Name:              "pod_container_waiting",
			Title:             "容器等待原因",
			Kind:              KindRange,
			Unit:              UnitCount,
			Dimensions:        []string{"reason"},
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id, reason) `+
						`(kube_pod_container_status_waiting_reason{%s,reason=~"%s"})`,
					namespaceSelector(matcher, params.Namespace),
					waitingReasons,
				)
			},
		},
		{
			// Why the containers that did stop, stopped. OOMKilled is the one
			// this exists for: it is invisible in every usage curve, because a
			// container killed for exceeding its memory limit stops reporting
			// the moment it is killed.
			Name:              "pod_container_terminated",
			Title:             "容器退出原因",
			Kind:              KindRange,
			Unit:              UnitCount,
			Dimensions:        []string{"reason"},
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return fmt.Sprintf(
					`sum by (zke_cluster_id, reason) `+
						`(kube_pod_container_status_last_terminated_reason{%s,reason=~"%s"})`,
					namespaceSelector(matcher, params.Namespace),
					terminatedReasons,
				)
			},
		},
		{
			// Desired minus ready, across the three controllers that promise a
			// replica count. A Deployment reporting 3/3 and one reporting 1/3 are
			// the same green tick in a list view; this is the number that separates
			// them, and topk puts the second one first.
			Name:              "workload_replicas_unavailable",
			Title:             "工作负载未就绪副本",
			Kind:              KindRange,
			Unit:              UnitCount,
			Dimensions:        []string{"namespace", "workload_kind", "workload"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				selector := namespaceSelector(matcher, params.Namespace)
				return topk(replicaShortfall(selector), params.Top)
			},
		},
		{
			// Desired and ready as two curves rather than one difference. The
			// shortfall above answers "is something missing"; these answer what
			// happened — a workload that was scaled up, a rollout replacing its
			// replicas, or a controller that lost them without being asked to
			// change anything.
			Name:              "workload_replicas_desired",
			Title:             "工作负载期望副本",
			Kind:              KindRange,
			Unit:              UnitCount,
			Dimensions:        []string{"namespace", "workload_kind", "workload"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return topk(replicaUnion(
					namespaceSelector(matcher, params.Namespace),
					desiredReplicaFamilies,
				), params.Top)
			},
		},
		{
			Name:              "workload_replicas_ready",
			Title:             "工作负载就绪副本",
			Kind:              KindRange,
			Unit:              UnitCount,
			Dimensions:        []string{"namespace", "workload_kind", "workload"},
			SupportsTop:       true,
			RequiresTop:       true,
			SupportsNamespace: true,
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, params buildParams) string {
				return topk(replicaUnion(
					namespaceSelector(matcher, params.Namespace),
					readyReplicaFamilies,
				), params.Top)
			},
		},
		// Disk and network saturation, beside the throughput queries above.
		// Bytes per second says how much is moving; these say whether the device
		// or the filesystem is about to stop it.
		{
			Name:              "node_filesystem_inode_utilization",
			Title:             "节点 inode 使用率",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"node", "mountpoint"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				// A filesystem with free bytes and no free inodes fails every
				// write, and the disk usage chart shows nothing wrong.
				return topk(fmt.Sprintf(
					`1 - (sum by (zke_cluster_id, node, mountpoint) `+
						`(node_filesystem_files_free{%s})`+
						` / on (zke_cluster_id, node, mountpoint) `+
						`sum by (zke_cluster_id, node, mountpoint) `+
						`(node_filesystem_files{%s}))`,
					matcher,
					matcher,
				), params.Top)
			},
		},
		{
			// PersistentVolumeClaim fullness, reported by the kubelet that
			// mounted it. Nothing else in the pipeline knows it: kube-state
			// knows the claim exists and how much was requested, and the Node's
			// own filesystem series describe the disk under it, not the claim.
			//
			// `max` rather than `sum`: a ReadWriteMany claim is reported by every
			// kubelet that mounted it, and adding those up would report a volume
			// as several times its own size.
			Name:              "pvc_utilization",
			Title:             "PVC 使用率",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"namespace", "persistentvolumeclaim"},
			SupportsTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				selector := namespaceSelector(matcher, params.Namespace)
				return topk(fmt.Sprintf(
					`max by (zke_cluster_id, namespace, persistentvolumeclaim) `+
						`(kubelet_volume_stats_used_bytes{%s})`+
						` / on (zke_cluster_id, namespace, persistentvolumeclaim) `+
						`(max by (zke_cluster_id, namespace, persistentvolumeclaim) `+
						`(kubelet_volume_stats_capacity_bytes{%s}) > 0)`,
					selector,
					selector,
				), params.Top)
			},
		},
		{
			Name:              "pvc_used_bytes",
			Title:             "PVC 已用空间",
			Kind:              KindRange,
			Unit:              UnitBytes,
			Dimensions:        []string{"namespace", "persistentvolumeclaim"},
			SupportsTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`max by (zke_cluster_id, namespace, persistentvolumeclaim) `+
						`(kubelet_volume_stats_used_bytes{%s})`,
					namespaceSelector(matcher, params.Namespace),
				), params.Top)
			},
		},
		{
			// The same distinction the Node filesystem view makes, one level up:
			// a claim can run out of inodes with most of its bytes unused, and
			// the writes fail with a message about disk space either way.
			Name:              "pvc_inode_utilization",
			Title:             "PVC inode 使用率",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"namespace", "persistentvolumeclaim"},
			SupportsTop:       true,
			SupportsNamespace: true,
			build: func(matcher string, params buildParams) string {
				selector := namespaceSelector(matcher, params.Namespace)
				return topk(fmt.Sprintf(
					`max by (zke_cluster_id, namespace, persistentvolumeclaim) `+
						`(kubelet_volume_stats_inodes_used{%s})`+
						` / on (zke_cluster_id, namespace, persistentvolumeclaim) `+
						`(max by (zke_cluster_id, namespace, persistentvolumeclaim) `+
						`(kubelet_volume_stats_inodes{%s}) > 0)`,
					selector,
					selector,
				), params.Top)
			},
		},
		{
			Name:              "node_disk_read_ops",
			Title:             "节点磁盘读 IOPS",
			Kind:              KindRange,
			Unit:              UnitOpsPerSecond,
			Dimensions:        []string{"node", "device"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, node, device) `+
						`(rate(node_disk_reads_completed_total{%s}[%s]))`,
					matcher,
					params.Window,
				), params.Top)
			},
		},
		{
			Name:              "node_disk_write_ops",
			Title:             "节点磁盘写 IOPS",
			Kind:              KindRange,
			Unit:              UnitOpsPerSecond,
			Dimensions:        []string{"node", "device"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, node, device) `+
						`(rate(node_disk_writes_completed_total{%s}[%s]))`,
					matcher,
					params.Window,
				), params.Top)
			},
		},
		{
			Name:              "node_disk_io_utilization",
			Title:             "节点磁盘繁忙度",
			Kind:              KindRange,
			Unit:              UnitRatio,
			Dimensions:        []string{"node", "device"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				// The fraction of wall time the device had a request in flight.
				// A device at 100% is the reason everything on that Node is slow,
				// whatever its throughput happens to be.
				return topk(fmt.Sprintf(
					`sum by (zke_cluster_id, node, device) `+
						`(rate(node_disk_io_time_seconds_total{%s}[%s]))`,
					matcher,
					params.Window,
				), params.Top)
			},
		},
		{
			Name:              "node_network_errors",
			Title:             "节点网络错误与丢包",
			Kind:              KindRange,
			Unit:              UnitOpsPerSecond,
			Dimensions:        []string{"node", "device"},
			SupportsTop:       true,
			RequiresComponent: observability.ComponentNodeExporter,
			build: func(matcher string, params buildParams) string {
				// Both directions and both kinds in one number: an operator asks
				// "is this interface dropping packets", not which counter moved.
				// Each term is reduced by the same grouping first, so a device
				// missing one counter does not void the sum.
				term := func(name string) string {
					return fmt.Sprintf(
						`sum by (zke_cluster_id, node, device) (rate(%s{%s}[%s]))`,
						name,
						matcher,
						params.Window,
					)
				}
				return topk(strings.Join([]string{
					term("node_network_receive_errs_total"),
					term("node_network_transmit_errs_total"),
					term("node_network_receive_drop_total"),
					term("node_network_transmit_drop_total"),
				}, " + "), params.Top)
			},
		},
		{
			// One request for the whole headline row. Each number here is a count
			// over an object family, cheap on its own but a separate round trip to
			// storage every time the window moves; the Console draws six of them
			// side by side, so they travel together under a `resource` label.
			Name:              "cluster_inventory",
			Title:             "集群对象概览",
			Kind:              KindInstant,
			Unit:              UnitCount,
			Dimensions:        []string{"resource"},
			RequiresComponent: observability.ComponentKubeState,
			build: func(matcher string, _ buildParams) string {
				return clusterInventory(matcher)
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

// waitingReasons and terminatedReasons bound the container state charts to the
// states an operator can act on.
//
// Read from the shared list the Agent filters its scrape with, so the two
// cannot drift: a reason queried here but dropped there is an empty chart, and
// one kept there but never queried is cardinality nobody reads.
var (
	waitingReasons    = strings.Join(observability.ContainerWaitingReasons, "|")
	terminatedReasons = strings.Join(observability.ContainerTerminatedReasons, "|")
)

// The three controllers that promise a replica count, and the family each one
// reports its desired and its ready number in.
var (
	desiredReplicaFamilies = [3]string{
		"kube_deployment_status_replicas",
		"kube_statefulset_status_replicas",
		"kube_daemonset_status_desired_number_scheduled",
	}
	readyReplicaFamilies = [3]string{
		"kube_deployment_status_replicas_available",
		"kube_statefulset_status_replicas_ready",
		"kube_daemonset_status_number_ready",
	}
)

// pressureStall reads one PSI counter as the share of wall clock time something
// spent waiting for a resource. The counter accumulates stall time, so its rate
// is already that share; `max` keeps a second exporter during a rollout from
// doubling it.
func pressureStall(name string, matcher string, window string) string {
	return fmt.Sprintf(
		`max by (zke_cluster_id, node) (rate(%s{%s}[%s]))`,
		name,
		matcher,
		window,
	)
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

// replicaShortfall reports how many replicas each workload is missing.
//
// Desired minus ready, across the three controllers that promise a replica
// count. A Deployment reporting 3/3 and one reporting 1/3 are the same green
// tick in a list view; this is the number that separates them, and topk puts
// the second one first.
//
// Clamped at zero. A controller mid-rollout can report more ready than desired
// for a moment, and a negative shortfall ranked by topk would put a healthy
// workload nowhere while reading as a defect wherever it did appear.
func replicaShortfall(selector string) string {
	return fmt.Sprintf(
		`clamp_min((%s) - (%s), 0)`,
		replicaUnion(selector, desiredReplicaFamilies),
		replicaUnion(selector, readyReplicaFamilies),
	)
}

// replicaUnion normalises one number reported by three controllers onto one set
// of labels.
//
// Each controller names its object with a different label, so all three are
// rewritten onto `workload` and `workload_kind` — the same two labels the usage
// rollup produces, so a reader moving between the views sees one identity for
// one workload. Jobs and CronJobs are deliberately absent: a Job that has
// finished is not a workload missing replicas.
func replicaUnion(selector string, families [3]string) string {
	branches := make([]string, 0, 3)
	for index, kind := range [3]struct{ label, kind string }{
		{"deployment", "Deployment"},
		{"statefulset", "StatefulSet"},
		{"daemonset", "DaemonSet"},
	} {
		branches = append(branches, fmt.Sprintf(
			`label_replace(label_replace(%s{%s}, "workload", "$1", "%s", "(.*)")`+
				`, "workload_kind", "%s", "namespace", ".*")`,
			families[index],
			selector,
			kind.label,
			kind.kind,
		))
	}
	return fmt.Sprintf(
		`sum by (zke_cluster_id, namespace, workload_kind, workload) ((%s))`,
		strings.Join(branches, ") or ("),
	)
}

// clusterInventory counts the object families behind the headline row.
//
// Each count carries its own `resource` label so the union returns one series
// per number instead of colliding on an identical label set. The alternative —
// one query per tile — is six round trips to shared storage every time the
// window moves, for six numbers that are always read together.
func clusterInventory(matcher string) string {
	entries := []struct {
		resource   string
		expression string
	}{
		// Node count and ready count come from the same family: every Node
		// reports the Ready condition, valued 1 only when it holds, so counting
		// the series gives the Nodes and summing them gives the ready ones.
		{"node", fmt.Sprintf(
			`count by (zke_cluster_id) `+
				`(kube_node_status_condition{%s,condition="Ready",status="true"})`,
			matcher,
		)},
		{"node_ready", fmt.Sprintf(
			`sum by (zke_cluster_id) `+
				`(kube_node_status_condition{%s,condition="Ready",status="true"})`,
			matcher,
		)},
		{"pod_running", phaseCount(matcher, "Running")},
		{"pod_pending", phaseCount(matcher, "Pending")},
		{"pod_failed", phaseCount(matcher, "Failed")},
		{"deployment", fmt.Sprintf(
			`count by (zke_cluster_id) (kube_deployment_status_replicas{%s})`,
			matcher,
		)},
		{"statefulset", fmt.Sprintf(
			`count by (zke_cluster_id) (kube_statefulset_status_replicas{%s})`,
			matcher,
		)},
		{"daemonset", fmt.Sprintf(
			`count by (zke_cluster_id) `+
				`(kube_daemonset_status_desired_number_scheduled{%s})`,
			matcher,
		)},
	}
	branches := make([]string, 0, len(entries))
	for _, entry := range entries {
		branches = append(branches, fmt.Sprintf(
			`label_replace(%s, "resource", "%s", "", "")`,
			entry.expression,
			entry.resource,
		))
	}
	return "(" + strings.Join(branches, ") or (") + ")"
}

func phaseCount(matcher string, phase string) string {
	return fmt.Sprintf(
		`sum by (zke_cluster_id) (kube_pod_status_phase{%s,phase="%s"})`,
		matcher,
		phase,
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
