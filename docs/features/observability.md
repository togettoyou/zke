# 可观测性平台

可观测性平台是多集群应用，用户进入时无需先选择集群。

当前状态：指标链路的第一个切片已实现——集群内采集、经 Agent 回传、Server 摄取与作用域改写、集中存储，
以及 Console 中的集群与节点用量视图和采集接入。日志、告警、事件与集群标签体系仍是规划。

一体镜像、Docker Compose 与 Helm 都自带 VictoriaMetrics 并默认启用指标。未配置指标存储时 Server 不向
Agent 提供摄取能力，集群侧也不部署采集组件，Console 会直接说明本部署未启用指标存储。

数据通路、协议、限额与安全边界见 [Phase 3 可观测性架构设计](../architecture/observability-phase-3.md)。
本文只描述产品侧的能力范围与作用域规则。

## 技术选型

指标与日志由 ZKE 自建链路采集与存储，不依赖 Server 直连集群网络：

- 集群内部署 vmagent 负责抓取。采集组件由该集群的 Agent 安装与卸载，Console 中一次操作即可，不需要使用者
  自己执行 `kubectl apply`；未安装采集的集群行为不变；
- 采集数据经该集群 ZKE Agent 已有的 QUIC/mTLS 出向连接回传，不新增网络开通要求；
- Server 侧集中存储在 VictoriaMetrics（指标）与后续的 VictoriaLogs（日志）；采集组件的镜像、拉取策略与
  资源请求/限制在「平台配置 → 指标采集」中管理，默认 `victoriametrics/vmagent:v1.149.0`；
- 可视化由 ZKE Console 自建，不集成、不嵌入 Grafana，也不提供 Grafana 数据源。

## 已实现

- 跨集群与单集群的 CPU、内存用量曲线，节点用量 Top N；
- 采集健康度，同时用作当前有数据的集群清单；
- 「采集接入」按集群列出采集状态、组件镜像与摄取凭证，支持一键安装与卸载；状态来自各集群本身，因此手工
  删除也能被发现；
- 摄取凭证由 Agent 在集群内生成，从不经过 Server，也不会出现在浏览器里；
- 权限按集群定域且读写分离：`cluster.metrics.read` 决定能查哪些集群的指标，`cluster.metrics.manage`
  决定能否安装或卸载采集组件——只能看图表的人无法改变集群里运行的东西。

## 规划能力

- 节点、工作负载和 Pod 的更多维度与命名空间视图；
- 日志查询与 Kubernetes Event；
- 告警规则与告警记录；
- 多集群资源对比；
- 集群标签体系。

上述能力按切片推进，顺序与范围见 [Roadmap](../roadmap.md)。

界面提供的是围绕上述场景设计的固定视图，不是通用仪表盘编辑器：自定义面板需要自由查询表达式，与"只开放
具名查询"的安全边界冲突。

## 作用域与数据身份

`cluster_id` 是指标与日志唯一不可变的数据身份，由 Server 在摄取时按 Agent 的 mTLS 连接身份强制写入：
摄取网关先删除批次中所有 `zke_` 前缀的标签，再写入自己的 `zke_cluster_id`，因此集群侧无论上报什么都
无法冒充另一个集群。

Tenant 与 Project 不写入存储：Cluster 的归属是 Server 数据库中的可变关系，写进时间序列标签会让同一集群的
历史数据按旧归属分裂。查询时由 Server 把调用者的可见范围展开成一组 `cluster_id`，再作为强制过滤注入查询。
`cluster_name` 只能作为展示属性，不能替代 `cluster_id` 作为数据身份。

可观测性平台默认提供用户权限范围内的全局视图，同时允许按集群和 Namespace 缩小范围。全局视图不代表全局
操作权限：查询结果始终受 Tenant、Project 与 RBAC 边界约束，规划中的读取权限词为 `cluster.metrics.read`，
采集启停为 `cluster.metrics.manage`。

## 不做的事

- 不向浏览器开放自由 PromQL 或存储后端的原始查询接口，Console 只能调用 Server 定义的具名查询；
- 不集成 Grafana：它的权限体系与 ZKE 的 Tenant/Project/RBAC 无法对齐，嵌入还会带来第二套导航、主题与
  登录；需要 Grafana 的使用者可以自行连接自己的 VictoriaMetrics，ZKE 不为此提供数据源或代理；
- 不做通用仪表盘编辑器；
- 不托管或迁移用户已有的 Prometheus 生态；
- 可观测性是可降级能力，存储不可用时不影响集群接入、容器服务与终端。
