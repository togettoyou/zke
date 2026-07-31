import { useQuery } from "@tanstack/react-query";

import { api, unwrap } from "../client";
import { queryKeys } from "../query-keys";
import type { KubernetesWorkloadResource } from "../types";

export type WorkloadListParams = {
  limit?: number;
  continue?: string;
  label_selector?: string;
  field_selector?: string;
};

/**
 * Reads one kind of workload out of one Namespace of one Cluster.
 *
 * Cluster, Namespace and workload type are all path segments rather than
 * filters, so a query can never widen itself: there is no request this hook can
 * make that spans Namespaces or mixes Deployments with Jobs. Writes are not
 * here — creating, patching and deleting a workload go through the controlled
 * generic resource endpoints, the same route the Node cordon patch takes.
 */
export function useWorkloads(
  clusterId: string | null,
  namespace: string | null,
  resource: KubernetesWorkloadResource,
  params: WorkloadListParams = {},
) {
  return useQuery({
    queryKey: queryKeys.workloads(clusterId ?? "", namespace ?? "", resource, params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}",
          {
            params: {
              path: {
                cluster_id: clusterId as string,
                namespace_name: namespace as string,
                workload_resource: resource,
              },
              query: params,
            },
            signal,
          },
        ),
      ),
    enabled: Boolean(clusterId && namespace),
    // Preserve a page only while paging the same scoped collection. Reusing a
    // Deployment page after changing Cluster, Namespace or workload type would
    // present old objects under a new scope until the replacement request ends.
    placeholderData: (previous, previousQuery) => {
      const previousKey = previousQuery?.queryKey;
      return previousKey?.[1] === clusterId &&
        previousKey[2] === namespace &&
        previousKey[3] === resource
        ? previous
        : undefined;
    },
  });
}

export function useWorkload(
  clusterId: string | null,
  namespace: string | null,
  resource: KubernetesWorkloadResource,
  name: string | null,
) {
  return useQuery({
    queryKey: queryKeys.workload(clusterId ?? "", namespace ?? "", resource, name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}",
          {
            params: {
              path: {
                cluster_id: clusterId as string,
                namespace_name: namespace as string,
                workload_resource: resource,
                workload_name: name as string,
              },
            },
            signal,
          },
        ),
      ),
    enabled: Boolean(clusterId && namespace && name),
  });
}
