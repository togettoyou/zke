import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";

export type NamespaceListParams = {
  limit?: number;
  continue?: string;
  label_selector?: string;
  field_selector?: string;
};

export function useNamespaces(clusterId: string | null, params: NamespaceListParams = {}) {
  return useQuery({
    queryKey: queryKeys.namespaces(clusterId ?? "", params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster_id}/namespaces", {
          params: {
            path: { cluster_id: clusterId as string },
            query: params,
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId),
    // Namespace names only have meaning inside their Cluster. Preserve the
    // previous page for paging within one Cluster, never across Clusters.
    placeholderData: (previous, previousQuery) =>
      previousQuery?.queryKey[1] === clusterId ? previous : undefined,
  });
}

export function useNamespace(clusterId: string | null, name: string | null) {
  return useQuery({
    queryKey: queryKeys.namespace(clusterId ?? "", name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}", {
          params: {
            path: {
              cluster_id: clusterId as string,
              namespace_name: name as string,
            },
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId && name),
  });
}

export function useCreateNamespace() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      name: string;
      labels?: Record<string, string>;
      dryRun: boolean;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.POST("/api/v1/clusters/{cluster_id}/namespaces", {
          params: {
            path: { cluster_id: input.clusterId },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            name: input.name,
            labels: input.labels,
            dry_run: input.dryRun,
            confirm: !input.dryRun,
          },
        }),
      ),
    onSuccess: async (data, variables) => {
      await queryClient.invalidateQueries({
        queryKey: queryKeyPrefixes.auditEvents,
      });
      if (!variables.dryRun) {
        await Promise.all([
          queryClient.invalidateQueries({
            queryKey: ["namespaces", variables.clusterId],
          }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.namespace(variables.clusterId, data.namespace.name),
          }),
        ]);
      }
    },
  });
}

/**
 * Deletes by name pinned to a UID.
 *
 * The endpoint also accepts a `resource_version` precondition, which this hook
 * deliberately does not send. What a caller has on hand is the version from a
 * list snapshot, and any unrelated change to the Namespace between reading that
 * list and confirming the deletion would turn it into a conflict. UID is what
 * pins the identity, so it is what rules out deleting a same-named object that
 * was recreated in the meantime.
 */
export function useDeleteNamespace() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      name: string;
      uid?: string;
      dryRun: boolean;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.DELETE("/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}", {
          params: {
            path: {
              cluster_id: input.clusterId,
              namespace_name: input.name,
            },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            dry_run: input.dryRun,
            confirm: !input.dryRun,
            uid: input.uid,
          },
        }),
      ),
    onSuccess: async (_data, variables) => {
      await queryClient.invalidateQueries({
        queryKey: queryKeyPrefixes.auditEvents,
      });
      if (!variables.dryRun) {
        queryClient.removeQueries({
          queryKey: queryKeys.namespace(variables.clusterId, variables.name),
        });
        await queryClient.invalidateQueries({
          queryKey: ["namespaces", variables.clusterId],
        });
      }
    },
  });
}
