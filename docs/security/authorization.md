# 安全与权限

ZKE 的安全模型仍在设计中，当前遵循以下原则：

- **最小权限**：用户、服务与 Agent 只获得完成任务所需的权限。
- **作用域明确**：所有查询和操作均关联租户、项目、集群及必要的 Namespace。
- **服务端校验**：不依赖前端隐藏操作，权限必须在服务端执行校验。
- **目标确认**：敏感操作必须展示目标集群、资源对象、操作内容和潜在影响。
- **人工确认**：AI 建议或生成的变更不能绕过用户确认。
- **Agent 定域执行**：集群操作由目标集群中的 Agent 执行。
- **全程审计**：记录操作发起者、目标、请求、结果和必要的分析依据。
- **凭证保护**：Kubernetes 凭证、API Key 和 Secret 不应以明文出现在日志、界面或 AI 上下文中。

## 权限边界

不同用户只能查看和操作其权限范围内的资源。所有跨集群查询均需遵守租户、项目和 RBAC 权限边界，全局视图不代表全局操作权限。

AI 发起的操作与用户直接发起的操作遵守相同权限规则，并额外要求展示分析依据、操作内容、目标资源和潜在影响。

当前项目尚未通过任何安全、云原生或 Kubernetes 认证，也不对生产可用性作出承诺。

## 当前实现状态

Phase 1 已实现本地用户密码安全基础、首个管理员初始化事务、用户与会话数据访问层，以及 Server 端
`login`、`logout`、`me` 认证 API。首个管理员拥有 Global `admin` RoleBinding。数据库只保存 Argon2id
密码摘要、Session Token 摘要和独立的 CSRF Token 摘要。

认证 API 使用统一登录错误、请求体上限、账户与直接网络来源限流、Argon2id 全局并发上限、Server 端 Session、
Cookie 属性、Synchronizer CSRF Token、应用层操作超时和 Go 标准库跨源保护。密码凭证版本校验、可选摘要参数升级、
Session 创建与成功审计在同一事务中完成；登录成功、失败、限流拒绝与注销均写入不包含凭证明文的审计事件。

RBAC 基础已经实现固定权限、`admin/viewer` 角色矩阵、Global/Tenant/Project RoleBinding 继承规则、默认拒绝、
Project 归属解析和 HTTP 授权 middleware。Global `admin` 拥有全部固定权限；`viewer` 只拥有 Cluster 与 Agent
读取权限。Tenant 绑定只向下覆盖同一 Tenant，Project 绑定只覆盖目标 Project，跨作用域访问会被拒绝。

RBAC 已接入 `POST /api/v1/projects/{project_id}/agent-enrollments`：该接口同时要求有效 Session、CSRF Token
和 `agent.enrollment.create` 权限；Project 的 Tenant 归属由 Server 查询，不接受调用方提供。一次性注册 Token
创建请求同时指定集群名称，该名称持久化在 Server 的 Enrollment 中，Agent 消费 Token 时不能覆盖。Token
明文只返回一次，数据库只保存 SHA-256 摘要，凭证与成功审计在同一事务写入。接口强制使用
`Idempotency-Key`，重复 Key 不会创建额外凭证或重复成功审计。Project 授权拒绝以及凭证创建的输入、状态和内部
失败会写入不含 Token 的审计事件；数据库不可用或请求 Deadline 已耗尽时降级为安全错误日志。

其他 Project、Cluster 或 Agent 业务 API 尚未实现。持久化账户锁定与恢复、管理员密码重置、可信反向代理来源解析、
Console 登录流程、Global/Tenant 授权拒绝的持久化审计和敏感操作确认也尚未实现；因此当前实现仍不能描述为完整
认证或 RBAC 系统，也不适用于生产环境。
