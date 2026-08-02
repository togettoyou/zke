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
- [x] 集群概览（Node、Namespace、Pod、五类工作负载和资源请求/容量聚合，有界并发、分页上限与显式部分
  结果，以及作为容器服务默认落地页的 Console 计数、量条与部分失败提示；Warning Event 保持独立权限和
  API，概览不跨命名空间聚合事件）
- [x] 节点列表/详情 Console 页面与调度开关（驱逐尚未支持）
- [x] Namespace List/Detail/Create/Delete、DryRun、确认、权限、审计与 Console 闭环
- [x] 工作负载类型化创建、List/Detail、伸缩、滚动重启、CronJob 暂停/恢复、删除，以及 Console
  列表/详情、Namespace 作用域选择器与全部变更的 DryRun、确认、幂等和审计闭环（高级 Pod 配置和
  类型化更新表单尚未支持）
- [x] Pod 管理（显式 Cluster/Namespace 定域的类型化 List/Detail/Delete、Agent 最小 RBAC、DryRun、
  UID 前置条件、确认、幂等、审计与 Console 列表/详情/删除闭环；Eviction 仍未支持）
- [x] Pod 日志（专用权限与 Agent 最小 RBAC、有界快照、实时 Follow、UID 防重建校验、取消、限流、超时、
  审计、HTTP/QUIC 流式测试，以及 Console 的容器选择、跟随、下载与取消闭环；浏览器不暴露 HTTP Trailer，
  Console 暂无法区分日志流的终止原因）
- [x] Web Terminal（一次性票据、同源 WebSocket、独立权限、确认、幂等、审计、Pod UID/容器绑定、
  bash 优先并回退 `/bin/sh`、限流/超时/权限重验，QUIC 与 Kubernetes WebSocket-first Pod Exec，以及基于
  xterm.js 的 Console 容器选择、确认、连接/断开与 resize 闭环；会话录制与回放尚未支持）
- [x] YAML 管理（完整 YAML 读取、严格单文档更新、DryRun、UID/resourceVersion 防误改、显式确认、幂等
  和审计，以及节点、命名空间、工作负载和 Pod 详情页的 Console 查看与编辑闭环；编辑器为纯文本，语法高亮
  与结构校验尚未实现）
- [x] Kubernetes Event（独立 `resource-watch.v1`、专用权限与 Agent 最小 RBAC、Namespace/资源过滤、
  初始快照与实时 Follow、SSE 心跳与正文内终止原因、resourceVersion 恢复、取消、限流、超时、权限重验、
  审计和真实 QUIC 测试，以及 Console 的筛选、跟随、按 UID 归并与断流恢复闭环；从具体对象跳转到关联事件
  尚未实现）
- [x] 服务与路由（Service、Ingress 与可选 Gateway API v1 Gateway 的类型化 List/Detail/Create/Update/Delete，
  明确 Cluster/Namespace 作用域、Gateway API 能力探测、DryRun、确认、幂等、并发身份保护、审计和 Agent
  最小 RBAC，以及 Console 的三类列表/详情、类型化创建与编辑表单、删除和 Gateway API 缺失提示；
  HTTPRoute 等 Gateway Route 类型尚未纳入）
- [x] ConfigMap（固定 `core/v1` GVR 的类型化 List/Detail/Create/Update/Delete，列表正文隔离、
  `data`/Base64 `binaryData` 与 1 MiB 校验、immutable 保护、DryRun、确认、幂等、并发身份保护、审计和
  Agent 最小 RBAC，以及 Console 的列表/详情、类型化创建与编辑表单和删除闭环；Secret 专用敏感链路
  尚未实现）
- [x] 存储（PersistentVolume、PersistentVolumeClaim 与 StorageClass 的类型化 List/Detail/Create/Update/Delete，
  集群级与命名空间级作用域隔离，PV CSI/NFS/Local 创建、PVC 只增不减扩容、PV 回收策略与 StorageClass
  扩展开关更新、DryRun、确认、幂等、并发身份保护、审计和 Agent 最小 RBAC，以及 Console 的三类列表/详情、
  按类型创建表单、单字段编辑弹窗和删除闭环）
- [x] 自动伸缩（固定 `autoscaling/v2 HorizontalPodAutoscaler` 的 List/Detail/Create/Update/Delete，
  Deployment/StatefulSet 目标约束，Resource/ContainerResource 指标、ScaleUp/ScaleDown Behavior、DryRun、
  确认、幂等、并发身份保护、审计和 Agent 最小 RBAC，以及 Console 的列表/详情、类型化创建与编辑表单和
  删除闭环；Metrics Server/Adapter 由集群自行安装，VPA 与 KEDA 尚未实现）

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
