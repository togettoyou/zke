# 安全与权限

ZKE 的安全模型遵循以下原则：

- **最小权限**：用户、服务与 Agent 只获得完成任务所需的权限。
- **作用域明确**：所有查询和操作均关联租户、项目、集群及必要的 Namespace。
- **服务端校验**：不依赖前端隐藏操作，权限必须在服务端执行校验。
- **目标确认**：敏感操作必须展示目标集群、资源对象、操作内容和潜在影响。
- **受控审批**：AI 操作必须遵守会话审批模式，任何模式都不能绕过授权、目标校验、幂等与审计。
- **Agent 定域执行**：集群操作由目标集群中的 Agent 执行。
- **全程审计**：记录操作发起者、目标、请求、结果和必要的分析依据。
- **凭证保护**：ZKE Session Token、Agent 证书、模型 API Key 和 kubeconfig 不得进入日志、轨迹或 AI 上下文。

## 权限边界

不同用户只能查看和操作其权限范围内的资源。所有跨集群查询均需遵守租户、项目和 RBAC 权限边界，全局视图不代表全局操作权限。

授权作用域只有 Global、Tenant 和 Project 三层，Cluster 通过所属 Project 继承授权。Namespace 不是授权
层级：对某个 Cluster 具有某项权限的用户，在该 Cluster 的所有 Namespace 上都具有该权限。因此
`cluster.namespace.manage` 是「在该 Cluster 内创建和删除 Namespace」，不是「管理某一个 Namespace」。详见
[应用作用域与资源模型](../architecture/resource-model.md)。

当前项目不声明已经通过任何安全、云原生或 Kubernetes 认证。

### AIOps 的权限模型

AIOps 的只读 Agent 循环已经实现。AIOps 与容器服务一样跟随 Console 当前 Tenant/Project，并在 App 内选择 Cluster
工作区；切换 Cluster 后只能看到该 Cluster 的会话。会话创建后不可切换目标，也不会跨 Cluster 查询或操作。
会话持久化 `tenant_id`、`project_id` 和 `cluster_id` 但不设置外键，每次访问都重新解析当前归属。Cluster 选择不新增授权层级。`ai.run` 是独立
前置权限且不进入内置 `viewer`；后台每个 Step 重新检查账户有效、Project 和会话 Cluster 权限，结构化证据还必须属于
同一 Cluster，并检查当前资源、Event、指标或 Pod 日志读取权限。

模型每次工具调用都是一次独立授权：工具声明它需要的权限集合，运行时针对当前用户在会话固定 Cluster 上逐项校验，
缺一项就不执行，并把这个判定写入 `tool_call`。`ai.run` 从不替代其中任何一项，因此只有 `ai.run` 的账户在 AIOps 里
读不到任何集群内容。工具的目标 Cluster 由运行时填入，模型无法在参数里改写；未在 Schema 中声明的参数一律拒绝，
而不是忽略——被静默丢弃的 `namespace` 会把一次窄查询变成全集群查询。

会话删除采用两阶段保护：活动会话只能归档，永久删除接口只接受已归档且空闲的会话，并同时删除其轨迹和附件。

关闭窗口不终止云端任务。后台任务以发起用户 ID 运行，不保存或继续持有浏览器 Session Token，并在每个外部阶段
重新读取账户和 RBAC。定时巡检和
事件触发任务必须使用显式委派，限制所有者、Project、Cluster、权限、审批策略与有效期；该机制落地前不开放无人值守写操作。

三档审批模式已经生效：请求批准 / 帮我批准 / 完全访问只改变何时等待人工确认，不改变权限上限。当前只读目录中
被标记为敏感的是 Pod 日志读取和 Cluster Terminal 命令，前两档会停下来等待用户在对话中批准或拒绝；拒绝不是运行失败，模型收到明确说明
后继续，无人答复超时才结束本轮。批准只对该次调用生效，不改变账户权限，并与请求一起写入轨迹。

资源写工具支持 Deployment/StatefulSet 伸缩、工作负载历史读取与回滚，以及 Manifest DryRun/差异/Apply/Delete。
它们遵守会话审批模式、确认、幂等与审计边界；Manifest 与回滚以服务端预检快照固定待批准内容，提交前重新判权并
再次 DryRun。Cluster Terminal 命令使用 Turn 级终端 Pod 和冻结的当前权限快照；所有调用经目标 Agent 定域执行，不在
Server 主机执行，也不给模型 kubeconfig。

历史读取需要重新检查当前权限。用户自己的问题和系统状态可以保留；已无权访问的集群正文、工具结果和证据由
服务端脱敏。Pod 日志、Event、annotation、ConfigMap、Secret 和终端输出均作为不可信数据，不能改变工具白名单、
作用域或授权判定。当前工具目录不提供读取 Secret 明文的工具：`cluster.secret.read` 不会因为 AIOps 而变成一条把
凭证送进模型上下文的路径。

完整设计见 [Phase 4 AIOps：架构设计](../architecture/ai-phase-4.md)。

## 当前实现状态

当前已实现本地用户密码安全基础、首个管理员初始化事务、用户与会话数据访问层，以及 Server 端
`setup`、`login`、`logout`、`me` 认证 API。Console 检测到系统没有 Global `admin` RoleBinding 时显示初始化页面，
由用户设置首个管理员的用户名和密码。数据库只保存 Argon2id 密码摘要、Session Token 摘要和独立的 CSRF Token
摘要；Server 不创建默认账号，也不生成或保存初始明文密码文件。

认证 API 使用统一登录错误、请求体上限、账户与直接网络来源限流、Argon2id 全局并发上限、Server 端 Session、
Cookie 属性、Synchronizer CSRF Token、应用层操作超时和 Go 标准库跨源保护。密码凭证版本校验、可选摘要参数升级、
Session 创建与成功审计在同一事务中完成；登录成功、失败、限流拒绝与注销均写入不包含凭证明文的审计事件。

审计事件带两个可选字段。`actor_ip` 记录发起请求的客户端地址，仅在事件来自用户驱动的 HTTP 请求时写入——
认证类事件（登录成功/失败、限流拒绝、账号锁定与豁免、改密）是它最主要的用途，因为「来自哪里」在请求结束后
再也无处可查；`system` 与 `agent` 发起者、以及在存储事务内写入的事件为空。地址以 `inet` 存储，无法解析的
地址会被丢弃而不是让审计写入失败——丢一个字段远好过丢掉整条记录。

`detail` 以稳定键记录 `result` 的结构化原因。授权拒绝会写入 `scope_type`；当实际校验的权限与路由声明的权限
不一致时——受保护命名空间会把普通资源权限替换为更严格的权限——还会写入 `requested_permission`，否则读者只能
看到一条针对调用方从未请求过的权限的拒绝记录。该字段不承载请求或响应正文、Token、凭证与 Kubernetes 对象内容：
审计记录的保留期远长于它所描述的数据，放进去的东西会一并继承这个生命周期。

`me` API 返回按 RoleBinding 作用域展开的权限能力，供 Console 展示当前用户可执行的操作；该信息只用于界面
能力发现，不能替代服务端授权。当前用户自助改密要求 Global `user.password.change` 权限、有效 Session、CSRF、
当前密码、新密码和显式确认；成功后撤销该用户全部 Session、写入 `auth.password.change` 审计并要求重新登录。

RBAC 基础已经实现固定权限词表、可由操作者定义的角色、Global/Tenant/Project RoleBinding 继承规则、默认拒绝、
Project/Cluster 归属解析和 HTTP 授权 middleware。Tenant 绑定只向下覆盖同一 Tenant，Project 绑定只覆盖目标
Project，跨作用域访问会被拒绝。

### 角色

角色是一组权限的命名集合，`role_bindings.role` 以外键引用它。权限词表本身仍然固定在 Server 代码里——它同时
是审计动作的一部分——但哪些权限组成一个角色由操作者决定。

内置角色只有 `admin` 与 `viewer` 两个，由 Server 定义，不可编辑也不可删除。`admin` 的语义是"Server 定义的
全部权限"而不是一份清单，`viewer` 则是一份固定清单——因为"只读"是一个需要有人明确决定的判断，新增的读权限
不会自动进入。

