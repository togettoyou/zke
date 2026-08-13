# Docker 与 Helm 部署

ZKE 提供 Docker Compose 和 Helm 两种部署入口。Docker Compose 使用独立的 Server 与 PostgreSQL 容器，适合需要
长期运行和独立维护数据库的环境；Helm 用于 Kubernetes。ZKE 当前仍处于开发预览阶段，投入关键环境前应自行完成
安全、备份、容量和升级验证。

## 配置边界

容器内置 `/etc/zke/zke-server.yaml`。该文件只负责 Server 启动前必须知道的引导配置：

- PostgreSQL 连接与迁移超时；
- HTTP、Pod Access 与 Agent QUIC Listener 的监听地址；
- Agent 安装的平台默认注册 URL 与 QUIC 地址；
- Managed PKI 的持久目录、证书有效期与续期窗口；
- 认证、请求超时、并发和资源上限。

以下运行配置保存在 PostgreSQL，只能由全局 `admin` 在 Console 的“设置 → 平台配置”修改：

- Agent 接入端点预设：注册 URL、QUIC 地址、可选注册 HTTPS CA；
- Agent 镜像、Namespace 与 Image Pull Policy；
- Cluster Terminal 镜像。

平台默认端点由 `agent_install.public_http_url` 与 `agent_install.public_quic_address` 配套提供；两项均为空时使用
“本机回环预览”。Server 每次启动都按当前有效配置重新同步：从预设切换到自定义地址时创建部署端点，从自定义地址
切回“本机回环预览”或“Docker Desktop / OrbStack”时删除已被替代的部署端点。Console 只能识别当前平台默认端点，
不能修改、删除或另设默认值。环境变量优先于 YAML。

Agent Namespace 参与敏感 Secret 与受保护命名空间边界，只能在签发首个 Enrollment 前修改。Cluster Terminal 镜像修改后
立即用于新会话；Agent 镜像、拉取策略和凭据选中的端点会在新 Enrollment 签发时立即进入其不可变快照。

Server 从持久目录维护 Agent Client CA、Listener CA 和 Listener 身份。启用端点的 QUIC Host 会进入 Listener 证书
SAN。保存包含新 Host 的端点时，Server 会复用 Listener 私钥在线重签叶子证书，并原子切换给新的 QUIC 握手；既有
Agent 连接不受影响，也不会轮换 CA 或要求重启 Server。编辑、停用或删除端点时会同步移除不再需要的 SAN；如果后续
数据库写入失败，Server 会恢复原证书 SAN。启动时也会再次与当前启用端点精确对齐。

部署环境变量只覆盖部署引导值：

| 环境变量 | 对应配置 |
| --- | --- |
| `ZKE_DATABASE_URL` | `database.url` |
| `ZKE_CONSOLE_DIRECTORY` | `http.console_directory` |
| `ZKE_POD_ACCESS_EXTERNAL_URL` | `pod_access.external_url` |
| `ZKE_AGENT_INSTALL_PUBLIC_HTTP_URL` | `agent_install.public_http_url` |
| `ZKE_AGENT_INSTALL_PUBLIC_QUIC_ADDRESS` | `agent_install.public_quic_address` |

## Docker 一键启动

一体镜像包含 ZKE Server 与 PostgreSQL，适合用最少步骤完成首次启动：

```bash
docker run -d --name zke \
  -p 8080:8080 \
  -p 8081:8081 \
  -p 8443:8443/udp \
  -v zke-data:/data \
  -v zke-postgresql-data:/var/lib/postgresql/data \
  ghcr.io/togettoyou/zke-server-pg:latest
```

打开 <http://127.0.0.1:8080> 并完成首个全局管理员初始化。升级或重建容器时应保留 `zke-data` 与
`zke-postgresql-data` 两个命名卷。

## Docker Compose 分离部署

需要分别升级、备份和运维 Server 与 PostgreSQL 时，使用仓库提供的 Compose 文件：

```bash
cd deploy/docker
cp .env.example .env
# 修改 .env 中的 ZKE_POSTGRES_PASSWORD
docker compose up -d
```

打开 <http://127.0.0.1:8080> 并完成首个全局管理员初始化。Compose 默认发布以下端口：

- TCP `8080`：Console 与 API；
- TCP `8081`：Pod Access；
- UDP `8443`：Agent QUIC/mTLS。

`zke-data` 保存 Managed PKI，`zke-postgresql-data` 保存数据库。升级容器前应备份数据并保留这两个命名卷。PostgreSQL
只将端口绑定到宿主机回环地址，供本机维护和开发使用；Compose 网络内的 Server 使用服务名连接数据库。可在 `.env` 中
覆盖镜像、宿主机端口和 Pod Access 外部地址。

数据库密码会进入 PostgreSQL URL，因此应使用足够长的 URL-safe 随机字符串。HTTP 注册接口与 Pod Access 均支持 HTTP
或 HTTPS；Agent QUIC 长连接固定使用 TLS 1.3 + mTLS。

查看状态和日志：

```bash
docker compose ps
docker compose logs -f server
```

停止服务不会删除数据卷：

```bash
docker compose down
```

## Helm

```bash
helm upgrade --install zke oci://ghcr.io/togettoyou/charts/zke \
  --version 0.0.0-latest \
  --namespace zke-system \
  --create-namespace
```

Chart 负责 Server、PostgreSQL、监听端口与持久化。两项 Agent 地址均留空时，Chart 使用当前 release 的 Server Service
FQDN 作为平台默认端点，集群域名由 `clusterDomain` 控制，默认是 `cluster.local`。跨集群接入时，应通过
`server.agentInstall.publicHTTPURL` 与 `server.agentInstall.publicQUICAddress` 配套提供目标集群可达的入口；其他端点和
安装默认值在 Console 管理。Pod Access 的浏览器外部地址仍属于部署引导配置，例如：

```bash
helm upgrade --install zke oci://ghcr.io/togettoyou/charts/zke \
  --version 0.0.0-latest \
  --namespace zke-system \
  --create-namespace \
  --set server.podAccessExternalURL=https://pod-access.example.com
```

## 数据与安全

- ZKE Server 当前按单实例运行；Agent 在线连接和部分限流状态在进程内存中。
- Managed PKI 私钥只能保存在受保护的持久卷；数据库有 PKI 状态而文件丢失时 Server 会失败关闭。
- Agent Enrollment Token 只返回一次；安装 Manifest 通过 Authorization Header 获取，不能把 Token 放入 URL 或日志。
- Console/API 的 HTTPS 与 Agent QUIC/mTLS 是两套独立身份。共享环境应在网关终止 Console/API HTTPS，并正确暴露 UDP
  `8443`。
