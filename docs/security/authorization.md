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
层级：对某个 Cluster 具有某项权限的用户，在该 Cluster 的所有 Namespace 上都具有该权限。因此
`cluster.namespace.manage` 是「在该 Cluster 内创建和删除 Namespace」，不是「管理某一个 Namespace」。详见
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

| 差 | 判定 | 拒绝 |
| --- | --- | --- |
| 新增（在新集合、不在旧集合） | 必须是调用者在 Global 已持有的 | `403 permission_escalation` |
| 移除（在旧集合、不在新集合） | 同上 | `403 permission_revocation` |
| 两个集合里都有 | 不判定，原样保留 | — |

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

判定在数据库事务内、对已存权限集加锁之后进行：在事务外读取旧集合会与并发修改比对出错误的差，另一次修改在
其间加入的权限会成为这次修改可以悄悄移除的对象。因此这条检查在 store 层，而不是与创建路径并列在服务层。

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

| 路径 | 执行的规则 |
| --- | --- |
| 删除用户、禁用用户、删除 RoleBinding | 两条都执行 |
| 授予 Global `admin` | 只有第二条——授予不会移除任何人 |
| 重置密码、解锁账户、修改显示名称 | 只有第二条 |

最后一行只执行第二条是必要的：夺取账号不等于移除账号，若把「必须保留一个」也套上，唯一的管理员将无法重置自己的
密码，也无法改自己的名字。

重置密码、解锁和改名为什么在这里，理由各不相同。重置密码是这条规则最初的来由：删除、禁用和解绑守的都是
RoleBinding，而重置全局管理员的密码再以其身份登录，拿到的是同一套权限，却不产生任何绑定可供那些检查看见——
`user.manage` 曾因此单独构成一条完整的提权路径。解锁按同样的理由归入：锁定是密码尝试的终止条件，从群体外部
清除它等于把这轮尝试还回去。

**修改显示名称不是因为它危险。** 改名什么也拿不到，这正是它当初被漏掉的原因，也正是它必须在这里的原因：把规则
写成「不得夺取该账号」，就等于让后来每一个新增操作各自判断自己够不够危险，而做这个判断的人正在看自己那个操作，
不是在看这个群体。写成「不得控制该账号」，就没有什么需要判断。

普通账号不受该规则约束，`user.manage` 承担的日常改名、密码重置和解锁照常可用。

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

### 权限的作用域下限

绑定可以选三种作用域，角色可以装任意权限，于是两者能被组合成一条永远不生效的授予。每项权限因此有一个**作用域
下限**——能够行使它的最窄绑定作用域，比它更窄的绑定携带它不生效。下限共两档，其余权限没有下限。

| 权限 | 下限 | 原因 |
| --- | --- | --- |
| `user.read`、`user.manage` | Global | 用户是全局对象，不属于任何租户 |
| `rbac.read`、`rbac.manage` | Global | 角色是全局对象，见上文提权天花板 |
| `tenant.create` | Global | 创建租户时还没有可供限定的租户 |
| `tenant.manage` | Global | 删除租户会级联移除其下全部项目、集群与凭证，**有意不交给租户自己的管理员** |
| `project.create` | Tenant | 创建项目时还没有可供限定的项目，但租户正是它可以被限定到的东西——所以它不是 Global |

`project.create` 是唯一一项 Tenant 下限，它的存在正是这里从「是否仅全局」改为「下限是哪一档」的原因：一个只含
`project.create` 的角色绑到 Project 上授予的恰好是零，而一个布尔标记会把它报成不受限。

其余权限都在与其名字相符的作用域校验：`project.read`、`project.manage` 在 Project，`cluster.*` 在
Cluster（经所属 Project 解析，因此等同 Project 作用域），`tenant.read` 与 `audit.read` 没有固定作用域路由，
改为在服务层按调用者的可见范围过滤。

**只有整个角色都由该作用域无法行使的权限组成时，绑定才会被拒绝**，返回 `400 role_unreachable_at_scope` 并列出
权限名——那样的绑定不授予任何权限，读起来却像一次授权，除了「写错了」没有别的解释。

混合角色不拒绝，这条界线是刻意的。内置 `admin` 绑到某个租户是一次真实且有用的授予：28 项里有 22 项在该租户内
生效，绑到项目则是 21 项。拒绝它等于让唯一表示「这里的全部权限」的角色无法用于任何作用域绑定，迫使每个部署手工
复制一份租户版 `admin`。问题从来不是授予是部分的，而是缺失的那部分不可见——所以修在它坏掉的地方：`me` 的作用域
能力列表不再包含该作用域行使不了的权限，权限字典为每一项返回 `minimum_scope`（`global`/`tenant`/`project`），
Console 在角色编辑器里标注「仅全局生效」或「仅全局和租户生效」，并在新建绑定时按本次选定的作用域列出不会授予的
权限。

