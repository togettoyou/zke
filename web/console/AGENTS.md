# ZKE Console 前端开发指南

本文件适用于 `web/console/` 及其所有子目录，在仓库根 `AGENTS.md` 的基础上补充前端约束。两者冲突时，根文件的
产品、架构、安全与 Git 纪律优先，本文件负责界面实现。根文件的 Canary 规则同样适用。

## 技术栈与命令

Vite 8 + React 19 + TypeScript 5.9（`strict`），pnpm 11 / Node 24；Tailwind CSS 4（语义变量集中在
`src/styles/theme.css`）、Radix UI、TanStack Query 5 与 Table 8、Zustand 5、Zod 4 + react-hook-form、
Sonner（经 `components/common/toaster.tsx`）、xterm.js；时序图表使用 uPlot。API 类型由 `openapi-typescript`
从 `api/openapi/zke-server.v1.yaml` 生成。

```bash
pnpm dev            # 开发服务器
pnpm typecheck      # tsc -b
pnpm lint           # eslint
pnpm format         # prettier --write
pnpm build          # tsc -b && vite build
pnpm gen:api        # 重新生成 src/api/schema.d.ts
```

`src/api/schema.d.ts` 是生成文件，不得手工编辑；OpenAPI 契约变更后运行 `pnpm gen:api`。

## 目录分层

```
src/
  api/        openapi-fetch 客户端、错误映射、SSE、按领域划分的 query hooks
  apps/       桌面应用。每个应用一个目录，registry.ts 是唯一的应用清单
  auth/       登录、首次初始化、会话与权限能力
  components/ui/      无样式原语的样式化封装；common/ 跨应用复合组件；brand/ 品牌标记
  desktop/    桌面外壳：窗口、Dock、顶栏、启动器、持久化
  scope/      租户/项目作用域选择器与 store
  lib/        无 UI 依赖的纯工具
  styles/     theme.css，全部设计变量的唯一来源
```

依赖方向：`apps` → `components` → `ui`/`lib`。`components/ui` 不得引用 `apps`。`api/queries` 只在需要暂停轮询
时引用 `desktop/window-visibility`，除此之外不得依赖外壳。新增原语放 `components/ui/`，跨应用复合组件放
`components/common/`，只有一个应用用到的留在该应用目录内。

## 设计系统：不可协商

**颜色只允许语义 token。** 禁止 Tailwind 调色板（`bg-blue-500`）、字面色值，以及未在 `theme.css` 中定义的
token。完整集合：

- 表面 `surface`、`surface-muted`、`surface-raised`、`surface-overlay`
- 文字 `foreground`、`muted-foreground`、`subtle-foreground`
- 描边 `border`、`border-strong`
- 主色 `primary`、`primary-hover`、`primary-foreground`、`primary-surface`
- 语义 `success`、`warning`、`danger`、`info`、`neutral`，各带 `-surface` 变体
- 焦点 `ring`；代码 `code-key`、`code-string`、`code-literal`、`code-comment`、`code-punctuation`、`code-meta`；
  桌面 `desktop-from`、`desktop-to`

不存在 `background`、`muted`、`card`、`accent`、`destructive`、`warning-foreground` 等 shadcn 习惯名。Tailwind
对未定义 token 不报错，只静默不生成类——`bg-background` 的元素是完全透明的。新增 token 必须同时加进
`theme.css` 的 `:root`、`[data-theme="dark"]` 和 `@theme inline` 三处。两个例外已在 `theme.css` 写明理由：
应用图标面上的字形恒为 `text-white`，对话框遮罩为 `bg-black/35`。**深浅色是一等公民**，任何新界面都必须在两套
主题下成立。

**圆角**只用 `rounded-inline`(4，行内文字按钮焦点光晕) / `rounded-control`(7，按钮、输入框、下拉项) /
`rounded-panel`(10，卡片、表格容器、菜单、弹层) / `rounded-window`(12，窗口与对话框) / `rounded-full`；不用
`rounded-sm|md|lg|xl|2xl` 和裸 `rounded`。应用图标瓦片不在此刻度上：圆角是边长的 31.25%，使同一图标在启动器、
Dock 和品牌位是同一形状。

