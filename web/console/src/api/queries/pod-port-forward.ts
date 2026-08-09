import { useMutation, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeyPrefixes } from "../query-keys";

export const POD_PORT_FORWARD_SUBPROTOCOL = "zke.pod-port-forward.v1";

export type PodPortForwardStatus = {
  type: "exit";
  result: string;
  reason?: string;
  message?: string;
  client_bytes?: number;
  pod_bytes?: number;
  client_limit_reached?: boolean;
  pod_limit_reached?: boolean;
};

export function useCreatePodPortForwardSession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      namespace: string;
      podName: string;
      uid: string;
      port: number;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.POST(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}/port-forward-sessions",
          {
            params: {
              path: {
                cluster_id: input.clusterId,
                namespace_name: input.namespace,
                pod_name: input.podName,
              },
              header: idempotentHeaders(input.idempotencyKey),
            },
            body: { uid: input.uid, port: input.port, confirm: true },
          },
        ),
      ),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents }),
  });
}

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

export function podPortForwardSocketUrl(websocketPath: string): string {
  const url = new URL(websocketPath, location.origin);
  if (url.origin !== location.origin) {
    throw new Error("Pod port-forward WebSocket path is not same-origin");
  }
  url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
  return url.toString();
}
