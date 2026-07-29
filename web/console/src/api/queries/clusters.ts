import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, csrfHeaders, idempotentHeaders, unwrap } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";
import type {
  ClusterAggregate,
  ClusterLifecycleStatus,
  ClusterStatus,
  ListParams,
  Pagination,
} from "../types";

export type ClusterListResult = { clusters: ClusterAggregate[]; pagination: Pagination };

type ClusterListParams = ListParams & { status?: ClusterStatus };

export function useClusters(projectId: string | null, params: ClusterListParams = {}) {
  return useQuery({
    queryKey: queryKeys.clusters(projectId ?? "", params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/projects/{project_id}/clusters", {
          params: { path: { project_id: projectId as string }, query: params },
          signal,
        }),
      ),
    enabled: Boolean(projectId),
    placeholderData: (previous) => previous,
  });
}

export function useCluster(clusterId: string | null) {
  return useQuery({
    queryKey: queryKeys.cluster(clusterId ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster_id}", {
          params: { path: { cluster_id: clusterId as string } },
          signal,
        }),
      ),
    enabled: Boolean(clusterId),
  });
}

export function useUpdateCluster() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      name: string;
      status: ClusterLifecycleStatus;
    }) =>
      unwrap(
        await api.PUT("/api/v1/clusters/{cluster_id}", {
          params: { path: { cluster_id: input.clusterId }, header: csrfHeaders() },
          // Suspending is the sensitive direction, so it carries the explicit
          // confirmation the Server requires; resuming does not.
          body: {
            name: input.name,
            status: input.status,
            confirm: input.status === "suspended",
          },
        }),
      ),
    onSuccess: async (_data, variables) => {
      await invalidateClusterViews(queryClient, variables.clusterId);
    },
  });
}

export function useDeleteCluster() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { clusterId: string }) =>
      unwrap(
        await api.DELETE("/api/v1/clusters/{cluster_id}", {
          params: { path: { cluster_id: input.clusterId }, header: csrfHeaders() },
          body: { confirm: true },
        }),
      ),
    onSuccess: async (_data, variables) => {
      await invalidateClusterViews(queryClient, variables.clusterId);
    },
  });
}

export function useRevokeClusterConnection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { clusterId: string }) =>
      unwrap(
        await api.POST("/api/v1/clusters/{cluster_id}/connection/revoke", {
          params: { path: { cluster_id: input.clusterId }, header: csrfHeaders() },
          body: { confirm: true },
        }),
      ),
    onSuccess: async (_data, variables) => {
      await invalidateClusterViews(queryClient, variables.clusterId);
    },
  });
}

/** Issues a fresh one-time credential for an existing Cluster. */
export function useReenrollCluster() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { clusterId: string; idempotencyKey: string }) =>
      unwrap(
        await api.POST("/api/v1/clusters/{cluster_id}/connection/reenroll", {
          params: {
            path: { cluster_id: input.clusterId },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: { confirm: true },
        }),
      ),
    onSuccess: async (_data, variables) => {
      await Promise.all([
        invalidateClusterViews(queryClient, variables.clusterId),
        queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.enrollments }),
      ]);
    },
  });
}

async function invalidateClusterViews(
  queryClient: ReturnType<typeof useQueryClient>,
  clusterId: string,
): Promise<void> {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: queryKeys.cluster(clusterId) }),
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.clusters }),
  ]);
}
