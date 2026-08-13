import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, csrfHeaders, idempotentHeaders, unwrap } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";
import type { ClusterEnrollmentRecord, EnrollmentStatus, ListParams, Pagination } from "../types";

export type EnrollmentListResult = {
  cluster_enrollments: ClusterEnrollmentRecord[];
  pagination: Pagination;
};

type EnrollmentListParams = ListParams & { status?: EnrollmentStatus };

export function useClusterEnrollments(projectId: string | null, params: EnrollmentListParams = {}) {
  return useQuery({
    queryKey: queryKeys.enrollments(projectId ?? "", params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/projects/{project_id}/cluster-enrollments", {
          signal,
          params: { path: { project_id: projectId as string }, query: params },
        }),
      ) as EnrollmentListResult,
    enabled: Boolean(projectId),
    placeholderData: (previous) => previous,
  });
}

/**
 * Creates a one-time enrollment credential. The response contains the plaintext
 * token exactly once: it is shown behind an explicit reveal and never cached,
 * persisted or logged.
 */
export function useCreateClusterEnrollment() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      projectId: string;
      clusterName: string;
      endpointProfileId: string;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.POST("/api/v1/projects/{project_id}/cluster-enrollments", {
          params: {
            path: { project_id: input.projectId },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: { cluster_name: input.clusterName, endpoint_profile_id: input.endpointProfileId },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.enrollments });
    },
  });
}

export function useRevokeClusterEnrollment() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { projectId: string; enrollmentId: string }) =>
      unwrap(
        await api.DELETE("/api/v1/projects/{project_id}/cluster-enrollments/{enrollment_id}", {
          params: {
            path: { project_id: input.projectId, enrollment_id: input.enrollmentId },
            header: csrfHeaders(),
          },
          body: { confirm: true },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.enrollments });
    },
  });
}

/**
 * Creates a one-time manifest credential. The Console constructs the command
 * against its current same-origin URL so reverse proxies need no Server config.
 */
export function useCreateClusterInstallation() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      projectId: string;
      clusterName: string;
      endpointProfileId: string;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.POST("/api/v1/projects/{project_id}/cluster-installations", {
          params: {
            path: { project_id: input.projectId },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: { cluster_name: input.clusterName, endpoint_profile_id: input.endpointProfileId },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.enrollments });
    },
  });
}
