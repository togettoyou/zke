import { useCallback, useEffect, useRef, useState } from "react";

import { useClusterEvents } from "@/api/events";
import { useSessionContext } from "@/auth/session-context";
import { cn } from "@/lib/cn";
import { useScopeStore } from "@/scope/scope-store";

import { Dock } from "./Dock";
import { IconGrid } from "./IconGrid";
import { TopBar } from "./TopBar";
import { Window } from "./Window";
import { clearDesktopState, loadDesktopState, saveDesktopState } from "./persistence";
import { toPersistedDesktop, useWindowStore } from "./window-store";

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
  const immersive = !stacked && focusedWindow?.mode === "maximized";

  const toggleDock = useCallback(() => setDockVisible((shown) => !shown), []);

  useEffect(() => {
    const applyViewport = () => {
      setViewport({ width: window.innerWidth, height: window.innerHeight });
      setStacked(window.innerWidth < STACKED_BREAKPOINT);
    };
    applyViewport();
    window.addEventListener("resize", applyViewport);
    return () => window.removeEventListener("resize", applyViewport);
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

  const handleResetDesktop = useCallback(() => {
    closeAll();
    resetScope();
    if (userId) {
      clearDesktopState(userId);
    }
  }, [closeAll, resetScope, userId]);

  // Stacking is derived from the focus counter, then flattened into a small
  // bounded range so a window can never render above the Dock or the top bar.
  const visibleWindows = order
    .map((id) => windows[id])
    .filter((instance) => Boolean(instance) && instance!.mode !== "minimized")
    .sort((left, right) => left!.zIndex - right!.zIndex);

  const topStackedId = stacked ? (visibleWindows[visibleWindows.length - 1]?.id ?? null) : null;

  return (
    <div className="from-desktop-from to-desktop-to relative h-full w-full overflow-hidden bg-linear-to-br">
      {/* Light sources and a faint grid, so the backdrop reads as a surface the
          windows rest on rather than a page they are printed on. */}
      <div aria-hidden className="zke-desktop-surface pointer-events-none absolute inset-0" />

      <TopBar
        streamState={streamState}
        lastEventAt={lastEventAt}
        onOpenApp={handleOpenApp}
        onResetDesktop={handleResetDesktop}
        className={cn(
          "transition-transform duration-200 ease-out",
          immersive && "pointer-events-none -translate-y-full",
        )}
      />

      {/* Dissolves the lower edge of the top bar into the desktop. It travels
          with the bar so full screen takes both away together. */}
      <div
        aria-hidden
        className={cn(
          "zke-topbar-fade pointer-events-none absolute inset-x-0 top-11 z-0 h-4 transition-transform duration-200 ease-out",
          immersive && "-translate-y-[3.75rem]",
        )}
      />

      <div className="absolute inset-x-0 top-11 bottom-0 overflow-y-auto">
        <div className="max-w-2xl">
          <IconGrid onOpen={handleOpenApp} />
        </div>
      </div>

      {/* Window coordinates are viewport coordinates, so this layer spans the
          whole desktop and only the windows themselves capture the pointer. */}
      <main className="pointer-events-none absolute inset-0" aria-label="应用窗口">
        {visibleWindows.map((instance, index) =>
          instance && (!stacked || instance.id === topStackedId) ? (
            <Window
              key={instance.id}
              instance={instance}
              focused={instance.id === focusedId}
              stacked={stacked}
              stackIndex={index}
            />
          ) : null,
        )}
      </main>

      <Dock visible={dockVisible} onToggleVisible={toggleDock} />
    </div>
  );
}
