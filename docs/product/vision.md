# 产品愿景与设计原则

ZKE（Z Kubernetes Engine）是一款构建在 Kubernetes 之上的云原生管理平台，面向多集群管理、容器服务与统一可观测性场景。

ZKE 不是 Linux 发行版，也不是 Kubernetes 的替代品。它是构建在 Kubernetes 之上的 Kubernetes 管理与算力平台。

## 设计原则

1. **以 Kubernetes 为统一基础设施底座**：工作负载最终运行在具体 Kubernetes 集群中。
2. **Server + Agent 管理多集群**：Agent 主动连接 Server，适应私有网络、混合云、多云和边缘环境。
3. **领域能力分层**：将容器与可观测性能力组织在同一平台内。
4. **全局查看、定域执行**：跨集群汇总信息，实际操作必须落到明确的集群与资源。
5. **默认受控与可审计**：敏感操作需要验证权限、确认目标、评估影响并记录审计日志。

> **作用域原则：全局观察，按集群执行。**

## 适用场景

- 企业多 Kubernetes 集群统一管理
- 私有云和混合云 Kubernetes 管理
- Kubernetes 容器服务与资源管理
- Kubernetes 可观测性
