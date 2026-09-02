package metricsquery

import (
	"fmt"
	"strings"
)

// parityCatalog contains the object-lifecycle and component-detail signals in
// the container-monitoring reference dashboards. They are separate from the
// original operational catalogue so the external parity boundary stays easy to
// audit when either Kubernetes or the reference dashboards change.
func parityCatalog() []Definition {
	definitions := []Definition{
		networkScope("cluster_network_receive", "集群网络接收", "container_network_receive_bytes_total", UnitBytesPerSecond, false),
		networkScope("cluster_network_transmit", "集群网络发送", "container_network_transmit_bytes_total", UnitBytesPerSecond, false),
		networkScope("cluster_network_receive_packets", "集群网络接收包", "container_network_receive_packets_total", UnitOpsPerSecond, false),
		networkScope("cluster_network_transmit_packets", "集群网络发送包", "container_network_transmit_packets_total", UnitOpsPerSecond, false),
		networkScope("cluster_network_receive_drops", "集群网络接收丢包", "container_network_receive_packets_dropped_total", UnitOpsPerSecond, false),
		networkScope("cluster_network_transmit_drops", "集群网络发送丢包", "container_network_transmit_packets_dropped_total", UnitOpsPerSecond, false),
		networkScope("namespace_network_receive", "Namespace 网络接收", "container_network_receive_bytes_total", UnitBytesPerSecond, true),
		networkScope("namespace_network_transmit", "Namespace 网络发送", "container_network_transmit_bytes_total", UnitBytesPerSecond, true),
		networkScope("namespace_network_receive_packets", "Namespace 网络接收包", "container_network_receive_packets_total", UnitOpsPerSecond, true),
		networkScope("namespace_network_transmit_packets", "Namespace 网络发送包", "container_network_transmit_packets_total", UnitOpsPerSecond, true),
		networkScope("namespace_network_receive_drops", "Namespace 网络接收丢包", "container_network_receive_packets_dropped_total", UnitOpsPerSecond, true),
		networkScope("namespace_network_transmit_drops", "Namespace 网络发送丢包", "container_network_transmit_packets_dropped_total", UnitOpsPerSecond, true),
		workloadRatio("workload_cpu_request_utilization", "工作负载 CPU 用量/申请量", "pod_cpu_usage_seconds_total", true, "kube_pod_container_resource_requests", "cpu"),
		workloadRatio("workload_cpu_limit_utilization", "工作负载 CPU 用量/限制量", "pod_cpu_usage_seconds_total", true, "kube_pod_container_resource_limits", "cpu"),
		workloadRatio("workload_memory_request_utilization", "工作负载内存用量/申请量", "pod_memory_working_set_bytes", false, "kube_pod_container_resource_requests", "memory"),
		workloadRatio("workload_memory_limit_utilization", "工作负载内存用量/限制量", "pod_memory_working_set_bytes", false, "kube_pod_container_resource_limits", "memory"),
		workloadNetwork("workload_network_receive_errors", "工作负载网络接收错误", "container_network_receive_errors_total", UnitOpsPerSecond),
		workloadNetwork("workload_network_transmit_errors", "工作负载网络发送错误", "container_network_transmit_errors_total", UnitOpsPerSecond),
		workloadNetwork("workload_filesystem_read", "工作负载文件系统读取", "container_fs_reads_bytes_total", UnitBytesPerSecond),
		workloadNetwork("workload_filesystem_write", "工作负载文件系统写入", "container_fs_writes_bytes_total", UnitBytesPerSecond),
		workloadRate("workload_cpu_user", "工作负载 CPU 用户态", "container_cpu_user_seconds_total", UnitMillicores, 1000),
		workloadGauge("workload_sockets", "工作负载 Socket 数", "container_sockets", UnitCount),
		objectAge("pod_age", "Pod 运行时长", "kube_pod_created", "pod"),
		objectAge("deployment_age", "Deployment 运行时长", "kube_deployment_created", "deployment"),
		objectAge("statefulset_age", "StatefulSet 运行时长", "kube_statefulset_created", "statefulset"),
		objectAge("daemonset_age", "DaemonSet 运行时长", "kube_daemonset_created", "daemonset"),
		objectGauge("statefulset_generation", "StatefulSet Generation", "kube_statefulset_metadata_generation", "statefulset", ""),
		objectGauge("deployment_replicas_requested", "Deployment 期望副本", "kube_deployment_spec_replicas", "deployment", ""),
		objectGauge("deployment_replicas_ready", "Deployment 就绪副本", "kube_deployment_status_replicas_ready", "deployment", ""),
		objectGauge("deployment_replicas_updated", "Deployment 已更新副本", "kube_deployment_status_replicas_updated", "deployment", ""),
		objectGauge("deployment_replicas_unavailable", "Deployment 不可用副本", "kube_deployment_status_replicas_unavailable", "deployment", ""),
		objectGauge("statefulset_replicas_requested", "StatefulSet 期望副本", "kube_statefulset_replicas", "statefulset", ""),
		objectGauge("statefulset_replicas_ready", "StatefulSet 就绪副本", "kube_statefulset_status_replicas_ready", "statefulset", ""),
		objectGauge("statefulset_replicas_updated", "StatefulSet 已更新副本", "kube_statefulset_status_replicas_updated", "statefulset", ""),
		objectGauge("statefulset_replicas_available", "StatefulSet 可用副本", "kube_statefulset_status_replicas_available", "statefulset", ""),
		objectGauge("daemonset_replicas_requested", "DaemonSet 期望副本", "kube_daemonset_status_desired_number_scheduled", "daemonset", ""),
		objectGauge("daemonset_replicas_ready", "DaemonSet 就绪副本", "kube_daemonset_status_number_ready", "daemonset", ""),
		objectGauge("daemonset_replicas_unavailable", "DaemonSet 不可用副本", "kube_daemonset_status_number_unavailable", "daemonset", ""),
		objectCount("namespace_statefulsets", "Namespace StatefulSet 数", "kube_statefulset_created", ""),
		objectCount("namespace_daemonsets", "Namespace DaemonSet 数", "kube_daemonset_created", ""),
		objectCount("namespace_jobs", "Namespace Job 数", "kube_job_info", ""),
		objectCount("namespace_active_jobs", "Namespace 活跃 Job 数", "kube_job_status_active", "> 0"),
		objectCount("namespace_cronjobs", "Namespace CronJob 数", "kube_cronjob_created", ""),
		objectCount("namespace_active_cronjobs", "Namespace 活跃 CronJob 数", "kube_cronjob_status_active", "> 0"),
		objectCount("namespace_pvcs", "Namespace PVC 数", "kube_persistentvolumeclaim_info", ""),
		objectCount("namespace_services", "Namespace Service 数", "kube_service_info", ""),
		objectCount("namespace_loadbalancers", "Namespace LoadBalancer Service 数", "kube_service_spec_type", `{type="LoadBalancer"}`),
		objectCount("namespace_ingresses", "Namespace Ingress 数", "kube_ingress_info", ""),
		namespaceValue("namespace_pvc_storage_requests", "Namespace PVC 申请容量", "kube_persistentvolumeclaim_resource_requests_storage_bytes", UnitBytes),
		objectGauge("pod_running_containers", "Pod 运行中容器数", "kube_pod_container_status_running", "pod", "sum"),
	}
	definitions = append(definitions, componentParityCatalog()...)
	definitions = append(definitions, gpuParityCatalog()...)
	return definitions
}

