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

type Unit = "millicores" | "bytes" | "bytes_per_second" | "ratio" | "count";

type Panel = {
  title: string;
  description?: string;
  query: string;
  unit: Unit;
  /** Series labels besides the Cluster, in display order. */
  labels: readonly string[];
  /** The scrape target this panel needs, when it is not the kubelet. */
  requires?: "kube-state-metrics" | "node-exporter";
};

/**
 * What the view shows: a dimension, and an angle on it.
 *
 * Two selects rather than one long list of charts. Each panel is a request that
 * a single-instance Server runs against storage every Cluster shares, and the
 * catalogue is large enough now that showing all of it at once would make an
 * open window a standing load on the whole deployment. It is also how the
 * question is asked — an operator looks at Namespace requests, or at Node
 * utilisation, not at twenty charts while comparing.
 */
const DIMENSIONS = [
  {
    id: "cluster",
    label: "集群",
    views: [
      {
        id: "usage",
        label: "用量",
        top: false,
        namespace: false,
        panels: [
          { title: "CPU 用量", query: "cluster_cpu_usage", unit: "millicores", labels: [] },
          { title: "内存用量", query: "cluster_memory_usage", unit: "bytes", labels: [] },
        ],
      },
      {
        id: "utilization",
        label: "利用率",
        top: false,
        namespace: false,
        panels: [
          {
            title: "CPU 利用率",
            description: "用量占全部节点可分配量的比例。",
            query: "cluster_cpu_utilization",
            unit: "ratio",
            labels: [],
            requires: "kube-state-metrics",
          },
          {
            title: "内存利用率",
            query: "cluster_memory_utilization",
            unit: "ratio",
            labels: [],
            requires: "kube-state-metrics",
          },
        ],
      },
    ],
  },
  {
    id: "node",
    label: "节点",
    views: [
      {
        id: "usage",
        label: "用量",
        top: true,
        namespace: false,
        panels: [
          { title: "CPU 用量", query: "node_cpu_usage", unit: "millicores", labels: ["node"] },
          { title: "内存用量", query: "node_memory_usage", unit: "bytes", labels: ["node"] },
        ],
      },
      {
        id: "utilization",
        label: "利用率",
        top: true,
        namespace: false,
        panels: [
          {
            title: "CPU 利用率",
            description: "用量占该节点可分配量的比例。",
            query: "node_cpu_utilization",
            unit: "ratio",
            labels: ["node"],
            requires: "kube-state-metrics",
          },
          {
            title: "内存利用率",
            query: "node_memory_utilization",
            unit: "ratio",
            labels: ["node"],
            requires: "kube-state-metrics",
          },
        ],
      },
    ],
  },
  {
    id: "namespace",
    label: "Namespace",
    views: [
      {
        id: "usage",
        label: "用量",
        top: true,
        namespace: true,
        panels: [
          {
            title: "CPU 用量",
            query: "namespace_cpu_usage",
            unit: "millicores",
            labels: ["namespace"],
          },
          {
            title: "内存用量",
            query: "namespace_memory_usage",
            unit: "bytes",
            labels: ["namespace"],
          },
        ],
      },
      {
        id: "requests",
        label: "申请量",
        top: true,
        namespace: true,
        panels: [
          {
            title: "CPU 申请量",
            description: "调度器为该 Namespace 预留、其他工作负载无法使用的量。",
            query: "namespace_cpu_requests",
            unit: "millicores",
            labels: ["namespace"],
            requires: "kube-state-metrics",
          },
          {
            title: "内存申请量",
            query: "namespace_memory_requests",
            unit: "bytes",
            labels: ["namespace"],
            requires: "kube-state-metrics",
          },
        ],
      },
      {
        id: "limits",
        label: "限制量",
        top: true,
        namespace: true,
        panels: [
          {
            title: "CPU 限制量",
            query: "namespace_cpu_limits",
            unit: "millicores",
            labels: ["namespace"],
            requires: "kube-state-metrics",
          },
          {
            title: "内存限制量",
            query: "namespace_memory_limits",
            unit: "bytes",
            labels: ["namespace"],
            requires: "kube-state-metrics",
          },
        ],
      },
    ],
  },
  {
    id: "workload",
    label: "工作负载",
    views: [
      {
        id: "usage",
        label: "用量",
        top: true,
        namespace: true,
        panels: [
          {
            title: "CPU 用量",
            description:
              "Pod 用量按拥有它的控制器汇总；Deployment 的 Pod 会归到 Deployment 而不是 ReplicaSet。",
            query: "workload_cpu_usage",
            unit: "millicores",
            labels: ["namespace", "workload_kind", "workload"],
            requires: "kube-state-metrics",
          },
          {
            title: "内存用量",
            query: "workload_memory_usage",
            unit: "bytes",
            labels: ["namespace", "workload_kind", "workload"],
            requires: "kube-state-metrics",
          },
        ],
      },
    ],
  },
  {
    id: "pod",
    label: "Pod",
    views: [
      {
        id: "usage",
        label: "用量",
        top: true,
        namespace: true,
        panels: [
          {
            title: "CPU 用量",
            query: "pod_cpu_usage",
            unit: "millicores",
            labels: ["namespace", "pod"],
          },
          {
            title: "内存用量",
            query: "pod_memory_usage",
            unit: "bytes",
            labels: ["namespace", "pod"],
          },
        ],
      },
      {
        id: "restarts",
        label: "重启",
        top: true,
        namespace: true,
        panels: [
          {
            title: "重启次数",
            description: "所选时间范围内新增的容器重启次数，不是计数器的累计值。",
            query: "pod_restarts",
            unit: "count",
            labels: ["namespace", "pod"],
            requires: "kube-state-metrics",
          },
        ],
      },
    ],
  },
  {
    id: "node-io",
    label: "磁盘与网络",
    views: [
      {
        id: "filesystem",
        label: "文件系统",
        top: true,
        namespace: false,
        panels: [
          {
            title: "文件系统使用率",
            query: "node_filesystem_utilization",
            unit: "ratio",
            labels: ["node", "mountpoint"],
            requires: "node-exporter",
          },
        ],
      },
      {
        id: "network",
        label: "网络",
        top: true,
        namespace: false,
        panels: [
          {
            title: "网络接收",
            query: "node_network_receive",
            unit: "bytes_per_second",
            labels: ["node", "device"],
            requires: "node-exporter",
          },
          {
            title: "网络发送",
            query: "node_network_transmit",
            unit: "bytes_per_second",
            labels: ["node", "device"],
            requires: "node-exporter",
          },
        ],
      },
      {
        id: "disk",
        label: "磁盘 IO",
        top: true,
        namespace: false,
        panels: [
          {
            title: "磁盘读取",
            query: "node_disk_read",
            unit: "bytes_per_second",
            labels: ["node", "device"],
            requires: "node-exporter",
          },
          {
            title: "磁盘写入",
            query: "node_disk_write",
            unit: "bytes_per_second",
            labels: ["node", "device"],
            requires: "node-exporter",
          },
        ],
      },
    ],
  },
] as const satisfies readonly {
  id: string;
  label: string;
  views: readonly {
    id: string;
    label: string;
    top: boolean;
    namespace: boolean;
    panels: readonly Panel[];
  }[];
}[];

