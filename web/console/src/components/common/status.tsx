import { useEffect, useState, type ReactNode } from "react";
import { Check, Copy } from "lucide-react";

import { Badge, StatusDot, type BadgeProps } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";
import { formatAbsolute, formatRelative } from "@/lib/time";

type Tone = NonNullable<BadgeProps["tone"]>;
type StatusDescriptor = { label: string; tone: Tone };

const STATUS_LABELS: Record<string, Record<string, StatusDescriptor>> = {
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

export function StatusBadge({
  kind,
  value,
  className,
}: {
  kind: StatusKind;
  value: string;
  className?: string;
}) {
  const descriptor = STATUS_LABELS[kind]?.[value] ?? { label: value, tone: "neutral" as Tone };
  return (
    <Badge tone={descriptor.tone} className={className}>
      <StatusDot tone={descriptor.tone} />
      {descriptor.label}
    </Badge>
  );
}

/** Absolute time in the title, relative time in the body, refreshed each minute. */
export function RelativeTime({
  value,
  className,
}: {
  value: string | null | undefined;
  className?: string;
}) {
  const [, setTick] = useState(0);

  useEffect(() => {
    const timer = setInterval(() => setTick((current) => current + 1), 60_000);
    return () => clearInterval(timer);
  }, []);

  if (!value) {
    return <span className={cn("text-subtle-foreground", className)}>—</span>;
  }

  return (
    <time dateTime={value} title={formatAbsolute(value)} className={className}>
      {formatRelative(value)}
    </time>
  );
}

export function AbsoluteTime({
  value,
  className,
}: {
  value: string | null | undefined;
  className?: string;
}) {
  if (!value) {
    return <span className={cn("text-subtle-foreground", className)}>—</span>;
  }
  return (
    <time dateTime={value} className={cn("zke-mono text-xs", className)}>
      {formatAbsolute(value)}
    </time>
  );
}

/**
 * A value that copies itself on click.
 *
 * An address, a port or an identifier is read here and used somewhere else —
 * a shell, a log query, a support thread — and selecting one out of a table
 * cell with the pointer is both fiddly and easy to get wrong by a character.
 *
 * What lands on the clipboard is named in the title before the click rather
 * than discovered after it, which is what lets a caller copy something the
 * screen shows in pieces, such as an address and the port beside it.
 */
export function CopyableValue({
  value,
  children,
  label,
  className,
}: {
  /** What lands on the clipboard. */
  value: string;
  /** What is shown, when that differs from the value itself. */
  children?: ReactNode;
  /** Accessible name, when "复制 <value>" is not specific enough. */
  label?: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) {
      return;
    }
    const timer = setTimeout(() => setCopied(false), 1_500);
    return () => clearTimeout(timer);
  }, [copied]);

  return (
    <button
      type="button"
      title={`${value}（点击复制）`}
      aria-label={label ?? `复制 ${value}`}
      onClick={async (event) => {
        // These often sit inside clickable rows.
        event.stopPropagation();
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
        } catch {
          setCopied(false);
        }
      }}
      className={cn(
        // `w-fit` and `whitespace-nowrap` for the same reason as the badge: a
        // value that stretches or wraps to fit its column is worse than useless.
        //
        // The hover state carries a border as well as a fill: `--surface-muted`
        // against the card it usually sits on is a couple of steps of grey, too
        // little to be read as "this responds to a click" on a page that is
        // otherwise full of plain text.
        "zke-focus group/copy hover:bg-surface-muted hover:border-border -mx-1 inline-flex w-fit cursor-pointer items-center rounded border border-transparent px-1 whitespace-nowrap transition-colors",
        className,
      )}
    >
      {children ?? value}
      {/*
       * The copy icon appears on hover and on keyboard focus, and the
       * confirmation is carried in flow.
       *
       * Reserved permanently — an `opacity-0` icon holding its slot — it padded
       * every value's trailing edge by its own width, which is invisible
       * in a table and obvious the moment the value sits in a right-aligned
       * column and ends short of the plain text above it. Hung outside the box
       * instead, it landed on whatever the layout put there: to the right, on
       * the edge of a right-aligned column that has no room there by
       * definition; to the left, on the username the identifier follows in the
       * users and role-binding tables.
       *
       * Shown only while hovered, focused or copied, it can neither pad the
       * resting state nor cover a neighbour. The cost is that the button is
       * wider for as long as it is shown, which moves nothing but its own
       * trailing edge — and by then the pointer is already on it.
       */}
      {copied ? (
        <Check aria-hidden className="text-success ml-1 size-3 shrink-0" />
      ) : (
        <Copy
          aria-hidden
          className="ml-1 hidden size-3 shrink-0 opacity-70 group-hover/copy:block group-focus-visible/copy:block"
        />
      )}
    </button>
  );
}

/**
 * Addresses, each copying itself on click.
 *
 * An address is read on one screen and used on another, so every one of them is
 * copyable wherever it is shown — a Service's ClusterIP, an Ingress's assigned
 * address, a Pod IP, a Node address.
 *
 * `None` is how Kubernetes reports that a headless Service has no address at
 * all, so it stays plain text: offering to copy it would be offering to copy
 * the word rather than an address. Wrapping is allowed here — an IPv6 address
 * is wider than the column it sits in, and a clipped one cannot be read.
 */
export function AddressValues({
  values,
  empty = "—",
  className,
}: {
  values: string[];
  /** Shown when there is no address, e.g. "尚未分配". */
  empty?: string;
  className?: string;
}) {
  const shown = values.filter(Boolean);
  if (shown.length === 0) {
    return <span className={cn("zke-mono text-xs", className)}>{empty}</span>;
  }
  return (
    <span className={cn("zke-mono inline-flex flex-wrap items-center gap-x-1 text-xs", className)}>
      {shown.map((value, index) =>
        value === "None" ? (
          <span key={`${value}/${index}`}>None</span>
        ) : (
          <CopyableValue
            key={`${value}/${index}`}
            value={value}
            className={cn("zke-mono text-xs break-all whitespace-normal", className)}
          >
            {value}
          </CopyableValue>
        ),
      )}
    </span>
  );
}

/**
 * Shortened identifier that copies its full value on click.
 *
 * Identifiers are what an operator carries into a log query or a support
 * thread, so the elided form has to give the whole thing back — the title
 * attribute alone cannot be selected out of a table cell.
 */
export function IdentifierLabel({ value, className }: { value: string; className?: string }) {
  return (
    <CopyableValue
      value={value}
      label={`复制标识 ${value}`}
      className={cn("zke-mono text-muted-foreground hover:text-foreground text-xs", className)}
    >
      {value.length > 12 ? `${value.slice(0, 8)}…${value.slice(-4)}` : value}
    </CopyableValue>
  );
}

export function CopyButton({
  value,
  label = "复制",
  className,
}: {
  /**
   * A function is called on click, so a caller whose value is expensive to
   * materialise — a whole log buffer — does not build it on every render.
   */
  value: string | (() => string);
  label?: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) {
      return;
    }
    const timer = setTimeout(() => setCopied(false), 2_000);
    return () => clearTimeout(timer);
  }, [copied]);

  return (
    <Button
      type="button"
      size="sm"
      variant="secondary"
      className={className}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(typeof value === "function" ? value() : value);
          setCopied(true);
        } catch {
          setCopied(false);
        }
      }}
    >
      {copied ? <Check /> : <Copy />}
      {copied ? "已复制" : label}
    </Button>
  );
}
