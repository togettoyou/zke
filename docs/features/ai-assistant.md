# AIOps

> 当前已完成模型配置、跟随桌面 Tenant/Project 并按 Cluster 隔离的会话与轨迹存储、模型自主工具循环
> （读取工具目录、Helm Release 读取与受控变更、工作负载伸缩/回滚和 Manifest 写操作）、敏感工具的审批等待、
> 流式输出、轨迹时间线、Cluster Terminal 受控非交互命令、变更时间线与变更后验证、随 Server 发布的排查技能、只读并行子任务，
> 以及由模型自行判断的主动打开桌面应用；
> 定时巡检与事件触发自动化仍在规划中。

AIOps 是 ZKE 中的云端 Codex 式运维 App：一个把目标 Cluster 当作工作区的 Agent。用户用自然语言提出问题，模型
自己决定读取哪些对象、Event、日志和指标，按什么顺序读取，读到什么程度算够；每一次调用、授权判断和返回结果都
写进 append-only 轨迹。

## 主要体验

- 左侧选择 Cluster 工作区，只展示该 Cluster 的会话，按最近活动分组；搜索是列表标题栏上的一个图标，需要时展开为
  输入框，已归档会话则收在同一行的显示选项里，不占据常驻位置；侧栏可收起为图标栏，保留展开、新对话与搜索；
- 会话在第一轮结束前由模型命名，列表里看到的是这次对话在查什么，而不是打开它的时刻；操作者手动改过标题的会话
  不会被再次改名；
- 重命名、归档、导出与删除都在会话行悬停出现的菜单里，作用于该行会话，不必先把它打开；
- 打开 AIOps 就有输入框：还没有会话的工作区，和刚建好、尚未提问的新会话，是同一个界面——同样以当前 Cluster
  命名的标题、同样四张起始问题卡片、以及位置和宽度都相同的输入区；在前者里发送本身就是创建会话，审批模式也可以
  先在这里选好。起始问题卡片只把问题写进输入框并把光标交给操作者，改完再自己发送，不会点一下就发出去；输入框
  不会在打开时自动抢焦点。没有在线 Cluster 时输入框停用并说明原因，卡片不再显示；
- 主工作区用“对话 / 轨迹”Tab 切换。对话把模型说明、工具调用与结果、审批请求和结论按发生顺序排成一条线索，
  工具调用默认折叠，展开可看参数、原始返回和证据入口；提问、回答、工具参数与结果、代码块都可一键复制，回到
  底部是一枚只在下方还有内容时出现的圆形按钮；
- 模型输出按 token 流式显示，落库后由持久条目接管，关闭窗口不影响记录；
- 需要看图或看对象才说得清的结论，AIOps 会自己打开对应的应用：指标直接打开监控并执行查询，对象打开容器服务。
  对话里同时留下一张卡片，写清打开了什么和为什么；你当时没在看 AIOps 时它不会抢画面，卡片上的按钮仍在；
- 输入区一行放齐会影响下一轮行为的东西：文本附件、审批模式、可用工具目录、可用技能、发送与停止；切到轨迹时
  输入区隐藏，把高度让给记录本身；
- 敏感工具在“请求批准”和“帮我批准”模式下会停下来等待，对话里直接批准或拒绝；拒绝不是失败，模型会在不执行它
  的前提下继续；同时有多个调用在等待时，顶部横幅给出数量，每一条各自答复；
- 并行子任务折叠在派发它们的那次工具调用里：展开可以看到每个分支的目标、读了什么和结论；分支自己的审批请求仍然
  留在对话里，并写明是哪一个分支在请求；
- 轨迹 Tab 顶部是输入 / 模型 / 工具三条时间轨：可以像指标图表那样横向拖出时间窗口来筛选记录，滚轮缩放、右键
  拖动平移，双击或 Esc 复位；点一个块直接定位到对应记录。时间轨可在“真实耗时”与“等宽”两种排布间切换，后者
  用于点选零耗时的瞬时事件；流式模型步骤的块会把首 token 之前的等待与其后的生成分成深浅两段；
