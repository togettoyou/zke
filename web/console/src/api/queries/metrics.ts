import { useQuery } from "@tanstack/react-query";

import { api, unwrap } from "../client";
import { queryKeys } from "../query-keys";

export function useNodeMetrics(clusterId: string | null) {
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
    refetchInterval: 30_000,
  });
}

export function usePodMetrics(clusterId: string | null, namespace: string | null) {
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
    refetchInterval: 30_000,
  });
}