**字号**只用 `text-[11px]`（外壳次级说明）/ `text-xs`(12，表头、提示、元信息) / `text-[13px]`（正文默认）/
`text-sm`(14，区块标题) / `text-[15px]`（对话框标题）/ `text-[22px]`（页面标题与概览大数字）。不引入新的中间值。

**高度**用 `shadow-e1|e2|e3` 与 `shadow-window|window-focused`，不用 Tailwind 默认阴影。**卡片不叠阴影**：窗口
本身已有高度，窗口内的卡片与表格容器一律靠 `border` + `bg-surface` 划分。

**焦点**一律 `zke-focus`（复合控件 `zke-focus-within`），它画紧贴边框的 3px 光晕。不要写 `outline-none` 而不补
焦点样式，也不要用默认的偏移 outline。

**动效**统一定义在 `theme.css`，组件只挂类名：`zke-overlay-motion`（遮罩）、`zke-dialog-motion`（对话框本体）、
`zke-pop-motion`（菜单、Select、Popover、Tooltip，按 Radix `data-side` 展开）、`zke-window-motion`、`zke-rise`、
`zke-interacting`。新的瞬态浮层必须挂对应类名，退场动画依赖 Radix 的 `data-state="closed"`，删掉动画等于删掉
退场。曲线只有两条：入场 `--ease-lift`，大位移 `--ease-reveal`；离场一律 `linear`。本项目**刻意不实现**
`prefers-reduced-motion`，原因写在 `theme.css` 末尾，要改这个决定先读那段注释。

## 弹窗还是页面

**用对话框当且仅当**：敏感操作确认（一律走 `SensitiveActionDialog`，它统一呈现目标、影响列表和输入名称二次
确认）；一次性密钥展示；单字段或两三个字段的短提示。

**其余一律用页面视图**：`PageHeader` + 替换工作区内容。判据是——需要滚动、字段超过约五个、带列表或目录、需要
并排比较、或者操作者可能中途去别处查东西。参考 `container-service/` 下的 `*View.tsx` 与 `*Form.tsx`。

- 标题、返回、主操作放在不滚动的 `PageHeader`：读到底部的人最需要「保存」和「返回」；
- 打开 `PageHeader` 时外壳隐藏工具栏，其表达的作用域（集群 / 命名空间）由 `AppShell` 的 `scope` 以文字接管——
  多集群产品里不写明集群的对象页是读不了的；
- 表单正文加 `max-w-4xl` 一类度量约束；服务端拒绝的原因显示在主操作附近；
- 单字段对话框必须包 `<form onSubmit>` 让回车可提交，取消按钮显式写 `type="button"`；
- 对话框宽度写成 `w-[min(720px,calc(100vw-2rem))]`——`DialogContent` 宽度是固定的 `w-`，加 `max-w-2xl` 无效；
- 请求进行中禁止通过遮罩点击或 Esc 关闭（`SensitiveActionDialog` 已实现）。

## 复用而不是重写

| 需求 | 用这个 |
| --- | --- |
| 按钮 | `ui/button` 的 `Button`，不要裸 `<button>`（纯文字链接式点击除外，且必须挂 `zke-focus`） |
| 勾选框 / 开关 | `Checkbox`、`Switch`，**绝不用原生 `<input type="checkbox">`**——它是页面上唯一不跟随主题的元素 |
| 输入 / 下拉 | `Input`、`Textarea`、`NumericInput`；`Select` 系列，不要原生 `<select>` |
| 表格 | `common/data-table` 的 `DataTable`，自带加载骨架、错误态、空态和两种分页 |
| 加载 / 空 / 错误 | `common/state` 的 `LoadingState`、`EmptyState`、`ErrorState` |
| 失败提示 | `common/notify` 的 `notifyFailure(动作, error)`，它带上服务端原因和请求 ID；不要裸 `toast.error()` |
| 详情行 / 状态标记 | `common/detail`；`common/status` 的 `StatusBadge`、`RelativeTime`、`IdentifierLabel` |
| 删除入口 | `common/delete-action` |
| 密钥展示 | `common/secret-reveal` 的 `SecretReveal`。**任何一次性凭证都要走它**——Enrollment Token、安装命令、Pod 访问地址一律遮蔽后显示，不要用只读 `Input` 明文摆出来 |