func networkScope(name, title, family string, unit Unit, byNamespace bool) Definition {
	dimensions := []string(nil)
	if byNamespace {
		dimensions = []string{"namespace"}
	}
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions: dimensions, SupportsNamespace: byNamespace,
		build: func(matcher string, params buildParams) string {
			groups := "zke_cluster_id"
			if byNamespace {
				groups += ", namespace"
			}
			return fmt.Sprintf(
				`sum by (%s) (rate(%s{%s,pod!=""}[%s]))`,
				groups, family, namespaceSelector(matcher, params.Namespace), params.Window,
			)
		},
	}
}

// workloadKindCatalog turns the shared workload queries into explicit
// Deployment, StatefulSet and DaemonSet vocabularies. Filtering happens before
// Top N is applied, so a busy Deployment cannot hide every StatefulSet from a
// StatefulSet view.
func workloadKindCatalog(definitions []Definition) []Definition {
	baseNames := []string{
		"workload_cpu_usage", "workload_memory_usage",
		"workload_cpu_requests", "workload_cpu_limits",
		"workload_memory_requests", "workload_memory_limits",
		"workload_network_receive", "workload_network_transmit",
		"workload_network_receive_packets", "workload_network_transmit_packets",
		"workload_network_receive_drops", "workload_network_transmit_drops",
		"workload_network_receive_errors", "workload_network_transmit_errors",
		"workload_filesystem_read", "workload_filesystem_write",
		"workload_cpu_user", "workload_sockets",
		"workload_cpu_request_utilization", "workload_cpu_limit_utilization",
		"workload_memory_request_utilization", "workload_memory_limit_utilization",
	}
	types := []struct{ prefix, label string }{
		{"deployment", "Deployment"},
		{"statefulset", "StatefulSet"},
		{"daemonset", "DaemonSet"},
	}
	result := make([]Definition, 0, len(baseNames)*len(types))
	for _, workloadType := range types {
		for _, baseName := range baseNames {
			var base Definition
			for _, candidate := range definitions {
				if candidate.Name == baseName {
					base = candidate
					break
				}
			}
			if base.build == nil {
				continue
			}
			baseBuild := base.build
			kind := workloadType.label
			clone := base
			clone.Name = workloadType.prefix + strings.TrimPrefix(baseName, "workload")
			clone.Title = workloadType.label + strings.TrimPrefix(base.Title, "工作负载")
			clone.build = func(matcher string, params buildParams) string {
				top := params.Top
				params.Top = 0
				expression := fmt.Sprintf(`label_match(%s, "workload_kind", %q)`, baseBuild(matcher, params), kind)
				return topk(expression, top)
			}
			result = append(result, clone)
		}
	}
	return result
}

