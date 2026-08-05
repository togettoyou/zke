# 容器服务

容器服务是单集群应用。用户进入应用时需要先选择一个 Kubernetes 集群，进入后所有页面和操作均作用于当前集群。

当前已完成 Node、Pod、Service、Ingress、Gateway、ConfigMap、PersistentVolume、PersistentVolumeClaim、StorageClass、HorizontalPodAutoscaler、
五类策略对象与 Kubernetes RBAC
类型化接口、Namespace 管理闭环、五类工作负载类型化后端管理和通用主资源 CRUD 底座：

- `GET /api/v1/clusters/{cluster_id}/overview`：聚合 Node、Namespace、Pod 和五类工作负载的状态计数，
  以及 Node 容量/可分配量与非终态 Pod 请求量；
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
- 工作负载详情作用域下的 `scale`、`restart`、`suspend` 和 `resume` 动作：分别支持
  Deployment/StatefulSet 伸缩，Deployment/StatefulSet/DaemonSet 滚动重启，以及 CronJob 暂停和恢复；
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
- `GET`、`POST /api/v1/clusters/{cluster_id}/storage/{storage_resource}` 和对应单对象
  `GET`、`PUT`、`DELETE`：固定管理集群级 `persistentvolumes` 或 `storageclasses`；
- `GET`、`POST /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/storage/persistentvolumeclaims` 和对应
  单对象 `GET`、`PUT`、`DELETE`：固定在明确 Namespace 管理 PVC；三类存储资源列表均支持 Kubernetes
  continuation token 和 Selector，写操作沿用 DryRun、确认、幂等、UID/resourceVersion 与审计；
- `GET`、`POST /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/autoscaling/horizontalpodautoscalers` 和
  对应单对象 `GET`、`PUT`、`DELETE`：固定管理 `autoscaling/v2 HorizontalPodAutoscaler`；类型化创建和更新
  只接受同一 Namespace 中的 Deployment/StatefulSet，以及 Resource/ContainerResource 指标和伸缩行为；
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
- `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}/logs`：要求当前 Pod UID、
  明确容器和专用 `cluster.pod.logs.read` 权限，支持默认最近 200 行的有界快照以及 `follow=true` 实时流；
- `POST /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}/terminal-sessions`：要求当前
  Pod UID、明确容器、`cluster.pod.exec`、CSRF、幂等键和显式确认，创建与用户及登录 Session 绑定的一次性票据；
- `GET /api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}/terminal-sessions/{session_id}`：
  通过同源 `zke.pod-exec.v1` WebSocket 传输 stdin、stdout、stderr、resize 和 exit 帧；
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
- 只读接口要求 Session 和目标 Cluster 的 `cluster.read` 权限，每个请求通过独立 QUIC Resource Stream
  交给该 Cluster 的 Agent；
- 写接口另外要求 CSRF、`cluster.resource.create`、`cluster.resource.update` 或
  `cluster.resource.delete`、16 至 128 字符幂等键，以及实际变更的显式确认；
- Agent 使用 Kubernetes dynamic client，只接受 Discovery 声明且作用域、Verb 匹配的主资源 CRUD；
  Discovery 策略缓存有 TTL 和条目上限，Secret 和 Subresource 明确拒绝；Kubernetes 授权资源只能使用专用
  `/authorization` API，不能借通用 Resource/YAML API 绕过专用权限；
- 支持 DryRun、JSON Patch、JSON Merge Patch、Strategic Merge Patch、Server-Side Apply、
  删除传播策略和 UID/resourceVersion 前置条件；Apply 默认 `force=false`；
- YAML 更新仅接受不超过 4 MiB 的 `application/yaml` 单文档，不接受 Alias、Anchor、重复字段或
  YAML-only 类型；Server 在更新前核对 URL/GVR/Namespace 与 `apiVersion`、`kind`、名称、UID 和
  `resourceVersion`，DryRun 无需确认，实际写入要求 `confirm=true`。成功响应仍为 `application/yaml`，
  错误使用统一 JSON 信封；审计不记录 YAML 正文；
