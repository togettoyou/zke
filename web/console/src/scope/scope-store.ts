import { create } from "zustand";

import { EMPTY_SCOPE, type ScopeSelection } from "@/api/types";

type ScopeState = {
  scope: ScopeSelection;
  setScope: (scope: ScopeSelection) => void;
  hydrate: (scope: ScopeSelection) => void;
  reset: () => void;
};

/**
 * The selected Project, shared by the whole Console.
 *
 * There is exactly one: every window works in it, so an operator can never be
 * looking at two Projects at once and mistake one for the other. It is always a
 * whole (Tenant, Project) pair or nothing at all, so a Project is never shown
 * attributed to the wrong Tenant.
 */
export const useScopeStore = create<ScopeState>((set) => ({
  scope: EMPTY_SCOPE,
  setScope: (scope) => set({ scope }),
  hydrate: (scope) => set({ scope }),
  reset: () => set({ scope: EMPTY_SCOPE }),
}));