func workloadRatio(name, title, numeratorFamily string, numeratorRate bool, denominatorFamily, resource string) Definition {
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: UnitRatio,
		Dimensions:  []string{"namespace", "workload_kind", "workload"},
		SupportsTop: true, RequiresTop: true, SupportsNamespace: true,
		RequiresComponent: "kube-state-metrics",
		build: func(matcher string, params buildParams) string {
			selector := namespaceSelector(matcher, params.Namespace)
			numerator := fmt.Sprintf(`sum by (zke_cluster_id, namespace, pod) (%s{%s})`, numeratorFamily, selector)
			if numeratorRate {
				numerator = fmt.Sprintf(`sum by (zke_cluster_id, namespace, pod) (rate(%s{%s}[%s]))`, numeratorFamily, selector, params.Window)
			}
			denominator := fmt.Sprintf(`sum by (zke_cluster_id, namespace, pod) (%s{%s,resource=%q})`, denominatorFamily, selector, resource)
			numerator = workloadRollup(numerator, selector)
			denominator = workloadRollup(denominator, selector)
			return topk(fmt.Sprintf(
				`(%s) / on (zke_cluster_id, namespace, workload_kind, workload) ((%s) > 0)`,
				numerator, denominator,
			), params.Top)
		},
	}
}

func workloadGauge(name, title, family string, unit Unit) Definition {
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions:  []string{"namespace", "workload_kind", "workload"},
		SupportsTop: true, RequiresTop: true, SupportsNamespace: true,
		RequiresComponent: "kube-state-metrics",
		build: func(matcher string, params buildParams) string {
			selector := namespaceSelector(matcher, params.Namespace)
			usage := fmt.Sprintf(`sum by (zke_cluster_id, namespace, pod) (%s{%s,pod!=""})`, family, selector)
			return topk(workloadRollup(usage, selector), params.Top)
		},
	}
}

func workloadRate(name, title, family string, unit Unit, multiplier int) Definition {
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions:  []string{"namespace", "workload_kind", "workload"},
		SupportsTop: true, RequiresTop: true, SupportsNamespace: true,
		RequiresComponent: "kube-state-metrics",
		build: func(matcher string, params buildParams) string {
			selector := namespaceSelector(matcher, params.Namespace)
			usage := fmt.Sprintf(`sum by (zke_cluster_id, namespace, pod) (rate(%s{%s,pod!=""}[%s]))`, family, selector, params.Window)
			if multiplier != 1 {
				usage = fmt.Sprintf("(%s) * %d", usage, multiplier)
			}
			return topk(workloadRollup(usage, selector), params.Top)
		},
	}
}

func objectAge(name, title, family, dimension string) Definition {
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: UnitSeconds,
		Dimensions: []string{"namespace", dimension}, SupportsNamespace: true,
		SupportsTop: true, RequiresTop: true, RequiresComponent: "kube-state-metrics",
		build: func(matcher string, params buildParams) string {
			selector := namespaceSelector(matcher, params.Namespace)
			return topk(fmt.Sprintf(
				`time() - max by (zke_cluster_id, namespace, %s) (%s{%s})`,
				dimension, family, selector,
			), params.Top)
		},
	}
}

func objectGauge(name, title, family, dimension, aggregation string) Definition {
	return objectValue(name, title, family, dimension, UnitCount, aggregation)
}

func objectValue(name, title, family, dimension string, unit Unit, aggregation string) Definition {
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions: []string{"namespace", dimension}, SupportsNamespace: true,
		SupportsTop: true, RequiresTop: true, RequiresComponent: "kube-state-metrics",
		build: func(matcher string, params buildParams) string {
			selector := namespaceSelector(matcher, params.Namespace)
			operator := "max"
			if aggregation == "sum" {
				operator = "sum"
			}
			return topk(fmt.Sprintf(
				`%s by (zke_cluster_id, namespace, %s) (%s{%s})`,
				operator, dimension, family, selector,
			), params.Top)
		},
	}
}

