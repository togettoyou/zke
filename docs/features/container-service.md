# 容器服务

容器服务是单集群应用。用户进入应用时需要先选择一个 Kubernetes 集群，进入后所有页面和操作均作用于当前集群。

当前已完成 Node、Pod、Service、Ingress、Gateway、ConfigMap、PersistentVolume、PersistentVolumeClaim、StorageClass、HorizontalPodAutoscaler，以及可选的 VerticalPodAutoscaler 与 KEDA ScaledObject、
五类策略对象与 Kubernetes RBAC
类型化接口、Namespace 管理闭环、五类工作负载类型化后端管理和通用主资源 CRUD 底座：

- `GET /api/v1/clusters/{cluster_id}/overview`：聚合 Node、Namespace、Pod、PersistentVolume、
  PersistentVolumeClaim 和五类工作负载的状态计数，Node 的 kubelet 版本分布，以及 Node 容量/可分配量、
  非终态 Pod 请求量与卷容量总量；
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
- `POST /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}`：使用稳定的
  容器模板和类型专属字段创建上述五类工作负载，支持 DryRun、显式确认、幂等和审计；
- `PUT /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}`：
  用与创建同形的类型化表单更新上述五类工作负载。请求携带的字段替换对象上的对应部分，表单未建模的字段由
  Server 从当前对象保留；强制要求 UID 与 resourceVersion 前置条件；
- 工作负载详情作用域下的 `scale`、`restart`、`suspend` 和 `resume` 动作：分别支持
  Deployment/StatefulSet 伸缩，Deployment/StatefulSet/DaemonSet 滚动重启，以及 CronJob 暂停和恢复；
- `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}/revisions`：
  返回 Deployment、StatefulSet 或 DaemonSet 记录的历史 Pod 模板，按修订号从新到旧排列，
  并标记哪一条就是当前运行的模板；Job 与 CronJob 返回 `400 workload_revisions_unsupported`；
- `POST /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}/rollback`：
  把上述三类工作负载回滚到指定修订记录的 Pod 模板，强制要求 UID 与 resourceVersion 前置条件，
  目标修订就是当前模板时返回 `409 workload_revision_unchanged`；
- `DELETE /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}`：
  删除上述五类工作负载，并强制要求 UID 删除前置条件；
- `GET`、`POST /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/networking/{network_resource}` 和
  `GET`、`PUT`、`DELETE /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/networking/{network_resource}/{network_name}`：
  按明确 Cluster、Namespace 和 `services`、`ingresses` 或 `gateways` 类型管理 Service、Ingress 与 Gateway；
  列表支持 Kubernetes continuation token、Label Selector 和 Field Selector，写操作沿用 DryRun、确认、幂等、
  UID/resourceVersion 前置条件与审计链路；
- `GET`、`POST /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/configmaps` 和
  `GET`、`PUT`、`DELETE /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/configmaps/{config_map_name}`：
  固定管理 `core/v1 ConfigMap`；列表仅返回键名和大小统计，详情返回完整 `data` 与标准 Base64
  `binary_data`，写操作包含 1 MiB 内容校验、immutable 保护、DryRun、确认、幂等、UID/resourceVersion
  并发保护与审计；
- `GET`、`POST /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/secrets` 和
  `GET`、`PUT`、`DELETE /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/secrets/{secret_name}`：
  固定管理 `core/v1 Secret`；读取要求 `cluster.secret.read`，写入要求 `cluster.secret.manage`，
  两者都不由 `cluster.read` 或 `cluster.resource.*` 蕴含；列表只返回键名、大小、类型和元数据，
  取值仅由单对象详情返回；属于 ZKE 安装本身的 Secret 既不列出也不可读写；
- `GET`、`POST /api/v1/clusters/{cluster_id}/storage/{storage_resource}` 和对应单对象
  `GET`、`PUT`、`DELETE`：固定管理集群级 `persistentvolumes` 或 `storageclasses`；
- `GET`、`POST /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/storage/persistentvolumeclaims` 和对应
  单对象 `GET`、`PUT`、`DELETE`：固定在明确 Namespace 管理 PVC；三类存储资源列表均支持 Kubernetes
  continuation token 和 Selector，写操作沿用 DryRun、确认、幂等、UID/resourceVersion 与审计；
- `GET`、`POST /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/autoscaling/horizontalpodautoscalers` 和
  对应单对象 `GET`、`PUT`、`DELETE`：固定管理 `autoscaling/v2 HorizontalPodAutoscaler`；类型化创建和更新
  只接受同一 Namespace 中的 Deployment/StatefulSet，以及 Resource/ContainerResource 指标和伸缩行为；
- `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/autoscaling/horizontalpodautoscalers/{hpa_name}/metrics-trend`：
  从 HPA status 采样当前指标和副本数，返回 Server 进程内最近一小时、最多 240 点的轻量趋势；每个 HPA 至少间隔
  5 秒采样，Console 每 15 秒刷新，Server 重启会清空这段运行时历史；
- `GET`、`POST /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/autoscaling/verticalpodautoscalers` 与
  对应单对象 `GET`、`PUT`、`DELETE`：类型化管理可选的 `autoscaling.k8s.io/v1 VerticalPodAutoscaler`，支持
  Deployment、StatefulSet、DaemonSet 目标、明确的更新模式、容器资源边界、建议值和 Condition；
- `GET`、`POST /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/autoscaling/scaledobjects` 与对应
  单对象 `GET`、`PUT`、`DELETE`：类型化管理可选的 `keda.sh/v1alpha1 ScaledObject`，支持 Deployment、
  StatefulSet 目标、副本区间、轮询/冷却时间、触发器、认证引用和控制器状态；VPA/KEDA CRD 未安装时列表以
  `available=false` 返回，不把可选组件缺失显示成容器服务故障；
- 集群级 `/authorization/{authorization_resource}` 与命名空间级
  `/namespaces/{namespace_name}/authorization/{authorization_resource}` 提供 ServiceAccount、Role、ClusterRole、
  RoleBinding、ClusterRoleBinding 的类型化 List/Detail/Create/Update/Delete；读取和写入分别要求独立的
  `cluster.rbac.read` 与 `cluster.rbac.manage`；
- `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods` 和
  `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}`：按明确 Cluster 和
  Namespace 返回 Pod 稳定摘要与详情，列表支持 Kubernetes continuation token、Label Selector 和
  Field Selector；
- `DELETE /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}`：删除明确作用域内的
  Pod，强制要求 UID 前置条件，并支持 DryRun、显式确认、幂等、删除传播策略和审计；
- `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}/describe`、
  `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}/describe`、
  `GET /api/v1/clusters/{cluster_id}/nodes/{node_name}/describe`、
  `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/networking/{network_resource}/{network_name}/describe`、
  `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/storage/{storage_resource}/{storage_name}/describe`、
  `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/autoscaling/horizontalpodautoscalers/{hpa_name}/describe`、
  `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/policies/{policy_resource}/{policy_name}/describe`
  和 `GET /api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}/describe`：在一次请求内返回对象、
  只属于该对象的 Kubernetes Event 快照，以及由两者共同支持的诊断结论；工作负载还会沿 owner 链返回它拥有的
  控制器、Pod、Pod 模板引用的 PVC 及各自的结论，Node 还会返回已分配的非终止 Pod 与 requests 汇总，Service
  会返回 EndpointSlice 端点统计和 selector 匹配的后端 Pod，Ingress 会返回所引用 Service、端口与端点状态，HPA
  会返回控制器 Condition 与已知类型化伸缩目标的状态，ResourceQuota 与 PodDisruptionBudget 会返回额度或中断
  保护状态；这些接口
  都要求同时持有 `cluster.read` 与
  `cluster.event.read`，Event 读取写入与 Event 流一致的审计记录；
- `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}/logs`：要求当前 Pod UID、
  明确容器和专用 `cluster.pod.logs.read` 权限，支持默认最近 200 行的有界快照以及 `follow=true` 实时流；
- `POST /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}/terminal-sessions`：要求当前
  Pod UID、明确容器、`cluster.pod.exec`、CSRF、幂等键和显式确认，创建与用户及登录 Session 绑定的一次性票据；
- `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}/terminal-sessions/{session_id}`：
  通过同源 `zke.pod-exec.v1` WebSocket 传输 stdin、stdout、stderr、resize 和 exit 帧；
- `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}/terminal-recordings` 与
  `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}/terminal-recordings/{recording_id}`：
  使用独立 `cluster.pod.terminal_recording.read` 权限列出和读取显式选择录制的终端输出；
- `GET`、`POST /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/policies/{policy_resource}` 与
  `GET`、`PUT`、`DELETE /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/policies/{policy_resource}/{policy_name}`：
  ResourceQuota、LimitRange、NetworkPolicy 和 PodDisruptionBudget 的类型化 CRUD；
- `GET`、`POST /api/v1/clusters/{cluster_id}/policies/{policy_resource}` 与
  `GET`、`PUT`、`DELETE /api/v1/clusters/{cluster_id}/policies/{policy_resource}/{policy_name}`：
  集群级 PriorityClass 的类型化 CRUD，命名空间级与集群级互相拒绝对方的作用域；
- `GET /api/v1/clusters/{cluster_id}/kubernetes/resource-types`：返回目标 Cluster 当前 Discovery 可见的
  内置资源和 CRD 资源目录，并逐条标记该资源是否由 CustomResourceDefinition 提供；Kubernetes Discovery
  本身不携带这一事实，Agent 需要另外读取 CRD 列表，读不到时目录用 `custom_resources_known=false`
  说明该判定不可用，而不是把所有资源当成内置资源；
- `GET /api/v1/clusters/{cluster_id}/kubernetes/resources`：按 GVR、Namespace、Selector 和 Kubernetes
  continuation token 查询任意已授权主资源；
- `GET /api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}`：按 GVR、Namespace 和名称读取
  完整对象；
- `POST /api/v1/clusters/{cluster_id}/kubernetes/resources`：创建具名主资源；
- `PUT`、`PATCH`、`DELETE /api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}`：
  更新、四类 Patch 或删除具名主资源；
- `GET`、`PUT /api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}/yaml`：读取或更新
  单个主资源的完整 YAML；读取要求 `cluster.read`，更新要求 `cluster.resource.update`、CSRF 和幂等键；
- `POST /api/v1/clusters/{cluster_id}/kubernetes/manifests/apply` 与 `.../manifests/delete`：以多文档 YAML
  清单批量应用或删除对象，等价于 `kubectl apply -f` 与 `kubectl delete -f`；正文为 `application/yaml`，
  最大 4 MiB 且文档数量有上限，要求 CSRF 与幂等键，`dry_run=true` 只规划与预检、实际执行要求 `confirm=true`；
  所需权限由正文逐文档判定而不是由路由决定，见下文；
- 只读接口要求 Session 和目标 Cluster 的 `cluster.read` 权限，每个请求通过独立 QUIC Resource Stream
  交给该 Cluster 的 Agent；
- 写接口另外要求 CSRF、`cluster.resource.create`、`cluster.resource.update` 或
  `cluster.resource.delete`、16 至 128 字符幂等键，以及实际变更的显式确认；
- Agent 使用 Kubernetes dynamic client，只接受 Discovery 声明且作用域、Verb 匹配的主资源 CRUD；
  Discovery 策略缓存有 TTL 和条目上限，Secret 和 Subresource 明确拒绝；Kubernetes 授权资源只能使用专用
  `/authorization` API，不能借通用 Resource/YAML API 绕过专用权限；Secret 与授权五类各有独立的 YAML
  路由，见下文，它们不放宽通用接口的拒绝，而是挂在各自的权限上；Namespace 的读取与更新照常走通用接口，
  但 Create、Delete 与 Patch 被排除——它们属于 `cluster.namespace.manage`，见下文；
- 支持 DryRun、JSON Patch、JSON Merge Patch、Strategic Merge Patch、Server-Side Apply、
  删除传播策略和 UID/resourceVersion 前置条件；Apply 默认 `force=false`；
- YAML 更新仅接受不超过 4 MiB 的 `application/yaml` 单文档，不接受 Alias、Anchor、重复字段或
  YAML-only 类型；Server 在更新前核对 URL/GVR/Namespace 与 `apiVersion`、`kind`、名称、UID 和
  `resourceVersion`，DryRun 无需确认，实际写入要求 `confirm=true`。成功响应仍为 `application/yaml`，
  错误使用统一 JSON 信封；审计不记录 YAML 正文；
- Agent 使用跨 QUIC 重连存活的有界重放缓存抑制同一幂等键重复执行，同键不同请求返回冲突；只有可能改变集群
  状态的结果会占用幂等键：DryRun 与 Kubernetes 以 4xx 拒绝的写入都没有落地，因此不占用，被拒绝后改正内容
  再次提交是一次新请求而不是冲突；5xx、超时和取消这些 Agent 无法判定结果的失败仍然占用该键，那正是重放缓存
  存在的场合；
- 安装 Manifest 为 Agent ServiceAccount 增加 Node 的 `get`、`list`、`update`、`patch`，Namespace 的
  `get`、`list`、`create`、`update`、`delete`，Pod 的 `get`、`list`、`update`、`delete`，以及 Deployment、StatefulSet、
  DaemonSet、Job、CronJob、Service、Ingress 和 Gateway 的完整主资源 CRUD 权限，以及 ConfigMap、PV、PVC、
  StorageClass、HorizontalPodAutoscaler、ResourceQuota、LimitRange、NetworkPolicy、PodDisruptionBudget、
  PriorityClass、ServiceAccount 及四类 Kubernetes RBAC 资源的
  `get`、`list`、`create`、`update`、`delete`，`apps/v1 replicasets` 与 `apps/v1 controllerrevisions` 的只读
  `get`、`list`（工作负载修订历史的来源；回滚写回的是工作负载本身，不需要创建、修改或清理修订对象），
  Secret 的同五个动词（不含 `watch`；Agent 只在专用 Secret 接口的
  请求上、且目标不是自身命名空间时才会执行），`apiextensions.k8s.io/v1 customresourcedefinitions` 的只读
  `get`、`list`（仅用于判定哪些资源来自 CRD，不含定义或修改 CRD 的能力），并单独授予 `pods/log` 的 `get`
  和 `pods/exec`、`pods/eviction` 的 `create`；后者只能经专用 Drain 接口与 Agent 的精确 allowlist 使用，
  其他资源也需安装方显式增加最小 RBAC。

Node 列表当前通过 Resource Stream 传输完整 Kubernetes 对象，再由 Server 转换成稳定的精简响应；Table
表示尚未实现。

集群概览后端复用现有 Resource Stream，并发但有上限地读取 Node、Namespace、Pod、PersistentVolume、
PersistentVolumeClaim 以及 Deployment、StatefulSet、DaemonSet、Job、CronJob；Server 不直接访问 Kubernetes
API。各部分分别分页，每部分最多读取 10000 个对象，因此概览是最终一致的聚合快照，不是同一个 Kubernetes
`resourceVersion` 下的原子视图。部分查询失败或达到上限时，接口仍返回成功响应，并通过 `partial` 和不含敏感
正文的 `issues` 标明受影响部分；只有所有部分都失败时才返回整体错误。CPU 以 millicores、内存与存储以 bytes
返回，Pod requests 按 Kubernetes 调度语义统计非终态 Pod，包含 init container、restartable init container、
Pod-level resources 和 overhead，不表示实时利用率。接口使用 `cluster.read`；Warning Event 仍通过现有 Event
API 和独立的 `cluster.event.read` 权限读取，避免概览扩大 Event 权限。

节点部分同时按 kubelet 版本计数。升级中的集群会出现多个版本，而版本偏斜决定了哪些 API 可以安全使用；
该字段来自本来就要读取的 Node 摘要，不增加查询。存储部分只返回计数与容量总量，不返回比例：CPU 与内存有
Kubernetes 报告的可分配量作分母，卷容量没有——动态制备下的可用容量由后端存储决定，API 不携带这个上限。
PersistentVolume 返回所有卷 `spec.capacity.storage` 之和（不分阶段：已释放的卷仍占用后端存储），
PersistentVolumeClaim 返回 `spec.resources.requests.storage` 之和，即申请量而非实际制备量，与本页其余
requests 口径一致。

