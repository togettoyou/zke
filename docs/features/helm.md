# Helm 应用

「Helm 应用」是 Console 桌面上的独立应用，负责一件容器服务不做的事：**改变**集群里装了什么。

容器服务里的「Helm 应用」区块回答「这个命名空间里装了哪些应用、什么 Chart 版本、用的什么 values」，读的是
Helm 自己的存储。本应用是另一半：选 Chart、编辑 values、预览渲染结果、安装、升级、回滚、卸载。两者共用同一份
Release 数据，入口不同是因为它们要的工作面不同——改一个 Release 需要一个可编辑、可比对、可反悔的页面，而不是
资源列表里的一个抽屉。

- 渲染与写入由**目标集群的 Agent** 用 Helm 自己的引擎完成，Server 不渲染 Chart，也不代写 Release Secret。
- 可安装的 Chart 只来自平台管理员维护的仓库目录。应用中没有任何接受任意地址的入口。
- 每次变更都可以先预览：`dry_run` 用完全相同的请求在目标集群渲染一次，返回将要应用的清单，不写入任何对象。

## 为什么由 Agent 执行

Release 不是 Kubernetes 的一种资源。Helm 3 把每次修订存成一个 `helm.sh/release.v1` 类型的 Secret，Chart、
values、渲染出的清单与 NOTES 都在里面；Chart 创建出来的 Deployment、Service 与 PVC 是普通对象，不带任何指回
Release 的引用。

要正确地安装或升级一个 Release，必须跑 Helm 自己的那一套：模板引擎、值合并、Hook 与执行顺序、历史改写。由
Server 拼一个「差不多」的实现去写 Release Secret，会破坏真正的 `helm` 客户端依赖的历史——而一个只写对了一半的
Release 比没有这个功能更难收拾。

所以分工是：

| 位置        | 做什么                                                                       |
| ----------- | ---------------------------------------------------------------------------- |
| Console     | 选 Chart、编辑 values、选开关、审阅预览                                      |
| Server      | 鉴权、审计、从仓库目录取 Chart 包、把请求与 Chart 交给目标集群的 Agent       |
| Agent       | 用 Helm 引擎渲染并写入，检查渲染结果是否越界，把 Helm 的结论原样报告回来      |

Server 与 Agent 之间为此新增了一条业务 Stream（`STREAM_KIND_HELM`，能力标识 `helm.v1`）。Chart 包与 values
文档跟在请求消息之后按字节流传输——Chart 远大于一个协议帧。Agent 不从自己的文件系统读任何东西：没有 Helm home、
没有仓库缓存、没有插件目录，因此 Chart 不会被节点上碰巧存在的同名文件替换掉。

不支持 Helm 的 Agent 会被直接拒绝（`424 helm_unsupported`），不会退回到某种「Server 自己写 Secret」的降级路径。

## Chart 仓库目录

Chart 必须有来源，而 ZKE 不接受调用方临时指定的来源：能装什么由持有 `helm.repository.manage` 的平台管理员决定。
目录是平台级的——Chart 是软件来源，不是某个 Project 的基础设施；而「能否把它装进某个集群」由集群权限决定，
与它来自哪个仓库无关。

一条目录条目包含：名称、说明、仓库地址、可选的用户名与口令、可选的自定义 CA、是否跳过 TLS 校验、是否启用。

- **地址**必须是绝对的 http 或 https 地址，且不得内嵌凭证。Server 在存储时校验一次，在真正发起请求前再校验
  一次——URL 是这台 Server 会去访问的地方，所以在使用点上也要判定，而不是只在录入点。
- **口令只写不读**。任何接口都不返回它，读取方只会看到 `has_credentials` 说明是否配置过。更新时不传该字段表示
  保留，传空字符串表示清除。凭证以请求头发送，不会出现在 URL、日志或错误文案里。
- **跳过 TLS 校验**是一次明确且可审计的选择，会显示在列表里，而不是某种静默回退。
- **停用**不影响已安装的 Release：Release 自带安装时使用的 Chart。删除仓库同理。

索引（`index.yaml`）由 Server 按需拉取并在内存中缓存 5 分钟，Chart 列表上的「索引读取于 …」就是这份缓存的
时间。搜索在 Server 端完成：公开仓库的索引动辄数千个 Chart，把它们全下发到浏览器里过滤，正是这份缓存要避免的
下载。

