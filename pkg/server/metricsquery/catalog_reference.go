package metricsquery

import (
	"fmt"
	"strings"

	"github.com/togettoyou/zke/pkg/shared/observability"
)

// referenceCatalog completes the operational views that are commonly needed
// after the base resource catalogue: Kubernetes control-plane components,
// CoreDNS, workload networking, detailed container signals and optional GPU
// exporters. The corresponding scrape jobs live with the Agent configuration;
// keeping these definitions separate makes that coverage auditable without
// making the original capacity catalogue harder to review.
func referenceCatalog() []Definition {
	definitions := []Definition{
		containerGauge("pod_memory_rss", "Pod 内存 RSS", "container_memory_rss", UnitBytes),
		containerGauge("pod_memory_cache", "Pod 内存缓存", "container_memory_cache", UnitBytes),
		containerRate("pod_memory_failures", "Pod 内存失败次数", "container_memory_failcnt", UnitOpsPerSecond),
		containerGauge("pod_processes", "Pod 进程数", "container_processes", UnitCount),
		containerGauge("pod_sockets", "Pod Socket 数", "container_sockets", UnitCount),
		podResource("pod_cpu_requests", "Pod CPU 申请量", "kube_pod_container_resource_requests", "cpu", UnitMillicores, 1000),
		podResource("pod_cpu_limits", "Pod CPU 限制量", "kube_pod_container_resource_limits", "cpu", UnitMillicores, 1000),
		podResource("pod_memory_requests", "Pod 内存申请量", "kube_pod_container_resource_requests", "memory", UnitBytes, 1),
		podResource("pod_memory_limits", "Pod 内存限制量", "kube_pod_container_resource_limits", "memory", UnitBytes, 1),
		podRatio("pod_cpu_request_utilization", "Pod CPU 用量/申请量", "pod_cpu_usage_seconds_total", true, "kube_pod_container_resource_requests", "cpu"),
		podRatio("pod_cpu_limit_utilization", "Pod CPU 用量/限制量", "pod_cpu_usage_seconds_total", true, "kube_pod_container_resource_limits", "cpu"),
		podRatio("pod_memory_request_utilization", "Pod 内存用量/申请量", "pod_memory_working_set_bytes", false, "kube_pod_container_resource_requests", "memory"),
		podRatio("pod_memory_limit_utilization", "Pod 内存用量/限制量", "pod_memory_working_set_bytes", false, "kube_pod_container_resource_limits", "memory"),
		podNetworkRate("pod_network_receive_packets", "Pod 网络接收包", "container_network_receive_packets_total", UnitOpsPerSecond),
		podNetworkRate("pod_network_transmit_packets", "Pod 网络发送包", "container_network_transmit_packets_total", UnitOpsPerSecond),
		podNetworkRate("pod_network_receive_errors", "Pod 网络接收错误", "container_network_receive_errors_total", UnitOpsPerSecond),
		podNetworkRate("pod_network_transmit_errors", "Pod 网络发送错误", "container_network_transmit_errors_total", UnitOpsPerSecond),
		workloadResource("workload_cpu_requests", "工作负载 CPU 申请量", "kube_pod_container_resource_requests", "cpu", UnitMillicores, 1000),
		workloadResource("workload_cpu_limits", "工作负载 CPU 限制量", "kube_pod_container_resource_limits", "cpu", UnitMillicores, 1000),
		workloadResource("workload_memory_requests", "工作负载内存申请量", "kube_pod_container_resource_requests", "memory", UnitBytes, 1),
		workloadResource("workload_memory_limits", "工作负载内存限制量", "kube_pod_container_resource_limits", "memory", UnitBytes, 1),
		workloadNetwork("workload_network_receive", "工作负载网络接收", "container_network_receive_bytes_total", UnitBytesPerSecond),
		workloadNetwork("workload_network_transmit", "工作负载网络发送", "container_network_transmit_bytes_total", UnitBytesPerSecond),
		workloadNetwork("workload_network_receive_packets", "工作负载网络接收包", "container_network_receive_packets_total", UnitOpsPerSecond),
		workloadNetwork("workload_network_transmit_packets", "工作负载网络发送包", "container_network_transmit_packets_total", UnitOpsPerSecond),
		workloadNetwork("workload_network_receive_drops", "工作负载网络接收丢包", "container_network_receive_packets_dropped_total", UnitOpsPerSecond),
		workloadNetwork("workload_network_transmit_drops", "工作负载网络发送丢包", "container_network_transmit_packets_dropped_total", UnitOpsPerSecond),
	}
	definitions = append(definitions, componentDefinitions()...)
	definitions = append(definitions, coreDNSDefinitions()...)
	definitions = append(definitions, gpuDefinitions()...)
	return definitions
}

