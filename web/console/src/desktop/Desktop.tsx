import { useCallback, useEffect, useRef, useState, type MouseEvent } from "react";

import { useClusterEvents } from "@/api/events";
import { useSessionContext } from "@/auth/session-context";
import { cn } from "@/lib/cn";
import { useScopeStore } from "@/scope/scope-store";

import { Dock } from "./Dock";
import { IconGrid } from "./IconGrid";
import { TopBar } from "./TopBar";
import { Window } from "./Window";
import { clearDesktopState, loadDesktopState, saveDesktopState } from "./persistence";
import { toPersistedDesktop, useWindowStore, type WindowInstance } from "./window-store";

const STACKED_BREAKPOINT = 1024;
const PERSIST_DEBOUNCE_MS = 400;

/**
 * The ZKE Web Desktop shell.
 *
 * It owns the desktop viewport (bounds for every window), the shared Cluster
 * event stream, layout persistence per user, and the keyboard shortcuts that
 * make multi-window work practical.
 */
export function Desktop() {
  const { session, permissions } = useSessionContext();
  const userId = session?.user.id ?? null;
  // The Server refuses the event stream to a caller who can observe no Cluster
  // at all, and `EventSource` never exposes that status, so an ungated stream
  // would reconnect forever. Mirror the Server's own check here: it resolves
  // `cluster.read` visibility across every binding, which is what
  // `canAnywhere` reports.
  const clusterEventsAllowed = Boolean(session) && permissions.canAnywhere("cluster.read");

  const setViewport = useWindowStore((state) => state.setViewport);
  const openWindow = useWindowStore((state) => state.openWindow);
  const toggleDesktopReveal = useWindowStore((state) => state.toggleDesktopReveal);
  const desktopRevealed = useWindowStore((state) => state.desktopRevealed);
  const cycleFocus = useWindowStore((state) => state.cycleFocus);
  const hydrate = useWindowStore((state) => state.hydrate);
  const closeAll = useWindowStore((state) => state.closeAll);
  const windows = useWindowStore((state) => state.windows);
  const order = useWindowStore((state) => state.order);
  const focusedId = useWindowStore((state) => state.focusedId);

  const globalScope = useScopeStore((state) => state.scope);
  const setScope = useScopeStore((state) => state.setScope);
  const hydrateScope = useScopeStore((state) => state.hydrate);
  const resetScope = useScopeStore((state) => state.reset);

  const [stacked, setStacked] = useState(
    () => typeof window !== "undefined" && window.innerWidth < STACKED_BREAKPOINT,
  );
  // The Dock is shown or hidden by the operator, never by pointer proximity, and
  // the choice holds whether or not a window is full screen. Read straight from
  // storage: this component only mounts once the session is known, so the saved
  // choice applies on the very first paint instead of flickering into place.
  const [dockVisible, setDockVisible] = useState(
    () => (userId ? loadDesktopState(userId)?.dockVisible : undefined) ?? true,
  );
  const hydratedFor = useRef<string | null>(null);
  const persistTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const { state: streamState, lastEventAt } = useClusterEvents(clusterEventsAllowed);

  /**
   * A full-screen window owns the whole surface, so the top bar steps out of its
   * way. The rule follows focus rather than "any window is maximized": a smaller
   * window raised above a maximized one is still working on the desktop and
   * needs its chrome. Stacked (narrow) layouts are excluded because every window
   * already fills the desktop there.
   *
   * The bar stays gone for as long as the window is full screen; the window
   * keeps its own title bar, which is where restoring happens.
   */
  const focusedWindow = focusedId ? windows[focusedId] : undefined;
  // A revealed desktop is the opposite of immersive: the full-screen window is
  // off screen, and the bar it was hiding for is the desktop's own chrome again.
  const immersive = !stacked && !desktopRevealed && focusedWindow?.mode === "maximized";

  const toggleDock = useCallback(() => setDockVisible((shown) => !shown), []);

  /*
   * Viewport changes are coalesced to one per frame.
   *
   * `resize` fires as fast as the window manager can report it while an edge is
   * being dragged, and each one is expensive at the far end: the store rebuilds
   * every window's rectangle through `clampRect` and re-renders the desktop. A
   * frame cannot show more than one of those anyway, so the extra ones are work
   * done to be overwritten.
   */
  useEffect(() => {
    let frame: number | null = null;
    const applyViewport = () => {
      frame = null;
      setViewport({ width: window.innerWidth, height: window.innerHeight });
      setStacked(window.innerWidth < STACKED_BREAKPOINT);
    };
    const scheduleViewport = () => {
      if (frame === null) {
        frame = requestAnimationFrame(applyViewport);
      }
    };
    applyViewport();
    window.addEventListener("resize", scheduleViewport);
    return () => {
      window.removeEventListener("resize", scheduleViewport);
      if (frame !== null) {
        cancelAnimationFrame(frame);
      }
    };
  }, [setViewport]);

  // Restore the previous layout for this user, then honour a deep link.
  useEffect(() => {
    if (!userId || hydratedFor.current === userId) {
      return;
    }
    hydratedFor.current = userId;

    const stored = loadDesktopState(userId);
    if (stored) {
      hydrateScope(stored.scope);
      hydrate(stored.desktop);
    }

    const params = new URLSearchParams(window.location.search);
    const appId = params.get("app");
    const tenantId = params.get("tenant");
    const projectId = params.get("project");

    // A deep link may carry the Project to work in. It arrives as bare ids; the
    // picker resolves the labels, and the Server still authorizes every request
    // against it, so an unreachable Project simply yields empty views.
    if (tenantId && projectId) {
      setScope({ tenantId, tenantName: null, projectId, projectName: null });
    }

    if (appId) {
      try {
        openWindow(appId);
      } catch {
        // Unknown application in the URL: ignore rather than break the desktop.
      }
    }
    if (appId || (tenantId && projectId)) {
      window.history.replaceState(null, "", window.location.pathname);
    }
  }, [hydrate, hydrateScope, openWindow, setScope, userId]);

  // Debounced layout persistence.
  useEffect(() => {
    if (!userId || hydratedFor.current !== userId) {
      return;
    }
    if (persistTimer.current) {
      clearTimeout(persistTimer.current);
    }
    persistTimer.current = setTimeout(() => {
      saveDesktopState(userId, {
        desktop: toPersistedDesktop({ windows, order, focusedId }),
        scope: globalScope,
        dockVisible,
      });
    }, PERSIST_DEBOUNCE_MS);
    return () => {
      if (persistTimer.current) {
        clearTimeout(persistTimer.current);
      }
    };
  }, [dockVisible, focusedId, globalScope, order, userId, windows]);

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key === "`") {
        event.preventDefault();
        cycleFocus(event.shiftKey ? -1 : 1);
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [cycleFocus]);

  const handleOpenApp = useCallback((appId: string) => openWindow(appId), [openWindow]);

  /*
   * Clicking bare desktop sweeps the windows aside; clicking it again brings
   * them back.
   *
   * The launcher sits in the middle of the screen, which is also where windows
   * open, so reaching a second application meant first getting the first one out
   * of the way — one window at a time, through the Dock. This does it in one
   * click, from the surface the windows are covering, and undoes it in one more.
   * Nothing is minimized and nothing is closed: the windows are held off screen
   * and come back to the place they left.
   *
   * The layer below is the desktop surface itself: windows and the Dock both sit
   * above it and swallow their own clicks, so anything that arrives here landed
   * on the desktop. The launcher's buttons are the one thing inside it that
   * means something else; everything else — the grid, its padding, the wallpaper
   * showing through — is desktop.
   */
  const handleDesktopClick = useCallback(
    (event: MouseEvent<HTMLDivElement>) => {
      if ((event.target as HTMLElement).closest("button")) {
        return;
      }
      toggleDesktopReveal();
    },
    [toggleDesktopReveal],
  );

  const handleResetDesktop = useCallback(() => {
    closeAll();
    resetScope();
    if (userId) {
      clearDesktopState(userId);
    }
  }, [closeAll, resetScope, userId]);

  // Keep every open application mounted. Minimizing a live terminal is a
  // presentation change, not a request to tear down its WebSocket and process.
  const openWindows = order
    .map((id) => windows[id])
    .filter((instance): instance is WindowInstance => Boolean(instance));

  // Keep the React/DOM order tied to the stable launch order. Reordering these
  // keyed subtrees on every focus change moves an application's live DOM node;
  // canvas-backed views such as xterm can lose their rendered surface during
  // that move. Stacking is instead derived separately and applied only through
  // CSS z-index, which changes which window is in front without relocating it.
  const stackedWindows = [...openWindows].sort((left, right) => left.zIndex - right.zIndex);
  const stackIndexById = new Map(stackedWindows.map((instance, index) => [instance.id, index]));
  const visibleWindows = stackedWindows.filter((instance) => instance.mode !== "minimized");

  const topStackedId = stacked ? (visibleWindows[visibleWindows.length - 1]?.id ?? null) : null;

  return (
    <div className="from-desktop-from to-desktop-to relative h-full w-full overflow-hidden bg-linear-to-br">
      {/*
       * The wallpaper: colour, then the polar field, then grain. Layers rather
       * than a wash, because the chrome above it is frosted glass — a top bar and
       * a Dock that blur and saturate whatever is behind them have to be given
       * something worth sampling.
       *
       * All of it sits below the windows and takes no pointer.
       */}
      <div aria-hidden className="zke-desktop-surface pointer-events-none absolute inset-0" />
      <div aria-hidden className="zke-desktop-field pointer-events-none absolute inset-0" />
      <div aria-hidden className="zke-grain pointer-events-none absolute inset-0" />

      <TopBar
        streamState={streamState}
        lastEventAt={lastEventAt}
        onOpenApp={handleOpenApp}
        onResetDesktop={handleResetDesktop}
        className={cn(
          "transition-transform duration-200 ease-out",
          // Clears the glass layer, not the content row: the bar's frosted
          // backdrop hangs 64px below its own top edge so its blur can fade out,
          // and `-translate-y-full` would only move the 40px header, leaving the
          // tail of the glass on screen.
          immersive && "pointer-events-none -translate-y-16",
        )}
      />

      {/* Centred and floated down the screen rather than jammed into the top
          corner. Ten icons pinned to a corner of a wide display read as a list
          that ran out; the same ten centred with air above them read as a
          composition — and the launcher is the only thing on the desktop until a
          window opens. */}
      <div
        className="absolute inset-x-0 top-10 bottom-0 overflow-y-auto"
        onClick={handleDesktopClick}
      >
        <div className="mx-auto max-w-3xl px-4 pt-[9vh] pb-10">
          <IconGrid onOpen={handleOpenApp} />
        </div>
      </div>

      {/* Window coordinates are viewport coordinates, so this layer spans the
          whole desktop and only the windows themselves capture the pointer. */}
      <main className="pointer-events-none absolute inset-0" aria-label="应用窗口">
        {openWindows.map((instance) => (
          <Window
            key={instance.id}
            instance={instance}
            focused={instance.id === focusedId}
            stacked={stacked}
            stackIndex={stackIndexById.get(instance.id) ?? 0}
            revealed={desktopRevealed}
            parked={instance.mode === "minimized" || (stacked && instance.id !== topStackedId)}
          />
        ))}
      </main>

      <Dock visible={dockVisible} onToggleVisible={toggleDock} />
    </div>
  );
}