func objectCount(name, title, family, suffix string) Definition {
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: UnitCount,
		Dimensions: []string{"namespace"}, SupportsNamespace: true,
		RequiresComponent: "kube-state-metrics",
		build: func(matcher string, params buildParams) string {
			selector := namespaceSelector(matcher, params.Namespace)
			metric := family + "{" + selector + "}"
			operation := suffix
			if strings.HasPrefix(operation, "{") {
				metric = family + "{" + selector + "," + strings.Trim(operation, "{}") + "}"
				operation = ""
			}
			return fmt.Sprintf(`count by (zke_cluster_id, namespace) (%s %s)`, metric, operation)
		},
	}
}

func namespaceValue(name, title, family string, unit Unit) Definition {
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions: []string{"namespace"}, SupportsNamespace: true,
		RequiresComponent: "kube-state-metrics",
		build: func(matcher string, params buildParams) string {
			return fmt.Sprintf(
				`sum by (zke_cluster_id, namespace) (%s{%s})`,
				family, namespaceSelector(matcher, params.Namespace),
			)
		},
	}
}

func componentParityCatalog() []Definition {
	definitions := []Definition{
		componentJobGauge("apiserver_up", "API Server 健康", "up", "kube-apiserver", UnitRatio),
		componentJobRate("apiserver_cpu", "API Server CPU", "process_cpu_seconds_total", "kube-apiserver", UnitMillicores, 1000),
		componentJobGauge("apiserver_memory", "API Server 内存", "process_resident_memory_bytes", "kube-apiserver", UnitBytes),
		componentJobGauge("controller_manager_up", "Controller Manager 健康", "up", "kube-controller-manager", UnitRatio),
		componentJobRate("controller_manager_cpu", "Controller Manager CPU", "process_cpu_seconds_total", "kube-controller-manager", UnitMillicores, 1000),
		componentJobGauge("controller_manager_memory", "Controller Manager 内存", "process_resident_memory_bytes", "kube-controller-manager", UnitBytes),
		componentJobRate("controller_manager_rest_requests", "Controller Manager API 请求", "rest_client_requests_total", "kube-controller-manager", UnitOpsPerSecond, 1),
		componentJobGauge("controller_manager_workqueue_depth", "Controller Manager 工作队列深度", "workqueue_depth", "kube-controller-manager", UnitCount),
		componentJobRate("controller_manager_workqueue_adds", "Controller Manager 工作队列新增", "workqueue_adds_total", "kube-controller-manager", UnitOpsPerSecond, 1),
		componentJobHistogram("controller_manager_workqueue_latency", "Controller Manager 工作队列 P99 等待", "workqueue_queue_duration_seconds_bucket", "kube-controller-manager"),
		componentJobGauge("scheduler_up", "Scheduler 健康", "up", "kube-scheduler", UnitRatio),
		componentJobRate("scheduler_cpu", "Scheduler CPU", "process_cpu_seconds_total", "kube-scheduler", UnitMillicores, 1000),
		componentJobGauge("scheduler_memory", "Scheduler 内存", "process_resident_memory_bytes", "kube-scheduler", UnitBytes),
		componentJobRate("scheduler_rest_requests", "Scheduler API 请求", "rest_client_requests_total", "kube-scheduler", UnitOpsPerSecond, 1),
		componentJobGauge("proxy_up", "Proxy 健康", "up", "kube-proxy", UnitRatio),
		componentJobRate("proxy_cpu", "Proxy CPU", "process_cpu_seconds_total", "kube-proxy", UnitMillicores, 1000),
		componentJobGauge("proxy_memory", "Proxy 内存", "process_resident_memory_bytes", "kube-proxy", UnitBytes),
		componentJobRate("proxy_rest_requests", "Proxy API 请求", "rest_client_requests_total", "kube-proxy", UnitOpsPerSecond, 1),
		componentNamedRate("apiserver_read_requests", "API Server 读请求速率", "apiserver_request_total", "kube-apiserver", `verb=~"GET|LIST|WATCH"`),
		componentNamedRate("apiserver_write_requests", "API Server 写请求速率", "apiserver_request_total", "kube-apiserver", `verb=~"POST|PUT|PATCH|DELETE"`),
		apiserverAvailability("apiserver_availability", "API Server 可用率", ".*"),
		apiserverErrorBudget("apiserver_error_budget", "API Server 错误预算消耗率", ".*"),
		apiserverAvailability("apiserver_read_availability", "API Server 读可用率", "GET|LIST|WATCH"),
		apiserverAvailability("apiserver_write_availability", "API Server 写可用率", "POST|PUT|PATCH|DELETE"),
		apiserverErrors("apiserver_read_errors", "API Server 读错误率", "GET|LIST|WATCH"),
		apiserverErrors("apiserver_write_errors", "API Server 写错误率", "POST|PUT|PATCH|DELETE"),
		apiserverAverageLatency("apiserver_latency_average", "API Server 平均延迟", ".*"),
		apiserverAverageLatency("apiserver_read_latency_average", "API Server 平均读延迟", "GET|LIST|WATCH"),
		apiserverAverageLatency("apiserver_write_latency_average", "API Server 平均写延迟", "POST|PUT|PATCH|DELETE"),
		componentRate("apiserver_self_requests", "API Server 自请求速率", "apiserver_selfrequest_total", UnitOpsPerSecond, 1),
		componentRate("apiserver_too_many_objects", "API Server Too Many Objects 事件", "list_too_many_objects_events_total", UnitOpsPerSecond, 1),
		componentRate("apiserver_too_old_objects", "API Server Too Old Objects 事件", "watch_too_old_objects_events_total", UnitOpsPerSecond, 1),
		componentJobQuantile("apiserver_response_size", "API Server P99 响应体大小", "apiserver_response_sizes_bucket", "kube-apiserver", UnitBytes, 0.99),
		componentRate("kubelet_pod_start_rate", "Kubelet Pod 启动速率", "kubelet_pod_start_duration_seconds_count", UnitOpsPerSecond, 1),
		componentHistogram("kubelet_pod_worker_latency", "Kubelet Pod Worker P99 耗时", "kubelet_pod_worker_duration_seconds_bucket"),
		componentRate("kubelet_storage_operations", "Kubelet 存储操作速率", "storage_operation_duration_seconds_count", UnitOpsPerSecond, 1),
		componentRate("kubelet_storage_errors", "Kubelet 存储操作错误率", "storage_operation_errors_total", UnitOpsPerSecond, 1),
		componentHistogram("kubelet_storage_latency", "Kubelet 存储操作 P99 耗时", "storage_operation_duration_seconds_bucket"),
		componentRate("kubelet_cgroup_operations", "Kubelet Cgroup 操作速率", "kubelet_cgroup_manager_duration_seconds_count", UnitOpsPerSecond, 1),
		componentHistogram("kubelet_cgroup_latency", "Kubelet Cgroup 操作 P99 耗时", "kubelet_cgroup_manager_duration_seconds_bucket"),
		componentRate("proxy_rules_sync", "Proxy 规则同步速率", "kubeproxy_sync_proxy_rules_duration_seconds_count", UnitOpsPerSecond, 1),
		componentHistogram("proxy_rules_sync_latency", "Proxy 规则同步 P99 耗时", "kubeproxy_sync_proxy_rules_duration_seconds_bucket"),
		componentRate("proxy_network_programming", "Proxy 网络编程速率", "kubeproxy_network_programming_duration_seconds_count", UnitOpsPerSecond, 1),
		componentHistogram("proxy_network_programming_latency", "Proxy 网络编程 P99 耗时", "kubeproxy_network_programming_duration_seconds_bucket"),
		coreDNSQuantile("coredns_request_size", "CoreDNS P99 请求大小", "coredns_dns_request_size_bytes_bucket", UnitBytes),
		coreDNSQuantile("coredns_response_size", "CoreDNS P99 响应大小", "coredns_dns_response_size_bytes_bucket", UnitBytes),
		coreDNSRate("coredns_forward_requests", "CoreDNS 上游请求速率", "coredns_forward_requests_total", UnitOpsPerSecond),
		coreDNSHistogram("coredns_forward_latency", "CoreDNS 上游 P99 延迟", "coredns_proxy_request_duration_seconds_bucket"),
		coreDNSRate("coredns_healthcheck_failures", "CoreDNS 健康检查失败率", "coredns_proxy_healthcheck_failures_total", UnitOpsPerSecond),
		coreDNSRate("coredns_cache_misses", "CoreDNS 缓存未命中率", "coredns_cache_misses_total", UnitOpsPerSecond),
		coreDNSGroupedRate("coredns_requests_by_pod", "CoreDNS 按 Pod 请求", "coredns_dns_requests_total", []string{"pod"}, ""),
		coreDNSGroupedRate("coredns_requests_by_type", "CoreDNS 按查询类型请求", "coredns_dns_requests_total", []string{"type"}, ""),
		coreDNSGroupedRate("coredns_requests_by_zone", "CoreDNS 按 Zone 请求", "coredns_dns_requests_total", []string{"zone"}, ""),
		coreDNSGroupedRate("coredns_responses_by_code", "CoreDNS 按响应码响应", "coredns_dns_responses_total", []string{"rcode"}, ""),
		coreDNSGroupedRate("coredns_normal_responses", "CoreDNS 正常响应", "coredns_dns_responses_total", []string{"rcode"}, `rcode="NOERROR"`),
		coreDNSGroupedRate("coredns_abnormal_responses", "CoreDNS 异常响应", "coredns_dns_responses_total", []string{"rcode"}, `rcode!="NOERROR"`),
		coreDNSSuccessRate(),
		coreDNSGroupedRate("coredns_requests_by_upstream", "CoreDNS 按上游请求", "coredns_forward_requests_total", []string{"to"}, ""),
	}
	return definitions
}