- 轨迹列表按轮次分组并可整体折叠，模型步骤与它请求的工具调用折叠为一条，右侧详情面板分“概要 / 预览 / 原文”
  三页；类型筛选、全文搜索与时间窗口三者共同作用于时间轨与列表，底部状态栏给出本次运行的耗时、轮次、步骤、
  工具调用、首 token 时延、吞吐、缓存命中和 token 用量；
- 任务在关闭窗口后继续，重新打开从上次轨迹位置接续；整个会话可导出为 JSON。

附件只接受文本、Markdown、JSON 和 YAML，单个最多 256 KiB；进入模型上下文时始终标记为不可信数据。导出前会重新
检查当前 Project、会话固定 Cluster 和每项证据权限，不能通过导出取回已失权的集群正文。

## 桌面作用域与会话 Cluster

AIOps 与容器服务一样使用 Console 当前 Tenant 和 Project，并在 App 内选择一个 Cluster 作为工作区。切换 Cluster 后，
列表与搜索只返回新 Cluster 的会话。创建会话时必须选择在线 Cluster；会话创建后不可迁移到其他 Cluster，也不能引用
或操作其他 Cluster。会话只能先归档，再从归档列表永久删除；服务端拒绝直接删除活动会话。删除对话框仍然写明目标
作用域与影响，但不再要求输入会话名称二次确认——归档本身就是那道闸门。

每次模型工具调用都由该 Cluster 的 Agent 定域执行，运行时不持有 kubeconfig，也不直连 Kubernetes API Server。

## 工具目录

工具目录由 Server 维护，模型只能从中选择，不能安装或定义新工具。`GET /api/v1/ai/tools` 返回当前部署组装出的
目录以及 `enabled`——平台是否已启用 AIOps 并配置了模型端点，Console 据此决定是否在桌面展示 AIOps 应用。
目录本身在输入区展示。当前工具包括：

