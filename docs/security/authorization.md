# 安全与权限

ZKE 的安全模型仍在设计中，当前遵循以下原则：

- **最小权限**：用户、服务与 Agent 只获得完成任务所需的权限。
- **作用域明确**：所有查询和操作均关联租户、项目、集群及必要的 Namespace。
- **服务端校验**：不依赖前端隐藏操作，权限必须在服务端执行校验。
- **目标确认**：敏感操作必须展示目标集群、资源对象、操作内容和潜在影响。
- **人工确认**：AI 建议或生成的变更不能绕过用户确认。
- **Agent 定域执行**：集群操作由目标集群中的 Agent 执行。
- **全程审计**：记录操作发起者、目标、请求、结果和必要的分析依据。
- **凭证保护**：Kubernetes 凭证、API Key 和 Secret 不应以明文出现在日志、界面或 AI 上下文中。

## 权限边界

不同用户只能查看和操作其权限范围内的资源。所有跨集群查询均需遵守租户、项目和 RBAC 权限边界，全局视图不代表全局操作权限。

授权作用域只有 Global、Tenant 和 Project 三层，Cluster 通过所属 Project 继承授权。Namespace 不是授权
层级：对某个 Cluster 具有某项权限的用户，在该 Cluster 的所有 Namespace 上都具有该权限。详见
[应用作用域与资源模型](../architecture/resource-model.md)。

AI 发起的操作与用户直接发起的操作遵守相同权限规则，并额外要求展示分析依据、操作内容、目标资源和潜在影响。

当前项目尚未通过任何安全、云原生或 Kubernetes 认证，也不对生产可用性作出承诺。

## 当前实现状态

Phase 1 已实现本地用户密码安全基础、首个管理员初始化事务、用户与会话数据访问层，以及 Server 端
`login`、`logout`、`me` 认证 API。首个管理员拥有 Global `admin` RoleBinding。数据库只保存 Argon2id
密码摘要、Session Token 摘要和独立的 CSRF Token 摘要。Server 启动时只在用户表为空时从受保护密码文件创建
首个管理员；已有用户时跳过初始化。

认证 API 使用统一登录错误、请求体上限、账户与直接网络来源限流、Argon2id 全局并发上限、Server 端 Session、
Cookie 属性、Synchronizer CSRF Token、应用层操作超时和 Go 标准库跨源保护。密码凭证版本校验、可选摘要参数升级、
Session 创建与成功审计在同一事务中完成；登录成功、失败、限流拒绝与注销均写入不包含凭证明文的审计事件。

`me` API 返回按 RoleBinding 作用域展开的权限能力，供 Console 展示当前用户可执行的操作；该信息只用于界面
能力发现，不能替代服务端授权。当前用户自助改密要求有效 Session、CSRF、当前密码、新密码和显式确认；成功后
撤销该用户全部 Session、写入 `auth.password.change` 审计并要求重新登录。

RBAC 基础已经实现固定权限、`admin/viewer` 角色矩阵、Global/Tenant/Project RoleBinding 继承规则、默认拒绝、
Project/Cluster 归属解析和 HTTP 授权 middleware。Global `admin` 拥有全部固定权限；`viewer` 只拥有 Tenant、
Project 和 Cluster 读取权限。Tenant 绑定只向下覆盖同一 Tenant，Project 绑定只覆盖目标 Project，跨作用域
访问会被拒绝。

当前固定权限还包括 `user.read`、`user.manage`、`rbac.read`、`rbac.manage` 和 `audit.read`。Phase 1 的用户与
RoleBinding 管理入口只允许 Global `admin` 使用，避免在委派规则尚未扩展前出现权限提升；创建的 RoleBinding
仍可绑定 Global、Tenant 或 Project 作用域。Server 提供用户列表、详情、创建、修改显示名称、启用/禁用、
删除、解锁和管理员密码重置 API，以及 RoleBinding 列表、详情、幂等创建和删除 API。RoleBinding 是不可变
授权关系，修改通过删除后重新创建完成。禁止当前用户禁用或删除自身，也禁止禁用、删除或移除最后一个有效的
Global `admin`。权限授予、权限移除、用户状态变更、删除、解锁和密码重置均要求显式确认；禁用、锁定和密码
重置会撤销目标用户现有 Session，删除则在同一事务中永久移除用户、全部 Session 和全部 RoleBinding，用户名
随之释放。Enrollment、资源创建幂等记录和审计事件保留原用户 ID，审计事件还保留删除时的用户名。

