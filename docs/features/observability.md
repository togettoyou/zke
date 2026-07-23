# 可观测性平台

可观测性平台是多集群应用，用户进入时无需先选择集群。初步计划集成 VictoriaMetrics、VictoriaLogs 和 Grafana。

规划能力包括：

- 多集群指标与集群健康状态；
- 节点、工作负载、Pod、GPU 和模型服务指标；
- 日志查询与 Kubernetes Event；
- 告警规则、告警记录和仪表盘；
- 多集群资源对比；
- 按集群、Namespace 和工作负载筛选；
- 数据源管理。

所有指标、日志和事件必须携带明确的集群标识，例如：

- `cluster_id`
- `cluster_name`
- `tenant_id`

可观测性平台默认提供用户权限范围内的全局视图，同时允许按集群和 Namespace 缩小范围。