| 工具 | 作用 | 需要的权限 |
| --- | --- | --- |
| `cluster_overview` | Cluster 整体快照：Node、Namespace、Pod、工作负载与存储的数量和状态分布 | `cluster.read` |
| `list_api_resources` | 目标 Cluster 支持的 API 资源类型，含 CRD | `cluster.read` |
| `list_resources` | 按 Kind 列出对象，返回名称、创建时间与状态摘要 | `cluster.read` |
| `get_resource` | 读取单个对象的完整定义 | `cluster.read` |
| `describe_resource` | 对象的关键状态、ZKE 归纳的问题点和指向它的 Event | `cluster.read` + `cluster.event.read` |
| `list_nodes` | Node 状态、可调度性、容量与 kubelet 版本 | `cluster.read` |
| `get_pod_logs` | Pod 容器日志尾部（敏感），按 Pod 实例身份读取；单容器 Pod 可以省略容器名 | `cluster.pod.logs.read` |
| `list_metric_queries` | 可用的指标查询目录，每行一个查询：查询名、标题、单位与参数标记 | `cluster.metrics.read` |
| `query_metrics` | 执行目录中的一个查询，返回每条曲线的最新值、峰值与均值 | `cluster.metrics.read` |
| `query_custom_metrics` | 执行一条自定义 MetricsQL；Server 把会话 Cluster 强制注入每个选择器，模型不提供 Cluster ID | `cluster.metrics.read` |
| `list_cluster_changes` | 当前 Cluster 的变更时间线：合并普通提交与 AIOps 写工具调用，不把 DryRun 当作变更 | `audit.read` |
| `verify_resource_change` | 验证对象当前健康、变更后 Warning Event、工作负载 generation 与副本收敛，返回三态结论 | `cluster.read` + `cluster.event.read` |
| `preview_workload_scale` | 对 Deployment/StatefulSet 目标副本数执行 Kubernetes 服务端 DryRun，不改变集群 | 普通 Namespace 使用 `cluster.resource.update`，受保护 Namespace 改用 system/agent manage |
| `scale_workload` | 实际调整 Deployment/StatefulSet 副本数；提交前内部再次执行同参数 DryRun | 同预检；实际伸缩始终按敏感操作处理 |
| `list_workload_revisions` | 读取 Deployment/StatefulSet/DaemonSet 历史版本及回滚并发前置条件 | `cluster.read` |
| `preview_workload_rollback` | 对指定 revision 执行 DryRun 并生成绑定用户和 Cluster 的预检快照 | `cluster.read` + 按 Namespace 选择 update/system/agent manage |
| `rollback_workload` | 使用 `preview_id` 提交回滚；提交前重验权限和 DryRun | 同预检；受保护 Namespace 属于敏感操作 |
| `preview_manifest_apply` | 严格解析多文档 YAML，逐文档判权、DryRun 并返回动作与有界字段路径差异 | `cluster.read` + 按文档选择 create/update/Namespace/Node/RBAC/受保护 Namespace 权限；RBAC 规则涉及 Secret 时还需对应 Secret 权限 |
| `apply_manifest` | 使用 `preview_id` 提交原始 Manifest；批准后重新逐文档判权和 DryRun | 同预检；RBAC、受保护 Namespace 或 force 属于敏感操作 |
| `preview_manifest_delete` | 逐文档判权并 DryRun 预检删除 | `cluster.read` + 按文档选择 delete/Namespace/Node/RBAC/受保护 Namespace 权限 |
| `delete_manifest` | 使用 `preview_id` 删除预检中的对象 | 同预检；始终为敏感操作 |
| `list_helm_releases` | 某个 Namespace 中安装的 Helm Release：名称、当前 revision、状态与最后写入时间 | `cluster.read` + `cluster.secret.read` |
| `list_helm_release_revisions` | 一个 Release 的修订历史，并标出当前版本 | `cluster.read` + `cluster.secret.read` |
| `get_helm_release` | 一个 revision 的 Chart 名称与版本、appVersion、状态说明、部署时间、被覆盖的 values 路径与渲染出的对象清单（敏感） | `cluster.read` + `cluster.secret.read` |
| `list_helm_repositories` | 平台维护的 Chart 仓库目录：`repository_id`、名称与是否启用 | 全局 `helm.repository.read` |
| `list_helm_charts` | 某个仓库中可安装的 Chart：名称、最新版本、appVersion 与简介 | 全局 `helm.repository.read` |
| `list_helm_chart_versions` | 一个 Chart 已发布的版本与发布时间 | 全局 `helm.repository.read` |
| `get_helm_chart` | 一个 Chart 版本的元信息与它自带的 `values.yaml` 默认值 | 全局 `helm.repository.read` |
| `preview_helm_install` | 对一次 Helm 安装执行 Helm 自己的 DryRun，返回将创建的对象清单与 `preview_id`，不改变集群 | `cluster.read` + `cluster.helm.manage` + `cluster.secret.manage`，再按 Namespace 选择 create/update 与受保护 Namespace 权限 |
| `preview_helm_upgrade` | 对一次升级执行 DryRun；只换 Chart 版本时用 `reuse_values` | 同上 |
| `preview_helm_rollback` | 对指定 revision 的回滚执行 DryRun | 同上 |
| `preview_helm_uninstall` | 对一次卸载执行 DryRun，返回将删除的对象清单 | `cluster.read` + `cluster.helm.manage` + `cluster.secret.manage` + `cluster.resource.delete`，受保护 Namespace 再叠加 |
| `apply_helm_release_change` | 使用 `preview_id` 提交已预检的安装 / 升级 / 回滚 / 卸载；批准后重新判权并再次 DryRun | 同对应预检；始终按敏感操作处理 |
| `run_terminal_command` | 在本 Turn 复用的 Cluster Terminal 中执行一条非交互 Shell 命令，可使用 kubectl、BusyBox、curl 与 jq | `cluster.terminal.exec`；命令内 Kubernetes 操作再由本 Turn 冻结的权限快照决定，`kubectl exec` 还需 `cluster.pod.exec` |
| `load_skill` | 读取一份 ZKE 发布的排查流程；只说明用哪些既有工具、按什么顺序取证 | `ai.run`（它不读取任何集群内容） |
| `run_subtasks` | 派发最多 3 个只读并行取证分支，汇总各自的结论、证据与失败分类 | `ai.run`；分支内的每次读取仍按该工具自己的权限逐次校验 |
| `open_console_view` | 在操作者当前桌面上打开一个 ZKE 应用并定位到指定视图，指标可在打开后直接执行查询 | `ai.run`（它不读取集群内容）；打开的视图按目标类型再校验 `cluster.read` / `cluster.event.read` / `cluster.metrics.read` / `cluster.pod.logs.read` / `cluster.secret.read` |