func apiserverErrorBudget(name, title, verbs string) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: UnitRatio,
		Dimensions: []string{"instance"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			base := fmt.Sprintf(`%s,job="kube-apiserver",verb=~%q`, matcher, verbs)
			return topk(fmt.Sprintf(
				`sum by (zke_cluster_id, instance) (rate(apiserver_request_total{%s,code=~"5.."}[%s])) / on (zke_cluster_id, instance) (sum by (zke_cluster_id, instance) (rate(apiserver_request_total{%s}[%s])) > 0)`,
				base, params.Window, base, params.Window,
			), params.Top)
		}}
}

func apiserverAvailability(name, title, verbs string) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: UnitRatio,
		Dimensions: []string{"instance"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			base := fmt.Sprintf(`%s,job="kube-apiserver",verb=~%q`, matcher, verbs)
			return topk(fmt.Sprintf(
				`1 - sum by (zke_cluster_id, instance) (rate(apiserver_request_total{%s,code=~"5.."}[%s])) / on (zke_cluster_id, instance) (sum by (zke_cluster_id, instance) (rate(apiserver_request_total{%s}[%s])) > 0)`,
				base, params.Window, base, params.Window,
			), params.Top)
		}}
}

func apiserverErrors(name, title, verbs string) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: UnitOpsPerSecond,
		Dimensions: []string{"instance", "code"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			return topk(fmt.Sprintf(
				`sum by (zke_cluster_id, instance, code) (rate(apiserver_request_total{%s,job="kube-apiserver",verb=~%q,code=~"5.."}[%s]))`,
				matcher, verbs, params.Window,
			), params.Top)
		}}
}

