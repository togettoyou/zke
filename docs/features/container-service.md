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
「工作负载」「Pod」「服务与路由」「配置管理」「存储」「自动伸缩」「策略管理」「授权管理」「资源对象浏览器」
和「事件」十三项，默认落在「概览」。「资源对象浏览器」排在全部类型化类别之后、「事件」之前：它是上面这些类别
没有建模的类型的兜底入口，读起来该是这份资源清单的收尾而不是其中一项；「事件」仍在最后，它根本不是一个资源
类别，而是关于上面那些资源的一条流。列表行可下钻到详情页再返回，分页使用
Kubernetes continuation token，并与 offset 分页一样固定渲染在表格下方。目标集群按项目
持久化在浏览器本地，只保存集群标识，且每次都会重新对照该项目当前在线的集群解析——已下线的集群不会被选中。
离线集群仍出现在选择器中但不可选，避免操作者以为集群不存在。命名空间提供 List/Detail/Create/Delete 与配额管理，
其中创建与删除要求 `cluster.namespace.manage`，配额管理与 YAML 编辑仍是 `cluster.resource.*`；
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

目标集群没有安装 Gateway API 时，列表返回 `409 gateway_api_unavailable`，Console 据此展示说明而不是错误，
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
显式确认、幂等和审计。VPA 与 KEDA 依赖额外组件或 CRD，本轮未作为内置能力实现。

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
写入票据，并在连接建立时和每次窗口变化时下发 `resize`。承载终端的元素本身不带内边距和边框，边框与留白由外层
容器提供：xterm 的 fit 插件按父元素的 `getComputedStyle().height` 计算行列数，而在 `box-sizing: border-box`
下那是包含父元素自身内边距和边框的边框盒，直接量一个带 `p-2` 和边框的面板会多算 18px——超过一行的高度，
最后一行会被画到可视区域之外裁掉，宽度上多出的部分也会让下发给容器 TTY 的列数偏大。

终端使用 [xterm.js](https://xtermjs.org/)（MIT，`@xterm/xterm` 与 `@xterm/addon-fit`）作为终端仿真器：交互式
Shell 需要完整的 ANSI/VT 序列、光标寻址与回滚支持，自行实现不现实。它按需加载为独立 chunk，不进入主包，
未打开终端的操作者不会为此付出加载成本。

Kubernetes Event 后端固定读取所选 Cluster 与 Namespace 的 `core/v1/events`，不接受调用方覆盖 GVR。默认
通过 SSE 返回有界初始快照；实时 Follow 使用独立 `resource-watch.v1` QUIC Stream，支持按关联资源 UID、Kind、
Name、Event type 和 reason 过滤，并通过 resourceVersion/`Last-Event-ID` 恢复。Session 或
`cluster.event.read` 被撤销时立即取消，`410 Expired` 以 `resource_version_expired` 的正文内 close 原因提示
客户端重新拉取快照。Event 正文不写入日志或审计。

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

工作负载编辑复用创建表单，而不是另做一张更小的表：一个只在创建时出现的字段，等于操作者可以设一次却永远
改不回来。差别在于哪些字段被固定，以及写入方式。

更新是合并而不是整体替换，这一点与 Service、策略等较小的类型化资源相反。那些对象是被整体读取的，因此也被
整体替换；而一个 Pod 模板还带着亲和性、拓扑分布约束、容器端口、ServiceAccount、主机网络和 `securityContext`
的其余字段——它们都不在本表单范围内，却都是某个人特意设过的。整份替换会让一次「只想改镜像标签」的
保存把它们一并删掉。因此 Server 先按 UID 与 resourceVersion 读回对象，把表单建模的字段写上去，其余文档原样
保留：容器按名称合并，保留该容器的端口、终止设置与 `securityContext` 其余字段；`fieldRef` 等本表单无法表达的
环境变量来源、projected/CSI 等无法表达的数据卷来源、gRPC 等无法表达的探针，都在提交回来时保持原状——它们读取
时就只返回一个名称，没有可显示的内容，写回时也不会被清空。滚动重启写在 Pod 模板上的 `zke.io/restart-request`
注解同样保留：删掉它本身就是一次模板变更，会再触发一轮滚动。

Kubernetes 自身的不可变约束如实反映，不是本表单的取舍：名称与 StatefulSet 的 `serviceName` 只读并说明原因；
Job 的 Pod 模板、选择器、完成数和失败重试上限创建后不可变，因此编辑一个 Job 时表单只显示并行度和完成后保留
秒数，其余分区不渲染，并说明要改变 Job 运行的内容只能新建一个 Job。Deployment、StatefulSet、DaemonSet 与
CronJob 的模板可变；Pod 模板的任何变化都会触发滚动更新，确认弹窗按类型说明这一点，CronJob 则说明改动对下一次
触发的 Job 生效。

回填要求详情接口返回完整的类型化模板，因此工作负载详情现在返回容器的命令、参数、工作目录、环境变量、资源
requests/limits、挂载、探针、生命周期钩子和特权开关，以及 Pod 层的数据卷、镜像访问凭证、节点标签选择和容忍，
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

通用接口返回 Unstructured JSON，并移除 `metadata.managedFields`。Discovery
目录表示 API Server 暴露的资源，不代表 Agent ServiceAccount 已获授权；管理更多内置资源或任意 CR 时，安装方
需要显式扩展该 ServiceAccount 的最小 RBAC，ZKE 无需增加新的资源协议或 HTTP Handler。

仓库包含默认跳过的本地真实集群 E2E。设置 `ZKE_LIVE_KUBERNETES_E2E=1` 后，它使用当前 kubeconfig，通过
真实 QUIC Stream 验证 Namespace、ConfigMap、Deployment、CRD 和自定义资源的 CRUD、四类 Patch、DryRun、
冲突与幂等重放，并使用随机名称和精确清理避免污染日常集群。

后续规划能力包括：

- 节点驱逐，以及它依赖的 Subresource allowlist 设计；
- 创建与编辑表单尚未覆盖的亲和性、拓扑分布约束与容器端口；
- 创建工作负载时联动创建 Service 与 HorizontalPodAutoscaler（需要先定义多对象写入的部分成功与回滚语义）；
- 终端会话的录制与回放；
- 在 Console 中区分日志流的终止原因（依赖 Trailer 之外的传达方式）；
- Gateway API 的 HTTPRoute、GRPCRoute、TLSRoute、TCPRoute 和 UDPRoute 类型化管理；
- 从 Pod、工作负载等具体对象直接跳转到按 UID 过滤的关联事件；
- YAML 编辑器的结构校验与差异对比；
- 面向具体资源的表单化创建、更新和删除体验。

产品体验将参考成熟 Kubernetes 管理平台的通用实践，但不会以与任何现有平台完全相同为目标。
