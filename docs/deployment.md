# 部署指南

ZKE Server 是单个 Go 二进制，内置 Console 静态资源，依赖 PostgreSQL、一个持久目录，以及启用多集群指标时的
VictoriaMetrics。

ZKE 当前处于开发预览阶段，投入关键环境前应自行完成安全、备份、容量和升级验证。

## 选择部署方式

| 方式 | 适用场景 | 数据库 | 指标存储 |
| --- | --- | --- | --- |
| [Docker 一体镜像](#方式一-docker-一体镜像) | 快速体验、单机部署 | 镜像内置 | 镜像内置 |
| [Docker 连接已有 PostgreSQL](#方式二-docker-连接已有-postgresql) | 已有数据库，独立备份与运维 | 自备 | 自备或关闭 |
| [Docker Compose](#方式三-docker-compose) | 单机运行，但分开升级各组件 | Compose 内的容器 | Compose 内的容器 |
| [Helm](#方式四-helm) | 部署到 Kubernetes | Chart 内的 StatefulSet | Chart 内的 StatefulSet |

除方式二外，多集群指标默认启用并自带存储，接入集群后在「可观测性 → 采集接入」中安装采集组件即可。

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

## 方式二 Docker 连接已有 PostgreSQL

用只包含 Server 的镜像启动，`ZKE_DATABASE_URL` 指向已有数据库：

```bash
docker run -d --name zke \
  -p 8080:8080 \
  -p 8081:8081 \
  -p 8443:8443/udp \
  -v zke-data:/data \
  -e ZKE_DATABASE_URL='postgres://zke:<password>@db.example.com:5432/zke?sslmode=disable' \
  ghcr.io/togettoyou/zke-server:latest
```

数据库和账号需要提前建好，账号要有在该库内建表的权限；Server 在开始监听 HTTP 之前自动执行迁移。密码会进入
URL，应使用 URL-safe 的随机字符串。仓库的镜像、Compose 与 Chart 都使用 PostgreSQL 17。

这个镜像不带指标存储，而内置配置默认开启指标并指向 `127.0.0.1:8428`。因此必须二选一：把存储地址指向可达的
VictoriaMetrics，或者关闭指标，取值见[启用多集群指标](#启用多集群指标)。

## 方式三 Docker Compose

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

## 方式四 Helm

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
沿用内置默认值，只写要改的部分即可。Helm Chart 用 ConfigMap 生成同一路径的文件，当前只写入 `agent_pki` 与
`agent_install`，其余字段尚未通过 values 开放。

平台默认端点由 `agent_install` 的两项配套提供；均为空时使用「本机回环预览」。Server 每次启动都按当前有效配置
重新同步部署端点，Console 不能修改、删除或另设平台默认端点。

### 启用多集群指标

一体镜像、Docker Compose 与 Helm 都自带单节点 VictoriaMetrics 并默认启用指标，不需要额外准备；只包含
Server 的镜像不带存储。

改用已有的 VictoriaMetrics，或关闭指标：

```bash
# docker run：指向自己的存储
-e ZKE_OBSERVABILITY_METRICS_STORAGE_WRITE_URL='http://victoriametrics.example.internal:8428/api/v1/write'
-e ZKE_OBSERVABILITY_METRICS_STORAGE_QUERY_URL='http://victoriametrics.example.internal:8428/prometheus'
# 或者关闭：
-e ZKE_OBSERVABILITY_METRICS_ENABLED=false
```

Compose 在 `.env` 中设置同名变量；Helm 使用 `server.metrics.enabled`、`server.metrics.storageWriteURL` 与
`server.metrics.storageQueryURL`，后两项必须同时提供。给出外部地址后 Chart 不再部署自带存储，自带存储的
镜像、保留期与容量在 `metrics.*` 下调整。ZKE 不管理外部存储的生命周期、容量与保留期。

关闭时 Server 不向 Agent 提供摄取能力，集群侧不部署任何采集组件，Console 的「可观测性」会直接说明本部署
未启用指标存储。

启用后在「可观测性 → 采集接入」的集群列表中逐个安装或卸载，需要 `cluster.metrics.manage`；查看指标是另一个
权限 `cluster.metrics.read`。组件由该集群的 Agent 安装到自己的 Agent Namespace，摄取凭证也由 Agent 在
集群内生成，不经过 Server。

**一次安装放进集群三个组件**，一次卸载全部删除——没人抓取的导出器是浪费，而抓取一个从未安装的目标只会
产生持续失败的 job：

| 组件 | 形态 | 提供什么 | 默认镜像 |
| --- | --- | --- | --- |
| 采集组件 vmagent | Deployment × 1 | 抓取下面两个与 kubelet，经 Agent 回传 | `victoriametrics/vmagent:v1.149.0` |
| kube-state-metrics | Deployment × 1 | 节点可分配量、Pod 申请/限制、工作负载归属与重启 | `registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.1` |
| node-exporter | DaemonSet（每节点） | 磁盘、文件系统、网络 | `quay.io/prometheus/node-exporter:v1.12.1` |

kube-state-metrics 是**利用率的分母**：没有它，用量曲线无法回答任何容量问题，「利用率」「申请量」「限制量」
与「工作负载」四组视图都会是空的。它以 `--resources` 与 `--metric-allowlist` 双重收窄，只 `list`/`watch`
Node、Pod、Namespace 与五类工作负载对象，**不包含 Secret 与 ConfigMap**，也没有任何写权限。

**node-exporter 需要 host 网络与 hostPath**，这是它读取 `/proc`、`/sys` 与根文件系统的唯一方式。运行在
`baseline` 或 `restricted` Pod Security 级别的 Namespace 会拒绝它。ZKE **不会**为此改写 Agent Namespace 的
安全等级——那不是一次安装可以顺带做的事。被拒绝时其余两个组件照常安装，采集接入列表中该行显示「已被集群
拒绝」并说明原因，磁盘与网络视图会说明它依赖这个组件。要启用它，需要部署方自行把 Agent Namespace 的
`pod-security.kubernetes.io/enforce` 设为 `privileged`。

三个组件的镜像、拉取策略与资源请求/限制都在「平台配置 → 指标采集」中管理，留空表示不设置该项。修改后对
下一次安装生效，已安装的集群会在采集接入列表中提示可以更新。node-exporter 跑在每个节点上，它的预算会乘以
集群规模，因此默认值刻意很小（请求 `10m` / `32Mi`，限制 `200m` / `128Mi`）。

安装不需要为 Agent 增加任何权限：Agent 授予 kube-state-metrics 的读取范围是它自己已经持有的子集，
Kubernetes 也正是这样保证的——它拒绝创建一个包含创建者本身没有的权限的 ClusterRole。

采集数据经该集群 Agent 已有的 QUIC 连接回传，不需要为指标开通新的网络路径，也不需要重新应用 Agent 清单。
存储不可用时只影响指标查询。

### 指标容量、保留期与摄取预算

ZKE 没有给出实测的吞吐、延迟或容量承诺。下面是估算方法和可调项，实际取值必须以自己部署中观察到的数字为准。

**一个集群产生多少序列。** 三个抓取目标的配置与 allowlist 都是固定的，因此序列数可以直接从集群规模算出来。
记 N = 节点数、P = Pod 数、C = 容器数：

```text
kubelet             ≈ 7×N + 2×P + 2×C
kube-state-metrics  ≈ 27×N + 6×P + 5×C
node-exporter       ≈ (8×单节点核数 + 140)×N
样本速率（每秒）     ≈ 序列总数 ÷ scrape_interval（默认 30s）
```

各项来源：kubelet 每节点约 7 条 = 2 条节点指标加约 5 条 vmagent 自己产生的抓取元信息（条数随 vmagent 版本
略有出入），Pod 与容器各贡献 CPU 和内存两条；kube-state-metrics 的每节点部分主要是可分配量、容量与节点
状况，每 Pod 部分是归属与 phase，每容器部分是申请、限制与重启计数；node-exporter 中占大头的是
`node_cpu_seconds_total`，它按「核数 × 模式数」展开，其余 meminfo、filesystem、diskstats、netdev、loadavg
合计每节点约 140 条。

例如 100 节点（每节点 16 核）、2000 Pod、3000 容器的集群：kubelet 约 10700 条、kube-state-metrics 约
29700 条、node-exporter 约 26800 条，合计约 67000 条序列、约 2200 样本/秒。**这大约是只抓 kubelet 时的
六倍**——多出来的部分买到的是利用率、申请量、工作负载归属与磁盘网络，但它确实是六倍，规划容量时要按这个
数字算。只安装采集组件而拒绝 node-exporter 的集群按前两项估算。

**磁盘。** 每个样本占用多少字节取决于压缩率，属于 VictoriaMetrics 的行为而不是 ZKE 的；请以上游文档的经验
值做初次规划，并在运行一到两个保留周期后按实际磁盘增长修正。ZKE 不为此提供预估公式。

**保留期。** 自带存储的保留期用 `ZKE_METRICS_RETENTION_PERIOD` 调整（一体镜像与 Compose），Helm 在
`metrics.*` 下；不带单位时以月计，也接受 `1d`、`12w`、`1y`。外部存储的保留期由部署方自己管理，ZKE 不修改它。

**每集群摄取预算。** Server 对每个集群分别限制样本速率与活跃序列数，防止一个集群占满所有集群共用的存储。
默认值是保护性的，远高于上面公式给出的正常量级：

| 配置项 | 默认值 | 含义 |
| --- | --- | --- |
| `max_samples_per_second_per_cluster` | 50000 | 每集群样本速率上限 |
| `sample_burst_window` | 1m | 允许一次性花掉的速率额度；断线重连时 vmagent 回灌缓冲需要它 |
| `max_active_series_per_cluster` | 500000 | 每集群活跃序列上限 |
| `active_series_window` | 10m | 活跃序列的统计窗口 |

超出预算时 Server 拒绝该批次并返回 `RESOURCE_EXHAUSTED`，vmagent 按自己的退避重试并在本地磁盘缓冲，
超出 `collector_buffer_size` 的最旧数据由 vmagent 丢弃。被拒绝不会静默发生：

- 「可观测性 → 采集接入」的**摄取预算**列显示该集群当前是否被限流、触碰的是速率还是基数，以及活跃序列的
  估算值与上限；限流恢复后仍会显示最近一次被限流的时间，否则一个已经恢复的空洞就没有解释了；
- 「指标总览」中受影响的集群会在图下明确写出空洞由 Server 拒绝造成，而不是采集组件故障；
- Server 日志在进入限流状态时记录一条结构化告警，携带 `cluster_id` 与原因。

活跃序列数是**估算值**，来自固定大小的概率草图：跟踪一个集群的开销与它上报多少序列无关。界面按近似值呈现，
不要把它当作精确计数使用。

### 平台配置

以下配置保存在 PostgreSQL，只能由全局 `admin` 在 Console 的「平台配置」应用修改，不需要重启：

- 端点 —— Agent 接入端点预设：注册 URL、QUIC 地址、可选注册 HTTPS CA；
- Agent —— Agent 的镜像、Image Pull Policy 与 CPU / 内存请求与限制；
- 集群终端 —— Cluster Terminal 会话 Pod 的镜像、Image Pull Policy、CPU / 内存请求与限制，以及会话存续时长，
  可选 1 分钟至 1 小时，默认 15 分钟；
- 指标采集 —— 三个采集组件（vmagent、kube-state-metrics、node-exporter）各自的镜像、Image Pull Policy
  与 CPU / 内存请求与限制。三者一并安装，因此版本也在同一页固定，避免一个部署跑着今年的采集组件和去年的
  导出器。

除端点外，每一页对应一个 ZKE 装进目标集群的工作负载，六个字段的形状完全相同：镜像、拉取策略和四项资源数量。
数量留空表示不在容器上设置该项——Kubernetes 没有别的方式表达「不限制」，需要交给 Namespace 的 LimitRange
决定时就留空。

两个默认值值得说明。Agent 默认 `50m` / `128Mi` 请求、`512Mi` 内存限制、**不设 CPU 限制**：请求的作用是让它不
再是 BestEffort Pod——节点内存不足时 BestEffort 最先被驱逐，而 Agent 一挂，该集群在 ZKE 里就整个失联；不设
CPU 限制则是因为被限流的 Agent 不会报错，只会让该集群的每一次查询、终端按键和日志流都变慢，且任何错误信息里
都看不出原因。这两个数字是让 Pod 可调度、可存活的下限，不是实测用量，实际开销取决于集群规模与被监视的对象
数量，投产前应按自己的部署观察后调整。

Cluster Terminal 默认 `25m` / `64Mi` 请求、`500m` / `256Mi` 限制。内存上限刻意保留余量：终端里的 kubectl
对着大集群取全量 JSON 是其中唯一真正占内存的操作，上限压得太低会让一条正常命令直接被 OOMKill，而现象上完全
看不出是内存限制导致的。

每一页各自保存，只写入本页的改动；离开一页会丢弃其中尚未保存的修改，回到该页看到的始终是当前实际生效的
配置。

生效时机不同：Cluster Terminal 的镜像、拉取策略、资源预算与会话时长立即用于新会话；三个采集组件的取值在下一次
安装时读取；Agent 的镜像、拉取策略、资源预算、Namespace 和凭证选中的端点在新 Enrollment 签发时进入不可变快照，
已签发的凭证和已接入的集群不受影响。

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
