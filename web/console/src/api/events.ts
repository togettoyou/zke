import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { createPermissionChecker } from "@/auth/capabilities";

import { api, unwrap } from "./client";
import { isUnauthenticated } from "./errors";
import { queryKeys, queryKeyPrefixes } from "./query-keys";
import type { ClusterAggregate } from "./types";

export type StreamState = "connecting" | "open" | "reconnecting" | "closed";

const EVENTS_URL = "/api/v1/events";
const INITIAL_BACKOFF_MS = 1_000;
const MAXIMUM_BACKOFF_MS = 30_000;
// How long a burst of status events is allowed to accumulate before the list
// views refetch once for all of it. Short enough to read as immediate.
const LIST_REFRESH_COALESCE_MS = 400;

function nextBackoff(previous: number): number {
  const doubled = Math.min(previous * 2, MAXIMUM_BACKOFF_MS);
  // Jitter keeps many open Console tabs from reconnecting in lockstep.
  return Math.round(doubled * (0.7 + Math.random() * 0.6));
}

type Handlers = {
  onStatus: (cluster: ClusterAggregate) => void;
  onStateChange: (state: StreamState) => void;
};

/** Why a dropped stream may or may not be worth reconnecting. */
type StreamProbe = "retry" | "unauthenticated" | "forbidden";

/**
 * `EventSource` never exposes the HTTP status of a failed connection, so an
 * expired session, a withdrawn permission and a transient network drop all look
 * identical. The cause is therefore probed through the regular API client, and
 * `/api/v1/auth/me` answers both questions in one round trip:
 *
 * - a 401 means the session is gone; the shell moves back to the login view;
 * - capabilities without `cluster.read` anywhere mean the Server will keep
 *   answering 403, which is what it does for a caller who can observe no
 *   Cluster. Reconnecting would loop forever, so the stream stops instead.
 *
 * Anything else says nothing conclusive and is retried.
 */
async function probeStreamAccess(): Promise<StreamProbe> {
  try {
    const session = unwrap(await api.GET("/api/v1/auth/me", {}));
    const permissions = createPermissionChecker(session.capabilities ?? []);
    return permissions.canAnywhere("cluster.read") ? "retry" : "forbidden";
  } catch (error) {
    // Only an explicit 401 is conclusive. A network error or timeout says
    // nothing about the session, so the stream must keep retrying.
    return isUnauthenticated(error) ? "unauthenticated" : "retry";
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
   * A dropped connection is only retried while reconnecting could actually
   * succeed. Once the Server has rejected the session or the caller has lost
   * every Cluster they could observe, another attempt just reproduces the same
   * status, so the stream stops.
   */
  const handleTransportError = async (): Promise<void> => {
    if (stopped) {
      return;
    }
    handlers.onStateChange("reconnecting");
    const probe = await probeStreamAccess();
    // The probe can outlive the stream: the desktop may have unmounted, or the
    // operator may have logged out, while it was in flight.
    if (stopped) {
      return;
    }
    if (probe !== "retry") {
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

    /*
     * List refetches are coalesced; the event's own payload is not.
     *
     * A Server restart brings a whole fleet back one Agent at a time, so these
     * events arrive in bursts. The cluster list is one request, but the overview
     * is a Tenant → Project → Cluster walk, and invalidating it per event asked
     * for that entire fan-out once for every Agent that reconnected. The events
     * carry the full aggregate, so the detail entry is still written
     * immediately and nothing an operator is looking at waits for the window.
     */
    let refreshTimer: ReturnType<typeof setTimeout> | null = null;
    const refreshLists = () => {
      if (refreshTimer) {
        return;
      }
      refreshTimer = setTimeout(() => {
        refreshTimer = null;
        void queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.clusters });
        void queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.clusterOverview });
      }, LIST_REFRESH_COALESCE_MS);
    };

    const stop = startClusterEventStream({
      onStatus: (cluster) => {
        setLastEventAt(new Date());
        queryClient.setQueryData(queryKeys.cluster(cluster.id), cluster);
        refreshLists();
      },
      onStateChange: (next) => {
        setState(next);
        // Nothing is replayed after a gap, so a fresh connection refetches.
        if (next === "open" && previousState.current === "reconnecting") {
          refreshLists();
        }
        previousState.current = next;
      },
    });

    return () => {
      if (refreshTimer) {
        clearTimeout(refreshTimer);
      }
      stop();
    };
  }, [enabled, queryClient]);

  return { state, lastEventAt };
}
