import { useEffect, useMemo, useState } from "react";

import { useMetricsQuery } from "@/api/queries/observability";
import type { MetricsQueryResult } from "@/api/types";
import { SectionTitle } from "@/apps/AppShell";
import { isForbidden } from "@/api/errors";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useWindowVisible } from "@/desktop/window-visibility";

import { TimeSeriesChart, type ChartSeries } from "./TimeSeriesChart";

/**
 * The ranges an operator actually asks for, with a step chosen so each one
 * lands well inside the Server's point ceiling. A free-form range picker would
 * mostly produce requests the Server refuses.
 */
const RANGES = [
  { id: "1h", label: "最近 1 小时", seconds: 60 * 60, stepSeconds: 60 },
  { id: "6h", label: "最近 6 小时", seconds: 6 * 60 * 60, stepSeconds: 300 },
  { id: "24h", label: "最近 24 小时", seconds: 24 * 60 * 60, stepSeconds: 900 },
] as const;

type RangeId = (typeof RANGES)[number]["id"];

const ALL_CLUSTERS = "__all__";

function formatMillicores(value: number): string {
  if (Math.abs(value) >= 1000) {
    return `${(value / 1000).toFixed(1)} 核`;
  }
  return `${Math.round(value)} m`;
}

function formatBytes(value: number): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let scaled = value;
  let unit = 0;
  while (Math.abs(scaled) >= 1024 && unit < units.length - 1) {
    scaled /= 1024;
    unit += 1;
  }
  return `${scaled.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function seriesLabel(series: MetricsQueryResult["series"][number], dimensions: string[]): string {
  const suffix = dimensions
    .map((dimension) => series.labels[dimension])
    .filter((value): value is string => Boolean(value))
    .join(" / ");
  const cluster = series.cluster_name || series.cluster_id;
  return suffix ? `${cluster} · ${suffix}` : cluster;
}

/**
 * One chart panel.
 *
 * Every state the Server can answer with is distinct here. "No data" and "no
 * permission" and "collection is off" lead to completely different actions, so
 * collapsing them into one empty box would leave the reader to guess which one
 * they are looking at.
 */
function MetricsPanel({
  title,
  description,
  queryName,
  clusterIds,
  range,
  window,
  top,
  unit,
  dimensions,
  live,
}: {
  title: string;
  description?: string;
  queryName: string;
  clusterIds: string[] | undefined;
  range: (typeof RANGES)[number];
  window: { start: Date; end: Date };
  top?: number;
  unit: "millicores" | "bytes";
  dimensions: string[];
  live: boolean;
}) {
  const query = useMetricsQuery(
    {
      name: queryName,
      clusterIds,
      start: window.start,
      end: window.end,
      stepSeconds: range.stepSeconds,
      ...(top ? { top } : {}),
    },
    { live },
  );

  const formatValue = unit === "bytes" ? formatBytes : formatMillicores;
  const chart = useMemo(() => {
    const result = query.data;
    if (!result) {
      return null;
    }
    const timestamps = result.series[0]?.points.map((point) => Number(point[0])) ?? [];
    const series: ChartSeries[] = result.series.map((item) => ({
      id: `${item.cluster_id}:${dimensions.map((dimension) => item.labels[dimension] ?? "").join("/")}`,
      label: seriesLabel(item, dimensions),
      values: item.points.map((point) => (point[1] === null ? null : Number(point[1]))),
    }));
    return { timestamps, series };
  }, [dimensions, query.data]);

  return (
    <section className="border-border bg-surface rounded-panel border p-4">
      <SectionTitle title={title} description={description} />
      {query.isPending ? <LoadingState /> : null}
      {query.error ? (
        <ErrorState
          error={query.error}
          onRetry={isForbidden(query.error) ? undefined : () => void query.refetch()}
        />
      ) : null}
      {!query.isPending && !query.error && chart && chart.series.length === 0 ? (
        <EmptyState
          title="暂无指标数据"
          description="该时间范围内没有采样。集群可能尚未启用采集，或采集刚刚开始。"
        />
      ) : null}
      {!query.error && chart && chart.series.length > 0 ? (
        <>
          <TimeSeriesChart
            timestamps={chart.timestamps}
            series={chart.series}
            formatValue={formatValue}
            ariaLabel={title}
          />
          <ul className="mt-3 flex flex-wrap gap-x-4 gap-y-1">
            {chart.series.map((item) => (
              <li key={item.id} className="text-muted-foreground text-xs">
                {item.label}
              </li>
            ))}
          </ul>
          {query.data?.truncated ? (
            <p className="text-warning mt-2 text-xs">
              序列数超过上限，图中只显示了前若干条。请缩小集群范围或使用 Top N。
            </p>
          ) : null}
        </>
      ) : null}
    </section>
  );
}

export function MetricsOverviewSection() {
  const live = useWindowVisible();
  const [rangeId, setRangeId] = useState<RangeId>("1h");
  const [clusterId, setClusterId] = useState<string>(ALL_CLUSTERS);
  const range = RANGES.find((item) => item.id === rangeId) ?? RANGES[0];

  // One clock for every panel, advanced in an effect rather than read during
  // render: all charts must describe the same window, or two of them side by
  // side are not comparable. The anchor is floored to the step so the window
  // only moves when a new point could exist, which keeps the query key — and
  // therefore the cache — stable in between.
  const [anchor, setAnchor] = useState<number | null>(null);
  useEffect(() => {
    const stepMillis = range.stepSeconds * 1000;
    const align = () => setAnchor(Math.floor(Date.now() / stepMillis) * stepMillis);
    align();
    if (!live) {
      return;
    }
    const timer = window.setInterval(align, stepMillis);
    return () => window.clearInterval(timer);
  }, [live, range.stepSeconds]);
  const chartWindow = useMemo(
    () =>
      anchor === null
        ? null
        : { start: new Date(anchor - range.seconds * 1000), end: new Date(anchor) },
    [anchor, range.seconds],
  );

  // Collection health doubles as the Cluster inventory for this view: it is an
  // instant query over every visible Cluster, so it names exactly the Clusters
  // that have sent anything. A Cluster with no data at all cannot be filtered
  // to here, which is the honest answer — there is nothing to show for it.
  const health = useMetricsQuery({ name: "collection_health" }, { live });
  const clusters = useMemo(() => {
    const seen = new Map<string, string>();
    for (const series of health.data?.series ?? []) {
      seen.set(series.cluster_id, series.cluster_name || series.cluster_id);
    }
    return [...seen.entries()].map(([id, name]) => ({ id, name }));
  }, [health.data]);

  const selectedClusters = clusterId === ALL_CLUSTERS ? undefined : [clusterId];

  if (health.isPending || !chartWindow) {
    return <LoadingState />;
  }
  if (health.error) {
    return (
      <ErrorState
        error={health.error}
        onRetry={isForbidden(health.error) ? undefined : () => void health.refetch()}
      />
    );
  }
  if (clusters.length === 0) {
    return (
      <EmptyState
        title="尚未收到任何集群的指标"
        description="在「采集接入」中为集群生成采集清单并在目标集群应用后，指标会在一个采集周期内出现。"
      />
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <Select value={clusterId} onValueChange={setClusterId}>
          <SelectTrigger className="w-[220px]" aria-label="集群范围">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL_CLUSTERS}>全部集群（{clusters.length}）</SelectItem>
            {clusters.map((cluster) => (
              <SelectItem key={cluster.id} value={cluster.id}>
                {cluster.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="flex gap-1">
          {RANGES.map((item) => (
            <Button
              key={item.id}
              variant={item.id === rangeId ? "primary" : "ghost"}
              size="sm"
              onClick={() => setRangeId(item.id)}
            >
              {item.label}
            </Button>
          ))}
        </div>
      </div>

      <MetricsPanel
        title="集群 CPU 用量"
        queryName="cluster_cpu_usage"
        clusterIds={selectedClusters}
        range={range}
        window={chartWindow}
        unit="millicores"
        dimensions={[]}
        live={live}
      />
      <MetricsPanel
        title="集群内存用量"
        queryName="cluster_memory_usage"
        clusterIds={selectedClusters}
        range={range}
        window={chartWindow}
        unit="bytes"
        dimensions={[]}
        live={live}
      />
      <MetricsPanel
        title="节点 CPU 用量 Top 10"
        description="按当前范围内的用量取前 10 个节点。"
        queryName="node_cpu_usage"
        clusterIds={selectedClusters}
        range={range}
        window={chartWindow}
        top={10}
        unit="millicores"
        dimensions={["node"]}
        live={live}
      />
    </div>
  );
}
