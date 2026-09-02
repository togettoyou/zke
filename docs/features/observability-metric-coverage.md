# 容器监控指标覆盖

本文记录 ZKE 对容器监控参考大盘的语义覆盖。参考大盘中的同一原始指标会在多个页面和维度重复出现；ZKE 以具名查询
复用同一个采集信号，再在 Console 按集群、Namespace、工作负载、Pod、容器和设备维度展示，避免为重复图表重复采集。

## 覆盖矩阵

| 参考分类 | ZKE 视图与采集来源 |
| --- | --- |
| 集群监控概览 | 集群总览、资源监控；kubelet resource、kube-state-metrics、node-exporter |
| 集群 Namespace 大盘 | 资源监控 → Namespace；kubelet resource、kube-state-metrics |
| API Server（独立/托管） | 核心组件 → API Server；自动发现 `kube-apiserver` Pod 的 `/metrics` |
| Controller Manager（独立/托管） | 核心组件 → 控制面；自动发现 `kube-controller-manager` Pod 的 `/metrics` |
| Scheduler（独立/托管） | 核心组件 → Scheduler；自动发现 `kube-scheduler` Pod 的 `/metrics` |
| Kubelet | 核心组件 → Kubelet；每个节点的 kubelet `/metrics` |
| Proxy | 核心组件 → 控制面；自动发现 `kube-proxy` Pod 的 `/metrics` |
| 集群节点监控详情 | 资源监控 → 节点、网络监控、存储监控；node-exporter 与 kubelet |
| 节点 Pod 监控 | 资源监控 → 节点 Pod 密度、应用监控 → Pod |
| 工作负载监控概览 | 应用监控 → 工作负载资源 |
| Deployment / StatefulSet / DaemonSet | 应用监控 → 工作负载资源和网络；kube-state-metrics 与 cAdvisor |
| 集群 Pod 监控 | 应用监控 → Pod 资源、网络监控 → Pod 网络；kubelet resource 与 cAdvisor |
| CoreDNS | 核心组件 → CoreDNS；自动发现 `k8s-app=kube-dns` Pod 的 9153 端口 |
| 集群网络监控 | 网络监控 → 节点网络；node-exporter |
| Namespace Pods 网络 | 网络监控 → Pod 网络，Namespace 过滤 |
| Namespace 工作负载网络 | 网络监控 → 工作负载网络，Namespace 过滤 |
| Pod 网络 | 网络监控 → Pod 网络 |
| 工作负载网络 | 网络监控 → 工作负载网络 |
| PVC 存储 | 存储监控 → PVC；kubelet volume stats |
| GPU Cluster / Node / Pod | GPU 监控；自动发现已有 NVIDIA DCGM Exporter 的 9400 端口 |

## 平台差异

- 腾讯云文档中的 `cluster` 标签在 ZKE 中由 Server 根据 Agent 连接身份强制写为 `zke_cluster_id`，客户端不能覆盖；
- `*_cpu_cores` 等旧版 kube-state-metrics 指标在 ZKE 中使用当前通用的
  `kube_pod_container_resource_requests|limits{resource="cpu|memory"}` 等价查询；
- 文档中的 TKE 预聚合指标在 ZKE 查询时由 MetricsQL 通过 Pod owner 关系即时归并，不要求集群安装云厂商记录规则；
- 托管 Kubernetes 可能不向租户暴露控制面 `/metrics`。ZKE 不绕过云厂商边界；端点不可达时该图明确显示无数据，
  其他节点和工作负载监控不受影响；
- GPU 只自动发现集群中已有的 DCGM Exporter。没有 GPU 或没有安装该导出器时，ZKE 不额外部署 GPU 工作负载；
- 重新安装采集组件会更新抓取配置。已有安装不会被 Server 静默改写，以免在集群中产生未确认的采集负载变化。
