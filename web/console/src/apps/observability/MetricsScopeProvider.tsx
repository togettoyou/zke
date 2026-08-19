import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useQueryClient } from "@tanstack/react-query";

import { useClusters } from "@/api/queries/clusters";
import { useMetricsQuery } from "@/api/queries/observability";
import { queryKeyPrefixes } from "@/api/query-keys";
import { isForbidden } from "@/api/errors";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { useWindowVisible } from "@/desktop/window-visibility";
import { useScopeStore } from "@/scope/scope-store";

import {
  DEFAULT_RANGE,
  MetricsHealthContext,
  MetricsScopeContext,
  useMetricsHealth,
  useMetricsScope,
  type MetricsScopeValue,
} from "./metrics-scope";
import {
  resolveWindow,
  selectionToRange,
  windowKeyFor,
  zoomOut,
  type TimeRange,
  type TimeWindow,
} from "./time-range";

/**
 * How many Clusters the target picker offers.
 *
 * The Server caps a page at 100 and rejects anything larger outright, so this
 * is the whole list in one request rather than an arbitrary ceiling. The
 * terminal's Cluster picker asks for the same page for the same reason: a
 * picker is a choice, not a listing, and paging inside a toolbar select would
 * be the wrong control for a Project that outgrows it.
 */
const CLUSTER_CHOICE_LIMIT = 100;

type ClusterListQuery = ReturnType<typeof useClusters>;

/**
 * The Cluster listing itself, so the gate can tell "this Project has no
 * Cluster" apart from "the list has not loaded yet" and from "the Cluster is
 * there but has never reported". Those three send the operator to three
 * different places.
 */
const ClusterListContext = createContext<ClusterListQuery | null>(null);

