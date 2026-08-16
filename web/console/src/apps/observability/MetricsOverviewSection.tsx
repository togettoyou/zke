import { useEffect, useId, useMemo, useState } from "react";

import { useMetricsQuery } from "@/api/queries/observability";
import type { MetricsQueryIssue, MetricsQueryResult } from "@/api/types";
import { SectionTitle } from "@/apps/AppShell";
import { isForbidden } from "@/api/errors";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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

/**
 * One dimension at a time, CPU and memory side by side.
 *
 * Not every chart at once: each panel is a request that a single-instance
 * Server runs against shared storage, and eight standing queries per open
 * window is a cost paid by every other Cluster in the deployment. It is also
 * how the question is actually asked — an operator looks at Nodes, or at Pods,
 * not at both while comparing.
 *
 * `top` is null where the query ranks nothing: Cluster totals are one series per
 * Cluster, and a Top N over them would drop Clusters from a view whose whole
 * point is covering all of them.
 */
const DIMENSIONS = [
  {
    id: "cluster",
    label: "集群",
    cpu: "cluster_cpu_usage",
    memory: "cluster_memory_usage",
    labels: [] as string[],
    top: null,
    namespace: false,
  },
  {
    id: "node",
    label: "节点",
    cpu: "node_cpu_usage",
    memory: "node_memory_usage",
    labels: ["node"],
    top: 10,
    namespace: false,
  },
  {
    id: "namespace",
    label: "Namespace",
    cpu: "namespace_cpu_usage",
    memory: "namespace_memory_usage",
    labels: ["namespace"],
    top: 10,
    namespace: true,
  },
  {
    id: "pod",
    label: "Pod",
    cpu: "pod_cpu_usage",
    memory: "pod_memory_usage",
    labels: ["namespace", "pod"],
    top: 10,
    namespace: true,
  },
] as const;

type DimensionId = (typeof DIMENSIONS)[number]["id"];

const TOP_CHOICES = [5, 10, 20] as const;

const ALL_CLUSTERS = "__all__";

/** What the Server accepts as a Namespace, checked here so a typo does not
 * become a 400 the operator has to read as an error banner. */
const NAMESPACE_PATTERN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

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

function seriesLabel(
  series: MetricsQueryResult["series"][number],
  dimensions: readonly string[],
): string {
  const suffix = dimensions
    .map((dimension) => series.labels[dimension])
    .filter((value): value is string => Boolean(value))
    .join(" / ");
  const cluster = series.cluster_name || series.cluster_id;
  return suffix ? `${cluster} · ${suffix}` : cluster;
}

/**
 * Why an answer does not describe the whole scope.
 *
 * The three reasons lead somewhere different — install a collector, reduce what
 * a Cluster produces, narrow the request — so they are never collapsed into one
 * "部分数据不可用". Throttling in particular has to name itself: the hole it
 * leaves in the chart is the Server's doing, and an operator reading it as a
 * broken collector would go and reinstall something that works.
 */
function IssueNotice({ issues }: { issues: MetricsQueryIssue[] }) {
  const throttled = issues.filter((issue) => issue.reason === "throttled");
  const silent = issues.filter((issue) => issue.reason === "no_data");
  const truncated = issues.some((issue) => issue.reason === "series_truncated");
  if (throttled.length === 0 && silent.length === 0 && !truncated) {
    return null;
  }
  return (
    <ul className="mt-2 space-y-1">
      {throttled.map((issue) => (
        <li key={`throttled-${issue.cluster_id}`} className="text-danger text-xs">
          {issue.cluster_name || issue.cluster_id}：Server 正在拒绝该集群的上报（
          {issue.detail === "cardinality" ? "序列基数超限" : "样本速率超限"}
          ），图中的空洞由此产生，不是采集组件故障。
        </li>
      ))}
      {truncated ? (
        <li className="text-warning text-xs">
          序列数超过上限，图中只显示了前若干条。请缩小集群范围或减小 Top N。
        </li>
      ) : null}
      {silent.length > 0 ? (
        <li className="text-muted-foreground text-xs">
          范围内 {silent.length} 个集群没有数据：
          {silent
            .slice(0, 3)
            .map((issue) => issue.cluster_name || issue.cluster_id)
            .join("、")}
          {silent.length > 3 ? " 等" : ""}。可能尚未安装采集组件。
        </li>
      ) : null}
    </ul>
  );
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
  namespace,
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
  namespace?: string;
  range: (typeof RANGES)[number];
  window: { start: Date; end: Date };
  top?: number;
  unit: "millicores" | "bytes";
  dimensions: readonly string[];
  live: boolean;
}) {
  const query = useMetricsQuery(
    {
      name: queryName,
      clusterIds,
      start: window.start,
      end: window.end,
      stepSeconds: range.stepSeconds,
      ...(namespace ? { namespace } : {}),
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
          description={
            namespace
              ? `该时间范围内 Namespace ${namespace} 没有采样。请确认名称，或该 Namespace 当前没有运行中的 Pod。`
              : "该时间范围内没有采样。集群可能尚未启用采集，或采集刚刚开始。"
          }
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
        </>
      ) : null}
      {!query.error && query.data ? <IssueNotice issues={query.data.issues} /> : null}
    </section>
  );
}

