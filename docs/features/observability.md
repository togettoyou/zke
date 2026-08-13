# 可观测性平台（规划）

可观测性平台是规划中的多集群应用，用户进入时无需先选择集群。初步计划集成 VictoriaMetrics、VictoriaLogs 和 Grafana，具体技术方案仍需后续设计与验证。

规划能力包括：

- 多集群指标与集群健康状态；
- 节点、工作负载和 Pod 指标；
- 日志查询与 Kubernetes Event；
- 告警规则、告警记录和仪表盘；
- 多集群资源对比；
- 按集群、Namespace 和工作负载筛选；
- 数据源管理。

所有指标、日志和事件必须至少携带以下不可变作用域标识：

- `cluster_id`
- `tenant_id`
- `project_id`

`cluster_name` 可作为展示属性，但不能替代 `cluster_id` 作为数据身份。可观测性平台默认提供用户权限范围内的
全局视图，同时允许按集群和 Namespace 缩小范围。
