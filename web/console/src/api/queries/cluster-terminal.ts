import { useMutation, useQueryClient } from "@tanstack/react-query";

import { idempotentHeaders, longRequestApi, unwrap } from "../client";
import { queryKeyPrefixes } from "../query-keys";

export function useCreateClusterTerminalSession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      columns: number;
      rows: number;
      idempotencyKey: string;
      signal: AbortSignal;
    }) =>
      unwrap(
        await longRequestApi.POST("/api/v1/clusters/{cluster_id}/terminal-sessions", {
          params: {
            path: { cluster_id: input.clusterId },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: { columns: input.columns, rows: input.rows, confirm: true },
          signal: input.signal,
        }),
      ),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents }),
  });
}
