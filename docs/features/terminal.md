# 终端

终端是独立的单集群 App，为 Kubernetes 熟练用户提供浏览器内的临时 CloudShell。用户选择目标 Cluster 后显式
确认创建会话；终端镜像内运行标准 `kubectl`，请求直接使用会话专属 ServiceAccount 访问目标 Kubernetes API
Server。终端 Pod 与 ServiceAccount 固定创建在目标 Cluster 保存的 Agent Namespace（首次接入界面默认 `zke-system`），
Shell 的默认 kubectl
Namespace 为 `default`。

该能力不复用 ZKE Agent ServiceAccount。每个会话由目标 Cluster Agent 创建以下短生命周期资源：

- 一个不可提权、无宿主机挂载并带 CPU/内存上限的 Terminal Pod；
- 一个自动挂载 projected ServiceAccount Token 的专属 ServiceAccount；
- 三个分别承载普通、`kube-*` 与 Agent Namespace 权限的 ClusterRole，以及每个现有 Namespace 中指向对应角色的 RoleBinding；
- 一组仅包含已授权集群级资源的 ClusterRole 与 ClusterRoleBinding。

普通资源权限只投射到普通 Namespace；`kube-*` 与 Agent Namespace 分别要求
`cluster.system_namespace.manage`、`cluster.agent_namespace.manage` 才投射写权限。Secret、Kubernetes RBAC、Pod Exec
和端口转发在受保护命名空间仍叠加各自的专用权限；普通资源读取继续由 `cluster.read` 控制。

浏览器仍通过 Server 和目标 Cluster Agent 的 `pod-exec.v1` QUIC Stream 接入 Terminal Pod。会话关闭、达到最长
时长或权限重验失败后，Agent 删除上述资源；Agent 内的清理任务也会回收 Server 异常退出后遗留的过期会话。

## 权限投影

打开入口要求独立的 `cluster.terminal.exec`。该权限可以像现有权限一样授予自定义角色，但它本身不授予任何
Kubernetes 资源权限。Server 在创建会话时读取当前用户在目标 Cluster 实际持有的权限，Agent 按固定规则生成
Kubernetes RBAC：

| ZKE 权限                                                                | 终端中的 Kubernetes 能力                                          |
| ----------------------------------------------------------------------- | ----------------------------------------------------------------- |
| `cluster.read`                                                          | 已支持主资源的 `get`、`list`、`watch`                             |
| `cluster.resource.create/update/delete`                                 | 普通 Namespace 中对应主资源的创建、更新/Patch、删除               |
| `cluster.system_namespace.manage`                                       | `kube-*` 中普通资源增删改及系统 Namespace 生命周期                 |
| `cluster.agent_namespace.manage`                                        | Agent Namespace 中普通资源增删改及 Agent Namespace 生命周期       |
| `cluster.secret.read`                                                   | Secret 的 `get`、`list`、`watch`；受保护空间叠加对应权限           |
| `cluster.secret.manage`                                                 | Secret 增删改；受保护空间叠加对应权限，且不隐含读取                |
| `cluster.rbac.read/manage`                                              | ServiceAccount、Role/RoleBinding 及集群级授权对象的对应操作       |
| `cluster.event.read`                                                    | Event 的 `get`、`list`、`watch`                                   |
| `cluster.pod.logs.read`、`cluster.pod.exec`、`cluster.pod.port_forward` | 对应 Pod Subresource                                              |
| `cluster.namespace.manage`                                              | 会话建立时已存在的普通 Namespace 更新、Patch、删除                 |

Kubernetes RBAC 无法按待创建对象的名称限制 `namespaces/create`。因此终端只有在会话同时持有普通、系统和 Agent 三类
Namespace 权限时才投射 Namespace 创建；只持有其中一类时仍可通过 Server 的类型化或通用 Resource 接口按目标名称创建。

会话保存创建时的权限快照并周期重新验证。快照中的任何权限被撤销都会终止会话并清理临时授权；会话期间新授予的
权限不会自动进入已有 Role，需要重新打开终端。会话建立后新建的 Namespace 也不会自动出现 RoleBinding，重新打开
终端后才会获得对应的命名空间级权限。

