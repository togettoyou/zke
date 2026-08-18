import { useMemo, useState } from "react";

import { useMetricsQueries } from "@/api/queries/observability";
import type { MetricsQueryIssue, MetricsQuerySeries } from "@/api/types";
import { isForbidden } from "@/api/errors";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { cn } from "@/lib/cn";

import { TimeSeriesChart, type ChartSeries } from "./TimeSeriesChart";
import { seriesColor, seriesDash, useChartPalette } from "./chart-palette";
import {
  COMPONENT_LABELS,
  axisFormatterFor,
  displayLabelValue,
  formatterFor,
  type Panel,
} from "./metrics-catalog";
import { useMetricsScope } from "./metrics-scope";

/** Every chart in the application shares a crosshair. */
const SYNC_KEY = "zke-observability";

export type ChartPanelProps = {
  panel: Panel;
  /** Passed only by a view whose queries accept it. */
  top?: number;
  namespace?: string;
};

type PanelSeries = ChartSeries & {
  stats: { last: number | null; mean: number | null; max: number | null };
};

/**
 * One chart, with everything the Server can answer kept distinct.
 *
 * "No data", "no permission" and "this query needs a component nobody
 * installed" lead to completely different actions, so collapsing them into one
 * empty box would leave the reader to guess which one they are looking at.
 */
