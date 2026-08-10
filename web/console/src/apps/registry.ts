import { lazy } from "react";
import {
  Activity,
  Boxes,
  Layers,
  Server,
  Settings,
  ShieldCheck,
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
const ContainerServiceApp = lazy(async () => ({
  default: (await import("./container-service/ContainerServiceApp")).ContainerServiceApp,
}));
const TerminalApp = lazy(async () => ({
  default: (await import("./terminal/TerminalApp")).TerminalApp,
}));
const PlannedApp = lazy(async () => ({
  default: (await import("./planned/PlannedApp")).PlannedApp,
}));

/**
 * Desktop application catalogue.
 *
 * Phase 1 applications are backed by real Server APIs. Everything else is
 * declared as `planned` with its roadmap phase: the icon exists so the product
 * shape is visible, but the window states plainly that the capability is not
 * implemented yet. No planned application renders fabricated data.
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
    description: "多集群指标、日志、事件与告警",
    icon: Activity,
    requiredPermissions: [],
    availability: {
      state: "planned",
      phase: 3,
      plannedCapabilities: [
        "VictoriaMetrics 与 VictoriaLogs 集成",
        "多集群指标与日志查询",
        "告警中心",
        "集群标签体系",
      ],
    },
    defaultSize: { width: 880, height: 560 },
    entry: PlannedApp,
  },
];

const MANIFESTS_BY_ID = new Map(APP_MANIFESTS.map((manifest) => [manifest.id, manifest]));

export function findAppManifest(appId: string): AppManifest | undefined {
  return MANIFESTS_BY_ID.get(appId);
}
