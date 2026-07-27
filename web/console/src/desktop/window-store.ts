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
};

export type OpenWindowOptions = {
  title?: string;
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

  setViewport: (viewport: Viewport) => void;
  openWindow: (appId: string, options?: OpenWindowOptions) => string;
  closeWindow: (windowId: string) => void;
  focusWindow: (windowId: string) => void;
  minimizeWindow: (windowId: string) => void;
  restoreWindow: (windowId: string) => void;
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
    };

    set({
      windows: { ...state.windows, [id]: instance },
      order: [...state.order, id],
      focusedId: id,
      nextZIndex: state.nextZIndex + 1,
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
        return state;
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
      };
    }),

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

  closeAll: () => set({ windows: {}, order: [], focusedId: null, nextZIndex: 1 }),

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
