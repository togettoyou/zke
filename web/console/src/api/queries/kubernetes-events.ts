import { useCallback, useEffect, useRef, useState } from "react";

import { raiseResponseError } from "../client";
import { errorCode } from "../errors";

/**
 * One Kubernetes Event, as the Server's SSE frames carry it.
 *
 * These are hand-written rather than generated: the OpenAPI contract types the
 * response as `text/event-stream`, so the frame payloads have no schema entry to
 * derive from. They mirror `kubernetesEventResponse` on the Server.
 */
export type KubernetesEventReference = {
  kind?: string;
  namespace?: string;
  name?: string;
  uid?: string;
  apiVersion?: string;
  fieldPath?: string;
};

export type KubernetesEventRecord = {
  watch_type: "ADDED" | "MODIFIED" | "DELETED" | "BOOKMARK";
  uid: string;
  name: string;
  namespace: string;
  type: string;
  reason: string;
  message: string;
  count: number;
  action?: string;
  reporting_controller?: string;
  reporting_instance?: string;
  regarding: KubernetesEventReference;
  related?: KubernetesEventReference;
  first_timestamp?: string;
  last_timestamp?: string;
  event_time?: string;
};

export type KubernetesEventFilters = {
  resourceUid?: string;
  resourceKind?: string;
  resourceName?: string;
  type?: "Normal" | "Warning";
  reason?: string;
};

export type KubernetesEventStreamOptions = {
  clusterId: string;
  /**
   * The Namespace to read, or an empty string for every Namespace in the
   * Cluster — the event centre an operator opens before they know which
   * Namespace the problem is in. Both routes answer to `cluster.event.read`,
   * which is granted per Cluster, so the wider one reaches nothing the
   * Namespace one could not already be pointed at.
   */
  namespace: string;
  follow: boolean;
  limit: number;
  filters: KubernetesEventFilters;
};

export type EventStreamStatus = "idle" | "loading" | "streaming" | "ended" | "error";

export type KubernetesEventStream = {
  events: KubernetesEventRecord[];
  status: EventStreamStatus;
  error: unknown;
  /** The Server's terminal reason from the in-body `close` frame, when it sent one. */
  closeReason: string | null;
  /** True when the Server capped the initial snapshot. */
  truncated: boolean;
  reload: () => void;
  stop: () => void;
};

/**
 * How many Events the client keeps.
 *
 * A followed stream has no end, and a busy Namespace produces Events faster than
 * anyone reads them. The Server bounds its snapshot; this bounds the table.
 */
const MAX_EVENTS = 1_000;

/** Delay between reconnections, so a Watch that closes immediately cannot spin. */
const RECONNECT_DELAY_MS = 1_000;

/** The terminal reasons the Server may report inside the `close` frame. */
type CloseFrame = { reason: string; last_resource_version?: string; limit_reached?: boolean };

function eventsUrl(options: KubernetesEventStreamOptions, resourceVersion?: string): string {
  const cluster = `/api/v1/clusters/${encodeURIComponent(options.clusterId)}`;
  const path = options.namespace
    ? `${cluster}/namespaces/${encodeURIComponent(options.namespace)}/events`
    : `${cluster}/events`;
  const query = new URLSearchParams({ limit: String(options.limit) });
  if (options.follow) {
    query.set("follow", "true");
  }
  if (resourceVersion) {
    // Resuming: the Server continues the Watch from here instead of replaying a
    // snapshot the caller already has.
    query.set("resource_version", resourceVersion);
    query.set("include_initial", "false");
  }
  const { filters } = options;
  if (filters.resourceUid) {
    query.set("resource_uid", filters.resourceUid);
  }
  if (filters.resourceKind) {
    query.set("resource_kind", filters.resourceKind);
  }
  if (filters.resourceName) {
    query.set("resource_name", filters.resourceName);
  }
  if (filters.type) {
    query.set("type", filters.type);
  }
  if (filters.reason) {
    query.set("reason", filters.reason);
  }
  return `${path}?${query.toString()}`;
}

/** When an Event last happened, for ordering. */
export function eventTimestamp(event: KubernetesEventRecord): string | undefined {
  return event.last_timestamp ?? event.event_time ?? event.first_timestamp;
}

function compareEvents(left: KubernetesEventRecord, right: KubernetesEventRecord): number {
  const leftTime = eventTimestamp(left) ?? "";
  const rightTime = eventTimestamp(right) ?? "";
  if (leftTime === rightTime) {
    return left.name.localeCompare(right.name);
  }
  return leftTime < rightTime ? 1 : -1;
}

/** One `event:`/`data:` block off the wire. */
type ParsedFrame = { event: string; data: string; id?: string };