两者的名称、说明和权限集只存在于代码（`pkg/server/rbac.BuiltinRoles`），由 Server 在每次启动时对账写入
数据库。**Schema 不播种任何角色**：把 `admin` 的权限清单抄进 SQL 就是把同一件事维护两遍，而抄本会在下一次新增
权限时过时，且过时的那一份正是运维会去读的那一份。这一列里也没有通配符：一个表示"全部"的取值必须被授权判定、
提权上限检查和能力上报三处分别解释，而它恰好又是自定义角色绝对不能持有的取值——保持这一列只存字面权限名，把
内容交给 Server，同时避免了这三个问题。

因此，迁移已执行但 Server 从未启动过的数据库里没有任何角色。这是安全的方向：那时也不可能存在任何 RoleBinding，
因为 `role_bindings.role` 引用的正是这张表。

自定义角色可以创建、修改和删除。标识（`name`）创建后不可修改：绑定和历史审计记录都以它指代该角色，改名会
让一条历史记录看起来在说另一件事；可修改的是显示名称、说明和权限集，其中权限集是整体替换而不是增量。仍被
绑定的角色不能删除，这一条由数据库外键强制，服务端在同一事务内先读绑定计数以给出可操作的错误。

### 提权防护

角色的权限集不得超出**调用者本人在 Global 作用域已持有的权限**，创建角色和创建绑定两条路径检查整份权限集，
超出时返回 `403 permission_escalation` 并列出越界的权限名。

两条路径都要检查，是因为它们互为绕过：只检查创建角色，持有 `rbac.manage` 的人可以直接把已经存在的 `admin`
绑定给自己；只检查绑定，则可以先造一个角色再绑。没有这条限制，`rbac.manage` 就等于全部权限，平台定义的其余
权限位会全部塌缩成一个。

**修改角色按「改动了什么」判定，两个方向都要在调用者的权限之内。** 一次修改提交的是整份权限集，但调用者实际
在做的是它与已存权限集的差：

| 差                           | 判定                           | 拒绝                        |
| ---------------------------- | ------------------------------ | --------------------------- |
| 新增（在新集合、不在旧集合） | 必须是调用者在 Global 已持有的 | `403 permission_escalation` |
| 移除（在旧集合、不在新集合） | 同上                           | `403 permission_revocation` |
| 两个集合里都有               | 不判定，原样保留               | —                           |

移除那一侧此前完全没有检查。它不授予任何人任何东西，所以不是提权，但它是对**高于自己权限层级的一次单方面
处置**：一个只持有 `rbac.read` 与 `rbac.manage` 的账号可以把平台上每个自定义角色的权限剥光，包括它自己永远
拿不到的那些，而被剥掉的人也无法自行恢复。在「撤销」这一侧，`rbac.manage` 曾经仍然等于全部权限。

**删除 RoleBinding 适用同一条规则**：删除一个绑定等于一次性收回它所授予角色的全部权限，因此要求调用者持有
该角色的每一项权限，否则同样返回 `403 permission_revocation` 并列出越界的权限名。少了这一条，角色那一侧的
限制只是半条规则——从角色里删掉 `tenant.create` 被拒绝，而删掉「授予某人一个含 `tenant.create` 的角色」的
绑定是同一次收回、落到同一个人头上，却是更短的一条路。Global `admin` 原本就由全局管理员保护挡住，但**一个
包含全部权限的自定义角色**不是 `admin`，此前不受任何约束。

该检查排在自解绑规则之后，因此给自己解绑仍然报 `409 self_unbind_forbidden` 这个更具体的说法。两者不会冲突：
授予你的绑定不会授予任何你未持有的权限，所以在那条路径上这项检查本来就通过。

按差集而不是按整份集合判定，还修掉了一个反向激励。此前天花板作用于提交的整份集合，于是一个含有调用者未持有
权限的角色，**唯一能保存的编辑就是先把那些权限删掉**——保留它们会被判为提权而拒绝，界面上的提示甚至在指路
「无法保存：创建项目、创建租户、管理租户」。改个说明、改个名称都得先做一次破坏。现在两个集合里都存在的权限
不参与判定，这类修改照常保存。

创建角色、修改角色、创建或删除绑定的最终判定都在数据库写事务内完成。事务先取得授权变更 advisory lock，再读取
调用者的 Global 权限与目标角色；角色行也会被锁定。服务层保留的创建预检只用于更早返回可读错误，不是安全边界。
这避免了调用者权限被并发撤销、目标角色被并发改写后，先前的检查结果仍被提交。

以 Global 作用域为准，而不是调用者当前所在的作用域：角色是全局对象，可以在之后被绑定到任何地方，因此一个只在
某个 Project 内持有的权限，否则就能通过写进角色变成在任何地方可用。Kubernetes 用 `escalate` 和 `bind` 解决
同一个问题，ZKE 没有对应的豁免动词，这是有意的。

依赖缺失按拒绝处理：Server 在构造时注入权限判定服务，缺少它时角色写入直接失败，而不是跳过检查。

### 自我锁死防护

提权天花板管的是角色权限集的**上界**，另有一条管**下界**：修改角色之后，调用者本人在 Global 持有的权限集
不得比修改前更小，否则返回 `409 self_lockout_forbidden` 并列出会失去的权限名。

**这条规则的存在完全是天花板造成的。** 角色只能包含作者在全局已持有的权限，而创建角色、修改角色和创建绑定
三条路径都执行这条检查。于是一名操作者若把某项权限从自己唯一的全局来源里删掉，就再也放不回去：放不回那个角色
（修改被天花板拒绝），也建不出带它的新角色，更不能给自己绑一个已经带它的角色——三条路都以同一个已经不含它的
集合为准。界面上的表现就是那一项复选框变成禁用并标着「当前账号未持有」：看得见，永远够不着。

因此这和禁止自解绑（`409 self_unbind_forbidden`）是同一个论证，只是从另一条路走到同一个地方——**一次做完之后
做的人自己无法撤销的修改**。

**这条规则一度只守 `rbac.read` 与 `rbac.manage`，理由是「其余权限都能靠这两项加回来」。那个理由是错的**：
天花板不区分「恢复」和「授予」，`tenant.read` 和 `rbac.manage` 一样收不回来。现在守的是全部权限。

只比较 **Global** 权限集，因为天花板本身只按全局绑定计算：一项只通过 Tenant 或 Project 绑定持有的权限，本来
就不可能被写进任何角色，也就不存在「失去了就补不回来」的问题。这不是取舍，是同一个集合。

不构成损失的修改照常允许：编辑自己不持有的角色、编辑只通过作用域绑定持有的角色、以及在同一权限另有全局来源时
把它从某个角色里去掉——被拒绝的只有真正让调用者的全局权限集变小的那一次。持有内置 `admin` 的人不受影响：它
不可编辑，任何自定义角色的修改都不会让全局管理员失去任何权限。

判定是**写入前后各读一次、在同一事务内**比较，而不是预测：调用者可能通过多个全局绑定持有同一项权限，这次编辑
是否真的让他失去它取决于全部绑定。拒绝时返回失去的权限名，与提权拒绝同一种形态——操作者面对的是一列复选框，
需要知道该把哪一个勾回去。

这条实现里没有硬编码任何权限名，因此不需要跨包常量对账。

删除角色不需要这条检查：仍被绑定的角色删不掉（外键与服务层绑定计数），而调用者持有它就意味着它被绑定。

### 全局管理员保护

**全局管理员**指在 Global 作用域绑定了内置 `admin` 角色的 `active` 账号——不是「持有等价权限集的账号」。
围绕它有两条规则，缺一不可：

1. **必须始终存在至少一个。** 否则该部署除了直连数据库没有任何回到管理态的途径。
2. **只有全局管理员可以控制全局管理员。** 持有自定义角色不算，哪怕那个角色包含全部权限。

第二条是针对账号被攻破的。它一度不存在：判定曾按「谁还持有 `user.manage` 与 `rbac.manage`」来算，于是一个
持有全部权限的自定义角色也被算作管理员，删除真正的管理员时计数是 2、看起来安全——对平台是安全的，对平台的
所有者不是。

「控制」是**这个账号上的一切写操作**，而不是一份危险操作清单。共七条路径，从群体外部发起时都返回
`403 global_admin_required`：

| 路径                                 | 执行的规则                     |
| ------------------------------------ | ------------------------------ |
| 删除用户、禁用用户、删除 RoleBinding | 两条都执行                     |
| 授予 Global `admin`                  | 只有第二条——授予不会移除任何人 |
| 重置密码、解锁账户、修改显示名称     | 只有第二条                     |