func apiserverAverageLatency(name, title, verbs string) Definition {
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: UnitSeconds,
		Dimensions: []string{"instance"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			base := fmt.Sprintf(`%s,job="kube-apiserver",verb=~%q`, matcher, verbs)
			return topk(fmt.Sprintf(
				`sum by (zke_cluster_id, instance) (rate(apiserver_request_duration_seconds_sum{%s}[%s])) / on (zke_cluster_id, instance) (sum by (zke_cluster_id, instance) (rate(apiserver_request_duration_seconds_count{%s}[%s])) > 0)`,
				base, params.Window, base, params.Window,
			), params.Top)
		}}
}

func coreDNSGroupedRate(name, title, family string, dimensions []string, extra string) Definition {
	allDimensions := append([]string{"instance"}, dimensions...)
	return Definition{Name: name, Title: title, Kind: KindRange, Unit: UnitOpsPerSecond,
		Dimensions: allDimensions, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			selector := matcher + `,job="coredns"`
			if extra != "" {
				selector += "," + extra
			}
			groups := append([]string{"zke_cluster_id", "instance"}, dimensions...)
			return topk(fmt.Sprintf(
				`sum by (%s) (rate(%s{%s}[%s]))`,
				strings.Join(groups, ", "), family, selector, params.Window,
			), params.Top)
		}}
}

func componentJobGauge(name, title, family, job string, unit Unit) Definition {
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions: []string{"instance"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			return topk(fmt.Sprintf(
				`max by (zke_cluster_id, instance) (%s{%s,job=%q})`,
				family, matcher, job,
			), params.Top)
		},
	}
}

func componentJobRate(name, title, family, job string, unit Unit, multiplier int) Definition {
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions: []string{"instance", "code", "verb"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			expression := fmt.Sprintf(
				`sum by (zke_cluster_id, instance, code, verb) (rate(%s{%s,job=%q}[%s]))`,
				family, matcher, job, params.Window,
			)
			if multiplier != 1 {
				expression = fmt.Sprintf("(%s) * %d", expression, multiplier)
			}
			return topk(expression, params.Top)
		},
	}
}