部署没有安装多集群指标时，指标工具不会出现在目录里，而不是出现后每次调用都失败。工具输出经过摘要与截断，
超出单次上限时会明确告知模型，让它缩小范围重读，而不是把截断当成完整事实。

**指标目录是唯一一个必须完整返回的工具输出。** 其余读取工具回答的都是调用方指名的东西——某个 Namespace、
某个选择器、某个条数上限——因此被截断之后可以问得更窄再读一次；而这一个是索引本身：看不见名字的查询就没法
被调用，截断保留的又是首尾，消失的正是目录中间那一段，且没有任何地方会报告这件事。所以它按每行一个查询
渲染，并且只写出调用方必须照做的标记（`ns`/`ns!`/`top`/`top!`/`instant`/`ksm`/`node`），而不是把九个字段
逐个拼成 JSON。目录会继续变长，因此「整份放得进一次工具结果」由一个单元测试守着，而不是靠人记得。

AIOps 读到的就是 Console 图表读的那一份目录：查询目录由 Server 现场枚举，新增查询不需要在 AIOps 侧登记，
权限也仍然是 `cluster.metrics.read` 按目标 Cluster 逐次校验。目录无法回答的问题可以调用 `query_custom_metrics`
书写一条 MetricsQL；工具 Schema 不接受 Cluster ID，Server 复用监控「数据探索」的查询服务，把表达式里已有的
`zke_cluster_id` 条件替换为会话固定 Cluster，并再次强制注入存储请求。模型引用的每条指标都会作为证据落进轨迹；
具名查询点开对应图表分区，自定义表达式点开「数据探索」并带回原表达式。

## 变更时间线与变更后验证

`list_cluster_changes` 从部署审计中读取会话固定 Cluster 的真实变更，默认回看 120 分钟、最多回看 7 天，按时间倒序返回
发起者、动作、目标、结果和 request ID。时间线包含普通 Kubernetes 资源写入、Helm 安装/升级/回滚/卸载、节点排空、
Pod 驱逐、采集组件安装/卸载等动作；AIOps 不经过 HTTP 写路由，因此它自己的写入从 `ai_tool.invoke` 审计记录中按
`mutating=true` 单独选出，再与普通变更合并。DryRun 和只读工具调用都不进入时间线。默认只返回成功提交，操作者明确
排查失败尝试时才包含失败记录。

时间线要求当前用户对该 Cluster 所属作用域具有 `audit.read`。工具仍通过审计服务解析调用者的 Global/Tenant/Project
可见范围，不直接读取数据库；`ai.run` 或 `cluster.read` 都不能替代 `audit.read`。

`verify_resource_change` 接收明确的 apiVersion、Kind、Namespace、名称与变更时间，复用对象诊断链路读取当前对象、
关联对象和指向它们的 Event，给出以下三种结果：

- `passed`：已建模对象的当前健康规则没有 Finding，变更后没有 Warning Event，工作负载 generation 与副本已经收敛；
- `warning`：发现当前 Finding、变更后 Warning Event、未观察到最新 generation、未收敛副本或未就绪关联对象；
- `inconclusive`：对象类型没有健康规则、Event/关联对象窗口不完整、部分诊断读取失败，或观察时间不足一分钟。

`passed` 只说明**当前 Kubernetes 状态与可见 Event 未发现问题**，不表示业务延迟、错误率或资源用量没有退化。随 Server
发布的「变更时间线与变更后验证」技能会继续调用指标目录与指标查询，用覆盖变更前后的小窗口检查副本不可用、Pod 重启、
CPU、内存和可用的业务指标；指标返回 `partial`/`issues` 时同样不能下“验证通过”的结论。该流程只读，不会自动回滚；
需要回退时仍进入受控变更流程，重新 DryRun、审批和提交。