- Agent 使用跨 QUIC 重连存活的有界重放缓存抑制同一幂等键重复执行，同键不同请求返回冲突；
- 安装 Manifest 为 Agent ServiceAccount 增加 Node 的 `get`、`list`、`update`、`patch`，Namespace 的
  `get`、`list`、`create`、`update`、`delete`，Pod 的 `get`、`list`、`update`、`delete`，以及 Deployment、StatefulSet、
  DaemonSet、Job、CronJob、Service、Ingress 和 Gateway 的完整主资源 CRUD 权限，以及 ConfigMap、PV、PVC、
  StorageClass、HorizontalPodAutoscaler、ResourceQuota、LimitRange、NetworkPolicy、PodDisruptionBudget、
  PriorityClass、ServiceAccount 及四类 Kubernetes RBAC 资源的
  `get`、`list`、`create`、`update`、`delete`，`apiextensions.k8s.io/v1 customresourcedefinitions` 的只读
  `get`、`list`（仅用于判定哪些资源来自 CRD，不含定义或修改 CRD 的能力），并单独授予 `pods/log` 的 `get`
  和 `pods/exec` 的 `create`；Eviction Subresource 仍未授权，
  其他资源也需安装方显式增加最小 RBAC。

Node 列表当前通过 Resource Stream 传输完整 Kubernetes 对象，再由 Server 转换成稳定的精简响应；Table
表示尚未实现。

集群概览后端复用现有 Resource Stream，并发但有上限地读取 Node、Namespace、Pod 以及 Deployment、
StatefulSet、DaemonSet、Job、CronJob；Server 不直接访问 Kubernetes API。各部分分别分页，每部分最多读取
10000 个对象，因此概览是最终一致的聚合快照，不是同一个 Kubernetes `resourceVersion` 下的原子视图。
部分查询失败或达到上限时，接口仍返回成功响应，并通过 `partial` 和不含敏感正文的 `issues` 标明受影响部分；
只有所有部分都失败时才返回整体错误。CPU 以 millicores、内存以 bytes 返回，Pod requests 按 Kubernetes
调度语义统计非终态 Pod，包含 init container、restartable init container、Pod-level resources 和 overhead，
不表示实时利用率。接口使用 `cluster.read`；Warning Event 仍通过现有 Event API 和独立的
`cluster.event.read` 权限读取，避免概览扩大 Event 权限。

Console 概览是容器服务的默认落地页，也是左侧导航第一项：操作者进入应用时通常还不知道该打开哪个资源类别。
页面展示节点、命名空间、Pod 和工作负载的计数与状态分布、五类工作负载的分类计数，以及 CPU、内存和 Pod 三项
的请求量对可分配量。这些都是计数和容量，没有趋势也没有多序列比较，因此用数字和量条呈现而不是图表；量条只在
请求量接近或超过可分配量时改用警告和危险色，且数值始终以文字同时给出，不靠颜色单独表意。工具栏显示
`generated_at` 并提供刷新按钮，说明这是聚合快照。`partial` 为 true 时在顶部列出受影响的部分及原因，避免把
偏低的计数当成真实值。概览不展示 Warning Event：Event 接口按 Namespace 定域，且需要独立的
`cluster.event.read`，跨命名空间聚合不在本接口范围内。

Console 容器服务按资源类别组织：进入应用后先选择一个目标集群，左侧导航当前包含「概览」「节点」「命名空间」
「工作负载」「Pod」「服务与路由」「配置管理」「存储」「自动伸缩」「策略管理」「资源对象浏览器」「授权管理」
和「事件」十三项，默认落在「概览」；列表行可下钻到详情页再返回，分页使用
Kubernetes continuation token，并与 offset 分页一样固定渲染在表格下方。目标集群按项目
持久化在浏览器本地，只保存集群标识，且每次都会重新对照该项目当前在线的集群解析——已下线的集群不会被选中。
离线集群仍出现在选择器中但不可选，避免操作者以为集群不存在。命名空间提供 List/Detail/Create/Delete 与配额管理；
节点提供 List/Detail 以及停止调度和恢复调度。所有变更都经过权限门控、DryRun 预检、影响展示与二次确认。

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

