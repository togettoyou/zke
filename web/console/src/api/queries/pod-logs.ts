import { useCallback, useEffect, useRef, useState } from "react";

import { raiseResponseError } from "../client";

/** Everything the Server needs to open one container's log stream. */
export type PodLogOptions = {
  clusterId: string;
  namespace: string;
  podName: string;
  /** Pinned so a Pod recreated under the same name cannot answer this request. */
  uid: string;
  container: string;
  follow: boolean;
  previous: boolean;
  timestamps: boolean;
  tailLines: number;
  sinceSeconds?: number;
};

export type PodLogStatus = "idle" | "loading" | "streaming" | "ended" | "error";

export type PodLogStream = {
  text: string;
  status: PodLogStatus;
  error: unknown;
  /** Bytes decoded from the response, including any the client buffer dropped. */
  bytes: number;
  /** True once the client buffer started discarding the oldest lines. */
  truncated: boolean;
  /** Restarts the stream from scratch. */
  reload: () => void;
  /** Ends a follow stream without discarding what it has already shown. */
  stop: () => void;
};

/**
 * The client-side ceiling on one stream's buffer.
 *
 * A followed log has no natural end, so something has to bound it. The Server
 * bounds the bytes it will send; this bounds what the browser keeps in memory
 * and in one DOM text node. When it fills, the oldest lines go — a log viewer
 * that stops updating is worse than one that forgets its beginning.
 */
const MAX_BUFFERED_CHARACTERS = 2_000_000;

function logsUrl(options: PodLogOptions): string {
  const path =
    `/api/v1/clusters/${encodeURIComponent(options.clusterId)}` +
    `/namespaces/${encodeURIComponent(options.namespace)}` +
    `/pods/${encodeURIComponent(options.podName)}/logs`;
  const query = new URLSearchParams({
    uid: options.uid,
    container: options.container,
    tail_lines: String(options.tailLines),
  });
  if (options.follow) {
    query.set("follow", "true");
  }
  if (options.previous) {
    query.set("previous", "true");
  }
  if (options.timestamps) {
    query.set("timestamps", "true");
  }
  if (options.sinceSeconds !== undefined) {
    query.set("since_seconds", String(options.sinceSeconds));
  }
  return `${path}?${query.toString()}`;
}

/** Drops whole leading lines once the buffer is over its ceiling. */
function boundBuffer(text: string): { text: string; truncated: boolean } {
  if (text.length <= MAX_BUFFERED_CHARACTERS) {
    return { text, truncated: false };
  }
  const excess = text.length - MAX_BUFFERED_CHARACTERS;
  const lineBreak = text.indexOf("\n", excess);
  return {
    text: lineBreak === -1 ? text.slice(excess) : text.slice(lineBreak + 1),
    truncated: true,
  };
}

/**
 * Reads one container's logs as a stream of text.
 *
 * This is not a react-query hook, and cannot be: the response is a `text/plain`
 * body that arrives in pieces and, when following, never completes on its own.
 * Caching it would mean caching a partial read of something still changing.
 *
 * The request is abandoned whenever its options change or the view unmounts, and
 * aborting the fetch is what tells the Server to cancel the Kubernetes request
 * behind it — a follow stream nobody is reading must not keep running.
 *
 * Note on the terminal status: the Server reports it in HTTP trailers, which no
 * major browser exposes to `fetch`. So a stream that the Server ended early —
 * on its byte ceiling, its duration limit, or a revoked permission — is
 * indistinguishable here from one that ended normally, and is reported as
 * "ended" either way.
 */
export function usePodLogStream(options: PodLogOptions | null): PodLogStream {
  const [text, setText] = useState("");
  const [status, setStatus] = useState<PodLogStatus>("idle");
  const [error, setError] = useState<unknown>(null);
  const [bytes, setBytes] = useState(0);
  const [truncated, setTruncated] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const abortRef = useRef<AbortController | null>(null);

  const stop = useCallback(() => {
    abortRef.current?.abort();
  }, []);

  const reload = useCallback(() => setAttempt((value) => value + 1), []);

  // Every field of `options` is a primitive, so the effect keys on the URL it
  // would request rather than on the object identity — a parent that rebuilds
  // the object each render must not restart the stream each render.
  const url = options ? logsUrl(options) : null;
  const streamKey = url === null ? null : `${attempt}\n${url}`;

  // Cleared during render rather than in the effect: one frame still showing the
  // previous container's log under the new container's heading is one frame too
  // many, and it is the same adjust-on-key-change the paging hooks use.
  const [appliedKey, setAppliedKey] = useState<string | null>(null);
  if (appliedKey !== streamKey) {
    setAppliedKey(streamKey);
    setText("");
    setBytes(0);
    setTruncated(false);
    setError(null);
    setStatus(streamKey === null ? "idle" : "loading");
  }

  useEffect(() => {
    if (!url || streamKey === null) {
      return;
    }
    const controller = new AbortController();
    abortRef.current = controller;

    let active = true;
    let buffered = "";
    let receivedBytes = 0;
    // Chunks arrive far faster than anything needs to repaint, so the state
    // update is coalesced onto an animation frame instead of running per chunk.
    let pendingFrame: number | null = null;
    const isCurrent = () => active && abortRef.current === controller;
    const flush = () => {
      pendingFrame = null;
      if (!isCurrent()) {
        return;
      }
      const bounded = boundBuffer(buffered);
      buffered = bounded.text;
      setText(buffered);
      setBytes(receivedBytes);
      if (bounded.truncated) {
        setTruncated(true);
      }
    };
    const flushNow = () => {
      if (pendingFrame !== null) {
        cancelAnimationFrame(pendingFrame);
        pendingFrame = null;
      }
      flush();
    };

    const run = async () => {
      try {
        const response = await fetch(url, {
          credentials: "same-origin",
          signal: controller.signal,
          headers: { Accept: "text/plain" },
        });
        if (!response.ok) {
          await raiseResponseError(response);
        }
        const body = response.body;
        if (!body) {
          throw new Error("Pod log response has no readable body");
        }
        if (!isCurrent()) {
          return;
        }
        setStatus("streaming");
        const reader = body.getReader();
        const decoder = new TextDecoder();
        for (;;) {
          const { done, value } = await reader.read();
          if (!isCurrent()) {
            return;
          }
          if (done) {
            break;
          }
          buffered += decoder.decode(value, { stream: true });
          receivedBytes += value.byteLength;
          if (pendingFrame === null) {
            pendingFrame = requestAnimationFrame(flush);
          }
        }
        buffered += decoder.decode();
        flushNow();
        setStatus("ended");
      } catch (caught) {
        if (!isCurrent()) {
          return;
        }
        if (controller.signal.aborted) {
          // Stopping a stream on purpose, or leaving the view, is not a failure.
          flushNow();
          setStatus("ended");
          return;
        }
        setError(caught);
        setStatus("error");
      } finally {
        if (abortRef.current === controller) {
          abortRef.current = null;
        }
      }
    };

    void run();
    return () => {
      active = false;
      if (abortRef.current === controller) {
        abortRef.current = null;
      }
      controller.abort();
      if (pendingFrame !== null) {
        cancelAnimationFrame(pendingFrame);
      }
    };
  }, [streamKey, url]);

  return { text, status, error, bytes, truncated, reload, stop };
}