function parseFrame(raw: string): ParsedFrame | null {
  let event = "message";
  let id: string | undefined;
  const data: string[] = [];
  for (const line of raw.split("\n")) {
    const trimmed = line.endsWith("\r") ? line.slice(0, -1) : line;
    // A comment line is the heartbeat: it keeps the connection warm and carries
    // nothing to parse.
    if (trimmed === "" || trimmed.startsWith(":")) {
      continue;
    }
    if (trimmed.startsWith("event:")) {
      event = trimmed.slice(6).trim();
    } else if (trimmed.startsWith("id:")) {
      id = trimmed.slice(3).trim();
    } else if (trimmed.startsWith("data:")) {
      data.push(trimmed.slice(5).trimStart());
    }
  }
  return data.length === 0 ? null : { event, data: data.join("\n"), ...(id ? { id } : {}) };
}

/**
 * Reads Kubernetes Events over SSE, from one Namespace or from the whole
 * Cluster.
 *
 * `EventSource` is not used, deliberately. It never exposes the HTTP status of a
 * failed connection, so an expired resourceVersion (409), a withdrawn permission
 * (403) and exhausted capacity (429) would all arrive as the same opaque error —
 * and the first of those has a specific recovery the client is expected to
 * perform. Reading the stream through `fetch` keeps the error envelope, and
 * makes the Server's in-body `close` reason actionable.
 *
 * Recovery follows what the Server documents: `watch_closed` resumes from the
 * last resourceVersion, `resource_version_expired` discards what is on screen and
 * asks for a fresh snapshot, and every other reason ends the stream.
 */
