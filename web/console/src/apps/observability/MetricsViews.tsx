import { useId, useState } from "react";

import { SectionToolbarActions } from "@/apps/AppShell";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/cn";

import { ChartPanel } from "./ChartPanel";
import type { MetricsView, MetricsViews } from "./metrics-catalog";
import { NAMESPACE_PATTERN, useMetricsScope } from "./metrics-scope";

const TOP_CHOICES = [5, 10, 20] as const;

/**
 * A row of choices that swaps what is drawn below it.
 *
 * Segments rather than a select: there are never more than a handful, and the
 * alternatives being visible is the point — an operator who has not used this
 * view before learns what it can answer by reading the row.
 */
export function SegmentedTabs<T extends { id: string; label: string }>({
  items,
  activeId,
  onSelect,
  label,
}: {
  items: readonly T[];
  activeId: string;
  onSelect: (id: string) => void;
  label: string;
}) {
  return (
    <div
      role="group"
      aria-label={label}
      className="border-border bg-surface-muted rounded-panel inline-flex flex-wrap items-center gap-1 border p-1"
    >
      {items.map((item) => {
        const active = item.id === activeId;
        return (
          <button
            key={item.id}
            type="button"
            aria-pressed={active}
            onClick={() => onSelect(item.id)}
            className={cn(
              "zke-focus rounded-control border border-transparent px-3 py-1 text-[13px] font-medium transition-colors",
              active
                ? "border-border bg-surface text-foreground shadow-e1"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {item.label}
          </button>
        );
      })}
    </div>
  );
}

/**
 * The controls a view brings with it.
 *
 * Top N and the Namespace filter belong to the view rather than to the
 * application: a Cluster total has no Top N, and sending a Namespace to a query
 * that does not declare one is refused by the Server rather than ignored. They
 * are portaled into the shell's toolbar so every filter still reads as one row.
 */
function ViewFilters({ view }: { view: MetricsView }) {
  const { namespace, setNamespace, top, setTop } = useMetricsScope();
  const namespaceInputId = useId();
  // Draft and applied are separate so a half-typed Namespace never becomes a
  // request: "kube-sys" is a valid name that simply has no data, and firing it
  // would answer 暂无数据 while the operator is still typing 「kube-system」.
  const [draft, setDraft] = useState(namespace);
  const invalid = draft !== "" && !NAMESPACE_PATTERN.test(draft);

  if (!view.top && !view.namespace) {
    return null;
  }
  return (
    <SectionToolbarActions>
      {view.namespace ? (
        <form
          className="flex items-center gap-1.5"
          onSubmit={(event) => {
            event.preventDefault();
            if (!invalid) {
              setNamespace(draft);
            }
          }}
        >
          <label htmlFor={namespaceInputId} className="text-muted-foreground text-xs">
            Namespace
          </label>
          <Input
            id={namespaceInputId}
            className="h-8 w-[11rem] text-[13px]"
            value={draft}
            placeholder="全部"
            aria-invalid={invalid}
            aria-describedby={invalid ? `${namespaceInputId}-error` : undefined}
            onChange={(event) => setDraft(event.target.value)}
            onBlur={() => !invalid && setNamespace(draft)}
          />
          {invalid ? (
            <span id={`${namespaceInputId}-error`} className="text-danger text-xs">
              只能包含小写字母、数字和短横线
            </span>
          ) : null}
        </form>
      ) : null}
      {view.top ? (
        <Select value={String(top)} onValueChange={(value) => setTop(Number(value))}>
          <SelectTrigger className="h-8 w-[7.5rem] text-[13px]" aria-label="Top N">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {TOP_CHOICES.map((choice) => (
              <SelectItem key={choice} value={String(choice)}>
                前 {choice} 条
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : null}
    </SectionToolbarActions>
  );
}

/**
 * One view's panels.
 *
 * Two per row once the window is wide enough to give each chart a readable
 * width, one otherwise — measured against the panel's own container rather than
 * the viewport, because these live in a window an operator can drag to any size
 * on a screen of any size.
 */
export function ViewPanels({ view }: { view: MetricsView }) {
  const { namespace, top } = useMetricsScope();
  return (
    <>
      <ViewFilters view={view} />
      {view.description ? (
        <p className="text-muted-foreground max-w-3xl text-xs leading-relaxed">
          {view.description}
        </p>
      ) : null}
      <div className="@container">
        <div className="grid grid-cols-1 gap-4 @4xl:grid-cols-2">
          {view.panels.map((panel) => (
            <ChartPanel
              key={panel.id}
              panel={panel}
              top={view.top ? top : undefined}
              namespace={view.namespace ? namespace : undefined}
            />
          ))}
        </div>
      </div>
    </>
  );
}

/** A set of views with the row that switches between them. */
export function ViewedPanels({ views, label }: { views: MetricsViews; label: string }) {
  const [viewId, setViewId] = useState(views[0].id);
  const view = views.find((item) => item.id === viewId) ?? views[0];
  return (
    <div className="flex flex-col gap-4">
      {/* The strip sits in a row of its own: it is inline, and a bare flex
          column child would stretch it across the whole work area. */}
      {views.length > 1 ? (
        <div className="flex flex-wrap items-center gap-2">
          <SegmentedTabs items={views} activeId={view.id} onSelect={setViewId} label={label} />
        </div>
      ) : null}
      <ViewPanels view={view} />
    </div>
  );
}
