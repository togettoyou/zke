import { EMPTY_SCOPE, type ScopeSelection } from "@/api/types";

import type { PersistedDesktop } from "./window-store";

export type PersistedDesktopState = {
  desktop: PersistedDesktop;
  scope: ScopeSelection;
  /** The operator's show/hide choice for the Dock. */
  dockVisible: boolean;
};

const STORAGE_PREFIX = "zke.desktop.layout.";

export function desktopStorageKey(userId: string): string {
  return `${STORAGE_PREFIX}${userId}`;
}

/**
 * Desktop layout persistence.
 *
 * Only window geometry and the selected scope identifiers are stored — never
 * server payloads, credentials or tokens. The key is per user so a shared
 * browser profile does not leak one operator's working set to another.
 */
export function loadDesktopState(userId: string): PersistedDesktopState | null {
  try {
    const raw = localStorage.getItem(desktopStorageKey(userId));
    if (!raw) {
      return null;
    }
    const parsed = JSON.parse(raw) as Partial<PersistedDesktopState>;
    if (!parsed.desktop || parsed.desktop.version !== 1 || !Array.isArray(parsed.desktop.windows)) {
      return null;
    }
    return {
      desktop: parsed.desktop,
      scope: { ...EMPTY_SCOPE, ...(parsed.scope ?? {}) },
      // Layouts written before the Dock could be hidden have no flag; shown is
      // the state they were saved in.
      dockVisible: parsed.dockVisible ?? true,
    };
  } catch {
    return null;
  }
}

export function saveDesktopState(userId: string, state: PersistedDesktopState): void {
  try {
    localStorage.setItem(desktopStorageKey(userId), JSON.stringify(state));
  } catch {
    // Storage may be unavailable or full; the desktop keeps working in memory.
  }
}

export function clearDesktopState(userId: string): void {
  try {
    localStorage.removeItem(desktopStorageKey(userId));
  } catch {
    // Ignored for the same reason as above.
  }
}
