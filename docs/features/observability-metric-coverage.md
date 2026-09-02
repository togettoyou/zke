# 容器监控指标覆盖

本文是 ZKE 容器监控与腾讯云[《容器监控图表指标》](https://cloud.tencent.com/document/product/248/105374)
的逐分类验收基线。参考页最近更新时间为
2025-07-18；本次核对共 27 个分类、465 个图表行、236 个原始或预聚合指标名。

“覆盖”表示图表回答的问题、作用域和单位已在 ZKE 中实现，不要求复制腾讯云的 Dashboard 布局或
云厂商私有记录规则。同一信号在参考页中经常以不同聚合维度重复出现，ZKE 使用一个采集信号和多个
具名查询按 Cluster、Namespace、工作负载类型、工作负载、Pod、容器、节点或 GPU 维度展示。
当前 Console 将 394 个可绘图查询组织为 59 个视图、276 个图表；其余两个目录查询用于集群清单和
采集健康判定。相关曲线使用相同单位时合并到一张图，因此图表数不与参考页的重复行数一一相等。

## 逐分类矩阵

| 参考分类 | 图表行 | ZKE 入口 | 实现方式 |
| --- | ---: | --- | --- |
| 集群监控概览 | 28 | 集群总览、资源监控 | 集群容量、申请、限制、利用率、对象健康和 Top N |
| 集群 Namespace 大盘 | 38 | 集群总览 → Namespace 资源大盘、资源监控 → Namespace | 用量、申请、限制、配额、对象规模、PVC 申请量 |
| API Server（独立集群） | 15 | 核心组件 → API Server | 可用率、读写 SLI、队列、延迟、资源 |
| Controller Manager（独立集群） | 9 | 核心组件 → Controller Manager | 健康、队列、API 请求、延迟、资源 |
| Scheduler（独立集群） | 6 | 核心组件 → Scheduler | 健康、API 请求、调度队列、尝试次数、资源 |
| Kubelet | 24 | 核心组件 → Kubelet | 运行对象、运行时、Pod Worker、存储、Cgroup、PLEG、资源 |
| Proxy | 12 | 核心组件 → Proxy | 健康、规则同步、网络编程、API 请求、资源 |
| 集群节点监控详情 | 23 | 资源监控 → 节点、网络监控、存储监控 | 资源、负载、磁盘、文件系统、网络、Socket、系统状态 |
| 节点 Pod 监控 | 8 | 资源监控 → 节点 Pod 密度、应用监控 → Pod | Pod 数量、申请、用量、限制及对象明细 |
| 工作负载监控概览 | 4 | 应用监控 → 工作负载资源 | CPU/内存用量、申请与限制 |
| Deployment | 26 | 应用监控 → Deployment | 生命周期、副本、CPU、内存、网络、Socket、文件系统 |
| StatefulSet | 25 | 应用监控 → StatefulSet | Generation、生命周期、副本、CPU、内存、网络、Socket、文件系统 |
| DaemonSet | 4 | 应用监控 → DaemonSet | 副本、CPU、内存、网络与文件系统；覆盖参考页并提供额外诊断图 |
| 集群 Pod 监控 | 29 | 应用监控 → Pod 资源、资源监控 → Pod/容器 | 生命周期、重启、资源、内存明细、限流、网络、文件系统、进程与 Socket |
| CoreDNS | 22 | 核心组件 → CoreDNS | 资源、请求维度、响应码、报文大小、延迟、上游、缓存 |
| 集群网络监控 | 13 | 网络监控 → 集群网络、节点网络 | 带宽、包、丢包、TCP 重传和 Socket |
| 命名空间 Pods 网络监控 | 9 | 网络监控 → Namespace Pods 网络 | Namespace 带宽、包和丢包；可继续按 Pod 下钻 |
| 命名空间工作负载网络监控 | 11 | 网络监控 → 工作负载网络 | Namespace 过滤后按工作负载归并 |
| Pod 网络监控 | 8 | 网络监控 → Pod 网络 | 带宽、包、错误与丢包 |
| 工作负载网络监控 | 11 | 网络监控 → 工作负载网络 | 带宽、包、错误与丢包，保留工作负载类型 |
| PVC 存储监控 | 4 | 存储监控 → 持久卷 | 空间和 inode 的已用、容量与利用率 |
| Controller Manager（托管集群） | 10 | 核心组件 → Controller Manager | 使用标准进程/cAdvisor 指标等价实现；端点不可见时明确无数据 |
| Scheduler（托管集群） | 9 | 核心组件 → Scheduler | 使用标准进程/调度器指标等价实现；端点不可见时明确无数据 |
| API Server（托管集群） | 23 | 核心组件 → API Server | 使用标准 API Server 指标等价实现；端点不可见时明确无数据 |
| GPU Cluster | 8 | GPU 监控 → GPU Cluster | 设备数、利用率和显存汇总 |
| GPU Node | 44 | GPU 监控 → GPU Node、GPU 设备明细 | 节点汇总和 DCGM 设备级引擎、链路、时钟、能耗、温度、错误与限制 |
| GPU Pod | 42 | GPU 监控 → GPU Pod、GPU 设备明细 | Pod 资源与 GPU 设备指标，保留 Namespace、Pod 和设备身份 |

## 指标兼容与采集边界

- 腾讯云文档中的 `cluster` 标签由 ZKE Server 根据 Agent 连接身份强制写为 `zke_cluster_id`；客户端
  不能覆盖。图表的“查看查询语句”展示可移植 MetricsQL，省略该内部标签，实际执行仍强制注入。
- `*_cpu_cores`、`*_memory_bytes` 等旧版 kube-state-metrics 指标使用当前通用的
  `kube_pod_container_resource_requests|limits{resource="cpu|memory"}` 等价实现。
- 腾讯云的 `node_namespace_pod_container:*`、`namespace_workload_pod:*`、`pod_core_usage`、
  `pod_mem_usage` 等云厂商记录规则不要求集群安装；ZKE 由 MetricsQL 在查询时从标准指标和 Pod owner
  关系归并。
- kube-state-metrics 采集节点、Pod、工作负载、Job/CronJob、Service、Ingress 和存储对象的状态与
  生命周期指标。Secret 和 ConfigMap 不授予常驻采集器全集读取权限；它们的当前对象信息只能通过
  用户请求时的 Kubernetes 资源 API 和 RBAC 查看，不形成后台时间序列。这是安全边界，不计入采集器
  的“已覆盖”声明。
- 托管 Kubernetes 可能不向租户暴露控制面 `/metrics`。ZKE 不绕过云厂商网络和权限边界；端点不可达
  时图表明确显示无数据，其他监控不受影响。
- GPU 指标自动发现集群中已有的 NVIDIA DCGM Exporter。ZKE 不在没有明确授权的集群中自动安装 GPU
  工作负载。
- 重新安装采集组件才会更新抓取配置；Server 不静默改写集群中已经运行的采集配置。

## 验收要求

- 每个具名查询生成的执行表达式必须能被 MetricsQL 解析。
- 每个图表响应必须同时返回不含 `zke_cluster_id` 的可移植表达式；Console 支持查看和复制。
- Console 中使用的查询名必须存在于 Server 目录；目录查询、采集 allowlist 和文档不得相互漂移。
- 需要真实端点的控制面、CoreDNS 和 GPU 图表必须在无端点时明确说明依赖，不把无数据伪装成零。