一份完整快照在 Server 内按 Cluster 缓存 15 秒，窗口内的重复请求直接返回同一份结果，`generated_at` 因此
可能早于当前时间。这是有意的：概览是每个操作者进入应用时的落地页，而一次概览是对集群的十次完整列举，也是
本应用最贵的读取。缓存按 Cluster 定键而与调用者无关——本响应描述的是 Cluster 本身，且每个请求都独立校验
该 Cluster 的 `cluster.read`；返回给调用方的是副本，不是缓存中的同一份对象。含失败部分的结果不进入缓存：
操作者按刷新正是为了确认那个部分是否仍不可用，用失败本身回答这个问题等于关掉了刷新按钮；只达到条目上限的
结果会进入缓存，因为重新列举一个数不完的集群只会在同样的代价下得到同样的上限。缓存条目数有上限，超出时
该 Cluster 不缓存，而不是挤掉别人正要读的快照。窗口内并发到达的首批请求仍会各自列举一次集群——缓存合并的
是完成之后的读取，不是同时在途的读取。

Console 概览是容器服务的默认落地页，也是左侧导航第一项：操作者进入应用时通常还不知道该打开哪个资源类别。
页面展示节点、命名空间、Pod 和工作负载的计数与状态分布、五类工作负载的分类计数、PersistentVolume 与
PersistentVolumeClaim 的计数与容量总量、节点的 kubelet 版本分布，以及 CPU、内存和 Pod 三项的请求量对
可分配量。这些都是计数和容量，没有趋势也没有多序列比较，因此用数字和量条呈现而不是图表；量条只在请求量接近
或超过可分配量时改用警告和危险色，且数值始终以文字同时给出，不靠颜色单独表意。存储没有量条，理由与后端返回
计数的理由相同：可用容量没有可读的分母。版本分布出现多个版本时页面只陈述这一事实而不着色——运行两个版本正是
升级过程中的正常形态，不是故障。工具栏显示 `generated_at` 并提供刷新按钮，说明这是聚合快照；服务端在短窗口内
返回同一份快照，因此连续两次刷新可能得到相同的 `generated_at`，这也正是这个时间戳要显示出来的原因。
`partial` 为 true 时在顶部列出受影响的部分及原因，避免把偏低的计数当成真实值。概览不展示 Warning Event：
Event 接口按 Namespace 定域，且需要独立的 `cluster.event.read`，跨命名空间聚合不在本接口范围内。

概览上的每个计数都可以点开对应的列表：节点、命名空间、Pod、工作负载四张卡片进入各自的分区，工作负载分布的每一行
进入「工作负载」并停在该类型的标签页，存储的两行进入「存储」的对应标签页。这不是装饰——概览之所以是落地页，
理由就是操作者此时还不知道该打开哪个类别，而一个不能把人送过去的页面并没有回答这个问题。可点击的卡片带一个
箭头图标、可点击的行末尾带一个 `>`，因此「这里能点」是看出来的而不是试出来的。

其中 Pod、工作负载和 PersistentVolumeClaim 的目标列表按 Namespace 定域，而概览统计的是整个集群，所以进去以后
看到的数会比卡片上的小。这一点写在点击前的 `title` 里而不是解释在点击后：跳转仍然去的是正确的列表，操作者在那里
换一个命名空间即可，但一个默不作声的落差会被读成计数错了。状态分布中的单个状态不做跳转——列表接口目前没有按状态
筛选的入口，一个跳过去却不筛选的链接说的是它做不到的事。

进入分区时携带的标签页只作为初始值使用一次，之后标签页归该分区自己的状态所有；从左侧导航进入则不携带，因为导航
说的是一个类别而不是其中某一类。这两个分区在同一时刻只挂载一个，因此被跳转到的分区本来就是新挂载的，不需要为了
换标签页而重置它已有的状态。

Console 容器服务按资源类别组织：进入应用后先选择一个目标集群，左侧导航当前包含「概览」「节点」「命名空间」
「工作负载」「Pod」「服务与路由」「配置管理」「存储」「自动伸缩」「策略管理」「授权管理」「资源对象浏览器」
「YAML 清单」和「事件」十四项，默认落在「概览」。「资源对象浏览器」排在全部类型化类别之后、「事件」之前：
它是上面这些类别没有建模的类型的兜底入口，读起来该是这份资源清单的收尾而不是其中一项；「YAML 清单」紧随其后，
是同一件事的写入侧——浏览器读任意类型，它写任意类型，两者都不从属于任何单个资源类别；「事件」仍在最后，
它根本不是一个资源类别，而是关于上面那些资源的一条流。列表行可下钻到详情页再返回，分页使用
Kubernetes continuation token，并与 offset 分页一样固定渲染在表格下方。目标集群按项目
持久化在浏览器本地，只保存集群标识，且每次都会重新对照该项目当前在线的集群解析——已下线的集群不会被选中。
离线集群仍出现在选择器中但不可选，避免操作者以为集群不存在。命名空间提供 List/Detail/Create/Delete 与配额管理，
其中创建与删除要求 `cluster.namespace.manage`，配额管理与 YAML 编辑仍是 `cluster.resource.*`；
节点提供 List/Detail 以及停止调度和恢复调度。所有变更都经过权限门控、DryRun 预检、影响展示与二次确认。
Console 对这一步统一使用「DryRun 预检」：校验成功显示「DryRun 预检已通过」；请求已经返回但存在静态阻断或
PDB 阻断时显示「DryRun 预检已完成，存在阻断项」，不得用「已通过」掩盖无法继续执行的结果。

列表页不再有标题和说明段：导航栏已经写明资源类别，工具栏已经写明目标集群与命名空间，标题只是把两者再抄一遍，
而说明段占据的是每一页都要让出的高度。各分区的操作——刷新、创建按钮、概览的生成时间、事件的实时跟随——通过一个
插槽投放到工具栏行尾，由 `AppShell` 提供 DOM 容器、分区自己 portal 进去，因此工具栏归 Shell 所有而按钮归知道
它含义的分区所有。

从列表进入的视图——详情、创建表单、YAML、日志、终端——都有自己的一行页头，位于工具栏和工作区之间，同样由
`AppShell` 提供容器、视图 portal 进去。左边是一个返回箭头和当前对象的名称，右边是对这个对象的操作（编辑、
YAML、伸缩、删除、刷新等），整行不随内容滚动。三处理由：详情页有多长取决于对象有多长，跟随内容滚动的头部会
把出口和全部操作一起带走，而读到底部的操作者恰恰是最需要它们的人；这些按钮不属于工具栏，工具栏说的是「正在
看哪个集群和命名空间」，本身已经有两个选择器那么宽，再塞四个按钮会把它挤成两行，反而让返回更远；返回在名称
之前而不是行尾，是因为它在每个界面上都是同一个动作，安静地说一次就够。
下钻视图的滚动内容末端由 `AppShell` 统一保留 16px 安全间距；间距使用位于内容之后的实际占位元素，而不是滚动
容器自身的 bottom padding，确保长详情和命名空间配额表滚到底时最后一张卡片不会贴住窗口边缘。

页头右侧的操作在所有详情页按同一次序排列：YAML 在最前，中间是随对象而定的动作（历史版本、编辑、配额管理、
日志、终端、伸缩、滚动重启、暂停），删除在最后。次序本身就是说明——最左边的只读，越往右影响越大，最右边那个收不回来；
如果十余个分区各排各的，操作者每进一个页面都要重新找一次删除在哪，而找错的代价并不对称。节点没有删除入口，
行尾留给停止调度：那是该页面上影响面最大的动作。Secret 与 RBAC 对象同样从 YAML 开始；不可变的 Secret 与属于
ZKE 自身的授权对象以只读方式打开，理由分别见下文。

支持删除的对象在详情页都有删除按钮，与列表行的删除入口走同一条确认链路：先服务端 DryRun 预检，再输入对象
名称确认，请求携带打开页面时的 UID 与 resourceVersion。删除成功后详情页退回列表，因为它正在展示的对象已经
不在了。确认对话框和仍以对话框形式存在的编辑表单都由分区本体渲染，不写在列表那条分支里：它们从列表和详情页
都能打开，而只存在于列表分支的 JSX 在详情页上根本不会出现——按钮看起来没有反应，直到返回列表才弹出来。删除按钮在列表行和页头都只用危险色描边而不填充：一列纯红按钮会让「读」这件事让位于「删」，而这个
操作真正的护栏是确认对话框；着色只是为了让它和旁边的按钮不是同一个颜色。ZKE Agent 自身的 RBAC 对象受保护，
按钮出现但禁用，并用提示说明原因——一个不解释的禁用按钮读起来像是坏了。

页头出现时工具栏整行隐藏，两者位置、内边距和高度一致，因此进入对象不会让页面在光标下抖动，同一时刻也只有一行
框架。工具栏属于列表——选择器决定列表显示什么，插槽放的是列表的操作——而此时列表并不在场；在打开的对象之上切换
命名空间，只会让页面转而向另一个集群索取同名对象。选择器让出的作用域改为以文字形式出现在对象名之后（`集群 ·
命名空间`）：同名对象在每个集群里都可能存在，一个不写明集群的详情页是读不得的。

每个列表分区在工具栏中都有刷新按钮，请求进行中时按钮禁用并旋转。Kubernetes 对象会在 Console 不知情的情况下
变化，而列表读取一次后按缓存有效期保留，此前唯一的重新读取方式是离开分区再回来——那依赖的是缓存过期，与操作者
想做的事无关。详情页不显示该按钮：它属于列表，而列表此时并未展示。

所有类型化创建与编辑表单都占据整个应用视图而不是弹窗，并按同一条规则报告校验：任一时刻只报最靠前的那一个问题，
消息渲染在能够修正它的那个分区里，底部按钮旁只说明是哪个分区拦住了提交（「「基本信息」中还有需要修正的项。」）。
工作负载与服务与路由先这样做，配置管理（ConfigMap、Secret）、存储（PV、PVC、StorageClass）、自动伸缩（HPA）、
策略管理（五类）与授权管理（五类）现在都一致。此前它们只是禁用按钮：表单比屏幕长，一个没有原因的禁用按钮要求
操作者逐个字段重读一遍，而拦住提交的往往不是他们正看着的那个。把所有问题一次列在表单末尾同样不行——那是一份
需要自己映射回字段的清单。按分区顺序报告，则修正的动作是顺着表单往下走而不是来回跳。

自动伸缩、策略管理和授权管理原本是弹窗，现在与其余表单一样是页面。理由与工作负载表单相同：一个带多条指标和两个
方向伸缩策略的 HPA、一份逐行读的配额、一条带若干规则的网络策略、一个带多条规则的 Role，都比盖在列表上的盒子高，
而填表期间下面那张列表本来也用不上。从详情页进入编辑时详情仍在其下，因此离开表单回到的是刚才在读的那个对象。
策略管理的分区随类型而变，其中 LimitRange 还按限制项逐个编号，因此这三个表单里问题携带的是分区标题而不是固定的
键；标题由同一处常量和一个按序号生成标题的函数产出，读写两侧都走它，避免标题和显示它的分区各说一套。

对象整体的限制不进这条链路，而是报在它被度量的地方：1 MiB 上限就显示在按钮旁的合计字节数附近，因为没有哪个分区
单独为它负责。非阻塞的提示也留在自己的分区里（例如 Secret 的「取值看起来已经是 Base64」）。

有几条校验刻意比 Kubernetes 更早拦截，因为 API Server 的说法到得晚且不指向具体那一行：Role 不能声明非资源 URL
（那是 ClusterRole 才有的作用域），一条 RBAC 规则不能同时覆盖资源和非资源 URL，NetworkPolicy 的 IP 段来源必须带
前缀长度，按标签选择的来源必须至少有一个标签否则它不选中任何对象，以及配额与限制范围里「填了取值却没填资源名」
的那一行——提交时它会被静默丢弃，操作者只会看到自己填过的限制事后不在那里。另有一条报在别处：NetworkPolicy 取消
勾选某个方向后，该方向已写的规则会被 Kubernetes 接受并忽略，这条消息出现在「策略方向」而不是规则分区里，因为取消
勾选的同时那个分区已经不在屏幕上了。

编辑表单的禁用另有一种原因，也一并说明：存储的三类编辑各自只提交一个字段，因此当那个字段与当前对象一致时，按钮
的禁用理由是「没有需要提交的改动」而不是「有需要修正的项」——一次什么都没改的写入只会留下一条审计记录。这类消息
只显示在它所属的分区，不再在底部重复：编辑视图只有一个可改字段，那个分区就在按钮上方。PVC 扩容是否真的在增大仍
由 Server 判定（缩容在发往 Agent 前即被拒绝）：比较两个 Kubernetes quantity 需要一个单位解析器，在前端另写一个
可能与真正做判定的那个不一致。

地址在列表和详情中都点击即复制，与标识列已有的交互同源：节点的内网 IP 与各类地址、Pod IP 与宿主 IP、Service
的 ClusterIP、Ingress 与 Gateway 的已分配地址。这些值是被带到别处使用的——一次 curl、一条排查命令——而在表格
单元格里用指针选中一段 IP 既费事又容易差一个字符。悬停和键盘聚焦时出现复制图标、底色和边框，复制后短暂显示
对勾：`--surface-muted` 与卡片底色只差两级灰，单靠底色在一页纯文本里读不出「这里可以点」。图标只在悬停、聚焦
或已复制时占位，因此静止状态下不会把每个值的右边缘撑开。`title` 写明将要复制的内容，因此屏幕上分开显示的
地址与端口可以作为一个整体复制。ClusterIP 为 `None` 时保持纯文本：那是「没有地址」的说法，不是一个地址。

节点的调度开关是对 `spec.unschedulable` 的 merge patch，走既有的受控通用 CRUD 路由，要求
`cluster.resource.update`。节点 Drain 使用独立的 `cluster.node.drain`（默认只授予 admin）：先按 Node UID
复核并用 `spec.nodeName` 一次列全 Pod，超过 500 个或出现下一页时拒绝执行；Mirror、DaemonSet 和终止中的 Pod
跳过，无控制器 Pod 与 emptyDir 默认阻断整个操作，必须分别显式接受才会继续。无阻断项后以带 UID test 的 JSON
Patch 停止调度，再为每个 Pod 提交携带 UID precondition 的 `policy/v1 Eviction`。PDB 的 429 逐 Pod 报告为
`pdb_blocked`，不回退成 DELETE；DryRun 同时覆盖 cordon 与 eviction。Agent 只对 core/v1 Pod 的 create eviction
组合接受专用协议位，通用 Resource、YAML 与 Manifest 路径无法设置该位，因此新增 Kubernetes RBAC 不会成为任意
Subresource 通道。响应中的 `evicted` 表示 API Server 已接受驱逐请求，不虚构 Pod 已越过终止宽限期；需要确认
节点已经为空时重新读取节点诊断中的已分配 Pod。Console 将清单分类得到的候选显示为“计划驱逐”；存在无控制器
Pod 或 emptyDir 等静态阻断时，明确说明尚未停止调度、也未发送 Eviction，并要求先接受对应风险后才能重新预检。
PDB 等可能随副本状态恢复的动态阻断仍可直接重新执行 DryRun。

命名空间的配额管理是同一批 `policies/resourcequotas` 类型化接口的另一个视图，不是新的后端能力：入口在命名空间
列表行和详情页，页面把 `core/v1 ResourceQuota` 的 `hard` 按计算资源配额、存储资源限制和其他资源限制三组展开成
固定字段，逐项显示该项的已用量与限额，留空即该项不受限制——ResourceQuota 中没有表示「无限制」的取值，不限制就是
这个键不存在。CPU 以核、内存与存储以 Gi、其余以个为单位显示；未被修改的字段按 Kubernetes 返回的原始字符串原样
提交，因此改动一项不会把其他项换算成另一种写法。表单未建模的配额键——GPU 等设备资源、其他
`count/<resource>.<group>`——按当前值原样保留并在页面上说明，而不是被一次没有提及它们的更新删掉。