/**
 * The filters every chart in this application answers to.
 *
 * One provider rather than per-section state, because the sections are angles
 * on the same question: an operator who narrowed to one hour to look at CPU
 * should not have to say it again to look at disk. It also keeps the window
 * itself in one place — panels that resolved their own clock would be
 * describing windows that differ by however long each one took to mount.
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
  const queryClient = useQueryClient();
  const projectId = useScopeStore((state) => state.scope.projectId);
  const [selectedClusterId, setSelectedClusterId] = useState("");
  // Remembered with the Cluster it was chosen in, not on its own. Namespace
  // names only mean something inside one Cluster, and a filter carried across a
  // Cluster switch would quietly narrow every chart to a name the new Cluster
  // may not even have — the picker would read 全部 while the queries did not.
  const [namespaceChoice, setNamespaceChoice] = useState({ clusterId: "", name: "" });
  const [top, setTop] = useState(10);
  const [range, setRangeState] = useState<TimeRange>(DEFAULT_RANGE);
  const [refreshSeconds, setRefreshSeconds] = useState<number | null>(60);
  const [nowMs, setNowMs] = useState(() => Date.now());

  const chartWindow = useMemo(() => resolveWindow(range, nowMs), [range, nowMs]);
  const windowKey = useMemo(() => windowKeyFor(range, chartWindow), [range, chartWindow]);

  // The window a request asks for is read when it asks, rather than being baked
  // into its cache key. One entry per chart then survives the clock moving,
  // which is what lets a refresh replace the numbers in place instead of asking
  // a question the cache has never seen and blanking the panel back to a
  // spinner.
  const windowRef = useRef<TimeWindow>(chartWindow);
  const readWindow = useCallback(() => windowRef.current, []);

  /**
   * Moves the window every chart reads, and tells them it moved.
   *
   * The ref is written here, where the move is initiated, and not from an
   * effect watching the resolved window. The panels are children, and a child's
   * effects run before its parent's: a new range gives them a new cache key,
   * and the effect that subscribes them issues the request for it while this
   * component's own effects are still pending. A window written afterwards
   * arrives too late for the very request it was meant to describe — the first
   * request for a new range asked for the previous one and filed the answer
   * under the new key, so the charts went on showing the old window until
   * something invalidated them. That something was the refresh button, which is
   * why a range only appeared to apply on the second try.
   */
  const moveWindow = useCallback((next: TimeRange) => {
    const at = Date.now();
    windowRef.current = resolveWindow(next, at);
    setRangeState(next);
    setNowMs(at);
  }, []);

  // The clock only advances for a relative range: an absolute one is a window
  // the operator pinned, and moving it would take the chart out from under the
  // thing they had just selected. A minimised window stops it entirely — every
  // request here ends up in storage every Cluster shares.
  useEffect(() => {
    if (!live || refreshSeconds === null || range.kind !== "relative") {
      return;
    }
    const timer = window.setInterval(() => moveWindow(range), refreshSeconds * 1000);
    return () => window.clearInterval(timer);
  }, [live, moveWindow, range, refreshSeconds]);

  const selectRange = useCallback(
    (startMs: number, endMs: number) => {
      const selected = selectionToRange(startMs, endMs);
      if (selected) {
        moveWindow(selected);
      }
    },
    [moveWindow],
  );

  const zoomOutRange = useCallback(
    () => moveWindow(zoomOut(range, Date.now())),
    [moveWindow, range],
  );

  const refresh = useCallback(() => moveWindow(range), [moveWindow, range]);

  // Refreshing is an invalidation, not a new key: the observers stay mounted
  // and keep the answer they are showing until the next one arrives.
  //
  // Skipped on the first run — the queries are fetching anyway, and invalidating
  // them at that moment would only ask twice.
  const primed = useRef(false);
  useEffect(() => {
    if (!enabled) {
      return;
    }
    if (!primed.current) {
      primed.current = true;
      return;
    }
    void queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.metricsQuery });
  }, [enabled, nowMs, queryClient]);

  // The target comes from the Project's own Clusters rather than from whatever
  // the caller can read metrics for. It is the list 采集接入 manages and the
  // list every other application works in, so the Cluster an operator was just
  // looking at in the container service is the one this picker offers — and a
  // Cluster name is unique inside its Project, so the names in it need no
  // further qualification to be unambiguous.
  const clusterList = useClusters(enabled ? projectId : null, {
    status: "active",
    limit: CLUSTER_CHOICE_LIMIT,
    offset: 0,
  });
  const clusters = useMemo(
    () =>
      (clusterList.data?.clusters ?? []).map((cluster) => ({
        id: cluster.id,
        name: cluster.name,
      })),
    [clusterList.data],
  );

  // Derived rather than written back by an effect: the operator's choice stands
  // as they made it, and the fallback to the first Cluster only applies while
  // the selection names nothing in the current list — which is what happens on
  // first load and after the Project changes underneath.
  const selectionListed = clusters.some((cluster) => cluster.id === selectedClusterId);
  const clusterId = selectionListed ? selectedClusterId : (clusters[0]?.id ?? "");

  // Nothing to ask about until a target exists, and the Server refuses a query
  // without one — so the probe waits instead of firing a request already known
  // to fail.
  // Polling is the view's job, not each query's: one invalidation moves every
  // panel to the same window, where independent intervals would drift apart.
  const health = useMetricsQuery(
    enabled && clusterId ? { name: "collection_health", clusterId } : null,
    { live, intervalMs: 0 },
  );

  const namespace = namespaceChoice.clusterId === clusterId ? namespaceChoice.name : "";
  const setNamespace = useCallback(
    (name: string) => setNamespaceChoice({ clusterId, name }),
    [clusterId],
  );

  const value = useMemo<MetricsScopeValue>(
    () => ({
      clusters,
      clusterId,
      setClusterId: setSelectedClusterId,
      namespace,
      setNamespace,
      top,
      setTop,
      range,
      setRange: moveWindow,
      selectRange,
      zoomOutRange,
      windowKey,
      readWindow,
      refreshSeconds,
      setRefreshSeconds,
      refresh,
      live,
    }),
    [
      clusterId,
      clusters,
      live,
      moveWindow,
      namespace,
      setNamespace,
      range,
      readWindow,
      refresh,
      refreshSeconds,
      selectRange,
      top,
      windowKey,
      zoomOutRange,
    ],
  );

  return (
    <MetricsScopeContext.Provider value={value}>
      <MetricsHealthContext.Provider value={health}>
        <ClusterListContext.Provider value={clusterList}>{children}</ClusterListContext.Provider>
      </MetricsHealthContext.Provider>
    </MetricsScopeContext.Provider>
  );
}

/**
 * Nothing is worth drawing until there is a target Cluster and it has reported
 * something. The reasons it might not have are answered differently: a failed
 * listing is retried, a forbidden one is not, a Project with no Cluster sends
 * the operator to 集群接入, and a Cluster that has never reported sends them to
 * 采集接入 rather than leaving them in front of a screen of blank axes.
 */
export function MetricsGate({ children }: { children: ReactNode }) {
  const health = useMetricsHealth();
  const clusterList = useContext(ClusterListContext);
  const { clusters, clusterId } = useMetricsScope();

  if (!clusterList || clusterList.isPending) {
    return <LoadingState />;
  }
  if (clusterList.error) {
    return (
      <ErrorState
        error={clusterList.error}
        onRetry={isForbidden(clusterList.error) ? undefined : () => void clusterList.refetch()}
      />
    );
  }
  if (clusters.length === 0 || !clusterId) {
    return (
      <EmptyState
        title="当前项目下还没有集群"
        description="在「集群接入」中接入集群后，这里可以选择要查看的目标集群。"
      />
    );
  }
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
  if ((health.data?.series.length ?? 0) === 0) {
    return (
      <EmptyState
        title="该集群尚未上报指标"
        description="在「采集接入」中为这个集群安装采集组件后，指标会在一个采集周期内出现。"
      />
    );
  }
  return <>{children}</>;
}
