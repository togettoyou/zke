import { lazy } from "react";
import {
  Activity,
  Boxes,
  Bot,
  Cpu,
  Layers,
  Network,
  Server,
  Settings,
  ShieldCheck,
  Sparkles,
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
    requiredPermissions: ["cluster.read", "cluster.enrollment.read", "cluster.enrollment.create"],
    availability: { state: "available" },
    defaultSize: { width: 1_020, height: 660 },
    entry: ClusterAccessApp,
  },
  {
    id: "resources",
    title: "组织与资源",
    description: "管理 Tenant、Project 及其生命周期",
    icon: Layers,
    requiredPermissions: ["tenant.read", "project.read"],
    availability: { state: "available" },
    defaultSize: { width: 900, height: 600 },
    entry: ResourcesApp,
  },
  {
    id: "access-audit",
    title: "访问与审计",
    description: "用户、RoleBinding 与审计事件",
    icon: ShieldCheck,
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
    requiredPermissions: [],
    availability: { state: "available" },
    defaultSize: { width: 780, height: 600 },
    entry: SettingsApp,
  },
  {
    id: "container-service",
    title: "容器服务",
    description: "节点、Namespace、工作负载与 Pod 管理",
    icon: Boxes,
    requiredPermissions: [],
    availability: {
      state: "planned",
      phase: 2,
      plannedCapabilities: [
        "集群选择与节点管理",
        "Namespace 与工作负载管理",
        "Pod 列表、日志与 Web Terminal",
        "YAML 管理与 Kubernetes Event",
      ],
    },
    defaultSize: { width: 880, height: 560 },
    entry: PlannedApp,
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
  {
    id: "job-platform",
    title: "作业平台",
    description: "批处理、训练与 GPU 作业",
    icon: Network,
    requiredPermissions: [],
    availability: {
      state: "planned",
      phase: 4,
      plannedCapabilities: [
        "作业管理与作业队列",
        "GPU 作业与分布式训练",
        "配额与优先级",
        "作业日志与状态跟踪",
      ],
    },
    defaultSize: { width: 880, height: 560 },
    entry: PlannedApp,
  },
  {
    id: "compute-platform",
    title: "算力平台",
    description: "多集群算力总览、算力池与配额",
    icon: Cpu,
    requiredPermissions: [],
    availability: {
      state: "planned",
      phase: 5,
      plannedCapabilities: [
        "多集群算力总览与 GPU 资源管理",
        "算力池",
        "租户与项目配额",
        "手动选择目标集群",
      ],
    },
    defaultSize: { width: 880, height: 560 },
    entry: PlannedApp,
  },
  {
    id: "model-service",
    title: "模型服务",
    description: "模型部署、推理实例与 OpenAI-compatible API",
    icon: Sparkles,
    requiredPermissions: [],
    availability: {
      state: "planned",
      phase: 5,
      plannedCapabilities: [
        "模型服务部署与推理实例管理",
        "OpenAI-compatible API",
        "API Key 管理",
        "调用统计",
      ],
    },
    defaultSize: { width: 880, height: 560 },
    entry: PlannedApp,
  },
  {
    id: "copilot",
    title: "ZKE Copilot",
    description: "在明确作用域内分析问题并给出受控建议",
    icon: Bot,
    requiredPermissions: [],
    availability: {
      state: "planned",
      phase: 7,
      plannedCapabilities: [
        "跨集群问题分析与根因判断",
        "证据与修复建议",
        "受控操作确认与执行",
        "完整审计记录",
      ],
    },
    defaultSize: { width: 820, height: 560 },
    entry: PlannedApp,
  },
];

const MANIFESTS_BY_ID = new Map(APP_MANIFESTS.map((manifest) => [manifest.id, manifest]));

export function findAppManifest(appId: string): AppManifest | undefined {
  return MANIFESTS_BY_ID.get(appId);
}
