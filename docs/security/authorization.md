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
密码摘要、Session Token 摘要和独立的 CSRF Token 摘要。Server 启动时只在用户表为空时从受保护密码文件创建
首个管理员；已有用户时跳过初始化。

认证 API 使用统一登录错误、请求体上限、账户与直接网络来源限流、Argon2id 全局并发上限、Server 端 Session、
Cookie 属性、Synchronizer CSRF Token、应用层操作超时和 Go 标准库跨源保护。密码凭证版本校验、可选摘要参数升级、
Session 创建与成功审计在同一事务中完成；登录成功、失败、限流拒绝与注销均写入不包含凭证明文的审计事件。

RBAC 基础已经实现固定权限、`admin/viewer` 角色矩阵、Global/Tenant/Project RoleBinding 继承规则、默认拒绝、
Project 归属解析和 HTTP 授权 middleware。Global `admin` 拥有全部固定权限；`viewer` 只拥有 Cluster 与 Agent
读取权限。Tenant 绑定只向下覆盖同一 Tenant，Project 绑定只覆盖目标 Project，跨作用域访问会被拒绝。

当前固定权限还包括 `user.read`、`user.manage`、`rbac.read`、`rbac.manage` 和 `audit.read`。Phase 1 的用户与
RoleBinding 管理入口只允许 Global `admin` 使用，避免在委派规则尚未扩展前出现权限提升；创建的 RoleBinding
仍可绑定 Global、Tenant 或 Project 作用域。Server 提供用户列表、详情、创建、启用/禁用、解锁和管理员密码
重置 API，以及 RoleBinding 列表、幂等创建和删除 API。禁止当前用户禁用自身，也禁止禁用或移除最后一个有效的
Global `admin`。权限授予、权限移除、用户状态变更、解锁和密码重置均要求显式确认；禁用、锁定和密码重置都会
撤销目标用户现有 Session。

账户错误密码计数和锁定期限持久化在 PostgreSQL，不因 Server 重启丢失。达到配置阈值后账户进入 `locked`，
现有 Session 被撤销；锁定期满后的首次正确登录会自动恢复，Global 管理员也可显式解锁。登录错误、账户锁定、
自动恢复、管理员解锁和密码重置均写入不包含密码的审计事件。

RBAC 已接入 Tenant/Project 创建、Cluster/Agent 定域查询、
`POST /api/v1/projects/{project_id}/agent-enrollments`、
`POST /api/v1/projects/{project_id}/agent-installations` 和
`GET /api/v1/projects/{project_id}/agents`，并已接入
`POST /api/v1/agents/{agent_id}/revoke`。Tenant/Project 创建分别要求 Global `tenant.create` 和 Tenant
`project.create`；Agent 接入创建接口要求 `agent.enrollment.create`；状态查询要求 `cluster.read` 或
`agent.read`。所有变更还要求有效 Session 和 CSRF Token。Project 的 Tenant 归属由 Server 查询，不接受调用方
提供。一次性注册 Token 创建请求同时指定集群名称，该名称持久化在 Server 的 Enrollment 中，Agent 消费 Token
时不能覆盖。Token 明文只返回一次，数据库只保存 SHA-256 摘要，凭证与成功审计在同一事务写入。创建接口强制
使用 `Idempotency-Key`，重复 Key 不会创建额外资源或重复成功审计。安装 Manifest 下载和 Agent 注册不使用用户
Session，而是使用创建时已经绑定 Project 的一次性 Bearer Token。

Agent 撤销接口按 Agent ID 解析 Project 作用域，要求 `agent.revoke` 权限、CSRF Token 和请求正文中的显式确认。
撤销状态、全部客户端 Credential 和成功审计在同一事务内处理；权限拒绝、确认缺失和执行失败分别记录
`denied` 或 `failed` 审计。接口不会接受调用方提供 Tenant、Project 或 Cluster 作用域。

Project 授权拒绝以及凭证创建的输入、状态和内部失败会写入不含 Token 的审计事件；数据库不可用或请求 Deadline
已耗尽时降级为安全错误日志。Tenant/Project 创建与权限范围列表、Cluster 列表/详情、Cluster Agent 详情以及
Global/Tenant/Project/Cluster 授权拒绝审计已经实现。`GET /api/v1/audit-events` 按调用者 `audit.read`
RoleBinding 的 Global、Tenant 或 Project 可见范围过滤结果，支持条件过滤和基于游标的有界分页。用户、
RoleBinding、账户恢复和密码重置的成功、失败与权限拒绝也会写入审计。

可信反向代理来源解析、Console 登录流程和跨组织的细粒度委派管理仍属于后续工作。Phase 1 后端认证与 RBAC
闭环已经实现，但项目仍处于早期开发阶段，不适用于生产环境。
