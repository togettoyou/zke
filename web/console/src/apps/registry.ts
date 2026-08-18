import { lazy } from "react";
import {
  Activity,
  Boxes,
  Layers,
  Server,
  Settings,
  ShieldCheck,
  SlidersHorizontal,
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

/**
 * Desktop application catalogue.
 *
 * Every application here is backed by real Server APIs. A capability that is
 * not implemented yet is declared as `planned` with its roadmap phase and
 * rendered by `planned/PlannedApp`, so the icon can show the product shape
 * without the window fabricating data; there is no such application at the
 * moment, which is why nothing imports it.
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
    requiredPermissions: ["cluster.read"],
    availability: { state: "available" },
    defaultSize: { width: 1_060, height: 680 },
    entry: ContainerServiceApp,
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
    // Wider than the other applications: every chart section lays its panels
    // out two per row, and this is the width that gives both of them a
    // readable plot without the operator resizing the window first.
    defaultSize: { width: 1_180, height: 680 },
    entry: ObservabilityApp,
  },
];

const MANIFESTS_BY_ID = new Map(APP_MANIFESTS.map((manifest) => [manifest.id, manifest]));

export function findAppManifest(appId: string): AppManifest | undefined {
  return MANIFESTS_BY_ID.get(appId);
}
