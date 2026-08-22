import { create } from "zustand";

import { findAppManifest } from "@/apps/registry";

import {
  cascadeRect,
  clampRect,
  computeDesktopBounds,
  fullscreenRect,
  type DesktopBounds,
  type Viewport,
  type WindowRect,
} from "./geometry";

export type WindowMode = "normal" | "minimized" | "maximized";

export type WindowInstance = {
  id: string;
  appId: string;
  title: string;
  rect: WindowRect;
  /** Geometry to return to when un-maximizing or restoring. */
  restoreRect: WindowRect;
  mode: WindowMode;
  zIndex: number;
  /**
   * Bumped to mount the application again in the window that is already open.
   *
   * Deliberately not persisted: it exists to hand a target to an application
   * that reads it as it mounts, and a restored desktop has no such target
   * waiting. See `OpenWindowOptions.restart`.
   */
  generation: number;
};

export type OpenWindowOptions = {
  title?: string;
  /**
   * Start the application over rather than only bringing its window forward.
   *
   * For a link that names where the application should open — an AIOps
   * evidence reference, a deep link from outside the Console. An application
   * that is already open picked its Cluster and its view when it mounted, so
   * focusing it would show the operator the wrong thing under a link that
   * claimed to go somewhere specific.
   */
  restart?: boolean;
};

type WindowState = {
  windows: Record<string, WindowInstance>;
  order: string[];
  focusedId: string | null;
  nextZIndex: number;
  /** Area available to normal windows: inside the top bar, Dock and margins. */
  bounds: DesktopBounds;
  /** The whole desktop surface, which is what a maximized window covers. */
  viewport: Viewport;
  /**
   * Show desktop: every window is held off screen, and nothing else changes.
   *
   * Deliberately not part of any window's own state. The windows keep their
   * geometry, their stacking and their focus while they are away, so coming
   * back is not a restore — it is the same desk with the same things on it.
   * Transient too: it is never persisted, because a layout that reopened with
   * its windows already pushed aside would look like an empty desktop.
   */
  desktopRevealed: boolean;

  setViewport: (viewport: Viewport) => void;
  openWindow: (appId: string, options?: OpenWindowOptions) => string;
  closeWindow: (windowId: string) => void;
  focusWindow: (windowId: string) => void;
  minimizeWindow: (windowId: string) => void;
  restoreWindow: (windowId: string) => void;
  toggleDesktopReveal: () => void;
  toggleMaximize: (windowId: string) => void;
  setWindowRect: (windowId: string, rect: WindowRect) => void;
  setWindowTitle: (windowId: string, title: string) => void;
  cycleFocus: (direction: 1 | -1) => void;
  closeAll: () => void;
  hydrate: (snapshot: PersistedDesktop) => void;
};

export type PersistedWindow = Pick<
  WindowInstance,
  "id" | "appId" | "title" | "rect" | "restoreRect" | "mode"
>;

export type PersistedDesktop = {
  version: 1;
  windows: PersistedWindow[];
  focusedId: string | null;
};

const INITIAL_VIEWPORT: Viewport = { width: 1_440, height: 900 };
const INITIAL_BOUNDS = computeDesktopBounds(INITIAL_VIEWPORT.width, INITIAL_VIEWPORT.height);

function createWindowId(): string {
  return crypto.randomUUID();
}

