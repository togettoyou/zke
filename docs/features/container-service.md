# 容器服务

容器服务是单集群应用。用户进入应用时需要先选择一个 Kubernetes 集群，进入后所有页面和操作均作用于当前集群。

当前已完成 Node 类型化接口和通用只读资源底座：

- `GET /api/v1/clusters/{cluster_id}/nodes`：支持 `limit`、Kubernetes continuation token、Label Selector 和
  Field Selector；
- `GET /api/v1/clusters/{cluster_id}/nodes/{node_name}`：返回 Node 状态、容量、地址、标签、污点、条件和
  Node System Info；
- `GET /api/v1/clusters/{cluster_id}/kubernetes/resource-types`：返回目标 Cluster 当前 Discovery 可见的
  内置资源和 CRD 资源目录；
- `GET /api/v1/clusters/{cluster_id}/kubernetes/resources`：按 GVR、Namespace、Selector 和 Kubernetes
  continuation token 查询任意已授权主资源；
- `GET /api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}`：按 GVR、Namespace 和名称读取
  完整对象；
- 所有接口都要求 Session 和目标 Cluster 的 `cluster.read` 权限，每个请求通过独立 QUIC Resource Stream
  交给该 Cluster 的 Agent；
- Agent 使用 Kubernetes dynamic client，并只接受主资源的 List/Get；Secret 和 Subresource 明确拒绝；
- 安装 Manifest 只为 Agent ServiceAccount 增加 Node 的 `get`、`list` 权限。

Node 列表当前通过 Resource Stream 传输完整 Kubernetes 对象，再由 Server 转换成稳定的精简响应；Table
表示和 Console 页面尚未实现。通用接口返回 Unstructured JSON，并移除 `metadata.managedFields`。Discovery
目录表示 API Server 暴露的资源，不代表 Agent ServiceAccount 已获授权；读取更多内置资源或任意 CR 时，安装方
需要显式扩展该 ServiceAccount 的最小 RBAC，ZKE 无需增加新的资源协议或 HTTP Handler。

规划能力包括：

- 集群概览、节点与 Namespace 管理；
- Deployment、StatefulSet、DaemonSet、Job 和 CronJob；
- Pod 管理、Pod 日志与 Web Terminal；
- Service 与 Ingress；
- ConfigMap 与 Secret；
- PersistentVolume、PersistentVolumeClaim 与 StorageClass；
- Kubernetes Event；
- YAML 查看与编辑；
- 资源创建、更新和删除；
- RBAC 权限控制。

产品体验将参考成熟 Kubernetes 管理平台的通用实践，但不会以与任何现有平台完全相同为目标。
