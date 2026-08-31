# 自定义应用

自定义应用让持有 `application.manage` 的用户把已有的 Web 系统作为一等桌面入口接入 ZKE Console。例如 Harbor、Grafana 或项目内部
的发布平台可以拥有自己的名称、说明、Logo 和 URL，并像内置应用一样出现在桌面、窗口和 Dock 中。

这项能力组织入口，不接管目标系统。ZKE 不代理请求、不注入目标系统凭证，也不改变目标系统自己的认证与授权。

## 作用域与权限

- 配置属于一个明确的 Project，Project 删除时随之删除；
- `project.read` 可以列出、查看和打开该 Project 的全部自定义应用；
- `application.manage` 可以创建、修改和删除自定义应用；
- `application.manage` 与 `project.manage` 相互独立，不会授予项目生命周期管理能力；通常还需同时授予
  `project.read`，让应用管理员能够查看应用列表；
- Console 的权限判断只用于隐藏管理入口，每个 API 请求仍由 Server 重新鉴权；
- 创建、修改和删除写入该 Project 的审计轨迹，记录操作者、应用身份、名称、目标 Origin 和结果，不记录 URL 的路径、
  Query 或 Fragment。

一个用户切换 Project 后，只看到新 Project 的应用。旧 Project 的自定义应用窗口会关闭，避免外部系统会话在另一个
项目桌面后继续保持挂载。

## 数据与接口

每条配置保存：

| 字段          | 约束                                          |
| ------------- | --------------------------------------------- |
| `name`        | 1–80 字节，在 Project 内忽略大小写唯一        |
| `description` | 可选，最多 500 字节                           |
| `url`         | 无用户凭证的绝对 HTTP(S) 地址，最多 2048 字节 |
| `logo_url`    | 可选；外链约束同 URL，或 JPEG、PNG、WebP、GIF、AVIF 的 Base64 Data URL（最多 64 KiB） |

项目最多保存 100 个自定义应用。创建请求使用 `Idempotency-Key`，相同操作者、Project 和 Key 的重试返回原对象；同一
Key 携带不同内容会被拒绝。

API 位于：

```text
GET    /api/v1/projects/{project_id}/custom-applications
POST   /api/v1/projects/{project_id}/custom-applications
GET    /api/v1/projects/{project_id}/custom-applications/{application_id}
PUT    /api/v1/projects/{project_id}/custom-applications/{application_id}
DELETE /api/v1/projects/{project_id}/custom-applications/{application_id}
```

## 加载与安全边界

Server 只保存元数据，不主动访问应用 URL 或外链 Logo URL，因此这些字段不会成为 Server 侧 SSRF 入口。外链 Logo 由浏览器直接
加载，并使用 `no-referrer`；Base64 Data URL 会随应用元数据保存，Server 只校验其格式和大小。

应用默认在统一的 ZKE 窗口内通过受限 iframe 打开，同时始终提供“新标签页打开”：

- iframe 不允许顶层导航；
- iframe 使用兼容嵌入模式，授予 `same-origin` 与脚本能力，使 Harbor 等使用 ES Module、Cookie 或浏览器存储的应用能够正常运行；
- 与 Console 初始同源的 URL 不允许内嵌；外部应用依靠浏览器同源策略与 Console 隔离，因此只应配置受信任的应用地址；
- 与 Console 同源的 URL 不内嵌，避免页面直接访问 Console 会话；
- 目标站点可以通过 `Content-Security-Policy: frame-ancestors` 或 `X-Frame-Options` 禁止嵌入，此时用户应使用新标签页；
- 目标系统的登录态、权限和审计仍由目标系统自己负责；ZKE 当前不提供 SSO、Header 注入或凭证托管。

配置中不应写入用户名、密码、Token 或其他敏感查询参数。需要统一认证时，应在目标系统或受信任的接入网关上配置。
