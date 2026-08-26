import { lazy } from "react";
import {
  Activity,
  Boxes,
  Layers,
  Server,
  Settings,
  ShieldCheck,
  ShipWheel,
  SlidersHorizontal,
  Sparkles,
  SquareTerminal,
} from "lucide-react";

import type { AppManifest } from "./types";

const ClusterAccessApp = lazy(async () => ({
  default: (await import("./cluster-access/ClusterAccessApp")).ClusterAccessApp,
}));
const ResourcesApp = lazy(async () => ({
  default: (await import("./resources/ResourcesApp")).ResourcesApp,
}));
const AccessAuditApp = lazy(async () => ({
  default: (await import("./access-audit/AccessAuditApp")).AccessAuditApp,
}));
const SettingsApp = lazy(async () => ({
  default: (await import("./settings/SettingsApp")).SettingsApp,
}));
const PlatformApp = lazy(async () => ({
  default: (await import("./platform/PlatformApp")).PlatformApp,
}));
const ContainerServiceApp = lazy(async () => ({
  default: (await import("./container-service/ContainerServiceApp")).ContainerServiceApp,
}));
const TerminalApp = lazy(async () => ({
  default: (await import("./terminal/TerminalApp")).TerminalApp,
}));
const ObservabilityApp = lazy(async () => ({
  default: (await import("./observability/ObservabilityApp")).ObservabilityApp,
}));
const AIOpsApp = lazy(async () => ({
  default: (await import("./aiops/AIOpsApp")).AIOpsApp,
}));
const PlannedApp = lazy(async () => ({
  default: (await import("./planned/PlannedApp")).PlannedApp,
}));

/**
 * Desktop application catalogue.
 *
 * Every application here is backed by real Server APIs. A capability that is
 * not implemented yet is declared as `planned` with its roadmap phase and
 * rendered by `planned/PlannedApp`, so the icon can show the product shape
 * without the window fabricating data.
 */