页头出现时工具栏整行隐藏，两者位置、内边距和高度一致，因此进入对象不会让页面在光标下抖动，同一时刻也只有一行
框架。工具栏属于列表——选择器决定列表显示什么，插槽放的是列表的操作——而此时列表并不在场；在打开的对象之上切换
命名空间，只会让页面转而向另一个集群索取同名对象。选择器让出的作用域改为以文字形式出现在对象名之后（`集群 ·
命名空间`）：同名对象在每个集群里都可能存在，一个不写明集群的详情页是读不得的。

每个列表分区在工具栏中都有刷新按钮，请求进行中时按钮禁用并旋转。Kubernetes 对象会在 Console 不知情的情况下
变化，而列表读取一次后按缓存有效期保留，此前唯一的重新读取方式是离开分区再回来——那依赖的是缓存过期，与操作者
想做的事无关。详情页不显示该按钮：它属于列表，而列表此时并未展示。

节点的调度开关是对 `spec.unschedulable` 的 merge patch，走既有的受控通用 CRUD 路由，不需要专用接口，要求
`cluster.resource.update` 权限。它只阻止新 Pod 被调度到该节点，不驱逐已运行的 Pod；驱逐（drain）需要
`pods/eviction` Subresource，当前协议明确拒绝所有 Subresource，因此尚未支持。

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

节点和命名空间是集群级资源，工作负载和 Pod 是 namespaced 资源，因此工具栏只在进入「工作负载」或「Pod」
时显示 Namespace 作用域选择器。选择器按集群持久化在浏览器本地，同样只保存名称并每次重新对照集群当前返回的 Namespace 解析；
名称不再存在时回退到 `default`，`default` 也不存在时回退到第一个。选择器一次读取该集群的一页 Namespace
（上限 500 个），超出时在状态栏说明列表已截断，完整分页仍在「命名空间」页面。

工作负载后端返回统一元数据、镜像、状态和控制器副本信息，并按具体类型返回 Job 或 CronJob 状态；详情还包含
Selector、容器、条件、更新策略等稳定字段。常用变更已提供类型化接口，底层继续复用通用 Patch/Delete
执行链路与 `cluster.resource.*` 权限、CSRF、DryRun、显式确认、幂等和审计边界。滚动重启将
`Idempotency-Key` 的 SHA-256 摘要写入 Pod Template 的 `zke.io/restart-request` 注解，使相同请求重试产生
完全相同的补丁；删除必须携带当前对象 UID，避免误删同名重建对象。

类型化创建支持名称、描述、标签、注解、完整的 Pod 模板，并按类型支持副本数、StatefulSet Service、Job 执行
参数与 CronJob 调度参数。Server 使用资源类型和名称生成客户端不能覆盖的 `zke.io/workload-id` 标签；Deployment、
StatefulSet 和 DaemonSet 使用该标签作为 Pod Selector，Job 的 Selector 则由 Kubernetes 按控制器 UID 生成，
避免不同控制器意外选中同一批 Pod。StatefulSet 的 `service_name` 必须引用同一 Namespace 中预先存在的
Service；更新仍可使用通用资源接口。

Pod 模板部分对五类工作负载共用——一个 Deployment 和一个 CronJob 的差别在于 Pod 怎样被产生，而不在于 Pod 是
什么。容器层面接受镜像与拉取策略、运行命令与参数、工作目录、环境变量、资源 requests/limits、数据卷挂载、
存活与就绪探针、生命周期钩子和特权开关；Pod 层面接受数据卷、镜像访问凭证、节点标签选择和容忍调度。描述写入
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

亲和性、拓扑分布约束、`securityContext` 的其余字段和容器端口不在类型化范围内：它们没有可在表单中稳定表达的
有界形状，创建后通过 YAML 管理。

