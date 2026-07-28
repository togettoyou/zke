import { useEffect, useState } from "react";
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
    revoked: { label: "已退役", tone: "danger" },
  },
  connection: {
    online: { label: "在线", tone: "success" },
    offline: { label: "离线", tone: "neutral" },
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
 * Shortened identifier that copies its full value on click.
 *
 * Identifiers are what an operator carries into a log query or a support
 * thread, so the elided form has to give the whole thing back — the title
 * attribute alone cannot be selected out of a table cell.
 */
export function IdentifierLabel({ value, className }: { value: string; className?: string }) {
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
      aria-label={`复制标识 ${value}`}
      onClick={async (event) => {
        // Identifiers often sit inside clickable rows.
        event.stopPropagation();
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
        } catch {
          setCopied(false);
        }
      }}
      className={cn(
        // `w-fit` and `whitespace-nowrap` for the same reason as the badge: an
        // elided identifier that stretches or wraps is worse than useless.
        "zke-focus zke-mono text-muted-foreground hover:text-foreground hover:bg-surface-muted relative -mx-1 inline-flex w-fit cursor-pointer items-center rounded border border-transparent px-1 text-xs whitespace-nowrap transition-colors",
        className,
      )}
    >
      {value.length > 12 ? `${value.slice(0, 8)}…${value.slice(-4)}` : value}
      {/*
       * No resting copy icon, and the confirmation hangs to the *left*.
       *
       * Carried in flow, a permanently rendered icon padded every identifier's
       * trailing edge by its own width even at `opacity-0` — invisible in a
       * table, obvious the moment the value sits in a right-aligned column and
       * ends short of the plain text above it. Hung outside the box instead, it
       * overflowed: a right-aligned column has no room on its right by
       * definition, which is the whole point of aligning right.
       *
       * So the affordance is the pointer, the hover fill and the title — which
       * already spells out that clicking copies — and the only thing drawn is a
       * check, for a second and a half, in the slack on the left where every
       * layout using this has room to spare.
       */}
      {copied ? (
        <Check
          aria-hidden
          className="text-success absolute top-1/2 right-full mr-1 size-3 -translate-y-1/2"
        />
      ) : null}
    </button>
  );
}

export function CopyButton({
  value,
  label = "复制",
  className,
}: {
  value: string;
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
          await navigator.clipboard.writeText(value);
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
