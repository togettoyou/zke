import { createContext, useContext } from "react";

import type { CurrentSession } from "@/api/types";

import { EMPTY_PERMISSION_CHECKER, type PermissionChecker } from "./capabilities";

export type SessionStatus = "loading" | "authenticated" | "unauthenticated";

export type SessionContextValue = {
  status: SessionStatus;
  session: CurrentSession | null;
  permissions: PermissionChecker;
  /** Re-reads `/api/v1/auth/me`, e.g. after role bindings changed. */
  refresh: () => void;
  logout: () => Promise<void>;
  logoutPending: boolean;
};

export const SessionContext = createContext<SessionContextValue>({
  status: "loading",
  session: null,
  permissions: EMPTY_PERMISSION_CHECKER,
  refresh: () => {},
  logout: async () => {},
  logoutPending: false,
});

export function useSessionContext(): SessionContextValue {
  return useContext(SessionContext);
}