func componentJobHistogram(name, title, family, job string) Definition {
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: UnitSeconds,
		Dimensions: []string{"instance"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			return topk(fmt.Sprintf(
				`histogram_quantile(0.99, sum by (zke_cluster_id, instance, le) (rate(%s{%s,job=%q}[%s])))`,
				family, matcher, job, params.Window,
			), params.Top)
		},
	}
}

func componentJobQuantile(name, title, family, job string, unit Unit, quantile float64) Definition {
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions: []string{"instance"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			return topk(fmt.Sprintf(
				`histogram_quantile(%g, sum by (zke_cluster_id, instance, le) (rate(%s{%s,job=%q}[%s])))`,
				quantile, family, matcher, job, params.Window,
			), params.Top)
		},
	}
}

func componentNamedRate(name, title, family, job, extra string) Definition {
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: UnitOpsPerSecond,
		Dimensions: []string{"instance", "verb"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			return topk(fmt.Sprintf(
				`sum by (zke_cluster_id, instance, verb) (rate(%s{%s,job=%q,%s}[%s]))`,
				family, matcher, job, extra, params.Window,
			), params.Top)
		},
	}
}

func coreDNSQuantile(name, title, family string, unit Unit) Definition {
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions: []string{"instance", "proto"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			return topk(fmt.Sprintf(
				`histogram_quantile(0.99, sum by (zke_cluster_id, instance, proto, le) (rate(%s{%s,job="coredns"}[%s])))`,
				family, matcher, params.Window,
			), params.Top)
		},
	}
}

func coreDNSSuccessRate() Definition {
	return Definition{Name: "coredns_success_rate", Title: "CoreDNS 成功率", Kind: KindRange, Unit: UnitRatio,
		Dimensions: []string{"instance", "proto"}, SupportsTop: true,
		build: func(matcher string, params buildParams) string {
			selector := matcher + `,job="coredns"`
			return topk(fmt.Sprintf(
				`sum by (zke_cluster_id, instance, proto) (rate(coredns_dns_responses_total{%s,rcode="NOERROR"}[%s])) / on (zke_cluster_id, instance, proto) (sum by (zke_cluster_id, instance, proto) (rate(coredns_dns_responses_total{%s}[%s])) > 0)`,
				selector, params.Window, selector, params.Window,
			), params.Top)
		}}
}

