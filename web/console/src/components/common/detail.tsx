import type { ReactNode } from "react";

import { Card, CardTitle } from "@/components/ui/misc";
import { formatAbsolute } from "@/lib/time";

export function DetailCard({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Card>
      <CardTitle>{title}</CardTitle>
      <dl className="mt-2">{children}</dl>
    </Card>
  );
}

/**
 * One fact in a detail card.
 *
 * A fixed label column with left-aligned values, not `justify-between`: pushing
 * every value to the right edge means a badge, a mono string and a timestamp all
 * start at a different place, and the card loses its vertical rhythm. Hairline
 * separators make it read as a spec sheet.
 */
export function DetailRow({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="border-border/50 grid grid-cols-[6.5rem_1fr] items-baseline gap-3 border-b py-2 text-[13px] last:border-b-0 last:pb-0">
      {/* A label wider than its column wraps inside it. Without this the column
          is a starting point rather than a width, and a long one is drawn over
          the value beside it. */}
      <dt className="text-muted-foreground min-w-0 break-words">{label}</dt>
      <dd className="text-foreground min-w-0">{value}</dd>
    </div>
  );
}

/** The fields every Kubernetes condition carries, whatever object it is on. */
export type DetailCondition = {
  type: string;
  status: string;
  reason: string;
  message: string;
  last_transition_time?: string;
};

/**
 * The conditions of one object.
 *
 * Not a {@link DetailRow} per condition: a condition type is a Kubernetes
 * identifier rather than a label — `PodReadyToStartContainers` is twenty-five
 * characters — and a label column narrow enough for 创建时间 cannot hold one. The
 * type gets the row's width and the status sits at the end of it, which also
 * puts the four objects that have conditions on the same layout.
 */
export function DetailConditions({ conditions }: { conditions: DetailCondition[] }) {
  if (conditions.length === 0) {
    return <div className="text-subtle-foreground py-2 text-[13px]">—</div>;
  }
  return (
    <div className="grid">
      {conditions.map((condition) => {
        const explanation = [condition.reason, condition.message].filter(Boolean).join(" · ");
        return (
          <div
            key={condition.type}
            className="border-border/50 grid gap-0.5 border-b py-2 text-[13px] last:border-b-0 last:pb-0"
          >
            <div className="flex items-baseline justify-between gap-3">
              <span className="text-foreground min-w-0 break-words">{condition.type}</span>
              <span className="text-muted-foreground shrink-0">{condition.status}</span>
            </div>
            {explanation ? (
              <span className="text-muted-foreground text-xs break-words">{explanation}</span>
            ) : null}
            {condition.last_transition_time ? (
              <span className="text-subtle-foreground text-xs">
                {formatAbsolute(condition.last_transition_time)}
              </span>
            ) : null}
          </div>
        );
      })}
    </div>
  );
}

/**
 * Key/value pairs read off a Kubernetes object — labels, annotations, node
 * system info. Rendered as its own block rather than a DetailRow value because
 * there is no useful upper bound on how many there are: a Node carries dozens,
 * and squeezing them into a value column makes every one of them unreadable.
 */
export function DetailKeyValues({ entries }: { entries: Record<string, string> }) {
  const pairs = Object.entries(entries);
  if (pairs.length === 0) {
    return <div className="text-subtle-foreground py-2 text-[13px]">—</div>;
  }
  // Plain `div`s rather than a list: this renders inside a DetailCard's `dl`,
  // where `div` is the one grouping element HTML allows.
  return (
    <div className="grid gap-1 py-1">
      {pairs.map(([key, value]) => (
        <div key={key} className="zke-mono text-muted-foreground text-xs break-all">
          <span className="text-foreground">{key}</span>
          {value ? `=${value}` : ""}
        </div>
      ))}
    </div>
  );
}
