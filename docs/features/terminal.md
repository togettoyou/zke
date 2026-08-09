# 终端

终端是独立的单集群 App，为 Kubernetes 熟练用户提供浏览器内的临时 CloudShell。用户选择目标 Cluster 后显式
确认创建会话；终端镜像内运行标准 `kubectl`，请求直接使用会话专属 ServiceAccount 访问目标 Kubernetes API
Server。终端 Pod 与 ServiceAccount 固定创建在 Agent Namespace（默认 `zke-system`），Shell 的默认 kubectl
Namespace 为 `default`。

该能力不复用 ZKE Agent ServiceAccount。每个会话由目标 Cluster Agent 创建以下短生命周期资源：

- 一个不可提权、无宿主机挂载并带 CPU/内存上限的 Terminal Pod；
- 一个自动挂载 projected ServiceAccount Token 的专属 ServiceAccount；
- 一个承载命名空间级权限的 ClusterRole，以及每个现有业务 Namespace 中指向它的 RoleBinding；
- 一组仅包含已授权集群级资源的 ClusterRole 与 ClusterRoleBinding。

不会在 Agent Namespace 中创建上述业务 RoleBinding，因此终端不能借助命名空间级权限读取其中的 Secret、Pod 或
其他 Agent 资源。Namespace 生命周期权限也按会话建立时的业务 Namespace 名单限制更新与删除目标，排除 Agent
Namespace。

浏览器仍通过 Server 和目标 Cluster Agent 的 `pod-exec.v1` QUIC Stream 接入 Terminal Pod。会话关闭、达到最长
时长或权限重验失败后，Agent 删除上述资源；Agent 内的清理任务也会回收 Server 异常退出后遗留的过期会话。

## 权限投影

打开入口要求独立的 `cluster.terminal.exec`。该权限可以像现有权限一样授予自定义角色，但它本身不授予任何
Kubernetes 资源权限。Server 在创建会话时读取当前用户在目标 Cluster 实际持有的权限，Agent 按固定规则生成
Kubernetes RBAC：

| ZKE 权限                                                                | 终端中的 Kubernetes 能力                                          |
| ----------------------------------------------------------------------- | ----------------------------------------------------------------- |
| `cluster.read`                                                          | 已支持主资源的 `get`、`list`、`watch`                             |
| `cluster.resource.create/update/delete`                                 | 对应主资源的创建、更新/Patch、删除                                |
| `cluster.secret.read`                                                   | 现有业务 Namespace 中 Secret 的 `get`、`list`、`watch`            |
| `cluster.secret.manage`                                                 | 现有业务 Namespace 中 Secret 的创建、更新/Patch、删除；不隐含读取 |
| `cluster.rbac.read/manage`                                              | ServiceAccount、Role/RoleBinding 及集群级授权对象的对应操作       |
| `cluster.event.read`                                                    | Event 的 `get`、`list`、`watch`                                   |
| `cluster.pod.logs.read`、`cluster.pod.exec`、`cluster.pod.port_forward` | 对应 Pod Subresource                                              |
| `cluster.namespace.manage`                                              | Namespace 创建，以及会话建立时已存在的非 Agent Namespace 删除     |

会话保存创建时的权限快照并周期重新验证。快照中的任何权限被撤销都会终止会话并清理临时授权；会话期间新授予的
权限不会自动进入已有 Role，需要重新打开终端。会话建立后新建的 Namespace 也不会自动出现 RoleBinding，重新打开
终端后才会获得对应的命名空间级权限。

`cluster.rbac.manage` 仍受 Kubernetes 的提权检查约束，不包含 `bind`、`escalate` 或 `impersonate`。ZKE 管理的
Agent ClusterRole/ClusterRoleBinding 不会进入终端可更新或删除的对象名单；会话中新建的集群级授权对象需要重新
打开终端后才能更新或删除。

## 配置与镜像

Server 的 `cluster_terminal.image` 为空时，终端会话创建保持禁用。仓库提供
`build/terminal/Dockerfile`，从 Kubernetes 官方 `registry.k8s.io/kubectl:v1.35.0` 镜像复制 kubectl，并使用
Alpine 提供交互式 Shell、基础工具和默认启用的 kubectl Bash Tab 补全。CI 首次成功发布前
`cluster_terminal.image` 仍需保持为空或由部署方填写实际
可拉取的镜像地址。`cluster_terminal.session_ttl` 可配置为 1 分钟至 1 小时，默认 15 分钟。

首次使用某个节点时可能需要拉取终端镜像。终端创建使用 `agent_listener.resource_request_timeout` 的独立请求预算
（默认 2 分钟），不会被普通 API 的 10 秒操作超时或 Console 的 30 秒通用请求超时提前中断；Agent 等待 Pod
进入 Running 的过程也以该请求预算为唯一上限。超时会返回 `504 timeout`，并清理本次创建的临时 Pod 与授权。
确认创建后，等待状态显示在终端主界面而不是阻塞式弹窗中；切换 App、最小化窗口或显示桌面不会中断创建。
关闭终端窗口会取消尚未完成的 HTTP 请求，取消信号继续传播到目标 Agent，并以独立清理上下文回收可能已经创建的
Pod 与 RBAC，避免请求完成与窗口关闭同时发生时留下临时资源。
连接建立后，Server 使用标准 WebSocket Ping/Pong 检查浏览器是否仍持有会话；该控制帧不进入容器，因此切换 App
不会触发 2 分钟空闲回收。关闭窗口仍会关闭 WebSocket，并立即清理终端 Pod 与临时授权。

`Terminal image` GitHub Actions 工作流只在 `build/terminal/Dockerfile` 变更时触发：Pull Request 构建
`linux/amd64`、`linux/arm64` 两个平台但不推送，合入 `main` 后发布 `ghcr.io/togettoyou/zke-terminal:latest`
和不可变的 `sha-<commit>` 标签。首次发布后仍需在 GitHub Packages 中确认镜像的公开可见性，再将选定标签写入
`cluster_terminal.image`。

## 当前边界

- Namespace 级权限覆盖会话建立时已经存在的全部业务 Namespace，但明确排除 Agent Namespace；新建 Namespace
  后需要重新打开会话才能获得其中的资源权限。
- 默认权限映射覆盖 ZKE 当前已经管理的内置资源和可选扩展。任意新 CRD 不会因 Discovery 自动获得写权限，需先
  扩展 Agent 安装 RBAC 和终端映射，避免通配符在未来新增资源时静默扩大权限。
- ZKE 审计记录会话创建、目标和 Pod Exec 生命周期，不记录键盘输入、stdout 或 Secret 正文。逐条 Kubernetes API
  请求的审计依赖目标集群启用 Kubernetes Audit；后续可将其接入 ZKE 统一审计。
- 终端镜像只包含 kubectl、bash、Bash completion、CA 证书、curl 和 jq。第三方 kubectl plugin、Helm 和云厂商
  CLI 不属于当前交付范围。
