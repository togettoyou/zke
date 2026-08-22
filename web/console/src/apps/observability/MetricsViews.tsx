import { useState } from "react";

import { useNamespaces } from "@/api/queries/namespaces";
import { SectionToolbarActions } from "@/apps/AppShell";
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
import { ALL_NAMESPACES, useMetricsScope } from "./metrics-scope";

const TOP_CHOICES = [5, 10, 20] as const;

/**
 * How many Namespaces the filter offers, matching the container service.
 *
 * The two applications pick a Namespace out of the same Cluster, so they should
 * offer the same list rather than disagreeing about how much of it exists.
 */
const NAMESPACE_PICKER_LIMIT = 500;

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
  const { clusterId, namespace, setNamespace, top, setTop } = useMetricsScope();

  // Chosen rather than typed, the way the container service chooses one.
  //
  // A text field asked the operator to know a name before they could use the
  // filter, and spelling it almost right produced an empty chart rather than an
  // error — 「kube-sys」 is a perfectly valid Namespace name that simply does not
  // exist. The list is the Cluster's own answer, so a choice from it always
  // names something real.
  //
  // Only fetched for a view that has the filter: the other views would be
  // asking a Cluster for a list nothing on screen can use.
  const namespaces = useNamespaces(view.namespace && clusterId ? clusterId : null, {
    limit: NAMESPACE_PICKER_LIMIT,
  });
  const listed = (namespaces.data?.namespaces ?? []).map((item) => item.name);
  const truncated = Boolean(namespaces.data?.continue_token);
  // The current filter is always an option, even when the list does not contain
  // it — while the list is still loading, or after the Namespace was deleted.
  // The alternative is a select reading 全部 while the queries under it are
  // still filtered, which is a picker that lies about what is on screen.
  const namespaceNames =
    namespace === "" || listed.includes(namespace) ? listed : [namespace, ...listed];
  const selected = namespace === "" ? ALL_NAMESPACES : namespace;

  if (!view.top && !view.namespace) {
    return null;
  }
  return (
    <SectionToolbarActions>
      {view.namespace ? (
        <div className="flex items-center gap-1.5">
          {/* Named as the navigation rail and the container service name it.
              One resource with two names in one Console is one name too many. */}
          <span className="text-muted-foreground text-xs">命名空间</span>
          <Select
            value={selected}
            onValueChange={(value) => setNamespace(value === ALL_NAMESPACES ? "" : value)}
            disabled={!clusterId || namespaces.isLoading}
          >
            <SelectTrigger className="h-8 w-[11rem] text-[13px]" aria-label="命名空间">
              <SelectValue placeholder={namespaces.isLoading ? "加载命名空间…" : "全部"} />
            </SelectTrigger>
            <SelectContent>
              {/* Unlike the container service, "every Namespace" is a real
                  answer here: a Cluster-wide curve is what most of these panels
                  are opened for, and the filter narrows it rather than being a
                  precondition for asking. */}
              <SelectItem value={ALL_NAMESPACES}>全部</SelectItem>
              {namespaceNames.map((name) => (
                <SelectItem key={name} value={name}>
                  {name}
                </SelectItem>
              ))}
              {truncated ? (
                <p className="text-subtle-foreground px-2 py-1.5 text-[11px]">
                  只列出前 {NAMESPACE_PICKER_LIMIT} 个命名空间
                </p>
              ) : null}
            </SelectContent>
          </Select>
        </div>
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
        <div className="grid grid-cols-1 gap-4 @3xl:grid-cols-2">
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
export function ViewedPanels({
  views,
  label,
  initialQuery,
}: {
  views: MetricsViews;
  label: string;
  initialQuery?: string;
}) {
  const [viewId, setViewId] = useState(
    () =>
      views.find((view) =>
        view.panels.some((panel) => panel.queries.some((query) => query.name === initialQuery)),
      )?.id ?? views[0].id,
  );
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
