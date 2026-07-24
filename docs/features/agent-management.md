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

Agent 使用凭证提交 CSR、创建 Cluster 与 Agent 身份、签发客户端证书以及建立主动连接仍属于后续实现范围。
