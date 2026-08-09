import { useMutation, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeyPrefixes } from "../query-keys";

export function useCreatePodAccessSession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      namespace: string;
      podName: string;
      uid: string;
      port: number;
      sessionDurationSeconds: 900 | 1800 | 3600;
      replaceExisting: boolean;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.POST(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}/access-sessions",
          {
            params: {
              path: {
                cluster_id: input.clusterId,
                namespace_name: input.namespace,
                pod_name: input.podName,
              },
              header: idempotentHeaders(input.idempotencyKey),
            },
            body: {
              uid: input.uid,
              port: input.port,
              session_duration_seconds: input.sessionDurationSeconds,
              replace_existing: input.replaceExisting,
              confirm: true,
            },
          },
        ),
      ),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents }),
  });
}