服务与路由后端固定使用 `core/v1 Service`、`networking.k8s.io/v1 Ingress` 和
`gateway.networking.k8s.io/v1 Gateway`，不会接受调用方覆盖 GVR。Service 支持 ClusterIP、NodePort、
LoadBalancer、ExternalName 与 headless 语义，更新时保留 Kubernetes 分配的 ClusterIP、IP family 和适用的
health check NodePort，并拒绝通过类型化接口切换不可变的 headless 身份。Ingress 支持 class、默认后端、
Host/Path、Service backend 和 TLS Secret 名称；接口只返回 Secret 引用名称，不读取 Secret 正文。

Gateway API 是目标集群的可选 CRD 能力。每次 Gateway 操作先通过 Discovery 确认
`gateway.networking.k8s.io/v1 Gateway` 存在；未安装时返回稳定的 `409 gateway_api_unavailable`，与已安装但
Agent ServiceAccount 无权访问时的 `403` 区分。ZKE 不负责安装 Gateway API CRD、GatewayClass 或具体
Controller，也不把 Gateway 的 `Programmed=True` 等同于外部流量已经可达。本轮只提供 Gateway 本身，
HTTPRoute、GRPCRoute、TLSRoute、TCPRoute 和 UDPRoute 等 Route 类型暂未纳入类型化接口，仍可在安装方授予
最小 RBAC 后通过通用资源接口管理。Gateway 的 TLS 同样只传递证书引用，不读取证书 Secret。

Console 服务与路由页面按 Service、Ingress、Gateway 三个标签页组织。三者形状不同，因此列表列和详情卡片
各自独立，而不是压成一张只显示共有字段的表：Service 展示类型、ClusterIP 与端口映射，Ingress 展示
IngressClass、主机与已分配地址，Gateway 展示 GatewayClass、监听器与地址。详情页在类型化视图之外还提供
YAML 入口，用于查看和修改本表单未建模的字段。

创建、更新和删除都先执行服务端 DryRun 再确认。更新和删除携带对象当前的 UID 与 resourceVersion，因此陈旧
编辑会被拒绝而不是覆盖；确认弹窗说明本表单建模的配置会整体替换现有配置，而 Kubernetes 分配的字段和未建模
的扩展字段由服务端保留。Service 的类型切换会连带显示对应字段（ExternalName 只要目标域名，NodePort 与
LoadBalancer 才有外部流量策略和 NodePort 输入）。

目标集群没有安装 Gateway API 时，列表返回 `409 gateway_api_unavailable`，Console 据此展示说明而不是错误，
并隐藏创建入口；这与已安装但 Agent 无权访问的 `403` 是两回事，后者仍按权限错误呈现。

ConfigMap 类型化后端固定使用 `core/v1 ConfigMap`，不会接受调用方覆盖 GVR。列表不返回配置值，避免列表和
搜索场景批量搬运正文；详情才返回 `data` 和以标准带填充 Base64 表示的 `binary_data`。创建和更新校验键名、
两类键不重叠及解码后总大小不超过 1 MiB。更新是两张数据表的完整替换，要求显式提交空表，并使用当前 UID 与
resourceVersion 防止覆盖同名重建或并发修改；一旦设置 immutable，内容变更或恢复为 false 会被拒绝。
该接口复用通用 Resource Stream、集群权限、CSRF、DryRun、确认、幂等与审计链路，但审计不记录配置正文。
Secret 仍被通用 Server 与 Agent 双重拒绝，也没有借用 ConfigMap 路由或 Agent ClusterRole；它需要独立权限、
响应脱敏和更严格的审计设计后才能开放。

Console 配置管理页面列出所选命名空间的 ConfigMap，展示键名、总大小和 immutable 标记——列表接口不返回配置
正文，页面也不去逐个补齐，否则等于把整个命名空间的配置搬进浏览器。内容只在详情页按对象读取：文本值按原样以
等宽预格式展示且不折行（配置文件的缩进和换行本身就是内容），二进制值只显示大小并说明是 Base64，不做任何
渲染——那些字节没有可靠的文本解释，需要原文时走 YAML 视图。

