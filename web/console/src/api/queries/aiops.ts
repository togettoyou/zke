import { useCallback, useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, csrfHeaders, raiseResponseError, unwrap, unwrapEmpty } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";
import type {
  AIAttachment,
  AIContextUsage,
  AIEvidence,
  AISession,
  AITool,
  AITrajectoryEntry,
} from "../types";

type SessionList = { sessions: AISession[] };
type Trajectory = { entries: AITrajectoryEntry[] };
type Attachments = { attachments: AIAttachment[] };
type Tools = { enabled: boolean; tools: AITool[] };

/**
 * Whether AIOps is switched on for this deployment, and the tool catalogue the
 * runtime advertises to the model.
 *
 * It describes the runtime rather than any Cluster, so it is fetched once and
 * shared: what AIOps can do does not change with the workspace, only whether
 * the operator holds the permissions each tool needs.
 *
 * The launcher reads `enabled` from here rather than from the platform
 * settings, which only a global administrator may read: whether an application
 * appears on the desktop cannot depend on a route most of its users are
 * refused.
 */
export function useAITools(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: queryKeys.aiTools(),
    queryFn: async ({ signal }) => unwrap(await api.GET("/api/v1/ai/tools", { signal })) as Tools,
    staleTime: 5 * 60 * 1_000,
    // The launcher asks this on every desktop, for every operator. Passing the
    // permission in keeps the request off the accounts that could not start a
    // session anyway, while still sharing one cached answer with the
    // application itself.
    enabled: options?.enabled ?? true,
  });
}

export function useAISessions(
  tenantId: string | null,
  projectId: string | null,
  clusterId: string | null,
  search: string,
  archived: boolean,
) {
  const params = {
    tenant_id: tenantId as string,
    project_id: projectId as string,
    cluster_id: clusterId as string,
    search: search || undefined,
    archived,
    limit: 100,
  };
  return useQuery({
    queryKey: queryKeys.aiSessions(params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/ai/sessions", { params: { query: params }, signal }),
      ) as SessionList,
    enabled: Boolean(tenantId && projectId && clusterId),
  });
}

export function useCreateAISession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      tenantId: string;
      projectId: string;
      clusterId: string;
      title: string;
      approvalMode?: AISession["approval_mode"];
    }) =>
      unwrap(
        await api.POST("/api/v1/ai/sessions", {
          params: { header: csrfHeaders() },
          body: {
            tenant_id: input.tenantId,
            project_id: input.projectId,
            cluster_id: input.clusterId,
            title: input.title,
            approval_mode: input.approvalMode ?? "ask",
          },
        }),
      ) as AISession,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.aiSessions });
    },
  });
}

export function useUpdateAISession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      sessionId: string;
      title?: string;
      archived?: boolean;
      approvalMode?: AISession["approval_mode"];
    }) =>
      unwrap(
        await api.PATCH("/api/v1/ai/sessions/{session_id}", {
          params: { path: { session_id: input.sessionId }, header: csrfHeaders() },
          body: {
            ...(input.title !== undefined ? { title: input.title } : {}),
            ...(input.archived !== undefined ? { archived: input.archived } : {}),
            ...(input.approvalMode !== undefined ? { approval_mode: input.approvalMode } : {}),
          },
        }),
      ) as AISession,
    onSuccess: async (session) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.aiSessions }),
        queryClient.invalidateQueries({ queryKey: queryKeys.aiSession(session.id) }),
      ]);
    },
  });
}

export function useDeleteAISession() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (sessionId: string) =>
      unwrapEmpty(
        await api.DELETE("/api/v1/ai/sessions/{session_id}", {
          params: { path: { session_id: sessionId }, header: csrfHeaders() },
        }),
      ),
    onSuccess: async (_none, sessionId) => {
      queryClient.removeQueries({ queryKey: queryKeys.aiSession(sessionId) });
      queryClient.removeQueries({ queryKey: queryKeys.aiTrajectory(sessionId) });
      queryClient.removeQueries({ queryKey: queryKeys.aiAttachments(sessionId) });
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.aiSessions });
    },
  });
}

export function useAITrajectory(sessionId: string | null) {
  return useQuery({
    queryKey: queryKeys.aiTrajectory(sessionId ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/ai/sessions/{session_id}/trajectory", {
          params: {
            path: { session_id: sessionId as string },
            query: { after_sequence: 0, limit: 500 },
          },
          signal,
        }),
      ) as Trajectory,
    enabled: Boolean(sessionId),
  });
}

/**
 * How much of the model's context window this session currently occupies.
 *
 * Computed by the Server rather than by the browser: the pressure that matters
 * is the one the loop measures before every request — the endpoint's own
 * reported usage where it exists, the local heuristic only for what has been
 * appended since — and a second implementation here would drift from it the
 * first time either side changed.
 *
 * A deployment whose model endpoint is not configured answers 409, and the
 * meter simply does not appear. That is not worth retrying.
 */
