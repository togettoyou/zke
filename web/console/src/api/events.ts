import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { api, unwrap } from "./client";
import { isUnauthenticated } from "./errors";
import { queryKeys, queryKeyPrefixes } from "./query-keys";
import type { ClusterAggregate } from "./types";

export type StreamState = "connecting" | "open" | "reconnecting" | "closed";

const EVENTS_URL = "/api/v1/events";
const INITIAL_BACKOFF_MS = 1_000;
const MAXIMUM_BACKOFF_MS = 30_000;

function nextBackoff(previous: number): number {
  const doubled = Math.min(previous * 2, MAXIMUM_BACKOFF_MS);
  // Jitter keeps many open Console tabs from reconnecting in lockstep.
  return Math.round(doubled * (0.7 + Math.random() * 0.6));
}

type Handlers = {
  onStatus: (cluster: ClusterAggregate) => void;
  onStateChange: (state: StreamState) => void;
};

/**
 * `EventSource` never exposes the HTTP status of a failed connection, so an
 * expired session is indistinguishable from a transient network drop. The
 * session is therefore probed through the regular API client, which shares the
 * Server's `RequireAuthentication` middleware with the stream endpoint: a 401
 * there notifies the shell (see `onUnauthenticated`) and moves the Console back
 * to the login view. Without this the stream would reconnect forever against an
 * endpoint that keeps answering 401.
 */
async function sessionRejected(): Promise<boolean> {
  try {
    unwrap(await api.GET("/api/v1/auth/me", {}));
    return false;
  } catch (error) {
    // Only an explicit 401 is conclusive. A network error or timeout says
    // nothing about the session, so the stream must keep retrying.
    return isUnauthenticated(error);
  }
}

/**
 * One shared Cluster status stream per desktop.
 *
 * The Server closes the stream after its maximum duration and sends a `close`
 * event first; both that and an unexpected drop reconnect with backoff. No
 * event history is replayed, so every (re)connection is followed by a refetch
 * of the cluster views.
 */
export function startClusterEventStream(handlers: Handlers): () => void {
  let source: EventSource | null = null;
  let retryTimer: ReturnType<typeof setTimeout> | null = null;
  let backoff = INITIAL_BACKOFF_MS;
  let stopped = false;

  const scheduleReconnect = () => {
    if (stopped || retryTimer) {
      return;
    }
    handlers.onStateChange("reconnecting");
    retryTimer = setTimeout(() => {
      retryTimer = null;
      connect();
    }, backoff);
    backoff = nextBackoff(backoff);
  };

  const teardown = () => {
    if (source) {
      source.close();
      source = null;
    }
  };

  /**
   * A dropped connection is only retried while the session is still valid.
   * Once the Server has rejected it, reconnecting would just reproduce the 401,
   * so the stream stops and lets the shell return to the login view.
   */
  const handleTransportError = async (): Promise<void> => {
    if (stopped) {
      return;
    }
    handlers.onStateChange("reconnecting");
    const rejected = await sessionRejected();
    // The probe can outlive the stream: the desktop may have unmounted, or the
    // operator may have logged out, while it was in flight.
    if (stopped) {
      return;
    }
    if (rejected) {
      stopped = true;
      handlers.onStateChange("closed");
      return;
    }
    scheduleReconnect();
  };

  function connect(): void {
    if (stopped) {
      return;
    }
    teardown();
    handlers.onStateChange(backoff === INITIAL_BACKOFF_MS ? "connecting" : "reconnecting");

    const stream = new EventSource(EVENTS_URL, { withCredentials: true });
    source = stream;

    stream.addEventListener("ready", () => {
      backoff = INITIAL_BACKOFF_MS;
      handlers.onStateChange("open");
    });

    stream.addEventListener("cluster.status", (event) => {
      try {
        handlers.onStatus(JSON.parse((event as MessageEvent<string>).data) as ClusterAggregate);
      } catch {
        // A malformed frame must not kill the stream; the next refetch wins.
      }
    });

    stream.addEventListener("close", () => {
      // Graceful server-side close (maximum stream duration): reconnect at the
      // base delay rather than treating it as a failure.
      backoff = INITIAL_BACKOFF_MS;
      teardown();
      scheduleReconnect();
    });

    stream.addEventListener("error", () => {
      // EventSource does not retry after an HTTP error status, so reconnection
      // is driven here.
      teardown();
      void handleTransportError();
    });
  }

  connect();

  return () => {
    stopped = true;
    if (retryTimer) {
      clearTimeout(retryTimer);
      retryTimer = null;
    }
    teardown();
    handlers.onStateChange("closed");
  };
}

export type ClusterEventsSnapshot = {
  state: StreamState;
  lastEventAt: Date | null;
};

/**
 * Subscribes the desktop to Cluster status events and keeps the query cache in
 * sync: the event payload is the full Cluster aggregate, so the detail entry is
 * updated directly and list views are invalidated.
 */
export function useClusterEvents(enabled: boolean): ClusterEventsSnapshot {
  const queryClient = useQueryClient();
  const [state, setState] = useState<StreamState>("closed");
  const [lastEventAt, setLastEventAt] = useState<Date | null>(null);
  const previousState = useRef<StreamState>("closed");

  useEffect(() => {
    if (!enabled) {
      // The previous stream's teardown already reported "closed".
      return;
    }

    const stop = startClusterEventStream({
      onStatus: (cluster) => {
        setLastEventAt(new Date());
        queryClient.setQueryData(queryKeys.cluster(cluster.id), cluster);
        void queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.clusters });
        void queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.clusterOverview });
      },
      onStateChange: (next) => {
        setState(next);
        // Nothing is replayed after a gap, so a fresh connection refetches.
        if (next === "open" && previousState.current === "reconnecting") {
          void queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.clusters });
          void queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.clusterOverview });
        }
        previousState.current = next;
      },
    });

    return stop;
  }, [enabled, queryClient]);

  return { state, lastEventAt };
}
