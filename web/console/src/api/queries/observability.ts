import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, csrfHeaders, unwrap } from "../client";
import { queryKeys } from "../query-keys";

/**
 * How often an open metrics view re-reads its window.
 *
 * Slower than the resource-usage poll: that one reads a Cluster's Agent for a
 * live number, while this reads the Server's storage for a curve whose newest
 * point is a scrape interval old anyway. Polling faster would redraw the same
 * chart.
 */
const METRICS_POLL_MS = 60_000;

/**
 * Collector status is polled faster than the charts: after an install the
 * operator is waiting for the Pod to come up, and a minute of "0/1 ready"
 * reads as a failure.
 */
const COLLECTOR_POLL_MS = 10_000;

/**
 * The list view asks every visible Cluster at once, so its cadence is the
 * single-Cluster one multiplied by the number of rows. Three times slower keeps
 * an open window from being a standing load on every Agent in the project.
 */
const COLLECTOR_LIST_POLL_MS = 30_000;

export type MetricsPollOptions = {
  /** Defaults to polling, so a caller with no opinion behaves as before. */
  live?: boolean;
};

export type MetricsQueryParams = {
  name: string;
  clusterIds?: string[];
  namespace?: string;
  start?: Date;
  end?: Date;
  stepSeconds?: number;
  top?: number;
};

export function useMetricsQueryCatalog() {
  return useQuery({
    queryKey: queryKeys.metricsQueryCatalog(),
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/observability/metrics/queries", { signal })),
    // The catalogue is the Server's fixed vocabulary; it changes with a
    // release, not with data.
    staleTime: Number.POSITIVE_INFINITY,
  });
}

export function useMetricsQuery(
  params: MetricsQueryParams | null,
  { live = true }: MetricsPollOptions = {},
) {
  const query = params
    ? {
        name: params.name,
        ...(params.clusterIds?.length ? { cluster_ids: params.clusterIds.join(",") } : {}),
        ...(params.namespace ? { namespace: params.namespace } : {}),
        ...(params.start ? { start: params.start.toISOString() } : {}),
        ...(params.end ? { end: params.end.toISOString() } : {}),
        ...(params.stepSeconds ? { step_seconds: params.stepSeconds } : {}),
        ...(params.top ? { top: params.top } : {}),
      }
    : null;
  return useQuery({
    queryKey: queryKeys.metricsQuery(query ?? {}),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/observability/metrics/query", {
          params: { query: query as NonNullable<typeof query> },
          signal,
        }),
      ),
    enabled: Boolean(query),
    refetchInterval: live && METRICS_POLL_MS,
  });
}

/**
 * Whether the target Cluster is collecting metrics.
 *
 * The answer comes from the Cluster through its Agent, not from a Server
 * record, so opening the view is how an operator finds out that somebody
 * removed the collector by hand. It polls while visible because an install
 * takes a moment to become ready.
 */
export function useMetricsCollector(
  clusterId: string | null,
  { live = true }: MetricsPollOptions = {},
) {
  return useQuery({
    queryKey: queryKeys.metricsCollector(clusterId ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster_id}/metrics-collector", {
          params: { path: { cluster_id: clusterId as string } },
          signal,
        }),
      ),
    enabled: Boolean(clusterId),
    refetchInterval: live && COLLECTOR_POLL_MS,
  });
}

/**
 * The same answer for a whole list of Clusters, one request each.
 *
 * There is no batch route on purpose: each answer comes from that Cluster's own
 * Agent, so a combined endpoint would be one request that is slow as the
 * slowest Cluster and partially wrong whenever any Agent is offline. Asking per
 * Cluster lets each row show its own state, including "this Agent is not
 * connected", while the others load.
 *
 * Polled more slowly than a single Cluster's status for the same reason: the
 * cost is multiplied by the number of Clusters, and each poll reaches a real
 * Kubernetes API server. An install or uninstall invalidates its own Cluster's
 * entry, so the operator's own action is still reflected immediately.
 */
export function useMetricsCollectorStates(
  clusterIds: string[],
  { live = true }: MetricsPollOptions = {},
) {
  return useQueries({
    queries: clusterIds.map((clusterId) => ({
      queryKey: queryKeys.metricsCollector(clusterId),
      queryFn: async ({ signal }: { signal: AbortSignal }) =>
        unwrap(
          await api.GET("/api/v1/clusters/{cluster_id}/metrics-collector", {
            params: { path: { cluster_id: clusterId } },
            signal,
          }),
        ),
      refetchInterval: live && COLLECTOR_LIST_POLL_MS,
    })),
  });
}

export function useInstallMetricsCollector() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (clusterId: string) =>
      unwrap(
        await api.POST("/api/v1/clusters/{cluster_id}/metrics-collector", {
          params: { path: { cluster_id: clusterId } },
          headers: csrfHeaders(),
        }),
      ),
    onSuccess: async (_data, clusterId) =>
      queryClient.invalidateQueries({ queryKey: queryKeys.metricsCollector(clusterId) }),
  });
}

export function useUninstallMetricsCollector() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (clusterId: string) =>
      unwrap(
        await api.DELETE("/api/v1/clusters/{cluster_id}/metrics-collector", {
          params: { path: { cluster_id: clusterId } },
          headers: csrfHeaders(),
        }),
      ),
    onSuccess: async (_data, clusterId) =>
      queryClient.invalidateQueries({ queryKey: queryKeys.metricsCollector(clusterId) }),
  });
}