export const APP_MANIFESTS: AppManifest[] = [
  {
    id: "cluster-access",
    title: "集群接入管理",
    description: "接入、查看和管理 Kubernetes 集群与其连接状态",
    icon: Server,
    accent: "cyan",
    requiredPermissions: ["cluster.read", "cluster.enrollment.read", "cluster.enrollment.create"],
    availability: { state: "available" },
    defaultSize: { width: 1_020, height: 660 },
    entry: ClusterAccessApp,
  },
  {
    id: "resources",
    title: "组织与资源",
    description: "管理租户、项目及其生命周期",
    icon: Layers,
    accent: "violet",
    requiredPermissions: ["tenant.read", "project.read"],
    availability: { state: "available" },
    defaultSize: { width: 900, height: 600 },
    entry: ResourcesApp,
  },
  {
    id: "access-audit",
    title: "访问与审计",
    description: "用户、角色绑定与审计事件",
    icon: ShieldCheck,
    accent: "emerald",
    requiredPermissions: ["user.read", "rbac.read", "audit.read"],
    availability: { state: "available" },
    defaultSize: { width: 1_060, height: 640 },
    entry: AccessAuditApp,
  },
  {
    id: "platform",
    title: "平台配置",
    description: "Agent 接入端点、镜像与集群终端的部署级默认值",
    icon: SlidersHorizontal,
    accent: "rose",
    // Guarded by role rather than permission: the Server puts every /platform
    // route behind RequireGlobalAdministrator.
    requiredPermissions: [],
    requiresGlobalAdmin: true,
    availability: { state: "available" },
    defaultSize: { width: 860, height: 640 },
    entry: PlatformApp,
  },
  {
    id: "settings",
    title: "系统设置",
    description: "当前身份、权限能力、密码与桌面偏好",
    icon: Settings,
    accent: "amber",
    requiredPermissions: [],
    availability: { state: "available" },
    defaultSize: { width: 780, height: 600 },
    entry: SettingsApp,
  },
  {
    id: "container-service",
    title: "容器服务",
    description: "在所选集群中管理节点、命名空间与工作负载",
    icon: Boxes,
    accent: "blue",
    requiredPermissions: ["cluster.read"],
    availability: { state: "available" },
    defaultSize: { width: 1_060, height: 680 },
    entry: ContainerServiceApp,
  },
  {
    id: "helm",
    title: "Helm 应用",
    description: "以 Helm 管理集群中的应用：仓库、安装、升级、回滚与卸载",
    // Helm's own mark is a ship's helm, so the wheel says which tool this is in
    // a way a generic package icon does not.
    icon: ShipWheel,
    // No accent, as every planned application: an unlit tile on a launcher of
    // lit ones says "not yet" before the caption under it does.
    //
    // Declared with the permission the read-only Helm view in 容器服务 already
    // requires. A Release is a Secret, and that will not change when this
    // application gains its writes — those will need permissions of their own,
    // decided when the design is. Planned applications are shown regardless of
    // this list, so nothing turns on it today.
    requiredPermissions: ["cluster.secret.read"],
    availability: {
      state: "planned",
      phase: 2,
      plannedCapabilities: [
        "Chart 仓库接入与 Chart 检索",
        "Release 安装与升级，提交前预览渲染差异",
        "Release 回滚到保留的历史修订",
        "Release 卸载，含保留历史与清理资源的选择",
        "values 编辑与来源追溯",
      ],
    },
    defaultSize: { width: 1_060, height: 680 },
    entry: PlannedApp,
  },
  {
    id: "terminal",
    title: "终端",
    description: "在所选集群中使用按当前角色授权的 kubectl",
    icon: SquareTerminal,
    accent: "slate",
    requiredPermissions: ["cluster.terminal.exec"],
    availability: { state: "available" },
    defaultSize: { width: 1_060, height: 680 },
    entry: TerminalApp,
  },
  {
    id: "observability",
    title: "可观测性",
    description: "跨集群指标与采集接入",
    icon: Activity,
    accent: "steel",
    // Reading metrics is the application's purpose; installing collection is a
    // second permission checked inside it. Gating the icon on the read
    // permission alone keeps an operator who may only look from finding no
    // application at all.
    requiredPermissions: ["cluster.metrics.read"],
    availability: { state: "available" },
    // The largest default in the catalogue, and deliberately larger than any
    // screen it is likely to open on: `cascadeRect` clamps a window to the
    // desktop, so asking for this much means "as much as there is" everywhere
    // below it and stays a fixed size above.
    //
    // Every chart section lays its panels out two per row and stacks several
    // sections down the page, so this application is the one where a default
    // that merely fits leaves the operator resizing before they can read
    // anything: the rail, the toolbar and the panel padding all come off the
    // top before the plots get their share, and a plot too short to separate
    // two series is a picture of nothing.
    defaultSize: { width: 1_200, height: 900 },
    entry: ObservabilityApp,
  },
  {
    id: "aiops",
    title: "AIOps",
    description: "以目标集群为工作区的运维 Agent：自主查证、可复核轨迹",
    icon: Sparkles,
    accent: "fuchsia",
    requiredPermissions: ["ai.run"],
    // The one application the deployment can switch off: without a model
    // endpoint there is nothing behind the icon, so it is left off the desktop
    // rather than opened onto a workspace whose every turn would be refused.
    requiresPlatformFeature: "aiops",
    availability: { state: "available" },
    // A conversation is a column, not a spreadsheet: the extra width a wide
    // window buys is margin, while the extra height is more of the exchange on
    // screen at once. Same width as the container service, taller.
    defaultSize: { width: 1_060, height: 760 },
    entry: AIOpsApp,
  },
];

const MANIFESTS_BY_ID = new Map(APP_MANIFESTS.map((manifest) => [manifest.id, manifest]));

export function findAppManifest(appId: string): AppManifest | undefined {
  return MANIFESTS_BY_ID.get(appId);
}
