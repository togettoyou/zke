# 容器服务

容器服务是单集群应用。用户进入应用时需要先选择一个 Kubernetes 集群，进入后所有页面和操作均作用于当前集群。

当前已完成 Node 类型化接口、Namespace 管理闭环、五类工作负载类型化后端查询和通用主资源 CRUD 底座：

- `GET /api/v1/clusters/{cluster_id}/nodes`：支持 `limit`、Kubernetes continuation token、Label Selector 和
  Field Selector；
- `GET /api/v1/clusters/{cluster_id}/nodes/{node_name}`：返回 Node 状态、容量、地址、标签、污点、条件和
  Node System Info；
- `GET /api/v1/clusters/{cluster_id}/namespaces` 和
  `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}`：返回 Namespace 列表与详情；
- `POST /api/v1/clusters/{cluster_id}/namespaces` 和
  `DELETE /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}`：执行带 DryRun、确认、幂等键与
  UID/resourceVersion 删除前置条件的 Namespace 创建和删除；
- `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}` 和
  `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}`：
  按明确 Cluster、Namespace 和工作负载类型查询 Deployment、StatefulSet、DaemonSet、Job 或 CronJob 的稳定
  摘要与详情；列表支持 Kubernetes continuation token、Label Selector 和 Field Selector；
- `GET /api/v1/clusters/{cluster_id}/kubernetes/resource-types`：返回目标 Cluster 当前 Discovery 可见的
  内置资源和 CRD 资源目录；
- `GET /api/v1/clusters/{cluster_id}/kubernetes/resources`：按 GVR、Namespace、Selector 和 Kubernetes
  continuation token 查询任意已授权主资源；
- `GET /api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}`：按 GVR、Namespace 和名称读取
  完整对象；
- `POST /api/v1/clusters/{cluster_id}/kubernetes/resources`：创建具名主资源；
- `PUT`、`PATCH`、`DELETE /api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}`：
  更新、四类 Patch 或删除具名主资源；
- 只读接口要求 Session 和目标 Cluster 的 `cluster.read` 权限，每个请求通过独立 QUIC Resource Stream
  交给该 Cluster 的 Agent；
- 写接口另外要求 CSRF、`cluster.resource.create`、`cluster.resource.update` 或
  `cluster.resource.delete`、16 至 128 字符幂等键，以及实际变更的显式确认；
- Agent 使用 Kubernetes dynamic client，只接受 Discovery 声明且作用域、Verb 匹配的主资源 CRUD；
  Discovery 策略缓存有 TTL 和条目上限，Secret 和 Subresource 明确拒绝；
- 支持 DryRun、JSON Patch、JSON Merge Patch、Strategic Merge Patch、Server-Side Apply、
  删除传播策略和 UID/resourceVersion 前置条件；Apply 默认 `force=false`；
- Agent 使用跨 QUIC 重连存活的有界重放缓存抑制同一幂等键重复执行，同键不同请求返回冲突；
- 安装 Manifest 为 Agent ServiceAccount 增加 Node 的 `get`、`list`、`patch`，Namespace 的
  `get`、`list`、`create`、`delete`，以及 Deployment、StatefulSet、DaemonSet、Job 和 CronJob 的完整主资源
  CRUD 权限；其他资源仍需安装方显式增加最小 RBAC。

Node 列表当前通过 Resource Stream 传输完整 Kubernetes 对象，再由 Server 转换成稳定的精简响应；Table
表示尚未实现。

Console 容器服务按资源类别组织：进入应用后先选择一个目标集群，左侧导航当前包含「节点」「命名空间」和
「工作负载」三个资源类别，列表行可下钻到详情页再返回，分页使用 Kubernetes continuation token。目标集群按项目
持久化在浏览器本地，只保存集群标识，且每次都会重新对照该项目当前在线的集群解析——已下线的集群不会被选中。
离线集群仍出现在选择器中但不可选，避免操作者以为集群不存在。命名空间提供 List/Detail/Create/Delete；节点提供
List/Detail 以及停止调度和恢复调度。所有变更都经过权限门控、DryRun 预检、影响展示与二次确认。

节点的调度开关是对 `spec.unschedulable` 的 merge patch，走既有的受控通用 CRUD 路由，不需要专用接口，要求
`cluster.resource.update` 权限。它只阻止新 Pod 被调度到该节点，不驱逐已运行的 Pod；驱逐（drain）需要
`pods/eviction` Subresource，当前协议明确拒绝所有 Subresource，因此尚未支持。

节点和命名空间是集群级资源，工作负载是 namespaced 资源，因此工具栏只在进入「工作负载」时显示 Namespace
作用域选择器。选择器按集群持久化在浏览器本地，同样只保存名称并每次重新对照集群当前返回的 Namespace 解析；
名称不再存在时回退到 `default`，`default` 也不存在时回退到第一个。选择器一次读取该集群的一页 Namespace
（上限 500 个），超出时在状态栏说明列表已截断，完整分页仍在「命名空间」页面。

工作负载后端返回统一元数据、镜像、状态和控制器副本信息，并按具体类型返回 Job 或 CronJob 状态；详情还包含
Selector、容器、条件、更新策略等稳定字段。创建、更新、Patch 和删除继续使用通用资源接口，从而复用现有
`cluster.resource.*` 权限、CSRF、DryRun、显式确认、幂等和审计边界。

Console 工作负载页面在目标 Cluster 和 Namespace 内按类型切换 Deployment、StatefulSet、DaemonSet、Job 和
CronJob，列表展示状态、副本或 Job/CronJob 进度、镜像和创建时间，可下钻到包含副本或 Job/CronJob 状态、
配置、容器、Selector、条件、标签和注解的详情页。该页面当前只读：面向工作负载的创建、伸缩、重启和删除表单
尚未实现。

通用接口返回 Unstructured JSON，并移除 `metadata.managedFields`。Discovery
目录表示 API Server 暴露的资源，不代表 Agent ServiceAccount 已获授权；管理更多内置资源或任意 CR 时，安装方
需要显式扩展该 ServiceAccount 的最小 RBAC，ZKE 无需增加新的资源协议或 HTTP Handler。

仓库包含默认跳过的本地真实集群 E2E。设置 `ZKE_LIVE_KUBERNETES_E2E=1` 后，它使用当前 kubeconfig，通过
真实 QUIC Stream 验证 Namespace、ConfigMap、Deployment、CRD 和自定义资源的 CRUD、四类 Patch、DryRun、
冲突与幂等重放，并使用随机名称和精确清理避免污染日常集群。

后续规划能力包括：

- 集群概览；
- 节点驱逐，以及它依赖的 Subresource allowlist 设计；
- Deployment、StatefulSet、DaemonSet、Job 和 CronJob 的类型化变更表单，包括伸缩、重启与删除；
- Pod 管理、Pod 日志与 Web Terminal；
- Service 与 Ingress；
- ConfigMap 与 Secret；
- PersistentVolume、PersistentVolumeClaim 与 StorageClass；
- Kubernetes Event；
- YAML 查看与编辑；
- 面向具体资源的表单化创建、更新和删除体验。

产品体验将参考成熟 Kubernetes 管理平台的通用实践，但不会以与任何现有平台完全相同为目标。