`cluster.rbac.manage` 仍受 Kubernetes 的提权检查约束，不包含 `bind`、`escalate` 或 `impersonate`。ZKE 管理的
Agent ClusterRole/ClusterRoleBinding 不会进入终端可更新或删除的对象名单；会话中新建的集群级授权对象需要重新
打开终端后才能更新或删除。

## 配置与镜像

Cluster Terminal 镜像、独立的 Image Pull Policy 与会话 Pod 的 CPU / 内存请求与限制由全局管理员在
“平台配置 → 集群终端”管理，修改后立即用于新建会话。资源预算此前是 Agent 里的常量，现在随镜像一同下发；
留空表示不在容器上设置该项。
`build/agent/Dockerfile` 构建的
`ghcr.io/togettoyou/zke-agent` 同时包含 Agent 二进制、kubectl、交互式 Shell、基础工具和默认启用的 kubectl
Bash Tab 补全。Agent Deployment 使用镜像默认入口运行 `zke-agent`；Cluster Terminal Pod 会覆盖入口命令并直接
启动 Shell，因此两种能力默认共用一个镜像。会话存续时长同样在该页设置，可选 1 分钟至 1 小时，默认 15 分钟；它随镜像在同一次读取中解析，修改后立即用于新建会话，已经建立的会话
不受影响。

首次使用某个节点时可能需要拉取终端镜像。终端创建使用 `agent_listener.resource_request_timeout` 的独立请求预算
（默认 2 分钟），不会被普通 API 的 10 秒操作超时或 Console 的 30 秒通用请求超时提前中断；Agent 等待 Pod
进入 Running 的过程也以该请求预算为唯一上限。超时会返回 `504 timeout`，并清理本次创建的临时 Pod 与授权。
确认创建后，等待状态显示在终端主界面而不是阻塞式弹窗中；切换 App、最小化窗口或显示桌面不会中断创建。
关闭终端窗口会取消尚未完成的 HTTP 请求，取消信号继续传播到目标 Agent，并以独立清理上下文回收可能已经创建的
Pod 与 RBAC，避免请求完成与窗口关闭同时发生时留下临时资源。
连接建立后，Server 使用标准 WebSocket Ping/Pong 检查浏览器是否仍持有会话；该控制帧不进入容器，因此切换 App
不会触发 2 分钟空闲回收。关闭窗口仍会关闭 WebSocket，并立即清理终端 Pod 与临时授权。

统一镜像工作流为 Pull Request 只构建 `linux/amd64` 且不推送，用于验证 Dockerfile 仍可构建；合入 `main` 或推送
Git Tag 时构建 `linux/amd64`、`linux/arm64` 两个平台并发布 `ghcr.io/togettoyou/zke-agent:latest` 或对应标签。
Server 默认把同一 Agent 镜像用于平台配置中的 Agent 镜像和 Cluster Terminal 镜像。

## 当前边界

- Namespace 级权限覆盖会话建立时已经存在的 Namespace，并按普通、`kube-*` 与 Agent 三类分别投射；新建 Namespace
  后需要重新打开会话才能获得其中的资源权限。
- 默认权限映射覆盖 ZKE 当前已经管理的内置资源和可选扩展。任意新 CRD 不会因 Discovery 自动获得写权限，需先
  扩展 Agent 安装 RBAC 和终端映射，避免通配符在未来新增资源时静默扩大权限。
- ZKE 审计记录会话创建、目标和 Pod Exec 生命周期，不记录键盘输入、stdout 或 Secret 正文。逐条 Kubernetes API
  请求的审计依赖目标集群启用 Kubernetes Audit；后续可将其接入 ZKE 统一审计。
- 终端镜像只包含 kubectl、bash、Bash completion、CA 证书、curl 和 jq。第三方 kubectl plugin、Helm 和云厂商
  CLI 不属于当前交付范围。
