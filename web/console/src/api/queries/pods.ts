import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";

export type PodListParams = {
  limit?: number;
  continue?: string;
  label_selector?: string;
  field_selector?: string;
};

/**
 * Reads the Pods of one Namespace of one Cluster.
 *
 * Both are path segments rather than filters, so a query cannot widen itself to
 * another Namespace. Logs, exec and eviction are not here and are not available:
 * they are Kubernetes subresources, which the Resource protocol rejects.
 */
export function usePods(
  clusterId: string | null,
  namespace: string | null,
  params: PodListParams = {},
) {
  return useQuery({
    queryKey: queryKeys.pods(clusterId ?? "", namespace ?? "", params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods", {
          params: {
            path: {
              cluster_id: clusterId as string,
              namespace_name: namespace as string,
            },
            query: params,
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId && namespace),
    // Hold a page only while paging the same scoped collection: reusing one
    // Namespace's Pods under another would show the wrong objects until the
    // replacement request lands.
    placeholderData: (previous, previousQuery) => {
      const previousKey = previousQuery?.queryKey;
      return previousKey?.[1] === clusterId && previousKey[2] === namespace ? previous : undefined;
    },
  });
}

export function usePod(clusterId: string | null, namespace: string | null, name: string | null) {
  return useQuery({
    queryKey: queryKeys.pod(clusterId ?? "", namespace ?? "", name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}", {
          params: {
            path: {
              cluster_id: clusterId as string,
              namespace_name: namespace as string,
              pod_name: name as string,
            },
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId && namespace && name),
  });
}

/**
 * Deletes a Pod, pinned to the UID the caller read it at.
 *
 * The endpoint requires the UID, so a Pod its controller recreated under the
 * same name between the read and the confirmation cannot be the one deleted —
 * which matters more here than anywhere else, because recreating deleted Pods is
 * precisely what those controllers do. `resource_version` is deliberately not
 * sent: a Pod's status changes constantly, and any of those updates would turn
 * an intended deletion into a conflict.
 *
 * This is a delete, not an eviction: it does not consult PodDisruptionBudgets.
 */
export function useDeletePod() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      namespace: string;
      name: string;
      uid: string;
      dryRun: boolean;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.DELETE(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}",
          {
            params: {
              path: {
                cluster_id: input.clusterId,
                namespace_name: input.namespace,
                pod_name: input.name,
              },
              header: idempotentHeaders(input.idempotencyKey),
            },
            body: { dry_run: input.dryRun, confirm: !input.dryRun, uid: input.uid },
          },
        ),
      ),
    onSuccess: async (_data, variables) => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
      if (variables.dryRun) {
        return;
      }
      queryClient.removeQueries({
        queryKey: queryKeys.pod(variables.clusterId, variables.namespace, variables.name),
      });
      await queryClient.invalidateQueries({
        queryKey: ["pods", variables.clusterId, variables.namespace],
      });
    },
  });
}
