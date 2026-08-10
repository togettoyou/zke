# Docker、Kubernetes 与 Helm 部署

ZKE 当前处于开发预览阶段，不适用于生产环境。本页提供可验证的公开预览部署入口；将服务暴露到共享网络前，必须替换默认凭据、配置持久化，并在外部网关终止 HTTP TLS。

## 镜像与发布规则

| 产物 | 地址 | 用途 |
| --- | --- | --- |
| ZKE Server | `ghcr.io/togettoyou/zke-server` | Server 与 Console 静态资源 |
| ZKE Server + PostgreSQL | `ghcr.io/togettoyou/zke-server-pg` | 单容器快速预览 |
| ZKE Agent | `ghcr.io/togettoyou/zke-agent` | Agent 与 Cluster Terminal 工具环境 |
| Helm Chart | `oci://ghcr.io/togettoyou/charts/zke` | Server 与 PostgreSQL Kubernetes 部署 |

`main` 分支只发布三个镜像的 `latest` 标签。Git Tag 发布同名镜像标签；Helm OCI Chart 要求标签必须是语义化版本，因此 `main` 发布为 `0.0.0-latest`，Git Tag 发布为去掉可选前缀 `v` 后的语义化版本，Chart 的 `appVersion` 保留对应镜像标签。

## 默认配置与覆盖

Server 和 Agent 镜像分别内置仓库中的：

- `/etc/zke/zke-server.yaml`
- `/etc/zke/zke-agent.yaml`

可以把完整配置文件挂载到相同路径进行替换。Server 还支持以下部署时环境变量，显式设置的环境变量优先于 YAML：

| 环境变量 | 对应配置 |
| --- | --- |
| `ZKE_DATABASE_URL` | `database.url` |
| `ZKE_POD_ACCESS_EXTERNAL_URL` | `pod_access.external_url` |
| `ZKE_AGENT_INSTALL_PUBLIC_HTTP_URL` | `agent_install.public_http_url` |
| `ZKE_AGENT_INSTALL_PUBLIC_QUIC_ADDRESS` | `agent_install.public_quic_address` |
| `ZKE_AGENT_IMAGE` | `agent_install.image` |
| `ZKE_CLUSTER_TERMINAL_IMAGE` | `cluster_terminal.image` |

默认 HTTP/API 与 Pod Access Listener 分别使用 TCP `8080` 和 `8081`，不在进程内启用 HTTP TLS；Agent Listener 使用 UDP `8443` 和独立的 QUIC/mTLS 身份。共享环境应由网关为 `8080`、`8081` 提供 HTTPS，并为 `8443/UDP` 提供可达入口。

Agent 安装功能默认开启，但 `agent_install.public_http_url`、`agent_install.public_quic_address` 和 Managed PKI Listener SAN 必须与目标集群实际可达的入口一致。默认回环地址只适用于本机预览，不能直接用于远程集群接入。

## Docker 快速预览

一体镜像在同一个容器中启动 ZKE Server 和 PostgreSQL：

```bash
docker run -d --name zke \
  -p 8080:8080 \
  -p 8081:8081 \
  -p 8443:8443/udp \
  -v zke-data:/var/lib/zke \
  -v zke-postgresql-data:/var/lib/postgresql/data \
  ghcr.io/togettoyou/zke-server-pg:latest
```

Console 地址为 <http://127.0.0.1:8080>。首次空库启动会生成管理员密码：

```bash
docker exec zke cat /var/lib/zke/admin-password
```

替换完整 Server 配置：

```bash
docker run -d --name zke \
  -p 8080:8080 -p 8081:8081 -p 8443:8443/udp \
  -v zke-data:/var/lib/zke \
  -v zke-postgresql-data:/var/lib/postgresql/data \
  -v "$(pwd)/configs/zke-server.yaml:/etc/zke/zke-server.yaml:ro" \
  ghcr.io/togettoyou/zke-server-pg:latest
```

`zke-server-pg` 使用固定的容器内数据库初始凭据，只用于单机预览；不得把 PostgreSQL 端口暴露到容器外部。删除容器前应确认两个命名卷仍然保留。

## Kubernetes 清单

静态清单位于 `deploy/kubernetes/zke.yaml`，包含：

- PostgreSQL Secret、Headless Service 与单副本 StatefulSet；
- ZKE Server PVC、单副本 Deployment 与 Service；
- TCP `8080`、TCP `8081` 和 UDP `8443` Service 端口。

清单为了能够直接应用，包含 `zke_change_me` 初始数据库密码。必须在第一次创建 StatefulSet 前替换；数据库初始化后只修改 Secret 不会自动修改 PostgreSQL 内部密码。

```bash
kubectl apply -f deploy/kubernetes/zke.yaml
kubectl -n zke-system rollout status statefulset/zke-postgresql
kubectl -n zke-system rollout status deployment/zke-server
kubectl -n zke-system port-forward service/zke-server 8080:8080 8081:8081
```

读取管理员密码：

```bash
kubectl -n zke-system exec deployment/zke-server -- cat /var/lib/zke/admin-password
```

Service 默认为 `ClusterIP`。外部部署需要根据环境选择 LoadBalancer、Ingress 或 Gateway；其中 `8443` 是 UDP，不能按普通 HTTP Ingress 转发。

## Helm

安装 `main` 分支对应 Chart：

```bash
helm upgrade --install zke oci://ghcr.io/togettoyou/charts/zke \
  --version 0.0.0-latest \
  --namespace zke-system \
  --create-namespace
```

安装 Git Tag 对应 Chart 时，把 `--version` 改为该 Tag 的语义化版本；例如镜像标签为 `vX.Y.Z` 时，Chart 版本为 `X.Y.Z`，Chart 会自动引用 `vX.Y.Z` 镜像。

Helm 默认生成随机 PostgreSQL 密码，并在升级时读取现有 Secret 继续使用。远程 Agent 接入至少需要提供实际入口：

```bash
helm upgrade --install zke oci://ghcr.io/togettoyou/charts/zke \
  --version 0.0.0-latest \
  --namespace zke-system \
  --create-namespace \
  --set server.agentInstall.publicHTTPURL=https://zke.example.com \
  --set server.agentInstall.publicQUICAddress=zke.example.com:8443 \
  --set server.podAccessExternalURL=https://pod-access.example.com
```

上述域名仅为占位值。HTTP TLS 可以在网关终止，但 Agent QUIC Listener 仍使用自身的 mTLS；公开地址还必须出现在 `agent_pki.managed.listener_sans` 中，因此正式接入远程集群前应挂载经过审核的完整 Server 配置。
