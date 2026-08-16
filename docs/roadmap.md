# Roadmap

Roadmap 表示当前规划，不代表发布时间或交付承诺。已勾选条目表示主链路已实现，不代表生产可用、完整兼容或水平
扩展能力；具体行为与限制以功能文档为准。ZKE Server 当前只支持单副本部署。

## Phase 1：平台基础

- [x] Server、Agent、PostgreSQL、版本化迁移和基础数据模型
- [x] 首个管理员引导、本地登录、Session、CSRF、账户与密码生命周期
- [x] 固定权限词表、内置与自定义角色、Global/Tenant/Project RoleBinding 和提权防护
- [x] Tenant、Project、Cluster 生命周期与权限定域审计
- [x] Cluster Enrollment、一次性 Token、安装 Manifest、撤销和重新接入
- [x] Agent identity Secret、QUIC/mTLS、心跳、重连和客户端证书续期
- [x] Managed Agent PKI、Cluster 状态 SSE、OpenAPI 3.1 与管理 Console

## Phase 2：容器服务

- [x] Server–Agent 业务 Stream、能力协商、并发、超时、取消和有界正文
- [x] 集群概览、Node、Namespace、Pod 与五类工作负载管理
- [x] 工作负载高级调度、修订历史、回滚、伸缩、滚动重启和 CronJob 暂停/恢复
- [x] Pod 日志、Web Terminal、输出录制与回放、Pod Access、Kubernetes Event 和资源用量
- [x] Service、Ingress、Gateway 与 Gateway API HTTP/GRPC/TLS/TCP/UDP Route 管理
- [x] ConfigMap、Secret、PV、PVC、StorageClass、HPA、VPA 与 KEDA 管理
- [x] ResourceQuota、LimitRange、NetworkPolicy、PodDisruptionBudget 与 PriorityClass 管理
- [x] ServiceAccount、Role、ClusterRole、RoleBinding 与 ClusterRoleBinding 管理
- [x] Discovery、CRD 资源浏览、通用主资源 CRUD 和 YAML 编辑
- [x] 多文档 YAML 清单 DryRun、逐文档授权、应用与删除
- [x] 资源诊断、关联对象与 Event 证据导航
- [x] 按当前用户权限投影 Kubernetes RBAC 的独立终端 App

## Phase 3：可观测性

数据通路与安全边界见 [Phase 3 可观测性架构设计](architecture/observability-phase-3.md)。指标、日志与告警
按切片推进，后一项依赖前一项就绪。已勾选的两项分别对着真实的 VictoriaMetrics 和真实集群验证过，但完整链路
（集群内 vmagent → Agent → Server → 存储 → Console）尚未在同一次运行中端到端跑通。

- [x] 指标端到端最小链路：采集清单生成、Agent 摄取端点、Metrics Ingest Stream、Server 摄取网关与作用域改写、VictoriaMetrics 写入
- [x] 多集群指标查询：固定查询目录、权限过滤、集群与节点用量视图、Console 自建图表
- [ ] 指标深化：节点、Namespace 与工作负载维度，数据空洞与限流状态呈现
- [ ] 集群标签体系
- [ ] VictoriaLogs 集成与多集群日志
- [ ] 告警中心

可视化由 Console 自建，不集成 Grafana，也不提供通用仪表盘编辑器。

## Phase 4：AI 运维与排障助手（Copilot）

- [ ] Copilot 交互模型、上下文范围与权限边界设计
- [ ] 按 Tenant、Project、Cluster、Namespace 和资源对象安全收集诊断上下文
- [ ] 关联资源状态、Kubernetes Event、日志和指标辅助定位故障
- [ ] 提供带依据、影响范围和风险说明的分析结论与处理建议
- [ ] Copilot 发起的敏感操作沿用权限检查、用户确认和审计机制
- [ ] 建立诊断效果评估、用户反馈与持续改进机制
