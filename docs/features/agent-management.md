# 集群接入管理

集群接入管理是多集群应用，用户无需预先选择集群。

## Cluster 与 Agent 的统一语义

管理端只暴露 `Cluster` 资源。一个接入 ZKE 的 Kubernetes Cluster 与部署在其中的 ZKE Agent 构成同一个
管理共同体：Cluster 是稳定的外部资源和权限目标，Agent 是该 Cluster 当前使用的内部连接与执行身份。

数据库仍分别保存 `clusters`、`agents` 和 `agent_credentials`，以保留重新接入、证书轮换、撤销和审计历史；
这些内部 Agent ID 不出现在管理 API。重新接入会保留原 `cluster_id`，撤销旧内部身份并创建一个新的内部身份，
同一 Cluster 同一时刻最多只有一个未撤销 Agent。

Agent 主动连接 ZKE Server，不要求 Server 直接访问 Kubernetes API Server。总体模型参见
[Server + Agent 架构](../architecture/server-agent.md)，注册、证书与连接过程参见
[Agent 注册与连接](../architecture/agent-enrollment-and-connection.md)。

## 当前已实现

### 接入凭证和安装

- `POST /api/v1/projects/{project_id}/cluster-enrollments` 创建 15 分钟有效的一次性接入凭证；
- `GET /api/v1/projects/{project_id}/cluster-enrollments` 查询凭证列表；
- `GET /api/v1/projects/{project_id}/cluster-enrollments/{enrollment_id}` 查询凭证详情；
- `DELETE /api/v1/projects/{project_id}/cluster-enrollments/{enrollment_id}` 撤销未消费的凭证；
- `POST /api/v1/projects/{project_id}/cluster-installations` 生成一键安装命令；
- `GET /agent-install/v1/manifest` 使用 Bearer Token 获取 Kubernetes Manifest。

创建接口要求 Session、CSRF、`cluster.enrollment.create` 权限和 16 至 128 字符的
`Idempotency-Key`。Token 明文只返回一次，数据库只保存 SHA-256 摘要；凭证与 Project、集群显示名称绑定。
查询和撤销分别要求 `cluster.enrollment.read` 与 `cluster.enrollment.revoke`。已消费凭证不可撤销。

### 首次注册和主动连接

`POST /agent-api/v1/enroll` 是内部 Agent 注册端点。Agent 在集群内生成 ECDSA P-256 私钥和 CSR，Server 从
Enrollment 读取作用域和集群名称，原子创建 Cluster、内部 Agent 身份、Credential、幂等结果和审计记录。
Agent 私钥不会发送给 Server。

完成注册后，Agent 使用 QUIC/mTLS 主动连接 Server，执行 Hello、心跳、断线重连和证书自动续期。首次有效连接
激活 Cluster；状态 API 将数据库状态与当前 Server 实例内存中的连接快照合并。

### Cluster 聚合查询和生命周期

- `GET /api/v1/projects/{project_id}/clusters` 查询 Project 内的 Cluster；
- `GET /api/v1/clusters/{cluster_id}` 查询 Cluster 及其 `connection`；
- `PUT /api/v1/clusters/{cluster_id}` 修改显示名称；
- `DELETE /api/v1/clusters/{cluster_id}` 删除 Cluster 记录及其内部身份与全部 Credential，不可恢复；
- `PUT /api/v1/clusters/{cluster_id}` 可将 `status` 置为 `suspended` 临时停用，或置回 `active` 恢复；
  停用不撤销任何身份或凭证，恢复后 Agent 以原身份自动重连；
- `POST /api/v1/clusters/{cluster_id}/connection/revoke` 撤销当前连接身份；
- `POST /api/v1/clusters/{cluster_id}/connection/reenroll` 为同一 Cluster 创建重新接入凭证；
- `GET /api/v1/events` 发送权限过滤后的 `cluster.status` SSE 事件。

连接信息嵌套在 Cluster 的 `connection` 字段中，包括生命周期、健康状态、Agent/协议版本、证书状态、
`online`/`offline` 状态、Connection ID、最近心跳和断开原因。外部响应不返回内部 `agent_id`。

连接撤销要求 `cluster.connection.revoke` 和显式 `{"confirm":true}`；它不会删除 Cluster。重新接入仅允许在
当前连接身份已撤销后执行，要求 `cluster.enrollment.create`、确认和幂等键，并始终复用原 `cluster_id`。
Cluster 删除使用 `cluster.manage`，会真正移除 Cluster 及其内部 Agent、Credential 和绑定的 Enrollment，
不可恢复，也不可重新接入。

Tenant、Project、User 和 RoleBinding 的 Phase 1 管理生命周期也已实现。Tenant 与 Project 停用只更新自身
`status`，立即断开作用域内的 Agent，但保留 Enrollment、Agent 身份和 Credential，恢复后 Agent 自动重连；
删除则真正移除其下所有资源，审计事件仍保留删除时的名称快照。

## 当前限制

- 连接快照不写数据库，Server 重启后离线断开详情会丢失；
- 多 Server 实例尚未汇总全局连接视图和任务路由；
- Agent 离线直至证书过期后，需要执行 Cluster 重新接入；
- Agent 专用 Helm 升级、日志、配置与连接诊断仍属于后续规划；
- 项目仍处于开发预览阶段，不适用于生产环境。

集群显示名称只用于识别且允许修改，不承担唯一身份语义；`cluster_id` 是跨接口、权限和内部身份绑定使用的稳定
唯一标识。
