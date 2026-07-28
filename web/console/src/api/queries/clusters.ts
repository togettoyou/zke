import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { fetchAllPages, mapWithConcurrency } from "@/lib/concurrency";

import { api, csrfHeaders, idempotentHeaders, unwrap } from "../client";
import { errorMessage } from "../errors";
import { queryKeys, queryKeyPrefixes } from "../query-keys";
import type {
  ClusterAggregate,
  ClusterStatus,
  ListParams,
  Pagination,
  Project,
  Tenant,
} from "../types";

export type ClusterListResult = { clusters: ClusterAggregate[]; pagination: Pagination };

type ClusterListParams = ListParams & { status?: ClusterStatus };

const AGGREGATION_CONCURRENCY = 4;
const AGGREGATION_PAGE_SIZE = 100;
const AGGREGATION_MAX_PAGES = 20;

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

export type ClusterOverviewEntry = {
  cluster: ClusterAggregate;
  tenantId: string;
  tenantName: string;
  projectId: string;
  projectName: string;
};

export type ClusterOverviewFailure = {
  scope: string;
  message: string;
};

export type ClusterOverview = {
  entries: ClusterOverviewEntry[];
  failures: ClusterOverviewFailure[];
  truncated: boolean;
};

/**
 * Cross-cluster overview.
 *
 * The Phase 1 contract only exposes clusters per Project, so the global view is
 * assembled client side by walking the visible Tenants and Projects. Anything
 * that fails or is truncated is reported explicitly — a partial aggregate must
 * not be presented as the complete picture.
 */
export function useClusterOverview(enabled = true) {
  return useQuery({
    queryKey: queryKeys.clusterOverview(),
    enabled,
    staleTime: 30_000,
    queryFn: async ({ signal }): Promise<ClusterOverview> => {
      const failures: ClusterOverviewFailure[] = [];
      let truncated = false;

      const tenantsResult = await fetchAllPages<Tenant>(
        AGGREGATION_PAGE_SIZE,
        AGGREGATION_MAX_PAGES,
        async (offset, limit) => {
          const page = unwrap(
            await api.GET("/api/v1/tenants", {
              params: { query: { limit, offset } },
              signal,
            }),
          );
          return { items: page.tenants, hasMore: page.pagination.has_more };
        },
      );
      truncated ||= tenantsResult.truncated;

      const projectGroups = await mapWithConcurrency(
        tenantsResult.items,
        AGGREGATION_CONCURRENCY,
        async (tenant) => {
          try {
            const projects = await fetchAllPages<Project>(
              AGGREGATION_PAGE_SIZE,
              AGGREGATION_MAX_PAGES,
              async (offset, limit) => {
                const page = unwrap(
                  await api.GET("/api/v1/tenants/{tenant_id}/projects", {
                    params: { path: { tenant_id: tenant.id }, query: { limit, offset } },
                    signal,
                  }),
                );
                return { items: page.projects, hasMore: page.pagination.has_more };
              },
            );
            truncated ||= projects.truncated;
            return projects.items.map((project) => ({ tenant, project }));
          } catch (error) {
            failures.push({ scope: `租户 ${tenant.name}`, message: errorMessage(error) });
            return [];
          }
        },
      );

      const clusterGroups = await mapWithConcurrency(
        projectGroups.flat(),
        AGGREGATION_CONCURRENCY,
        async ({ tenant, project }) => {
          try {
            const clusters = await fetchAllPages<ClusterAggregate>(
              AGGREGATION_PAGE_SIZE,
              AGGREGATION_MAX_PAGES,
              async (offset, limit) => {
                const page = unwrap(
                  await api.GET("/api/v1/projects/{project_id}/clusters", {
                    params: { path: { project_id: project.id }, query: { limit, offset } },
                    signal,
                  }),
                );
                return { items: page.clusters, hasMore: page.pagination.has_more };
              },
            );
            truncated ||= clusters.truncated;
            return clusters.items.map((cluster) => ({
              cluster,
              tenantId: tenant.id,
              tenantName: tenant.name,
              projectId: project.id,
              projectName: project.name,
            }));
          } catch (error) {
            // A Project the user cannot read is not an error worth surfacing;
            // anything else is reported so the view is never silently partial.
            failures.push({
              scope: `${tenant.name} / ${project.name}`,
              message: errorMessage(error),
            });
            return [];
          }
        },
      );

      return { entries: clusterGroups.flat(), failures, truncated };
    },
  });
}

export function useUpdateCluster() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { clusterId: string; name: string }) =>
      unwrap(
        await api.PUT("/api/v1/clusters/{cluster_id}", {
          params: { path: { cluster_id: input.clusterId }, header: csrfHeaders() },
          body: { name: input.name },
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
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.clusterOverview }),
  ]);
}