**Helm Release 是目录里唯一一个只回答“是什么”、不回答“里面是什么”的读取。** Release 不是 Kubernetes 的一种
Kind，其余工具都答不了它：资源列表看到的是 Chart 渲染出的 Deployment，而 Deployment 上没有任何指回 Release 的
引用——所以“这个工作负载属于哪个应用、Chart 是哪个版本、最近是不是升级过”在别处无解，而两小时前坏掉的滚动更新
往往就是两小时前的一次 Helm 升级。Helm 把 Release 存成 Secret，这决定了两件事：权限上，三个工具都要求
`cluster.read` 加 `cluster.secret.read`，与 Console 的 Release 路由完全一致；内容上，values 取值、NOTES.txt 与
渲染后的 Manifest 正文属于 Secret 内容，不进入模型上下文，也不进入轨迹。返回的是 Chart 身份、revision、状态、
部署时间、被覆盖的 values **路径**，以及渲染出的对象清单——后者同时是这次回答引用的证据，点开直接落到对象本身。
需要看具体取值时，请在 Helm 应用或容器服务的 Helm 分区里用自己的身份打开。

**安装和升级要先能找到 Chart。** `repository_id` 是平台管理员添加仓库时分配的标识，集群里没有任何地方能推断出它，
所以四个目录工具是写工具的前置：`list_helm_repositories` 给出标识，`list_helm_charts` 找到 Chart，
`list_helm_chart_versions` 用于固定或降级版本，`get_helm_chart` 读它自带的 `values.yaml`——合法的 values 路径只写在
那里，凭空撰写的配置会被 Chart 自己的 `values.schema.json` 拒绝。这四个读的是平台配置而不是集群内容，因此它们的
权限是**全局**的 `helm.repository.read`，在每次调用时按全局作用域判定，而不是随会话 Cluster 判定——这个权限的作用域
下限就是全局，按 Project 判定会让一个本该无效的绑定变得有效。它们只返回仓库的标识、名称和启用状态，不返回仓库的
用户名、CA 证书或密钥环。

**Release 变更走的是和 Manifest 一样的“预检 → preview_id → 提交”两步，只是权限栈更长。** 四个 `preview_helm_*`
执行 Helm 自己的 DryRun：Server 从平台维护的仓库目录取 Chart，目标 Cluster 的 Agent 用 Helm 的引擎渲染，什么都
不写，返回将要创建、替换或删除的对象清单和一个绑定当前用户与 Cluster 的服务端快照。`apply_helm_release_change`
只接受 `preview_id`，不能在提交时换一份 Chart、values 或 revision；批准后它重新判权、再跑一次 DryRun，然后才提交。
同一个快照第二次提交返回第一次的结果，而不是再改一次集群。

四种动作都由 `apply_helm_release_change` 提交，因为提交这一步对四者是同一件事——重放快照。`preview_id` 里带着
动作（`helm_uninstall_…`、`helm_upgrade_…`），因为审批弹窗上能看到的就是这一个字符串，而“批准一次卸载”和
“批准一次升级”不是同一个决定。它**始终**是敏感操作：一次 Release 变更会写入这个应用拥有的每一个对象，没有哪种
Release 写入算例行操作。

权限按动作分开算，不取并集：安装、升级、回滚花掉 `cluster.resource.create` 与 `cluster.resource.update`，
卸载花掉 `cluster.resource.delete`；三项公共权限（`cluster.read`、`cluster.helm.manage`、`cluster.secret.manage`）
由运行时逐次重验，其余在工具内按动作和目标 Namespace 解析，并在批准之后再解析一次——权限可能在等待审批期间被收回。
Chart 是否可以渲染出不属于任何 Namespace 的对象，由操作者的 `cluster.manage` 决定，永远不从工具参数里取；
没有它，Agent 会按对象名逐个拒绝。

values 是模型唯一可以自由撰写的字段，因此它被限制在 3 KiB：工具调用的参数在轨迹里就是按这个量级保存的，
一份存不全的 values 等于一次事后无法完整复核的变更。**任何情况下都不要把凭证明文写进 values**——它会进入轨迹
并发送到模型端点，这与 `run_terminal_command` 的命令是同一条规则。只想换 Chart 版本时用 `reuse_values=true`，
不必也不应该重新撰写配置。更长或含凭证的配置留给 Helm 应用里的人来做。

三个不暴露给模型的开关：`atomic`（失败后自动回滚，那是一次没有人批准的第二次写入）、`disable_hooks`
（跳过 Hook 的 Release 不是 Chart 描述的那个 Release）、`max_history`（它会悄悄删掉将来可用的回滚目标）。
等待就绪是可选的，并且最长 600 秒——一次工具调用跑在一个 Turn 里，而 Turn 不会等一小时。