已存在的绑定不受影响——这是创建路径上的检查，原本就不生效的权限继续不生效，而不是被一次升级夺走。

`pkg/server/httpapi.TestPermissionScopeFloorsMatchRoutes` 解析 `routes.go`，把每项权限被传入的授权 middleware
折算成下限，与 `rbac.MinimumScope` 逐项比对，确保这份清单与路由实际校验的作用域不会悄悄脱节。

### 访问管理接口

当前固定权限还包括 `user.read`、`user.manage`、`rbac.read`、`rbac.manage` 和 `audit.read`。用户、角色与
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
以及读写 Kubernetes Secret 使用的 `cluster.secret.read`、`cluster.secret.manage`，以及创建和删除
Kubernetes Namespace 使用的 `cluster.namespace.manage`。
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

#### Namespace 的创建与删除

类型化 Namespace 创建与删除沿用上述安全边界——实际操作必须确认，删除可携带 UID/resourceVersion 前置条件，
Console 在确认前先执行服务端 DryRun 并展示目标 Cluster 与影响——但使用独立的 `cluster.namespace.manage`，
不由 `cluster.resource.create/delete` 蕴含。

理由不是 Namespace 更敏感，而是影响面不在一个量级：其余 `cluster.resource.*` 写入作用于一个对象，删除一个
Namespace 则连同其中的全部对象一起移除，创建一个则新增了其余权限得以行使的作用域本身。把两者放在同一个权限位
上，等于「能改一个 ConfigMap」和「能清空一个命名空间」由同一次授予决定。

与 Secret、Kubernetes 授权资源不同，Namespace 的**读取没有被收窄**：列表与详情仍是 `cluster.read`，通用
Resource 与 YAML 接口照常返回它们。被从通用入口排除的只有会让 Namespace 出现或消失的三个动作——Create、Delete，
以及 Patch（服务端 Apply 会在对象不存在时创建它）；Update 保持开放，因为修改一个已存在 Namespace 的元数据既不
创建作用域也不销毁其中的内容，Namespace 的 YAML 编辑器正是走这条路径。若不做这条排除，
`cluster.namespace.manage` 将只是一道可以绕开的门。

排除在 Server 的两层各判一次：HTTP 层在读取请求处拒绝，领域层用一个包外无法设置的 `namespaceAccess` 标记拒绝，
只有类型化 Namespace 服务会设置它——与 Secret 的 `secretAccess` 同一套办法。资源目录另外把 `create`、`delete`、
`patch` 从 `core/v1 namespaces` 的 Verb 列表中去掉，使资源对象浏览器不会显示一个每次点击都被拒绝的删除按钮。

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
补 Kubernetes 权限就能解决。列表不返回任何取值，详情返回的取值默认在界面上遮蔽。

Secret 的**读取**同样写入审计，这是它与 ConfigMap 的区别所在：一次成功的读取本身就是全部暴露，权限可以收回，
已经交出去的凭证收不回来，事后唯一还能回答的问题就是谁在什么时候取走了它。列表与单对象读取记为两个不同的
动作（`kubernetes_secret.list` 与 `kubernetes_secret.read`），因为列表不返回任何取值——区分这两者就是区分
"浏览"与"取走"。审计记录发起者、Cluster、Namespace、对象名和结果，不记录任何键名或取值。

Secret 的 YAML 是一对独立路由，读要求 `cluster.secret.read`，写要求 `cluster.secret.manage`，不经过通用 YAML
入口——后者对 Secret 的拒绝没有放开。该路由使用 Secret 服务自己的资源访问，其只接受 `core/v1 Secret`，因此上述
平台对象过滤与 Agent 两条判定原样生效。写入前另外拒绝改变 `type`、拒绝写入已 immutable 的对象、拒绝为对象添加
`app.kubernetes.io/managed-by=zke-server`。一份 YAML 会一次返回该 Secret 的全部取值，与详情接口逐键遮蔽的呈现
方式不同，但两者要求的是同一个 `cluster.secret.read`，也记入同一个 `kubernetes_secret.read` 审计动作——按更
显眼的那个动作筛选的人不该因此漏掉暴露面更大的那条路径；审计仍不记录正文。