最后一行只执行第二条是必要的：夺取账号不等于移除账号，若把「必须保留一个」也套上，唯一的管理员将无法重置自己的
密码，也无法改自己的名字。

重置密码、解锁和改名为什么在这里，理由各不相同。重置密码是这条规则最初的来由：删除、禁用和解绑守的都是
RoleBinding，而重置全局管理员的密码再以其身份登录，拿到的是同一套权限，却不产生任何绑定可供那些检查看见——
`user.manage` 曾因此单独构成一条完整的提权路径。解锁按同样的理由归入：锁定是密码尝试的终止条件，从群体外部
清除它等于把这轮尝试还回去。

**修改显示名称不是因为它危险。** 改名什么也拿不到，这正是它当初被漏掉的原因，也正是它必须在这里的原因：把规则
写成「不得夺取该账号」，就等于让后来每一个新增操作各自判断自己够不够危险，而做这个判断的人正在看自己那个操作，
不是在看这个群体。写成「不得控制该账号」，就没有什么需要判断。

普通账号不受「必须由全局管理员操作」这条成员资格规则约束；但密码重置、禁用、启用、解锁和删除仍受下文账号权限
上限约束。修改显示名称不改变权限，继续只要求 `user.manage`。

**全局管理员在用户列表中被标出，而不是被隐藏。** 上面七条拒绝本身就是一个 oracle——持 `user.manage` 的账号
逐个试一次改名，看返回 200 还是 403，就精确知道谁是管理员——所以隐藏列表只是在一扇打开的门前拉一道帘子。要让
隐藏成立，就得让拒绝无法区分（例如一律返回 404），代价是账号明明存在却被告知不存在，日常 helpdesk 流程被破坏，
而运维为此展开的追查反而更引人注意。隐藏面也会失控：RoleBinding 列表按定义就会暴露谁绑了 Global `admin`，
审计事件按 `target_id` 记录每一次针对该账号的操作——要保持一致就得按被审计者的身份过滤审计，而那是比暴露管理员
身份坏得多的主意。何况 `user.read`、`rbac.read`、`audit.read` 都是 Global 下限的权限，持有者本身就是平台级的
监督角色，把最要紧的账号从监督权限的视野里拿掉，方向是反的。真正缺的是那七条拒绝此前毫无预告——现在行上有标记，
标记需要 `rbac.read`，没有该权限时退化为不标注而不是报错。

**「是不是管理员的账号」不看账号状态，「还剩几个管理员」才看。** 两个问题的答案不同，用同一个集合回答会开一个
和锁定完全同形的口子：五次错误密码会把管理员从 `active` 上摘下来，凡是按活跃集合判定的守卫此后都认为那是一个
普通账号。因此成员判定只问是否在 Global 绑定了 `admin`，与 `active`/`locked`/`disabled` 无关；计数仍只算活跃
账号，且「最后一个」的拒绝只在目标本身处于活跃集合内时才触发——移除一个被锁定的管理员不会让活跃计数减少。

表中「授予 Global `admin`」那一行是第二条规则容易被忽略的一半：**成员资格必须从群体内部授予**。少了它前两条
形同虚设——一个包含全部权限的自定义角色满足 `admin` 的提权天花板，其持有者可以先把 `admin` 绑给自己、成为全局
管理员，再删掉原来那个。守卫「最后一道账号」的这个群体，谁能进来只能由已经在里面的人决定。

不按权限而按角色判定不会削弱恢复能力：`admin` 是内置角色，不可编辑，且每次启动从代码对账，所以只要还有一个
全局管理员，Server 定义的每一项权限就都还在可达范围内。自定义角色的权限收窄因此不再需要这条检查——它触碰不到
`admin`。所有判定与写入在同一事务内，并由 advisory lock 串行化。

账户锁定同样按这条规则豁免最后一个全局管理员（见下文）。

### 账号操作权限上限

`user.manage` 不是越过其他权限边界的通行证。密码重置可以直接接管目标身份，禁用和删除会一次性撤销目标的全部
RoleBinding 效果，启用和解锁则会恢复这些权限。因此执行密码重置、禁用、启用、解锁或删除前，调用者必须在 Global
作用域持有目标账号所有 RoleBinding 所携带的每一项权限；否则返回 `403 target_authority_exceeded`，且账号、密码、
Session 和绑定均不发生变化。调用者重置自己的密码不受此限制，因为操作前后仍是同一个身份。

该上限不依赖目标当前是否 active：锁定或禁用高权限账号不会把它降格成可由低权限账号接管或删除的普通目标。判定与
写入位于同一数据库事务，并与角色和 RoleBinding 变更共用授权变更 advisory lock，避免检查后并发改权形成竞态。
内置 Global `admin` 还会先执行更严格的全局管理员成员资格规则。

### 权限的作用域下限

绑定可以选三种作用域，角色可以装任意权限，于是两者能被组合成一条永远不生效的授予。每项权限因此有一个**作用域
下限**——能够行使它的最窄绑定作用域，比它更窄的绑定携带它不生效。下限共两档，其余权限没有下限。

| 权限                       | 下限   | 原因                                                                              |
| -------------------------- | ------ | --------------------------------------------------------------------------------- |
| `user.read`、`user.manage`、`user.password.change` | Global | 用户身份是全局对象，不属于任何租户                                      |
| `rbac.read`、`rbac.manage` | Global | 角色是全局对象，见上文提权天花板                                                  |
| `tenant.create`            | Global | 创建租户时还没有可供限定的租户                                                    |
| `tenant.manage`            | Global | 删除租户会级联移除其下全部项目、集群与凭证，**有意不交给租户自己的管理员**        |
| `project.create`           | Tenant | 创建项目时还没有可供限定的项目，但租户正是它可以被限定到的东西——所以它不是 Global |

`project.create` 是唯一一项 Tenant 下限，它的存在正是这里从「是否仅全局」改为「下限是哪一档」的原因：一个只含
`project.create` 的角色绑到 Project 上授予的恰好是零，而一个布尔标记会把它报成不受限。

其余权限都在与其名字相符的作用域校验：`project.read`、`project.manage` 在 Project，`cluster.*` 在
Cluster（经所属 Project 解析，因此等同 Project 作用域），`tenant.read` 与 `audit.read` 没有固定作用域路由，
改为在服务层按调用者的可见范围过滤。

**只有整个角色都由该作用域无法行使的权限组成时，绑定才会被拒绝**，返回 `400 role_unreachable_at_scope` 并列出
权限名——那样的绑定不授予任何权限，读起来却像一次授权，除了「写错了」没有别的解释。

混合角色不拒绝，这条界线是刻意的。内置 `admin` 绑到某个租户或项目仍是一条真实且有用的部分授予。拒绝它等于让
唯一表示「这里的全部权限」的角色无法用于任何作用域绑定，迫使每个部署手工
复制一份租户版 `admin`。问题从来不是授予是部分的，而是缺失的那部分不可见——所以修在它坏掉的地方：`me` 的作用域
能力列表不再包含该作用域行使不了的权限，权限字典为每一项返回 `minimum_scope`（`global`/`tenant`/`project`），
Console 在角色编辑器里标注「仅全局生效」或「仅全局和租户生效」，并在新建绑定时按本次选定的作用域列出不会授予的
权限。

已存在的绑定不受影响——这是创建路径上的检查，原本就不生效的权限继续不生效，而不是被一次升级夺走。

`pkg/server/httpapi.TestPermissionScopeFloorsMatchRoutes` 解析 `routes.go`，把每项权限被传入的授权 middleware
折算成下限，与 `rbac.MinimumScope` 逐项比对，确保这份清单与路由实际校验的作用域不会悄悄脱节。

### 访问管理接口

当前固定权限还包括 `user.read`、`user.manage`、`user.password.change`、`rbac.read`、`rbac.manage` 和
`audit.read`。`user.password.change` 只允许调用者修改自己的密码，不授予查看用户或重置他人密码的能力。用户、角色与
RoleBinding 管理入口都要求 Global 作用域的对应权限；创建的 RoleBinding 仍可绑定 Global、Tenant 或 Project
作用域（受上文「只在 Global 生效的权限」约束）。Server 提供用户列表、详情、创建、修改显示名称、启用/禁用、删除、解锁和管理员密码重置 API，角色列表、
详情、创建、修改和删除 API，权限字典 API，以及 RoleBinding 列表、详情、幂等创建和删除 API。