func containerGauge(name, title, family string, unit Unit) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions: []string{"namespace", "pod", "container"}, SupportsTop: true,
		RequiresTop: true, SupportsNamespace: true,
		build: func(matcher string, params buildParams) string {
			return topk(fmt.Sprintf(`sum by (zke_cluster_id, namespace, pod, container) (%s{%s,pod!="",container!=""})`, family, namespaceSelector(matcher, params.Namespace)), params.Top)
		}}
}

func containerRate(name, title, family string, unit Unit) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions: []string{"namespace", "pod", "container"}, SupportsTop: true,
		RequiresTop: true, SupportsNamespace: true,
		build: func(matcher string, params buildParams) string {
			return topk(fmt.Sprintf(`sum by (zke_cluster_id, namespace, pod, container) (rate(%s{%s,pod!="",container!=""}[%s]))`, family, namespaceSelector(matcher, params.Namespace), params.Window), params.Top)
		}}
}

func podResource(name, title, family, resource string, unit Unit, multiplier int) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions: []string{"namespace", "pod"}, SupportsTop: true, RequiresTop: true,
		SupportsNamespace: true, RequiresComponent: observability.ComponentKubeState,
		build: func(matcher string, params buildParams) string {
			expression := fmt.Sprintf(`sum by (zke_cluster_id, namespace, pod) (%s{%s,resource="%s"})`, family, namespaceSelector(matcher, params.Namespace), resource)
			if multiplier != 1 {
				expression = fmt.Sprintf(`(%s) * %d`, expression, multiplier)
			}
			return topk(expression, params.Top)
		}}
}

func podRatio(name, title, numeratorFamily string, numeratorRate bool, denominatorFamily, resource string) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: UnitRatio,
		Dimensions: []string{"namespace", "pod"}, SupportsTop: true, RequiresTop: true,
		SupportsNamespace: true, RequiresComponent: observability.ComponentKubeState,
		build: func(matcher string, params buildParams) string {
			selector := namespaceSelector(matcher, params.Namespace)
			num := fmt.Sprintf(`%s{%s}`, numeratorFamily, selector)
			if numeratorRate {
				num = fmt.Sprintf(`rate(%s{%s}[%s])`, numeratorFamily, selector, params.Window)
			}
			den := fmt.Sprintf(`%s{%s,resource="%s"}`, denominatorFamily, selector, resource)
			return topk(fmt.Sprintf(`sum by (zke_cluster_id, namespace, pod) (%s) / on (zke_cluster_id, namespace, pod) (sum by (zke_cluster_id, namespace, pod) (%s) > 0)`, num, den), params.Top)
		}}
}

func podNetworkRate(name, title, family string, unit Unit) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions: []string{"namespace", "pod"}, SupportsTop: true, RequiresTop: true,
		SupportsNamespace: true,
		build: func(matcher string, params buildParams) string {
			return topk(fmt.Sprintf(`sum by (zke_cluster_id, namespace, pod) (rate(%s{%s,pod!=""}[%s]))`, family, namespaceSelector(matcher, params.Namespace), params.Window), params.Top)
		}}
}

func workloadResource(name, title, family, resource string, unit Unit, multiplier int) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions: []string{"namespace", "workload_kind", "workload"}, SupportsTop: true,
		RequiresTop: true, SupportsNamespace: true, RequiresComponent: observability.ComponentKubeState,
		build: func(matcher string, params buildParams) string {
			selector := namespaceSelector(matcher, params.Namespace)
			usage := fmt.Sprintf(`sum by (zke_cluster_id, namespace, pod) (%s{%s,resource="%s"})`, family, selector, resource)
			if multiplier != 1 {
				usage = fmt.Sprintf(`(%s) * %d`, usage, multiplier)
			}
			return topk(workloadRollup(usage, selector), params.Top)
		}}
}

func workloadNetwork(name, title, family string, unit Unit) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions: []string{"namespace", "workload_kind", "workload"}, SupportsTop: true,
		RequiresTop: true, SupportsNamespace: true, RequiresComponent: observability.ComponentKubeState,
		build: func(matcher string, params buildParams) string {
			selector := namespaceSelector(matcher, params.Namespace)
			usage := fmt.Sprintf(`sum by (zke_cluster_id, namespace, pod) (rate(%s{%s,pod!=""}[%s]))`, family, selector, params.Window)
			return topk(workloadRollup(usage, selector), params.Top)
		}}
}

