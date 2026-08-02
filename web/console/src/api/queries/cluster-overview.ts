import { useQuery } from "@tanstack/react-query";

import { api, unwrap } from "../client";
import { queryKeys } from "../query-keys";

/**
 * Aggregated counts and capacity for one Cluster.
 *
 * The Server reads each section separately and pages each one, so this is an
 * eventually-consistent snapshot rather than one atomic Kubernetes read. A
 * section that fails or hits its item ceiling does not fail the request: the
 * response comes back with `partial` set and the affected sections named in
 * `issues`, which is what the page has to surface rather than quietly showing
 * numbers that are too low.
 */
export function useClusterOverview(clusterId: string | null) {
  return useQuery({
    queryKey: queryKeys.clusterOverview(clusterId ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster_id}/overview", {
          params: { path: { cluster_id: clusterId as string } },
          signal,
        }),
      ),
    enabled: Boolean(clusterId),
  });
}
