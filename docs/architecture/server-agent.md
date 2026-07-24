# Server + Agent 架构

## ZKE Server

ZKE Server 是平台统一控制端，规划负责：

- Web Desktop；
- 用户认证；
- RBAC 权限控制；
- 多租户与项目管理；
- Agent 连接管理；
- 集群元数据管理；
- 操作任务下发；
- 数据存储；
- 审计日志；
- AI 分析与任务编排；
- 模型 API Gateway；
- 多集群资源汇总。

## ZKE Agent

每个接入的 Kubernetes 集群部署一个 ZKE Agent。Agent 规划负责：

- 主动连接 ZKE Server；
- 上报 Agent 版本、健康状态和集群状态；
- 查询 Kubernetes 资源；
- 执行经过授权的集群操作；
- 获取 Pod 日志；
- 建立 Web Terminal 会话；
- 执行作业和模型服务相关操作；
- 收集或转发指标、日志和事件；
- 返回操作结果。

## 当前实现边界

当前已完成 Server 注册凭证创建、Agent 注册 HTTP API，以及 Agent 首次注册与身份 Secret 持久化。Agent 在集群
内生成私钥，Server 只接收 CSR；注册结果中的证书身份显式绑定 Tenant、Project、Cluster 和 Agent。Agent 先
持久化私钥、CSR 和幂等键再发出网络请求，因此请求结果不确定或 Pod 重启时仍能使用同一注册尝试恢复结果。

身份存储使用固定名称 Kubernetes Secret。Agent 通过 client-go 按配置的 `identity.namespace` 和
`identity.secret_name` 查找它；不存在时直接创建并写入待注册私钥、CSR 和幂等键，存在时读取或更新。部署清单
不再需要预创建空 Secret。ServiceAccount 至少需要所在 Namespace 的 Secret `create` 权限，以及身份 Secret 的
`get`、`update` 权限；不需要通过 API 读取注册 Token Secret。

一次性注册 Token 的来源不同：Server 创建 Enrollment 后只返回一次 Token，运维系统负责把它写入独立的临时
Kubernetes Secret，并由 Agent Deployment 将其中的 `token` Key 只读挂载到
`/var/run/secrets/zke-enrollment/token`。这里的路径由 Pod VolumeMount 决定，不是 Kubernetes Secret 的资源名；
Agent 只读取文件，不通过 Kubernetes API 查询这个临时 Secret，因此无需相应的 Secret `get` 权限。完整身份存在
后，Agent 不再读取注册 Token，临时 Secret 应由运维系统删除。

当前仓库还没有提供 Helm Chart 或 Kubernetes Deployment/RBAC 清单。身份 Secret 已由 Agent 自动创建；一次性
Token Secret、VolumeMount 和实际资源管理 RBAC 仍必须由部署者准备，部署清单自动化属于待实现范围。
注册后的 QUIC/mTLS 主动连接、证书身份与 `ClientHello` 交叉校验、`ServerHello`、心跳确认、有界重连和
`last_seen_at` 限频持久化已经实现。HTTP API 使用 TCP；QUIC 使用 UDP，并复用 `http.address` 的主机与数字端口。
任务路由、业务 Stream、证书续期、撤销后的现有连接关闭以及对外 Agent 在线状态查询仍未实现。

Agent 为固定身份 Secret、注册 Token 路径、注册重试参数和日志级别提供默认值，但示例配置会显式展示这些部署
约定，避免隐藏运维依赖。Agent 默认使用 Pod 内的 InCluster Kubernetes 配置；本地开发或特殊环境可以显式设置
`kubeconfig_file`，未设置时回退到 `KUBECONFIG` 或 `~/.kube/config`。显式文件始终优先于环境自动识别。
顶层 `server_ca_file` 只用于可选的 HTTP API HTTPS；`connection.server_ca_file` 用于验证 QUIC Server 身份，
两者都与 Kubernetes API 使用的 CA 无关。`enrollment_token_file` 也可以在非标准挂载场景覆盖默认路径。

## 连接模型

Agent 必须主动连接 Server，不要求 Server 直接访问 Kubernetes API Server。这一连接模型计划适用于：

- 私有网络；
- 混合云；
- 多云；
- 边缘集群；
- 无法由 Server 主动访问的 Kubernetes 集群。

集群操作由目标集群中的 Agent 执行。Server 负责认证、授权、目标确认、任务下发与审计，不应绕过 Agent 直接执行未受控操作。