export function useAIContextUsage(sessionId: string | null, revision: string) {
  return useQuery({
    queryKey: [...queryKeys.aiContextUsage(sessionId ?? ""), revision],
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/ai/sessions/{session_id}/context", {
          params: { path: { session_id: sessionId as string } },
          signal,
        }),
      ) as AIContextUsage,
    enabled: Boolean(sessionId),
    retry: false,
    // Keyed by the caller's revision rather than refetched on a timer: measuring
    // means replaying a whole trajectory on the Server, and asking for that once
    // per appended entry would spend a request per tool result to move a figure
    // nobody is reading mid-step.
    staleTime: Number.POSITIVE_INFINITY,
    // The previous reading stays on screen while the next one is fetched, so
    // the ring does not blink out between steps of a running turn.
    placeholderData: (previous) => previous,
  });
}

export function useStartAITurn() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      sessionId: string;
      text: string;
      attachmentIds: string[];
      evidence?: AIEvidence[];
    }) =>
      unwrap(
        await api.POST("/api/v1/ai/sessions/{session_id}/turns", {
          params: { path: { session_id: input.sessionId }, header: csrfHeaders() },
          body: { text: input.text, attachment_ids: input.attachmentIds, evidence: input.evidence },
        }),
      ) as AITrajectoryEntry,
    onSuccess: async (entry, input) => {
      queryClient.setQueryData<Trajectory>(queryKeys.aiTrajectory(input.sessionId), (current) => ({
        entries: mergeEntries(current?.entries ?? [], [entry]),
      }));
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.aiSessions });
    },
  });
}

export function useCancelAITurn() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (sessionId: string) =>
      unwrapEmpty(
        await api.DELETE("/api/v1/ai/sessions/{session_id}/turns/current", {
          params: { path: { session_id: sessionId }, header: csrfHeaders() },
        }),
      ),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.aiSessions });
    },
  });
}

/**
 * Answers one tool call the runtime parked on a person.
 *
 * Nothing is written into the cache here on purpose: the decision only becomes
 * true once the runtime has recorded it, and that record arrives on the stream
 * like every other entry. An optimistic answer would show an approval the
 * runtime may have already timed out of.
 */
export function useDecideAIApproval() {
  return useMutation({
    mutationFn: async (input: {
      sessionId: string;
      callId: string;
      decision: "approved" | "denied";
    }) =>
      unwrapEmpty(
        await api.POST("/api/v1/ai/sessions/{session_id}/approvals", {
          params: { path: { session_id: input.sessionId }, header: csrfHeaders() },
          body: { call_id: input.callId, decision: input.decision },
        }),
      ),
  });
}

export function useAIAttachments(sessionId: string | null) {
  return useQuery({
    queryKey: queryKeys.aiAttachments(sessionId ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/ai/sessions/{session_id}/attachments", {
          params: { path: { session_id: sessionId as string } },
          signal,
        }),
      ) as Attachments,
    enabled: Boolean(sessionId),
  });
}

export function useCreateAIAttachment() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      sessionId: string;
      name: string;
      mediaType: "text/plain" | "text/markdown" | "application/json" | "application/yaml";
      content: string;
    }) =>
      unwrap(
        await api.POST("/api/v1/ai/sessions/{session_id}/attachments", {
          params: { path: { session_id: input.sessionId }, header: csrfHeaders() },
          body: { name: input.name, media_type: input.mediaType, content: input.content },
        }),
      ) as AIAttachment,
    onSuccess: async (_attachment, input) => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.aiAttachments(input.sessionId) });
    },
  });
}

export function useDeleteAIAttachment() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { sessionId: string; attachmentId: string }) =>
      unwrapEmpty(
        await api.DELETE("/api/v1/ai/sessions/{session_id}/attachments/{attachment_id}", {
          params: {
            path: { session_id: input.sessionId, attachment_id: input.attachmentId },
            header: csrfHeaders(),
          },
        }),
      ),
    onSuccess: async (_none, input) => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.aiAttachments(input.sessionId) });
    },
  });
}

export async function downloadAISession(sessionId: string): Promise<void> {
  const response = await fetch(`/api/v1/ai/sessions/${encodeURIComponent(sessionId)}/export`, {
    credentials: "same-origin",
  });
  if (!response.ok) await raiseResponseError(response);
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = `aiops-${sessionId}.json`;
  anchor.click();
  URL.revokeObjectURL(url);
}

export type AIStreamState = "connecting" | "open" | "reconnecting" | "closed";

/**
 * What the model is producing right now, before the step it belongs to is
 * durable.
 *
 * Held in component state rather than in the query cache because it is not a
 * record: the moment the step is written, the `model` entry replaces it, and a
 * reconnect starts this empty and replays nothing.
 */
export type AILiveOutput = {
  turn: number;
  step: number;
  text: string;
  reasoning: string;
};

const emptyLiveOutput: AILiveOutput = { turn: 0, step: 0, text: "", reasoning: "" };

export type AIStream = {
  state: AIStreamState;
  live: AILiveOutput;
};