export function ChartPanel({ panel, top, namespace }: ChartPanelProps) {
  const { clusterIds, window: chartWindow, live, selectRange } = useMetricsScope();
  const palette = useChartPalette();
  const [hidden, setHidden] = useState<ReadonlySet<string>>(() => new Set());

  const results = useMetricsQueries(
    panel.queries.map((query) => ({
      name: query.name,
      clusterIds,
      start: new Date(chartWindow.startMs),
      end: new Date(chartWindow.endMs),
      stepSeconds: chartWindow.stepSeconds,
      ...(namespace ? { namespace } : {}),
      ...(top ? { top } : {}),
    })),
    { live, intervalMs: 0 },
  );

  const formatValue = useMemo(() => formatterFor(panel.unit), [panel.unit]);
  const formatAxis = useMemo(() => axisFormatterFor(panel.unit), [panel.unit]);
  const pending = results.some((result) => result.isPending);
  const fetching = results.some((result) => result.isFetching);
  const failed = results.find((result) => result.error);
  // Every request carries the same window, so one signature over the answers is
  // enough to know whether anything on the chart changed.
  const signature = results.map((result) => result.dataUpdatedAt).join(",");

  const chart = useMemo(
    () =>
      buildChart(
        panel,
        results.map((result) => result.data),
      ),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [panel, signature],
  );

  const issues = useMemo(() => {
    const seen = new Map<string, MetricsQueryIssue>();
    for (const result of results) {
      for (const issue of result.data?.issues ?? []) {
        seen.set(`${issue.reason}:${issue.cluster_id}`, issue);
      }
    }
    return [...seen.values()];
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [signature]);

  const toggle = (id: string) =>
    setHidden((current) => {
      const next = new Set(current);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });

  return (
    <section className="border-border bg-surface rounded-panel flex min-w-0 flex-col border p-4">
      <div className="mb-3 flex min-w-0 items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="text-foreground text-[13px] font-semibold tracking-tight">
            {panel.title}
          </h3>
          {panel.description ? (
            <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
              {panel.description}
            </p>
          ) : null}
        </div>
        {/* A quiet mark rather than a spinner over the chart: the previous
            answer stays on screen while the next one loads, and something has
            to say that it is the previous one. */}
        {fetching && !pending ? (
          <span className="text-subtle-foreground shrink-0 text-[11px]">更新中…</span>
        ) : null}
      </div>

      {pending ? <LoadingState /> : null}
      {!pending && failed?.error ? (
        <ErrorState
          error={failed.error}
          onRetry={isForbidden(failed.error) ? undefined : () => void failed.refetch()}
        />
      ) : null}
      {!pending && !failed && chart.series.length === 0 ? (
        <EmptyState title="暂无指标数据" description={emptyDescription(panel, namespace)} />
      ) : null}
      {!failed && chart.series.length > 0 ? (
        <div className={cn("transition-opacity", fetching && !pending && "opacity-60")}>
          <TimeSeriesChart
            timestamps={chart.timestamps}
            series={chart.series}
            palette={palette}
            formatValue={formatValue}
            formatAxis={formatAxis}
            ariaLabel={panel.title}
            hidden={hidden}
            fullScale={panel.fullScale}
            stacked={panel.stack}
            reference={panel.reference}
            onSelectRange={selectRange}
            syncKey={SYNC_KEY}
          />
          <ChartLegend
            series={chart.series}
            hidden={hidden}
            onToggle={toggle}
            formatValue={formatValue}
            colorAt={(index) => seriesColor(palette, index)}
            dashAt={seriesDash}
          />
        </div>
      ) : null}
      <IssueNotice issues={issues} />
    </section>
  );
}

/**
 * The legend, which is also the values without a pointer.
 *
 * Three summary numbers per curve rather than a colour chip and a name: the
 * question a chart is opened with is usually "which one is the highest" or
 * "which one moved", and both are answered here without hovering anything. It
 * is also what keeps identity off colour alone — several palette steps sit
 * below 3:1 against the light surface, which is only acceptable while every
 * series is named in text beside its value.
 */
function ChartLegend({
  series,
  hidden,
  onToggle,
  formatValue,
  colorAt,
  dashAt,
}: {
  series: PanelSeries[];
  hidden: ReadonlySet<string>;
  onToggle: (id: string) => void;
  formatValue: (value: number) => string;
  colorAt: (index: number) => string;
  dashAt: (index: number) => number[] | undefined;
}) {
  return (
    <div className="mt-3">
      <div className="text-subtle-foreground grid grid-cols-[minmax(0,1fr)_4.25rem_4.25rem_4.25rem] gap-x-3 px-1 pb-1 text-[11px]">
        <span>序列</span>
        <span className="text-right">最新</span>
        <span className="text-right">平均</span>
        <span className="text-right">最大</span>
      </div>
      <ul className="max-h-36 overflow-y-auto">
        {series.map((item, index) => {
          const off = hidden.has(item.id);
          const dash = dashAt(index);
          return (
            <li key={item.id}>
              <button
                type="button"
                role="switch"
                aria-checked={!off}
                onClick={() => onToggle(item.id)}
                className={cn(
                  "zke-focus rounded-control hover:bg-surface-muted grid w-full grid-cols-[minmax(0,1fr)_4.25rem_4.25rem_4.25rem] items-center gap-x-3 px-1 py-0.5 text-left text-xs transition-colors",
                  off && "opacity-45",
                )}
              >
                <span className="flex min-w-0 items-center gap-2">
                  <span
                    aria-hidden
                    className="h-0 w-3.5 shrink-0 border-t-2"
                    style={{
                      borderColor: colorAt(index),
                      borderTopStyle: dash ? "dashed" : "solid",
                    }}
                  />
                  <span className="text-muted-foreground truncate">{item.label}</span>
                </span>
                <span className="zke-tnum text-foreground text-right font-medium">
                  {item.stats.last === null ? "—" : formatValue(item.stats.last)}
                </span>
                <span className="zke-tnum text-muted-foreground text-right">
                  {item.stats.mean === null ? "—" : formatValue(item.stats.mean)}
                </span>
                <span className="zke-tnum text-muted-foreground text-right">
                  {item.stats.max === null ? "—" : formatValue(item.stats.max)}
                </span>
              </button>
            </li>
          );
        })}
      </ul>
    </div>
  );
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
export function IssueNotice({ issues }: { issues: MetricsQueryIssue[] }) {
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
 * An empty panel that depends on an exporter says so.
 *
 * Otherwise a Cluster that never installed the object exporter and a Cluster
 * that is genuinely idle produce the same blank chart, and the first one is
 * fixed by an install the operator would never think to make.
 */
function emptyDescription(panel: Panel, namespace: string | undefined): string {
  const note = panel.emptyNote ? ` ${panel.emptyNote}` : "";
  const required = [...new Set(panel.queries.map((query) => query.requires).filter(Boolean))];
  if (required.length > 0) {
    const names = required.map((component) => COMPONENT_LABELS[component!]).join("、");
    return `该视图需要${names}。它随采集组件一并安装——若集群已安装采集但这里仍为空，请在「采集接入」中确认该组件的状态。${note}`;
  }
  if (namespace) {
    return `该时间范围内 Namespace ${namespace} 没有采样。请确认名称，或该 Namespace 当前没有运行中的 Pod。${note}`;
  }
  return `该时间范围内没有采样。集群可能尚未启用采集，或采集刚刚开始。${note}`;
}

type QueryResult = { series: MetricsQuerySeries[] } | undefined;

/**
 * Merges every query behind a panel onto one set of axes.
 *
 * They share a window and a step, so the Server returns the same grid for each
 * and the columns line up by construction. A mismatch is still padded rather
 * than trusted: uPlot reads the columns positionally, and a short one would
 * silently shift a curve in time.
 */
function buildChart(
  panel: Panel,
  answers: QueryResult[],
): { timestamps: number[]; series: PanelSeries[] } {
  let timestamps: number[] = [];
  for (const answer of answers) {
    const points = answer?.series[0]?.points;
    if (points && points.length > timestamps.length) {
      timestamps = points.map((point) => Number(point[0]));
    }
  }
  const clusters = new Set<string>();
  for (const answer of answers) {
    for (const item of answer?.series ?? []) {
      clusters.add(item.cluster_id);
    }
  }
  const showCluster = clusters.size > 1;

  const series: PanelSeries[] = [];
  answers.forEach((answer, index) => {
    const query = panel.queries[index];
    if (!query) {
      return;
    }
    for (const item of answer?.series ?? []) {
      const values = alignValues(item, timestamps);
      series.push({
        id: `${query.name}:${item.cluster_id}:${panel.labels
          .map((label) => item.labels[label] ?? "")
          .join("/")}`,
        label: seriesLabel(item, panel.labels, query.label, showCluster),
        values,
        stats: summarise(values),
      });
    }
  });
  return { timestamps, series };
}

function alignValues(item: MetricsQuerySeries, timestamps: number[]): (number | null)[] {
  const values = item.points.map((point) => (point[1] === null ? null : Number(point[1])));
  if (values.length === timestamps.length) {
    return values;
  }
  const byTimestamp = new Map<number, number | null>();
  item.points.forEach((point, index) => {
    byTimestamp.set(Number(point[0]), values[index] ?? null);
  });
  return timestamps.map((at) => byTimestamp.get(at) ?? null);
}

function summarise(values: (number | null)[]): PanelSeries["stats"] {
  let last: number | null = null;
  let max: number | null = null;
  let total = 0;
  let counted = 0;
  for (const value of values) {
    if (value === null || !Number.isFinite(value)) {
      continue;
    }
    last = value;
    max = max === null ? value : Math.max(max, value);
    total += value;
    counted += 1;
  }
  return { last, mean: counted === 0 ? null : total / counted, max };
}

/**
 * What a curve is called.
 *
 * The Cluster is named only when the answer covers more than one — inside a
 * single Cluster it is the same word on every row, and it is the part that
 * pushes the Pod name out of the column.
 */
function seriesLabel(
  item: MetricsQuerySeries,
  dimensions: readonly string[],
  queryLabel: string | undefined,
  showCluster: boolean,
): string {
  const parts: string[] = [];
  if (queryLabel) {
    parts.push(queryLabel);
  }
  if (showCluster) {
    parts.push(item.cluster_name || item.cluster_id);
  }
  for (const dimension of dimensions) {
    const value = item.labels[dimension];
    if (value) {
      parts.push(displayLabelValue(dimension, value));
    }
  }
  if (parts.length === 0) {
    return item.cluster_name || item.cluster_id;
  }
  return parts.join(" · ");
}