export const useWindowStore = create<WindowState>((set, get) => ({
  windows: {},
  order: [],
  focusedId: null,
  nextZIndex: 1,
  bounds: INITIAL_BOUNDS,
  viewport: INITIAL_VIEWPORT,
  desktopRevealed: false,

  setViewport: (viewport) =>
    set((state) => {
      const bounds = computeDesktopBounds(viewport.width, viewport.height);
      const windows: Record<string, WindowInstance> = {};
      for (const [id, instance] of Object.entries(state.windows)) {
        windows[id] =
          instance.mode === "maximized"
            ? { ...instance, rect: fullscreenRect(viewport) }
            : { ...instance, rect: clampRect(instance.rect, viewport) };
      }
      return { bounds, viewport, windows };
    }),

  openWindow: (appId, options = {}) => {
    const manifest = findAppManifest(appId);
    if (!manifest) {
      throw new Error(`unknown application: ${appId}`);
    }

    const state = get();

    // One window per application: launching an application that is already open
    // returns to the window that is there, keeping whatever the operator had
    // arranged in it, rather than starting over in a duplicate.
    const existing = state.order.find((id) => state.windows[id]?.appId === appId);
    if (existing) {
      get().restoreWindow(existing);
      if (options.title) {
        get().setWindowTitle(existing, options.title);
      }
      if (options.restart) {
        set((current) => {
          const instance = current.windows[existing];
          return instance
            ? {
                windows: {
                  ...current.windows,
                  [existing]: { ...instance, generation: instance.generation + 1 },
                },
              }
            : current;
        });
      }
      return existing;
    }

    const id = createWindowId();
    // Cascade against everything already open so a newly launched application
    // never lands exactly on top of another one.
    const rect = cascadeRect(
      state.bounds,
      state.order.length,
      manifest.defaultSize,
      state.viewport,
    );

    const instance: WindowInstance = {
      id,
      appId,
      title: options.title ?? manifest.title,
      rect,
      restoreRect: rect,
      mode: "normal",
      zIndex: state.nextZIndex,
      generation: 0,
    };

    set({
      windows: { ...state.windows, [id]: instance },
      order: [...state.order, id],
      focusedId: id,
      nextZIndex: state.nextZIndex + 1,
      // Launching something is asking to see it, so the desk comes back with it
      // rather than leaving the new window off screen with the rest.
      desktopRevealed: false,
    });
    return id;
  },

  closeWindow: (windowId) =>
    set((state) => {
      if (!state.windows[windowId]) {
        return state;
      }
      const windows = { ...state.windows };
      delete windows[windowId];
      const order = state.order.filter((id) => id !== windowId);
      // Focus moves to the top-most remaining window rather than to nothing.
      const focusedId =
        state.focusedId === windowId
          ? (order
              .map((id) => windows[id])
              .filter((instance): instance is WindowInstance =>
                Boolean(instance && instance.mode !== "minimized"),
              )
              .sort((left, right) => right.zIndex - left.zIndex)[0]?.id ?? null)
          : state.focusedId;
      return { windows, order, focusedId };
    }),

  focusWindow: (windowId) =>
    set((state) => {
      const instance = state.windows[windowId];
      if (!instance) {
        return state;
      }
      if (state.focusedId === windowId && instance.mode !== "minimized") {
        // Already the focused window, so there is nothing to raise — but asking
        // for it while the desk is cleared is asking to have it back.
        return state.desktopRevealed ? { desktopRevealed: false } : state;
      }
      return {
        windows: {
          ...state.windows,
          [windowId]: {
            ...instance,
            zIndex: state.nextZIndex,
            mode: instance.mode === "minimized" ? "normal" : instance.mode,
          },
        },
        focusedId: windowId,
        nextZIndex: state.nextZIndex + 1,
        desktopRevealed: false,
      };
    }),

  minimizeWindow: (windowId) =>
    set((state) => {
      const instance = state.windows[windowId];
      if (!instance) {
        return state;
      }
      const remaining = state.order
        .map((id) => state.windows[id])
        .filter(
          (candidate): candidate is WindowInstance =>
            Boolean(candidate) && candidate!.id !== windowId && candidate!.mode !== "minimized",
        )
        .sort((left, right) => right.zIndex - left.zIndex);
      return {
        windows: { ...state.windows, [windowId]: { ...instance, mode: "minimized" } },
        focusedId: state.focusedId === windowId ? (remaining[0]?.id ?? null) : state.focusedId,
      };
    }),

  restoreWindow: (windowId) =>
    set((state) => {
      const instance = state.windows[windowId];
      if (!instance) {
        return state;
      }
      return {
        windows: {
          ...state.windows,
          [windowId]: {
            ...instance,
            mode: instance.mode === "minimized" ? "normal" : instance.mode,
            zIndex: state.nextZIndex,
          },
        },
        focusedId: windowId,
        nextZIndex: state.nextZIndex + 1,
        desktopRevealed: false,
      };
    }),

  /**
   * Show desktop, and show it back.
   *
   * The windows are held off screen by the view rather than moved: their rects
   * are untouched, so this costs nothing to undo and cannot drift from where
   * the operator left them.
   */
  toggleDesktopReveal: () =>
    set((state) =>
      // Nothing open, nothing to clear away — and toggling a flag no one can see
      // would only leave the next desktop click undoing an invisible state.
      Object.keys(state.windows).length === 0 ? state : { desktopRevealed: !state.desktopRevealed },
    ),

  toggleMaximize: (windowId) =>
    set((state) => {
      const instance = state.windows[windowId];
      if (!instance) {
        return state;
      }
      const maximized = instance.mode === "maximized";
      return {
        windows: {
          ...state.windows,
          [windowId]: {
            ...instance,
            mode: maximized ? "normal" : "maximized",
            rect: maximized
              ? clampRect(instance.restoreRect, state.viewport)
              : fullscreenRect(state.viewport),
            restoreRect: maximized ? instance.restoreRect : instance.rect,
            zIndex: state.nextZIndex,
          },
        },
        focusedId: windowId,
        nextZIndex: state.nextZIndex + 1,
        desktopRevealed: false,
      };
    }),

  setWindowRect: (windowId, rect) =>
    set((state) => {
      const instance = state.windows[windowId];
      if (!instance) {
        return state;
      }
      const clamped = clampRect(rect, state.viewport);
      return {
        windows: {
          ...state.windows,
          [windowId]: {
            ...instance,
            rect: clamped,
            restoreRect: instance.mode === "maximized" ? instance.restoreRect : clamped,
            mode: instance.mode === "maximized" ? "normal" : instance.mode,
          },
        },
      };
    }),

  setWindowTitle: (windowId, title) =>
    set((state) => {
      const instance = state.windows[windowId];
      return instance
        ? { windows: { ...state.windows, [windowId]: { ...instance, title } } }
        : state;
    }),

  cycleFocus: (direction) => {
    const state = get();
    const visible = state.order.filter((id) => state.windows[id]?.mode !== "minimized");
    if (visible.length === 0) {
      return;
    }
    const currentIndex = state.focusedId ? visible.indexOf(state.focusedId) : -1;
    const nextIndex = (currentIndex + direction + visible.length) % visible.length;
    const nextId = visible[nextIndex];
    if (nextId) {
      get().focusWindow(nextId);
    }
  },

  closeAll: () =>
    set({ windows: {}, order: [], focusedId: null, nextZIndex: 1, desktopRevealed: false }),

  hydrate: (snapshot) =>
    set((state) => {
      const windows: Record<string, WindowInstance> = {};
      const order: string[] = [];
      const seenApps = new Set<string>();
      let zIndex = 1;
      for (const persisted of snapshot.windows) {
        if (!findAppManifest(persisted.appId)) {
          // Applications can disappear between releases; skip unknown ids.
          continue;
        }
        if (seenApps.has(persisted.appId)) {
          // Layouts saved before one-window-per-application may hold duplicates;
          // the first is restored and the rest are dropped.
          continue;
        }
        seenApps.add(persisted.appId);
        windows[persisted.id] = {
          ...persisted,
          rect:
            persisted.mode === "maximized"
              ? fullscreenRect(state.viewport)
              : clampRect(persisted.rect, state.viewport),
          restoreRect: clampRect(persisted.restoreRect, state.viewport),
          zIndex: zIndex++,
          generation: 0,
        };
        order.push(persisted.id);
      }
      const focusedId =
        snapshot.focusedId && windows[snapshot.focusedId] ? snapshot.focusedId : null;
      return { windows, order, focusedId, nextZIndex: zIndex };
    }),
}));

/** Serializable desktop layout; never includes server data or credentials. */
export function toPersistedDesktop(state: {
  windows: Record<string, WindowInstance>;
  order: string[];
  focusedId: string | null;
}): PersistedDesktop {
  return {
    version: 1,
    focusedId: state.focusedId,
    windows: state.order
      .map((id) => state.windows[id])
      .filter((instance): instance is WindowInstance => Boolean(instance))
      .map((instance) => ({
        id: instance.id,
        appId: instance.appId,
        title: instance.title,
        rect: instance.rect,
        restoreRect: instance.restoreRect,
        mode: instance.mode,
      })),
  };
}