权限字典（`GET /api/v1/permissions`）返回 Server 定义的全部权限，并标注调用者本人是否在全局持有该权限。
它由接口提供而不是固化在 Console 里：一份写在前端的清单会与 Server 静默脱节，Server 新增而前端未列出的权限
将是一个任何角色都无法被授予的权限，且没有任何地方会报错。该接口要求 `rbac.read` 且返回调用者自己的权限上限，
因此不对未认证访问开放。

RoleBinding 是不可变授权关系，修改通过删除后重新创建完成。禁止当前用户禁用或删除自身，**也禁止删除授予自身
的 RoleBinding**（`409 self_unbind_forbidden`）：绑定按 ID 删除，请求里没有任何字段说明这一条授予的是谁，
而删掉自己那条的结果是失去授权本次删除的权限，操作者自己无法撤销。需要卸任时由另一名持有 `rbac.manage`
的账号执行；同理也禁止通过修改角色让自己的全局权限集变小（`409 self_lockout_forbidden`，见上文
「自我锁死防护」）。两条合起来是同一条：**收回一个人的权限只能由别人来做。**同样禁止禁用、删除或移除最后一个全局管理员，也禁止非全局管理员对全局管理员的账号
或成员资格做任何写操作（见上文「全局管理员保护」）。

两条规则在同一条路径上都会命中时，**先判定最后一个全局管理员**。这不是排版偏好：`administrators <= 1` 要成立，
发起者和目标必须同时在那个集合里，而只有一个成员的集合里两者就是同一个人——也就是说「最后一个全局管理员解绑
自己」是这条路径上唯一能产生 `last_global_admin` 的情形，把自解绑排在前面会让该错误码整体不可达。它给出的说法
也更差：自解绑规则让人去找另一名管理员，而此时并不存在另一名管理员。管理员在两名以上时，自解绑规则才既适用
又准确。权限授予、权限移除、用户状态变更、删除、解锁和密码重置均要求显式确认；禁用、锁定和密码
重置会撤销目标用户现有 Session，删除则在同一事务中永久移除用户、全部 Session 和全部 RoleBinding，用户名
随之释放。Enrollment、资源创建幂等记录和审计事件保留原用户 ID，审计事件还保留删除时的用户名。

账户错误密码计数和锁定期限持久化在 PostgreSQL，不因 Server 重启丢失。达到配置阈值后账户进入 `locked`，
现有 Session 被撤销；锁定期满后的首次正确登录会自动恢复，Global 管理员也可显式解锁。登录错误、账户锁定、
自动恢复、管理员解锁和密码重置均写入不包含密码的审计事件。

最后一个处于 `active` 状态的 Global `admin` 不会被锁定。账户锁定本身是一种针对已知用户名的拒绝服务手段：
若唯一管理员被锁定，没有第二个管理员可以调用解锁 API；首次初始化接口也不会绕过已有的 Global `admin` 绑定，
该部署将失去全部管理入口。这与“禁止删除或移除最后一个有效 Global `admin`”是同一条不变量的两面，因此按同样的理由拒绝。
即将跨过锁定阈值的失败请求会取得与删除、禁用和解绑相同的全局管理员不变量 advisory lock，再统计活跃管理员并
写入状态。普通失败计数不取得该锁。这样两个管理员同时达到阈值，或锁定与另一名管理员的禁用、解绑并发发生时，
也只会有一个操作移除活跃管理员，至少保留一个可登录账号。

被豁免的只有锁定本身：失败次数照常累计并可在用户列表中看到，审计事件照常写入，按账户的登录限流
（`auth.login_rate_limit.max_attempts_per_account`）继续生效，因此该账户仍然受到节流。阈值被跨过而未锁定时
额外写入一条 `auth.account.lock_withheld` 审计事件，每轮攻击一次。代价是该账户的口令必须足够强——它在限流
速率下可被持续尝试，因此最小口令长度要求同样适用于它。

RBAC 已接入 Tenant、Project、Cluster 的管理生命周期和 Cluster 聚合查询。权限词表固定在 Server 代码中
（`pkg/server/rbac`）并由 `GET /api/v1/permissions` 发布，当前分为以下几族，具体语义见本文各节：

| 族 | 权限 |
| --- | --- |
| 组织与资源 | `tenant.create/read/manage`、`project.create/read/manage`、`cluster.read`、`cluster.manage` |
| 集群接入 | `cluster.enrollment.create/read/revoke`、`cluster.connection.revoke` |
| Kubernetes 资源 | `cluster.resource.create/update/delete` |
| 命名空间 | `cluster.namespace.manage`、`cluster.system_namespace.manage`、`cluster.agent_namespace.manage` |
| 敏感资源 | `cluster.secret.read/manage`、`cluster.rbac.read/manage`、`cluster.event.read` |
| 节点与 Pod 操作 | `cluster.node.manage`、`cluster.node.drain`、`cluster.pod.logs.read`、`cluster.pod.exec`、`cluster.pod.port_forward`、`cluster.pod.terminal_recording.create/read`、`cluster.terminal.exec` |
| 可观测性 | `cluster.metrics.read`、`cluster.metrics.manage` |
| 平台管理 | `user.read/manage`、`user.password.change`、`rbac.read/manage`、`audit.read` |
| AIOps | `ai.run`（只允许在当前 Project 创建并运行固定 Cluster 会话，不包含任何集群读取权限） |

所有变更要求有效 Session 和 CSRF Token；创建
Enrollment、重新接入和 Kubernetes 写操作还要求 `Idempotency-Key`。Project、Cluster 的归属由 Server 查询，
不接受调用方覆盖。

多文档 YAML 清单接口不引入新权限，但它的所需权限由正文而不是路由决定，见下文
「多文档清单：逐文档判权，整份拒绝」。

通用 Kubernetes 写操作只允许明确 Cluster、GVR、Namespace 和名称的非 Secret、非授权主资源；Agent 与 Server
双重拒绝 Secret 和任意 Subresource，Kubernetes 授权资源由 Server 拒绝并要求使用专用接口，最终资源权限继续由 Agent ServiceAccount 的 Kubernetes RBAC 裁决。
实际变更要求显式确认，DryRun 可在确认前预览 API Server 校验和默认值。Create 禁止 `generateName`，
Update 要求 `resourceVersion`，Apply 默认不抢占字段所有权，Delete 支持 UID/resourceVersion 前置条件。
审计记录发起用户、Cluster、GVR/Namespace/名称、动作和结果，不记录资源正文。
DryRun 使用独立的 `.dry_run` 审计动作，不会与实际写入混记。

#### 受保护命名空间的独立权限

普通 `cluster.resource.create/update/delete` 与 `cluster.namespace.manage` 不能单独修改系统命名空间。目标为
`kube-*` 时，资源增删改和 Namespace 生命周期改用 `cluster.system_namespace.manage`；`default` 内的普通资源仍按
通用资源权限管理，但创建、修改或删除 `default` Namespace 本身同样使用系统命名空间权限。目标为该 Cluster
保存的 Agent Namespace（首次接入界面默认 `zke-system`）时，资源和 Namespace 的增删改改用
`cluster.agent_namespace.manage`。两项权限均可在 Global、Tenant 或 Project 作用域授予，内置 `admin` 由权限词表
自动获得，其他角色不会因持有通用资源权限而自动获得。

独立命名空间权限不替代资源家族自身的敏感权限：在这些命名空间读写 Secret 仍同时要求
`cluster.secret.read/manage`，修改 Kubernetes RBAC 仍要求 `cluster.rbac.manage`，Pod Exec 和端口转发仍要求各自的
专用权限。普通对象读取保持 `cluster.read`。节点 Drain 在读取 Pod 清单后逐 Pod 判定，缺少相应命名空间权限时把
该 Pod 标为阻塞项，不会先驱逐其他 Pod。类型化接口、通用 Resource/YAML、多文档清单和 Console 使用同一判定；
清单逐文档使用对应的命名空间权限替代普通资源权限；Secret 与 Kubernetes RBAC 等敏感资源仍额外检查资源家族权限，
任一文档不满足就整份拒绝。

#### 节点对象的独立权限

Node 对象自身的写入使用独立的 `cluster.node.manage`，不由 `cluster.resource.create/update/delete` 蕴含。
覆盖范围是「改的是 Node 这个对象」的全部入口：容器服务的节点标签编辑与调度开关（对 `metadata.labels` 与
`spec.unschedulable` 的 merge patch）、节点 YAML 编辑、资源对象浏览器中的 Node、通用 Resource/YAML 路由上
GVR 为 core/v1 `nodes` 的写入，以及清单中的 Node 文档。集群终端的 kubectl 同样只在会话持有该权限时才被投射
`nodes` 的 `update`、`patch`。

