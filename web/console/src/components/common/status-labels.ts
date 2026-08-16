import type { BadgeProps } from "@/components/ui/badge";

export type Tone = NonNullable<BadgeProps["tone"]>;
type StatusDescriptor = { label: string; tone: Tone };

/**
 * Every server-side status value, said once in the operator's language.
 *
 * It lives beside `StatusBadge` rather than inside it because badges are not
 * the only thing that has to say these words: a filter that offers `Warning`
 * next to a column reading 「警告」 makes the operator translate between two
 * names for one value.
 */
export const STATUS_LABELS: Record<string, Record<string, StatusDescriptor>> = {
  resource: {
    active: { label: "启用", tone: "success" },
    suspended: { label: "已停用", tone: "neutral" },
  },
  cluster: {
    pending: { label: "待接入", tone: "warning" },
    active: { label: "已接入", tone: "success" },
    suspended: { label: "已停用", tone: "neutral" },
  },
  connection: {
    online: { label: "在线", tone: "success" },
    offline: { label: "离线", tone: "neutral" },
  },
  // Node readiness, from the Ready condition the Server already reduced to one
  // of three values.
  node: {
    ready: { label: "就绪", tone: "success" },
    not_ready: { label: "未就绪", tone: "danger" },
    unknown: { label: "未知", tone: "neutral" },
  },
  // Whether a Cluster is collecting metrics, read from that Cluster through its
  // Agent. The three unreadable values stay apart because they are acted on
  // differently: an offline Agent is waited out, a refusal is not.
  metrics_collector: {
    running: { label: "运行中", tone: "success" },
    starting: { label: "启动中", tone: "warning" },
    not_installed: { label: "未安装", tone: "neutral" },
    agent_unavailable: { label: "Agent 未连接", tone: "warning" },
    forbidden: { label: "无权限查看", tone: "neutral" },
    unreadable: { label: "状态不可用", tone: "danger" },
  },
  // `spec.unschedulable`. Named for what it does to the scheduler rather than
  // for kubectl's cordon, which means nothing to an operator reading a table.
  scheduling: {
    schedulable: { label: "可调度", tone: "success" },
    unschedulable: { label: "已停止调度", tone: "warning" },
  },
  // Workload health, from the single value the Server reduces each controller,
  // Job and CronJob to. `scheduled` and `pending` are resting states — a CronJob
  // waiting for its next trigger and a Job whose Pods have not started are both
  // working as configured — so they carry no alarm colour.
  workload: {
    available: { label: "可用", tone: "success" },
    progressing: { label: "更新中", tone: "info" },
    // Kubernetes' own reason, said in Chinese: the rollout ran past
    // `progressDeadlineSeconds`. Not "降级" — the previous ReplicaSet may still
    // be serving everything while the new one fails to start.
    progress_deadline_exceeded: { label: "发布超时", tone: "danger" },
    suspended: { label: "已暂停", tone: "warning" },
    running: { label: "运行中", tone: "info" },
    completed: { label: "已完成", tone: "success" },
    failed: { label: "失败", tone: "danger" },
    scheduled: { label: "等待触发", tone: "neutral" },
    pending: { label: "等待中", tone: "neutral" },
  },
  // Pod phase, verbatim from Kubernetes. `Running` is not the same as healthy —
  // a Pod can run with an unready container — so readiness is shown next to this
  // badge rather than folded into it.
  pod: {
    Pending: { label: "等待中", tone: "warning" },
    Running: { label: "运行中", tone: "success" },
    Succeeded: { label: "已完成", tone: "info" },
    Failed: { label: "失败", tone: "danger" },
    Unknown: { label: "未知", tone: "neutral" },
  },
  // PersistentVolume and PersistentVolumeClaim phases. `Bound` and `Available`
  // are both healthy — one is in use, the other is waiting to be — so only the
  // states that need attention carry an alarm colour.
  volume: {
    Available: { label: "可用", tone: "info" },
    Bound: { label: "已绑定", tone: "success" },
    Pending: { label: "等待中", tone: "warning" },
    Released: { label: "已释放", tone: "warning" },
    Failed: { label: "失败", tone: "danger" },
    Lost: { label: "已丢失", tone: "danger" },
  },
  // Kubernetes Event `type`. Only two values exist, and `Normal` is the great
  // majority of them, so it stays quiet.
  eventType: {
    Normal: { label: "普通", tone: "neutral" },
    Warning: { label: "警告", tone: "warning" },
  },
  // The state of one container inside a Pod.
  containerState: {
    waiting: { label: "等待中", tone: "warning" },
    running: { label: "运行中", tone: "success" },
    terminated: { label: "已终止", tone: "neutral" },
    unknown: { label: "未知", tone: "neutral" },
  },
  // Agent lifecycle and health, per the `agents` table's CHECK constraints.
  // These reached the UI as raw enum values before.
  lifecycle: {
    pending: { label: "待激活", tone: "warning" },
    active: { label: "已激活", tone: "success" },
    revoked: { label: "已撤销", tone: "danger" },
  },
  health: {
    unknown: { label: "未知", tone: "neutral" },
    healthy: { label: "健康", tone: "success" },
    degraded: { label: "降级", tone: "warning" },
  },
  certificate: {
    valid: { label: "证书有效", tone: "success" },
    expiring: { label: "证书即将过期", tone: "warning" },
    expired: { label: "证书已过期", tone: "danger" },
    revoked: { label: "证书已撤销", tone: "danger" },
  },
  user: {
    active: { label: "正常", tone: "success" },
    locked: { label: "已锁定", tone: "warning" },
    disabled: { label: "已禁用", tone: "neutral" },
  },
  enrollment: {
    active: { label: "待使用", tone: "info" },
    consumed: { label: "已使用", tone: "success" },
    expired: { label: "已过期", tone: "neutral" },
    revoked: { label: "已撤销", tone: "danger" },
  },
  auditResult: {
    succeeded: { label: "成功", tone: "success" },
    failed: { label: "失败", tone: "danger" },
    denied: { label: "拒绝", tone: "warning" },
  },
  actor: {
    user: { label: "用户", tone: "primary" },
    agent: { label: "Agent", tone: "info" },
    system: { label: "系统", tone: "neutral" },
  },
};

export type StatusKind = keyof typeof STATUS_LABELS;

/** The word a badge would show, for the controls that filter on the same value. */
export function statusLabel(kind: StatusKind, value: string): string {
  return STATUS_LABELS[kind]?.[value]?.label ?? value;
}
