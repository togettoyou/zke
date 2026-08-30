# Roadmap

Roadmap 表示当前规划，不代表发布时间或交付承诺。已勾选条目表示主链路已实现；具体行为与限制以功能文档为准。
ZKE Server 当前只支持单副本部署。

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
- [x] 节点污点管理、Pod 驱逐（PDB 感知）与 CronJob 立即运行
- [x] 全集群事件中心，与 Namespace 级事件共用 `cluster.event.read`
- [x] 容器服务内只读的 Helm Release 清单、修订历史与 values，使用 `cluster.secret.read`
- [x] 按当前用户权限投影 Kubernetes RBAC 的独立终端 App
- [x] 独立的「Helm 应用」App：Chart 仓库、安装与升级、渲染差异、回滚与卸载

## Phase 3：可观测性

数据通路与安全边界见 [Phase 3 可观测性架构设计](architecture/observability-phase-3.md)。指标、日志与告警
按切片推进，后一项依赖前一项就绪。全部 119 个查询对着真实的 VictoriaMetrics 验证过，并且要求每一条都
真的选到数据——指标族齐备的种子加上「每个目录条目至少返回一条序列」的断言，堵住了「模板能编译但选不到
任何东西」这一类静默空图。查询读取的 kubelet resource、cAdvisor 与**全部** node-exporter 指标名，都在真实
kubelet（v1.31.1）与真实 node-exporter（v1.12.1，使用 ZKE 下发的同一组 collector 参数）上核对过，采集质量
视图读的抓取元信息也在真实 vmagent（v1.149.0）上核对过；kube-state-metrics 新增的封锁与就绪与 Job 与
PVC/PV 状态、kubelet 自身健康、cAdvisor 的容器磁盘与 OOM **还没有做这一步核对**，它们需要真实的
API Server 或 kubelet。三个采集
组件在真实集群上一起装过，完整链路（集群内 vmagent → Agent → Server → 存储 → Console）也跑通到图表；
但每集群摄取预算只在单元测试中验证过，没有在真实集群的 vmagent 上观察过退避行为，
`kubelet_volume_stats_*` 也还没有在带 CSI 卷的真实集群上抓到过。

- [x] 指标端到端最小链路：采集清单生成、Agent 摄取端点、Metrics Ingest Stream、Server 摄取网关与作用域改写、VictoriaMetrics 写入
- [x] 多集群指标查询：固定查询目录、权限过滤、集群与节点用量视图、Console 自建图表
- [x] 指标深化：Namespace 与 Pod 维度、Top N 与 Namespace 过滤、每集群摄取预算与限流状态呈现、查询响应的 `partial` 与 `issues`、容量与保留期运维文档
- [x] 抓取目标扩展：kube-state-metrics 与 node-exporter 随采集组件一并安装/卸载，三者镜像与资源预算进入平台配置
- [x] 深度指标：集群与节点利用率、Namespace 申请量与限制量、工作负载维度（Deployment 两级归属）、Pod 重启、节点文件系统/网络/磁盘 IO
- [x] 完整可观测性视图：容量与申请占比、节点饱和度与 Pod 密度、磁盘 IOPS 与繁忙度、inode、网络错误丢包、Pod 与节点状态、未就绪副本；Console 拆为集群总览 / 计算资源 / 存储与网络 / Kubernetes 资源 / 采集质量五个分区，共享时间范围选择、图上拖拽选取区间与光标读数
- [x] 数据探索：Console 中自己书写 MetricsQL 表达式（多条同时执行、可隐藏、图表 / 表格 / JSON 三种读法），排在「采集接入」之后、四个仪表分区之前，Server 用 VictoriaMetrics 自己的解析器把目标集群强制改写进每一个序列选择器，并按项目保存可共享的具名表达式
- [ ] VictoriaLogs 集成与多集群日志：计划在 Console 中作为独立的「日志」应用，不并入「监控」
- [ ] 告警中心

可视化由 Console 自建，不集成 Grafana。自定义的是查询表达式，不是仪表盘布局：「数据探索」开放表达式并强制
改写作用域，但不提供通用仪表盘编辑器。

## Phase 4：AIOps

产品形态与安全边界见 [Phase 4 AIOps：架构设计](architecture/ai-phase-4.md)，能力范围见
[AIOps](features/ai-assistant.md)。

AIOps 是运行在 ZKE 云端的 Codex 式运维 App：它跟随 Console 当前 Tenant/Project，每个会话固定一个 Cluster，并提供
对话、后台任务与轨迹。它计划在用户授权范围内读写该集群资源、查询指标与日志、执行受控 Cluster Terminal 命令、
部署和分析应用；每次操作仍由会话 Cluster 的 Agent 定域执行，不考虑跨 Cluster 会话。

已落地模型端点配置、跟随桌面 Tenant/Project 并按 Cluster 工作区隔离的会话与轨迹存储、后台运行时、模型自主
工具循环与读取工具目录、敏感工具审批等待、流式输出，以及使用对话/轨迹 Tab 的 AIOps App。受控资源写入已支持
Deployment/StatefulSet 副本数伸缩、工作负载回滚，以及多文档 Manifest 的 DryRun、差异、Apply 与 Delete。
Helm 也已接入：Chart 仓库目录与 Chart 内容的读取，Release 的清单、历史与详情，以及安装、升级、回滚、卸载的预检与受控提交。
随 Server 发布的排查技能（Playbook）与只读并行子任务也已落地：技能只规定用哪些既有工具、按什么顺序取证，
子任务只有只读工具、有独立预算、写回同一条轨迹。

- [x] 运行时底座：`ai.run`、后台任务、SSE/重连、权限重验、按比例触发的检查点压缩、模型失败分类与退避重试、证据引用
- [x] AIOps App：随当前 Tenant/Project 和 Cluster 工作区切换的会话、对话/轨迹 Tab、轨迹时间线与详情、附件、搜索、归档、删除、导出和证据深链
- [x] Agent 循环：多 Step 工具调用、同一 Step 内有界并发读取、读取工具目录（概览、资源、诊断、Event、Pod 日志、指标）、逐次权限重验、敏感工具审批、收敛与预算保护、流式增量与运行统计
- [x] 首个受控写操作：Deployment/StatefulSet 副本数 DryRun 预检与实际伸缩、稳定幂等键、写入批次顺序执行和三档审批
- [x] 资源写操作：YAML/DryRun/有界差异、Manifest 部署与删除、工作负载历史读取与回滚、预检快照、逐目标权限重验
- [x] Cluster Terminal 的 AIOps 受控命令执行：Turn 级终端复用、冻结权限快照、敏感审批、有界输出与自动清理
- [x] 技能与并行子任务：随 Server 发布、按需加载的排查 Playbook；只读、有界、可回收并写回同一轨迹的并行取证分支
- [x] Helm 接入：Chart 仓库目录、Chart 与版本、Chart 自带 values.yaml 的读取（全局 `helm.repository.read`，按全局作用域判定）；Release 清单、修订历史与 revision 详情的读取；安装 / 升级 / 回滚 / 卸载的预检与受控提交，权限与 Console 的 Release 路由一致并按动作解析，提交始终为敏感操作；Release 侧不返回 values 取值、NOTES.txt 与 Manifest 正文
- [ ] 扩展能力：图表/资源就地唤起、定时巡检和事件触发自动化
- [ ] 配额、诊断效果评估与用户反馈