理由与 Namespace 同源，是影响面而不是敏感度：其余 `cluster.resource.*` 写入作用于一个对象，而节点的标签、
污点与 `spec.unschedulable` 决定整个集群的调度结果——一次 `kubectl label node` 足以把工作负载吸引到或排除出
一批机器。把它和「改一个 ConfigMap」放在同一个权限位上，等于由同一次授予决定两件量级不同的事。

读取没有被收窄：节点列表、详情、诊断和 YAML 读取仍是 `cluster.read`。驱逐节点上已有的 Pod 仍是独立的
`cluster.node.drain`，两者互不蕴含——只持有 `cluster.node.manage` 可以停止调度，但不能驱逐；只持有
`cluster.node.drain` 可以排空，但不能改标签。Node 是集群级对象，因此受保护命名空间权限不参与它的判定。

内置 `admin` 由权限词表自动获得该权限；升级前已存在的自定义角色不会因为持有 `cluster.resource.update` 而
自动获得，需要显式授予。

#### Namespace 的创建与删除

类型化 Namespace 创建与删除沿用上述安全边界——实际操作必须确认，删除可携带 UID/resourceVersion 前置条件，
Console 在确认前先执行服务端 DryRun 并展示目标 Cluster 与影响。普通 Namespace 使用独立的
`cluster.namespace.manage`，受保护 Namespace 则使用上一节对应的系统或 Agent 命名空间权限；这些权限都不由
`cluster.resource.create/delete` 蕴含。

理由不是 Namespace 更敏感，而是影响面不在一个量级：其余 `cluster.resource.*` 写入作用于一个对象，删除一个
Namespace 则连同其中的全部对象一起移除，创建一个则新增了其余权限得以行使的作用域本身。把两者放在同一个权限位
上，等于「能改一个 ConfigMap」和「能清空一个命名空间」由同一次授予决定。

与 Secret、Kubernetes 授权资源不同，Namespace 的**读取没有被收窄**：列表与详情仍是 `cluster.read`，通用
Resource 与 YAML 接口照常返回它们。Server 会在类型化、通用 Resource 与 Manifest 路径中解析实际 Namespace 目标，
并以对应的独立权限替代通用资源写权限；资源目录无需通过移除 `create`、`delete`、`patch` 动词隐藏能力。因此具备
相应权限的调用者可以从任一受支持入口操作 Namespace，同时普通 `cluster.resource.*` 不能成为绕过路径。

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

VerticalPodAutoscaler 与 KEDA ScaledObject 类型化接口分别固定 `autoscaling.k8s.io/v1` 和
`keda.sh/v1alpha1`，同样沿用 `cluster.read` 与 `cluster.resource.create/update/delete`，并始终定域到明确
Cluster 和 Namespace。两类更新都先读回对象核对 UID/resourceVersion，删除把两者交给 Kubernetes 作为前置条件；
实际写入要求 CSRF、幂等键和显式确认。缺少 CRD 的列表返回稳定的不可用状态，但 ServiceAccount 已发现 CRD 后的
权限不足仍返回禁止访问，不能用“可选组件”语义掩盖 RBAC 拒绝。KEDA metadata 中疑似凭证的键在写入时被拒绝，
已有对象读取时只返回脱敏占位符；认证只能通过同 Namespace 的 TriggerAuthentication 名称引用。接口不读取该认证
对象或任何 Secret 正文，审计也只记录 ScaledObject 身份和结果。

HPA 指标趋势只是对受 `cluster.read` 保护的 HPA status 进行有界采样，没有新增越权的数据源；每个序列最多 240 点、
保留一小时，Server 进程内最多 2000 个序列并淘汰最旧项。它不读取 Metrics API、Pod 日志或 Secret，也不持久化。

ResourceQuota、LimitRange、NetworkPolicy、PodDisruptionBudget 与 PriorityClass 同样沿用 `cluster.read` 和
`cluster.resource.create/update/delete`：这五类对象约束工作负载可以做什么，但不能提升调用者自身在 ZKE 或
Kubernetes 中的权限，因此不引入独立权限位，也不从通用 Resource/YAML 入口排除。HTTP 与领域层同时校验作用域，
前四类必须指定 Namespace，PriorityClass 必须是集群级。更新替换整份托管 spec，并在写入前重新读取对象核对
UID/resourceVersion；ResourceQuota 的 scopes、PriorityClass 的 value 和 PodDisruptionBudget 的 selector 不接受
类型化修改。实际写入要求 CSRF、幂等键和显式确认；审计记录资源身份与结果，不记录 spec 正文。

Kubernetes Secret 使用独立的 `cluster.secret.read` 与 `cluster.secret.manage`，不由 `cluster.read` 或
`cluster.resource.*` 蕴含：能读工作负载配置和能读凭证是两个问题，一个角色不该因为前者顺带获得后者。通用
Resource 与 YAML API 对 Secret 的拒绝保持不变，专用 Secret 服务是进程内唯一会在 Resource Stream 请求上设置
`secret_access` 的地方，而该字段在 Go 中不可导出。Agent 侧再判定一次：只在带该字段时才动 Secret。Agent 自身
命名空间不再按名称永久拒绝，而由 `cluster.agent_namespace.manage` 与 Secret 专用权限共同控制；这允许被明确授权的
恢复操作，同时避免普通资源角色接触 Agent 凭证。`app.kubernetes.io/managed-by=zke-server` 仅作为来源标记；调用者
持有 Agent Namespace 与 Secret 两项权限后可读写这类对象，但不能给普通对象新加该标记。列表不返回任何取值，详情
返回的取值默认在界面上遮蔽。

Secret 的**读取**同样写入审计，这是它与 ConfigMap 的区别所在：一次成功的读取本身就是全部暴露，权限可以收回，
已经交出去的凭证收不回来，事后唯一还能回答的问题就是谁在什么时候取走了它。列表与单对象读取记为两个不同的
动作（`kubernetes_secret.list` 与 `kubernetes_secret.read`），因为列表不返回任何取值——区分这两者就是区分
"浏览"与"取走"。审计记录发起者、Cluster、Namespace、对象名和结果，不记录任何键名或取值。

Secret 的 YAML 是一对独立路由，读要求 `cluster.secret.read`，写要求 `cluster.secret.manage`，不经过通用 YAML
入口——后者对 Secret 的拒绝没有放开。该路由使用 Secret 服务自己的资源访问，其只接受 `core/v1 Secret`，并保留
Agent 的 `secret_access` 判定。写入前另外拒绝改变 `type`、拒绝写入已 immutable 的对象、拒绝为对象添加
`app.kubernetes.io/managed-by=zke-server`。一份 YAML 会一次返回该 Secret 的全部取值，与详情接口逐键遮蔽的呈现
方式不同，但两者要求的是同一个 `cluster.secret.read`，也记入同一个 `kubernetes_secret.read` 审计动作——按更
显眼的那个动作筛选的人不该因此漏掉暴露面更大的那条路径；审计仍不记录正文。

目标集群内的 Kubernetes RBAC 使用独立的 `cluster.rbac.read` 与 `cluster.rbac.manage`，不复用普通
`cluster.read` 或 `cluster.resource.*`。ServiceAccount、Role、ClusterRole、RoleBinding、ClusterRoleBinding
从通用 Resource/YAML API 排除，只能通过固定资源类型和作用域的专用接口访问：类型化接口，或同样挂在
`cluster.rbac.read/manage` 上的专用 YAML 路由（按作用域分为命名空间级与集群级两条，作用域不符即拒绝）。
写入需要 CSRF、DryRun、确认、幂等、UID/resourceVersion 与审计；ServiceAccount 响应不返回 Secret 名称或正文。
Agent ClusterRole 不包含 `escalate`、`bind`、`impersonate`；类型化规则与 YAML 守卫同时拒绝这些 Verb 和
ServiceAccount Token，最终提权检查仍由 Kubernetes API Server 执行。ZKE 管理的命名空间级 Agent 授权对象在同时
持有 `cluster.rbac.manage` 与 Agent Namespace 权限时可以更新或删除；ClusterRole、ClusterRoleBinding 没有 Namespace
可供独立权限定域，因此仍保留对象级保护。提交的文档不能给普通对象添加
`app.kubernetes.io/managed-by=zke-server`。YAML 与
类型化接口执行同一套规则是这条路由能够存在的前提：两者若不一致，宽的那条就是另一条的旁路。

