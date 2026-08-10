# 系统架构

> 状态：目标架构。当前已完成 Phase 1 Server 与 Agent 后端闭环，包括本地认证与 RBAC、Agent 注册与安装
> Manifest、QUIC/mTLS 连接、心跳、证书续期、撤销、实时状态、用户权限和审计 API。
> Phase 2 已完成 Resource Stream、Node 类型化读取、受控通用 Kubernetes CRUD 基座、Namespace
> 管理闭环、Pod 类型化查询/删除与 Console 确认闭环，以及五类工作负载的类型化查询、创建、常用变更与
> Console 确认闭环；其余集群资源管理和平台组件仍处于规划阶段。

ZKE 的目标架构采用 Server + Agent 模型。ZKE Server 负责统一控制与编排；每个 Kubernetes 集群中的 ZKE
Agent 负责资源查询和定域执行。

```mermaid
flowchart TB
    Client["用户或平台客户端"] --> Server["ZKE Server"]

    Server --> Observability["可观测性系统<br/>VictoriaMetrics / VictoriaLogs / Grafana"]

    Server <--> AgentA["ZKE Agent A"]
    Server <--> AgentB["ZKE Agent B"]
    Server <--> AgentN["ZKE Agent N"]

    AgentA <--> ClusterA["Kubernetes Cluster A"]
    AgentB <--> ClusterB["Kubernetes Cluster B"]
    AgentN <--> ClusterN["Kubernetes Cluster N"]

    AgentA --> Observability
    AgentB --> Observability
    AgentN --> Observability
```

## 规划数据流

- 用户或平台客户端通过 ZKE Server API 使用当前已实现的平台能力。
- ZKE Server 计划通过各集群内的 ZKE Agent 查询资源、下发任务和执行已授权操作。
- Agent 计划收集或转发携带集群标识的指标、日志与事件。
- 可观测性系统计划汇总多集群遥测数据，为平台应用提供分析依据。

## 延伸阅读

- [Server + Agent 架构](server-agent.md)
- [应用作用域与资源模型](resource-model.md)
- [安全与权限](../security/authorization.md)
