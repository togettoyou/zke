# 产品愿景与设计原则

ZKE（Z Kubernetes Engine）是 AI 原生的 Kubernetes 云操作环境。

ZKE 通过 Server + Agent 连接分散在数据中心、私有云、公有云和边缘环境中的 Kubernetes 集群，把它们收敛成一个
可以直接操作的云环境：Console 用桌面、窗口和 Dock 组织平台能力，Server + Agent 负责把每一次操作定域到目标集群，
Tenant / Project / RBAC 负责划定边界，审计与轨迹负责留痕。AIOps 作为常驻其中的运维 Agent，与人共用同一套权限、
同一条 Agent 通道和同一份审计。

ZKE 不是 Linux 发行版，不是 Kubernetes 的替代品，也不负责托管 Kubernetes 控制面。实际工作负载始终运行在明确的
目标 Kubernetes 集群中。

当前产品重点是多集群接入、安全治理、单集群容器资源管理与 AIOps；可观测性已实现指标链路，同样按单集群呈现。
日志、告警，以及 AIOps 的技能、子任务与自动化属于后续规划能力。

## 操作环境的组成

| 操作系统里的概念 | ZKE 中对应的东西 |
| --- | --- |
| 硬件 | 已接入的 Kubernetes 集群 |
| 内核与驱动 | ZKE Server 与每个集群里的 Agent；Server 不直连任何集群的 Kubernetes API Server |
| 系统调用 | 携带明确 Cluster、Namespace 与资源身份的具名操作，逐次判权并写入审计 |
| 桌面与窗口 | Console 的窗口、Dock 与多应用并行工作区 |
| 应用 | 集群接入、组织与资源、容器服务、终端、监控、项目自定义应用、访问与审计、平台配置、AIOps |
| Shell | Cluster Terminal 与 Pod 终端，按当前用户权限投影 Kubernetes RBAC |
| 用户与权限 | Tenant、Project、RBAC 三层作用域与细粒度操作权限 |
| 系统日志 | 审计事件，以及 AIOps 的 append-only 轨迹 |
| 常驻的操作者 | AIOps：模型自主工具循环、敏感操作审批、结论携带证据引用 |
| 进程调度 | 仍由 Kubernetes 自己负责，ZKE 不重新发明调度 |

## 设计原则

1. **以 Kubernetes 为统一基础设施底座**：工作负载最终运行在具体 Kubernetes 集群中。
2. **Server + Agent 管理多集群**：Agent 主动连接 Server，适应私有网络、混合云、多云和边缘环境。
3. **领域能力分层**：将集群接入、容器服务、安全审计组织为清晰的能力边界，并逐步扩展可观测性和 AIOps 能力。
4. **全局查看、定域执行**：跨集群汇总信息，实际操作必须落到明确的集群与资源。
5. **默认受控与可审计**：敏感操作需要验证权限、确认目标、评估影响并记录审计日志。
6. **AI 与人共用同一条通路**：模型不持有 kubeconfig，也不直连 Kubernetes API Server；它使用与桌面相同的
   Server + Agent 链路、相同的权限判定和相同的审计，能力上限永远是发起用户自己的 RBAC。
7. **AI 按会话 Cluster 运行**：AIOps 跟随桌面当前 Tenant/Project，每个会话固定一个 Cluster；交互任务以发起用户
   身份工作，后台自动化使用显式、可撤销的委派；两者都受当前 RBAC、目标 Cluster、审批和审计约束。
8. **AI 的每一步可复核**：写操作先 DryRun 再按预检提交，模型调用、工具参数与结果、授权判断和审批全部写入
   append-only 轨迹，结论携带可回到桌面窗口的证据引用。

> **作用域原则：全局观察，按集群执行。**

## 适用场景

- 数据中心、私有云和公有云中的多 Kubernetes 集群统一管理
- 具有独立网络边界的私有云与混合云 Kubernetes 环境
- Kubernetes 资源、工作负载与敏感运维操作的统一入口
- 统一权限边界与审计要求下的平台工程和 SRE 协作
- 跨集群的资源用量观察，以及规划中的日志、告警；AIOps 按会话固定到单个 Cluster
- 希望把巡检、故障定位与受控变更交给模型执行，同时保留权限边界、审批与完整轨迹的团队