#### PolicyRule 中的 Secret：按调用者权限条件放行

Kubernetes RBAC 本身就是一种「把访问权交给别人」的手段，因此规则里出现 `secrets` 等于在发放 Secret 访问权。
这类规则要求调用者**在该 Cluster 上持有对应的 Secret 权限**，按 Verb 区分：

- 只读 Verb（`get`、`list`、`watch`）→ 要求 `cluster.secret.read`
- 其余 Verb 或通配符 `*` → 要求 `cluster.secret.manage`
- `resources: ["*"]` 视同点名 Secret（通配符覆盖 Secret 与点名它一样确实；不依赖「Agent 没有通配符权限」这个
  本层看不见的性质来兜底）

不满足时返回 `403 secret_rule_forbidden`，与格式错误的 `400` 区分开：规则本身是良构的，换一个持有该权限的调用者
提交就会被写入。这条限制与平台角色的提权天花板是同一条原则——**不得授出自己没有的权限**——只是作用在
Kubernetes RBAC 这一侧。类型化接口与 YAML 守卫共用同一份判定。

在此之前这类规则被无条件拒绝，代价是无法用 ZKE 给 ServiceAccount 授予读取自身配置 Secret 的权限——一个几乎每个
应用都需要的授权，运维只能退回 `kubectl`，而那比在 ZKE 里做更缺少审计。

#### 绑定不得指向 Agent 自身的 ClusterRole

创建绑定和**给已有绑定追加主体**都会拒绝指向 `zke-agent` 的 `roleRef`，`roleRef` 本身也不可改写。

这一条是 ZKE 独有的，Kubernetes 帮不上忙：创建绑定要求执行者持有目标角色的全部权限，而执行者正是 Agent 的
ServiceAccount，`zke-agent` ClusterRole 恰好就是它自己的权限集，所以 API Server 会放行。`managed-by` 标签也盖
不住——它保护那个 ClusterRole 不被改删，而绑定到它创建的是一个不带该标签的新对象。角色名取自安装器常量而非
字面量，重命名 ServiceAccount 不会让这道检查静默失效。

资源对象浏览器不引入新的权限面：资源目录与对象列表使用 `cluster.read`，YAML 编辑使用
`cluster.resource.update`，删除使用 `cluster.resource.delete` 并要求 UID/resourceVersion 前置条件、CSRF、
幂等键与显式确认；目标为 core/v1 Node 时按上文改用 `cluster.node.manage`。它能看到的范围就是通用 Resource
接口的范围——Secret 与 Event 被 Agent 拒绝，五类 Kubernetes 授权资源被 Server 从该入口排除；Namespace 的增删改按目标名称要求独立的普通、系统或 Agent 命名空间权限——因此
浏览器不会成为绕过 `cluster.rbac.*`、命名空间独立权限或敏感资源限制的旁路。CRD 判定所需的
`customresourcedefinitions` 只读权限属于 Agent ServiceAccount，不改变调用者在 ZKE 中的权限。

`pkg/server/httpapi.TestDiscoveryOmitsAuthorizationResources` 断言资源目录不返回那五类资源，
`TestDiscoveryNarrowsNamespaceVerbs` 断言它不为 Namespace 报告 `create`、`delete`、`patch`。两者守的是
「浏览器被显示了什么」，与守请求路径的 `TestGenericResourceIdentityRejectsAuthorizationResources` 和
`TestGenericMutationIdentityRejectsNamespaceCreateAndDelete` 互为另一半：只有请求侧的拒绝仍会让不该出现的类型
出现在资源树里，点开才发现打不开。

通用 YAML 读取沿用 `cluster.read`，更新沿用 `cluster.resource.update`（Node 改用 `cluster.node.manage`），
不扩大 Agent ServiceAccount 权限；
Secret 与 Kubernetes 授权资源从该入口排除，避免绕过 `cluster.secret.*` 与 `cluster.rbac.*`，它们的 YAML 由上文
各自的专用路由提供。
更新只接受有界的严格单文档 YAML，并在发往目标 Cluster Agent 前，将正文的 GVR、Namespace、名称、UID 与
`resourceVersion` 和当前实时对象逐项核对；同名对象已重建或版本已变化时返回冲突。实际更新还要求 CSRF、
幂等键与显式确认，DryRun 使用同一 API Server 校验链路。日志与审计均不记录 YAML 正文或字段值。

#### 多文档清单：逐文档判权，整份拒绝

`POST /api/v1/clusters/{cluster_id}/kubernetes/manifests/apply` 与 `.../manifests/delete` 是平台里**唯一**
所需权限无法由路由决定的写接口。其余每个写路由都在 URL 里点名了一个族的一个对象，因此
`RequireCluster` 能在 handler 运行前判完；一份清单同时携带 Deployment、Secret 和 RoleBinding，而这三者正是
类型化 API 刻意分开的三个权限。

因此权限在两处判定，缺一不可：

- **路由层**只要求 `cluster.read`。它是下限——确认调用者至少看得见这个 Cluster——并为看不见的调用者产生
  与其他路由一致的拒绝审计。它不是这两个接口的授权。
- **逐文档层**由 `ResolveClusterManifestGrant` 一次性解析
  `cluster.resource.create/update/delete`、`cluster.namespace.manage`、`cluster.node.manage`、
  `cluster.secret.read/manage` 与 `cluster.rbac.manage`（与 Secret Grant 同一套「只解析、不拒绝」的中间件
  模式），再对每个文档判定它所属族需要的权限。

映射与类型化 API 完全一致，没有为清单放宽任何一条：

| 文档                | apply 需要                                                               | delete 需要                |
| ------------------- | ------------------------------------------------------------------------ | -------------------------- |
| Secret              | `cluster.secret.manage`                                                  | `cluster.secret.manage`    |
| Kubernetes 授权五类 | `cluster.rbac.manage`                                                    | `cluster.rbac.manage`      |
| Namespace           | `cluster.namespace.manage`                                               | `cluster.namespace.manage` |
| Node                | `cluster.node.manage`                                                    | `cluster.node.manage`      |
| Event（两个 group） | 一律拒绝                                                                 | 一律拒绝                   |
| 其余主资源          | 对象不存在时 `cluster.resource.create`，存在时 `cluster.resource.update` | `cluster.resource.delete`  |

这比路由级判定**更严**而不是更松：只要清单中有一个文档不被覆盖，整份清单被拒绝并返回 403，**不写入任何
对象**——包括调用者本来有权写的那些。放行「有权的部分」等于由权限而不是操作者决定了一次部分应用，而落地的
恰好是没人单独要求过的那一半。

同一条「整份拒绝」规则也适用于**无法解析成请求的文档**（未知 Kind、缺名称、`generateName`、清单内重名、
集群不支持该 Verb），返回 400。理由从另一侧到达同一处：带有拼错 Kind 的文件是操作者马上要修正后重新提交的，
先把其余九个对象写进去，只会让这次重新提交面对一个已经改了一半的集群。

DryRun 是例外，且只在**响应形态**上例外：无论文档是被拒绝还是无法解析，DryRun 一律返回 200 与逐文档判定，
因为这正是 DryRun 存在的目的——说清楚是哪个文档、缺哪个权限。它同样什么都不写，这才使得这个区别是安全的。
实际写入则以状态码作答：请求改变集群却什么都没改变，不该要求调用者仔细读一个 200 的正文才发现。

Namespace 对 apply 一律要求与目标名称对应的独立命名空间权限（而不是按 Update 放行），因为服务端 Apply 在对象
不存在时会创建它；若按「已存在就用弱权限」判定，操作者只要应用两次就能改变授权结果。

`secretAccess` 仍是包外无法设置的标记。清单服务位于 `pkg/server/kubernetesmanifest`，
够不到它们；打开这两道边界的唯一入口是 `kubernetesresource.ManifestAccess`，且只对**已经通过该族权限判定**的
文档打开。边界没有被放宽，只是被同一套判定从另一条路径抵达。

