import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, csrfHeaders, idempotentHeaders, unwrap } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";
import type { ListParams, Pagination, Project, ResourceStatus, Tenant } from "../types";

export type TenantListResult = { tenants: Tenant[]; pagination: Pagination };
export type ProjectListResult = { projects: Project[]; pagination: Pagination };

type ResourceListParams = ListParams & { status?: ResourceStatus };

export function useTenants(params: ResourceListParams = {}, enabled = true) {
  return useQuery({
    queryKey: queryKeys.tenants(params),
    queryFn: async ({ signal }) =>
      unwrap(await api.GET("/api/v1/tenants", { params: { query: params }, signal })),
    enabled,
    placeholderData: (previous) => previous,
  });
}

export function useTenant(tenantId: string | null) {
  return useQuery({
    queryKey: queryKeys.tenant(tenantId ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/tenants/{tenant_id}", {
          params: { path: { tenant_id: tenantId as string } },
          signal,
        }),
      ),
    enabled: Boolean(tenantId),
  });
}

export function useCreateTenant() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { name: string; idempotencyKey: string }) =>
      unwrap(
        await api.POST("/api/v1/tenants", {
          params: { header: idempotentHeaders(input.idempotencyKey) },
          body: { name: input.name },
        }),
      ),
    onSuccess: async () => {
      await Promise.all([queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.tenants })]);
    },
  });
}

export function useUpdateTenant() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { tenantId: string; name: string; status: ResourceStatus }) =>
      unwrap(
        await api.PUT("/api/v1/tenants/{tenant_id}", {
          params: { path: { tenant_id: input.tenantId }, header: csrfHeaders() },
          // Suspending a Tenant is a sensitive change, so the Server requires an
          // explicit confirmation flag alongside the new state.
          body: { name: input.name, status: input.status, confirm: input.status === "suspended" },
        }),
      ),
    onSuccess: async (_data, variables) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.tenants }),
        queryClient.invalidateQueries({ queryKey: queryKeys.tenant(variables.tenantId) }),
        queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.projects }),
      ]);
    },
  });
}

export function useDeleteTenant() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { tenantId: string }) =>
      unwrap(
        await api.DELETE("/api/v1/tenants/{tenant_id}", {
          params: { path: { tenant_id: input.tenantId }, header: csrfHeaders() },
          body: { confirm: true },
        }),
      ),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.tenants }),
        queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.projects }),
        queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.clusters }),
      ]);
    },
  });
}

export function useProjects(tenantId: string | null, params: ResourceListParams = {}) {
  return useQuery({
    queryKey: queryKeys.projects(tenantId ?? "", params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/tenants/{tenant_id}/projects", {
          params: { path: { tenant_id: tenantId as string }, query: params },
          signal,
        }),
      ),
    enabled: Boolean(tenantId),
    placeholderData: (previous) => previous,
  });
}

export function useProject(projectId: string | null) {
  return useQuery({
    queryKey: queryKeys.project(projectId ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/projects/{project_id}", {
          params: { path: { project_id: projectId as string } },
          signal,
        }),
      ),
    enabled: Boolean(projectId),
  });
}

export function useCreateProject() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { tenantId: string; name: string; idempotencyKey: string }) =>
      unwrap(
        await api.POST("/api/v1/tenants/{tenant_id}/projects", {
          params: {
            path: { tenant_id: input.tenantId },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: { name: input.name },
        }),
      ),
    onSuccess: async () => {
      await Promise.all([queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.projects })]);
    },
  });
}

export function useUpdateProject() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { projectId: string; name: string; status: ResourceStatus }) =>
      unwrap(
        await api.PUT("/api/v1/projects/{project_id}", {
          params: { path: { project_id: input.projectId }, header: csrfHeaders() },
          body: { name: input.name, status: input.status, confirm: input.status === "suspended" },
        }),
      ),
    onSuccess: async (_data, variables) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.projects }),
        queryClient.invalidateQueries({ queryKey: queryKeys.project(variables.projectId) }),
      ]);
    },
  });
}

export function useDeleteProject() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { projectId: string }) =>
      unwrap(
        await api.DELETE("/api/v1/projects/{project_id}", {
          params: { path: { project_id: input.projectId }, header: csrfHeaders() },
          body: { confirm: true },
        }),
      ),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.projects }),
        queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.clusters }),
      ]);
    },
  });
}
