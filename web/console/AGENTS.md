# ZKE Console 前端开发指南

本文件适用于 `web/console/` 及其所有子目录，在仓库根 `AGENTS.md` 的基础上补充前端约束。两者冲突时，根文件的产品、架构、安全与 Git 纪律优先，本文件负责界面实现。

根 `AGENTS.md` 的 Canary 规则同样适用：每次面向用户的回复都必须以独立一行 `ZKE 开发` 开头。

## 技术栈

| 项         | 选型                                                          |
| ---------- | ------------------------------------------------------------- |
| 构建       | Vite 8 + React 19 + TypeScript 5.9（`strict`）                |
| 包管理     | pnpm 11，Node 24                                              |
| 样式       | Tailwind CSS 4，语义变量集中在 `src/styles/theme.css`         |
| 无样式原语 | Radix UI                                                      |
| 服务端状态 | TanStack Query 5                                              |
| 客户端状态 | Zustand 5                                                     |
| 表格       | TanStack Table 8                                              |
| 表单校验   | Zod 4 + react-hook-form                                       |
| 通知       | Sonner，经 `components/common/toaster.tsx` 适配               |
| 终端       | xterm.js                                                      |
| 图表       | uPlot（已选定，随 Phase 3 可观测性视图引入，尚未加入依赖）    |
| API 类型   | `openapi-typescript` 从 `api/openapi/zke-server.v1.yaml` 生成 |

常用命令（在 `web/console/` 下执行）：

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
  components/
    ui/       无样式原语的样式化封装（Button、Input、Select…）
    common/   跨应用的复合组件（DataTable、SensitiveActionDialog、状态视图…）
    brand/    品牌标记
  desktop/    桌面外壳：窗口、Dock、顶栏、启动器、持久化
  scope/      租户/项目作用域选择器与 store
  lib/        无 UI 依赖的纯工具
  styles/     theme.css，全部设计变量的唯一来源