各族自己的守卫同样在清单路径上重跑，不是只在类型化路径上跑：普通对象不得声明 ZKE 的 `managed-by` 标签、
Secret 的 `type` 与不可变性、RoleBinding 的 `roleRef` 不可改（创建时不适用，
因为无从谈起「改」）、以及 PolicyRule 中的 Secret 授予必须由调用者自身持有对应 `cluster.secret.*`——
最后一条尤其关键：少了它，一份清单就会把 `cluster.rbac.manage` 变成平台里所有 Secret 权限。

执行语义与审计：apply 按文档顺序、delete 按文档反序逐条执行，遇到第一个失败即停止；Kubernetes 没有事务，
已写入的对象不回滚，响应逐条报告 `succeeded`、`failed` 与 `not_attempted`。delete 先读取对象并携带读到的
UID 与 `resourceVersion` 作为前置条件，对象已不存在记为跳过而非失败。幂等键按「请求键 + 操作 + 是否 DryRun +
对象身份」派生，因此重排文件不会让某个对象沿用另一个对象的键，DryRun 与实际执行也不会共用键。

审计的粒度按**集群中是否真的发生了变化**决定，两条都不新增动作名（apply 记为 `kubernetes_resource.patch`——
服务端 Apply 本就是一次 Patch，delete 记为 `kubernetes_resource.delete`，DryRun 记为对应的 `.dry_run` 变体）：

- **DryRun 与被拒绝的请求各写一条聚合记录。** 两者都没有改变任何对象，也都是会被反复发起的请求——DryRun 在
  操作者修正文件时反复点，被拒绝的请求在权限补齐前反复试。逐文档记录会让每次预检往审计表里写几十行，把真正
  写入了对象的记录淹没在其中。聚合记录仍然带有发起者、Cluster、操作类型、文档总数，以及被拒绝的文档数——
  最后这个数字是这条记录与「什么都没做」的区别，它说明触到了一次权限边界。
- **实际执行逐文档记录。** 对象确实变了，审计就必须逐个点名，包括失败的和因首错停止而未执行的那些——一次中途
  停下的执行走到了哪里，正是审计要回答的问题。删除时对象本就不存在的文档不记录：没有向集群发出任何请求。

无论哪种粒度都不记录 YAML 正文。

#### AIOps 资源写入：预检快照与动态权限

AIOps 不新增 Kubernetes 写权限，也不直接调用 Agent。Manifest 工具复用上述清单服务，运行时先要求 `cluster.read`，
工具再为当前用户解析 create/update/delete、Namespace、RBAC、系统 Namespace 与 Agent Namespace 权限，并由
`ManifestAccess` 逐文档选择实际需要的一项。工作负载回滚同样按目标 Namespace 在
`cluster.resource.update`、`cluster.system_namespace.manage` 与 `cluster.agent_namespace.manage` 中选择有效权限。
工作负载伸缩使用完全相同的选择规则，不再按 Namespace 名称永久禁止。
权限在预检和实际提交前各解析一次；预检后撤权会使提交拒绝，而不是沿用会话开始时的授权结果。

Manifest Apply/Delete 和回滚的预检成功后只返回一个随机 `preview_id`。对应快照仅保存在 Server 内存中，默认 15 分钟
有效并绑定发起用户、会话 Cluster、操作类型及原始 YAML/回滚前置条件；实际工具只接受该 ID，模型不能在审批后替换
正文、对象或 revision。提交会再次 DryRun，首次提交固定幂等键，并发重复提交会拒绝；成功重试返回缓存结果，不再向
Agent 发出第二次写入。进程重启会使尚未提交的预检失效，操作者需要重新预检。

删除始终属于敏感操作；RBAC、受保护 Namespace 和强制字段接管按预检结果升级为敏感操作，因此“帮我批准”模式仍会
停下来等待用户确认。Manifest 调用按解析出的每个对象写入目标与结果，逐文档权限拒绝记为 `denied`，失败和成功也
分别记录；审计只保存工具、目标、作用域和结果，不保存 YAML 正文或字段差异值。AIOps 对 Secret 清单一律在进入清单服务前拒绝，即使调用者持有
`cluster.secret.manage`；这是为了不让 Secret 明文进入模型上下文和 append-only 轨迹，Secret 写入继续只走专用入口。

Pod 日志读取不复用宽泛的 `cluster.read` 或通用资源写权限。请求必须明确 Cluster、Namespace、Pod 当前 UID
和容器，Server 与 Agent 通过独立日志协议执行，最终还受 Agent ServiceAccount 的 `pods/log` 最小权限约束。
实时 Follow 会周期重新验证 Session 和 `cluster.pod.logs.read`，权限收回后立即取消。成功与失败审计记录目标
作用域和结果，但不记录或摘要日志正文；鉴权拒绝使用同名权限动作进入 `denied` 词表。

Web Terminal 使用独立的 `cluster.pod.exec`，不复用 `cluster.read` 或 Kubernetes 通用写权限。创建一次性票据
必须通过 CSRF、幂等键和显式确认；WebSocket 必须同源并使用固定子协议，票据绑定用户、登录 Session、Cluster、
Namespace、Pod UID 和容器且只能消费一次。长会话周期重新验证 Session 与权限，设置空闲/总时长、输入/输出
字节和并发上限。Agent 只使用固定 Shell 选择逻辑（优先 bash，回退 `/bin/sh`），默认 ServiceAccount 仅增加
`pods/exec` 的 `get/create`，分别用于 WebSocket 与 SPDY。审计记录票据创建、会话目标与结果，默认不记录终端输入输出。

独立终端 App 使用另一项 `cluster.terminal.exec`，可通过现有自定义角色分配。该权限只允许创建终端会话，不等于
Kubernetes 资源权限。Server 在目标 Cluster 上逐项计算当前登录用户实际持有的 `cluster.*` 权限，Agent 将其按
固定白名单投影到会话专属 ServiceAccount 的 Role/ClusterRole。例如，没有 `cluster.secret.read` 的用户即使可以
进入终端，`kubectl get secret` 仍由 Kubernetes RBAC 拒绝；`cluster.secret.manage` 也不隐含读取。

终端必须明确目标 Cluster。Terminal Pod 与专属 ServiceAccount 固定落到 Agent 身份 Namespace；命名空间级权限通过
三组 RoleBinding 按普通、`kube-*` 与 Agent Namespace 分别投射，写入受保护命名空间必须持有对应独立权限，敏感资源
继续叠加其专用权限。Pod 不挂载宿主机目录，以非 root、
禁提权、丢弃全部 Linux capabilities 和受限资源运行。会话权限在长连接中周期重验；入口权限或快照内任何权限被
收回时立即断开并清理 ServiceAccount、RoleBinding 和 Pod。审计记录会话创建、目标和连接结果，不记录命令、
stdin/stdout 或 Secret 正文；逐条 `kubectl` API 请求审计依赖目标集群启用 Kubernetes Audit。

AIOps 的 `run_terminal_command` 复用同一终端资源模型，但不是交互式终端录制规则：模型请求的命令和有界输出会进入
append-only 轨迹并发送到配置的模型端点，所以该工具固定为敏感且可能变更，并且不投射
`cluster.secret.read/manage`。Agent Namespace 和系统 Namespace 不再附加 AIOps 特例，而是与容器服务、独立终端一样分别验证
`cluster.agent_namespace.manage` 和 `cluster.system_namespace.manage`。调用入口要求 `cluster.terminal.exec`；批准后重新计算当前 Cluster 权限，命令里的
`kubectl exec` 还必须由 Kubernetes `pods/exec` RBAC 验证 `cluster.pod.exec`。Namespace 级只读规则通过 ClusterRoleBinding 投射，因此 `cluster.read` 包含
`kubectl get ... -A`；写规则继续通过分类 RoleBinding 限制到会话建立时已存在的 Namespace。AIOps 命令容器不挂载
ServiceAccount Token，而是通过同 Pod、只监听 localhost 的凭证代理访问 API Server；代理明确拒绝对 `zke-terminal-*` 会话 Pod 的 exec、attach 与 port-forward。一个 Turn 首次执行命令时创建
Pod 并冻结权限快照，后续命令复用；Turn 结束、失败或取消时删除 Pod、ServiceAccount 和临时 RBAC，TTL 清理兜底。
周期重验覆盖命令执行和命令间空闲期，快照内任一权限被撤销都会取消命令、立即清理并阻止本 Turn 重新创建终端；新授予
权限要到下一 Turn 才会进入快照。撤权不会回滚此前已经完成的操作。审计保存工具名、会话、目标和结果，不复制命令或
输出正文。