## 数据、作用域与权限

- 所有服务端读写走 `api/queries/` 下按领域划分的 hook，组件里不直接调 `api.GET`；查询键集中在
  `api/query-keys.ts`；变更成功后失效相关查询，不手动改缓存里的列表；
- 幂等的创建类请求用 `lib/use-submission-key` 生成键，重试不得产生第二个对象；
- 权限判断用 `useSessionContext().permissions.can(permission, scope)`，**只决定是否显示入口**；服务端仍会对每个
  请求重新授权，前端判断不是安全边界；
- 跨集群操作必须显式携带目标 Cluster，必要时还有 Namespace；section 组件按 `clusterId` 加 `key`，切换目标基础
  设施时丢弃旧状态；
- 错误必须区分认证失败、无权限、目标不存在、连接失败、执行失败和超时——用 `api/errors.ts` 的 `isForbidden` 等
  判定，不要比对文案；
- 密钥、Token、kubeconfig 不得进入日志、遥测或错误文案。

## 性能

- **应用懒加载**：`apps/registry.ts` 里每个应用都是 `lazy()`，新增应用照办；
- **窗口与内容解耦**：`Window` 是 `memo` 的，应用内容用 `useMemo` 固定引用，拖拽时只动 `transform`；不要把新的
  props 直接内联进 `<manifest.entry />`；
- **拖拽走合成层**：移动用 `transform`，位置状态用 `translate`，两者是不同的 CSS 属性，不要合并；
- **窗口不可见时暂停轮询**：带 `refetchInterval` 的查询要接受 `{ live }` 并用 `useWindowVisible()` 在最小化时
  停摆——每个请求最终都由某个集群的 Agent 执行；
- 长列表分页由服务端决定，不在前端拉全量再切片；加载态与内容态高度接近，避免数据到达时整页跳动；
- 避免 `transition-all`，只写真正会变的属性。

## 可访问性与文案

- 图标按钮必须有 `aria-label`，纯装饰图标加 `aria-hidden`；表单控件用 `htmlFor` / `id` 配对；
- 异步失败用常驻挂载的 `aria-live` 区域播报（插入时才出现的 live region 可能完全不会被读出）；
- 文案以简体中文为主，保留 Kubernetes 与 ZKE 的英文专名（Pod、Namespace、Agent、Enrollment Token 等）；中文标题
  不做 `uppercase` 与 `tracking` 处理；
- 不虚构未实现的能力；规划中的能力用 `availability: { state: "planned" }` 声明，由 `PlannedApp` 如实呈现。

## 注释风格

注释解释**为什么**，不解释做了什么。凡是「看起来可以更简单却没有」的地方——一个刻意的取值、一个绕开的坑、一个
被推翻的做法——都应说明它为什么是现在这样，以及改回去会发生什么。不写重述代码的注释，也不写变更日志式注释。

## 提交前

```bash
pnpm typecheck && pnpm lint && pnpm format:check && pnpm build
```

改了视觉的还要自查：深浅两套主题都看过；没有引入 `theme.css` 之外的颜色、圆角、字号、阴影；新浮层挂了动效类名；
Tab 顺序合理、焦点可见、回车能提交表单；窄视口没有横向滚动；加载态不让内容跳动；敏感操作走
`SensitiveActionDialog` 且写清作用域与影响。无法验证的项要在最终回复里如实说明，不能写成「已通过」。
