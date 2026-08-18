import { useMemo } from "react";

import { useMetricsQuery } from "@/api/queries/observability";
import { isForbidden } from "@/api/errors";
import { ErrorState } from "@/components/common/state";
import { Skeleton } from "@/components/ui/misc";
import { cn } from "@/lib/cn";

import { ChartPanel, IssueNotice } from "./ChartPanel";
import { OVERVIEW_PANELS } from "./metrics-catalog";
import { useMetricsScope } from "./metrics-scope";

/**
 * The headline row and the four curves behind it.
 *
 * A landing screen answers one question — is anything wrong right now — and
 * then says where to look. So the tiles are counts of things that are either
 * healthy or not, and the charts under them are the four series whose shape
 * explains a tile that reads badly: two utilisations, what the Pods are doing,
 * and whether containers are restarting.
 */
export function OverviewSection() {
  const { clusterIds, clusters, window: chartWindow, live } = useMetricsScope();
  const inventory = useMetricsQuery(
    {
      name: "cluster_inventory",
      clusterIds,
      // Anchored to the same window end as the charts, so the row and the
      // curves under it describe the same moment.
      end: new Date(chartWindow.endMs),
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
    <div className="flex flex-col gap-4">
      {inventory.error ? (
        <ErrorState
          error={inventory.error}
          onRetry={isForbidden(inventory.error) ? undefined : () => void inventory.refetch()}
        />
      ) : (
        <div className="@container">
          <div className="grid grid-cols-2 gap-3 @2xl:grid-cols-3 @5xl:grid-cols-6">
            <StatTile
              label="集群"
              value={clusters.length}
              note="正在上报指标"
              loading={inventory.isPending}
            />
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
    </div>
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
