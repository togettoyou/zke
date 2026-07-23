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

## 连接模型

Agent 必须主动连接 Server，不要求 Server 直接访问 Kubernetes API Server。这一连接模型计划适用于：

- 私有网络；
- 混合云；
- 多云；
- 边缘集群；
- 无法由 Server 主动访问的 Kubernetes 集群。

集群操作由目标集群中的 Agent 执行。Server 负责认证、授权、目标确认、任务下发与审计，不应绕过 Agent 直接执行未受控操作。