账户错误密码计数和锁定期限持久化在 PostgreSQL，不因 Server 重启丢失。达到配置阈值后账户进入 `locked`，
现有 Session 被撤销；锁定期满后的首次正确登录会自动恢复，Global 管理员也可显式解锁。登录错误、账户锁定、
自动恢复、管理员解锁和密码重置均写入不包含密码的审计事件。

最后一个处于 `active` 状态的 Global `admin` 不会被锁定。账户锁定本身是一种针对已知用户名的拒绝服务手段：
若唯一管理员被锁定，没有第二个管理员可以调用解锁 API，初始管理员引导只在用户表为空时运行，该部署将失去
全部管理入口。这与"禁止删除或移除最后一个有效 Global `admin`"是同一条不变量的两面，因此按同样的理由拒绝。
该判定与写入在同一条 SQL 语句内完成，不会被并发的绑定变更分开。

被豁免的只有锁定本身：失败次数照常累计并可在用户列表中看到，审计事件照常写入，按账户的登录限流
（`auth.login_rate_limit.max_attempts_per_account`）继续生效，因此该账户仍然受到节流。阈值被跨过而未锁定时
额外写入一条 `auth.account.lock_withheld` 审计事件，每轮攻击一次。代价是该账户的口令必须足够强——它在限流
速率下可被持续尝试，因此最小口令长度要求同样适用于它。

RBAC 已接入 Tenant、Project、Cluster 的管理生命周期和 Cluster 聚合查询。固定资源权限包括
`tenant.create`、`tenant.read`、`tenant.manage`、`project.create`、`project.read`、`project.manage`、
`cluster.read`、`cluster.manage`、
`cluster.enrollment.create`、`cluster.enrollment.read`、`cluster.enrollment.revoke` 和
`cluster.connection.revoke`，以及通用 Kubernetes 写操作使用的 `cluster.resource.create`、
`cluster.resource.update` 和 `cluster.resource.delete`，以及读取 Pod 日志使用的专用
`cluster.pod.logs.read`、Web Terminal 使用的 `cluster.pod.exec`，以及读取 Kubernetes Event 使用的
`cluster.event.read`，以及目标集群 Kubernetes RBAC 使用的 `cluster.rbac.read`、`cluster.rbac.manage`，
以及读写 Kubernetes Secret 使用的 `cluster.secret.read`、`cluster.secret.manage`。
所有变更要求有效 Session 和 CSRF Token；创建
Enrollment、重新接入和 Kubernetes 写操作还要求 `Idempotency-Key`。Project、Cluster 的归属由 Server 查询，
不接受调用方覆盖。

通用 Kubernetes 写操作只允许明确 Cluster、GVR、Namespace 和名称的非 Secret、非授权主资源；Agent 与 Server
双重拒绝 Secret 和任意 Subresource，Kubernetes 授权资源由 Server 拒绝并要求使用专用接口，最终资源权限继续由 Agent ServiceAccount 的 Kubernetes RBAC 裁决。
实际变更要求显式确认，DryRun 可在确认前预览 API Server 校验和默认值。Create 禁止 `generateName`，
Update 要求 `resourceVersion`，Apply 默认不抢占字段所有权，Delete 支持 UID/resourceVersion 前置条件。
审计记录发起用户、Cluster、GVR/Namespace/名称、动作和结果，不记录资源正文。
DryRun 使用独立的 `.dry_run` 审计动作，不会与实际写入混记。类型化 Namespace 创建与删除沿用相同安全
边界：实际操作必须确认，删除可携带 UID/resourceVersion 前置条件，Console 在确认前先执行服务端 DryRun
并展示目标 Cluster 与影响。

Service、Ingress 与 Gateway 类型化接口沿用 `cluster.read` 和 `cluster.resource.create/update/delete`，
并固定 GVR 与 Namespace，客户端不能改写资源类型。更新要求当前 UID/resourceVersion，删除同时把两者作为
Kubernetes 前置条件；Gateway API 未安装与 ServiceAccount 无权访问分别返回能力缺失和禁止访问。Ingress 与
Gateway 只暴露 TLS Secret 引用名称，不读取或审计 Secret 正文。