`ai.run` 只负责打开 AIOps，不替代上表中的任何权限。固定权限由运行时逐次重验；Manifest 和回滚再按实际文档、动作与
Namespace 选择一项有效权限，少一项就整次拒绝并把拒绝写进轨迹和审计。工具目录 API 用 `conditional_permissions`
公开这些候选权限，不表示调用者必须同时持有全部权限。

## 主动打开应用

AIOps 可以自己打开桌面上的其他应用来展示结论：说「看看集群最近一小时的内存」，它写好 MetricsQL 之后直接打开
监控的「数据探索」，把表达式填进去**并执行**，图已经在那里了。这是 `open_console_view`，目录里唯一一个作用在
屏幕上而不是集群上的工具。它存在的理由很直接——有些答案不是文字，而一个能写出表达式却不能把它显示出来的 Agent，
恰好把最后一步（打开监控、选集群、粘贴、点执行查询）留给了那个正因为不想做这一步才来提问的人。

**要不要替你打开，由模型按当下场景自己判断。** 结论本来就带证据入口，那是你想看时才点开的邀请；替你切换画面是
更强的动作，所以它是例外而不是默认：用户明确说了「打开 / 看看」，或者一张图、一个对象本身就是答案时才用，其余
情况仍然只给证据标签。

能打开的视图和证据能指向的东西完全一样，用的也是同一套引用与判权：

| 展示的东西 | 打开的应用 | 需要的权限 |
| --- | --- | --- |
| 指标（具名查询或自定义 MetricsQL） | 监控：具名查询落到对应图表分区，表达式落到「数据探索」并直接执行 | `cluster.metrics.read` |
| 对象 | 容器服务，定位到该对象或该 Kind 的列表 | `cluster.read` |
| Event | 容器服务 | `cluster.event.read` |
| Pod 日志 | 容器服务 | `cluster.pod.logs.read` |
| Helm Release | 容器服务的 Helm 分区 | `cluster.secret.read` |

**打开窗口不授予任何东西。** 被打开的应用仍以你自己的身份逐次授权它发出的每一个请求，和你从 Dock 点开它完全
一样；Server 在写入意图时按目标类型判权，读取轨迹时再判一次，权限已撤销的意图不会再出现。目标 Cluster 由 Server
从会话工作区填入，模型不能指定别的集群。

**画面什么时候真的会动，由 Console 决定，而且条件比较窄。** 意图写在那次调用的 `tool_result` 上，是可导出、可
复核的记录；但一条持久条目每次加载都会被重放，所以只有三件事同时成立时桌面才会动：意图是在你正看着这个会话时
到达的（打开旧会话不会重放出一堆窗口）、浏览器标签可见且 AIOps 窗口没有最小化、并且这一轮还没有打开过（Server
与 Console 各自守着同一条上限）。任何一条不成立，它就退化成对话里的一张卡片：写清打开的是什么、模型给出的理由，
以及一个你自己按的按钮。

系统设置 → 桌面偏好里的**「允许 AIOps 主动打开应用」**可以关掉自动打开这件事本身。关掉之后不会丢任何东西——
卡片和按钮照常出现，只是不再有人替你按。

## 技能

技能是 ZKE 随 Server 发布的排查流程（Playbook）：一份技能规定这一类问题该用目录里的哪些工具、按什么顺序取证、
以什么标准下结论、以及不该做什么。当前提供 Pod 反复重启、工作负载不就绪、Pod Pending 与节点压力、Service 与
Ingress 不通、PVC 与卷挂载、资源饱和度评估、接入自定义指标采集、变更时间线与变更后验证、受控变更与回滚、
Helm Release 的受控变更十份。
技能只列出读取和预检工具，不列出提交类工具——一份能把变更带过“由人决定要不要改”这一步的流程，就不再只是流程了。

技能不是能力：它不新增工具，不放大权限，不改变审批模式，也不能指向别的 Cluster。模型在系统提示词里只看到技能 ID
和一句话摘要，判断用得上时再用 `load_skill` 读取正文；技能里的每一步仍按工具目录逐次重验权限、按当前审批模式停下
等待。技能正文是给模型遵循的指令，因此只随代码发布，不能由会话、租户或集群内容写入——这也是它成为工具目录里唯一
被标记为“可信”的答复的原因，其余所有工具结果一律是不可信数据。

