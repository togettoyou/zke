# 容器服务

容器服务是单集群应用。用户进入应用时需要先选择一个 Kubernetes 集群，进入后所有页面和操作均作用于当前集群。

当前已完成 Node、Pod 类型化接口、Namespace 管理闭环、五类工作负载类型化后端管理和通用主资源 CRUD 底座：

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
- `GET /api/v1/clusters/{cluster_id}/kubernetes/resource-types`：返回目标 Cluster 当前 Discovery 可见的
  内置资源和 CRD 资源目录；
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
  Discovery 策略缓存有 TTL 和条目上限，Secret 和 Subresource 明确拒绝；
- 支持 DryRun、JSON Patch、JSON Merge Patch、Strategic Merge Patch、Server-Side Apply、
  删除传播策略和 UID/resourceVersion 前置条件；Apply 默认 `force=false`；
- YAML 更新仅接受不超过 4 MiB 的 `application/yaml` 单文档，不接受 Alias、Anchor、重复字段或
  YAML-only 类型；Server 在更新前核对 URL/GVR/Namespace 与 `apiVersion`、`kind`、名称、UID 和
  `resourceVersion`，DryRun 无需确认，实际写入要求 `confirm=true`。成功响应仍为 `application/yaml`，
  错误使用统一 JSON 信封；审计不记录 YAML 正文；
- Agent 使用跨 QUIC 重连存活的有界重放缓存抑制同一幂等键重复执行，同键不同请求返回冲突；
- 安装 Manifest 为 Agent ServiceAccount 增加 Node 的 `get`、`list`、`update`、`patch`，Namespace 的
  `get`、`list`、`create`、`update`、`delete`，Pod 的 `get`、`list`、`update`、`delete`，以及 Deployment、StatefulSet、
  DaemonSet、Job 和 CronJob 的完整主资源 CRUD 权限，并单独授予 `pods/log` 的 `get` 和 `pods/exec` 的
  `create`；Eviction Subresource 仍未授权，
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
请求量接近或超过可分配量时改用警告和危险色，且数值始终以文字同时给出，不靠颜色单独表意。页面顶部显示
`generated_at` 并提供刷新按钮，说明这是聚合快照。`partial` 为 true 时在顶部列出受影响的部分及原因，避免把
偏低的计数当成真实值。概览不展示 Warning Event：Event 接口按 Namespace 定域，且需要独立的
`cluster.event.read`，跨命名空间聚合不在本接口范围内。

Console 容器服务按资源类别组织：进入应用后先选择一个目标集群，左侧导航当前包含「概览」「节点」
「命名空间」「工作负载」「Pod」和「事件」六项，默认落在「概览」；列表行可下钻到详情页再返回，分页使用
Kubernetes continuation token。目标集群按项目
持久化在浏览器本地，只保存集群标识，且每次都会重新对照该项目当前在线的集群解析——已下线的集群不会被选中。
离线集群仍出现在选择器中但不可选，避免操作者以为集群不存在。命名空间提供 List/Detail/Create/Delete；节点提供
List/Detail 以及停止调度和恢复调度。所有变更都经过权限门控、DryRun 预检、影响展示与二次确认。

节点的调度开关是对 `spec.unschedulable` 的 merge patch，走既有的受控通用 CRUD 路由，不需要专用接口，要求
`cluster.resource.update` 权限。它只阻止新 Pod 被调度到该节点，不驱逐已运行的 Pod；驱逐（drain）需要
`pods/eviction` Subresource，当前协议明确拒绝所有 Subresource，因此尚未支持。

节点和命名空间是集群级资源，工作负载和 Pod 是 namespaced 资源，因此工具栏只在进入「工作负载」或「Pod」
时显示 Namespace 作用域选择器。选择器按集群持久化在浏览器本地，同样只保存名称并每次重新对照集群当前返回的 Namespace 解析；
名称不再存在时回退到 `default`，`default` 也不存在时回退到第一个。选择器一次读取该集群的一页 Namespace
（上限 500 个），超出时在状态栏说明列表已截断，完整分页仍在「命名空间」页面。

工作负载后端返回统一元数据、镜像、状态和控制器副本信息，并按具体类型返回 Job 或 CronJob 状态；详情还包含
Selector、容器、条件、更新策略等稳定字段。常用变更已提供类型化接口，底层继续复用通用 Patch/Delete
执行链路与 `cluster.resource.*` 权限、CSRF、DryRun、显式确认、幂等和审计边界。滚动重启将
`Idempotency-Key` 的 SHA-256 摘要写入 Pod Template 的 `zke.io/restart-request` 注解，使相同请求重试产生
完全相同的补丁；删除必须携带当前对象 UID，避免误删同名重建对象。

