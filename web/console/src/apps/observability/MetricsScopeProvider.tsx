import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";

import { useMetricsQuery } from "@/api/queries/observability";
import { isForbidden } from "@/api/errors";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { useWindowVisible } from "@/desktop/window-visibility";

import {
  ALL_CLUSTERS,
  DEFAULT_RANGE,
  MetricsHealthContext,
  MetricsScopeContext,
  useMetricsHealth,
  useMetricsScope,
  type MetricsScopeValue,
} from "./metrics-scope";
import { resolveWindow, selectionToRange, zoomOut, type TimeRange } from "./time-range";

/**
 * The filters every chart in this application answers to.
 *
 * One provider rather than per-section state, because the sections are angles
 * on the same question: an operator who narrowed to one Cluster and one hour to
 * look at CPU should not have to say it again to look at disk. It also keeps
 * the window itself in one place — panels that resolved their own clock would
 * be describing windows that differ by however long each one took to mount.
 */
export function MetricsScopeProvider({
  enabled,
  children,
}: {
  enabled: boolean;
  children: ReactNode;
}) {
  const visible = useWindowVisible();
  const live = visible && enabled;
  const [clusterId, setClusterId] = useState<string>(ALL_CLUSTERS);
  const [namespace, setNamespace] = useState("");
  const [top, setTop] = useState(10);
  const [range, setRangeState] = useState<TimeRange>(DEFAULT_RANGE);
  const [refreshSeconds, setRefreshSeconds] = useState<number | null>(60);
  const [nowMs, setNowMs] = useState(() => Date.now());

  // The clock only advances for a relative range: an absolute one is a window
  // the operator pinned, and moving it would take the chart out from under the
  // thing they had just selected. A minimised window stops it entirely — every
  // request here ends up in storage every Cluster shares.
  useEffect(() => {
    if (!live || refreshSeconds === null || range.kind !== "relative") {
      return;
    }
    const timer = window.setInterval(() => setNowMs(Date.now()), refreshSeconds * 1000);
    return () => window.clearInterval(timer);
  }, [live, refreshSeconds, range.kind]);

  const setRange = useCallback((next: TimeRange) => {
    setNowMs(Date.now());
    setRangeState(next);
  }, []);

  const selectRange = useCallback(
    (startMs: number, endMs: number) => {
      const selected = selectionToRange(startMs, endMs);
      if (selected) {
        setRange(selected);
      }
    },
    [setRange],
  );

  const zoomOutRange = useCallback(() => {
    const at = Date.now();
    setRangeState((current) => zoomOut(current, at));
    setNowMs(at);
  }, []);

  const refresh = useCallback(() => setNowMs(Date.now()), []);

  const chartWindow = useMemo(() => resolveWindow(range, nowMs), [range, nowMs]);

  // A Cluster with no data at all cannot be filtered to here, which is the
  // honest answer — there is nothing to show for it.
  const health = useMetricsQuery(enabled ? { name: "collection_health" } : null, { live });
  const clusters = useMemo(() => {
    const seen = new Map<string, string>();
    for (const series of health.data?.series ?? []) {
      seen.set(series.cluster_id, series.cluster_name || series.cluster_id);
    }
    return [...seen.entries()].map(([id, name]) => ({ id, name }));
  }, [health.data]);

  const value = useMemo<MetricsScopeValue>(
    () => ({
      clusters,
      clusterId,
      setClusterId,
      clusterIds: clusterId === ALL_CLUSTERS ? undefined : [clusterId],
      namespace,
      setNamespace,
      top,
      setTop,
      range,
      setRange,
      selectRange,
      zoomOutRange,
      window: chartWindow,
      refreshSeconds,
      setRefreshSeconds,
      refresh,
      live,
    }),
    [
      chartWindow,
      clusterId,
      clusters,
      live,
      namespace,
      range,
      refresh,
      refreshSeconds,
      selectRange,
      setRange,
      top,
      zoomOutRange,
    ],
  );

  return (
    <MetricsScopeContext.Provider value={value}>
      <MetricsHealthContext.Provider value={health}>{children}</MetricsHealthContext.Provider>
    </MetricsScopeContext.Provider>
  );
}

/**
 * Nothing is worth drawing until some Cluster has reported something, and the
 * three reasons it might not have are answered differently: a failed inventory
 * is retried, a forbidden one is not, and an empty one sends the operator to
 * the install screen rather than leaving them in front of blank axes.
 */
export function MetricsGate({ children }: { children: ReactNode }) {
  const health = useMetricsHealth();
  const { clusters } = useMetricsScope();
  if (!health || health.isPending) {
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
  return <>{children}</>;
}