func componentDefinitions() []Definition {
	return []Definition{
		componentGauge("control_plane_up", "核心组件健康度", "up", UnitRatio),
		componentRate("control_plane_cpu", "核心组件 CPU 用量", "process_cpu_seconds_total", UnitMillicores, 1000),
		componentGauge("control_plane_memory", "核心组件内存用量", "process_resident_memory_bytes", UnitBytes),
		componentGauge("control_plane_goroutines", "核心组件 Goroutine", "go_goroutines", UnitCount),
		componentRate("control_plane_rest_requests", "核心组件 API 请求速率", "rest_client_requests_total", UnitOpsPerSecond, 1),
		componentGauge("control_plane_workqueue_depth", "核心组件工作队列深度", "workqueue_depth", UnitCount),
		componentRate("control_plane_workqueue_adds", "核心组件工作队列新增速率", "workqueue_adds_total", UnitOpsPerSecond, 1),
		componentHistogram("control_plane_workqueue_latency", "核心组件工作队列 P99 等待", "workqueue_queue_duration_seconds_bucket"),
		componentRate("apiserver_requests", "API Server 请求速率", "apiserver_request_total", UnitOpsPerSecond, 1),
		componentHistogram("apiserver_latency", "API Server P99 请求延迟", "apiserver_request_duration_seconds_bucket"),
		componentGauge("apiserver_inflight", "API Server 并发请求", "apiserver_current_inflight_requests", UnitCount),
		componentRate("apiserver_watch_events", "API Server Watch 事件速率", "apiserver_watch_events_total", UnitOpsPerSecond, 1),
		componentGauge("scheduler_pending_pods", "Scheduler 待调度 Pod", "scheduler_pending_pods", UnitCount),
		componentJobQuantile("scheduler_attempts", "Scheduler 成功调度尝试次数 P90", "scheduler_pod_scheduling_attempts_bucket", "kube-scheduler", UnitCount, 0.9),
		componentRate("kubelet_runtime_operations", "Kubelet 运行时操作速率", "kubelet_runtime_operations_total", UnitOpsPerSecond, 1),
		componentHistogram("kubelet_runtime_latency", "Kubelet 运行时操作 P99 延迟", "kubelet_runtime_operations_duration_seconds_bucket"),
		componentGauge("kubelet_config_errors", "Kubelet 配置错误", "kubelet_node_config_error", UnitCount),
		componentGauge("kubelet_volumes", "Kubelet 卷管理数量", "volume_manager_total_volumes", UnitCount),
	}
}

func componentSelector(matcher string) string {
	return matcher + `,job=~"kube-apiserver|kube-controller-manager|kube-scheduler|kube-proxy|kubelet"`
}

func componentGauge(name, title, family string, unit Unit) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: unit, Dimensions: []string{"job", "instance"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			return topk(fmt.Sprintf(`max by (zke_cluster_id, job, instance) (%s{%s})`, family, componentSelector(matcher)), params.Top)
		}}
}

func componentRate(name, title, family string, unit Unit, multiplier int) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: unit, Dimensions: []string{"job", "instance"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			expression := fmt.Sprintf(`sum by (zke_cluster_id, job, instance) (rate(%s{%s}[%s]))`, family, componentSelector(matcher), params.Window)
			if multiplier != 1 {
				expression = fmt.Sprintf(`(%s) * %d`, expression, multiplier)
			}
			return topk(expression, params.Top)
		}}
}

func componentHistogram(name, title, family string) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: UnitSeconds, Dimensions: []string{"job", "instance"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			return topk(fmt.Sprintf(`histogram_quantile(0.99, sum by (zke_cluster_id, job, instance, le) (rate(%s{%s}[%s])))`, family, componentSelector(matcher), params.Window), params.Top)
		}}
}

func coreDNSDefinitions() []Definition {
	return []Definition{
		coreDNSRate("coredns_requests", "CoreDNS 请求速率", "coredns_dns_requests_total", UnitOpsPerSecond),
		coreDNSRate("coredns_responses", "CoreDNS 响应速率", "coredns_dns_responses_total", UnitOpsPerSecond),
		coreDNSHistogram("coredns_latency", "CoreDNS P99 响应延迟", "coredns_dns_request_duration_seconds_bucket"),
		coreDNSRate("coredns_cache_hits", "CoreDNS 缓存命中速率", "coredns_cache_hits_total", UnitOpsPerSecond),
		coreDNSGauge("coredns_cache_entries", "CoreDNS 缓存条目", "coredns_cache_entries", UnitCount),
		coreDNSRate("coredns_forward_failures", "CoreDNS 上游健康检查失败", "coredns_forward_healthcheck_failures_total", UnitOpsPerSecond),
		coreDNSGauge("coredns_goroutines", "CoreDNS Goroutine", "go_goroutines", UnitCount),
		coreDNSRate("coredns_cpu", "CoreDNS CPU 用量", "process_cpu_seconds_total", UnitMillicores),
		coreDNSGauge("coredns_memory", "CoreDNS 内存用量", "process_resident_memory_bytes", UnitBytes),
	}
}

