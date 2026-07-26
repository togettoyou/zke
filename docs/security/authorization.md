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
Project/Cluster 归属解析和 HTTP 授权 middleware。Global `admin` 拥有全部固定权限；`viewer` 只拥有 Tenant、
Project 和 Cluster 读取权限。Tenant 绑定只向下覆盖同一 Tenant，Project 绑定只覆盖目标 Project，跨作用域
访问会被拒绝。

当前固定权限还包括 `user.read`、`user.manage`、`rbac.read`、`rbac.manage` 和 `audit.read`。Phase 1 的用户与
RoleBinding 管理入口只允许 Global `admin` 使用，避免在委派规则尚未扩展前出现权限提升；创建的 RoleBinding
仍可绑定 Global、Tenant 或 Project 作用域。Server 提供用户列表、详情、创建、修改显示名称、启用/禁用、
逻辑删除、解锁和管理员密码重置 API，以及 RoleBinding 列表、详情、幂等创建和删除 API。RoleBinding 是不可变
授权关系，修改通过删除后重新创建完成。禁止当前用户禁用或删除自身，也禁止禁用、删除或移除最后一个有效的
Global `admin`。权限授予、权限移除、用户状态变更、删除、解锁和密码重置均要求显式确认；禁用、删除、锁定和
密码重置都会撤销目标用户现有 Session。

账户错误密码计数和锁定期限持久化在 PostgreSQL，不因 Server 重启丢失。达到配置阈值后账户进入 `locked`，
现有 Session 被撤销；锁定期满后的首次正确登录会自动恢复，Global 管理员也可显式解锁。登录错误、账户锁定、
自动恢复、管理员解锁和密码重置均写入不包含密码的审计事件。

RBAC 已接入 Tenant、Project、Cluster 的管理生命周期和 Cluster 聚合查询。固定资源权限包括
`tenant.create`、`tenant.read`、`tenant.manage`、`project.create`、`project.read`、`project.manage`、
`cluster.read`、`cluster.manage`、
`cluster.enrollment.create`、`cluster.enrollment.read`、`cluster.enrollment.revoke` 和
`cluster.connection.revoke`。所有变更要求有效 Session 和 CSRF Token；创建 Enrollment 和重新接入还要求
`Idempotency-Key`。Project、Cluster 的归属由 Server 查询，不接受调用方覆盖。

管理端不暴露独立 Agent 资源。连接身份属于 Cluster 聚合内部状态，连接撤销接口
`POST /api/v1/clusters/{cluster_id}/connection/revoke` 按 Cluster ID 解析 Project 作用域，要求
`cluster.connection.revoke` 和显式确认。重新接入接口
`POST /api/v1/clusters/{cluster_id}/connection/reenroll` 仅在当前内部身份撤销后创建绑定原 `cluster_id` 的
一次性凭证。Cluster 逻辑删除使用 `cluster.manage`，同时撤销全部内部身份和 Credential。

Project 授权拒绝以及凭证创建的输入、状态和内部失败会写入不含 Token 的审计事件；数据库不可用或请求 Deadline
已耗尽时降级为安全错误日志。Tenant/Project/Cluster 生命周期、Cluster 列表/详情以及
Global/Tenant/Project/Cluster 授权拒绝审计已经实现。`GET /api/v1/audit-events` 按调用者 `audit.read`
RoleBinding 的 Global、Tenant 或 Project 可见范围过滤结果，支持条件过滤和基于游标的有界分页。用户、
RoleBinding、账户恢复和密码重置的成功、失败与权限拒绝也会写入审计。

可信反向代理来源解析和跨组织的细粒度委派管理仍属于后续工作。Phase 1 认证、用户与 RoleBinding 管理以及
审计查询后端已经实现，但项目仍处于早期开发阶段，不适用于生产环境。
