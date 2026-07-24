# Server + Agent 架构

## ZKE Server

ZKE Server 是平台统一控制端，规划负责：

- Web Desktop；
- 用户认证；
- RBAC 权限控制；
- 多租户与项目管理；
- Agent 连接管理；
- 集群元数据管理；
- 操作任务下发；
- 数据存储；
- 审计日志；
- AI 分析与任务编排；
- 模型 API Gateway；
- 多集群资源汇总。

## ZKE Agent

每个接入的 Kubernetes 集群部署一个 ZKE Agent。Agent 规划负责：

- 主动连接 ZKE Server；
- 上报 Agent 版本、健康状态和集群状态；
- 查询 Kubernetes 资源；
- 执行经过授权的集群操作；
- 获取 Pod 日志；
- 建立 Web Terminal 会话；
- 执行作业和模型服务相关操作；
- 收集或转发指标、日志和事件；
- 返回操作结果。

## 当前实现边界

当前已完成 Server 注册凭证创建、Agent 注册 HTTP API，以及 Agent 首次注册与身份 Secret 持久化。Agent 在集群
内生成私钥，Server 只接收 CSR；注册结果中的证书身份显式绑定 Tenant、Project、Cluster 和 Agent。Agent 先
持久化私钥、CSR 和幂等键再发出网络请求，因此请求结果不确定或 Pod 重启时仍能使用同一注册尝试恢复结果。

身份存储使用预创建的固定名称 Kubernetes Secret。Agent 只需要该 Secret 的 `get`、`update` 权限，不需要创建
或列举 Secret；一次性注册 Token 通过另一个临时 Secret 只读挂载。完整身份存在后，Agent 不再读取注册 Token。
QUIC/mTLS 长连接、心跳、任务路由、证书续期和撤销后的连接处理尚未实现。

Agent 默认优先使用 Pod 内的 InCluster Kubernetes 配置；本地开发可以显式设置 `kubeconfig_file`，未设置时
回退到 `KUBECONFIG` 或 `~/.kube/config`。显式文件始终优先于环境自动识别。

## 连接模型

Agent 必须主动连接 Server，不要求 Server 直接访问 Kubernetes API Server。这一连接模型计划适用于：

- 私有网络；
- 混合云；
- 多云；
- 边缘集群；
- 无法由 Server 主动访问的 Kubernetes 集群。

集群操作由目标集群中的 Agent 执行。Server 负责认证、授权、目标确认、任务下发与审计，不应绕过 Agent 直接执行未受控操作。
