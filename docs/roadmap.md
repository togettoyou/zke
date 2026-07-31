# Roadmap

Roadmap 表示当前规划，不代表发布时间或交付承诺。所有条目均可能随产品设计和技术验证调整。

已勾选条目表示该能力已实现，不代表已具备生产可用性或水平扩展能力。当前 ZKE Server 按单副本部署模型
实现，详见[技术基础设计](architecture/technical-foundation.md)。

## Phase 1：平台基础

- [x] Server、Agent 可运行工程骨架
- [x] PostgreSQL 本地开发环境、版本化迁移与 Phase 1 最小数据模型
- [x] 首个管理员自动引导、本地登录、Session 与 CSRF 安全基础
- [x] Global、Tenant、Project RoleBinding 与 Project HTTP 授权基础
- [x] Cluster Enrollment、一次性 Token、凭证查询/撤销与一键安装 Manifest
- [x] Agent identity Secret、QUIC/mTLS、Hello、心跳与断线重连
- [x] Agent 客户端证书自动续期、Cluster 连接撤销/重新接入、通知断连与实时连接/证书状态 API
- [x] Server Managed Agent PKI 初始化与 Listener 叶子证书自动续期
- [x] Tenant、Project 与 Cluster 查询、更新、停用/恢复与删除生命周期 API
- [x] Cluster 聚合外部模型，内部 Agent 身份不独立暴露
- [x] 用户完整管理生命周期、RoleBinding 查询/创建/删除、账户锁定恢复与管理员密码重置 API
- [x] Console 权限能力发现、当前用户自助改密与管理列表分页筛选
- [x] 权限定域审计查询、Cluster 状态 SSE 与 OpenAPI 3.1 契约
- [x] ZKE Server Phase 1 后端
- [x] ZKE Agent Phase 1 身份与连接
- [x] 用户认证后端
- [x] RBAC 后端
- [x] 集群接入

## Phase 2：容器服务

- [x] Server–Agent 业务 Stream 传输内核
- [x] Node List/Detail dynamic client、类型化 HTTP API 与真实 QUIC 资源闭环
- [x] Kubernetes Discovery、通用只读 List/Get API 与任意 CRD 资源闭环
- [x] 通用主资源 Create/Update/Patch/Delete、安全写权限、审计与真实集群 E2E
- [x] Console 容器服务集群选择
- [x] 节点列表/详情 Console 页面与调度开关（驱逐尚未支持）
- [x] Namespace List/Detail/Create/Delete、DryRun、确认、权限、审计与 Console 闭环
- [x] 工作负载类型化创建、List/Detail、伸缩、滚动重启、CronJob 暂停/恢复、删除，以及 Console
  列表/详情、Namespace 作用域选择器与全部变更的 DryRun、确认、幂等和审计闭环（高级 Pod 配置和
  类型化更新表单尚未支持）
- [ ] Pod 管理
- [ ] Pod 日志
- [ ] Web Terminal
- [ ] YAML 管理
- [ ] Kubernetes Event

## Phase 3：可观测性

- [ ] VictoriaMetrics 集成
- [ ] VictoriaLogs 集成
- [ ] Grafana 集成
- [ ] 多集群指标
- [ ] 多集群日志
- [ ] 告警中心
- [ ] 集群标签体系

## Phase 4：作业平台

- [ ] Volcano 与 Kueue 选型
- [ ] 作业管理
- [ ] 作业队列
- [ ] GPU 作业
- [ ] 分布式训练
- [ ] 配额与优先级
- [ ] 作业日志
- [ ] 作业状态跟踪

## Phase 5：算力平台

- [ ] 多集群算力总览
- [ ] GPU 资源管理
- [ ] 算力池
- [ ] 租户与项目配额
- [ ] 手动选择目标集群
- [ ] 模型服务部署
- [ ] 推理实例管理
- [ ] OpenAI-compatible API
- [ ] API Key
- [ ] 调用统计

## Phase 6：智能调度与多集群模型服务

- [ ] 集群推荐
- [ ] 算力池调度
- [ ] 自动选择集群
- [ ] 多集群模型部署
- [ ] 模型流量路由
- [ ] 故障转移
- [ ] 跨集群资源策略

## Phase 7：ZKE Copilot

- [ ] 自然语言查询
- [ ] 多集群分析
- [ ] 日志、指标与事件联合分析
- [ ] 根因分析
- [ ] 修复建议
- [ ] 用户确认
- [ ] Agent 执行
- [ ] 结果验证
- [ ] 操作审计