该页面只在命名空间恰好有一个不带 scope 的 ResourceQuota 时可编辑。Kubernetes 会同时执行一个命名空间中的所有
ResourceQuota，带 `scopes` 或 `scopeSelector` 的配额又只统计其中一部分对象，因此命名空间有多个配额对象、或唯一
的配额带作用域时，页面按对象只读展示并指向「策略管理 → ResourceQuota」，不把它们合并成一份无法说明数字归属的
表单。命名空间原本没有配额时，保存会创建名为 `zke-namespace-quota` 的对象；把所有字段清空并保存则是删除该对象，
需要输入对象名称确认。创建、更新和删除复用既有链路：`cluster.resource.create/update/delete`、CSRF、DryRun 预检、
显式确认、幂等键与审计，更新和删除携带打开页面时的 UID 与 resourceVersion。

页面只发一个请求，而且只能是列表请求：目标对象的名称事先并不知道。命名空间可能没有配额、有一个由操作者或
kubectl 以任意名称创建的配额，也可能有多个；`zke-namespace-quota` 只是本页自己创建时使用的名称，直接按该名称
读取会把所有配额来自别处的命名空间报成「没有配额」。列表同时回答了有几个、那一个叫什么，以及编辑所需的
`hard`、`used`、`scopes` 与 `scope_selector`。写入成功后列表失效重取，新的 resourceVersion 会让表单重新挂载并
重新固定身份，因此连续两次保存不会用掉同一个版本号。

节点和命名空间是集群级资源，因此工具栏只在当前分区确实按 Namespace 定域时才显示 Namespace 作用域选择器——
工作负载、Pod、服务与路由、配置管理、自动伸缩、事件始终显示，存储、授权管理、策略管理按当前标签页是命名空间级
还是集群级决定，其余分区不显示一个什么都不限定的控件。选择器按集群持久化在浏览器本地，同样只保存名称并每次重新对照集群当前返回的 Namespace 解析；
名称不再存在时回退到 `default`，`default` 也不存在时回退到第一个。选择器一次读取该集群的一页 Namespace
（上限 500 个），超出时在状态栏说明列表已截断，完整分页仍在「命名空间」页面。

切换 Namespace 不会重置分区内的位置。每个分区按目标集群定键，且只按集群：换集群意味着窗口指向了另一套基础设施，
此前看的东西没有一样还成立；换 Namespace 不是。分区自己保留的状态只有「打开的是哪个标签页」和「列表停在第几页」，
而后者自行重置——continuation token 只对签发它的那个列表有意义，因此每个列表本来就把 Namespace 算进了分页的重置键。
把 Namespace 也算进分区的键，唯一效果是连标签页一起丢掉：在 DaemonSet 上选一个命名空间会跳回 Deployment。
按命名空间定域的状态不会因此变陈旧——详情页、创建/编辑表单和 YAML 编辑器都会立起页头，而页头出现时 Shell 隐藏整行
工具栏，选择器根本不在屏幕上；确认弹窗是模态的，效果相同。「事件」是唯一的例外，仍按 Namespace 定键：它保留的是
该命名空间事件的累积流，重挂正是丢弃它们的正确方式，而它没有标签页可丢。

工作负载后端返回统一元数据、镜像、状态和控制器副本信息，并按具体类型返回 Job 或 CronJob 状态；详情还包含
Selector、容器、条件、更新策略等稳定字段。常用变更已提供类型化接口，底层继续复用通用 Patch/Delete
执行链路与 `cluster.resource.*` 权限、CSRF、DryRun、显式确认、幂等和审计边界。滚动重启将
`Idempotency-Key` 的 SHA-256 摘要写入 Pod Template 的 `zke.io/restart-request` 注解，使相同请求重试产生
完全相同的补丁；删除必须携带当前对象 UID，避免误删同名重建对象。

修订历史与回滚只对 Deployment、StatefulSet 和 DaemonSet 提供，因为只有这三类控制器由 Kubernetes 记录历史：
Deployment 的历史是它拥有的 ReplicaSet（修订号来自 `deployment.kubernetes.io/revision` 注解），StatefulSet
与 DaemonSet 的历史是 ControllerRevision（修订号是对象上的 `revision` 字段）。Job 跑一次就结束，CronJob
产出的是 Job，两者都没有可回滚的上一版。Server 先读取工作负载本身，用它的 Pod Selector 查询修订对象，再按
owner UID 二次过滤——同一 Namespace 中两个控制器可能选中同一批 Pod，标签相同不等于归属相同，而按 UID 匹配还
避免同名重建的控制器借用旧历史。单次查询只取一页（上限 500 条），超出时以 `truncated` 说明返回的不是全部修订。

回滚只写回 `spec.template`。对这三类控制器而言，修订恰恰是在 Pod 模板变化时产生的，模板就是该修订记录的全部；
副本数、更新策略以及对象自身的标签和注解从来不属于它，因此保持不变——否则一次回滚会顺带撤销没人提起过的扩容或
配额调整。写回的模板按记录原样落回对象，不先解码成 Server 编译期的 Pod 类型：那会静默丢掉新版本 Kubernetes
才有的字段，而一次悄悄删字段的回滚正是它绝不能做的事。ReplicaSet 模板上的 `pod-template-hash` 标签在写回前
移除（它标识 ReplicaSet 而不是 Deployment），ControllerRevision 记录里的 `$patch` 指令同样移除（那是给
strategic merge patch 的指令，不是 Pod 模板的字段）。判断某个修订是否就是当前模板使用 Kubernetes 的语义比较
而不是逐字节比较：`1` 和 `1000m` 是同一个 CPU 请求，只是写法不同的修订不构成可回滚的目标，此时接口返回
`409 workload_revision_unchanged`，而不是写入一次什么都不改、却留下审计记录说改过的更新。

回滚复用现有的 Update 执行链路：`cluster.resource.update` 权限、CSRF、幂等键、DryRun 预检、显式确认与审计，
并强制携带读取修订列表时的 UID 与 resourceVersion，因此期间被他人改动的对象会被拒绝而不是覆盖。修订列表本身
只要求 `cluster.read`。`kubernetes.io/change-cause` 注解按修订原样展示，但回滚不恢复它——它标注的是那次修订，
不是模板的一部分。

Console 的「历史版本」入口在 Deployment、StatefulSet、DaemonSet 详情页页头，位置紧随 YAML：它首先是拿来读的，
而每行后面的回滚在那一页上确认。Job 与 CronJob 详情页不出现这个按钮——一个按下去必然返回 400 的入口不如不给。
页面本身是一张表，每行是一个修订：修订号与承载它的 ReplicaSet/ControllerRevision 名称（便于直接用 kubectl
查看那个对象）、各容器的镜像、变更说明和记录时间；当前模板那一行带「当前」标记且不提供回滚按钮，因为服务端本来
就会拒绝它。列出镜像而不是整份模板，是因为同一个工作负载的历次修订在其余字段上大多相同，操作者比较的正是镜像。
回滚沿用本分区其他写操作的两步链路：先服务端 DryRun，再确认；UID 与 resourceVersion 在打开对话框时固定，
确认框写明本次只恢复 Pod 模板、控制器会按更新策略滚动替换全部 Pod，以及期间对象被改动时本次回滚会被拒绝。

类型化创建支持名称、描述、标签、注解、完整的 Pod 模板，并按类型支持副本数、StatefulSet Service、Job 执行
参数与 CronJob 调度参数。Server 使用资源类型和名称生成客户端不能覆盖的 `zke.io/workload-id` 标签；Deployment、
StatefulSet 和 DaemonSet 使用该标签作为 Pod Selector，Job 的 Selector 则由 Kubernetes 按控制器 UID 生成，
避免不同控制器意外选中同一批 Pod。StatefulSet 的 `service_name` 必须引用同一 Namespace 中预先存在的
Service；更新仍可使用通用资源接口。

Pod 模板部分对五类工作负载共用——一个 Deployment 和一个 CronJob 的差别在于 Pod 怎样被产生，而不在于 Pod 是
什么。容器层面接受镜像与拉取策略、运行命令与参数、工作目录、环境变量、资源 requests/limits、数据卷挂载、
容器端口、存活与就绪探针、生命周期钩子和特权开关；Pod 层面接受数据卷、镜像访问凭证、节点标签选择、容忍调度、
Node/Pod 亲和与反亲和以及拓扑分布约束。描述写入
工作负载与 Pod 模板的 `zke.io/description` 注解，该键因此不接受在 `annotations` 中另行设置。

几处校验刻意比 Kubernetes 更早拦截：探针的 `exec`、`httpGet`、`tcpSocket` 必须且只能设置一个——没有处理器的
探针不会执行，两个处理器没有确定语义；数据卷的六种来源同理；容器的挂载必须引用同一请求中声明的数据卷，挂载
路径必须是绝对路径且不含冒号，同一容器也不能把两个卷挂到同一路径，那样 Kubernetes 会静默让其中一个生效；
`subPath` 不接受绝对路径和 `..`，因为它本就应当落在所选卷内部；同名资源的 requests 不得大于 limits，否则容器
永远不会被调度；初始化容器不接受探针和生命周期钩子，它在 Pod 启动前就已运行完毕；容忍在 `Exists` 下不接受
取值，只有 `NoExecute` 接受容忍时长；`privileged: false` 不会被写入 `securityContext`，否则一个普通容器会看
起来像是被专门配置过的。名称长度按类型收紧：Job 最长 63 个字符，因为该名称会成为 Pod 的 `job-name` 标签值，
CronJob 最长 52 个字符，需要为控制器派生的 Job 名称留出余量。资源 requests/limits 保持为 quantity map 而不是
四个具名字段，因此 `nvidia.com/gpu` 等扩展资源无需新增接口字段即可声明。

高级调度结构保持 Kubernetes 的嵌套语义：Required NodeAffinity 的多个 term 之间为 OR、同一 term 内的
`matchExpressions` 与 `matchFields` 为 AND；PodAffinity/PodAntiAffinity 支持 required/preferred、权重、
Namespace 列表与 selector、动态 `matchLabelKeys`/`mismatchLabelKeys`；TopologySpread 支持 `minDomains`、
NodeAffinity/taint 纳入策略与动态标签键。Server 对每类集合设 50 项上限，校验标签选择器、操作符与 values 的组合、
1–100 权重、拓扑键和 `minDomains` 的硬约束语义。容器端口限制为每容器 100 项、1–65535，协议为 TCP/UDP/SCTP，
端口名在容器内唯一。`hostPort`/`hostIP` 会占用节点网络，`securityContext` 的其余字段仍通过 YAML 管理。

服务与路由后端固定使用 `core/v1 Service`、`networking.k8s.io/v1 Ingress`、Gateway API 的 Gateway 与五种
协议型 Route，不会接受调用方覆盖 GVR。Service 支持 ClusterIP、NodePort、
LoadBalancer、ExternalName 与 headless 语义，更新时保留 Kubernetes 分配的 ClusterIP、IP family 和适用的
health check NodePort，并拒绝通过类型化接口切换不可变的 headless 身份。Ingress 支持 class、默认后端、
Host/Path、Service backend 和 TLS Secret 名称；接口只返回 Secret 引用名称，不读取 Secret 正文。

Gateway API 是目标集群的可选 CRD 能力。每一种资源操作都先通过 Discovery 独立确认目标 GVR 存在：Gateway、
HTTPRoute、GRPCRoute、TLSRoute 使用 `gateway.networking.k8s.io/v1`，TCPRoute 与 UDPRoute 使用实验通道的
`v1alpha2`；未安装其中一种只影响该标签页并返回稳定的 `409 gateway_api_unavailable`，与已安装但
Agent ServiceAccount 无权访问时的 `403` 区分。ZKE 不负责安装 Gateway API CRD、GatewayClass 或具体
Controller，也不把 Gateway 的 `Programmed=True` 等同于外部流量已经可达。Route 类型化接口以完整的
Kubernetes-native `spec`（camelCase JSON）为输入，按路径类型的官方 Gateway API Go 类型严格拒绝未知字段，
再交给目标 API Server DryRun 执行该集群实际 CRD/CEL 与准入校验。更新只替换完整 `spec`，保留 metadata、status
和 Controller 扩展；跨 Namespace ParentRef/BackendRef 可以表达，但 ZKE 不代建 ReferenceGrant，也不追踪读取
被引用命名空间，授权与解析结果以 Controller 写入的 `Accepted`、`ResolvedRefs` Condition 为准。Gateway 的 TLS
同样只传递证书引用，不读取证书 Secret。

Console 服务与路由页面按 Service、Ingress、Gateway 和五种 Route 标签页组织。各类型形状不同，因此列表列和详情卡片
各自独立，而不是压成一张只显示共有字段的表：Service 展示类型、ClusterIP 与端口映射，Ingress 展示
IngressClass、主机与已分配地址，Gateway 展示 GatewayClass、监听器与地址。详情页在类型化视图之外还提供
YAML 入口，用于查看和修改本表单未建模的字段。

Service 端口点击复制的是「地址:端口」，因为那才是被粘贴出去的整体；headless Service 没有地址，只复制端口号，
也不替它拼一个 `svc.cluster.local` 域名——集群域名由集群自身配置决定，接口并不携带它。NodePort 单独可复制。

创建和编辑占据整个应用视图而不是弹窗，与工作负载创建表单一致；从详情页进入编辑时详情仍在其下，因此离开表单
回到的是刚才在读的那个对象而不是列表，与该页的 YAML 入口一致。一个带三个监听器的 Gateway 或一条带若干路由的
Ingress 比盖在列表上的盒子高，而填表期间下面那张列表本来也用不上。表单按类型只渲染该类型接受的字段，Service
的类型切换会连带显示对应字段（ExternalName 只要目标域名，NodePort 与 LoadBalancer 才有外部流量策略和
NodePort 输入）。编辑一个创建时即为 headless 的 Service 时，NodePort 与 LoadBalancer 两项不可选，并在字段下
写明原因：这两种类型都建立在 ClusterIP 之上，而 `spec.clusterIPs` 创建后不可变，Kubernetes 不会再为它分配
——一个只被置灰的选项说出了结论却没说出理由。headless 开关同样在创建后不可切换。

表单在提交前做一次与 Server 同形的校验，校验消息显示在能够修正它的那个区块里，底部按钮旁只说明是哪个区块拦住了
提交——一条关于端口的提示出现在表单末尾没有意义。端口、NodePort 和监听器端口是只接受数字、最多五位的输入框——端口不会超过 65535，
第六位数字不是一个待拒绝的取值，而是一个不可能通向合法端口的按键；1–65535 这个范围写在区块说明里，不用先填错
才看得到，越界仍由校验拦下（99999 是五位数）。目标端口和后端端口仍是普通输入框，因为它们也可以写容器端口的名称，校验按
Kubernetes 的 `IsValidPortName` 执行（最长 15 个字符、至少含一个字母、不能以 `-` 开头结尾或连续两个 `-`）。
其余规则逐条对齐 Server：Service 名称按 DNS-1035（必须字母开头）、Ingress 与 Gateway 名称按 DNS 子域名；多个
端口时每个都必须有名称，端口名称与「协议/端口」都不得重复；NodePort 只在 NodePort 与 LoadBalancer 类型下可填，
且说明留空由 Kubernetes 分配、通常落在 30000–32767，具体范围由集群配置决定，因此表单不按该范围拦截；Ingress 的
Exact 与 Prefix 路径必须以 `/` 开头，路径不接受 `//`、`/./`、`/../` 与 `%2f`；Gateway 的 HTTPS 监听器必须
Terminate 且至少一个证书，Passthrough 不接受证书。有一条刻意比 Kubernetes 更早拦截：同一主机下完全相同的
路径与匹配方式会被拒绝，Kubernetes 接受这样的重复但只有其中一条生效，且不说明是哪一条。