```

依赖方向：`apps` → `components` → `ui`/`lib`。`components/ui` 不得引用 `apps`。`api/queries` 只在需要暂停轮询时引用 `desktop/window-visibility`，除此之外不得依赖外壳。

## 设计系统：不可协商的约束

### 颜色

**只允许使用语义 token。** 禁止 Tailwind 调色板（`bg-blue-500`、`text-gray-400`）、禁止字面色值、禁止未在 `theme.css` 中定义的 token。

可用的完整集合：

- 表面：`surface`、`surface-muted`、`surface-raised`、`surface-overlay`
- 文字：`foreground`、`muted-foreground`、`subtle-foreground`
- 描边：`border`、`border-strong`
- 主色：`primary`、`primary-hover`、`primary-foreground`、`primary-surface`
- 语义：`success`、`warning`、`danger`、`info`、`neutral`，各自带 `-surface` 变体
- 焦点：`ring`
- 代码：`code-key`、`code-string`、`code-literal`、`code-comment`、`code-punctuation`、`code-meta`
- 桌面：`desktop-from`、`desktop-to`

不存在 `background`、`muted`、`card`、`accent`、`destructive`、`warning-foreground` 等 shadcn 习惯名。Tailwind 对未定义的 token 不会报错，只会静默不生成任何类——写了 `bg-background` 的元素是完全透明的，`text-warning-foreground` 的折线图是看不见的。新增 token 必须先加进 `theme.css` 的 `:root`、`[data-theme="dark"]` 和 `@theme inline` 三处。

两个例外，都已在 `theme.css` 中写明理由：应用图标面上的字形恒为 `text-white`，对话框遮罩为 `bg-black/35`。

**深浅色是一等公民。** 任何新界面都必须在两套主题下都成立；因为颜色只来自语义 token，正确写法自动满足这一点。

### 圆角

只用 `rounded-inline`(4) / `rounded-control`(7) / `rounded-panel`(10) / `rounded-window`(12) / `rounded-full`。不用 `rounded-sm|md|lg|xl|2xl` 和裸 `rounded`。

- `inline`：无边框的行内文字按钮的焦点光晕
- `control`：按钮、输入框、下拉项、小分组
- `panel`：卡片、表格容器、菜单、弹层
- `window`：窗口与对话框

应用图标瓦片不在此刻度上：其圆角是边长的 31.25%（24→8、40→13、48→15、64→20），这样同一个图标在启动器、Dock 和品牌位是同一个形状。

### 字号

`text-[11px]` / `text-xs`(12) / `text-[13px]` / `text-sm`(14) / `text-[15px]` / `text-[22px]`。不引入新的中间值——`12.5px` 和 `10.5px` 这类微调只会让同一排文字看起来像没对齐。

- `11px`：桌面外壳的次级说明、徽标副标题
- `12px`：表头、提示、元信息
- `13px`：正文默认，也是列表与表单的主力字号
- `14px`：区块标题
- `15px`：对话框标题
- `22px`：页面级标题与概览大数字

### 高度与阴影

`shadow-e1|e2|e3` 与 `shadow-window|window-focused`，不用 Tailwind 默认阴影。

**卡片不叠阴影。** 窗口本身已经有高度，窗口内部的卡片、表格容器一律靠 `border` + `bg-surface` 划分，不再加阴影——阴影套阴影会把界面变成一堆卡片。

### 焦点

交互元素一律 `zke-focus`（复合控件用 `zke-focus-within`），它画的是紧贴边框的 3px 光晕。不要写 `outline-none` 而不补焦点样式，也不要用默认的偏移 outline——整宽输入框套一圈蓝框非常难看。

### 动效

统一在 `theme.css` 中定义，组件只挂类名：

- `zke-overlay-motion`：对话框遮罩淡入淡出
- `zke-dialog-motion`：对话框本体淡入 + 轻微缩放
- `zke-pop-motion`：菜单、Select、Popover、Tooltip，按 Radix 的 `data-side` 朝触发元素方向展开
- `zke-window-motion`、`zke-rise`、`zke-interacting`：窗口与首屏

新的瞬态浮层必须挂上对应类名，否则它会毫无过渡地出现——瞬间出现不等于快，而是让人看不出它从哪里来。退场动画依赖 Radix 的 `data-state="closed"`，删掉动画就等于删掉退场。

曲线只有两条：入场用 `--ease-lift`，大位移用 `--ease-reveal`。离场一律 `linear`。

本项目**刻意不实现** `prefers-reduced-motion`，原因写在 `theme.css` 末尾。要改这个决定，先读那段注释。

## 弹窗还是页面

这是最容易做错的一件事，规则是硬的：

**用对话框（`Dialog` / `SensitiveActionDialog`）当且仅当：**

- 敏感操作确认——一律走 `SensitiveActionDialog`，它统一呈现操作目标、影响列表和输入名称二次确认，不要另起炉灶；
- 一次性密钥展示，必须打断操作者并要求确认已保存；
- 单字段或两三个字段的短提示（重命名、改显示名、新建一个只有名字的对象）。

**其余一律用页面视图**：`PageHeader` + 替换工作区内容。判据是——需要滚动、字段超过约五个、带列表或目录、需要并排比较、或者操作者可能中途去别处查东西，就不是对话框。参考 `container-service/` 下的 `*View.tsx` 与 `*Form.tsx`。

页面视图的规矩：

- 标题、返回、主操作放在 `PageHeader`，它是不滚动的那一行。读到底部的人恰恰最需要「保存」和「返回」；
- 打开 `PageHeader` 时外壳会隐藏工具栏，工具栏原本表达的作用域（集群 / 命名空间）由 `AppShell` 的 `scope` 以文字接管——多集群产品里不写明集群的对象页是读不了的；
- 表单正文加 `max-w-4xl` 一类的度量约束，不要在最大化窗口里铺成一米长的一行；
- 服务端拒绝的原因显示在主操作附近，不要埋在长列表底部。

对话框的规矩：

- 单字段对话框必须包一层 `<form onSubmit>`，让回车可以提交；取消按钮显式写 `type="button"`；
- 宽度写在 `w-[min(720px,calc(100vw-2rem))]` 这类类名上。`DialogContent` 的宽度是固定的 `w-`，加 `max-w-2xl` 不会生效；
- 请求进行中禁止通过遮罩点击或 Esc 关闭（`SensitiveActionDialog` 已实现）。

## 复用而不是重写

已经存在的东西，不要再手写一份：

| 需求             | 用这个                                                                                                                                                                                                            |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 按钮             | `components/ui/button` 的 `Button`，不要裸 `<button>`（纯文字链接式点击除外，且必须挂 `zke-focus`）                                                                                                               |
| 勾选框 / 开关    | `Checkbox`、`Switch`，**绝不用原生 `<input type="checkbox">`**——原生控件是页面上唯一不跟随主题的元素                                                                                                              |
| 输入             | `Input`、`Textarea`、`NumericInput`                                                                                                                                                                               |
| 下拉             | `Select` 系列，不要原生 `<select>`                                                                                                                                                                                |
| 表格             | `components/common/data-table` 的 `DataTable`，它自带加载骨架、错误态、空态和两种分页                                                                                                                             |
| 加载 / 空 / 错误 | `components/common/state` 的 `LoadingState`、`EmptyState`、`ErrorState`                                                                                                                                           |
| 失败提示         | `components/common/notify` 的 `notifyFailure(动作, error)`，它会带上服务端原因和请求 ID。不要裸 `toast.error(errorMessage(e))`                                                                                    |
| 详情行           | `components/common/detail`                                                                                                                                                                                        |
| 状态标记         | `components/common/status` 的 `StatusBadge`、`RelativeTime`、`IdentifierLabel`                                                                                                                                    |
| 删除入口         | `components/common/delete-action`                                                                                                                                                                                 |
| 密钥展示         | `components/common/secret-reveal` 的 `SecretReveal`。**任何一次性凭证都要走它**——Enrollment Token、安装命令、Pod 访问地址一律遮蔽后再显示，不要用只读 `Input` 明文摆出来；处理方式不同的用 `warning` 覆盖默认提示 |

新增原语放进 `components/ui/`，新增跨应用复合组件放进 `components/common/`。只有一个应用会用到的东西留在该应用目录里。

## 数据、作用域与权限

- 所有服务端读写走 `api/queries/` 下按领域划分的 hook，组件里不直接调 `api.GET`；
- 查询键集中在 `api/query-keys.ts`；
- 变更成功后失效相关查询，不要手动改缓存里的列表；
- 幂等的创建类请求用 `lib/use-submission-key` 生成键，重试不得产生第二个对象；
- 权限判断用 `useSessionContext().permissions.can(permission, scope)`，**只用于决定是否显示入口**；服务端仍然会对每个请求重新授权，前端判断不是安全边界；
- 跨集群操作必须显式携带目标 Cluster，必要时还有 Namespace；section 组件按 `clusterId` 加 `key`，切换目标基础设施时丢弃旧状态；
- 错误必须区分认证失败、无权限、目标不存在、连接失败、执行失败和超时——`api/errors.ts` 已经做了映射，用 `isForbidden` 等判定而不是比对文案；
- 密钥、Token、kubeconfig 不得进入日志、遥测或错误文案。

## 性能

已经生效、不要破坏的机制：

- **应用懒加载**：`apps/registry.ts` 里每个应用都是 `lazy()`，新增应用照办；
- **窗口与内容解耦**：`Window` 是 `memo` 的，应用内容用 `useMemo` 固定引用，拖拽时只动 `transform`。不要把新的 props 直接内联进 `<manifest.entry />`；
- **拖拽走合成层**：移动用 `transform`，位置状态用 `translate`，两者是不同的 CSS 属性，不要合并；
- **窗口不可见时暂停轮询**：`desktop/window-visibility` 提供 `useWindowVisible()`，带 `refetchInterval` 的查询要接受 `{ live }` 并在最小化时停摆。新增轮询查询一律照此办理——每个请求最终都由某个集群的 Agent 执行；
- **列表加载用骨架**：`DataTable` 已经实现，自己搭的列表也应该保持加载态与内容态高度接近，避免数据到达时整页跳动；
- 长列表分页由服务端决定，不要在前端一次性拉全量再切片；
- 避免 `transition-all`；只写真正会变的属性。

## 可访问性与文案

- 图标按钮必须有 `aria-label`；纯装饰图标加 `aria-hidden`；
- 表单控件用 `htmlFor` / `id` 配对；
- 异步失败用 `aria-live` 区域播报，并且该区域要常驻挂载（插入时才出现的 live region 可能完全不会被读出）；
- 界面文案以简体中文为主，保留 Kubernetes 与 ZKE 的英文专名（Pod、Namespace、Agent、Enrollment Token 等）不翻译；
- 中文标题不做大写与字距处理——`uppercase` 对中文没有作用，`tracking` 只会把字撬开；
- 不虚构未实现的能力；规划中的能力用 `availability: { state: "planned" }` 声明，由 `PlannedApp` 如实呈现。

## 注释风格

这个代码库的注释解释**为什么**，不解释做了什么。凡是「看起来可以更简单却没有」的地方——一个刻意的取值、一个绕开的坑、一个被推翻的做法——都应该留下一段说明它为什么是现在这样，以及改回去会发生什么。不要写重述代码的注释，也不要写变更日志式的注释（「2024 年改为……」）。

## 提交前

按改动风险选择最低充分验证，界面改动至少要过：

```bash
pnpm typecheck && pnpm lint && pnpm format:check && pnpm build
```

改了视觉的，还要自查：

- [ ] 深色和浅色主题都看过
- [ ] 没有引入 `theme.css` 之外的颜色、圆角、字号、阴影
- [ ] 新的浮层挂了动效类名，进出都不突兀
- [ ] 键盘可达：Tab 顺序合理，焦点可见，回车能提交表单
- [ ] 窄视口（桌面外壳会切换为堆叠布局）没有横向滚动
- [ ] 加载态不会让内容跳动
- [ ] 敏感操作走 `SensitiveActionDialog`，作用域和影响写清楚了

无法验证的项要在最终回复里如实说明，不能写成「已通过」。