缓存是明说的，不是让人自己发现的：刚发布的 Chart 在缓存过期前不会出现，而「我的 Chart 不见了」在没有这行时间
的界面上会变成一次误报。「重新拉取索引」（`POST .../repositories/{id}/index-refresh`）就是显式重读的入口——
它丢掉 Server 持有的派生数据，再发起一次与缓存过期时相同的上游请求，不改变任何存储状态，因此要求的是
`helm.repository.read` 而不是管理权限。修改仓库也会立即作废它的缓存，避免改正地址之后仍然看到旧结果。

Server 向仓库发起的请求有明确边界：整个交换有超时上限，索引与 Chart 包各有大小上限，https 的重定向不会被跟进
到 http（否则凭证会经明文一跳）。

## 一次变更的流程

1. 在「Chart 目录」里选仓库、搜索、打开一个 Chart。这里能读到它自带的 `values.yaml` 原文、README、依赖的子
   Chart 与全部已发布版本。values 以文本原样返回而不是解析后的对象——其中的注释就是一半的文档。
2. 点「安装」进入表单：Chart 版本、Release 名、values 编辑器、执行方式开关。
3. 点「预览」。Server 用**完全相同**的请求体加上 `dry_run` 调用一次，Agent 在目标集群渲染 Chart 并返回将要
   应用的清单，不写入任何对象。安装展示清单本身，升级展示与当前修订的差异。
4. 审阅之后才能提交。改动表单里的任何一项都会作废这份预览——一张描述着没人会发出的请求的图，放在「提交」按钮
   旁边比没有更糟。

升级同理，只是起点是正在运行的那个修订：它的 values 预填进编辑器，它的渲染清单成为差异的左侧。Helm 只记录
Release 用的是哪个 Chart，不记录它来自哪个仓库，所以升级时需要指明仓库。

回滚与卸载是对目标的决定而不是对内容的决定，因此是确认对话框而不是页面：

- **回滚**重放某次修订记录下来的对象，并写入一个新的修订——它不抹掉中间发生过的事。只能回到存储中仍然保留的
  修订：被 `--history-max` 清理掉的修订不再存在，界面也不会假装它们还在。
- **卸载**删除 Release 拥有的对象。`keep_history` 决定是否保留修订历史：保留下来的历史正是之后回滚所需要的，
  不保留则连同历史一并删除。

## 权限

Helm 的写入是本 Server 上权限栈最长的一组路由，每一条回答的是同一个请求的不同问题。

| 操作                 | 需要的权限                                                                                                              |
| -------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| 浏览仓库与 Chart     | `helm.repository.read`（全局）                                                                                          |
| 管理仓库             | `helm.repository.manage`（全局）                                                                                        |
| 查看 Release 与 values | `cluster.read` + `cluster.secret.read`                                                                                 |
| 安装 / 升级 / 回滚   | `cluster.read` + `cluster.helm.manage` + `cluster.resource.create` + `cluster.resource.update` + `cluster.secret.manage` |
| 卸载                 | `cluster.read` + `cluster.helm.manage` + `cluster.resource.delete` + `cluster.secret.manage`                            |

- `cluster.helm.manage` 说明「可以改 Release」，它单独不够：一次安装真正花掉的是对象的创建与更新权限，卸载花掉
  的是删除权限，持有 Helm 权限不会凭空变出写对象的能力。
- `cluster.secret.manage` 是必需的，因为 Helm 的 Release 存储**本身**就是一个 Secret，而它保存的 values 就是这个
  Secret 的内容。不能写 Secret 的角色不能写 Release。
- kube-\* 与 Agent 所在的命名空间照旧需要 `cluster.system_namespace.manage` 或 `cluster.agent_namespace.manage`：
  往 `kube-system` 装一个 Release 就是往 `kube-system` 写 Secret 和对象，路由叫什么名字不能成为绕过它的方式。

内置 `admin` 角色自动持有全部权限；内置 `viewer` 不含其中任何一项。

### Agent 侧的两条硬规则

权限判定发生在 Server，但有两件事只有渲染完成之后才知道，因此由 Agent 在写入之前对**实际渲染结果**判定：

1. **不得跨命名空间。** Server 授权的是一个命名空间。Chart 渲染出的对象若显式写着别的命名空间，Agent 指名拒绝
   （`helm_chart_cross_namespace`）。没有写命名空间的对象照旧落在 Release 的命名空间里，这是 Helm 自己的行为。
2. **集群级对象需要集群级授权。** CRD、ClusterRole、StorageClass 这类对象不属于任何命名空间，命名空间级的授权
   说明不了它们。只有持有该集群 `cluster.manage` 的操作者，请求里才会带上允许标记；否则 Agent 指名拒绝
   （`helm_chart_cluster_scoped`）。这个标记由 Server 从权限推出，请求正文里带这个字段会被直接拒绝。

