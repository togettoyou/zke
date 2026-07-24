# Agent 管理

Agent 管理是多集群应用，用户无需预先选择集群。

规划能力包括：

- 集群接入与 Agent 安装引导；
- Agent 在线状态、版本、升级与最后心跳时间；
- 集群连接状态和基本信息；
- Agent 日志、配置与连接诊断；
- 多集群统一管理、集群分组与标签管理；
- Agent 权限控制。

Agent 主动连接 ZKE Server，不要求 Server 直接访问 Kubernetes API Server。总体模型参见
[Server + Agent 架构](../architecture/server-agent.md)，注册、证书与连接过程参见
[Agent 注册与连接](../architecture/agent-enrollment-and-connection.md)。

## 当前实现进度

Server 已实现 `POST /api/v1/projects/{project_id}/agent-enrollments`，用于由具备
`agent.enrollment.create` 权限的用户创建 15 分钟有效的一次性 Agent 注册凭证。接口要求有效 Session 和 CSRF
Token，并在请求正文中指定集群名称。Project 归属由 Server 解析，集群名称随注册凭证持久化；注册 Token 明文只
返回一次，数据库只保存 SHA-256 摘要，并同步记录成功审计。
请求还必须携带 16 至 128 字符的 `Idempotency-Key`；重复 Key 返回 `409 idempotency_conflict`，不会生成额外
凭证。Project 权限拒绝和创建失败会在数据库可用且请求 Deadline 尚未耗尽时记录安全审计。

Server 已实现 `POST /agent-api/v1/enroll`。Agent 通过 Bearer 注册 Token、`Idempotency-Key`、CSR、Agent
版本和协议版本发起注册，不提交或覆盖集群名称。Server 从注册凭证读取由用户预先指定的名称，校验 Token 与
CSR，由配置的 Agent Client CA 签发 ClientAuth 证书，并以单个
事务创建 Cluster、Agent 与证书元数据、消费凭证、保存幂等响应和成功审计；签发或持久化失败会记录失败审计，
并保留可重试的注册尝试。证书身份显式绑定 Tenant、Project、Cluster 和 Agent，Agent 私钥不会发送给 Server。

相同幂等键与 CSR 可以恢复已有结果，换用 CSR 会被拒绝，过期、撤销或已由其他尝试消费的凭证不能继续使用。
接口限制 128 KiB 请求正文、拒绝未知字段并按来源限流。ZKE Server 可选原生 HTTP TLS；生产环境必须使用
Server 原生 HTTPS 或由上游网关终止 TLS，包含注册 Token 的明文 HTTP 不得直接暴露到不可信网络。

Agent 已实现首次注册流程：在集群内生成 ECDSA P-256 私钥和 CSR，自行创建固定名称 Kubernetes Secret 并写入
私钥、原始 CSR 与幂等键，再调用注册接口。网络错误、`429` 和 Server `5xx` 会使用相同 CSR 与幂等键重试；
成功响应中的证书、Cluster ID、Agent ID 和过期时间会原子写回同一 Secret。Agent 重启后直接复用完整身份，
不再读取一次性 Token；部分写入、私钥与证书不匹配、证书作用域错误或证书过期都会拒绝启动。

注册后的 QUIC/mTLS 主动连接、Hello、心跳和重连已经实现。Server 使用证书序列号和 URI SAN 校验 Agent 身份，
并在首次有效连接后激活 Cluster 与 Agent；心跳限频更新健康状态和 `last_seen_at`。Agent 会在证书到期前通过
Control Stream 自动续期，持久化 CSR 以支持幂等恢复，并在新证书连接成功后撤销旧 Credential。Credential、
Agent 或 Cluster 被撤销时，Server 通过 PostgreSQL 通知关闭现有连接；连接也不会越过客户端证书自然过期时间。
业务任务 Stream、Web 展示、Helm Chart 和升级管理仍属于后续实现范围。
Agent ServiceAccount 需要 Secret 的 `create` 权限，对固定的 Enrollment、Trust 和 identity Secret 具有
`get` 权限，并只能更新 identity Secret。注册 Token 只保存在独立 Secret 中，不能写入 Agent YAML、日志或
身份 Secret；Agent 通过 client-go 定域读取它。

Server 已实现 `POST /api/v1/projects/{project_id}/agent-installations` 和 Bearer 保护的
`GET /agent-install/v1/manifest`。前者返回可直接执行的 `curl | kubectl apply` 命令；后者生成 Namespace、
Enrollment/Trust Secret、ConfigMap、ServiceAccount、最小 Role/RoleBinding 和 Deployment。资源包不创建
Service、PVC 或 identity Secret。Enrollment Secret 保留不会导致重复注册，因为 Agent 重启优先使用 identity
Secret，Server 也已单次消费 Token。

Server 已实现 `GET /api/v1/projects/{project_id}/agents`，返回 Agent 当前证书过期时间、剩余秒数和证书状态，
供后续 Web 使用；同时按配置周期输出临近过期的结构化告警。

HTTP 注册与 QUIC 长连接使用独立端点。Server 分别配置 `http.address` 和 `agent_listener.address`；Agent 分别
配置 `registration.server_url` 和 `connection.server_address`，不从注册 URL 隐式派生 QUIC 地址。

集群名称当前是便于用户识别的显示名称，不承担唯一身份语义，也不要求唯一；Server 生成的 `cluster_id` 才是
跨接口、权限和 Agent 身份绑定使用的唯一标识。