目标集群内的 Kubernetes RBAC 使用独立的 `cluster.rbac.read` 与 `cluster.rbac.manage`，不复用普通
`cluster.read` 或 `cluster.resource.*`。ServiceAccount、Role、ClusterRole、RoleBinding、ClusterRoleBinding
从通用 Resource/YAML API 排除，只能通过固定资源类型和作用域的专用接口访问：类型化接口，或同样挂在
`cluster.rbac.read/manage` 上的专用 YAML 路由（按作用域分为命名空间级与集群级两条，作用域不符即拒绝）。
写入需要 CSRF、DryRun、确认、幂等、UID/resourceVersion 与审计；ServiceAccount 响应不返回 Secret 名称或正文。
Agent ClusterRole 不包含 `escalate`、`bind`、`impersonate`；类型化规则与 YAML 守卫同时拒绝这些 Verb 和
ServiceAccount Token，最终提权检查仍由 Kubernetes API Server 执行。ZKE 管理的 Agent 授权对象禁止经该接口更新
或删除，YAML 路由只允许读取；提交的文档也不能给对象添加 `app.kubernetes.io/managed-by=zke-server`。YAML 与
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
幂等键与显式确认。它能看到的范围就是通用 Resource 接口的范围——Secret 与 Event 被 Agent 拒绝，五类 Kubernetes
授权资源被 Server 从该入口排除，Namespace 可以浏览和编辑但不能在此创建或删除——因此浏览器不会成为绕过
`cluster.rbac.*`、`cluster.namespace.manage` 或敏感资源限制的旁路。CRD 判定所需的
`customresourcedefinitions` 只读权限属于 Agent ServiceAccount，不改变调用者在 ZKE 中的权限。

`pkg/server/httpapi.TestDiscoveryOmitsAuthorizationResources` 断言资源目录不返回那五类资源，
`TestDiscoveryNarrowsNamespaceVerbs` 断言它不为 Namespace 报告 `create`、`delete`、`patch`。两者守的是
「浏览器被显示了什么」，与守请求路径的 `TestGenericResourceIdentityRejectsAuthorizationResources` 和
`TestGenericMutationIdentityRejectsNamespaceCreateAndDelete` 互为另一半：只有请求侧的拒绝仍会让不该出现的类型
出现在资源树里，点开才发现打不开。

通用 YAML 读取沿用 `cluster.read`，更新沿用 `cluster.resource.update`，不扩大 Agent ServiceAccount 权限；
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
  `cluster.resource.create/update/delete`、`cluster.namespace.manage`、`cluster.secret.read/manage` 与
  `cluster.rbac.manage`（与 Secret Grant 同一套「只解析、不拒绝」的中间件模式），再对每个文档判定它所属族
  需要的权限。

映射与类型化 API 完全一致，没有为清单放宽任何一条：

| 文档 | apply 需要 | delete 需要 |
| --- | --- | --- |
| Secret | `cluster.secret.manage` | `cluster.secret.manage` |
| Kubernetes 授权五类 | `cluster.rbac.manage` | `cluster.rbac.manage` |
| Namespace | `cluster.namespace.manage` | `cluster.namespace.manage` |
| Event（两个 group） | 一律拒绝 | 一律拒绝 |
| 其余主资源 | 对象不存在时 `cluster.resource.create`，存在时 `cluster.resource.update` | `cluster.resource.delete` |

这比路由级判定**更严**而不是更松：只要清单中有一个文档不被覆盖，整份清单被拒绝并返回 403，**不写入任何
对象**——包括调用者本来有权写的那些。放行「有权的部分」等于由权限而不是操作者决定了一次部分应用，而落地的
恰好是没人单独要求过的那一半。

同一条「整份拒绝」规则也适用于**无法解析成请求的文档**（未知 Kind、缺名称、`generateName`、清单内重名、
集群不支持该 Verb），返回 400。理由从另一侧到达同一处：带有拼错 Kind 的文件是操作者马上要修正后重新提交的，
先把其余九个对象写进去，只会让这次重新提交面对一个已经改了一半的集群。

DryRun 是例外，且只在**响应形态**上例外：无论文档是被拒绝还是无法解析，DryRun 一律返回 200 与逐文档判定，
因为这正是 DryRun 存在的目的——说清楚是哪个文档、缺哪个权限。它同样什么都不写，这才使得这个区别是安全的。
实际写入则以状态码作答：请求改变集群却什么都没改变，不该要求调用者仔细读一个 200 的正文才发现。

Namespace 之所以对 apply 一律要求 `cluster.namespace.manage`（而不是像通用 YAML 更新那样按 Update 放行），
是因为服务端 Apply 在对象不存在时会创建它——那正是 `deniedNamespaceWrite` 存在的那一半；若按「已存在就用弱
权限」判定，操作者只要应用两次就能满足它。

`secretAccess` 与 `namespaceAccess` 仍是包外无法设置的标记。清单服务位于 `pkg/server/kubernetesmanifest`，
够不到它们；打开这两道边界的唯一入口是 `kubernetesresource.ManifestAccess`，且只对**已经通过该族权限判定**的
文档打开。边界没有被放宽，只是被同一套判定从另一条路径抵达。

各族自己的守卫同样在清单路径上重跑，不是只在类型化路径上跑：对象不得声明 ZKE 的 `managed-by` 标签、ZKE 自身
的 Secret 与授权对象不可读写删、Secret 的 `type` 与不可变性、RoleBinding 的 `roleRef` 不可改（创建时不适用，
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
