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

/**
 * One block of the buffer.
 *
 * The log is delivered as blocks rather than as one string because of what it
 * costs to change one: a browser re-lays out a whole block when its text
 * changes, and the buffer is up to two million characters. Sealed blocks never
 * change again, so each update only re-lays out the one still growing.
 */
export type PodLogPage = {
  /** Stable for the life of a page, so a growing page keeps its DOM node. */
  id: number;
  text: string;
  /** True when the newline that ended this page was consumed as the seal. */
  endsLine: boolean;
};

export type PodLogStream = {
  pages: PodLogPage[];
  /** Nothing has been received — distinct from a buffer of blank lines. */
  empty: boolean;
  /**
   * Joins the pages back into the log as received.
   *
   * A function rather than a value: copying and downloading are the only things
   * that need the whole buffer, and materialising two million characters on
   * every render to keep a prop up to date would cost far more than either.
   */
  readText: () => string;
  status: PodLogStatus;
  error: unknown;
  /** Bytes decoded from the response, including any the client buffer dropped. */
  bytes: number;
  /** True once the client buffer started discarding the oldest lines. */
  truncated: boolean;
  /** Restarts the stream from scratch. */
  reload: () => void;
  /**
   * Ends a follow stream without discarding what it has already shown.
   *
   * The caller is expected to turn its follow mode off in the same event; that
   * change alone does not re-read anything.
   */
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

/**
 * How much text one page holds before it is sealed.
 *
 * This is the real bound on what an update costs. Held as a single text node,
 * the whole buffer is re-laid out every time a chunk arrives, and that cost
 * follows the buffer rather than the chunk. Measured in headless Chrome at this
 * surface's width and font, driving this hook with 8 KB a frame up to the
 * ceiling: 150 ms per update as one text node, 2.3 ms split into pages. The
 * first number is a tab that stops answering exactly when logs are arriving
 * fastest; the second is a fraction of a frame.
 *
 * 16 KB keeps the growing page small while leaving about 125 blocks at the
 * ceiling. Smaller pages measured no faster and only multiply the nodes.
 */
const PAGE_CHARACTERS = 16_000;

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

function pageText(page: PodLogPage): string {
  return page.endsLine ? `${page.text}\n` : page.text;
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
  const [pages, setPages] = useState<PodLogPage[]>([]);
  const [status, setStatus] = useState<PodLogStatus>("idle");
  const [error, setError] = useState<unknown>(null);
  const [bytes, setBytes] = useState(0);
  const [truncated, setTruncated] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const abortRef = useRef<AbortController | null>(null);

  const stop = useCallback(() => {
    const controller = abortRef.current;
    if (!controller) {
      return;
    }
    controller.abort();
    // Ended here rather than when the aborted fetch rejects: that rejection is a
    // microtask, and the caller turns its follow mode off in this same event, so
    // the render in between would otherwise still see a running stream.
    setStatus("ended");
  }, []);

  const reload = useCallback(() => setAttempt((value) => value + 1), []);

  // Every field of `options` is a primitive, so the effect keys on the URL it
  // would request rather than on the object identity — a parent that rebuilds
  // the object each render must not restart the stream each render.
  const url = options ? logsUrl(options) : null;
  const streamKey = url === null ? null : `${attempt}\n${url}`;

  // The request being read, held as state rather than taken from the key above,
  // so that one stopped stream can outlive a change of options.
  const [applied, setApplied] = useState<{ key: string; url: string } | null>(null);

  // Turning follow off is not a new read: every line a snapshot with these
  // options would return is already on screen. So a stopped follow keeps the
  // request it was reading, and nothing is re-read until the operator reloads or
  // changes something else. Without this case the caller could not turn the mode
  // off at all — doing so would throw away the lines the follow had collected —
  // and leaving it on means the next reload or checkbox silently starts
  // following again.
  const stoppedFollow =
    status === "ended" &&
    applied !== null &&
    options !== null &&
    !options.follow &&
    applied.key === `${attempt}\n${logsUrl({ ...options, follow: true })}`;

  // Cleared during render rather than in the effect: one frame still showing the
  // previous container's log under the new container's heading is one frame too
  // many, and it is the same adjust-on-key-change the paging hooks use.
  if (!stoppedFollow && (applied?.key ?? null) !== streamKey) {
    setApplied(streamKey === null || url === null ? null : { key: streamKey, url });
    setPages([]);
    setBytes(0);
    setTruncated(false);
    setError(null);
    setStatus(streamKey === null ? "idle" : "loading");
  }

  useEffect(() => {
    if (applied === null) {
      return;
    }
    const requestUrl = applied.url;
    const controller = new AbortController();
    abortRef.current = controller;

    let active = true;
    // Sealed pages are never touched again; `tail` is the one still growing, and
    // `total` counts the characters both hold together.
    let sealed: PodLogPage[] = [];
    let tail = "";
    let total = 0;
    let nextId = 0;
    let pending = "";
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
      if (pending === "") {
        // A chunk can decode to nothing — a multi-byte character split across
        // two reads — and its bytes still count as received.
        setBytes(receivedBytes);
        return;
      }
      tail += pending;
      total += pending.length;
      pending = "";

      // Seal full pages off the front of the tail, at a line break so that a
      // page boundary lands where a line already ended.
      while (tail.length >= PAGE_CHARACTERS) {
        const lineBreak = tail.lastIndexOf("\n", PAGE_CHARACTERS - 1);
        if (lineBreak === -1) {
          // A single line longer than a page. It is wrapped on screen either
          // way, so sealing mid-line moves a break rather than inventing one,
          // and it keeps one block from growing without bound.
          sealed.push({ id: nextId++, text: tail.slice(0, PAGE_CHARACTERS), endsLine: false });
          tail = tail.slice(PAGE_CHARACTERS);
          continue;
        }
        sealed.push({ id: nextId++, text: tail.slice(0, lineBreak), endsLine: true });
        tail = tail.slice(lineBreak + 1);
      }

      // Over the ceiling, whole pages go. Dropping a page is one node removed
      // rather than the buffer rewritten, and it is why the cost of an update
      // does not grow once the buffer is full.
      let dropped = 0;
      for (const page of sealed) {
        if (total <= MAX_BUFFERED_CHARACTERS) {
          break;
        }
        total -= page.text.length + (page.endsLine ? 1 : 0);
        dropped += 1;
      }
      if (dropped > 0) {
        sealed = sealed.slice(dropped);
        setTruncated(true);
      }

      // The tail carries the id it will keep once sealed, so the page still
      // growing keeps its DOM node across updates.
      setPages([...sealed, { id: nextId, text: tail, endsLine: false }]);
      setBytes(receivedBytes);
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
        const response = await fetch(requestUrl, {
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
          pending += decoder.decode(value, { stream: true });
          receivedBytes += value.byteLength;
          if (pendingFrame === null) {
            pendingFrame = requestAnimationFrame(flush);
          }
        }
        pending += decoder.decode();
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
  }, [applied]);

  const readText = useCallback(() => pages.map(pageText).join(""), [pages]);

  return {
    pages,
    empty: pages.every((page) => page.text === ""),
    readText,
    status,
    error,
    bytes,
    truncated,
    reload,
    stop,
  };
}
