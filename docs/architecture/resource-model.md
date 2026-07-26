# 应用作用域与资源模型

## 应用作用域

| 应用 | 入口范围 | 查看范围 | 创建或执行范围 |
| --- | --- | --- | --- |
| 集群接入管理 | 多集群 | 全部已接入集群 | 指定 Cluster |
| 容器服务 | 单集群 | 当前选择的集群 | 当前选择的集群 |
| 作业平台 | 单集群为主 | 当前选择的集群 | 当前选择的集群 |
| 算力平台 | 多集群 | 全局算力与模型服务 | 指定集群、算力池或调度策略 |
| 可观测性平台 | 多集群 | 全局，可按集群筛选 | 以查询、分析和告警为主 |
| ZKE Copilot | 多集群 | 全局或用户限定范围 | 执行操作时必须明确目标集群 |

跨集群查询必须遵守租户、项目和 RBAC 权限边界。全局视图不代表全局操作权限。

## 资源层次

ZKE 使用以下资源层次组织权限与作用域；当前已实现 Global、Tenant、Project 和 Cluster，Cluster Group
与 Namespace 业务管理仍在规划中：

```text
Global
└── Tenant
    └── Project
        └── Cluster Group
            └── Cluster
                └── Namespace
                    └── Workload or Service
```

不同用户只能查看和操作其权限范围内的资源。所有跨集群查询均需遵守租户、项目和 RBAC 权限边界。

## Cluster 与 Agent

外部资源模型中，Cluster 与部署在其中的 ZKE Agent 是同一个管理共同体。管理 API、RBAC 和审计均以稳定的
`cluster_id` 为目标，不向用户暴露独立 Agent 资源或 Agent ID。

内部数据模型仍保存 Agent 连接身份，以支持证书轮换、撤销和重新接入历史。重新接入复用原 `cluster_id`，创建新
的内部 Agent 身份；同一 Cluster 同一时刻最多只有一个未撤销身份。实际 Kubernetes 查询和操作仍由该 Cluster
当前有效的 Agent 定域执行。
