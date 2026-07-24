# Agent 管理

Agent 管理是多集群应用，用户无需预先选择集群。

规划能力包括：

- 集群接入与 Agent 安装引导；
- Agent 在线状态、版本、升级与最后心跳时间；
- 集群连接状态和基本信息；
- Agent 日志、配置与连接诊断；
- 多集群统一管理、集群分组与标签管理；
- Agent 权限控制。

Agent 主动连接 ZKE Server，不要求 Server 直接访问 Kubernetes API Server。更多信息参见 [Server + Agent 架构](../architecture/server-agent.md)。

## 当前实现进度

Server 已实现 `POST /api/v1/projects/{project_id}/agent-enrollments`，用于由具备
`agent.enrollment.create` 权限的用户创建 15 分钟有效的一次性 Agent 注册凭证。接口要求有效 Session 和 CSRF
Token，Project 归属由 Server 解析；注册 Token 明文只返回一次，数据库只保存 SHA-256 摘要，并同步记录成功审计。
请求还必须携带 16 至 128 字符的 `Idempotency-Key`；重复 Key 返回 `409 idempotency_conflict`，不会生成额外
凭证。Project 权限拒绝和创建失败会在数据库可用且请求 Deadline 尚未耗尽时记录安全审计。

Server 已实现 `POST /agent-api/v1/enroll`。Agent 通过 Bearer 注册 Token、`Idempotency-Key`、CSR、集群名称、
Agent 版本和协议版本发起注册。Server 校验 Token 与 CSR，由配置的 Agent CA 签发 ClientAuth 证书，并以单个
事务创建 Cluster、Agent 与证书元数据、消费凭证、保存幂等响应和成功审计；签发或持久化失败会记录失败审计，
并保留可重试的注册尝试。证书身份显式绑定 Tenant、Project、Cluster 和 Agent，Agent 私钥不会发送给 Server。

相同幂等键与 CSR 可以恢复已有结果，换用 CSR 会被拒绝，过期、撤销或已由其他尝试消费的凭证不能继续使用。
接口限制 128 KiB 请求正文、拒绝未知字段、按来源限流且默认要求 TLS；只允许在监听回环地址时显式开启本地明文
开发模式。未配置 Agent CA 时 Server 仍可启动，但注册接口返回服务不可用。

Agent 侧生成私钥、调用注册接口、将证书写入 Kubernetes Secret，以及注册后的 QUIC/mTLS 主动连接仍属于后续
实现范围。