ConfigMap 类型化接口同样固定 Cluster、Namespace 和 `core/v1/configmaps`，沿用上述读写权限。列表不返回正文，
更新和删除要求当前 UID/resourceVersion，实际写入要求 CSRF、幂等键与显式确认，审计只记录资源身份和结果。
ConfigMap 数据不按 Secret 处理，但仍不写入日志或审计正文。Secret 继续被通用 Resource/YAML 路径双重拒绝，
只能走下文的专用接口。

PV、PVC 与 StorageClass 类型化接口沿用相同的 `cluster.read` 和 `cluster.resource.create/update/delete`，并在
HTTP 与领域层同时校验资源作用域：PV、StorageClass 必须是集群级，PVC 必须指定 Namespace。所有更新先读取
当前对象并核对 UID/resourceVersion，删除把二者作为 Kubernetes 前置条件；实际写入仍要求 CSRF、幂等键和
显式确认。CSI Secret Reference 只包含 Namespace 与名称，Server、Agent 和审计均不读取或记录 Secret 正文。

HorizontalPodAutoscaler 类型化接口固定 `autoscaling/v2` 和明确 Namespace，沿用 `cluster.read` 与
`cluster.resource.create/update/delete`。Server 只接受同 Namespace 的 Deployment/StatefulSet 目标；实际写入
要求 CSRF、幂等键和显式确认，更新前重新读取并核对 HPA UID/resourceVersion，删除把二者作为 Kubernetes
前置条件。审计记录 HPA 资源身份和操作结果，不记录指标 Selector 或完整 spec 正文。

ResourceQuota、LimitRange、NetworkPolicy、PodDisruptionBudget 与 PriorityClass 同样沿用 `cluster.read` 和
`cluster.resource.create/update/delete`：这五类对象约束工作负载可以做什么，但不能提升调用者自身在 ZKE 或
Kubernetes 中的权限，因此不引入独立权限位，也不从通用 Resource/YAML 入口排除。HTTP 与领域层同时校验作用域，
前四类必须指定 Namespace，PriorityClass 必须是集群级。更新替换整份托管 spec，并在写入前重新读取对象核对
UID/resourceVersion；ResourceQuota 的 scopes、PriorityClass 的 value 和 PodDisruptionBudget 的 selector 不接受
类型化修改。实际写入要求 CSRF、幂等键和显式确认；审计记录资源身份与结果，不记录 spec 正文。

Kubernetes Secret 使用独立的 `cluster.secret.read` 与 `cluster.secret.manage`，不由 `cluster.read` 或
`cluster.resource.*` 蕴含：能读工作负载配置和能读凭证是两个问题，一个角色不该因为前者顺带获得后者。通用
Resource 与 YAML API 对 Secret 的拒绝保持不变，专用 Secret 服务是进程内唯一会在 Resource Stream 请求上设置
`secret_access` 的地方，而该字段在 Go 中不可导出。Agent 侧再判定一次：只在带该字段时才动 Secret，并拒绝任何
指向 Agent 自身命名空间的 Secret 请求——那里存放 Agent 身份私钥、注册令牌和它据以信任 Server 的证书。旧版本
Agent 不认识该字段会继续拒绝，因此 Server 先于 Agent 升级时该能力不可用，而不是被绕过。带
`app.kubernetes.io/managed-by=zke-server` 的 Secret 不列出、不可读写，返回与权限不足区分开的
`403 secret_managed_by_platform`；指向 Agent 自身命名空间的请求返回 `403 agent_namespace_forbidden`。两者都是
ZKE 的固定边界而不是上游 Kubernetes 的拒绝，因此不使用 5xx：那会被客户端当作可重试的故障，也会被读成给 Agent
补 Kubernetes 权限就能解决。列表不返回任何取值，详情返回的取值默认在界面上遮蔽；审计记录发起者、目标和
结果，不记录取值。

