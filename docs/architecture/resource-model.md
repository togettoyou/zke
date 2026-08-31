# 应用作用域与资源模型

## 应用作用域

| 应用 | 入口范围 | 查看范围 | 创建或执行范围 |
| --- | --- | --- | --- |
| 集群接入管理 | 多集群 | 全部已接入集群 | 指定 Cluster |
| 容器服务 | 单集群 | 当前选择的集群 | 当前选择的集群 |
| 监控 | 单集群 | 项目内选定一个目标 Cluster | 查询与采集组件的安装、卸载都定域到该 Cluster |
| 自定义应用 | 单项目 | 当前 Project 配置的应用入口 | `application.manage` 在当前 Project 创建、修改和删除入口 |

跨集群查询必须遵守租户、项目和 RBAC 权限边界。全局视图不代表全局操作权限。

## 资源层次

ZKE 使用以下层次组织当前资源归属：

```text
Global
└── Tenant
    └── Project
        └── Cluster
            └── Namespace
                └── Workload or Service
```

不同用户只能查看和操作其权限范围内的资源。所有跨集群查询均需遵守租户、项目和 RBAC 权限边界。

**授权作用域止于 Cluster：Namespace 不是授权层级。** 上面的层次同时表达资源归属和授权作用域，但两者
的边界不同。RoleBinding 只能绑定 Global、Tenant 和 Project 三种作用域，Cluster 通过所属 Project 继承
授权；Namespace 及其以下的 Workload 只是 Cluster 内部的资源维度，不能承载 RoleBinding，也不会出现在
可见范围计算中。

这意味着对某个 Cluster 具有某项权限的用户，在该 Cluster 的所有 Namespace 上都具有该权限。需要按
Namespace 区分权限时，当前的做法是把工作负载放到不同 Cluster，或者由 Project 边界隔离，而不是细分
授权层级。该决策影响 `scope` 结构、RoleBinding 校验、可见范围解析和全部按作用域过滤的查询，改变它需要
成套修改上述位置，因此不应在单个功能中就地放宽。

## Cluster 与 Agent

外部资源模型中，Cluster 与部署在其中的 ZKE Agent 是同一个管理共同体。管理 API、RBAC 和审计均以稳定的
`cluster_id` 为目标，不向用户暴露独立 Agent 资源或 Agent ID。

内部数据模型仍保存 Agent 连接身份，以支持证书轮换、撤销和重新接入历史。重新接入复用原 `cluster_id`，创建新
的内部 Agent 身份；同一 Cluster 同一时刻最多只有一个未撤销身份。实际 Kubernetes 查询和操作仍由该 Cluster
当前有效的 Agent 定域执行。
