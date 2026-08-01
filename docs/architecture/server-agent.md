# Server + Agent 架构

Agent 首次注册、identity Secret、证书信任链和 QUIC/mTLS 长连接的逐步说明参见
[Agent 注册与连接](agent-enrollment-and-connection.md)。

## ZKE Server

ZKE Server 是平台统一控制端，规划负责：

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
- 按明确 Cluster/Namespace 获取 Kubernetes Event 快照与实时 Watch；
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
`get`、`update` 权限。

一次性注册 Token 的来源不同：Server 创建 Enrollment 后只返回一次 Token，运维系统负责把它写入独立的临时
Kubernetes Secret。Agent 通过 client-go 读取固定名称 `zke-agent-enrollment` 的 `token` Key，不把 Token
写入 YAML 或宿主机文件。完整身份存在后，Agent 不再读取注册 Token；Secret 可以保留以简化 Pod 重建，也可以
由外部生命周期策略清理。

当前仓库还没有提供 Helm Chart，但 Server 已能生成 Kubernetes Deployment、Secret、ConfigMap 和最小 RBAC
清单。ServiceAccount 可以在所在 Namespace 创建 Secret，对 Enrollment、Trust 和 identity Secret 具有定域的
`get` 权限，并且只能更新 identity Secret；独立 ClusterRole 授予 Node 的 `get`、`list`、`patch` 以及
Namespace 的 `get`、`list`、`create`、`delete`、Pod 的 `get`、`list`、`delete` 以及 `pods/log` 的
`get` 权限；Exec 与 Eviction Subresource 不在默认授权中。
ZKE Server 的 HTTP Listener 可选原生 TLS：同时配置 `http.tls.certificate_file` 与
`http.tls.private_key_file` 时提供 HTTPS；省略时提供 HTTP。本地明文开发只绑定回环地址，生产环境必须使用
原生 HTTPS 或由上游网关终止 TLS。
注册后的 QUIC/mTLS 主动连接、证书身份与 `ClientHello` 交叉校验、`ServerHello`、心跳确认、有界重连和
`last_seen_at` 限频持久化已经实现。Agent 会在证书进入配置的续期窗口后，通过已认证的 Control Stream 自动
续期并使用新证书重连；凭据或 Agent 身份被撤销时，PostgreSQL 通知会让所有 Server 实例关闭匹配的现有连接
并拒绝当前身份重连。Tenant、Project 或 Cluster 停用同样会立即断连，但 Agent 保持重试，恢复后复用原身份。
HTTP API 使用 `http.address` 的 TCP Listener；QUIC 使用独立
`agent_listener.address` 的 UDP Listener，两者必须分别配置。管理面把 Cluster 和其中的 Agent 视为一个
聚合资源：Server 按 Project 查询 Cluster，并在 `connection` 字段中返回内部连接身份的生命周期、健康、版本、
最后心跳、证书有效期和当前 Server 实例内存中的 `online`/`offline` 状态；管理 API 不暴露内部 Agent ID。
Server 也已提供以 Cluster ID 为目标且需要显式确认的连接撤销和重新接入 API。当前连接快照只代表处理请求的
Server 实例，重启后不保留离线历史；多实例全局连接视图和跨实例任务路由仍未实现。

Phase 2 已实现单 Server 实例内的业务 Stream 传输内核，包括双方 accept 循环、Resource Stream、能力协商、
单 Stream 取消和并发限制。Agent dynamic client 与 Server 类型化 API 已完成 Node List/Detail；Discovery 和
受控通用 CRUD API 已完成任意已授权内置主资源及 CRD 资源的真实 QUIC 闭环，包含 DryRun、四类 Patch、
删除前置条件、写能力协商和有界幂等重放。类型化 Namespace List/Detail/Create/Delete 与 Console
集群选择、DryRun/确认闭环已经实现；Deployment、StatefulSet、DaemonSet、Job 和 CronJob 已提供显式
Cluster/Namespace 定域的类型化 List/Detail API。默认 Agent RBAC 已覆盖 Namespace、Pod 和这五类工作负载；
类型化 Pod 后端提供显式 Cluster/Namespace 定域的 List/Detail，以及带 UID
前置条件、DryRun、确认、幂等和审计的删除。类型化工作负载后端还提供 Deployment/StatefulSet 伸缩、
五类工作负载创建、Deployment/StatefulSet/DaemonSet 滚动重启、CronJob 暂停/恢复以及五类工作负载删除，
并复用通用变更链路。
其他资源仍由安装方按实际管理范围显式扩展最小 RBAC。工作负载 Console 已实现列表、详情、类型化创建和上述
类型化变更的 DryRun、影响展示与确认闭环；Pod Console 已实现列表、详情和删除的 DryRun、影响展示与确认闭环；
Pod Logs 后端已通过专用权限和独立 QUIC Stream 实现有界快照与实时 Follow；跨 Server 实例任务路由以及
Watch、Exec 等流式能力仍未实现。

当前 Server 同时提供经过 Session 与 Cluster 权限过滤的 Cluster 状态 SSE。连接建立、健康变化、生命周期撤销和断开会触发
`cluster.status` 事件；该事件流只负责管理面状态通知，不是 Server–Agent 业务 Stream，也不包含
Kubernetes 资源查询。

Agent 为固定的 Enrollment、Trust 和 identity Secret 名称以及注册重试参数和日志级别提供默认值。Agent 默认
使用 Pod 内的 InCluster Kubernetes 配置；本地开发或特殊环境可以显式设置
`kubeconfig_file`，未设置时回退到 `KUBECONFIG` 或 `~/.kube/config`。显式文件始终优先于环境自动识别。
`registration.server_url`、Enrollment Secret 与可选的 `registration.ca_certificate_file` 用于 HTTP(S)
注册；`connection.server_address` 与 Trust Secret 中的 Listener CA 用于 QUIC/mTLS 长连接。特殊部署可以
用 CA 文件覆盖 Trust Secret。两类端点独立配置，信任根也不混用；它们都与 Kubernetes API 使用的 CA 无关。

Agent mTLS 使用两条独立信任链：`agent-client-ca` 签发并验证 Agent 客户端身份，`agent-listener-ca` 签发
Agent Listener 服务端证书。Managed PKI 模式在受保护的 Server PV 中保存两套 CA 与 Listener 身份，Server
首次启动自动生成，并在 Listener 叶子证书进入续期窗口时复用原私钥续期；数据库保存证书指纹，PV 丢失时拒绝
静默重建 CA。需要离线 Listener CA 或外部密钥管理时可以使用 external 模式。

## 连接模型

Agent 必须主动连接 Server，不要求 Server 直接访问 Kubernetes API Server。这一连接模型计划适用于：

- 私有网络；
- 混合云；
- 多云；
- 边缘集群；
- 无法由 Server 主动访问的 Kubernetes 集群。

集群操作由目标集群中的 Agent 执行。Server 负责认证、授权、目标确认、任务下发与审计，不应绕过 Agent 直接执行未受控操作。
