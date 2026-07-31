# 系统架构

> 状态：目标架构。当前已完成 Phase 1 Server 与 Agent 后端闭环，包括本地认证与 RBAC、Agent 注册与安装
> Manifest、QUIC/mTLS 连接、心跳、证书续期、撤销、实时状态、用户权限和审计 API。
> Phase 2 已完成 Resource Stream、Node 类型化读取、受控通用 Kubernetes CRUD 基座、Namespace
> 管理闭环，以及五类工作负载的类型化查询、创建、常用变更与 Console 确认闭环；其余集群资源管理和平台组件
> 仍处于规划阶段。

ZKE 的目标架构采用 Server + Agent 模型。ZKE Server 负责统一控制与编排；每个 Kubernetes 集群中的 ZKE
Agent 负责资源查询和定域执行。

```mermaid
flowchart TB
    Client["用户或平台客户端"] --> Server["ZKE Server"]

    Server <--> Copilot["ZKE Copilot"]
    Server --> Gateway["模型 API Gateway<br/>OpenAI-compatible API"]
    Copilot --> Gateway
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
    Observability --> Copilot
```

## 规划数据流

- 用户或平台客户端通过 ZKE Server API 使用当前已实现的平台能力。
- ZKE Server 计划通过各集群内的 ZKE Agent 查询资源、下发任务和执行已授权操作。
- Agent 计划收集或转发携带集群标识的指标、日志与事件。
- 可观测性系统计划汇总多集群遥测数据，并为平台应用和 ZKE Copilot 提供分析依据。
- ZKE Copilot 计划联合资源状态与遥测数据进行分析；需要执行的操作仍由目标集群 Agent 完成。
- 模型 API Gateway 计划为模型服务提供统一访问入口，调用方无需直接感知底层 Pod。

## 延伸阅读

- [Server + Agent 架构](server-agent.md)
- [应用作用域与资源模型](resource-model.md)
- [安全与权限](../security/authorization.md)