func gpuParityCatalog() []Definition {
	items := []struct {
		name   string
		title  string
		family string
		unit   Unit
	}{
		{"gpu_count", "GPU 数量", "DCGM_FI_DEV_COUNT", UnitCount},
		{"gpu_bar1_total", "GPU BAR1 总量", "DCGM_FI_DEV_BAR1_TOTAL", UnitBytes},
		{"gpu_graphics_active", "GPU Graphics Engine 活跃度", "DCGM_FI_PROF_GR_ENGINE_ACTIVE", UnitRatio},
		{"gpu_dram_active", "GPU DRAM 活跃度", "DCGM_FI_PROF_DRAM_ACTIVE", UnitRatio},
		{"gpu_fp16_active", "GPU FP16 Engine 活跃度", "DCGM_FI_PROF_PIPE_FP16_ACTIVE", UnitRatio},
		{"gpu_fp32_active", "GPU FP32 Engine 活跃度", "DCGM_FI_PROF_PIPE_FP32_ACTIVE", UnitRatio},
		{"gpu_fp64_active", "GPU FP64 Engine 活跃度", "DCGM_FI_PROF_PIPE_FP64_ACTIVE", UnitRatio},
		{"gpu_total_energy", "GPU 总能耗", "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION", UnitCount},
		{"gpu_memory_temperature", "GPU 显存温度", "DCGM_FI_DEV_MEMORY_TEMP", UnitCount},
		{"gpu_sm_clock", "GPU SM 时钟", "DCGM_FI_DEV_SM_CLOCK", UnitCount},
		{"gpu_memory_clock", "GPU 显存时钟", "DCGM_FI_DEV_MEM_CLOCK", UnitCount},
		{"gpu_app_sm_clock", "GPU 应用 SM 时钟", "DCGM_FI_DEV_APP_SM_CLOCK", UnitCount},
		{"gpu_app_memory_clock", "GPU 应用显存时钟", "DCGM_FI_DEV_APP_MEM_CLOCK", UnitCount},
		{"gpu_video_clock", "GPU 视频时钟", "DCGM_FI_DEV_VIDEO_CLOCK", UnitCount},
		{"gpu_clock_throttle_reasons", "GPU 时钟降频原因", "DCGM_FI_DEV_CLOCK_THROTTLE_REASONS", UnitCount},
		{"gpu_retired_sbe", "GPU 单比特错误退役页", "DCGM_FI_DEV_RETIRED_SBE", UnitCount},
		{"gpu_retired_dbe", "GPU 双比特错误退役页", "DCGM_FI_DEV_RETIRED_DBE", UnitCount},
		{"gpu_power_violation", "GPU 功耗限制", "DCGM_FI_DEV_POWER_VIOLATION", UnitCount},
		{"gpu_thermal_violation", "GPU 温度限制", "DCGM_FI_DEV_THERMAL_VIOLATION", UnitCount},
		{"gpu_sync_boost_violation", "GPU Sync Boost 限制", "DCGM_FI_DEV_SYNC_BOOST_VIOLATION", UnitCount},
		{"gpu_reliability_violation", "GPU 可靠性限制", "DCGM_FI_DEV_RELIABILITY_VIOLATION", UnitCount},
		{"gpu_board_limit_violation", "GPU Board Limit 限制", "DCGM_FI_DEV_BOARD_LIMIT_VIOLATION", UnitCount},
		{"gpu_low_util_violation", "GPU 低利用率限制", "DCGM_FI_DEV_LOW_UTIL_VIOLATION", UnitCount},
	}
	definitions := make([]Definition, 0, len(items))
	for _, item := range items {
		definitions = append(definitions, gpuGauge(item.name, item.title, item.family, item.unit))
	}
	definitions = append(definitions,
		gpuScoped("gpu_cluster_count", "集群 GPU 数量", "DCGM_FI_DEV_GPU_UTIL", UnitCount, nil, "count"),
		gpuScoped("gpu_cluster_utilization", "集群平均 GPU 利用率", "DCGM_FI_DEV_GPU_UTIL", UnitRatio, nil, "avg"),
		gpuScoped("gpu_cluster_memory_used", "集群已用显存", "DCGM_FI_DEV_FB_USED", UnitBytes, nil, "sum"),
		gpuScoped("gpu_cluster_memory_total", "集群总显存", "DCGM_FI_DEV_FB_TOTAL", UnitBytes, nil, "sum"),
		gpuScoped("gpu_node_utilization", "节点平均 GPU 利用率", "DCGM_FI_DEV_GPU_UTIL", UnitRatio, []string{"node"}, "avg"),
		gpuScoped("gpu_node_memory_used", "节点已用显存", "DCGM_FI_DEV_FB_USED", UnitBytes, []string{"node"}, "sum"),
		gpuScoped("gpu_node_memory_total", "节点总显存", "DCGM_FI_DEV_FB_TOTAL", UnitBytes, []string{"node"}, "sum"),
		gpuScoped("gpu_pod_utilization", "Pod 平均 GPU 利用率", "DCGM_FI_DEV_GPU_UTIL", UnitRatio, []string{"namespace", "pod"}, "avg"),
		gpuScoped("gpu_pod_memory_used", "Pod 已用显存", "DCGM_FI_DEV_FB_USED", UnitBytes, []string{"namespace", "pod"}, "sum"),
	)
	return definitions
}

func gpuScoped(name, title, family string, unit Unit, dimensions []string, aggregation string) Definition {
	definition := Definition{
		Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions: dimensions, SupportsTop: len(dimensions) > 0,
		RequiresTop: len(dimensions) > 0,
	}
	for _, dimension := range dimensions {
		if dimension == "namespace" {
			definition.SupportsNamespace = true
		}
	}
	definition.build = func(matcher string, params buildParams) string {
		selector := namespaceSelector(matcher, params.Namespace)
		groups := append([]string{"zke_cluster_id"}, dimensions...)
		expression := fmt.Sprintf(
			`%s by (%s) (%s{%s,job="dcgm-exporter"})`,
			aggregation, strings.Join(groups, ", "), family, selector,
		)
		if unit == UnitRatio {
			expression = "(" + expression + ") / 100"
		}
		return topk(expression, params.Top)
	}
	return definition
}

func gpuGauge(name, title, family string, unit Unit) Definition {
	return Definition{
		Name: name, Title: title, Kind: KindRange, Unit: unit,
		Dimensions:  []string{"node", "gpu", "pod", "namespace"},
		SupportsTop: true, RequiresTop: true,
		build: func(matcher string, params buildParams) string {
			expression := fmt.Sprintf(
				`max by (zke_cluster_id, node, gpu, pod, namespace) (%s{%s,job="dcgm-exporter"})`,
				family, matcher,
			)
			if unit == UnitRatio {
				expression = "(" + expression + ") / 100"
			}
			return topk(expression, params.Top)
		},
	}
}