type DimensionId = (typeof DIMENSIONS)[number]["id"];

const TOP_CHOICES = [5, 10, 20] as const;

const ALL_CLUSTERS = "__all__";

/** Which install a missing component belongs to, in the operator's words. */
const COMPONENT_LABELS: Record<string, string> = {
  "kube-state-metrics": "对象指标导出器（kube-state-metrics）",
  "node-exporter": "节点指标导出器（node-exporter）",
};

/**
 * What the Server accepts as a Namespace, checked here so a typo does not
 * become a 400 the operator has to read as an error banner.
 */
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

function formatterFor(unit: Unit): (value: number) => string {
  switch (unit) {
    case "bytes":
      return formatBytes;
    case "bytes_per_second":
      return (value) => `${formatBytes(value)}/s`;
    case "ratio":
      return (value) => `${(value * 100).toFixed(1)}%`;
    case "count":
      return (value) => `${Math.round(value)} 次`;
    default:
      return formatMillicores;
  }
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
 * permission" and "this query needs a component nobody installed" lead to
 * completely different actions, so collapsing them into one empty box would
 * leave the reader to guess which one they are looking at.
 */
function MetricsPanel({
  panel,
  clusterIds,
  namespace,
  range,
  window: chartWindow,
  top,
  live,
}: {
  panel: Panel;
  clusterIds: string[] | undefined;
  namespace?: string;
  range: (typeof RANGES)[number];
  window: { start: Date; end: Date };
  top?: number;
  live: boolean;
}) {
  const query = useMetricsQuery(
    {
      name: panel.query,
      clusterIds,
      start: chartWindow.start,
      end: chartWindow.end,
      stepSeconds: range.stepSeconds,
      ...(namespace ? { namespace } : {}),
      ...(top ? { top } : {}),
    },
    { live },
  );

  const formatValue = formatterFor(panel.unit);
  const labels = panel.labels;
  const chart = useMemo(() => {
    const result = query.data;
    if (!result) {
      return null;
    }
    const timestamps = result.series[0]?.points.map((point) => Number(point[0])) ?? [];
    const series: ChartSeries[] = result.series.map((item) => ({
      id: `${item.cluster_id}:${labels.map((label) => item.labels[label] ?? "").join("/")}`,
      label: seriesLabel(item, labels),
      values: item.points.map((point) => (point[1] === null ? null : Number(point[1]))),
    }));
    return { timestamps, series };
  }, [labels, query.data]);

  return (
    <section className="border-border bg-surface rounded-panel border p-4">
      <SectionTitle title={panel.title} description={panel.description} />
      {query.isPending ? <LoadingState /> : null}
      {query.error ? (
        <ErrorState
          error={query.error}
          onRetry={isForbidden(query.error) ? undefined : () => void query.refetch()}
        />
      ) : null}
      {!query.isPending && !query.error && chart && chart.series.length === 0 ? (
        <EmptyState title="暂无指标数据" description={emptyDescription(panel, namespace)} />
      ) : null}
      {!query.error && chart && chart.series.length > 0 ? (
        <>
          <TimeSeriesChart
            timestamps={chart.timestamps}
            series={chart.series}
            formatValue={formatValue}
            ariaLabel={panel.title}
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

/**
 * An empty panel that depends on an exporter says so.
 *
 * Otherwise a Cluster that never installed the object exporter and a Cluster
 * that is genuinely idle produce the same blank chart, and the first one is
 * fixed by an install the operator would never think to make.
 */
function emptyDescription(panel: Panel, namespace: string | undefined): string {
  if (panel.requires) {
    return `该视图需要${COMPONENT_LABELS[panel.requires]}。它随采集组件一并安装——若集群已安装采集但这里仍为空，请在「采集接入」中确认该组件的状态。`;
  }
  if (namespace) {
    return `该时间范围内 Namespace ${namespace} 没有采样。请确认名称，或该 Namespace 当前没有运行中的 Pod。`;
  }
  return "该时间范围内没有采样。集群可能尚未启用采集，或采集刚刚开始。";
}

export function MetricsOverviewSection() {
  const live = useWindowVisible();
  const namespaceInputId = useId();
  const [rangeId, setRangeId] = useState<RangeId>("1h");
  const [dimensionId, setDimensionId] = useState<DimensionId>("cluster");
  const [viewId, setViewId] = useState<string>("usage");
  const [clusterId, setClusterId] = useState<string>(ALL_CLUSTERS);
  const [top, setTop] = useState<number>(10);
  // Draft and applied are separate so a half-typed Namespace never becomes a
  // request: "kube-sys" is a valid name that simply has no data, and firing it
  // would answer 暂无数据 while the operator is still typing 「kube-system」.
  const [namespaceDraft, setNamespaceDraft] = useState("");
  const [namespace, setNamespace] = useState("");
  const range = RANGES.find((item) => item.id === rangeId) ?? RANGES[0];
  const dimension = DIMENSIONS.find((item) => item.id === dimensionId) ?? DIMENSIONS[0];
  // Falling back to the dimension's first view rather than clearing the
  // selection: 用量 exists everywhere, so moving between dimensions keeps
  // showing the same angle wherever it makes sense.
  const view = dimension.views.find((item) => item.id === viewId) ?? dimension.views[0];

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
  // dropped here when the view changes under it.
  const appliedNamespace = view.namespace ? namespace : "";

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
        <div className="flex flex-wrap gap-1">
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

      {dimension.views.length > 1 ? (
        <div className="flex flex-wrap gap-1">
          {dimension.views.map((item) => (
            <Button
              key={item.id}
              variant={item.id === view.id ? "primary" : "ghost"}
              size="sm"
              onClick={() => setViewId(item.id)}
            >
              {item.label}
            </Button>
          ))}
        </div>
      ) : null}

      {view.top || view.namespace ? (
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            if (!namespaceInvalid) {
              setNamespace(namespaceDraft);
            }
          }}
        >
          {view.namespace ? (
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
          {view.top ? (
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
          ) : null}
          {view.namespace ? (
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

      {view.panels.map((panel) => (
        <MetricsPanel
          key={panel.query}
          panel={panel}
          clusterIds={selectedClusters}
          namespace={appliedNamespace}
          range={range}
          window={chartWindow}
          top={view.top ? top : undefined}
          live={live}
        />
      ))}
    </div>
  );
}
