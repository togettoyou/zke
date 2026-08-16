# 部署清单

本目录保存 ZKE 的部署清单。完整的部署方式选择、端口说明、集群接入步骤与升级备份边界见
[部署指南](../docs/deployment.md)。

| 目录 | 内容 |
| --- | --- |
| `docker/` | Docker Compose 文件与环境变量样例，Server、PostgreSQL 与指标存储分离为三个容器 |
| `chart/` | Helm Chart，部署 ZKE Server、PostgreSQL 与指标存储到 Kubernetes |

只想最快跑起来时不需要本目录：一体镜像 `ghcr.io/togettoyou/zke-server-all` 一条 `docker run` 即可启动，见
[README 的快速开始](../README.md#快速开始)。

## Docker Compose

```bash
cd deploy/docker
cp .env.example .env
# 把 .env 中的 ZKE_POSTGRES_PASSWORD 换成随机密码，例如 openssl rand -hex 24
docker compose up -d
```

`.env.example` 列出了全部可覆盖项：镜像、宿主机端口、Pod Access 外部地址、Agent 默认接入端点和指标存储。
多集群指标默认启用，写入 Compose 内的 `victoriametrics` 服务。

## Helm

```bash
helm upgrade --install zke oci://ghcr.io/togettoyou/charts/zke \
  --version 0.0.0-latest \
  --namespace zke-system --create-namespace
```

可配置项见 `chart/values.yaml`。Chart 默认启用多集群指标并部署单节点 VictoriaMetrics；把
`server.metrics.storageWriteURL` 与 `server.metrics.storageQueryURL` 指向已有存储时不再部署它。
本地修改 Chart 后先自检再提交：

```bash
helm lint deploy/chart
helm template zke deploy/chart --namespace zke-system > /dev/null
```
