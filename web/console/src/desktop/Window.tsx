import { memo, Suspense, useCallback, useMemo } from "react";
import { Maximize2, Minus, Minimize2, X } from "lucide-react";

import { AppGlyph } from "@/apps/AppGlyph";
import type { AppManifest } from "@/apps/types";
import { AppErrorBoundary } from "@/components/common/error-boundary";
import { LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";

import { revealTranslation, snapRect, type ResizeEdge } from "./geometry";
import { useWindowInteraction } from "./useWindowInteraction";
import { WindowVisibilityContext } from "./window-visibility";
import { useWindowStore, type WindowInstance } from "./window-store";

const RESIZE_HANDLES: Array<{ edge: ResizeEdge; className: string }> = [
  { edge: "n", className: "top-0 right-3 left-3 h-1.5 cursor-ns-resize" },
  { edge: "s", className: "right-3 bottom-0 left-3 h-1.5 cursor-ns-resize" },
  { edge: "w", className: "top-3 bottom-3 left-0 w-1.5 cursor-ew-resize" },
  { edge: "e", className: "top-3 right-0 bottom-3 w-1.5 cursor-ew-resize" },
  { edge: "nw", className: "top-0 left-0 size-3 cursor-nwse-resize" },
  { edge: "ne", className: "top-0 right-0 size-3 cursor-nesw-resize" },
  { edge: "sw", className: "bottom-0 left-0 size-3 cursor-nesw-resize" },
  { edge: "se", className: "right-0 bottom-0 size-3 cursor-nwse-resize" },
];

/** Windows stack above the desktop but always below the Dock and top bar. */
const BASE_WINDOW_Z_INDEX = 10;

/*
 * Memoized, because the desktop above it re-renders for reasons that have
 * nothing to do with any window.
 *
 * The Cluster event stream lands on the Desktop component — a Server restart
 * brings a fleet back one Agent at a time, and each event is a state update up
 * there. Without this, every one of them reconciles every open window frame.
 * The store hands back the same `instance` object for a window that did not
 * change, so the comparison is a reference check and the answer is usually no.
 */
export const Window = memo(function Window({
  instance,
  focused,
  stacked,
  stackIndex,
  revealed,
  parked,
  manifest,
}: {
  instance: WindowInstance;
  focused: boolean;
  /** Narrow viewports stack windows full-size instead of floating them. */
  stacked: boolean;
  /** Position in the desktop stacking order, lowest first. */
  stackIndex: number;
  /** Show desktop: held off screen, keeping its place and everything in it. */
  revealed: boolean;
  /** Not drawn, but kept mounted so live application state survives switching. */
  parked: boolean;
  manifest?: AppManifest;
}) {
  const bounds = useWindowStore((state) => state.bounds);
  const viewport = useWindowStore((state) => state.viewport);
  const focusWindow = useWindowStore((state) => state.focusWindow);
  const closeWindow = useWindowStore((state) => state.closeWindow);
  const minimizeWindow = useWindowStore((state) => state.minimizeWindow);
  const toggleMaximize = useWindowStore((state) => state.toggleMaximize);
  const setWindowRect = useWindowStore((state) => state.setWindowRect);
  const openWindow = useWindowStore((state) => state.openWindow);

  const interaction = useWindowInteraction({
    rect: instance.rect,
    viewport,
    disabled: parked || stacked || instance.mode === "maximized",
    onStart: () => focusWindow(instance.id),
    onCommit: (rect) => setWindowRect(instance.id, rect),
    onSnap: (zone) => {
      if (zone === "maximize") {
        if (instance.mode !== "maximized") {
          toggleMaximize(instance.id);
        }
        return;
      }
      setWindowRect(instance.id, snapRect(zone, viewport));
    },
  });

  const handleOpenApp = useCallback(
    (appId: string, options?: { title?: string }) => {
      openWindow(appId, options);
    },
    [openWindow],
  );

  const appProps = useMemo(
    () => (manifest ? { windowId: instance.id, manifest, openApp: handleOpenApp } : null),
    [handleOpenApp, instance.id, manifest],
  );

  // A link that names where an application should open restarts it, and the
  // generation is what makes React mount it again: the applications read their
  // target as they mount, so an already-open window has to start over to honour
  // one. The window itself — its place, its size, its stacking — is untouched.
  const generation = instance.generation;

  /*
   * The application is held apart from the frame, and this is what keeps a drag
   * cheap on the React side.
   *
   * Dragging updates a preview rectangle on this component once per animation
   * frame. Written inline, the application below it would be re-rendered sixty
   * times a second for the whole length of the drag — every row of every table
   * reconciled to produce the identical output, while the only thing that
   * actually changed was a transform on the frame. Memoized, the element is the
   * same reference each time and React skips the subtree entirely.
   *
   * Every dependency here is already stable: the manifest is a module constant,
   * the id does not change for the life of the window, and `openWindow` comes
   * from the store.
   */
  const content = useMemo(
    () =>
      manifest && appProps ? (
        <AppErrorBoundary key={generation} label={manifest.title}>
          <Suspense fallback={<LoadingState label="正在加载应用…" />}>
            <manifest.entry {...appProps} />
          </Suspense>
        </AppErrorBoundary>
      ) : null,
    [appProps, generation, manifest],
  );

  if (!manifest || !appProps) {
    return null;
  }

  /*
   * A dragged window is moved by transform; everything else is laid out.
   *
   * `left`/`top` place the window, and rewriting them on every pointer frame
   * lays the window out again each time — and the box being laid out holds a
   * whole application, tables and all. A drag changes nothing about that box
   * except where it is, which `translate` says without touching layout or paint,
   * so the moving is handled on the compositor.
   *
   * A resize genuinely changes the box, so it keeps the live rectangle and the
   * relayout that has to come with it.
   */
  const dragged = interaction.isDragging ? interaction.previewRect : null;
  const rect = dragged ? instance.rect : (interaction.previewRect ?? instance.rect);
  const titleId = `window-title-${instance.id}`;

  const geometry = stacked
    ? { left: bounds.x, top: bounds.y, width: bounds.width, height: bounds.height }
    : { left: rect.x, top: rect.y, width: rect.width, height: rect.height };
  const zIndex = BASE_WINDOW_Z_INDEX + stackIndex;
  // Full screen means full screen: a rounded outline against the desktop edge
  // would only advertise a frame that is no longer there.
  const fullscreen = !stacked && instance.mode === "maximized";
  // Which way the window leaves is read from where it is actually drawn, which
  // in a stacked layout is the desktop bounds rather than its own rect.
  const reveal = revealed
    ? revealTranslation(
        { x: geometry.left, y: geometry.top, width: geometry.width, height: geometry.height },
        viewport,
      )
    : null;
  // What the clamped preview has moved from the placed rectangle: the pointer
  // delta, after the screen edges have had their say.
  const dragOffset = dragged ? { x: dragged.x - rect.x, y: dragged.y - rect.y } : null;

  return (
    <>
      {interaction.snapPreviewRect ? (
        <div
          aria-hidden
          className="rounded-window border-primary/60 bg-primary/10 pointer-events-none absolute border-2"
          style={{
            left: interaction.snapPreviewRect.x,
            top: interaction.snapPreviewRect.y,
            width: interaction.snapPreviewRect.width,
            height: interaction.snapPreviewRect.height,
            zIndex,
          }}
        />
      ) : null}

      <section
        role="dialog"
        aria-labelledby={titleId}
        aria-modal={false}
        tabIndex={-1}
        data-window-id={instance.id}
        data-app-id={instance.appId}
        data-focused={focused}
        aria-hidden={parked || undefined}
        className={cn(
          "bg-surface zke-pointer-layer absolute flex flex-col overflow-hidden border",
          fullscreen ? "rounded-none" : "rounded-window",
          focused ? "border-border-strong shadow-window-focused" : "border-border shadow-window",
          !interaction.isInteracting && "zke-window-motion",
          // Do not apply a CSS filter to the whole unfocused window. Both xterm
          // views render through canvas, and moving that canvas in and out of a
          // filtered compositing layer can leave it black after refocusing.
          // Border and shadow already carry the focus distinction.
          // Parked at the edge, and out of the pointer's way with it. The strip
          // still showing is meant to be clicked, and taking no pointer is how
          // it works: the click falls through to the desktop, which is what
          // brings every window back.
          revealed && "pointer-events-none",
        )}
        style={{
          ...geometry,
          zIndex,
          display: parked ? "none" : undefined,
          // Two properties, because only one of them is transitioned — see
          // `.zke-window-motion`. Both are composited, and they cannot collide:
          // a revealed window takes no pointer, so nothing can be dragged while
          // the desk is cleared.
          translate: reveal ? `${reveal.x}px ${reveal.y}px` : undefined,
          transform: dragOffset ? `translate(${dragOffset.x}px, ${dragOffset.y}px)` : undefined,
        }}
        onPointerDownCapture={() => focusWindow(instance.id)}
      >
        {/*
         * `bg-surface-muted/50` rather than the translucent overlay this used to
         * carry: the overlay was paired with `backdrop-blur-xl`, and a backdrop
         * filter over an opaque window body has nothing behind it to filter — it
         * bought a compositing layer per window and rendered as plain white. A
         * barely-tinted fill is what actually separates chrome from content.
         */}
        <header
          className={cn(
            "border-border bg-surface-muted/50 flex h-10 shrink-0 items-center gap-2 border-b px-2.5",
            !stacked && "cursor-grab active:cursor-grabbing",
          )}
          onDoubleClick={() => (stacked ? undefined : toggleMaximize(instance.id))}
          {...(stacked ? {} : interaction.dragHandleProps)}
        >
          {/* The one spot of colour in the frame, and it follows focus: on a
              desktop of open windows, which one is live should be readable from
              the title bar alone. */}
          <AppGlyph
            manifest={manifest}
            className={cn(
              "size-4 shrink-0 transition-colors duration-150",
              focused ? "text-primary" : "text-subtle-foreground",
            )}
          />
          <h2 id={titleId} className="text-foreground shrink-0 text-[13px] font-medium">
            {instance.title}
          </h2>

          <div
            className="ml-auto flex shrink-0 items-center gap-0.5"
            onPointerDown={(event) => event.stopPropagation()}
          >
            {/* Frame controls sit back until they are wanted: at rest they are
                the faintest ink in the bar, and hover is what brings them up. */}
            <Button
              size="icon-sm"
              variant="ghost"
              className="text-subtle-foreground"
              aria-label="最小化窗口"
              onClick={() => minimizeWindow(instance.id)}
            >
              <Minus />
            </Button>
            <Button
              size="icon-sm"
              variant="ghost"
              className="text-subtle-foreground"
              aria-label={instance.mode === "maximized" ? "还原窗口" : "最大化窗口"}
              disabled={stacked}
              onClick={() => toggleMaximize(instance.id)}
            >
              {instance.mode === "maximized" ? <Minimize2 /> : <Maximize2 />}
            </Button>
            <Button
              size="icon-sm"
              variant="ghost"
              className="text-subtle-foreground hover:bg-danger-surface hover:text-danger"
              aria-label="关闭窗口"
              onClick={() => closeWindow(instance.id)}
            >
              <X />
            </Button>
          </div>
        </header>

        {/*
         * The provider sits outside the memoized element on purpose. `content`
         * is the same reference from one render to the next, so parking or
         * restoring the window does not reconcile the application — only the
         * handful of hooks that actually read this value are woken, which is
         * exactly the set that has a poll to pause.
         */}
        {/*
         * The window is the container every layout inside it should be asking
         * about. Viewport breakpoints answer a different question — how wide the
         * screen is — and on a desktop of resizable windows the two are only
         * accidentally the same number: a 420px window on a 2560px display would
         * otherwise lay itself out as if it had the whole screen. Declaring the
         * container here means a view narrowed by a drag and a view narrowed by
         * a phone are the same case, handled once.
         */}
        <div className="@container min-h-0 flex-1 overflow-hidden">
          <WindowVisibilityContext.Provider value={!parked}>
            {content}
          </WindowVisibilityContext.Provider>
        </div>

        {!stacked && instance.mode !== "maximized"
          ? RESIZE_HANDLES.map((handle) => (
              <div
                key={handle.edge}
                aria-hidden
                className={cn("absolute touch-none select-none", handle.className)}
                {...interaction.resizeHandleProps(handle.edge)}
              />
            ))
          : null}
      </section>
    </>
  );
});
