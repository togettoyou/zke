import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { csrfHeaders, idempotentHeaders } from "@/api/client";
import type { CustomApplicationRequest } from "@/api/types";

import { api, unwrap } from "../client";
import { queryKeys } from "../query-keys";

export function useCustomApplications(projectId: string | null, enabled = true) {
  return useQuery({
    queryKey: queryKeys.customApplications(projectId ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/projects/{project_id}/custom-applications", {
          params: { path: { project_id: projectId as string } },
          signal,
        }),
      ),
    enabled: Boolean(projectId) && enabled,
  });
}

export function useCreateCustomApplication() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      projectId: string;
      application: CustomApplicationRequest;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.POST("/api/v1/projects/{project_id}/custom-applications", {
          params: {
            path: { project_id: input.projectId },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: input.application,
        }),
      ),
    onSuccess: async (_data, variables) => {
      await queryClient.invalidateQueries({
        queryKey: queryKeys.customApplications(variables.projectId),
      });
    },
  });
}

export function useUpdateCustomApplication() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      projectId: string;
      applicationId: string;
      application: CustomApplicationRequest;
    }) =>
      unwrap(
        await api.PUT("/api/v1/projects/{project_id}/custom-applications/{application_id}", {
          params: {
            path: { project_id: input.projectId, application_id: input.applicationId },
            header: csrfHeaders(),
          },
          body: input.application,
        }),
      ),
    onSuccess: async (_data, variables) => {
      await queryClient.invalidateQueries({
        queryKey: queryKeys.customApplications(variables.projectId),
      });
    },
  });
}

export function useDeleteCustomApplication() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { projectId: string; applicationId: string }) =>
      unwrap(
        await api.DELETE("/api/v1/projects/{project_id}/custom-applications/{application_id}", {
          params: {
            path: { project_id: input.projectId, application_id: input.applicationId },
            header: csrfHeaders(),
          },
        }),
      ),
    onSuccess: async (_data, variables) => {
      await queryClient.invalidateQueries({
        queryKey: queryKeys.customApplications(variables.projectId),
      });
    },
  });
}
