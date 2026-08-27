# 部署指南

ZKE Server 是单个 Go 二进制，内置 Console 静态资源，依赖 PostgreSQL（仓库的镜像、Compose 与 Chart 都使用
17）、一个持久目录，以及启用多集群指标时的 VictoriaMetrics。

## 选择部署方式

| 方式 | 适用场景 | 数据库 | 指标存储 |
| --- | --- | --- | --- |
| [Docker 一体镜像](#方式一-docker-一体镜像) | 快速体验、单机部署 | 镜像内置 | 镜像内置 |
| [Docker Compose](#方式二-docker-compose) | 单机运行，但分开升级各组件 | Compose 内的容器 | Compose 内的容器 |
| [Helm](#方式三-helm) | 部署到 Kubernetes | Chart 内的 StatefulSet | Chart 内的 StatefulSet |

三种方式都自带数据库与指标存储，开箱即用。要接自己的 PostgreSQL 或已有的 VictoriaMetrics，见[配置参考](#配置参考)。

启动之后的使用过程相同：打开 Console，创建全局管理员，然后[接入第一个集群](#接入第一个集群)。

## 端口、镜像与持久化

| 端口 | 协议 | 用途 |
| --- | --- | --- |
| `8080` | TCP | Console、HTTP API 和 Agent 一次性注册接口 |
| `8081` | TCP | Pod Access 独立 Origin，使用 Pod 临时访问时需要 |
| `8443` | UDP | Agent QUIC/mTLS 长连接，接入集群时需要 |

`8443` 是 UDP。安全组、负载均衡和 `docker run -p` 都要显式按 UDP 放通，否则 Agent 能完成 HTTP 注册但建不起长连接。

| 镜像 | 内容 |
| --- | --- |
| `ghcr.io/togettoyou/zke-server-all` | Server + PostgreSQL + 指标存储一体镜像 |
| `ghcr.io/togettoyou/zke-server` | 仅 Server |
| `ghcr.io/togettoyou/zke-agent` | Agent，由集群安装清单引用，不需要手动运行 |

`main` 分支推送 `latest`，Git Tag 推送同名版本 Tag；Helm Chart 发布在 `oci://ghcr.io/togettoyou/charts/zke`，
分别对应 `0.0.0-latest` 与去掉 `v` 前缀的语义化版本。

持久化目录有三个：`/data` 保存 Server Managed PKI，所有方式都需要；`/var/lib/postgresql/data` 保存数据库；
`/var/lib/victoria-metrics` 保存一体镜像中的指标样本。`/data` 丢失但数据库仍有 PKI 状态时，Server 会失败
关闭而不是重新签发 CA，因此升级和重建容器时必须保留该卷。

## 方式一 Docker 一体镜像

```bash
docker run -d --name zke \
  -p 8080:8080 \
  -p 8081:8081 \
  -p 8443:8443/udp \
  -v zke-data:/data \
  -v zke-postgresql-data:/var/lib/postgresql/data \
  -v zke-metrics-data:/var/lib/victoria-metrics \
  ghcr.io/togettoyou/zke-server-all:latest
```

容器内依次启动 PostgreSQL、VictoriaMetrics 和 ZKE Server，任何一个退出都会结束整个容器。

容器内的 PostgreSQL 使用镜像默认账号和密码，`5432` 不对外发布，只在容器内可达。要把它接入共享网络或发布该
端口时，先改掉默认密码——密码只在数据卷首次初始化时生效，因此两项必须在第一次启动时一起给出：

```bash
# 追加到上面的 docker run
-e POSTGRES_PASSWORD='<password>'
-e ZKE_DATABASE_URL='postgres://zke:<password>@127.0.0.1:5432/zke?sslmode=disable'
```

指标存储是单节点 VictoriaMetrics，只监听容器内的 `127.0.0.1:8428`。可调整项：

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `ZKE_OBSERVABILITY_METRICS_ENABLED` | `true` | 设为 `false` 时不启动指标存储，Server 也不向 Agent 提供摄取能力 |
| `ZKE_METRICS_RETENTION_PERIOD` | `1` | 保留期，不带单位时以月计；也接受 `1d`、`12w`、`1y` |
| `ZKE_METRICS_STORAGE_DATA_PATH` | `/var/lib/victoria-metrics` | 样本存放目录 |
| `ZKE_METRICS_LISTEN_ADDRESS` | `127.0.0.1:8428` | 改为 `0.0.0.0:8428` 并发布端口后可用 Grafana 等直接查询。该端点没有认证 |

查看启动日志：`docker logs -f zke`。

## 方式二 Docker Compose

Compose 文件位于 `deploy/docker/`，Server、PostgreSQL 与指标存储是三个独立容器：

```bash
cd deploy/docker
cp .env.example .env
# 把 .env 中的 ZKE_POSTGRES_PASSWORD 换成随机密码，例如 openssl rand -hex 24
docker compose up -d
```

`.env` 可覆盖镜像、宿主机端口、Pod Access 外部地址、Agent 默认接入端点和指标存储，取值见 `.env.example`。
多集群指标默认启用；PostgreSQL 只绑定宿主机回环地址，指标存储不发布到宿主机。

`docker compose logs -f server` 查看日志，`docker compose down` 停止服务且不删除数据卷。

## 方式三 Helm

```bash
helm upgrade --install zke oci://ghcr.io/togettoyou/charts/zke \
  --version 0.0.0-latest \
  --namespace zke-system \
  --create-namespace
```

Chart 负责 Server、PostgreSQL、指标存储、监听端口与持久化，可配置项见 `deploy/chart/values.yaml`。默认
Service 类型是 `ClusterIP`，本机验证可以直接端口转发：

```bash
kubectl -n zke-system port-forward service/zke-server 8080:8080 8081:8081
```

两项 Agent 地址均留空时，Chart 使用当前 release 的 Server Service FQDN 作为平台默认端点，只对 Server 所在集群
有效。要接入其他集群，需要把 Server 暴露到集群外，并配套提供目标集群可达的入口：

```bash
helm upgrade --install zke oci://ghcr.io/togettoyou/charts/zke \
  --version 0.0.0-latest \
  --namespace zke-system \
  --create-namespace \
  --set server.service.type=LoadBalancer \
  --set server.agentInstall.publicHTTPURL=http://zke.example.com:8080 \
  --set server.agentInstall.publicQUICAddress=zke.example.com:8443 \
  --set server.podAccessExternalURL=http://pod-access.example.com:8081
```

两项 Agent 地址必须同时提供，只配置其中一项时 Chart 渲染会直接失败。Ingress 控制器通常只处理 TCP/HTTP，UDP
`8443` 需要通过 `LoadBalancer`、`NodePort` 或四层入口单独暴露。其余接入端点和安装默认值在 Console 的
「平台配置」中管理，不需要重新部署。

## 接入第一个集群

打开 Console（默认 <http://127.0.0.1:8080>），首次进入时按引导设置全局管理员的用户名和密码。Server 不预置任何
默认账号，初始化只能成功执行一次。随后：

1. 「组织与资源」中创建 Tenant 和 Project：Cluster 必须属于某个 Project，权限也按这两层划分；
2. 「集群接入管理」中创建接入凭证，填写凭证名称（首次注册时作为集群初始名称）、选择接入端点、指定 Agent
   将要部署到的 Namespace；
3. 复制 `curl | kubectl apply` 命令，在目标集群执行，部署 Agent、Secret 与最小 RBAC；
4. 回到「集群接入管理」等待集群状态变为在线。

凭证一次性使用，15 分钟内有效，Token 明文只出现一次。Agent Namespace 在首次接入时固化到该 Cluster，重新接入
沿用已保存的值。

### 让 Agent 能连上 Server

Agent 先用 HTTP 注册地址完成一次性注册，再用 QUIC 地址建立长连接。两个地址都来自创建凭证时选择的接入端点，并
写入该凭证的不可变快照，选错后只能重新创建凭证。

数据库预置了两个端点预设：

| 预设 | HTTP 注册地址 | QUIC 地址 | 适用场景 |
| --- | --- | --- | --- |
| 本机回环预览 | `http://127.0.0.1:8080` | `127.0.0.1:8443` | Agent 与 Server 在同一网络命名空间 |
| Docker Desktop / OrbStack | `http://host.docker.internal:8080` | `host.docker.internal:8443` | 本机 Docker Desktop / OrbStack 集群 |

接入其他主机或网络中的集群时，在「平台配置」的「端点」中新增一条：填写目标集群能访问到的 HTTP 注册 URL 和
`host:port` 形式的 QUIC 地址；注册地址由私有 CA 签发 HTTPS 证书时，一并提供注册 HTTPS CA。保存后即可在创建凭证
时选择该端点，新端点的 QUIC Host 会自动进入 Server Listener 证书的 SAN，既有连接不受影响，也不需要重启。

连不上时按顺序排查：

- 目标集群能否访问该 HTTP 注册地址（`curl` 它的 `/readyz`）；
- UDP `8443` 是否在安全组、防火墙和负载均衡上放通；
- QUIC 地址是否与凭证选中的端点一致——证书 SAN 按端点签发，改用其他地址会握手失败；
- Server 日志中是否有该 Cluster 的注册或握手记录。

## 配置参考

ZKE 的配置分两层：Server 启动前必须确定的引导配置，和启动后由全局管理员在 Console 中修改的平台配置。

### 引导配置

容器内置 `/etc/zke/zke-server.yaml`，负责数据库连接、三个监听地址、平台默认接入端点、Managed PKI 目录与证书
有效期，以及认证、超时、并发和资源上限。完整字段见仓库中的 `configs/zke-server.yaml`。

部署常用的几项可以直接用环境变量覆盖，环境变量优先于 YAML：

| 环境变量 | 对应配置 | 说明 |
| --- | --- | --- |
| `ZKE_DATABASE_URL` | `database.url` | PostgreSQL 连接串 |
| `ZKE_CONSOLE_DIRECTORY` | `http.console_directory` | Console 静态资源目录，镜像已设置 |
| `ZKE_POD_ACCESS_EXTERNAL_URL` | `pod_access.external_url` | 浏览器访问 Pod Access 的外部地址 |
| `ZKE_AGENT_INSTALL_PUBLIC_HTTP_URL` | `agent_install.public_http_url` | 平台默认端点的 HTTP 注册地址 |
| `ZKE_AGENT_INSTALL_PUBLIC_QUIC_ADDRESS` | `agent_install.public_quic_address` | 平台默认端点的 QUIC 地址 |
| `ZKE_OBSERVABILITY_METRICS_ENABLED` | `observability.metrics.enabled` | 是否启用多集群指标，布尔值 |
| `ZKE_OBSERVABILITY_METRICS_STORAGE_WRITE_URL` | `observability.metrics.storage_write_url` | 指标存储的 remote write 端点 |
| `ZKE_OBSERVABILITY_METRICS_STORAGE_QUERY_URL` | `observability.metrics.storage_query_url` | 指标存储的查询根路径 |

要改其余字段（例如启用原生 HTTPS、调整会话超时），挂载一份自定义 YAML 覆盖容器内的
`/etc/zke/zke-server.yaml`，例如 `-v "$(pwd)/zke-server.yaml:/etc/zke/zke-server.yaml:ro"`。未出现在文件中的键
沿用内置默认值，只写要改的部分即可。Helm Chart 用 ConfigMap 生成同一路径的文件，当前只写入 `agent_pki`、
`helm` 与 `agent_install`，其余字段尚未通过 values 开放。

Server 的数据卷挂在 `/data`，其中 `data/pki` 是 Managed PKI 的材料，丢了会导致启动失败；`data/helm` 是
Chart 仓库缓存（索引与 Chart 包），随时可以删除，只会让下一次浏览目录重新联网。

平台默认端点由 `agent_install` 的两项配套提供；均为空时使用「本机回环预览」。Server 每次启动都按当前有效配置
重新同步部署端点，Console 不能修改、删除或另设平台默认端点。

### 指标存储

三种部署方式都自带单节点 VictoriaMetrics 并默认启用指标，不需要额外准备。改用已有的 VictoriaMetrics，或者
关闭指标：

```bash
# docker run：指向自己的存储
-e ZKE_OBSERVABILITY_METRICS_STORAGE_WRITE_URL='http://victoriametrics.example.internal:8428/api/v1/write'
-e ZKE_OBSERVABILITY_METRICS_STORAGE_QUERY_URL='http://victoriametrics.example.internal:8428/prometheus'
# 或者关闭：
-e ZKE_OBSERVABILITY_METRICS_ENABLED=false
```

Compose 在 `.env` 中设置同名变量；Helm 使用 `server.metrics.enabled`、`server.metrics.storageWriteURL` 与
`server.metrics.storageQueryURL`，后两项必须同时提供。给出外部地址后 Chart 不再部署自带存储；自带存储的
镜像、保留期与容量在 `metrics.*` 下调整。ZKE 不管理外部存储的生命周期、容量与保留期。

关闭时 Server 不向 Agent 提供摄取能力，集群侧不部署任何采集组件，Console 的「可观测性」会说明本部署未启用
指标存储。

集群侧的采集组件不随部署下发：接入集群后在「可观测性 → 采集接入」中一键安装，三个组件一并安装、一并卸载。
其中 node-exporter 需要 host 网络与 hostPath，`baseline` 或 `restricted` Pod Security 级别的 Namespace 会
拒绝它——其余组件照常工作，要启用它需要自行把 Agent Namespace 的 `pod-security.kubernetes.io/enforce` 设为
`privileged`。组件清单、权限边界与视图能力见[可观测性平台](features/observability.md)，容量估算与每集群
摄取预算见[Phase 3 可观测性架构设计 · 7.4](architecture/observability-phase-3.md#74-容量估算与默认预算)。

### 平台配置

Agent 与采集组件的镜像、资源预算，接入端点预设，以及集群终端的会话参数，都保存在数据库，由全局管理员在
Console 的「平台配置」中修改，不需要重新部署。见[平台配置](features/platform-settings.md)。

## 升级、备份与卸载

升级只需替换镜像并保留数据卷，数据库迁移由新版本 Server 在启动时自动执行；迁移失败或超时会导致启动失败，而不是
带着半迁移的库继续运行。

```bash
# Docker：拉取新镜像后用原来的命令重建容器，保持相同的卷名
docker pull ghcr.io/togettoyou/zke-server-all:latest && docker rm -f zke

# Docker Compose
cd deploy/docker && docker compose pull && docker compose up -d

# Helm
helm upgrade --install zke oci://ghcr.io/togettoyou/charts/zke \
  --version 0.0.0-latest --namespace zke-system
```

Server 升级后，已接入集群里的 Agent 仍运行原有版本，Console 的集群列表和集群详情会标出与 Server 版本不一致的
Agent。该提示只用于发现没有跟上的集群：版本不同不影响连接和任何功能，Server 与 Agent 仍按原有协议通信。

备份要同时覆盖数据库（`pg_dump` 或卷快照）和 `/data` 中的 Managed PKI，并且是同一时刻的一致快照；只恢复其中
一个会导致 Server 失败关闭或 Agent 证书链失效。指标样本不在此列，丢失只影响历史曲线。`docker rm` 与
`helm uninstall` 都不会删除数据卷，确认不再需要后手动删除对应的 volume 或 PVC。

## 数据与安全

- ZKE Server 当前按单实例运行，Agent 连接和部分限流状态在进程内存中；第二个实例会把自己没有持有的 Cluster
  报告为离线，并使限流阈值翻倍。
- Managed PKI 私钥只能保存在受保护的持久卷；数据库有 PKI 状态而文件丢失时 Server 会失败关闭。
- Agent Enrollment Token 只返回一次；安装 Manifest 通过 Authorization Header 获取，不能把 Token 放入 URL 或日志。
- Console/API 的 HTTPS 与 Agent QUIC/mTLS 是两套独立身份。共享环境应在网关终止 Console/API HTTPS，并正确暴露
  UDP `8443`。
- 会话 Cookie 的 `Secure` 属性没有配置项：Server 按请求判断，原生 TLS 直接成立，网关终止 TLS 时取
  `X-Forwarded-Proto`。网关必须转发这个请求头，否则 Cookie 会缺少 `Secure`。
- Pod Access 是与 Console 隔离的第二 Origin；`pod_access.external_url` 必须是浏览器真正访问到的地址，前置网关
  必须保留该 Host 头。
