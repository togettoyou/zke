import { useQuery } from "@tanstack/react-query";

import { api, unwrap } from "../client";
import { queryKeys } from "../query-keys";

/**
 * How often resource usage is re-read while the view holding it is on screen.
 *
 * `live` is the caller's answer to "is anyone looking": passing `false` holds
 * the poll without disturbing what is already cached, which is what a minimized
 * window wants. Every request here is executed by a Cluster's Agent, so a poll
 * nobody can see is work asked of real infrastructure for nothing.
 */
const USAGE_POLL_MS = 30_000;

export type PollOptions = {
  /** Defaults to polling, so a caller that has no opinion behaves as before. */
  live?: boolean;
};

export function useNodeMetrics(clusterId: string | null, { live = true }: PollOptions = {}) {
  return useQuery({
    queryKey: queryKeys.nodeMetrics(clusterId ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster_id}/metrics/nodes", {
          params: { path: { cluster_id: clusterId as string } },
          signal,
        }),
      ),
    enabled: Boolean(clusterId),
    refetchInterval: live && USAGE_POLL_MS,
  });
}

export function usePodMetrics(
  clusterId: string | null,
  namespace: string | null,
  { live = true }: PollOptions = {},
) {
  return useQuery({
    queryKey: queryKeys.podMetrics(clusterId ?? "", namespace ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/metrics/pods", {
          params: {
            path: {
              cluster_id: clusterId as string,
              namespace_name: namespace as string,
            },
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId && namespace),
    refetchInterval: live && USAGE_POLL_MS,
  });
}
