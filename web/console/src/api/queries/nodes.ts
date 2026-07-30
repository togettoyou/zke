import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";

export type NodeListParams = {
  limit?: number;
  continue?: string;
  label_selector?: string;
  field_selector?: string;
};

export function useNodes(clusterId: string | null, params: NodeListParams = {}) {
  return useQuery({
    queryKey: queryKeys.nodes(clusterId ?? "", params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster_id}/nodes", {
          params: {
            path: { cluster_id: clusterId as string },
            query: params,
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId),
    placeholderData: (previous) => previous,
  });
}

export function useNode(clusterId: string | null, name: string | null) {
  return useQuery({
    queryKey: queryKeys.node(clusterId ?? "", name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster_id}/nodes/{node_name}", {
          params: {
            path: { cluster_id: clusterId as string, node_name: name as string },
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId && name),
  });
}

/**
 * Marks a Node schedulable or unschedulable — cordon and uncordon.
 *
 * There is no typed endpoint for this and none is needed: it is a merge patch of
 * `spec.unschedulable` on a primary resource, which the controlled generic CRUD
 * route already covers. The patch names only that one field, so it cannot carry
 * an unrelated change to the Node along with it.
 *
 * Draining is a different operation and is not available: it evicts Pods through
 * the `pods/eviction` subresource, which the Resource protocol rejects.
 */
export function useSetNodeSchedulable() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      name: string;
      unschedulable: boolean;
      dryRun: boolean;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.PATCH("/api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}", {
          params: {
            path: { cluster_id: input.clusterId, resource_name: input.name },
            query: { version: "v1", resource: "nodes" },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            patch_type: "merge",
            patch: { spec: { unschedulable: input.unschedulable } },
            options: { dry_run: input.dryRun, force: false },
            confirm: !input.dryRun,
          },
        }),
      ),
    onSuccess: async (_data, variables) => {
      await queryClient.invalidateQueries({
        queryKey: queryKeyPrefixes.auditEvents,
      });
      if (!variables.dryRun) {
        await Promise.all([
          queryClient.invalidateQueries({
            queryKey: ["nodes", variables.clusterId],
          }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.node(variables.clusterId, variables.name),
          }),
        ]);
      }
    },
  });
}
