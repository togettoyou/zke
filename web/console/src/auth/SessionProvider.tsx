import { useCallback, useEffect, useMemo, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { onUnauthenticated } from "@/api/client";
import { useLogout, useSession } from "@/api/queries/auth";
import { queryKeys } from "@/api/query-keys";

import { createPermissionChecker } from "./capabilities";
import { SessionContext, type SessionContextValue, type SessionStatus } from "./session-context";

/**
 * Owns the browser's view of the ZKE session.
 *
 * The session itself lives in an HttpOnly cookie, so the Console never holds a
 * token: it asks the Server who it is and drops all cached data as soon as the
 * Server answers 401 anywhere.
 */
export function SessionProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const sessionQuery = useSession();
  const logoutMutation = useLogout();

  useEffect(
    () =>
      onUnauthenticated(() => {
        // Drop every cached server payload, but never the session query itself:
        // its own 401 is what moves the shell to the login view, and removing
        // that result here would make the two re-trigger each other forever.
        queryClient.removeQueries({
          predicate: (query) => query.queryKey[0] !== queryKeys.session()[0],
        });
        // A 401 from some other endpoint means the cached identity is stale.
        if (queryClient.getQueryState(queryKeys.session())?.data) {
          void queryClient.invalidateQueries({ queryKey: queryKeys.session() });
        }
      }),
    [queryClient],
  );

  const refresh = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: queryKeys.session() });
  }, [queryClient]);

  const logout = useCallback(async () => {
    await logoutMutation.mutateAsync().catch(() => undefined);
    await queryClient.invalidateQueries({ queryKey: queryKeys.session() });
  }, [logoutMutation, queryClient]);

  // A background refetch must not flash the login view, so only the very first
  // load (no data and no error yet) counts as "loading".
  const status: SessionStatus = sessionQuery.data
    ? "authenticated"
    : sessionQuery.isPending
      ? "loading"
      : "unauthenticated";

  const value = useMemo<SessionContextValue>(
    () => ({
      status,
      session: sessionQuery.data ?? null,
      permissions: createPermissionChecker(sessionQuery.data?.capabilities ?? []),
      refresh,
      logout,
      logoutPending: logoutMutation.isPending,
    }),
    [status, sessionQuery.data, refresh, logout, logoutMutation.isPending],
  );

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}