func coreDNSGauge(name, title, family string, unit Unit) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: unit, Dimensions: []string{"instance"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			return topk(fmt.Sprintf(`max by (zke_cluster_id, instance) (%s{%s,job="coredns"})`, family, matcher), params.Top)
		}}
}

func coreDNSRate(name, title, family string, unit Unit) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: unit, Dimensions: []string{"instance"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			expression := fmt.Sprintf(`sum by (zke_cluster_id, instance) (rate(%s{%s,job="coredns"}[%s]))`, family, matcher, params.Window)
			if unit == UnitMillicores {
				expression = "(" + expression + ") * 1000"
			}
			return topk(expression, params.Top)
		}}
}

func coreDNSHistogram(name, title, family string) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: UnitSeconds, Dimensions: []string{"instance"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			return topk(fmt.Sprintf(`histogram_quantile(0.99, sum by (zke_cluster_id, instance, le) (rate(%s{%s,job="coredns"}[%s])))`, family, matcher, params.Window), params.Top)
		}}
}

func gpuDefinitions() []Definition {
	gauges := []struct {
		name, title, family string
		unit                Unit
	}{
		{"gpu_utilization", "GPU 利用率", "DCGM_FI_DEV_GPU_UTIL", UnitRatio},
		{"gpu_memory_copy_utilization", "GPU 显存复制利用率", "DCGM_FI_DEV_MEM_COPY_UTIL", UnitRatio},
		{"gpu_memory_used", "GPU 显存已用", "DCGM_FI_DEV_FB_USED", UnitBytes},
		{"gpu_memory_total", "GPU 显存总量", "DCGM_FI_DEV_FB_TOTAL", UnitBytes},
		{"gpu_bar1_used", "GPU BAR1 已用", "DCGM_FI_DEV_BAR1_USED", UnitBytes},
		{"gpu_encoder_utilization", "GPU 编码器利用率", "DCGM_FI_DEV_ENC_UTIL", UnitRatio},
		{"gpu_decoder_utilization", "GPU 解码器利用率", "DCGM_FI_DEV_DEC_UTIL", UnitRatio},
		{"gpu_temperature", "GPU 温度", "DCGM_FI_DEV_GPU_TEMP", UnitCount},
		{"gpu_power_usage", "GPU 功耗", "DCGM_FI_DEV_POWER_USAGE", UnitCount},
		{"gpu_xid_errors", "GPU XID 错误", "DCGM_FI_DEV_XID_ERRORS", UnitCount},
		{"gpu_pcie_rx", "GPU PCIe 接收", "DCGM_FI_PROF_PCIE_RX_BYTES", UnitBytesPerSecond},
		{"gpu_pcie_tx", "GPU PCIe 发送", "DCGM_FI_PROF_PCIE_TX_BYTES", UnitBytesPerSecond},
		{"gpu_nvlink_rx", "GPU NVLink 接收", "DCGM_FI_PROF_NVLINK_RX_BYTES", UnitBytesPerSecond},
		{"gpu_nvlink_tx", "GPU NVLink 发送", "DCGM_FI_PROF_NVLINK_TX_BYTES", UnitBytesPerSecond},
		{"gpu_sm_active", "GPU SM 活跃度", "DCGM_FI_PROF_SM_ACTIVE", UnitRatio},
		{"gpu_sm_occupancy", "GPU SM 占用率", "DCGM_FI_PROF_SM_OCCUPANCY", UnitRatio},
		{"gpu_tensor_active", "GPU Tensor Core 活跃度", "DCGM_FI_PROF_PIPE_TENSOR_ACTIVE", UnitRatio},
	}
	definitions := make([]Definition, 0, len(gauges))
	for _, item := range gauges {
		item := item
		definitions = append(definitions, Definition{Name: item.name, Title: item.title, Kind: KindRange, Unit: item.unit,
			Dimensions: []string{"node", "gpu", "pod", "namespace"}, SupportsTop: true,
			build: func(matcher string, params buildParams) string {
				expression := fmt.Sprintf(`max by (zke_cluster_id, node, gpu, pod, namespace) (%s{%s,job="dcgm-exporter"})`, item.family, matcher)
				if strings.Contains(item.family, "_FB_") || strings.Contains(item.family, "_BAR1_") {
					expression = "(" + expression + ") * 1024 * 1024"
				}
				if item.unit == UnitRatio && strings.Contains(item.family, "DCGM_FI_DEV_") {
					expression = "(" + expression + ") / 100"
				}
				return topk(expression, params.Top)
			}})
	}
	return definitions
}