创建、更新和删除都先执行服务端 DryRun 再确认。更新和删除携带对象当前的 UID 与 resourceVersion，因此陈旧
编辑会被拒绝而不是覆盖；确认弹窗说明本表单建模的配置会整体替换现有配置，而 Kubernetes 分配的字段和未建模
的扩展字段由服务端保留。

DryRun 预检通过不等于实际写入一定成功，NodePort 就是一例：Kubernetes 在端口分配器里检查
`--service-node-port-range`，而 DryRun 请求不走分配这一步，因此超出范围的 NodePort 预检通过、实际创建被拒。
ZKE 不能替 Kubernetes 补上这个检查——真实范围由该集群 API Server 的启动参数决定，按默认值拦截会挡住配置了
其他范围的集群——因此表单在填入 30000–32767 之外的 NodePort 时给出警告而不是阻止提交，并说明预检不会发现它。

与之配套，Kubernetes 因对象本身不合法（`Invalid`）而拒绝写入时，API Server 自己的说明会原样返回给操作者，
错误码为 `cluster_api_rejected`。此前这类失败被折叠成「请求内容无效，请检查输入」，而 Kubernetes 明明已经说清
是哪个字段、为什么——「NodePort 38080 不在 30000-32767 范围内」这句话只存在于那条消息里，请求和对象本身都不
携带这个范围。只有 `Invalid` 被这样透传：它描述的是调用方刚提交的对象；其余失败讲的是集群自身的状况，仍使用
固定文案。

被 Kubernetes 拒绝的提交可以直接在表单里改正后重新提交，不需要退出重填。表单在打开时为预检和写入各取一个
幂等键并保持到关闭——那是为了让一次因响应丢失而重试的提交不会执行两次——而被拒绝的提交没有落地，Agent 因此
不为它占用该键，改正后的内容在同一个键下是一次新请求（详见上文重放缓存）。只有当上一次提交以 5xx、超时或
连接中断结束、结果无法判定时，改动内容再提交才会返回 `idempotency_conflict`，此时该键可能已经对应一次真实
写入，正确的做法是重新读取该对象。

目标集群没有安装当前标签所需的 Gateway API GVR 时，列表返回 `409 gateway_api_unavailable`，Console 据此展示具体
group/version/resource 说明而不是错误，
并隐藏创建入口；这与已安装但 Agent 无权访问的 `403` 是两回事，后者仍按权限错误呈现。

ConfigMap 类型化后端固定使用 `core/v1 ConfigMap`，不会接受调用方覆盖 GVR。列表不返回配置值，避免列表和
搜索场景批量搬运正文；详情才返回 `data` 和以标准带填充 Base64 表示的 `binary_data`。创建和更新校验键名、
两类键不重叠及解码后总大小不超过 1 MiB。更新是两张数据表的完整替换，要求显式提交空表，并使用当前 UID 与
resourceVersion 防止覆盖同名重建或并发修改；一旦设置 immutable，内容变更或恢复为 false 会被拒绝。
该接口复用通用 Resource Stream、集群权限、CSRF、DryRun、确认、幂等与审计链路，但审计不记录配置正文。
Secret 有独立的类型化接口，不借用 ConfigMap 路由，也不经过通用 Resource 与 YAML 接口——后两者对 Secret 的
拒绝保持原样。Secret 的 YAML 是另一对独立路由
`GET`、`PUT /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/secrets/{secret_name}/yaml`，
读要求 `cluster.secret.read`，写要求 `cluster.secret.manage`、CSRF 和幂等键。它复用 YAML 服务的单文档解析与
身份核对，但走的是 Secret 服务自己的资源访问——那是进程内唯一会设置 `secret_access` 的地方——因此平台对象过滤
与 Agent 的两条判定原样生效。写入前另有一层校验：拒绝改变 `type`、拒绝写入已 immutable 的对象、拒绝给对象添上
`app.kubernetes.io/managed-by=zke-server`（那会把它从这个 API 里摘出去）。

Console 配置管理页面列出所选命名空间的 ConfigMap，展示键名、总大小和 immutable 标记——列表接口不返回配置
正文，页面也不去逐个补齐，否则等于把整个命名空间的配置搬进浏览器。内容只在详情页按对象读取：文本值按原样以
等宽预格式展示且不折行（配置文件的缩进和换行本身就是内容），二进制值只显示大小并说明是 Base64，不做任何
渲染——那些字节没有可靠的文本解释，需要原文时走 YAML 视图。

创建和更新占据整个应用视图而不是弹窗，与工作负载和服务与路由的表单一致：一份配置文件有多长就有多长，
通过盖在列表上的盒子读它比离开列表更难，而填表期间下面那张列表本来也用不上。从详情页进入编辑时详情仍在
其下，因此离开表单回到的是刚才在读的那个对象。

创建和更新都先执行服务端 DryRun 再确认。更新是整体替换，确认弹窗明说「本次未提交的键将从对象中移除」，
并提示 Volume 挂载与环境变量注入的生效时机不同。UID 与 resourceVersion 在打开编辑器时固定，不随后台重新
拉取而更新：取一个更新的版本号会把本该被服务端拒绝的冲突变成静默覆盖。immutable 只在创建时可设，
已标记为不可变的对象在列表和详情中都不提供编辑入口，并说明只能删除重建。

Secret 管理与 ConfigMap 放在同一个「配置管理」分区的两个标签页下：它们是同一件事——交给工作负载的配置——
而形状与代价不同，因此不压成一张表。

四道关卡决定谁能通过 Secret 接口读到一个 Secret 的取值，缺一不可：

- **专用权限**：读要求 `cluster.secret.read`，写要求 `cluster.secret.manage`。两者都不由 `cluster.read` 或
  `cluster.resource.*` 蕴含——能看配置和能看凭证是两个问题，一个角色不该因为前者顺带获得后者。没有读权限时
  Secret 标签页不出现，服务端仍独立判定。
- **服务端专线**：通用 Resource 与 YAML 接口对 `core/v1 Secret` 的拒绝没有放开；类型化 Secret 服务是进程内
  唯一会在请求上设置 `secret_access` 的地方，而该字段在 Go 中不可导出，包外无法设置。Secret 的 YAML 路由不是
  例外：它拿到的访问器只接受 `core/v1 Secret`，其余 GVR 一律返回无效输入，因此它不是一个恰好指向 Secret 的
  通用访问器。
- **Agent 二次判定**：Agent 只在请求带 `secret_access` 时才动 Secret，并且拒绝任何指向自己所在命名空间的
  Secret 请求——那里放着 Agent 的身份私钥、注册令牌和它据以信任 Server 的证书，读到它们等于冒充这个 Agent。
  这两条在 Agent 侧检查，而不是信任 Server 的说法。旧版本 Agent 不认识该字段，会继续拒绝，因此 Server 先于
  Agent 升级时该能力表现为不可用，而不是绕过。Agent 自身命名空间那条拒绝带有独立的 reason，Server 据此返回
  `403 agent_namespace_forbidden` 而不是 `502 cluster_api_forbidden`：那是 ZKE 的固定规则，既不该被客户端重试，
  也不该读成「给 Agent 补 Kubernetes 权限就能解决」。
- **平台对象过滤**：带 `app.kubernetes.io/managed-by=zke-server` 的 Secret 不出现在列表中，按名称读取、更新和
  删除都返回 `403 secret_managed_by_platform`——这与权限不足是两回事，调用者可能持有全部 Secret 权限，那个对象
  依然不属于它管理的范围。

安装清单为 Agent ServiceAccount 增加了 Secret 的 `get`、`list`、`create`、`update`、`delete`，没有 `watch`，
也没有 Subresource。这个授权是该能力得以存在的前提，而不是它的授权边界——边界是上面四条。

这四条约束的是 **Secret 接口**，不是 Secret 里的字节。持有 `cluster.resource.create` 的人可以创建一个挂载了某个
Secret 的 Deployment 或 Job，再用 `cluster.pod.logs.read` 或 `cluster.pod.exec` 把内容读出来，全程不经过
`cluster.secret.*`。这不是 ZKE 的缺陷，而是 Kubernetes 本身的权限等价关系——`kubectl` 同样如此——但它意味着
不授予 `cluster.secret.read` 并不等于该用户读不到 Secret。真正把两者分开，需要同时限制工作负载创建权限；
在设计角色时应当把 `cluster.resource.create` 与 `cluster.pod.exec`、`cluster.pod.logs.read` 的组合视为等同于
该 Namespace 的 Secret 读取权限。

这条等价关系不靠人记住：角色编辑器在选中 `cluster.resource.create` 与两项 Pod 权限之一、却未选中
`cluster.secret.read` 时直接说明它。不拒绝这种组合——那是运行工作负载并排查它的日常形态，本身完全正当——要修的
是此前界面上没有任何迹象，让「不给 Secret 读权限」看起来像是隔离了 Secret，而它不是。

读取本身写入审计（`kubernetes_secret.list` 与 `kubernetes_secret.read`），记录发起者、Cluster、Namespace、
对象名和结果，不记录键名或取值。

列表只返回键名、大小、类型和不可变标记；取值只在按名称打开一个对象时返回，且默认遮蔽，逐个键点击才显示：
这个页面被用来核对名称和大小的次数远多于用来读凭证，而一打开就把命名空间里所有口令铺在屏幕上，泄露的对象
是站在操作者身后的任何人。取值以标准 Base64 传输（Kubernetes 就是这样存的）；字节不是 UTF-8 文本时不做渲染，
只显示大小并提供复制 Base64——浏览器从那些字节里猜出来的文本不是内容。

表单把取值分成「数据」和「二进制数据」两组，与 ConfigMap 一致：前者按 UTF-8 编码为 Base64 后提交，后者原样
提交。类型在创建时选择、创建后只读，Kubernetes 不允许修改；`kubernetes.io/service-account-token` 不接受，
它由 Kubernetes 为 ServiceAccount 签发，手工创建等于为一个身份铸造令牌。更新是整体替换，携带打开表单时的
UID 与 resourceVersion。审计记录发起者、目标和结果，不记录任何取值。

类型不只是对象上的一个标签，Kubernetes 会按类型要求特定的键，`dockerconfigjson` 还会把取值当 JSON 解析。
表单因此按类型给出这些键，而不是让操作者从一次拒绝里反推规则：选定类型后预置该类型要求的键，键名只读且不可
移除——Kubernetes 在这里只接受那一个名字，一个可编辑的键名给了填错的机会却没给填得更对的机会。`basic-auth`
的两个键只需其一，因此编辑一个只有 `username` 的对象时不会补上空的 `password`：更新是整体替换，凭空多出的键
会被写进对象，而那是一次没人要求的改动。`ssh-privatekey` 不接受空值，`tls.crt` 与 `tls.key` 必须同时存在。

镜像仓库凭证不走键值表单，而是用「镜像仓库」分区按地址、用户名、密码和可选邮箱逐个仓库填写，由 Console 拼出
docker config 文档、算好 `auth`（即 Base64 的 `用户名:密码`，多数仓库真正读的是这个字段）并写入
`.dockerconfigjson`。这样做是因为这个键的取值在世界上出现的形态就是 Base64——`kubectl get secret -o yaml`
是这样，仓库文档给的也是这样——而「数据」分区会把它再编码一次，Kubernetes 解码一次后拿到的仍是 Base64
文本，于是报出一个关于操作者从未输入过的字符的 JSON 解析错误。一个索要该键取值的输入框本身就在邀请这次错误，
因此它不再存在。`auths` 支持多个仓库；`identitytoken` 等未建模的成员按当前取值原样保留并在界面上说明。极少数
情况下已有对象的该键读不成 docker config JSON（Kubernetes 在写入时校验它，因此只可能来自别的途径），此时表单
不启用「镜像仓库」分区，把原值留在「数据」分区原样回传——一份没人能重新输入的凭证比一个更好的编辑器重要。

「数据」分区另有一条提示而不是拦截：取值若是合法 Base64 且解码后是 JSON 或 PEM 文本，表单指出它看起来已经
编码过、这里会再编码一次，并说明想原样提交应改用「二进制数据」分区。只认 JSON 和 PEM 是为了不去猜一个恰好
长得像 Base64 的口令；因为是猜测，所以是提示，不阻止提交。

密码在表单里明文显示，与该表单其他取值一致（详情页才默认遮蔽、逐键点开）。这里的取值通常是操作者刚刚粘贴进来
的，遮住它恰好会挡住粘贴错误——而粘贴错误正是这一组改动要消除的那一类问题。

Secret 详情提供 YAML 入口，与其他分区一样是页头上的一个按钮。它比详情页多暴露的是「一次显示全部取值」而不是
「取值」本身——两者都要求 `cluster.secret.read`，这个权限判过之后再加一道点击并不多挡住什么。不可变 Secret 的
YAML 以只读打开并说明原因，而不是让人改完再被 Kubernetes 拒绝。

存储类型化后端把作用域写进路由和领域校验：PV、StorageClass 只能走集群级路径，PVC 只能走明确 Namespace
路径，调用方不能覆盖 GVR。PV 创建首轮支持 CSI、NFS 和 Local source；CSI Secret 只传引用名称，不读取 Secret
正文，Local PV 必须提供 Node Affinity。已有其他历史 Volume Source 仍可列表和查看 source 类型，复杂字段可走
通用 YAML 管理。StorageClass 创建支持 provisioner、parameters、回收策略、绑定模式、卷扩展开关、挂载参数和
拓扑约束。

类型化更新刻意限制在 Kubernetes 明确且常用的可变字段：PV 只修改 ReclaimPolicy，PVC 只允许提高 requested
storage，StorageClass 只修改 allowVolumeExpansion；PVC 最终能否扩容还取决于对应 StorageClass 和 CSI Driver。
缩容在发往 Agent 前即被拒绝。三类删除都使用 UID/resourceVersion 前置条件，避免同名重建或并发变化后误删。
删除 PVC 可能影响工作负载，删除 PV 的后果还取决于当前 Phase 与 ReclaimPolicy。

Console 存储页面按 PersistentVolume、PersistentVolumeClaim、StorageClass 三个标签页组织。三者作用域不同：
PV 和 StorageClass 是集群级对象，PVC 是命名空间级，Server 也据此分成两组路由。页面不掩盖这一点——工具栏的
命名空间选择器只在 PVC 标签页出现，其余两个标签页不显示一个什么都不限定的控件。

创建和编辑共用一个整页表单，占据整个应用视图而不是弹窗，与工作负载和服务与路由一致；从详情页进入编辑时详情
仍在其下，离开表单回到详情而不是列表。表单按类型渲染：PV 需要容量、访问模式和来源（CSI/NFS/Local 三选一，
字段随之切换），并明确说明 ZKE 不会创建底层存储、填写的卷必须已经存在；PVC 需要申请容量和访问模式，并把
「使用集群默认 StorageClass」与「显式指定（留空表示不使用任何 StorageClass）」区分开，因为这两者在 Kubernetes
中语义不同；StorageClass 需要 provisioner 和参数。访问模式用复选框而不是下拉，因为它本来就是集合而不是单选。

编辑打开的是同一张表单，字段按当前对象填好，可改的只有类型化接口开放的那一个：PV 的回收策略、PVC 的申请容量
（且必须增大）、StorageClass 的扩容开关；其余字段禁用，页首用一句话说明哪些字段 Kubernetes 在创建后就不再接受
修改。不把它们藏起来是有意的——一张只剩一个输入框的表单，操作者看不到自己即将保留下来的是什么。PV 的来源和
StorageClass 的参数只在详情中返回，因此编辑会先取一次详情再渲染，而不是先给出一屏空白。表单在对象标识变化时
整体重挂，避免后台刷新改写正在填写的内容。编辑只在该字段确实变化后才可提交：一次什么都没改的写入只会留下一条
审计记录。确认弹窗按具体选择给出后果，例如把 PV 回收策略改为 Delete 会在删除时销毁数据，PVC 扩容需要 CSI
驱动配合且部分驱动要求 Pod 重启后文件系统才扩展。删除的影响文案同样按类型区分，包括 PV 回收策略决定数据存亡、
仍被占用的对象会停在 Terminating。