一份技能所需的工具没有全部组装时（例如部署没有安装多集群指标），它不会出现在目录里，而不是出现后在中途卡住。
输入区的“技能”按钮列出当前可用的技能、用途和它会用到的工具。

## 并行子任务

需要同时查清几件互不依赖的事时，模型可以调用 `run_subtasks` 派发最多 3 个并行分支，例如“资源状态 / 近期 Event /
指标异常”。每个分支是一次独立的只读调查：

- 只拿到自己的目标和主 Agent 显式传递的少量已知信息，看不到对话，也看不到其他分支；
- 只有只读工具，不能改变集群，也不能再派生子任务；
- 有自己的步骤、工具调用和超时预算（默认 8 步、24 次调用、5 分钟），都远小于主任务；同一步内的并发读取上限在
  各分支之间分摊，派发不会放大对目标 Agent 的并发压力；
- 每一次读取仍按该工具的权限逐次重验，敏感读取仍按当前审批模式停下等待——对话里会写明是哪一个分支在请求；
- 只把结论、证据引用和失败分类交回主任务，由主任务负责汇总和消解冲突。

分支的每一步都写进同一条轨迹并带分支标记：对话 Tab 把它们折叠在派发它们的那次工具调用下面，轨迹 Tab 原位显示并
标注来源。一个分支失败不会结束整轮运行，它以明确的失败分类回到主任务，模型必须如实说明这一路没有结论。父 Turn
结束前一定回收全部分支，不存在关掉窗口后仍在跑的分支。分支不推送流式增量，它们的持久条目照常即时到达。

写操作不参与派发：写入的顺序、幂等和审批保证定义在一条串行路径上。部署也可以把 `aiops.subtask.max_parallel`
设为 0 完全关闭派发，此时该工具不出现在目录里。

## 审批模式

审批模式在输入区切换，运行中切换会追加 `system` 轨迹，因此记录能说明每一段运行在哪个模式下。

| 模式 | 行为 |
| --- | --- |
| 请求批准（默认） | 敏感工具与写工具逐次确认 |
| 帮我批准 | 只有敏感工具确认 |
| 完全访问 | 不再停下来询问 |

三档都不扩大权限：上限始终是发起用户自己的 RBAC，模式只决定谁来按下确认。它真正改变的是一段来自 Pod 日志的
提示注入能走多远，所以每次请求都会记录当时的模式。等待批准超过时限、或运行被取消，本轮以明确的失败分类结束，
不会补写虚构的成功。

所有资源写工具在“请求批准”模式逐次确认；删除、RBAC、受保护 Namespace、Manifest `force` 等敏感调用在“帮我批准”
模式也会等待。DryRun 工具不标记为写操作。Manifest 与回滚预检成功后由 Server 保存 15 分钟有效的 `preview_id`，
它绑定当前用户、Cluster、操作和原始内容；实际工具不接受新 YAML 或新回滚参数。批准后先重验当前 RBAC 并重新 DryRun，
失败就不写入。首次实际提交固定预检的幂等键，成功后的重试直接返回缓存结果，不重复写 Agent。包含写工具的同一步调用
按模型顺序执行。AIOps 明确拒绝 Secret 清单，即使账号持有 `cluster.secret.manage`，Secret 仍只能从 ZKE 专用入口修改。

Cluster Terminal 命令始终同时标记为敏感和可能变更：“请求批准”和“帮我批准”都会逐次等待确认；“完全访问”只省略
人工停顿，不扩大权限。Server 在本 Turn 首次命令批准后重新计算当前用户的全部 `cluster.*` 权限，仅移除 Secret 读写
权限后交给 Agent 按固定白名单投射到 Turn 专属 ServiceAccount；后续命令复用同一 Pod 和冻结快照。Agent Namespace 与系统 Namespace 不再有 AIOps 额外禁止，而是分别依据
`cluster.agent_namespace.manage` 与 `cluster.system_namespace.manage`，Pod Exec 再叠加 `cluster.pod.exec`。命令容器
不挂载该 ServiceAccount Token，而是通过同 Pod 的 localhost
凭证代理运行 kubectl；代理凭证不会进入命令、stdout/stderr、轨迹或模型上下文。命令及有界输出会进入轨迹并发送给
模型，因此调用参数不得包含密码、Token 或其他凭证明文。Turn 结束、失败或取消后立即清理 Pod 和临时 RBAC，异常时
仍由 Agent TTL 清理任务兜底；从 Pod 创建到清理的完整生命周期持续重验冻结快照，覆盖命令执行和模型思考的空闲期。
快照中的任一权限被撤销都会取消命令、清理资源并禁止本 Turn 重建终端；已经发生的 Kubernetes 操作不会伪装成已回滚。

