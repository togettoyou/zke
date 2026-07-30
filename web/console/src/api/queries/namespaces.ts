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
    placeholderData: (previous) => previous,
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

export function useDeleteNamespace() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      name: string;
      uid?: string;
      resourceVersion?: string;
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
            resource_version: input.resourceVersion,
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