Secret 的 YAML 是一对独立路由，读要求 `cluster.secret.read`，写要求 `cluster.secret.manage`，不经过通用 YAML
入口——后者对 Secret 的拒绝没有放开。该路由使用 Secret 服务自己的资源访问，其只接受 `core/v1 Secret`，因此上述
平台对象过滤与 Agent 两条判定原样生效。写入前另外拒绝改变 `type`、拒绝写入已 immutable 的对象、拒绝为对象添加
`app.kubernetes.io/managed-by=zke-server`。一份 YAML 会一次返回该 Secret 的全部取值，与详情接口逐键遮蔽的呈现
方式不同，但两者要求的是同一个 `cluster.secret.read`；审计仍不记录正文。

目标集群内的 Kubernetes RBAC 使用独立的 `cluster.rbac.read` 与 `cluster.rbac.manage`，不复用普通
`cluster.read` 或 `cluster.resource.*`。ServiceAccount、Role、ClusterRole、RoleBinding、ClusterRoleBinding
从通用 Resource/YAML API 排除，只能通过固定资源类型和作用域的专用接口访问：类型化接口，或同样挂在
`cluster.rbac.read/manage` 上的专用 YAML 路由（按作用域分为命名空间级与集群级两条，作用域不符即拒绝）。
写入需要 CSRF、DryRun、确认、幂等、UID/resourceVersion 与审计；ServiceAccount 响应不返回 Secret 名称或正文。
Agent ClusterRole 不包含 `escalate`、`bind`、`impersonate`；类型化规则与 YAML 守卫同时拒绝这些 Verb、Secret 和
ServiceAccount Token，绑定不能直接引用内置 `zke-agent` 角色，也不能改写 `roleRef`，最终提权检查仍由 Kubernetes
API Server 执行。ZKE 管理的 Agent 授权对象禁止经该接口更新或删除，YAML 路由只允许读取；提交的文档也不能给对象
添加 `app.kubernetes.io/managed-by=zke-server`。YAML 与类型化接口执行同一套规则是这条路由能够存在的前提：
两者若不一致，宽的那条就是另一条的旁路。

资源对象浏览器不引入新的权限面：资源目录与对象列表使用 `cluster.read`，YAML 编辑使用
`cluster.resource.update`，删除使用 `cluster.resource.delete` 并要求 UID/resourceVersion 前置条件、CSRF、
幂等键与显式确认。它能看到的范围就是通用 Resource 接口的范围——Secret 与 Event 被 Agent 拒绝，五类 Kubernetes
授权资源被 Server 从该入口排除——因此浏览器不会成为绕过 `cluster.rbac.*` 或敏感资源限制的旁路。CRD 判定所需的
`customresourcedefinitions` 只读权限属于 Agent ServiceAccount，不改变调用者在 ZKE 中的权限。

通用 YAML 读取沿用 `cluster.read`，更新沿用 `cluster.resource.update`，不扩大 Agent ServiceAccount 权限；
Secret 与 Kubernetes 授权资源从该入口排除，避免绕过 `cluster.secret.*` 与 `cluster.rbac.*`，它们的 YAML 由上文
各自的专用路由提供。
更新只接受有界的严格单文档 YAML，并在发往目标 Cluster Agent 前，将正文的 GVR、Namespace、名称、UID 与
`resourceVersion` 和当前实时对象逐项核对；同名对象已重建或版本已变化时返回冲突。实际更新还要求 CSRF、
幂等键与显式确认，DryRun 使用同一 API Server 校验链路。日志与审计均不记录 YAML 正文或字段值。

Pod 日志读取不复用宽泛的 `cluster.read` 或通用资源写权限。请求必须明确 Cluster、Namespace、Pod 当前 UID
和容器，Server 与 Agent 通过独立日志协议执行，最终还受 Agent ServiceAccount 的 `pods/log` 最小权限约束。
实时 Follow 会周期重新验证 Session 和 `cluster.pod.logs.read`，权限收回后立即取消。成功与失败审计记录目标
作用域和结果，但不记录或摘要日志正文；鉴权拒绝使用同名权限动作进入 `denied` 词表。

