package agent

// renderPlatformScrapeJobs discovers standard Kubernetes component exporters
// that already exist in a cluster. ZKE does not install or mutate these
// workloads: it only reads endpoints that are reachable with the collector's
// ServiceAccount. Jobs that are absent simply have no targets, while the
// collector self-metrics make an unreachable discovered target visible.
func renderPlatformScrapeJobs() string {
	return `  - job_name: coredns
    scheme: http
    metrics_path: /metrics
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_k8s_app]
        regex: kube-dns
        action: keep
      - source_labels: [__meta_kubernetes_pod_ip]
        target_label: __address__
        replacement: $1:9153
      - source_labels: [__meta_kubernetes_namespace]
        target_label: namespace
      - source_labels: [__meta_kubernetes_pod_name]
        target_label: pod
      - regex: ^zke_.*$
        action: labeldrop
  - job_name: kube-apiserver
    scheme: https
    metrics_path: /metrics
    tls_config:
      insecure_skip_verify: true
    authorization:
      type: Bearer
      credentials_file: /var/run/secrets/kubernetes.io/serviceaccount/token
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_component]
        regex: kube-apiserver
        action: keep
      - source_labels: [__meta_kubernetes_pod_ip]
        target_label: __address__
        replacement: $1:6443
      - source_labels: [__meta_kubernetes_pod_node_name]
        target_label: node
      - regex: ^zke_.*$
        action: labeldrop
  - job_name: kube-controller-manager
    scheme: https
    metrics_path: /metrics
    tls_config:
      insecure_skip_verify: true
    authorization:
      type: Bearer
      credentials_file: /var/run/secrets/kubernetes.io/serviceaccount/token
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_component]
        regex: kube-controller-manager
        action: keep
      - source_labels: [__meta_kubernetes_pod_ip]
        target_label: __address__
        replacement: $1:10257
      - source_labels: [__meta_kubernetes_pod_node_name]
        target_label: node
      - regex: ^zke_.*$
        action: labeldrop
  - job_name: kube-scheduler
    scheme: https
    metrics_path: /metrics
    tls_config:
      insecure_skip_verify: true
    authorization:
      type: Bearer
      credentials_file: /var/run/secrets/kubernetes.io/serviceaccount/token
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_component]
        regex: kube-scheduler
        action: keep
      - source_labels: [__meta_kubernetes_pod_ip]
        target_label: __address__
        replacement: $1:10259
      - source_labels: [__meta_kubernetes_pod_node_name]
        target_label: node
      - regex: ^zke_.*$
        action: labeldrop
  - job_name: kube-proxy
    scheme: http
    metrics_path: /metrics
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_k8s_app]
        regex: kube-proxy
        action: keep
      - source_labels: [__meta_kubernetes_pod_ip]
        target_label: __address__
        replacement: $1:10249
      - source_labels: [__meta_kubernetes_pod_node_name]
        target_label: node
      - regex: ^zke_.*$
        action: labeldrop
  - job_name: dcgm-exporter
    scheme: http
    metrics_path: /metrics
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_app_kubernetes_io_name, __meta_kubernetes_pod_label_app]
        regex: (nvidia-dcgm-exporter|dcgm-exporter);.*|.*;(nvidia-dcgm-exporter|dcgm-exporter)
        action: keep
      - source_labels: [__meta_kubernetes_pod_ip]
        target_label: __address__
        replacement: $1:9400
      - source_labels: [__meta_kubernetes_pod_node_name]
        target_label: node
      - source_labels: [__meta_kubernetes_namespace]
        target_label: namespace
      - source_labels: [__meta_kubernetes_pod_name]
        target_label: pod
      - regex: ^zke_.*$
        action: labeldrop
`
}