export function useKubernetesEventStream(
  options: KubernetesEventStreamOptions | null,
): KubernetesEventStream {
  const [events, setEvents] = useState<KubernetesEventRecord[]>([]);
  const [status, setStatus] = useState<EventStreamStatus>("idle");
  const [error, setError] = useState<unknown>(null);
  const [closeReason, setCloseReason] = useState<string | null>(null);
  const [truncated, setTruncated] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const abortRef = useRef<AbortController | null>(null);

  const stop = useCallback(() => abortRef.current?.abort(), []);
  const reload = useCallback(() => setAttempt((value) => value + 1), []);

  // The effect keys on the request it would make, so a parent rebuilding the
  // options object each render does not restart the stream each render.
  const url = options ? eventsUrl(options) : null;
  const streamKey = url === null ? null : `${attempt}\n${url}`;
  const follow = options?.follow ?? false;

  // Cleared during render rather than inside the effect, so no frame shows one
  // Namespace's Events under another's heading.
  const [appliedKey, setAppliedKey] = useState<string | null>(null);
  if (appliedKey !== streamKey) {
    setAppliedKey(streamKey);
    setEvents([]);
    setError(null);
    setCloseReason(null);
    setTruncated(false);
    setStatus(streamKey === null ? "idle" : "loading");
  }

  useEffect(() => {
    if (!options || streamKey === null) {
      return;
    }
    const controller = new AbortController();
    abortRef.current = controller;

    // Events are keyed by UID: Kubernetes reports a repeat of the same event as
    // a MODIFIED frame with a higher `count`, which has to replace the row
    // rather than pile another one under it.
    const byUid = new Map<string, KubernetesEventRecord>();
    let lastResourceVersion: string | undefined;
    let pendingFrame: number | null = null;
    const isCurrent = () => abortRef.current === controller && !controller.signal.aborted;

    const publish = () => {
      pendingFrame = null;
      if (!isCurrent()) {
        return;
      }
      setEvents([...byUid.values()].sort(compareEvents).slice(0, MAX_EVENTS));
    };
    const schedulePublish = () => {
      if (pendingFrame === null) {
        pendingFrame = requestAnimationFrame(publish);
      }
    };
    const publishNow = () => {
      if (pendingFrame !== null) {
        cancelAnimationFrame(pendingFrame);
        pendingFrame = null;
      }
      publish();
    };

    /** Reads one connection to completion; returns how the Server ended it. */
    const readOnce = async (resourceVersion?: string): Promise<CloseFrame | null> => {
      const response = await fetch(eventsUrl(options, resourceVersion), {
        credentials: "same-origin",
        signal: controller.signal,
        headers: { Accept: "text/event-stream" },
      });
      if (!response.ok) {
        await raiseResponseError(response);
      }
      if (!response.body) {
        throw new Error("Kubernetes Event response has no readable body");
      }
      if (isCurrent()) {
        setStatus("streaming");
      }
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      let closeFrame: CloseFrame | null = null;

      const handleFrame = (raw: string) => {
        const frame = parseFrame(raw);
        if (!frame) {
          return;
        }
        try {
          if (frame.id) {
            lastResourceVersion = frame.id;
          }
          if (frame.event === "ready") {
            const payload = JSON.parse(frame.data) as {
              initial_events_truncated?: boolean;
              resource_version?: string;
            };
            if (payload.resource_version) {
              lastResourceVersion = payload.resource_version;
            }
            if (payload.initial_events_truncated) {
              setTruncated(true);
            }
            return;
          }
          if (frame.event === "close") {
            closeFrame = JSON.parse(frame.data) as CloseFrame;
            if (closeFrame.last_resource_version) {
              lastResourceVersion = closeFrame.last_resource_version;
            }
            return;
          }
          if (frame.event !== "kubernetes.event") {
            // `bookmark` only advances the resume point, which the close frame
            // reports anyway.
            return;
          }
          const record = JSON.parse(frame.data) as KubernetesEventRecord;
          if (record.watch_type === "DELETED") {
            byUid.delete(record.uid);
          } else {
            byUid.set(record.uid, record);
          }
          schedulePublish();
        } catch {
          // One malformed frame must not end a stream that is otherwise fine.
        }
      };

      for (;;) {
        const { done, value } = await reader.read();
        if (!isCurrent()) {
          return null;
        }
        if (done) {
          break;
        }
        buffer += decoder.decode(value, { stream: true });
        for (;;) {
          const boundary = buffer.indexOf("\n\n");
          if (boundary === -1) {
            break;
          }
          handleFrame(buffer.slice(0, boundary));
          buffer = buffer.slice(boundary + 2);
        }
      }
      publishNow();
      return closeFrame;
    };

    const run = async () => {
      let resumeVersion: string | undefined;
      for (;;) {
        let outcome: CloseFrame | null;
        try {
          outcome = await readOnce(resumeVersion);
        } catch (caught) {
          if (!isCurrent()) {
            return;
          }
          if (controller.signal.aborted) {
            publishNow();
            setStatus("ended");
            return;
          }
          // An expired resourceVersion is not a failure; it is the Server asking
          // for a fresh snapshot, which is exactly what dropping the resume
          // point produces.
          if (errorCode(caught) === "resource_version_expired" && resumeVersion) {
            resumeVersion = undefined;
            lastResourceVersion = undefined;
            byUid.clear();
            publishNow();
            await delay(RECONNECT_DELAY_MS, controller.signal);
            continue;
          }
          // Once a valid SSE connection has supplied a recovery point, a
          // transport interruption is handled like EventSource would handle
          // it: reconnect from the last processed resourceVersion. HTTP
          // errors before the stream opens still remain visible to the user.
          if (follow && lastResourceVersion && errorCode(caught) === null) {
            resumeVersion = lastResourceVersion;
            await delay(RECONNECT_DELAY_MS, controller.signal);
            continue;
          }
          setError(caught);
          setStatus("error");
          return;
        }
        if (!isCurrent()) {
          return;
        }
        const reason = outcome?.reason ?? "ended";
        // Only a Watch the upstream closed on its own is worth resuming, and
        // only while the operator asked to follow.
        if (follow && reason === "watch_closed") {
          resumeVersion = outcome?.last_resource_version || lastResourceVersion;
          await delay(RECONNECT_DELAY_MS, controller.signal);
          if (!isCurrent()) {
            return;
          }
          continue;
        }
        if (follow && reason === "resource_version_expired") {
          resumeVersion = undefined;
          lastResourceVersion = undefined;
          byUid.clear();
          publishNow();
          await delay(RECONNECT_DELAY_MS, controller.signal);
          if (!isCurrent()) {
            return;
          }
          continue;
        }
        // A proxy or transport may close the response before the Server can
        // send its in-band `close` frame. Resume only after a valid recovery
        // point has been observed, so an invalid initial response cannot spin.
        if (follow && reason === "ended" && lastResourceVersion) {
          resumeVersion = lastResourceVersion;
          await delay(RECONNECT_DELAY_MS, controller.signal);
          if (!isCurrent()) {
            return;
          }
          continue;
        }
        setCloseReason(reason);
        setStatus("ended");
        return;
      }
    };

    void run();
    return () => {
      if (pendingFrame !== null) {
        cancelAnimationFrame(pendingFrame);
      }
      controller.abort();
    };
    // `options` is rebuilt every render by design; `streamKey` is what actually
    // identifies the request, and `follow` only changes together with it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [streamKey]);

  return { events, status, error, closeReason, truncated, reload, stop };
}

function delay(milliseconds: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const timer = setTimeout(resolve, milliseconds);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(timer);
        resolve();
      },
      { once: true },
    );
  });
}