自动伸缩类型化后端固定使用 `autoscaling/v2 HorizontalPodAutoscaler`，并强制 Cluster 与 Namespace 定域。
创建和更新时 `scaleTargetRef` 只接受同一 Namespace 中的 `apps/v1 Deployment` 或 `apps/v1 StatefulSet`；HPA
原生引用不携带 UID，因此同名目标重建后会被同一个 HPA 继续识别为目标，这一点不能用 HPA 自身的 UID 前置条件
规避。HPA 对象本身的更新和删除仍使用当前 UID/resourceVersion 防止覆盖或删除同名重建的 HPA。

类型化写入首轮支持 Resource 和 ContainerResource 指标，Target 支持平均利用率或平均值；Behavior 支持
ScaleUp/ScaleDown 稳定窗口、Max/Min/Disabled 选择策略，以及 Pods/Percent 策略。列表与详情仍能读取并标记
已有 HPA 的 Object、Pods、External 等指标，但若要创建或完整编辑这些高级指标，应使用 YAML。资源利用率通常
依赖 Metrics Server，其他指标依赖对应 Metrics API Adapter；ZKE 不安装也不伪造这些组件，指标不可用时 HPA
可创建但会通过 `ScalingActive=False` 等 Condition 报告原因。

HPA 与手动伸缩会同时写目标工作负载的 replicas；启用 HPA 后，手动副本数可能在下一次控制循环被覆盖。删除 HPA
只停止后续自动调节，不删除目标工作负载，也不会自动把副本数恢复到启用前的值。实际写入继续要求 CSRF、DryRun、
显式确认、幂等和审计。

VPA 与 KEDA 是可选集成，ZKE 不安装对应控制器或 CRD。VPA 类型化接口固定 `autoscaling.k8s.io/v1`，创建时
不接受已废弃的 `Auto` 更新模式，而要求显式选择 Off、Initial、Recreate、InPlaceOrRecreate 或 InPlace；容器策略
只允许 CPU/Memory 的正数 Kubernetes Quantity，且下界不得超过上界。KEDA 类型化接口固定
`keda.sh/v1alpha1`，每个触发器必须提供非空 metadata；密码、Token、Secret、API Key、账户密钥、SAS Token
和连接串等敏感值不得直接写入 metadata，必须用 `authentication_ref_name` 引用同 Namespace 的
TriggerAuthentication。为兼容集群中已有对象，读取时发现这些敏感键只返回 `[redacted]` 并列出脱敏键，不把正文
送到 Console、日志或审计。类型化编辑会拒绝把脱敏占位符写回，操作者需先移除敏感键并配置认证引用。

Console 自动伸缩页面列出所选命名空间的 HPA，展示目标工作负载、当前副本与期望副本、副本区间、指标数量、
状态和最近一次伸缩时间。状态只显示需要注意的情况——无法伸缩、指标不可用、已触达上下限、尚未同步——健康的
HPA 只显示一个「正常」，否则每行三个绿标会把真正异常的那一个淹掉。

创建和编辑表单覆盖后端首轮支持的范围：目标（Deployment 或 StatefulSet）、副本区间、Resource 与
ContainerResource 指标（Utilization 百分比或 AverageValue），以及可选的扩容/缩容行为（稳定窗口、
Max/Min/Disabled 选择策略、Pods/Percent 策略）。更新替换整份 spec，确认弹窗明说未提交的指标或行为策略会被
移除；创建的确认弹窗则点明副本数将由控制器接管、手动伸缩会在下一个控制周期被覆盖，以及指标不可用时 HPA
不会伸缩而是在 Condition 中报告原因。已有 HPA 若使用了 Object、Pods、External 等本表单未建模的指标，列表和
详情仍可读取，编辑请走 YAML——该表单不会打开，并说明用它保存会丢掉这些指标。

HPA 摘要同时返回 `generation` 与 `observed_generation`，Console 据此区分控制器尚未处理最新 spec 的状态。
详情页另外展示当前/期望副本折线与近期 status 指标样本。该趋势只是有界的 Server 运行时采样，不替代 Prometheus
等持久监控后端，不跨 Server 重启保留，也不会主动安装 Metrics Server 或指标 Adapter。

Console 自动伸缩区分 HPA、VPA 与 KEDA 三个视图。VPA/KEDA 都提供列表、详情、YAML、类型化创建和编辑、
DryRun 后二次确认以及带 UID/resourceVersion 的删除。控制器未安装时对应视图说明所缺 CRD 并禁用创建，不影响
HPA 或其他容器服务页面。三类列表使用与工作负载、存储等资源分区一致的页签和表格骨架；进入详情、YAML 或表单
后隐藏资源类型页签，避免让一个只作用于列表的切换控件悬在对象页面上。Agent 将不存在的可选 API Group 缓存为
负向 Discovery 结果，Server 稳定返回 `available=false`；Console 不对扩展能力探测自动重试，因此未安装状态无需
等待错误重试退避，真实 5xx 也会立即展示并由操作者决定是否重试。

策略管理后端把五类约束放在一组接口下：命名空间级的 `core/v1 ResourceQuota`、`core/v1 LimitRange`、
`networking.k8s.io/v1 NetworkPolicy`、`policy/v1 PodDisruptionBudget`，以及集群级的
`scheduling.k8s.io/v1 PriorityClass`。两种作用域是两组路由，互相拒绝对方的资源，因此一个没有 Namespace 的
对象不会被当成命名空间对象执行。权限沿用通用资源权限：读要求 `cluster.read`，写要求
`cluster.resource.create/update/delete`、CSRF、幂等键和显式确认——这些对象约束工作负载，但不像 RBAC 那样能
提升调用者自身的权限，因此不需要独立权限位。

类型化更新替换整份托管 spec 而不是逐字段合并：一份配额或一条网络策略是作为一个整体被读的，逐字段编辑会让
没人动过的部分看起来是刻意保留的。为此更新前先按 UID 与 resourceVersion 读回对象，任何一项不匹配就返回冲突。
Kubernetes 自身的不可变约束也被如实反映：ResourceQuota 的 `scopes` 与 PriorityClass 的 `value` 创建后不可
修改，接口不接受它们的新值；PodDisruptionBudget 的 `selector` 不在更新范围内，因为把预算指向另一组 Pod 会
悄悄解除原来那组的保护，这与调整预算是两件事。ResourceQuota 的读取优先返回 `status.hard`，那才是集群正在
执行的额度；`scopes` 和 `scope_selector` 与 `hard`、`used` 一起放在摘要里，因此列表响应本身就带有它们——两者
都决定这份配额统计哪些对象，缺了它们就会把命名空间的一个子集读成全部。配额没有独立的 `resource_quota_detail`：
它原本只承载 `scope_selector`。NetworkPolicy 写入时校验 CIDR 与 `except` 的包含关系、端口范围和命名端口的组合，并拒绝声明在
`policyTypes` 之外的方向上写规则——那样的规则会被 Kubernetes 静默忽略，看起来生效而实际没有。

Console 策略管理页面按五种类型分页展示：配额显示 `used/hard` 与作用范围，限制范围显示类型和限制项数量，
网络策略显示作用对象、方向与规则数，中断预算显示预算、健康副本数与当前允许中断数，优先级显示 value、抢占
策略和集群默认标记。创建与编辑共用一个表单，因为对四类对象来说它们提交的是同一份 spec；PriorityClass 的编辑
表单只开放描述与集群默认开关，并说明 value 不可变。表单未建模的高级字段——scopeSelector、selector 的
matchExpressions——按当前值原样回传并在界面上说明，需要修改时走 YAML。删除确认逐类型说明失去的是哪一重
约束，例如删除 NetworkPolicy 后未被其他策略选中的 Pod 会回到默认放行。

策略是否真正生效取决于集群自身：NetworkPolicy 需要支持它的 CNI，配额与限制范围由 API Server 准入控制执行。
ZKE 不安装网络插件，也不为不支持的插件伪造效果。

资源对象浏览器不是又一个类型化模块，而是通用 Discovery 与通用 Resource 接口的一个视图：左侧资源树直接来自
目标集群 Agent 的 API Discovery，按 API Group、Version 和资源名分层，右侧按 Kubernetes 原样列出该类型的对象。
「仅显示 CRD」筛选依赖目录中的 `custom_resource` 标记；该标记来自 Agent 对 CustomResourceDefinition 列表的
只读查询，读不到时界面明说无法判定并提示需要补充的最小 RBAC，而不是给出一个看起来「没有 CRD」的空列表。该分区
同时读取资源树与当前类型的对象列表，工具栏中的那一个刷新按钮两者都重新读取，右侧不再单独放一个只刷新列表的按钮。

浏览器只提供读取、YAML 编辑和删除三件事，且都复用既有链路：读取要求 `cluster.read`，YAML 编辑走
`cluster.resource.update`，删除走 `cluster.resource.delete` 并携带该对象当前的 UID 与 resourceVersion 前置
条件、Background 传播策略、DryRun 与显式确认。它不为未知类型编造类型化表单——对一个 ZKE 不认识其语义的 CR，
YAML 是唯一诚实的编辑方式。命名空间选择器提供「所有命名空间」，对应通用接口省略 Namespace 参数的跨命名空间
查询；名称筛选只作用于已加载的当前页并在界面上说明，因为 Kubernetes Field Selector 只能精确匹配名称，把它
当成模糊搜索会静默变成另一种语义。Secret、Event 和五类 Kubernetes 授权资源不出现在这里：它们分别被通用接口
拒绝或只允许走专用链路。Namespace 是唯一一个「在这里但只有一半」的类型——可以浏览、查看和编辑 YAML，但资源目录
不为它报告 `create`、`delete`、`patch`，因此这两个动作的按钮不会出现；它们属于 `cluster.namespace.manage` 和
命名空间分区。目录里的 Verb 描述的是**这个入口提供什么**而不是集群支持什么，这一点对每个类型都成立，Namespace
只是唯一一处两者不同的地方。

### YAML 清单

YAML 清单是资源对象浏览器的写入侧对应物：浏览器读任意类型，它写任意类型。支持上传 `.yaml`/`.yml` 文件
（可多选，按 `---` 拼成一份清单）或直接手动输入，然后以多文档清单整体应用或删除。

**apply 使用 Server-Side Apply**，field manager 固定为 `zke-manifest`，不由客户端指定：Apply 只有在
manager 保持不变时才会在重复运行中收敛，客户端可选会让第二次应用把第一次拥有的字段变成孤儿。对象不存在则创建、
存在则合并，一条请求路径覆盖 `kubectl apply -f` 的语义，也没有读改写窗口让对象在中间发生变化。字段所有权冲突
默认拒绝，可显式勾选强制接管。既有的单对象 YAML 编辑器**语义不变**——它仍是「整份替换 + UID/resourceVersion
前置条件」，那是编辑一个已知对象，与 apply 是两件事，不应合并。

单对象编辑器在输入停止 250ms 后执行本地严格校验：拒绝多文档、重复键、非映射顶层、anchor/alias、自定义 tag，
并要求 `apiVersion`、`kind`、名称、Namespace、UID 与 resourceVersion 和刚读取的对象身份一致。该检查只在
浏览器内运行，不产生请求或审计，也不宣称代替集群 schema；保存仍先走目标集群 Kubernetes DryRun，由 API
Server 和准入控制给出权威结果。DryRun 通过后，确认框比较当前对象和 **DryRun 返回的最终 YAML**，因此能把
默认化、准入修改与用户输入共同造成的真实增删行展示出来。普通文档使用精确行级差异；超大内嵌内容使用有界粗粒度
差异、变化区间统计和抽样渲染，避免一个 4 MiB ConfigMap 卡住浏览器。Secret 仍只通过专用权限读取
和更新，差异不发送到其他服务或进入审计正文。

**Kind 到 GVR 的解析**来自目标集群自身的 API Discovery，键是 `apiVersion` 的 group、version 加 `kind` 三元组
而不是 Kind 单独一项：`Ingress` 和 `Event` 都在多个 group 中出现过，只凭 Kind 无法定位，而文档总是携带
`apiVersion`。Discovery 不完整时接口返回 `catalog_partial`，界面据此说明「集群未提供该 Kind」只表示本次没
查到，不代表集群中没有该类型。`core/v1 Secret` 是一条**声明**而不是发现来的条目：Agent 有意不把 Secret 放进
它上报的资源目录（那份目录供资源对象浏览器使用，浏览器不该展示一个点开就被拒的类型），但「哪个资源服务
Kind `Secret`」是个固定答案，与「能不能写」是两个问题——交给发现会让清单里的每个 Secret 都以「集群不提供该
Kind」失败。能不能写仍然由 grant 判定，与其他条目一样。

**服务端 DryRun 有一个固有盲区，界面据实呈现。** 清单先创建 Namespace、再往里放对象，是多文档清单最常见的
形态，而它整份都无法被预检：DryRun 创建的 Namespace 不落地，后续文档提交进去只会得到 Kubernetes 关于
**Namespace** 的 `not found`，却挂在 ConfigMap 头上；按失败处理还会让执行停在第二个文档，等于这种清单在 ZKE
里根本无法应用。因此 DryRun 不发送这些文档，逐条标记为「未预检」（响应里的 `previewed: false`），实际执行时
照常应用。这确实少了一层校验，所以明说而不是藏起来——替代方案是直接拒绝整份清单。同一份文件里
CustomResourceDefinition 与其自定义资源有同样的限制，该情形目前尚未识别，仍会以 DryRun 失败呈现。
仅对**本清单将要创建**的 Namespace 生效，已存在的 Namespace 照常完整校验。

DryRun 成功的文档记为 `planned`（界面显示「DryRun 预检已通过」）而不是 `succeeded`：它是「准备好了」，不是「已经
发生了」，而 DryRun 最不该让人以为的就是对象已经写进去了。

**权限逐文档判定，整份拒绝**，详见[安全与权限](../security/authorization.md)。请求携带的 `namespace` 只为
未写明 `metadata.namespace` 的命名空间级文档填充，等价于 `kubectl -n`；文档自带且与之不同时该文档被判为无效
而不是被静默改写——把对象悄悄挪到另一个命名空间是没人会要的那种解释。集群级对象不接受填充，自带 Namespace
同样判为无效。

拒绝的粒度是**整份清单**，两种原因都是：有文档不被权限覆盖（403），或有文档无法解析成请求（400）。后者与前者
同理——带有拼错 Kind 的文件是操作者马上要修正后重新提交的，先把其余对象写进去只会让这次重新提交面对一个已经
改了一半的集群。DryRun 一律以 200 返回逐文档判定而不是报错，因为说清楚「是哪个文档、缺哪个权限」正是它的目的。

通用资源被拒绝时，服务端仍会先读取一次再判定该报 `cluster.resource.create` 还是 `cluster.resource.update`：
只有读过才知道对象是否已存在，而报错的那个权限名如果猜错，操作者会去申请一个根本不解决问题的权限。这次读取
只需要路由已经要求的 `cluster.read`。Secret、RBAC 与 Namespace 三族的创建与更新本就是同一个权限，无需读取即可
判定，Secret 更必须如此——它的读取本身就受同一个权限保护。

**解析严格度与单文档接口一致**：不接受 Anchor、Alias、重复字段或 YAML-only 类型，每个文档顶层必须是映射。
一份清单恰恰是「读起来是一个意思、实际是另一个意思」危害最大的地方——没有人会像审阅一个对象那样审阅三十个。
空文档（首尾的 `---`、模板渲染出的空白文档）跳过而不是拒绝，`kubectl` 也是这样。两个文档指向同一个对象会被
拒绝：apply 时结果取决于谁在后面，delete 时会把一次成功报成失败。`metadata.generateName` 不接受——服务端生成的
名字写不进文件，而对象名字随每次提交而变的清单根本无法被「再应用一次」，那正是 apply 的全部含义。