创建和更新都先执行服务端 DryRun 再确认。更新是整体替换，确认弹窗明说「本次未提交的键将从对象中移除」，
并提示 Volume 挂载与环境变量注入的生效时机不同。UID 与 resourceVersion 在打开编辑器时固定，不随后台重新
拉取而更新：取一个更新的版本号会把本该被服务端拒绝的冲突变成静默覆盖。immutable 只在创建时可设，
已标记为不可变的对象在列表和详情中都不提供编辑入口，并说明只能删除重建。

Secret 不在此处管理的原因（Agent 未被授予 Secret 权限）当前只记录在本文档中，页面上没有对应说明。

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

创建表单按类型渲染：PV 需要容量、访问模式和来源（CSI/NFS/Local 三选一，字段随之切换），并明确说明 ZKE 不会
创建底层存储、填写的卷必须已经存在；PVC 需要申请容量和访问模式，并把「使用集群默认 StorageClass」与「显式
指定（留空表示不使用任何 StorageClass）」区分开，因为这两者在 Kubernetes 中语义不同；StorageClass 需要
provisioner 和参数。访问模式用复选框而不是下拉，因为它本来就是集合而不是单选。

编辑不是通用表单，而是每种类型各自唯一可变字段的小弹窗：PV 只改回收策略，PVC 只改申请容量（且必须增大），
StorageClass 只改扩容开关，并说明类型化接口之外的高级字段需通过 YAML 管理并接受 API Server 校验。确认弹窗按具体
选择给出后果，例如把 PV 回收策略改为 Delete 会在删除时销毁数据，PVC 扩容需要 CSI 驱动配合且部分驱动要求
Pod 重启后文件系统才扩展。删除的影响文案同样按类型区分，包括 PV 回收策略决定数据存亡、仍被占用的对象会
停在 Terminating。

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
显式确认、幂等和审计。VPA 与 KEDA 依赖额外组件或 CRD，本轮未作为内置能力实现。

Console 自动伸缩页面列出所选命名空间的 HPA，展示目标工作负载、当前副本与期望副本、副本区间、指标数量、
状态和最近一次伸缩时间。状态只显示需要注意的情况——无法伸缩、指标不可用、已触达上下限、尚未同步——健康的
HPA 只显示一个「正常」，否则每行三个绿标会把真正异常的那一个淹掉。

创建和编辑表单覆盖后端首轮支持的范围：目标（Deployment 或 StatefulSet）、副本区间、Resource 与
ContainerResource 指标（Utilization 百分比或 AverageValue），以及可选的扩容/缩容行为（稳定窗口、
Max/Min/Disabled 选择策略、Pods/Percent 策略）。更新替换整份 spec，确认弹窗明说未提交的指标或行为策略会被
移除；创建的确认弹窗则点明副本数将由控制器接管、手动伸缩会在下一个控制周期被覆盖，以及指标不可用时 HPA
不会伸缩而是在 Condition 中报告原因。已有 HPA 若使用了 Object、Pods、External 等本表单未建模的指标，列表和
详情仍可读取，编辑请走 YAML。

HPA 摘要同时返回 `generation` 与 `observed_generation`，Console 据此区分控制器尚未处理最新 spec 的状态。

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
只读查询，读不到时界面明说无法判定并提示需要补充的最小 RBAC，而不是给出一个看起来「没有 CRD」的空列表。

浏览器只提供读取、YAML 编辑和删除三件事，且都复用既有链路：读取要求 `cluster.read`，YAML 编辑走
`cluster.resource.update`，删除走 `cluster.resource.delete` 并携带该对象当前的 UID 与 resourceVersion 前置
条件、Background 传播策略、DryRun 与显式确认。它不为未知类型编造类型化表单——对一个 ZKE 不认识其语义的 CR，
YAML 是唯一诚实的编辑方式。命名空间选择器提供「所有命名空间」，对应通用接口省略 Namespace 参数的跨命名空间
查询；名称筛选只作用于已加载的当前页并在界面上说明，因为 Kubernetes Field Selector 只能精确匹配名称，把它
当成模糊搜索会静默变成另一种语义。Secret、Event 和五类 Kubernetes 授权资源不出现在这里：它们分别被通用接口
拒绝或只允许走专用链路。

