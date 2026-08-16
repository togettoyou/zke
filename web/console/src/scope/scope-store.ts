import { create } from "zustand";

import { EMPTY_SCOPE, type ScopeSelection } from "@/api/types";

/**
 * How long the top bar's Project picker stays highlighted after something asked
 * for it. Long enough to be found on a wide display, short enough that it is
 * over before the operator has finished reading the view that asked.
 *
 * Shared because two places need the same number: the picker, which draws the
 * highlight, and the desktop, which holds the top bar on screen while it runs.
 */
export const SCOPE_ATTENTION_MS = 2_600;

type ScopeState = {
  scope: ScopeSelection;
  /**
   * Bumped every time a view reports that it cannot work without a Project.
   *
   * A counter rather than a boolean: two windows opened without a scope, or one
   * asked twice, both have to restart the highlight, and a flag already set to
   * `true` cannot say "again".
   */
  attentionToken: number;
  setScope: (scope: ScopeSelection) => void;
  hydrate: (scope: ScopeSelection) => void;
  reset: () => void;
  requestAttention: () => void;
};

/**
 * The selected Project, shared by the whole Console.
 *
 * There is exactly one: every window works in it, so an operator can never be
 * looking at two Projects at once and mistake one for the other. It is always a
 * whole (Tenant, Project) pair or nothing at all, so a Project is never shown
 * attributed to the wrong Tenant.
 *
 * It also carries the request to point at the picker, because the picker lives
 * in the top bar and the views that need a Project live inside windows: there is
 * no parent between them to pass a callback through, and the one thing they
 * already share is this store.
 */
export const useScopeStore = create<ScopeState>((set) => ({
  scope: EMPTY_SCOPE,
  attentionToken: 0,
  setScope: (scope) => set({ scope }),
  hydrate: (scope) => set({ scope }),
  reset: () => set({ scope: EMPTY_SCOPE }),
  requestAttention: () => set((state) => ({ attentionToken: state.attentionToken + 1 })),
}));