类型化创建支持名称、标签、主容器和初始化容器，并按类型支持副本数、StatefulSet Service、Job 执行参数与
CronJob 调度参数。Server 使用资源类型和名称生成客户端不能覆盖的 `zke.io/workload-id` 标签；Deployment、
StatefulSet 和 DaemonSet 使用该标签作为 Pod Selector，Job 的 Selector 则由 Kubernetes 按控制器 UID 生成，
避免不同控制器意外选中同一批 Pod。StatefulSet 的 `service_name` 必须引用同一 Namespace 中预先存在的
Service；高级 Pod 配置和其他更新仍可使用通用资源接口。

Pod 类型化后端返回统一元数据、Phase、Ready、Node、Pod IP、控制器 Owner、镜像和总重启次数；详情补充
Annotations、完整 Owner References、调度与网络信息、主容器、初始化容器和 Ephemeral Container 的当前/上次
状态、资源 requests/limits、重启次数以及 Pod Conditions。删除 Pod 时必须携带当前 UID，避免误删同名重建对象；
由 Deployment、StatefulSet、DaemonSet、Job 等控制器管理的 Pod 删除后通常会被控制器重新创建。Pod 删除不是
Eviction，不执行 PodDisruptionBudget 语义；Logs 通过独立协议和最小 `pods/log` 权限实现，不会放宽通用
Subresource 边界；Exec 只通过独立 Pod Exec 协议开放，Eviction 仍留待后续设计。

Pod 日志后端在读取前和打开 Kubernetes 日志流后分别核对 Pod UID，避免同名 Pod 重建竞态；支持主容器、
初始化容器和临时容器，`tail_lines` 最大 5000、`since_seconds` 最大 7 天。快照和 Follow 都使用独立 QUIC
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
嵌套。保存走两步，先服务端 DryRun，再在确认弹窗中要求输入对象名称，并说明整份文档会替换现有对象、文档中
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
Kubernetes 请求。浏览器端缓冲上限约 2 000 000 字符，超出后丢弃较早的行并在状态栏说明；自动滚动只在视图已
位于底部时生效，操作者向上翻阅时不会被拉回。

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

创建表单需要 `cluster.resource.create`，并直接创建当前标签页所选的类型，不再提供第二个类型选择器。表单
按类型只渲染该类型接受的字段：Deployment 和 StatefulSet 有副本数，StatefulSet 另有 Service 名称，Job 和
CronJob 有并行度、完成数、失败重试上限与完成后保留秒数，CronJob 另有 Cron 表达式、时区、并发策略、启动
截止秒数、历史保留数量和「创建后暂停」。留空的可选字段不会出现在请求里——Server 会拒绝而不是忽略不适用的
字段，因此空值必须是缺席而不是空串。名称、容器名、镜像、Service 名称和标签在提交前先做一次与 Server 同形的
校验，`zke.io/workload-id` 作为保留键在表单中被拒绝；最终判定仍由 Server 执行。创建同样走 DryRun 预检和
确认弹窗。

通用接口返回 Unstructured JSON，并移除 `metadata.managedFields`。Discovery
目录表示 API Server 暴露的资源，不代表 Agent ServiceAccount 已获授权；管理更多内置资源或任意 CR 时，安装方
需要显式扩展该 ServiceAccount 的最小 RBAC，ZKE 无需增加新的资源协议或 HTTP Handler。

仓库包含默认跳过的本地真实集群 E2E。设置 `ZKE_LIVE_KUBERNETES_E2E=1` 后，它使用当前 kubeconfig，通过
真实 QUIC Stream 验证 Namespace、ConfigMap、Deployment、CRD 和自定义资源的 CRUD、四类 Patch、DryRun、
冲突与幂等重放，并使用随机名称和精确清理避免污染日常集群。

后续规划能力包括：

- 节点驱逐，以及它依赖的 Subresource allowlist 设计；
- 工作负载的高级 Pod 配置（环境变量、资源限制、存储卷、探针）与类型化更新表单；
- 终端会话的录制与回放；
- 在 Console 中区分日志流的终止原因（依赖 Trailer 之外的传达方式）；
- Service 与 Ingress；
- ConfigMap 与 Secret；
- PersistentVolume、PersistentVolumeClaim 与 StorageClass；
- 从 Pod、工作负载等具体对象直接跳转到按 UID 过滤的关联事件；
- YAML 编辑器的语法高亮、结构校验与差异对比；
- 面向具体资源的表单化创建、更新和删除体验。

产品体验将参考成熟 Kubernetes 管理平台的通用实践，但不会以与任何现有平台完全相同为目标。