export function MetricsOverviewSection() {
  const live = useWindowVisible();
  const namespaceInputId = useId();
  const [rangeId, setRangeId] = useState<RangeId>("1h");
  const [dimensionId, setDimensionId] = useState<DimensionId>("cluster");
  const [clusterId, setClusterId] = useState<string>(ALL_CLUSTERS);
  const [top, setTop] = useState<number>(10);
  // Draft and applied are separate so a half-typed Namespace never becomes a
  // request: "kube-sys" is a valid name that simply has no data, and firing it
  // would answer 暂无数据 while the operator is still typing 「kube-system」.
  const [namespaceDraft, setNamespaceDraft] = useState("");
  const [namespace, setNamespace] = useState("");
  const range = RANGES.find((item) => item.id === rangeId) ?? RANGES[0];
  const dimension = DIMENSIONS.find((item) => item.id === dimensionId) ?? DIMENSIONS[0];

  const namespaceInvalid = namespaceDraft !== "" && !NAMESPACE_PATTERN.test(namespaceDraft);

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
  // A Namespace filter only travels with a query that declares it; sending it
  // elsewhere is refused by the Server rather than ignored, so the value is
  // dropped here when the dimension changes under it.
  const appliedNamespace = dimension.namespace ? namespace : "";

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
        description="在「采集接入」中为集群安装采集组件后，指标会在一个采集周期内出现。"
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
          {DIMENSIONS.map((item) => (
            <Button
              key={item.id}
              variant={item.id === dimensionId ? "primary" : "ghost"}
              size="sm"
              onClick={() => setDimensionId(item.id)}
            >
              {item.label}
            </Button>
          ))}
        </div>
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

      {dimension.top !== null ? (
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            if (!namespaceInvalid) {
              setNamespace(namespaceDraft);
            }
          }}
        >
          {dimension.namespace ? (
            <div className="flex flex-col gap-1">
              <label htmlFor={namespaceInputId} className="text-muted-foreground text-xs">
                Namespace（留空表示全部）
              </label>
              <Input
                id={namespaceInputId}
                className="w-[220px]"
                value={namespaceDraft}
                placeholder="kube-system"
                aria-invalid={namespaceInvalid}
                onChange={(event) => setNamespaceDraft(event.target.value)}
                onBlur={() => !namespaceInvalid && setNamespace(namespaceDraft)}
              />
            </div>
          ) : null}
          <div className="flex flex-col gap-1">
            <span className="text-muted-foreground text-xs">Top N</span>
            <Select value={String(top)} onValueChange={(value) => setTop(Number(value))}>
              <SelectTrigger className="w-[110px]" aria-label="Top N">
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
          </div>
          {dimension.namespace ? (
            <Button type="submit" size="sm" variant="ghost" disabled={namespaceInvalid}>
              应用
            </Button>
          ) : null}
          {namespaceInvalid ? (
            <p className="text-danger self-center text-xs">
              Namespace 只能包含小写字母、数字和短横线，且以字母或数字开头和结尾。
            </p>
          ) : null}
        </form>
      ) : null}

      <MetricsPanel
        title={`${dimension.label} CPU 用量`}
        description={dimension.top !== null ? `按当前范围内的用量取前 ${top} 条。` : undefined}
        queryName={dimension.cpu}
        clusterIds={selectedClusters}
        namespace={appliedNamespace}
        range={range}
        window={chartWindow}
        top={dimension.top === null ? undefined : top}
        unit="millicores"
        dimensions={dimension.labels}
        live={live}
      />
      <MetricsPanel
        title={`${dimension.label}内存用量`}
        description={dimension.top !== null ? `按当前范围内的用量取前 ${top} 条。` : undefined}
        queryName={dimension.memory}
        clusterIds={selectedClusters}
        namespace={appliedNamespace}
        range={range}
        window={chartWindow}
        top={dimension.top === null ? undefined : top}
        unit="bytes"
        dimensions={dimension.labels}
        live={live}
      />
    </div>
  );
}