**执行不是原子的，界面照此呈现。** Kubernetes 没有事务：apply 按文档顺序、delete 按文档反序（清单通常先写容器、
后写其中的对象，反序删除才不会在文件尚未提到依赖对象时先移除承载它们的对象）逐条执行，遇到第一个失败即停止，
已写入的对象**不回滚**——回滚一次部分应用本身就是由 Server 而不是操作者选择的第二组破坏性操作。因此流程是
「预检 → 确认 → 执行」：预检逐文档解析、探测存在性、判定权限并调用 Kubernetes DryRun，把大部分错误挡在任何
写入之前；结果以表格逐条给出序号、对象、动作（创建/更新/删除/对象不存在）、结果与所需权限，而不是一句话——
一份文件里有很多对象，「将要发生什么」对每一个都是不同的答案。确认对话框要求输入**集群名称**而不是对象名称：
目标是一组对象而不是一个，而当同一份清单可以应用到任何集群时，写明集群才是那道真正要过的检查。执行后的结果表
逐条标出「成功」「失败」「未执行」，让操作者能在修正后重新提交。

delete 先读取每个对象，携带读到的 UID 与 `resourceVersion` 作为前置条件，因此不会删到同名重建的对象；对象
已不存在的文档记为「已跳过」而不是失败——文件描述的是一种期望的缺席，而对象确实不在。

审计粒度按集群中是否真的发生了变化决定：DryRun 与被整份拒绝的请求各写一条聚合记录（带文档总数与被拒绝数），
实际执行则逐文档记录。理由是前两者都没有改变任何对象，又都会被反复发起，逐文档记录会让每次预检往审计表里写
几十行，把真正写入了对象的记录淹没掉。详见[安全与权限](../security/authorization.md)。

清单正文最大 4 MiB，单次文档数量有上限（当前 64）。这两个接口使用独立的、比单对象写入更长的请求超时：一份清单
是若干次单对象写入的顺序执行，用约束一次写入的预算去约束它会拒掉本来会成功的文件；每个文档仍在普通操作超时内
执行，因此单个卡住的对象不会吃掉整个预算。左侧导航仅在当前身份至少持有一种相关写权限时显示该入口——这不是授权，
Server 仍逐请求判定，只是不去提供一个每次请求都被拒绝的页面。

Kubernetes 授权管理后端把命名空间级 ServiceAccount、Role、RoleBinding 与集群级 ClusterRole、
ClusterRoleBinding 分开定域。Role/ClusterRole 类型化写入完整规则，RoleBinding/ClusterRoleBinding 更新只替换
Subjects 并保留不可变的 RoleRef；聚合 ClusterRole 不接受类型化更新，避免清除 AggregationRule。
ServiceAccount 只返回关联 Secret 数量，不返回 Secret 名称、Token 或正文。

五类资源另有一对 YAML 路由，
`GET`、`PUT /api/v1/clusters/{cluster_id}[/namespaces/{namespace_name}]/authorization/{authorization_resource}/{authorization_name}/yaml`，
按作用域分开：命名空间级路径只接受 ServiceAccount、Role、RoleBinding，集群级路径只接受 ClusterRole、
ClusterRoleBinding，作用域不符的请求在到达服务之前就被拒绝。它们与类型化接口共用权限、CSRF、幂等键、
DryRun/确认和审计链路，并在提交前重跑类型化接口的全部规则校验；审计不记录 YAML 正文。

该能力使用独立的 `cluster.rbac.read/manage`，并从通用 Resource/YAML 入口排除上述五类资源——那条排除正是这里
需要专用 YAML 路由的原因：把它们放回通用入口，等于让持有 `cluster.resource.update` 的角色改写 RoleBinding。
ZKE 安装清单不会给 Agent `escalate`、`bind` 或 `impersonate`；类型化规则与 YAML 守卫同样拒绝这些 Verb 和
ServiceAccount Token，也不能改写 `roleRef`，API Server 还会拒绝其他超出 Agent 当前权限上限的规则或绑定。

规则里出现 `secrets` 不再被无条件拒绝，而是要求调用者在该 Cluster 上持有对应权限：只读 Verb 要
`cluster.secret.read`，其余 Verb 或通配符要 `cluster.secret.manage`，`resources: ["*"]` 视同点名 Secret，不满足
返回 `403 secret_rule_forbidden`。原因是 RBAC 规则本身就是在把访问权交给别人，所以它遵守和平台角色一样的
「不得授出自己没有的权限」；一刀切的代价则是无法用 ZKE 给 ServiceAccount 授予读取自身配置 Secret 的权限，
而那几乎是每个应用都要的。

指向 `zke-agent` 的绑定始终被拒绝，创建和给已有绑定追加主体都算——后者一度是缺口：Kubernetes 在这里不会拦，
因为执行者就是 Agent 自己、目标角色恰好是它自己的权限集；`managed-by` 标签也不会，因为新建的绑定不带该标签。
带 `app.kubernetes.io/managed-by=zke-server` 的 Agent ServiceAccount、
Role、ClusterRole 和绑定禁止通过该 API 更新或删除，YAML 路由同样只允许读取；任何对象也不能通过提交把这个标签
加到自己身上——那会把它从这套 API 中摘出去。

Console 授权管理页面按 ServiceAccount、Role、ClusterRole、RoleBinding、ClusterRoleBinding 五个标签页组织，
作用域与后端一致：前三类中的 ServiceAccount 和 Role 以及 RoleBinding 按命名空间定域，ClusterRole 与
ClusterRoleBinding 是集群级对象，工具栏的命名空间选择器只在前者出现。导航项按 `cluster.rbac.read` 隐藏，
写操作按 `cluster.rbac.manage` 门控；两者都与 `cluster.resource.*` 相互独立。

名称按 Kubernetes 自己的规则校验，而不是一律按 DNS 子域名：Role、ClusterRole 与两类绑定使用路径段名称，因此
`system:basic-user`、`system:aggregate-to-edit`、`kubeadm:cluster-admins` 这类带冒号的内置对象是合法的，只有
ServiceAccount 作为 core/v1 对象仍要求 DNS 子域名。Server 与表单用同一套规则——按 DNS 子域名校验会把集群里
每一个内置角色都判成非法输入，而它们恰好是最常被打开的那些对象。

不可操作的对象不给假入口：ZKE 管理的 Agent 授权对象在列表和详情中都不提供编辑与删除，并用 tooltip 和提示条
说明原因；聚合 ClusterRole 不提供类型化编辑，因为它的规则由控制器合成。绑定的编辑表单只替换 Subjects，
RoleRef 显示为只读并说明「要指向其他角色需要删除后重建」——Kubernetes 本就不允许改，而静默地重新指向一个
已有授权是最不容易被察觉的变更。

ServiceAccount 只显示关联 Secret 的数量，并在列表和详情中都写明「不展示名称」，与后端不返回 Secret 名称、
Token 或正文一致。规则编辑器的字段用逗号分隔，API 组留空即 core 组——空字符串在 Kubernetes 里是有意义的取值，
提交时会保留而不是当成空项丢弃。表单旁注说明 Agent 不持有 `escalate`/`bind`，超出其权限范围的规则会被
Kubernetes 拒绝；涉及 `secrets` 的规则还要求提交者本人持有对应的 Secret 权限。

本页的 YAML 入口走的是专用路由，而不是通用 YAML 接口：后者对这五类资源的整体排除没有放开，读取也一样被拒绝。
详情页头的 YAML 挂在 `cluster.rbac.read` 上，写入挂在 `cluster.rbac.manage` 上，与类型化表单同一组权限。

YAML 不是绕过类型化护栏的通道：提交前会重跑同一套规则——拒绝 `escalate`、`bind`、`impersonate` 与
`serviceaccounts/token`，`secrets` 按调用者持有的 Secret 权限条件放行（与类型化表单同一份判定），拒绝引用内置
`zke-agent` 角色，拒绝改写 `roleRef`，拒绝给对象添上 ZKE 的 managed-by 标签，ZKE 管理的授权对象只读打开。不这么做的话，这个编辑器就不是「表单没建模的字段的出口」，而是任何持有
`cluster.rbac.manage` 的人绕开全部检查去改写授权的地方。

聚合 ClusterRole 是这里唯一比表单更宽的地方：类型化编辑拒绝它，因为表单表达不了 `aggregationRule`，而一份
文档可以——这正是需要这个视图的场景。界面上说明规则由控制器合成，手写的 `rules` 会被覆盖。

Pod 类型化后端返回统一元数据、Phase、Ready、Node、Pod IP、控制器 Owner、镜像和总重启次数；详情补充
Annotations、完整 Owner References、调度与网络信息、主容器、初始化容器和 Ephemeral Container 的当前/上次
状态、资源 requests/limits、重启次数以及 Pod Conditions。删除 Pod 时必须携带当前 UID，避免误删同名重建对象；
由 Deployment、StatefulSet、DaemonSet、Job 等控制器管理的 Pod 删除后通常会被控制器重新创建。Pod 删除不是
Eviction，不执行 PodDisruptionBudget 语义；Logs 通过独立协议和最小 `pods/log` 权限实现，不会放宽通用
Subresource 边界；Exec 只通过独立 Pod Exec 协议开放。Eviction 只由节点 Drain 的精确 allowlist 使用，Pod
详情中的删除仍然是 DELETE，不会伪装成 PDB 感知的操作。

Pod 日志后端在读取前和打开 Kubernetes 日志流后分别核对 Pod UID，避免同名 Pod 重建竞态；支持主容器、
初始化容器和临时容器，`tail_lines` 最大 5000、`since_seconds` 最大 7 天。`previous` 不接受与 `follow`
同时使用——已终止的实例不会再产生新日志——因此一次被拒绝的上一个实例读取只剩一种含义。Agent 在请求
Kubernetes 之前先按容器状态判断该容器是否运行过：没有重启过就没有上一个实例，直接返回
`404 previous_logs_not_found`；已经重启过但 Kubernetes 仍拒绝时（上一个实例的日志已被节点清理）同样按该
错误返回。Kubernetes 对这两种情况都回 400，照直传给操作者就成了「请求内容无效，请检查输入」，而请求本身
是良构的，缺的是被请求的对象。快照和 Follow 都使用独立 QUIC
Stream 逐块转发，默认每条最多 16 MiB，Follow 最长 30 分钟。Follow 会周期重新验证 Session 和权限；客户端
断开、权限撤销、超时或 Agent 连接排空都会取消目标 Kubernetes 请求。默认 HTTP 正文为未包装的 `text/plain`，
终止状态和字节统计通过 Trailer 返回；Console 使用 `application/x-ndjson`，日志块以 Base64 帧承载，最终 `end`
帧明确区分正常结束、服务端字节上限、最长读取时长、权限撤销、取消和异常中断。日志正文不会进入 Server 日志或审计事件。