Web Terminal 使用独立的 `cluster.pod.exec`，不复用 `cluster.read` 或 Kubernetes 通用写权限。创建一次性票据
必须通过 CSRF、幂等键和显式确认；WebSocket 必须同源并使用固定子协议，票据绑定用户、登录 Session、Cluster、
Namespace、Pod UID 和容器且只能消费一次。长会话周期重新验证 Session 与权限，设置空闲/总时长、输入/输出
字节和并发上限。Agent 只使用固定 Shell 选择逻辑（优先 bash，回退 `/bin/sh`），默认 ServiceAccount 仅增加
`pods/exec` 的 `create`。审计记录票据创建、会话目标与结果，不记录终端输入输出。

Kubernetes Event 同样不复用 `cluster.read`。Server 和 Agent 的通用 Resource 接口会拒绝并从 Discovery 中
隐藏 `core/v1/events`，只能通过独立 Resource Watch 协议读取。请求必须明确 Cluster 和 Namespace，可使用受限
字段过滤器定域到具体资源；实时 Follow 周期重新验证 Session 与 `cluster.event.read`。Agent ServiceAccount 仅
增加 `events` 的 `get/list/watch`，Event 的 message 正文不写入日志或审计，审计只记录作用域、过滤目标和结果。

长连接读取（Event 流、Pod 日志 Follow、Web Terminal）的审计 `result` 记录的是「服务端是否完成了这次读取」，
而不是流为什么结束。操作者关闭页面、Server 自身的最长时长到期、上游 Watch 轮换、resourceVersion 过期都属于
流的正常终止，记为 `succeeded`；实时重新验证发现权限被收回记为 `denied`；Agent 不可达、上游超时、容量耗尽和
上游错误记为 `failed`。这三类流都会在每次连接结束时各写一条审计事件，因此客户端的每次重连也各有一条记录：
把正常终止记成失败，只会让真正被拒绝或失败的读取淹没在噪声里。审计 `result` 只有 `succeeded`、`failed` 和
`denied` 三个取值，写入其他取值的事件会被审计服务丢弃，因此流的关闭原因必须先映射到这三者再落库。

管理端不暴露独立 Agent 资源。连接身份属于 Cluster 聚合内部状态，连接撤销接口
`POST /api/v1/clusters/{cluster_id}/connection/revoke` 按 Cluster ID 解析 Project 作用域，要求
`cluster.connection.revoke` 和显式确认。重新接入接口
`POST /api/v1/clusters/{cluster_id}/connection/reenroll` 仅在当前内部身份撤销后创建绑定原 `cluster_id` 的
一次性凭证。Cluster 停用与删除都使用 `cluster.manage`：停用断开 Agent 连接但保留身份与 Credential，
删除移除 Cluster 记录及其全部内部身份与 Credential。

Project 授权拒绝以及凭证创建的输入、状态和内部失败会写入不含 Token 的审计事件；数据库不可用或请求 Deadline
已耗尽时降级为安全错误日志。Tenant/Project/Cluster 生命周期、Cluster 列表/详情以及
Global/Tenant/Project/Cluster 授权拒绝审计已经实现。`GET /api/v1/audit-events` 按调用者 `audit.read`
RoleBinding 的 Global、Tenant 或 Project 可见范围过滤结果，支持条件过滤和与其他列表接口一致的有界
offset 分页。过滤条件通过统一的列表参数解析，未声明的查询参数会被拒绝而不是被静默忽略。用户、
RoleBinding、账户恢复和密码重置的成功、失败与权限拒绝也会写入审计。

`action` 与 `target_type` 过滤都是精确匹配，两份词表均由 Server 定义并通过
`GET /api/v1/audit-events/actions` 发布，客户端不应自行硬编码副本。鉴权拒绝没有独立的动作名，按被拒绝的
权限记录，因此权限名同样属于该词表，归入 `denied` 分组，其事件 `result` 恒为 `denied`；
`target_type` 记录被拒绝权限所保护的对象类型。

审计写入与失败登录计数不随请求取消而中止：判定完成后客户端断开连接，记录仍会落库，
被审计方无法通过断开连接决定哪些拒绝留痕、哪些失败登录不计入锁定。

可信反向代理来源解析和跨组织的细粒度委派管理仍属于后续工作。Phase 1 认证、用户与 RoleBinding 管理以及
审计查询后端已经实现，但项目仍处于早期开发阶段，不适用于生产环境。
