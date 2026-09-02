import { useMemo, useRef, useState } from "react";
import { Code2, Maximize2 } from "lucide-react";

import { useMetricsQueries } from "@/api/queries/observability";
import type { MetricsQueryIssue, MetricsQuerySeries } from "@/api/types";
import { isForbidden } from "@/api/errors";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { CopyIconButton } from "@/components/common/copy";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/cn";

import { ChartLegend, type LegendSeries } from "./ChartLegend";
import { TimeSeriesChart } from "./TimeSeriesChart";
import { seriesColor, seriesDash, useChartPalette } from "./chart-palette";
import { summarise } from "./series-stats";
import {
  COMPONENT_LABELS,
  axisFormatterFor,
  displayLabelValue,
  formatterFor,
  type Panel,
} from "./metrics-catalog";
import { useMetricsScope } from "./metrics-scope";

/** Every chart in the application shares a crosshair. */
const SYNC_KEY = "zke-monitoring";

export type ChartPanelProps = {
  panel: Panel;
  /** Passed only by a view whose queries accept it. */
  top?: number;
  namespace?: string;
};

type PanelSeries = LegendSeries;

/**
 * One chart, with everything the Server can answer kept distinct.
 *
 * "No data", "no permission" and "this query needs a component nobody
 * installed" lead to completely different actions, so collapsing them into one
 * empty box would leave the reader to guess which one they are looking at.
 */
export function ChartPanel({ panel, top, namespace }: ChartPanelProps) {
  const { clusterId, windowKey, readWindow, live, selectRange } = useMetricsScope();
  const palette = useChartPalette();
  const [hidden, setHidden] = useState<ReadonlySet<string>>(() => new Set());
  const [expanded, setExpanded] = useState(false);
  const [queryOpen, setQueryOpen] = useState(false);
  const queryTitleRef = useRef<HTMLHeadingElement>(null);

  const results = useMetricsQueries(
    panel.queries.map((query) => ({
      name: query.name,
      clusterId,
      windowKey,
      window: readWindow,
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

  const expressions = results.flatMap((result, index) => {
    const expression = result.data?.expression;
    if (!expression) return [];
    return [
      {
        name:
          panel.queries[index]?.label ||
          result.data?.title ||
          panel.queries[index]?.name ||
          "MetricsQL",
        expression,
      },
    ];
  });

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
    <>
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
            to say that it is the previous one.

            Always mounted and faded, never conditionally rendered: appearing
            and disappearing changes the width left to the title beside it, and
            a heading that reflows once a minute is its own kind of flicker. */}
          <div className="flex shrink-0 items-center gap-1">
            <span
              aria-hidden={!(fetching && !pending)}
              className={cn(
                "text-subtle-foreground text-[11px] transition-opacity duration-200",
                fetching && !pending ? "opacity-100" : "opacity-0",
              )}
            >
              更新中…
            </span>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={`查看${panel.title}的查询语句`}
              title="查看查询语句"
              disabled={expressions.length === 0}
              onClick={() => setQueryOpen(true)}
            >
              <Code2 />
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={`展开查看${panel.title}`}
              title="展开查看"
              onClick={() => setExpanded(true)}
            >
              <Maximize2 />
            </Button>
          </div>
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
        {/* The chart is not dimmed while the next answer loads.
          uPlot redraws in place through `setData`, so a refresh moves the
          curves and nothing else — but fading the whole panel to 60% and back
          on every poll turned that quiet update into a blink across every chart
          on screen at once, which reads as a full redraw. The 更新中… mark in
          the header is what says the answer on screen is the previous one, and
          it costs no ink on the data itself. */}
        {!failed && chart.series.length > 0 ? (
          <div>
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
      <Dialog open={expanded} onOpenChange={setExpanded}>
        <DialogContent aria-describedby={undefined} className="w-[min(1120px,calc(100vw-2rem))]">
          <DialogHeader>
            <DialogTitle>{panel.title}</DialogTitle>
            {panel.description ? <DialogDescription>{panel.description}</DialogDescription> : null}
          </DialogHeader>
          {chart.series.length > 0 ? (
            <div className="min-h-[420px]">
              <TimeSeriesChart
                timestamps={chart.timestamps}
                series={chart.series}
                palette={palette}
                formatValue={formatValue}
                formatAxis={formatAxis}
                ariaLabel={`${panel.title}详细视图`}
                hidden={hidden}
                fullScale={panel.fullScale}
                stacked={panel.stack}
                reference={panel.reference}
                onSelectRange={selectRange}
                syncKey={`${SYNC_KEY}-expanded`}
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
          ) : (
            <EmptyState title="暂无指标数据" description={emptyDescription(panel, namespace)} />
          )}
        </DialogContent>
      </Dialog>
      <Dialog open={queryOpen} onOpenChange={setQueryOpen}>
        <DialogContent
          aria-describedby={undefined}
          className="w-[min(880px,calc(100vw-2rem))]"
          onOpenAutoFocus={(event) => {
            event.preventDefault();
            queryTitleRef.current?.focus();
          }}
        >
          <DialogHeader>
            <DialogTitle ref={queryTitleRef} tabIndex={-1} className="outline-none">
              {panel.title} · 查询语句
            </DialogTitle>
            <DialogDescription>
              以下是当前图表实际参数对应的可移植 MetricsQL。Server
              执行时仍会强制注入目标集群条件，展示和复制内容不包含 ZKE 集群标识。
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            {expressions.map((item) => (
              <section
                key={`${item.name}:${item.expression}`}
                className="border-border rounded-control border"
              >
                <div className="border-border bg-surface-muted flex items-center justify-between border-b px-3 py-2">
                  <span className="text-foreground text-xs font-medium">{item.name}</span>
                  <CopyIconButton value={item.expression} label={`复制${item.name}查询语句`} />
                </div>
                <pre className="zke-mono text-foreground overflow-x-auto p-3 text-xs leading-relaxed whitespace-pre-wrap">
                  {item.expression}
                </pre>
              </section>
            ))}
          </div>
        </DialogContent>
      </Dialog>
    </>
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
