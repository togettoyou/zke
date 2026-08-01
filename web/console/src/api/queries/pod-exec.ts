import { useMutation, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeyPrefixes } from "../query-keys";

/** The wire protocol the `zke.pod-exec.v1` subprotocol carries, as JSON frames. */
export const POD_EXEC_SUBPROTOCOL = "zke.pod-exec.v1";

// The Agent protocol accepts at most 32 KiB in one input frame. A terminal's
// onData callback usually emits a few bytes, but a paste can be arbitrarily
// large, so the browser must preserve the same frame boundary.
const MAX_INPUT_FRAME_BYTES = 32 * 1024;

/** What the browser sends. */
export type PodExecClientMessage =
  | { type: "stdin"; data: string }
  | { type: "resize"; columns: number; rows: number }
  | { type: "close_stdin" };

/** What the Server sends. */
export type PodExecServerMessage =
  | { type: "stdout" | "stderr"; data?: string }
  | {
      type: "exit";
      result?: string;
      exit_code?: number;
      reason?: string;
      message?: string;
      output_bytes?: number;
      output_limit_reached?: boolean;
    };

/**
 * Requests a one-shot ticket for a Pod terminal.
 *
 * The ticket is what the WebSocket presents; it expires in seconds, is bound to
 * this user, session, Cluster, Namespace, Pod, Pod UID and container, and can be
 * consumed once. The Console therefore mints one per connection attempt and
 * never stores it.
 */
export function useCreateTerminalSession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      namespace: string;
      podName: string;
      uid: string;
      container: string;
      columns: number;
      rows: number;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.POST(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}/terminal-sessions",
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
              container: input.container,
              columns: input.columns,
              rows: input.rows,
              confirm: true,
            },
          },
        ),
      ),
    // Opening a terminal is audited whether or not anything is typed into it.
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents }),
  });
}

/**
 * The same-origin WebSocket URL for a ticket.
 *
 * The Server hands back the path it wants used, so the scheme is all the client
 * decides — and it follows the page, which keeps a `https` Console from opening
 * an unencrypted socket.
 */
export function terminalSocketUrl(websocketPath: string): string {
  const url = new URL(websocketPath, location.origin);
  if (url.origin !== location.origin) {
    throw new Error("Pod terminal WebSocket path is not same-origin");
  }
  url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
  return url.toString();
}

/*
 * Terminal traffic is bytes, and JSON has no byte type: the Server marshals
 * `[]byte` as base64, so both directions are encoded here rather than sending
 * text and hoping every shell speaks UTF-8 in whole characters.
 */

function encodeTerminalBytes(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary);
}

export function encodeTerminalInput(text: string): string[] {
  const bytes = new TextEncoder().encode(text);
  const chunks: string[] = [];
  for (let offset = 0; offset < bytes.length; offset += MAX_INPUT_FRAME_BYTES) {
    chunks.push(encodeTerminalBytes(bytes.subarray(offset, offset + MAX_INPUT_FRAME_BYTES)));
  }
  return chunks;
}

export function decodeTerminalOutput(data: string): Uint8Array {
  const binary = atob(data);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes;
}