终端输出录制必须由操作者显式选择，创建额外要求
`cluster.pod.terminal_recording.create`；内置角色中默认只有 `admin` 持有，自定义角色持有该权限时同样可以使用。录制只旁路保存 stdout/stderr，不保存 stdin、Cookie、一次性票据或认证头。
读取录制使用另一项 `cluster.pod.terminal_recording.read`，不从 `cluster.pod.exec` 推导，并在每次列表/详情请求上
重新执行项目范围内的 Cluster 判权和审计。数据库记录绑定不可变 Cluster UUID、Namespace、Pod 名称与 Pod UID，
详情还必须匹配 recording ID；记录有界且默认 7 天过期，列表不返回输出帧，只有详情读取暴露正文。

Pod 端口访问使用另一项 `cluster.pod.port_forward`；内置角色中默认只有 `admin` 持有，自定义角色持有该权限时同样可以使用。该权限不因持有 `cluster.read`、
`cluster.pod.exec` 或通用资源写权限而获得。浏览器 Pod Access 激活地址必须通过 CSRF、
幂等键和显式确认，并绑定用户、登录 Session、Cluster、Namespace、Pod UID、单个远端端口与用户从 15、30、
60 分钟中选择的时长，只能消费一次；所选时长不得超过 Server 配置的一小时硬上限，并进入幂等指纹与审计目标；
长连接或活跃访问会话周期重新验证 Session 和该权限。Agent ServiceAccount 只增加 `pods/portforward` 的 `get/create`，
Agent 在连接前再次核对 Pod UID，且只在回环地址建立临时桥接。

Server 以 Cluster UUID 与 Pod UID 唯一定域待激活和活跃入口。已存在入口时普通创建返回冲突，只有显式携带
替换确认的请求才会结束旧地址及其连接；该选择进入幂等指纹与审计。Pod Access 的会话级双向流量预算默认各
1 GiB，并受 Server 与 Agent 统一执行的 1 小时、1 GiB 传输硬上限约束。达到预算、权限撤销或上游失败会关闭
全部相关连接，内存中只短期保留
Cookie 摘要对应的终止原因，不保存 Token 或流量正文。

浏览器入口使用不承载任何 ZKE API 的独立 Pod Access Origin。激活 Token 与访问 Cookie 使用 256-bit 随机值并
短时有效，Token 消费后从 URL 清除；除短期幂等响应外，服务端只持有摘要。Listener 不信任 Pod 内容：进入 Pod
前删除 ZKE Session、CSRF 与其他非当前访问会话 Cookie，返回浏览器的 Pod Cookie 使用会话级命名空间并移除
Domain。即使 Access Listener 与 API 使用同一 IP 的不同端口——Cookie 本身并不按端口隔离——Pod 也拿不到
平台登录凭证，不能通过 `Set-Cookie` 覆盖它。`Clear-Site-Data`、HSTS、Alt-Svc 等会改变共享 Host 状态的响应头
同样由 Listener 删除，只能由部署入口设置。外部访问地址支持 HTTP 与 HTTPS，Listener 原生 TLS 也可由受信上游
网关的 TLS 终止替代；经过不可信网络时应使用 HTTPS 保护激活 Token 与访问 Cookie。双向流量正文、Token、Cookie、Authorization 和响应头不
进入日志、审计或 AI 上下文；审计只记录创建者、目标 Cluster/Namespace/Pod UID、端口、时长与会话结果。

Kubernetes Event 同样不复用 `cluster.read`。Server 和 Agent 的通用 Resource 接口会拒绝并从 Discovery 中
隐藏 `core/v1/events`，只能通过独立 Resource Watch 协议读取。普通请求必须明确 Cluster 和 Namespace，可使用受限
字段过滤器定域到具体资源；唯一的空 Namespace 例外是 Node describe 使用的一次性非 Follow 快照，且 Server、
协议校验和 Agent 都要求 `involvedObject.kind=Node` 与非空精确 UID。实时 Follow 周期重新验证 Session 与
`cluster.event.read`。Agent ServiceAccount 仅
增加 `events` 的 `get/list/watch`，Event 的 message 正文不写入日志或审计，审计只记录作用域、过滤目标和结果。

describe 接口（`.../pods/{pod_name}/describe`、
`.../workloads/{workload_resource}/{workload_name}/describe` 与
`.../nodes/{node_name}/describe`、`.../networking/{network_resource}/{network_name}/describe`、
`.../storage/{storage_resource}/{storage_name}/describe`、
`.../autoscaling/horizontalpodautoscalers/{hpa_name}/describe`、
`.../policies/{policy_resource}/{policy_name}/describe`、
`.../kubernetes/resources/{resource_name}/describe`）在一次响应里同时给出
对象与该对象的 Event，因此要求
调用方同时持有 `cluster.read` 与 `cluster.event.read`，两个检查都在路由层，各自留下自己的拒绝记录。只要求 `cluster.read` 会使 describe 成为绕开 Event 权限读取命名空间事件的
通道；而把 Event 当作可选部分静默省略，则会让缺少权限的调用者拿到一份看不出缺了什么的结论。Event 读取写入
与 Event 流一致的 `kubernetes_event.read` 审计记录，避免同一次读取因为走了另一个接口就在审计中消失。集群级
对象除已单独建模并受精确 UID 约束的 Node 外不返回 Event（`events.omitted=unsupported_scope`），Event 读取失败时返回对象部分并显式标注
（`events.omitted=unavailable`）。describe 的结论只从 Kubernetes 报告的 Condition、容器状态和 Event 读出，
消息原文照搬，不写入日志或审计。

工作负载的 describe 会读取它拥有的控制器与 Pod，以及 Pod 模板明确引用的 PVC，这不扩大任何权限边界：这些对象
都在同一个 Cluster 与 Namespace 内，走的是 `cluster.read` 已经覆盖的读取路径；控制器与 Pod 每一跳都按
controller owner UID 过滤，PVC 只按模板里的明确名称读取，因此不会把同一 Namespace 中其他工作负载的对象算
进来。关联对象与它们的事件读取次数都有上限，避免一次页面加载放大成对目标集群 Agent 的大量往返。

Service describe 只读取同一 Cluster 与 Namespace 中携带精确
`kubernetes.io/service-name=<Service name>` 标签的 EndpointSlice，以及由 Service selector 匹配的 Pod；响应中的
EndpointSlice 还会再次核对 Namespace 与 Service 标签，防止上游返回越界对象。Service 自身的 Event 仍按精确 UID
过滤，不读取后端 Pod 的 Event，也不把 selector 匹配解释为 Kubernetes owner 关系。

Ingress describe 将后端引用去重并限制为 20 个，只读取同一 Cluster 与 Namespace 的 Service 清单，以及携带这些
Service 名称标签的 EndpointSlice；两类清单各一次且上限 500。响应再次核对 Service 的资源类型、Namespace 和
EndpointSlice 的 Namespace、Service 标签。Service 清单分页时未找到的对象保持未知，EndpointSlice 分页时端点
计数仅作为下限，两者都不会产生确定性的缺失结论。Ingress Event 仍只按 Ingress 自身精确 UID 读取。

Gateway describe 不读取 Route 或引用对象，只使用 Gateway 详情中 Controller 已报告的对象/Listener Condition，
并按 Gateway 自身精确 UID 读取 Event。这样既不跨 Namespace 追踪证书或 Route，也不会把 Listener 的
`attachedRoutes` 计数解释成调用方有权读取那些 Route 的授权。

HPA describe 只在同一 Cluster 与 Namespace 内读取类型化接口已支持的 `apps/v1 Deployment` 或 `StatefulSet`
目标；自定义 scale target 不进行通用资源读取。目标读取只补充工作负载状态，不扇出读取目标 Event，HPA Event 始终
按 HPA 自身精确 UID 过滤；因此目标聚合不会扩大 Event 权限所覆盖的对象范围。

策略 describe 只接受同一 Cluster 与 Namespace 内的 ResourceQuota 和 PodDisruptionBudget，不读取它们选择或约束
的 Pod，也不读取其他关联对象；Event 只按策略对象自身精确 UID 过滤。LimitRange、NetworkPolicy 和 PriorityClass
没有类型化诊断路由，因此该入口不能被用作这些对象的额外读取路径。

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

可信反向代理来源解析和跨组织的细粒度委派管理仍属于后续工作。认证、用户与 RoleBinding 管理以及
审计查询后端已经实现。
