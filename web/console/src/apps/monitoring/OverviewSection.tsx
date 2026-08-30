import { useMemo, useState } from "react";

import { useMetricsQuery } from "@/api/queries/observability";
import { isForbidden } from "@/api/errors";
import { ErrorState } from "@/components/common/state";
import { Skeleton } from "@/components/ui/misc";
import { cn } from "@/lib/cn";

import { ChartPanel, IssueNotice } from "./ChartPanel";
import { SegmentedTabs, ViewPanels } from "./MetricsViews";
import { KUBERNETES_VIEWS, OVERVIEW_PANELS } from "./metrics-catalog";
import { useMetricsScope } from "./metrics-scope";

const SUMMARY_VIEW = { id: "summary", label: "整体" } as const;
const OVERVIEW_VIEWS = [SUMMARY_VIEW, ...KUBERNETES_VIEWS] as const;

/**
 * The Cluster landing view and the Kubernetes object health views behind it.
 *
 * Kubernetes 资源 used to be a peer of 集群总览 in the application rail, even
 * though its Pod and restart charts overlapped the landing screen and the label
 * described an implementation domain rather than an operator's task. Keeping
 * the concrete resource choices inside the overview makes this one place for
 * answering whether the Cluster is healthy without loading every panel at once.
 */
export function OverviewSection({ initialQuery }: { initialQuery?: string }) {
  const [viewId, setViewId] = useState(
    () =>
      KUBERNETES_VIEWS.find((view) =>
        view.panels.some((panel) => panel.queries.some((query) => query.name === initialQuery)),
      )?.id ?? SUMMARY_VIEW.id,
  );
  const resourceView = KUBERNETES_VIEWS.find((view) => view.id === viewId);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <SegmentedTabs
          items={OVERVIEW_VIEWS}
          activeId={resourceView?.id ?? SUMMARY_VIEW.id}
          onSelect={setViewId}
          label="集群总览视角"
        />
      </div>
      {resourceView ? <ViewPanels key={resourceView.id} view={resourceView} /> : <SummaryPanels />}
    </div>
  );
}

/**
 * The headline row and the curves that explain a headline count that reads badly.
 */
function SummaryPanels() {
  const { clusterId, windowKey, readWindow, live } = useMetricsScope();
  const inventory = useMetricsQuery(
    {
      name: "cluster_inventory",
      clusterId,
      // Anchored to the same window as the charts, so the row and the curves
      // under it describe the same moment. An instant query reads only the end
      // of it; the Server ignores the rest.
      windowKey,
      window: readWindow,
    },
    { live, intervalMs: 0 },
  );

  const counts = useMemo(() => {
    const totals = new Map<string, number>();
    for (const series of inventory.data?.series ?? []) {
      const resource = series.labels.resource;
      const point = series.points[series.points.length - 1];
      const value = point && point[1] !== null ? Number(point[1]) : null;
      if (!resource || value === null) {
        continue;
      }
      totals.set(resource, (totals.get(resource) ?? 0) + value);
    }
    return totals;
  }, [inventory.data]);

  const nodes = counts.get("node") ?? 0;
  const nodesReady = counts.get("node_ready") ?? 0;
  const workloads =
    (counts.get("deployment") ?? 0) +
    (counts.get("statefulset") ?? 0) +
    (counts.get("daemonset") ?? 0);
  const pending = counts.get("pod_pending") ?? 0;
  const failed = counts.get("pod_failed") ?? 0;

  return (
    <>
      {inventory.error ? (
        <ErrorState
          error={inventory.error}
          onRetry={isForbidden(inventory.error) ? undefined : () => void inventory.refetch()}
        />
      ) : (
        <div className="@container">
          {/* Five tiles, not six: the Cluster count that used to lead this row
              answered a question this application no longer asks. The target
              Cluster is named in the toolbar, and counting it as "1" beside
              real numbers reads as a measurement rather than as a heading. */}
          <div className="grid grid-cols-2 gap-3 @2xl:grid-cols-3 @5xl:grid-cols-5">
            <StatTile
              label="节点"
              value={nodes}
              note={nodes === nodesReady ? "全部就绪" : `${nodes - nodesReady} 个未就绪`}
              tone={nodes === nodesReady ? "neutral" : "warning"}
              loading={inventory.isPending}
            />
            <StatTile
              label="运行中 Pod"
              value={counts.get("pod_running") ?? 0}
              loading={inventory.isPending}
            />
            <StatTile
              label="等待中 Pod"
              value={pending}
              note={pending > 0 ? "尚未调度或未启动" : undefined}
              tone={pending > 0 ? "warning" : "neutral"}
              loading={inventory.isPending}
            />
            <StatTile
              label="失败 Pod"
              value={failed}
              tone={failed > 0 ? "danger" : "neutral"}
              loading={inventory.isPending}
            />
            <StatTile
              label="工作负载"
              value={workloads}
              note="控制器对象"
              loading={inventory.isPending}
            />
          </div>
        </div>
      )}
      {inventory.data ? <IssueNotice issues={inventory.data.issues} /> : null}

      <div className="@container">
        <div className="grid grid-cols-1 gap-4 @3xl:grid-cols-2">
          {OVERVIEW_PANELS.map((panel) => (
            <ChartPanel key={panel.id} panel={panel} />
          ))}
        </div>
      </div>
    </>
  );
}

const TONES = {
  neutral: "text-foreground",
  warning: "text-warning",
  danger: "text-danger",
} as const;

/**
 * One number, with the word that makes it mean something.
 *
 * The tone is never the only signal: a tile that has gone warning or danger
 * also says what the number is in its note, because a reader who cannot see the
 * difference between two greys still has to be able to read the row.
 */
function StatTile({
  label,
  value,
  note,
  tone = "neutral",
  loading,
}: {
  label: string;
  value: number;
  note?: string;
  tone?: keyof typeof TONES;
  loading?: boolean;
}) {
  return (
    <div className="border-border bg-surface rounded-panel flex flex-col gap-0.5 border p-3">
      <span className="text-muted-foreground text-xs">{label}</span>
      {loading ? (
        <Skeleton className="my-1 h-6 w-16" />
      ) : (
        <span className={cn("zke-tnum text-[22px] leading-tight font-semibold", TONES[tone])}>
          {Math.round(value).toLocaleString("zh-CN")}
        </span>
      )}
      {note ? <span className="text-subtle-foreground text-[11px]">{note}</span> : null}
    </div>
  );
}
