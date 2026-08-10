# ZKE 文档

这里收录 ZKE 的产品、架构、功能、安全与开发规划文档。

> ZKE 当前处于开发预览阶段。文档同时描述已经实现的行为与后续规划；具体状态以功能文档和 Roadmap 为准。当前版本不适用于生产环境，产品范围与技术选型仍可能调整。

## 产品

- [产品愿景与设计原则](product/vision.md)

## 部署

- [Docker、Kubernetes 与 Helm 部署](deployment.md)

## 架构

- [系统架构](architecture/overview.md)
- [Server + Agent 架构](architecture/server-agent.md)
- [Agent 注册与连接](architecture/agent-enrollment-and-connection.md)
- [Phase 2 Server–Agent 协议设计](architecture/agent-protocol-phase-2.md)
- [应用作用域与资源模型](architecture/resource-model.md)
- [技术基础设计](architecture/technical-foundation.md)

## 功能

- [集群接入管理](features/agent-management.md)
- [容器服务](features/container-service.md)
- [终端](features/terminal.md)
- [可观测性平台](features/observability.md)

## 安全

- [安全与权限](security/authorization.md)
- [Server OpenAPI 3.1 契约](../api/openapi/zke-server.v1.yaml)

## 规划

- [Roadmap](roadmap.md)