Kubernetes 授权管理后端把命名空间级 ServiceAccount、Role、RoleBinding 与集群级 ClusterRole、
ClusterRoleBinding 分开定域。Role/ClusterRole 类型化写入完整规则，RoleBinding/ClusterRoleBinding 更新只替换
Subjects 并保留不可变的 RoleRef；聚合 ClusterRole 只读展示，避免类型化更新清除 AggregationRule。
ServiceAccount 只返回关联 Secret 数量，不返回 Secret 名称、Token 或正文。

该能力使用独立的 `cluster.rbac.read/manage`，并从通用 Resource/YAML 入口排除上述五类资源。ZKE 安装清单不会
给 Agent `escalate`、`bind` 或 `impersonate`；类型化规则也拒绝这些 Verb、Secret 和 ServiceAccount Token，
绑定不能直接引用内置 `zke-agent` 角色，API Server 还会拒绝其他超出 Agent 当前权限上限的规则或绑定。
带 `app.kubernetes.io/managed-by=zke-server` 的 Agent ServiceAccount、Role、ClusterRole 和绑定禁止通过
该 API 更新或删除，避免平台自行切断连接与执行权限。

Console 授权管理页面按 ServiceAccount、Role、ClusterRole、RoleBinding、ClusterRoleBinding 五个标签页组织，
作用域与后端一致：前三类中的 ServiceAccount 和 Role 以及 RoleBinding 按命名空间定域，ClusterRole 与
ClusterRoleBinding 是集群级对象，工具栏的命名空间选择器只在前者出现。导航项按 `cluster.rbac.read` 隐藏，
写操作按 `cluster.rbac.manage` 门控；两者都与 `cluster.resource.*` 相互独立。

不可操作的对象不给假入口：ZKE 管理的 Agent 授权对象在列表和详情中都不提供编辑与删除，并用 tooltip 和提示条
说明原因；聚合 ClusterRole 不提供类型化编辑，因为它的规则由控制器合成。绑定的编辑表单只替换 Subjects，
RoleRef 显示为只读并说明「要指向其他角色需要删除后重建」——Kubernetes 本就不允许改，而静默地重新指向一个
已有授权是最不容易被察觉的变更。

ServiceAccount 只显示关联 Secret 的数量，并在列表和详情中都写明「不展示名称」，与后端不返回 Secret 名称、
Token 或正文一致。规则编辑器的字段用逗号分隔，API 组留空即 core 组——空字符串在 Kubernetes 里是有意义的取值，
提交时会保留而不是当成空项丢弃。表单旁注说明 Agent 不持有 `escalate`/`bind`，超出其权限范围的规则会被
Kubernetes 拒绝。

本页不提供 YAML 入口：这五类资源被通用 Resource 与 YAML 接口整体排除——读取也一样被拒绝，而不是只拒绝
写入——所以一个 YAML 按钮在这里永远打不开。它们只能通过本页的类型化表单管理。

Pod 类型化后端返回统一元数据、Phase、Ready、Node、Pod IP、控制器 Owner、镜像和总重启次数；详情补充
Annotations、完整 Owner References、调度与网络信息、主容器、初始化容器和 Ephemeral Container 的当前/上次
状态、资源 requests/limits、重启次数以及 Pod Conditions。删除 Pod 时必须携带当前 UID，避免误删同名重建对象；
由 Deployment、StatefulSet、DaemonSet、Job 等控制器管理的 Pod 删除后通常会被控制器重新创建。Pod 删除不是
Eviction，不执行 PodDisruptionBudget 语义；Logs 通过独立协议和最小 `pods/log` 权限实现，不会放宽通用
Subresource 边界；Exec 只通过独立 Pod Exec 协议开放，Eviction 仍留待后续设计。

