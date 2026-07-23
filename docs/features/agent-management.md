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