界面会在没有 `cluster.manage` 时提前说明这一点，但判定始终在 Agent。

### Agent 的 Kubernetes 权限上限

Agent 在目标集群里的 ClusterRole 是一份显式清单，只覆盖 ZKE 自身功能涉及的资源类型。Chart 会创建它声明的任何
类型，因此**一个需要清单之外类型的 Chart 会被 Kubernetes 拒绝**，错误里指名是哪个类型——这是 Kubernetes 的
拒绝，不是 ZKE 的。

为此 Agent 绑定的 ClusterRole 是一个聚合角色：ZKE 自己的那份权限放在 `zke-agent-base` 里并带上聚合标签，集群
管理员可以创建自己的 ClusterRole、打上同一个标签来扩展它，Kubernetes 会把它们合并进来。

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: zke-agent-extra
  labels:
    zke.io/aggregate-to-agent: "true"
rules:
  - apiGroups: ["apiextensions.k8s.io"]
    resources: ["customresourcedefinitions"]
    verbs: ["create", "update", "patch", "delete"]
```

这样授权是集群管理员做出的、写在他们自己集群里的、`kubectl get clusterrole -l` 能看见的，也能在不重装 Agent
的情况下收回；ZKE 不会替他们决定发一份覆盖一切的权限。

## 审计

四种操作各有自己的动作，且预演与真正写入分开记录：

| 动作                                   | 何时写入                     |
| -------------------------------------- | ---------------------------- |
| `kubernetes_helm_release.install`      | 安装                         |
| `kubernetes_helm_release.upgrade`      | 升级                         |
| `kubernetes_helm_release.rollback`     | 回滚                         |
| `kubernetes_helm_release.uninstall`    | 卸载                         |
| 以上四个的 `.dry_run` 变体             | 预览                         |
| `helm_repository.create/update/delete` | 仓库目录变更（全局作用域）   |

四种操作不合并成一个动作，因为事后要问的正是「发生的是哪一种」：升级和回滚都产生一个新修订，含义却相反。
预览也记录，因为它确实在目标集群渲染了一次并读回了将要应用的清单；它不写入任何东西，把这两者分开正是分开记录
的意义。

审计目标名沿用 Secret 家族并附上 Release 名，这样按「谁改了这个命名空间的 Secret」筛选的人不会漏掉这条路径。
Release 的说明里会记下发起者，`helm history` 在 ZKE 之外也能读到。

## 重试与幂等

每次写入都带 `Idempotency-Key`，由 Console 在打开表单时生成并在整个提交过程中保持不变。响应在提交之后丢失是
这里代价最高的一种失败：调用方分不清「升级没有发生」和「升级发生了但答复丢了」，盲目重试会给一个已经升级过的
应用再加一个修订。Agent 因此按这个键记住结果——同一个键、同一份请求重放上一次的答复，同一个键、不同的请求
则拒绝为冲突。

保留这个键是有代价的：键被占用之后，下一次用它的尝试会被判为冲突。因此只有**可能改变了集群**的结果才占用它。
预演不占用（它什么都没写，这也正是「先预览再提交」能共用同一个键的原因）；能够证明什么都没写的拒绝也不占用——
请求或正文被拒、集群根本连不上、Helm 在自己的存储上先行拒绝、以及渲染结果被 Agent 的两条硬规则拦下（post
renderer 在应用之前运行）。其余一律按「可能已经写入」处理：Helm 一旦开始应用，失败也可能留下对象，而 Agent
分不清这两种情况。

## 边界与限制

- 只支持 Secret 存储驱动。使用 ConfigMap 或 SQL 驱动的 Release 不会出现在列表里，也不能由这里管理。
- 只支持 HTTP(S) Chart 仓库。OCI 仓库（`oci://`）尚未支持。
- Chart 必须自带依赖（`charts/` 已打包）。ZKE 不会在安装时去解析和下载子 Chart，未打包依赖的 Chart 会被拒绝，
  而不是装出一个缺了一半的应用。
- 每个集群同一时刻只执行一个 Release 变更。两个并发操作会在 Helm 自己的存储上竞争，而 Helm 没有锁可以阻止它们；
  第二个请求会被拒绝并提示稍后重试，而不是排队等待——调用方是一个有自己期限的 HTTP 请求。
- 一次操作的等待时间有上限（默认 15 分钟，见 `agent_listener.helm_request_timeout` 与 Agent 的
  `connection.max_helm_stream_timeout`）。
- 渲染出的清单超过上限时会被截断并明确标注；提交本身不受影响。