Pod 日志后端在读取前和打开 Kubernetes 日志流后分别核对 Pod UID，避免同名 Pod 重建竞态；支持主容器、
初始化容器和临时容器，`tail_lines` 最大 5000、`since_seconds` 最大 7 天。`previous` 不接受与 `follow`
同时使用——已终止的实例不会再产生新日志——因此一次被拒绝的上一个实例读取只剩一种含义。Agent 在请求
Kubernetes 之前先按容器状态判断该容器是否运行过：没有重启过就没有上一个实例，直接返回
`404 previous_logs_not_found`；已经重启过但 Kubernetes 仍拒绝时（上一个实例的日志已被节点清理）同样按该
错误返回。Kubernetes 对这两种情况都回 400，照直传给操作者就成了「请求内容无效，请检查输入」，而请求本身
是良构的，缺的是被请求的对象。快照和 Follow 都使用独立 QUIC
Stream 逐块转发，默认每条最多 16 MiB，Follow 最长 30 分钟。Follow 会周期重新验证 Session 和权限；客户端
断开、权限撤销、超时或 Agent 连接排空都会取消目标 Kubernetes 请求。HTTP 正文为未包装的 `text/plain`，
终止状态和字节统计通过 Trailer 返回，日志正文不会进入 Server 日志或审计事件。