Web Terminal 后端使用两步会话：先创建短期、一次性且与用户、登录 Session、Cluster、Pod UID、容器和路径绑定的
票据，再以同源 WebSocket 消费。Agent 在 Kubernetes Exec 前重新读取 Pod 并核对 UID 和容器；到 Kubernetes
API Server 优先使用 WebSocket streaming protocol，仅在旧 API Server 或 HTTPS 代理无法完成 WebSocket Upgrade
时回退 SPDY。
启动命令固定为优先 `bash`、不存在时回退 `/bin/sh`，不接受客户端提供任意命令。会话具有最大时长、空闲超时、输入/输出字节
上限和 Server/单 Agent 并发上限，并周期重新验证 Session 与 `cluster.pod.exec`。审计只记录票据创建、目标和
会话结果，不记录或摘要终端输入输出。操作者可显式选择录制 stdout/stderr；该选择额外要求
`cluster.pod.terminal_recording.create`，stdin、Cookie、票据和认证头始终不录制。录制副本不超过终端输出上限，
默认保留 7 天，过期后清理；读取另需 `cluster.pod.terminal_recording.read`，并重新按 Cluster UUID 判权和按
Cluster、Namespace、Pod 名称、recording ID 联合定域，`cluster.pod.exec` 本身不授予历史输出读取能力。该顺序与 Kubernetes 1.31 开始默认使用 WebSocket 的
[Streaming Transitions](https://kubernetes.io/zh-cn/blog/2024/08/20/websockets-transition/) 一致；SPDY 只作为
未定义最低支持版本前的临时兼容路径。

Console 终端入口在 Pod 列表行和详情页，需要 `cluster.pod.exec`（默认只授予 admin）。终端占据整个应用视图，
打开前弹出确认，说明将在目标容器中启动交互式 Shell、命令以容器自身身份执行、以及默认不记录终端输入输出；
持有录制创建权限时可在确认框显式勾选输出录制，持有读取权限时可打开同一 Pod 名称下的历史列表并按帧时间回放。
确认后才创建票据并立即用它建立 WebSocket；票据不落任何前端存储，重新连接就是重新申请一张。视图打开时会重新
读取 Pod，若 UID 与入口绑定的不一致则拒绝连接并要求返回列表重开。终端实例在整个视图生命周期内只创建一次，
会话结束后保留回滚缓冲；离开视图或点击断开都会关闭 WebSocket，从而结束容器中的 Shell。终端尺寸由前端测量后
写入票据，并在连接建立时和每次窗口变化时下发 `resize`。承载终端的元素本身不带内边距和边框，边框与留白由外层
容器提供：xterm 的 fit 插件按父元素的 `getComputedStyle().height` 计算行列数，而在 `box-sizing: border-box`
下那是包含父元素自身内边距和边框的边框盒，直接量一个带 `p-2` 和边框的面板会多算 18px——超过一行的高度，
最后一行会被画到可视区域之外裁掉，宽度上多出的部分也会让下发给容器 TTY 的列数偏大。

终端使用 [xterm.js](https://xtermjs.org/)（MIT，`@xterm/xterm` 与 `@xterm/addon-fit`）作为终端仿真器：交互式
Shell 需要完整的 ANSI/VT 序列、光标寻址与回滚支持，自行实现不现实。它按需加载为独立 chunk，不进入主包，
未打开终端的操作者不会为此付出加载成本。

Pod 端口转发使用独立的 `cluster.pod.port_forward` 权限和 `pod-port-forward.v1` Agent 能力，不复用终端或
通用资源权限。Server 创建与用户、登录 Session、Cluster、Namespace、Pod UID、单个端口和路径绑定的短期
一次性票据，再升级同源 `zke.pod-port-forward.v1` WebSocket；二进制消息承载原始 TCP 字节，最终文本消息只
携带终止原因与双向字节数。Agent 在传输前重新读取 Pod 核对 UID，通过 client-go 的 WebSocket-first/SPDY
fallback port-forward 在 `127.0.0.1` 随机端口建立进程内桥接，不对节点或外部网络暴露监听端口。会话受总时长、
空闲、双向字节和 Server/单 Agent 并发上限约束，并周期重验 Session 与权限。审计记录票据、Pod UID、端口和
结果，不记录流量正文。Console 提供一次 HTTP GET 原始响应预览；非 HTTP 客户端可直接使用同一 WebSocket
二进制协议，Console 不把该能力伪装成本机 `localhost` 监听。

Kubernetes Event 后端固定读取所选 Cluster 与 Namespace 的 `core/v1/events`，不接受调用方覆盖 GVR。默认
通过 SSE 返回有界初始快照；实时 Follow 使用独立 `resource-watch.v1` QUIC Stream，支持按关联资源 UID、Kind、
Name、Event type 和 reason 过滤，并通过 resourceVersion/`Last-Event-ID` 恢复。Session 或
`cluster.event.read` 被撤销时立即取消，`410 Expired` 以 `resource_version_expired` 的正文内 close 原因提示
客户端重新拉取快照。Event 正文不写入日志或审计。

describe 接口回答的是「这个对象为什么没跑起来」。这个答案原本分在两处：对象详情说某个容器在 waiting，
为什么则要离开当前页面，去命名空间事件流里翻找同一时间窗内所有对象的事件。describe 在 Server 上做这次
join——读对象、按对象 UID 读它自己的 Event、给出两者共同支持的结论——并且不向 Agent 协议增加任何能力：对象
走既有资源服务，Event 走 Event 流用的同一条有界 `resource-watch.v1`，只是关掉 Follow 当快照用。

Event 按 UID 而不是名称过滤：同名重建的 Pod 会留下前一个对象的事件，把它们挂到当前对象上，等于用一场已经
结束的故障解释眼前的现象。快照默认 50 条，按时间正序返回，超出窗口时以 `truncated` 说明。

诊断结论（findings）只报告问题，且只从 Kubernetes 已经报告的状态读出，不做推断：退出码 137 就报成退出码
137，只有 reason 明确是 `OOMKilled` 时才报成内存超限——137 是 SIGKILL，谁发的信号需要 Kubernetes 自己说。
每条结论携带稳定 code、Kubernetes 原始 reason 与 message 原文，以及指向 Condition、容器状态或 Event 的证据，
面向操作者的中文标题与处理建议由 Console 按 code 渲染，Server 不写这层措辞。Pod 规则包括
无法调度（`PodScheduled=False` 加调度器的 `FailedScheduling` 事件）、镜像拉取失败、容器配置错误（引用的
ConfigMap/Secret 不存在）、反复重启、容器异常退出、内存超限被终止、存储挂载失败和探针未通过。探针只在
Running 且未就绪时报告——启动过程中失败一次随后通过是常态，报出来会让每个热身稍慢的 Pod 都变成告警。
Node 规则包括 Ready 异常、Memory/Disk/PID Pressure、NetworkUnavailable、停止调度，以及 requests 或 Pod 数达到
可分配量 90% 的容量信号。除下文单独建模的 PVC、Service、Ingress、Gateway、HPA 与策略状态资源外，其他类型走通用 describe，只返回身份与自己的
Event，findings 恒为空数组：没有为某个类型写过的规则不是规则。

类型化诊断的覆盖边界如下。这里的“无类型化入口”是有意的产品边界，不表示忘记接按钮；只有当该类型出现可由
Kubernetes status、Condition、明确关联对象或精确 Event 归属证明的故障语义时，才应新增规则。

| 资源                                                     | 当前诊断能力    | 边界                                                                                     |
| -------------------------------------------------------- | --------------- | ---------------------------------------------------------------------------------------- |
| Pod                                                      | 类型化          | Condition、容器当前/上一次状态、精确 UID Event                                           |
| Deployment、StatefulSet、DaemonSet、Job、CronJob         | 类型化聚合      | owner UID 链、控制器、Pod、模板引用 PVC 与有界关联 Event                                 |
| Node                                                     | 类型化聚合      | Node Condition、taint、已分配非终止 Pod 与 scheduler requests；Node Event 受三层精确过滤 |
| Service、Ingress、Gateway、五种 Gateway API Route        | 类型化          | EndpointSlice/后端引用或 Gateway Controller Condition；不跨 Namespace 读取 Secret/Route  |
| PersistentVolumeClaim                                    | 类型化          | phase、Condition 与精确 UID Event                                                        |
| HorizontalPodAutoscaler                                  | 类型化聚合      | 标准 HPA Condition；只补充已知 apps/v1 目标状态，不读取目标 Event                        |
| ResourceQuota、PodDisruptionBudget                       | 类型化          | quantity 用量或 `DisruptionAllowed` Condition                                            |
| ConfigMap、LimitRange、NetworkPolicy                     | 无类型化入口    | 没有可可靠解释为对象故障的 status；可在资源对象浏览器使用通用 Event-only describe        |
| Secret                                                   | 无诊断入口      | 敏感资源不经过通用资源/describe 路径，不能为事件便利放宽 Secret 读取边界                 |
| Namespace、PersistentVolume、StorageClass、PriorityClass | 无类型化入口    | 集群级 Event 的 Namespace 归属不确定，且当前没有对象级确定性规则                         |
| Role、RoleBinding、ClusterRole、ClusterRoleBinding       | 无诊断入口      | 授权对象没有故障 status，且继续由独立 RBAC 权限与专用接口隔离                            |
| 其他可发现的命名空间级主资源                             | 通用 Event-only | 返回对象身份和按 UID 过滤的 Event，findings 为空；不把未知类型猜成已知家族               |
| 其他可发现的集群级主资源                                 | 无 Console 入口 | Server 即使收到通用 describe 也以 `unsupported_scope` 明示不读取 Event                   |

PVC 的独立 describe 复用工作负载聚合中同一条 `PVCPending` 规则。Bound PVC 不报告问题；Pending PVC 先从自身
Event 中选择最近的 `WaitForFirstConsumer`、`ProvisioningFailed` 或 `FailedBinding` 原因与消息，事件窗口没有对应
记录时再使用 PVC Condition。存储页只在 PersistentVolumeClaim 标签显示诊断入口；PV 与 StorageClass 是集群级对象，
在没有明确且安全的 Event 归属与诊断规则前不提供一个只会返回空结论的入口。

Service 的独立 describe 以 `discovery.k8s.io/v1 EndpointSlice` 作为端点事实来源，统计全部、Ready、Serving 与
Terminating 端点，并报告没有端点、存在端点但没有 Ready 端点，以及 LoadBalancer 外部地址仍未分配三类问题。
selector 只用于补充展示可能作为后端的 Pod，不用于判断端点是否存在：selectorless Service 可以由人工维护的
EndpointSlice 提供后端。ExternalName Service 不要求 EndpointSlice。EndpointSlice 与 Pod 各只做一次有界 List，
Pod 不健康优先且最多展示 10 个；列表截断或读取失败时显式降级，并不基于不完整统计生成缺失端点结论。

Ingress 的独立 describe 对默认后端和每条 host/path 引用的 `Service:port` 去重，最多诊断 20 个组合。后端聚合只
增加一次同 Namespace Service List 和一次带 `kubernetes.io/service-name in (...)` 集合选择器的 EndpointSlice
List，不按路径逐个往返。它报告入口地址尚未发布、Controller 通过 Warning Event 明确拒绝或同步失败、Service
不存在、Service 端口不存在、没有端点和没有 Ready 端点；Controller 事件的 reason 与 message 保留原文。Service
或 EndpointSlice 清单存在下一页时，计数按下限展示，但不把未出现在当前页解释为不存在或没有端点。Ingress 的
自定义 Resource backend 当前不在类型化投影中，因此不会被误报成缺失 Service。

Gateway 的独立 describe 直接使用 Gateway API Controller 报告的对象与 Listener Condition，不猜测 Controller
内部状态。对象级规则覆盖地址尚未分配，以及 `Accepted`、`Programmed`、`Ready` 不为 True；Listener 规则覆盖
未接受、未编程、配置冲突和 `ResolvedRefs` 失败。每条结论保留 Condition 的 reason、message 与带 Listener 名称的
证据路径。Gateway 不额外枚举 Route：Listener 已提供 `attachedRoutes`，避免把一次 Gateway 诊断扩大成全 Namespace
Route 清单读取。

Route 的独立 describe 使用每个 `status.parents` 条目中的 Controller Condition：没有任何条目报告未绑定；
`Accepted` 不为 True 报告父级未接受；`ResolvedRefs` 不为 True 报告后端/授权引用未解析；
`PartiallyInvalid=True` 报告部分规则无效。ParentRef 只作为关联对象身份展示，不据此读取父级或后端所在的其他
Namespace；特别是 `RefNotPermitted` 直接保留 Controller 对 ReferenceGrant 的判定和原始消息，不能为了诊断便利
绕过调用者的 Namespace 与资源读取边界。

HPA 的独立 describe 使用 `status.observedGeneration` 与 `AbleToScale`、`ScalingActive`、`ScalingLimited`
三个标准 Condition，分别报告控制器状态滞后、无法读取或更新伸缩目标、指标不可用和伸缩受到上下限约束；reason
与 message 保留 Kubernetes 原文。它只对类型化 HPA 表单支持的 `apps/v1 Deployment` 和 `StatefulSet` 读取一次
同 Namespace 目标详情并展示就绪副本与目标自身的工作负载结论。自定义 scale target 仍是合法对象，保持只由 HPA
Condition 解释，不因未读取目标而降级；已知目标读取失败则以 `autoscaler.target` 显式降级。Event 只按 HPA 自身
精确 UID 读取，不读取目标 Event，避免一次诊断变成跨对象事件通道。

策略页只为具有可解释状态的 ResourceQuota 与 PodDisruptionBudget 提供独立 describe。ResourceQuota 使用
Kubernetes quantity 语义比较每个 `status.used` 与 `status.hard`，`1000m` 与 `1` 会被正确识别为相等；一项或
多项达到上限时报告 `ResourceQuotaExhausted`，摘要列出每个资源键的已用、上限和是否耗尽。PDB 只在 Controller
明确报告 `DisruptionAllowed=False` 时报告 `PDBNoDisruptionsAllowed`，保留 reason 与 message；这表示当前 eviction
会被预算保护拦截，可能是符合预期的保护状态，不被描述成 Controller 故障。LimitRange、NetworkPolicy 与
PriorityClass 没有能可靠表达对象故障的 status，因此不提供一个只返回空结论的类型化入口；命名空间级对象仍可从
资源对象浏览器使用通用 Event-only describe。

describe 同时读取资源与 Event，因此要求调用方同时持有 `cluster.read` 与 `cluster.event.read`，两个检查都在
路由层且各自留下自己的拒绝记录，被拒时能看出缺的是哪一个；只要 `cluster.read` 就能通过的话，describe 会成为
绕开 `cluster.event.read` 读取命名空间事件的通道，而 Event 恰恰是这里最值钱的那一半。Event 读取写入与 Event
流一致的审计记录，避免同一件事经由两条路径时审计只记下其中一条。通用集群级对象不返回 Event：Event 归属哪个
Namespace 属于约定而非规则，接口以 `events.omitted=unsupported_scope` 说明，而不是猜一个 `default`。Node 是
显式建模的例外：只允许关闭 Follow 的一次性快照，并在 Server、协议校验和 Agent 三层强制
`involvedObject.kind=Node` 与精确 UID，不能借此读取宽泛的跨 Namespace Event。Event
读取本身失败时接口仍返回对象部分，以 `events.omitted=unavailable` 与 `degraded_sections` 说明——静默返回空
事件列表会被读成「这个对象什么都没发生过」。

工作负载的 describe 多做一件事：沿 owner 链下钻。工作负载自身通常不是答案——副本不足只是现象，原因在没能
创建出 Pod 的控制器上，或在已创建但起不来的 Pod 上。因此接口按类型走不同的链路：Deployment → ReplicaSet →
Pod，CronJob → Job（不再下钻到 Pod，那属于对应 Job 的 describe，再走一级只会对「哪些 Pod 属于谁」做出更弱的
判断），StatefulSet、DaemonSet 与 Job 直接到 Pod。可使用 Pod Selector 的链路先以它缩小列表，最终每一跳都按
controller owner UID 过滤，与修订历史用的是同一条规则：标签相同不等于归属相同，而按 UID 匹配还避免同名重建
的控制器借用别人的对象。

关联 Pod 按不健康优先排序后截断（上限 10 个），控制器取最近创建的 2 个，Pod 模板引用的 PVC 去重后最多读取
10 个；`truncated` 说明还有未展示的关联对象，但不承诺被省略的对象均为健康状态。每个 Pod 携带按 Pod 规则得出
的结论，Pending PVC 给出 `PVCPending`，并在事件窗口包含 `WaitForFirstConsumer`、`ProvisioningFailed` 或
`FailedBinding` 时附上其原始原因和消息。工作负载自身的结论只包含它作为控制器的问题：
进度停滞（`Progressing=False`）、副本创建被拒绝（`ReplicaFailure=True` 或 `FailedCreate` 事件）、任务失败
（Job 的 `Failed=True`）。副本创建被拒绝是 Pod 级规则永远看不到的一类失败——配额、Pod Security 准入或
ServiceAccount 缺失让创建本身失败，根本没有 Pod 可查，而对 Deployment 来说这条事件记录在 ReplicaSet 上，
这正是接口要读关联控制器事件的原因。

事件按对象逐个读取并合并成一条时间线，每条注明它属于哪个对象。读取次数有上限（工作负载自身，加最多 4 个
排在前面的关联对象）；控制器和 PVC 排在 Pod 前面，为只存在于 ReplicaSet 的 `FailedCreate` 与只存在于 PVC 的
供应/绑定原因保留读取预算，同批 Pod 失败则由前几个代表。被读到事件的 Pod 会用
这些事件重新推导一次结论——状态只能说「Unschedulable」，调度器的事件才说清是哪种资源不够。工作负载自身的
事件读取失败会让整段事件为空，关联对象的读取失败只是让时间线变短，两者分别以 `events` 和 `events.related`
出现在 `degraded_sections` 中。

Node 的 describe 只用一次跨 Namespace Pod List，Field Selector 固定为 `spec.nodeName=<node>`，不会按 Pod 逐个
往返。非终止 Pod 的 requests 使用 Kubernetes scheduler 语义计算，包含普通容器、init/restartable init、
Pod-level resources 和 overhead，不等同于实时利用率；Pod 列表单页最多 500 个，关联视图不健康优先并最多展示
10 个。如果列表还有下一页，CPU、内存 requests 与 Pod 数只代表已读取部分的下限，响应显式标记 `truncated`，
且不生成 90% 容量结论，避免用不完整分母分子给出确定判断。Node Event 只读取 Node 自身，不扇出读取其上每个 Pod
的事件。

Console 诊断入口在 Pod、工作负载、Node、PVC、Service、Ingress、Gateway、HPA、ResourceQuota 与 PodDisruptionBudget 的列表行和详情页页头，在详情页排在 YAML 之前：打开
一个没跑起来的对象时，这就是操作者带着的那个问题。资源对象浏览器的命名空间级类型行上也有同一个入口（集群级
类型不提供，那里的视图只会说自己没有可展示的事件）。页面上方是结论卡片，中间是关联对象（不健康的逐个展开并带上自己的结论，就绪的
折成一行），下方是事件表，工作负载的事件表多一列说明每条属于哪个对象。页头提供「复制为文本」，把结论、关联
对象与事件渲染成可直接贴进工单的纯文本；该文本在前端生成，不是第二份由服务端维护、会与界面漂移的措辞。诊断
主体整体纵向滚动，事件表保留可展示多行的最小高度，避免小窗口被上方结论和关联对象压缩成只有表头。没有
`cluster.event.read` 时入口不出现——一个按下去必然返回 403 的按钮不如不给。

诊断页内的处理入口形成一条可返回的证据链：CrashLoopBackOff、异常退出和 OOMKilled 可直接打开对应容器的
上一次日志，探针失败打开当前日志；Pod 日志按钮只在调用者持有 `cluster.pod.logs.read` 时出现，并固定携带诊断
快照中的 Pod UID，防止同名重建后读到另一个实例。关联 Pod、PVC、Ingress 后端 Service 与 HPA 目标工作负载可
继续打开各自诊断；每次跳转都叠在当前诊断之上，返回时保留原页面的滚动位置和快照。Condition 证据标签会滚动并
短暂高亮对应的原始 Condition，Event 证据标签会定位到时间线中的精确 Event。页头的「精确事件」用对象 UID、
Kind 和 Name 一起下推到 Event Watch，不会混入同名重建对象；该入口继续要求 `cluster.event.read`。这些前端入口
只负责可达性，日志、事件和关联 describe 的最终权限检查仍分别由 Server 端对应路由执行。

Console YAML 入口出现在支持它的各分区详情页（节点、命名空间、工作负载、Pod、配置管理、存储、服务与路由、
自动伸缩、策略管理、授权管理与资源对象浏览器），占据整个应用视图。同一个编辑器接三条后端路由——通用、Secret
和授权——由调用方按对象所属的族指定，界面上没有区别。文档按 Kubernetes 原样展示，
包含 `metadata.uid` 和 `metadata.resourceVersion`——Server 正是拿它们做写入前置条件，把它们「整理掉」等于
去掉了防止基于陈旧读取覆盖他人改动的保护。编辑器不折行：YAML 靠缩进表达层级，软折行会读成一层并不存在的
嵌套。语法高亮由仓库内的轻量 Tokenizer 提供，着色层画在一个透明 textarea 之下，因此原生选区、撤销、输入法
组合和读屏行为都保持浏览器自身的实现，高亮出错最多丢失颜色，不会改动任何一个字符；着色只区分键、引号字符串、
数字与布尔等字面量、注释、标点和 Anchor/Tag，未加引号的普通标量保持正文色，超过 200,000 字符的文档直接放弃
着色而不是让编辑变卡。着色层跟随 textarea 的滚动位置，用的是 transform 位移而不是自身的
`scrollTop`/`scrollLeft`：只有 textarea 为滚动条留出布局空间，它因此比着色层矮一条水平滚动条、窄一条垂直
滚动条，也就能多滚这么多；把它的偏移量赋给滚动位置会被浏览器钳制在着色层自己较小的上限上，于是恰好在文档
滚到尽头时颜色落后字符一条滚动条的距离，位移则没有可被钳制的上限。保存走两步，先服务端 DryRun，再在确认弹窗中要求输入对象名称，并说明整份文档会替换现有对象、文档中
未出现的字段会被移除。保存成功后重新读取一次，使编辑器持有新的 resourceVersion 而不是刚刚被用掉的那个。
没有对应写权限时页面只读并说明原因；不可变的 Secret 与 ZKE 自身的授权对象同样只读打开，说明的是「为什么不能
写」而不是「你没有权限」——那是两件不同的事。文档超过 4 MiB 时在提交前就拒绝，不做无谓往返。

Console 事件页面按所选 Cluster 和 Namespace 展示 Event，可按 Event type、关联资源 Kind/名称和 reason 筛选。
筛选分两层执行。Event type 与关联资源 Kind 是下拉选择，由服务端转成 Field Selector 下推到 Watch，快照上限因此
只花在已经匹配的事件上；两者都不做输入，是因为 Field Selector 只能精确匹配，手输的 `pod` 或 `Pods` 只会静默
返回空列表。type 的选项与列表「类型」列共用同一份文案；Kind 的选项来自目标集群 API Discovery 的资源目录（因此
包含 CRD），并补上当前事件中出现过的 Kind，使 Discovery 不可用时仍可筛选。关联资源名称与 reason 则是客户端
子串匹配：Kubernetes 没有模糊 Field Selector，把「名称的一部分」下推会变成另一种语义，因此它们只作用于已读取
的事件，界面在筛选生效时说明这一点，并同时给出匹配条数与已读取条数。默认开启实时跟随。事件按 UID 归并：
Kubernetes 用递增 `count` 的 MODIFIED 表示同一事件重复发生，因此新帧替换原行而不是堆叠；DELETED 移除该行。
列表按最近发生时间倒序，客户端最多保留 1000 条。

Console 不使用 `EventSource`，而是用 `fetch` 自行解析 SSE 帧。`EventSource` 不暴露失败连接的 HTTP 状态，
而这里的 409 `resource_version_expired`、403 和 429 需要区分处理，其中第一种还要求客户端执行特定恢复。
按后端约定，正文内 `close.reason` 为 `watch_closed` 时从 `last_resource_version` 续读，为
`resource_version_expired` 时丢弃当前列表并重新拉取快照；网络或代理在正文内 `close` 到达前断开时，从已解析
SSE `id` 保存的最后 resourceVersion 续读。其余原因结束读取并在状态栏说明；恢复之间固定等待 1 秒，避免立即
关闭的 Watch 造成重连风暴。

事件页面需要 `cluster.event.read`。当前身份在所选项目没有该权限时，左侧导航不显示「事件」类别；服务端仍然
独立判定。

Console Pod 页面列出所选命名空间的 Pod，展示 Phase、就绪与终止状态、控制器 Owner、节点、Pod IP、累计重启
次数和创建时间，可下钻到包含调度与网络信息、主容器/初始化容器/临时容器的当前与上次状态、资源
requests/limits、Owner References、Conditions、标签和注解的详情页。Phase 与就绪分开展示，因为 `Running`
并不等于健康；带 `deletionTimestamp` 的 Pod 单独标记为「删除中」。删除需要 `cluster.resource.delete`，同样
先执行 DryRun 再确认，确认弹窗要求输入 Pod 名称，并在该 Pod 有控制器时明确说明删除后通常会被重新创建。
页面不提供 Exec 或 Eviction 入口。

Console 日志入口在 Pod 列表行和详情页，需要 `cluster.pod.logs.read`。日志读取占据整个应用视图而不是弹窗，
可以按容器（含初始化容器与临时容器）、尾部行数、时间范围、时间戳和「上一个实例」读取，并支持实时跟随。
请求固定携带打开日志入口时的 Pod UID；详情刷新发现同名 Pod 已重建时停止读取并要求返回列表重新打开。跟随流
在切换任一选项、点击停止或离开视图时被 abort，Server 据此取消目标
Kubernetes 请求。点击停止只放弃请求本身：已经到达的行留在屏幕上，同时跟随模式关闭，因此之后的重新加载、切换容器
或勾选选项读取的是快照，而不是又开一条跟随流；重新跟随是再点一次该按钮，那会以当前选项发起一次新的请求。
浏览器端缓冲上限约 2 000 000 字符，超出后丢弃较早的行并在状态栏说明；自动滚动只在视图已
位于底部时生效，操作者向上翻阅时不会被拉回。

缓冲区不是一个字符串，而是按约 16 000 字符封存的分页，页面为每一页渲染一个块级元素。浏览器修改一个文本节点
就要重排它所在的整块内容，而这笔开销取决于缓冲区已经有多大，与本次新到多少字节无关：以 8 KB/帧驱动到缓冲
上限实测，单个文本节点每次更新约 150 毫秒，分页后约 2.3 毫秒。跟随一个输出密集的容器恰好是缓冲区最满、数据
最密的时候，前者会让整个标签页停止响应。分页后每次更新只重排仍在增长的那一页，丢弃较早内容也变成移除整页，
因此开销不再随缓冲区增长。长度超过一页的单行会在页边界被截断封存，这类页改用行内渲染，使浏览器仍把它们排成
同一行——否则一行长日志会在非页边距处断开，跨该处选中的文本还会多出一个换行。复制与下载取的是拼回的完整缓冲，
不是 DOM 文本。

Server 的原始文本表示继续通过 HTTP Trailer 返回终止状态和字节统计。主流浏览器的 `fetch` 不暴露 Trailer，
因此 Console 请求 NDJSON 表示：任意二进制和换行安全地放入 Base64 `chunk` 帧，最终 `end` 帧携带
`succeeded`、`limit_reached`、`timeout`、`canceled`、`access_revoked` 或 `failed`。状态栏据此显示精确原因，
手动停止则由浏览器本地标记为 `stopped`；正文缺少最终帧会按协议中断而不是误报正常结束。

Console 工作负载页面在目标 Cluster 和 Namespace 内按类型切换 Deployment、StatefulSet、DaemonSet、Job 和
CronJob，列表展示状态、副本或 Job/CronJob 进度、镜像和创建时间，可下钻到包含副本或 Job/CronJob 状态、
配置、容器、Selector、条件、标签和注解的详情页。

列表行和详情页提供与后端一致的变更操作：Deployment 和 StatefulSet 伸缩，Deployment、StatefulSet 和
DaemonSet 滚动重启，CronJob 暂停和恢复，以及五类工作负载删除。伸缩、重启和暂停/恢复需要
`cluster.resource.update`，删除需要 `cluster.resource.delete`；Console 只在权限和资源类型都允许时展示对应
菜单项，实际判定仍由 Server 执行。每个操作都先提交一次服务端 DryRun，通过后再在确认弹窗中展示目标集群、
命名空间、对象 UID 和具体影响，删除还要求输入对象名称。伸缩确认提交的是 DryRun 校验过的副本数，而不是
确认时输入框中的值。每个步骤各自携带一个在弹窗生命周期内稳定的幂等键，因此重试同一次提交不会重复执行，
滚动重启也不会产生第二轮滚动。

创建表单需要 `cluster.resource.create`，并直接创建当前标签页所选的类型，不再提供第二个类型选择器。它占据
整个应用视图而不是弹窗：Pod 模板是一个工作负载的绝大部分，把四十个字段塞进一个盖在列表上的盒子里，比离开
列表更难读。表单按类型只渲染该类型接受的字段：Deployment 和 StatefulSet 有实例数量，StatefulSet 另有 Service
名称，Job 和 CronJob 有并行度、完成数、失败重试上限与完成后保留秒数，CronJob 另有 Cron 表达式、时区、并发
策略、启动截止秒数、历史保留数量和「创建后暂停」。留空的可选字段不会出现在请求里——Server 会拒绝而不是忽略
不适用的字段，因此空值必须是缺席而不是空串。表单在提交前对全部字段做一次与 Server 同形的校验：名称按类型的
长度上限、标签与注解的键名和取值、数据卷来源、容器名与镜像、环境变量及其引用的对象名与键、CPU/内存/GPU 数值
与 requests 不大于 limits、挂载路径与子路径、探针和钩子的端口与参数、容忍的取值与时长、集合数量上限，以及
CronJob 的 Cron 表达式形状和时区，`zke.io/workload-id` 与 `zke.io/description` 作为保留键在表单中被拒绝；最终
判定仍由 Server 执行。创建同样走 DryRun 预检和确认弹窗，确认弹窗会点出特权容器、主机路径数据卷和容忍调度这三
类需要注意的配置。

容器以标签条组织，「添加容器」在同一份 Pod 模板中追加一个；初始化容器不是另一个列表，而是容器自身的一个开关，
与 Kubernetes 的模型一致——init container 与主容器是同一种模板，区别只在于运行时机；打开这个开关会同时收起并
清空探针与生命周期钩子，因为初始化容器不接受它们。常用字段（名称、镜像、镜像版本、拉取策略、环境变量、
CPU/内存、GPU）直接展开，其余收在「显示高级设置」之后。CPU 以核、内存以 MiB、emptyDir 容量上限以 MiB 输入，
提交时转换为 Kubernetes quantity；GPU 卡数写入 `nvidia.com/gpu` 限额，是否可用取决于集群是否安装了对应的设备
插件，ZKE 不安装也不伪造它。数据卷在 Pod 层声明一次，容器的「数据卷挂载」只能从已声明的卷中选择，因此表单里
不存在一个指向不存在的卷的挂载。一次只看得到一个容器，因此有问题的容器会在标签条上标出——否则一个看不见的
错误只会表现为一个没有原因的禁用按钮。同样地，校验消息显示在能够修正它的那个区块里，而不是堆在表单末尾：一条
关于名称的提示出现在十屏之外没有意义；底部按钮旁只说明是哪个区块拦住了提交。

工作负载编辑复用创建表单，而不是另做一张更小的表：一个只在创建时出现的字段，等于操作者可以设一次却永远
改不回来。差别在于哪些字段被固定，以及写入方式。

更新是合并而不是整体替换，这一点与 Service、策略等较小的类型化资源相反。那些对象是被整体读取的，因此也被
整体替换；而一个 Pod 模板还带着 ServiceAccount、主机网络、hostPort 与 `securityContext`
的其余字段——它们不在本表单范围内，却都是某个人特意设过的。整份替换会让一次「只想改镜像标签」的
保存把它们一并删掉。因此 Server 先按 UID 与 resourceVersion 读回对象，把表单建模的字段写上去，其余文档原样
保留：容器按名称合并，保留该容器的 hostPort、终止设置与 `securityContext` 其余字段；`fieldRef` 等本表单无法表达的
环境变量来源、projected/CSI 等无法表达的数据卷来源、gRPC 等无法表达的探针，都在提交回来时保持原状——它们读取
时就只返回一个名称，没有可显示的内容，写回时也不会被清空。滚动重启写在 Pod 模板上的 `zke.io/restart-request`
注解同样保留：删掉它本身就是一次模板变更，会再触发一轮滚动。

Kubernetes 自身的不可变约束如实反映，不是本表单的取舍：名称与 StatefulSet 的 `serviceName` 只读并说明原因；
Job 的 Pod 模板、选择器、完成数和失败重试上限创建后不可变，因此编辑一个 Job 时表单只显示并行度和完成后保留
秒数，其余分区不渲染，并说明要改变 Job 运行的内容只能新建一个 Job。Deployment、StatefulSet、DaemonSet 与
CronJob 的模板可变；Pod 模板的任何变化都会触发滚动更新，确认弹窗按类型说明这一点，CronJob 则说明改动对下一次
触发的 Job 生效。

回填要求详情接口返回完整的类型化模板，因此工作负载详情现在返回容器的命令、参数、工作目录、环境变量、资源
requests/limits、挂载、端口、探针、生命周期钩子和特权开关，以及 Pod 层的数据卷、镜像访问凭证、节点标签选择、容忍、亲和性和拓扑分布约束，
并把 Job/CronJob 声明的执行参数与调度参数一并返回。响应与请求同形——读到的就是将要写回的，否则读侧缺的每一个
字段都会变成保存时被清空的字段。CronJob 的并行度、完成数等返回在 `parallelism`、`completions` 等字段上而不是
`job` 状态里：CronJob 自己没有 Job，一份全是零的状态会被读成「运行过且什么都没做」。

数量保持原样提交。表单以核和 MiB 输入，而 `1Gi` 显示成 1024 再写回 `1024Mi` 是同一个量、不同的对象，会在
一次什么都没改的保存上触发滚动更新；因此仍显示为原值解析结果的字段按原始字符串提交。表单读不懂的数量（例如
`1.5Gi` 换算不成整数 MiB）显示为空，并与 `nvidia.com/gpu` 之外的扩展资源、`ephemeral-storage` 等一起原样保留，
而不是被一次没有提及它们的更新删掉。

编辑入口在列表行菜单和详情页，需要 `cluster.resource.update`。表单在对象读取完成后才挂载，UID 与
resourceVersion 在挂载时固定，不随后台重新拉取更新：取一个更新的版本号会把本该被服务端拒绝的冲突变成静默覆盖。
写入同样先 DryRun 预检再确认，并沿用 CSRF、幂等键与审计链路。

Console 的容器高级设置以结构化行编辑端口，不开放 hostPort。亲和性与拓扑分布的 AND/OR 嵌套层级无法用一张
扁平表准确表达，因此「高级调度」保留完整的接口 JSON（snake_case）编辑：编辑现有对象时从详情原样格式化回填，
本地先检查 JSON 根类型与 128 KiB 上限，Server 再严格拒绝未知字段并执行上述语义校验，最后仍由目标集群 DryRun
判定 schema、版本和准入策略。更新中省略这些新增字段表示兼容旧客户端并保留当前值；Console 始终显式提交，空对象
或空数组表示有意清除。该能力没有新增旁路接口，仍使用原工作负载创建/更新的权限、目标作用域、审计与并发前置条件。

通用接口返回 Unstructured JSON，并移除 `metadata.managedFields`。Discovery
目录表示 API Server 暴露的资源，不代表 Agent ServiceAccount 已获授权；管理更多内置资源或任意 CR 时，安装方
需要显式扩展该 ServiceAccount 的最小 RBAC，ZKE 无需增加新的资源协议或 HTTP Handler。

仓库包含默认跳过的本地真实集群 E2E。设置 `ZKE_LIVE_KUBERNETES_E2E=1` 后，它使用当前 kubeconfig，通过
真实 QUIC Stream 验证 Namespace、ConfigMap、Deployment、CRD 和自定义资源的 CRUD、四类 Patch、DryRun、
冲突与幂等重放，并使用随机名称和精确清理避免污染日常集群。

后续规划能力包括：

- 创建工作负载时联动创建 Service 与 HorizontalPodAutoscaler（需要先定义多对象写入的部分成功与回滚语义）；
- 面向具体资源的表单化创建、更新和删除体验。

产品体验将参考成熟 Kubernetes 管理平台的通用实践，但不会以与任何现有平台完全相同为目标。