export function useAIEventStream(sessionId: string | null, enabled: boolean): AIStream {
  const queryClient = useQueryClient();
  const [state, setState] = useState<AIStreamState>("closed");
  const [live, setLive] = useState<AILiveOutput>(emptyLiveOutput);
  const lastSequence = useRef(0);

  // A durable entry always supersedes whatever was being typed for that step:
  // it is the same output, recorded. Clearing here rather than on a timer is
  // what stops the answer appearing twice for a frame.
  const clearLive = useCallback((step: number) => {
    setLive((current) => (step === 0 || current.step === step ? emptyLiveOutput : current));
  }, []);

  useEffect(() => {
    if (!sessionId || !enabled) return;
    let stopped = false;
    let source: EventSource | null = null;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let backoff = 1_000;
    const connect = () => {
      if (stopped) return;
      setState(lastSequence.current ? "reconnecting" : "connecting");
      source = new EventSource(
        `/api/v1/ai/sessions/${encodeURIComponent(sessionId)}/events?after_sequence=${lastSequence.current}`,
        { withCredentials: true },
      );
      source.addEventListener("ready", () => {
        backoff = 1_000;
        setState("open");
      });
      source.addEventListener("trajectory", (event) => {
        try {
          const entry = JSON.parse((event as MessageEvent<string>).data) as AITrajectoryEntry;
          lastSequence.current = Math.max(lastSequence.current, entry.sequence);
          queryClient.setQueryData<Trajectory>(queryKeys.aiTrajectory(sessionId), (current) => ({
            entries: mergeEntries(current?.entries ?? [], [entry]),
          }));
          if (entry.kind === "model" || entry.kind === "error" || entry.kind === "conclusion") {
            clearLive(entry.kind === "model" ? (entry.content.step ?? 0) : 0);
          }
          void queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.aiSessions });
        } catch {
          // The next durable replay repairs a malformed frame.
        }
      });
      // The session record, not an entry: its title changes when the first turn
      // names the conversation, and its status is what says the turn ended.
      source.addEventListener("session", (event) => {
        try {
          const session = JSON.parse((event as MessageEvent<string>).data) as AISession;
          queryClient.setQueryData<AISession>(queryKeys.aiSession(session.id), session);
          void queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.aiSessions });
        } catch {
          // A malformed frame is repaired by the next list refetch.
        }
      });
      source.addEventListener("delta", (event) => {
        try {
          const delta = JSON.parse((event as MessageEvent<string>).data) as {
            kind: "delta" | "reasoning" | "reset";
            turn: number;
            step: number;
            text: string;
          };
          // A retried request starts its answer over. Keeping what the failed
          // attempt had already typed would splice the beginning of one answer
          // onto the whole of another.
          if (delta.kind === "reset") {
            setLive((current) =>
              current.step === delta.step && current.turn === delta.turn
                ? { ...emptyLiveOutput, turn: delta.turn, step: delta.step }
                : current,
            );
            return;
          }
          setLive((current) => {
            const fresh = current.step === delta.step && current.turn === delta.turn;
            const base = fresh
              ? current
              : { ...emptyLiveOutput, turn: delta.turn, step: delta.step };
            return delta.kind === "reasoning"
              ? { ...base, reasoning: base.reasoning + delta.text }
              : { ...base, text: base.text + delta.text };
          });
        } catch {
          // A malformed delta costs a few characters of animation, nothing more.
        }
      });
      const reconnect = () => {
        source?.close();
        if (stopped || timer) return;
        setState("reconnecting");
        setLive(emptyLiveOutput);
        timer = setTimeout(() => {
          timer = null;
          connect();
        }, backoff);
        backoff = Math.min(backoff * 2, 30_000);
      };
      source.addEventListener("close", (event) => {
        try {
          const reason = (JSON.parse((event as MessageEvent<string>).data) as { reason?: string })
            .reason;
          if (reason === "unauthenticated" || reason === "forbidden") {
            stopped = true;
            source?.close();
            setState("closed");
            return;
          }
        } catch {
          // An unclassified close is recoverable from the durable sequence.
        }
        reconnect();
      });
      source.addEventListener("error", reconnect);
    };
    const cached = queryClient.getQueryData<Trajectory>(queryKeys.aiTrajectory(sessionId));
    lastSequence.current = cached?.entries.at(-1)?.sequence ?? 0;
    connect();
    return () => {
      stopped = true;
      source?.close();
      if (timer) clearTimeout(timer);
      setState("closed");
      setLive(emptyLiveOutput);
    };
  }, [clearLive, enabled, queryClient, sessionId]);
  return { state, live };
}

function mergeEntries(
  current: AITrajectoryEntry[],
  incoming: AITrajectoryEntry[],
): AITrajectoryEntry[] {
  const bySequence = new Map(current.map((entry) => [entry.sequence, entry]));
  for (const entry of incoming) bySequence.set(entry.sequence, entry);
  return [...bySequence.values()].sort((left, right) => left.sequence - right.sequence);
}