Web Terminal 后端使用两步会话：先创建短期、一次性且与用户、登录 Session、Cluster、Pod UID、容器和路径绑定的
票据，再以同源 WebSocket 消费。Agent 在 Kubernetes Exec 前重新读取 Pod 并核对 UID 和容器；到 Kubernetes
API Server 优先使用 WebSocket streaming protocol，仅在旧 API Server 或 HTTPS 代理无法完成 WebSocket Upgrade
时回退 SPDY。
启动命令固定为优先 `bash`、不存在时回退 `/bin/sh`，不接受客户端提供任意命令。会话具有最大时长、空闲超时、输入/输出字节
上限和 Server/单 Agent 并发上限，并周期重新验证 Session 与 `cluster.pod.exec`。审计只记录票据创建、目标和
会话结果，不记录或摘要终端输入输出。该顺序与 Kubernetes 1.31 开始默认使用 WebSocket 的
[Streaming Transitions](https://kubernetes.io/zh-cn/blog/2024/08/20/websockets-transition/) 一致；SPDY 只作为
未定义最低支持版本前的临时兼容路径。

Console 终端入口在 Pod 列表行和详情页，需要 `cluster.pod.exec`（默认只授予 admin）。终端占据整个应用视图，
打开前弹出确认，说明将在目标容器中启动交互式 Shell、命令以容器自身身份执行、以及审计不记录终端输入输出。
确认后才创建票据并立即用它建立 WebSocket；票据不落任何前端存储，重新连接就是重新申请一张。视图打开时会重新
读取 Pod，若 UID 与入口绑定的不一致则拒绝连接并要求返回列表重开。终端实例在整个视图生命周期内只创建一次，
会话结束后保留回滚缓冲；离开视图或点击断开都会关闭 WebSocket，从而结束容器中的 Shell。终端尺寸由前端测量后
写入票据，并在连接建立时和每次窗口变化时下发 `resize`。

终端使用 [xterm.js](https://xtermjs.org/)（MIT，`@xterm/xterm` 与 `@xterm/addon-fit`）作为终端仿真器：交互式
Shell 需要完整的 ANSI/VT 序列、光标寻址与回滚支持，自行实现不现实。它按需加载为独立 chunk，不进入主包，
未打开终端的操作者不会为此付出加载成本。

Kubernetes Event 后端固定读取所选 Cluster 与 Namespace 的 `core/v1/events`，不接受调用方覆盖 GVR。默认
通过 SSE 返回有界初始快照；实时 Follow 使用独立 `resource-watch.v1` QUIC Stream，支持按关联资源 UID、Kind、
Name、Event type 和 reason 过滤，并通过 resourceVersion/`Last-Event-ID` 恢复。Session 或
`cluster.event.read` 被撤销时立即取消，`410 Expired` 以 `resource_version_expired` 的正文内 close 原因提示
客户端重新拉取快照。Event 正文不写入日志或审计。

Console YAML 入口在节点、命名空间、工作负载和 Pod 的详情页，占据整个应用视图。文档按 Kubernetes 原样展示，
包含 `metadata.uid` 和 `metadata.resourceVersion`——Server 正是拿它们做写入前置条件，把它们「整理掉」等于
去掉了防止基于陈旧读取覆盖他人改动的保护。编辑器不折行：YAML 靠缩进表达层级，软折行会读成一层并不存在的
嵌套。语法高亮由仓库内的轻量 Tokenizer 提供，着色层画在一个透明 textarea 之下，因此原生选区、撤销、输入法
组合和读屏行为都保持浏览器自身的实现，高亮出错最多丢失颜色，不会改动任何一个字符；着色只区分键、引号字符串、
数字与布尔等字面量、注释、标点和 Anchor/Tag，未加引号的普通标量保持正文色，超过 200,000 字符的文档直接放弃
着色而不是让编辑变卡。保存走两步，先服务端 DryRun，再在确认弹窗中要求输入对象名称，并说明整份文档会替换现有对象、文档中
未出现的字段会被移除。保存成功后重新读取一次，使编辑器持有新的 resourceVersion 而不是刚刚被用掉的那个。
没有 `cluster.resource.update` 权限时页面只读并说明原因；文档超过 4 MiB 时在提交前就拒绝，不做无谓往返。

Console 事件页面按所选 Cluster 和 Namespace 展示 Event，可按 Event type、关联资源 Kind/名称和 reason 筛选，
筛选条件由服务端执行，输入框做防抖以免每次击键都开一条新 Watch。默认开启实时跟随。事件按 UID 归并：
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

Server 通过 HTTP Trailer 返回终止状态（`succeeded`、`timeout`、`canceled`、`access_revoked`、`failed`）和
字节统计，但主流浏览器的 `fetch` 均不暴露 Trailer，因此 Console 目前无法区分「正常结束」与「服务端因字节
上限、时长上限或权限撤销提前结束」，两者都显示为「已结束」。如果需要在界面上区分，需要另行设计在正文内或
响应头中传达该状态的方式。

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

通用接口返回 Unstructured JSON，并移除 `metadata.managedFields`。Discovery
目录表示 API Server 暴露的资源，不代表 Agent ServiceAccount 已获授权；管理更多内置资源或任意 CR 时，安装方
需要显式扩展该 ServiceAccount 的最小 RBAC，ZKE 无需增加新的资源协议或 HTTP Handler。

仓库包含默认跳过的本地真实集群 E2E。设置 `ZKE_LIVE_KUBERNETES_E2E=1` 后，它使用当前 kubeconfig，通过
真实 QUIC Stream 验证 Namespace、ConfigMap、Deployment、CRD 和自定义资源的 CRUD、四类 Patch、DryRun、
冲突与幂等重放，并使用随机名称和精确清理避免污染日常集群。

后续规划能力包括：

- 节点驱逐，以及它依赖的 Subresource allowlist 设计；
- 工作负载的类型化更新表单，以及创建表单尚未覆盖的亲和性、拓扑分布约束与容器端口；
- 创建工作负载时联动创建 Service 与 HorizontalPodAutoscaler（需要先定义多对象写入的部分成功与回滚语义）；
- 终端会话的录制与回放；
- 在 Console 中区分日志流的终止原因（依赖 Trailer 之外的传达方式）；
- Gateway API 的 HTTPRoute、GRPCRoute、TLSRoute、TCPRoute 和 UDPRoute 类型化管理；
- Secret 的专用敏感管理链路；
- 从 Pod、工作负载等具体对象直接跳转到按 UID 过滤的关联事件；
- YAML 编辑器的结构校验与差异对比；
- 面向具体资源的表单化创建、更新和删除体验。

产品体验将参考成熟 Kubernetes 管理平台的通用实践，但不会以与任何现有平台完全相同为目标。