## 运行时

- 问题先写入轨迹，Agent 循环再脱离 HTTP 请求后台运行，关闭窗口不终止任务；
- 一个 Turn 由多个 Step 组成：一次模型调用加上它请求的工具执行，工具结果进入下一个 Step；
- 每个 Step 都从持久轨迹重建模型输入，因此两步之间被撤销的权限在下一步就不再可读；
- 单个 Turn 有最大 Step 数、最大工具调用数、总耗时与上下文预算；相同参数的重复调用会被收敛保护拦下；
- 轨迹通过 SSE 推送。持久事件带序号，重连用 `Last-Event-ID` 或 `after_sequence` 精确续传；流式增量不带序号、
  不重放，也不是记录——对应步骤的 `model` 条目才是；
- 模型调用前按管理员填写的上下文窗口、输出预留和触发阈值做预算，超阈值自动生成保留目标与证据的摘要轨迹；
- 结论携带结构化证据引用，只列本轮真正读过的对象且同一对象只出现一次；历史读取失去证据权限后由 Server 移除引用
  并脱敏集群正文；
- 点开一条证据在当前桌面打开对应应用的窗口并定位到该对象或该指标，而不是另开浏览器标签页重载 Console。

## 模型与长上下文

管理员配置 OpenAI Responses 或 Chat Completions 兼容端点，并填写模型真实的上下文窗口、最大输出和超时。运行时以
函数调用协议向端点公开工具 Schema，并始终请求流式响应；端点若以单个 JSON 文档应答，也照常处理，只是不再有首
token 时延可测。限流、5xx 与连接中断按指数退避重试；凭证被拒和额度用尽不重试，各自以自己的分类结束。

何时压缩不是模型配置，而是部署策略：Server 配置文件里写的是**比例**（默认窗口的 80% 触发、保留窗口 16% 的最近
尾部），因此换一个上下文窗口更大的模型只需要改模型配置，不必再维护一个绝对的 token 阈值。达到阈值后，运行时把
较早的一段对话交给模型写成结构化检查点，最近几步原样保留，检查点连同被替换的轨迹区间一起写入 `compaction`；
端点若直接以"请求过大"拒绝，也会压缩后重发一次。原始条目始终保持仅追加。

输入框旁的环形读数显示当前会话占了多少上下文，点开可以看到系统提示词、工具 Schema 与对话消息的分段，以及触发
压缩的位置。它由 Server 计算，与循环每次请求前所做的是同一次测量。

模型端点会收到用户问题、文本附件、显式引用的证据，以及工具返回的集群内容。ZKE Session Token、Agent 证书、模型
API Key、Secret 明文和 kubeconfig 不会进入模型上下文或轨迹。

## 规划切片

1. 只读运行时、实时推送、权限重验、上下文预算、摘要压缩和证据引用（已实现）；
2. AIOps App、轨迹、文本附件、搜索、归档、删除、导出与证据深链（已实现）；
3. 模型自主工具循环、读取工具目录、敏感工具审批、流式输出与轨迹时间线（已实现）；
4. 资源写工具与 Cluster Terminal：DryRun 差异、预检快照、幂等键、审计闭环、三档审批及受控命令（已实现）；
5. 随 Server 发布的排查技能与只读并行子任务（已实现）；
6. 审计变更时间线、对象变更后验证及指标验证 Playbook（已实现）；
7. 定时巡检与事件触发自动化。

详细约束见 [Phase 4 AIOps 架构](../architecture/ai-phase-4.md) 与
[AIOps Agent 运行时与上下文设计](../architecture/ai-agent-runtime.md)。
