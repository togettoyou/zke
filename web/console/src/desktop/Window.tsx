import { Suspense, useCallback, useMemo } from "react";
import { Maximize2, Minus, Minimize2, X } from "lucide-react";

import { findAppManifest } from "@/apps/registry";
import { AppErrorBoundary } from "@/components/common/error-boundary";
import { LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";

import { snapRect, type ResizeEdge } from "./geometry";
import { useWindowInteraction } from "./useWindowInteraction";
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

export function Window({
  instance,
  focused,
  stacked,
  stackIndex,
}: {
  instance: WindowInstance;
  focused: boolean;
  /** Narrow viewports stack windows full-size instead of floating them. */
  stacked: boolean;
  /** Position in the desktop stacking order, lowest first. */
  stackIndex: number;
}) {
  const bounds = useWindowStore((state) => state.bounds);
  const viewport = useWindowStore((state) => state.viewport);
  const focusWindow = useWindowStore((state) => state.focusWindow);
  const closeWindow = useWindowStore((state) => state.closeWindow);
  const minimizeWindow = useWindowStore((state) => state.minimizeWindow);
  const toggleMaximize = useWindowStore((state) => state.toggleMaximize);
  const setWindowRect = useWindowStore((state) => state.setWindowRect);
  const openWindow = useWindowStore((state) => state.openWindow);

  const manifest = findAppManifest(instance.appId);

  const interaction = useWindowInteraction({
    rect: instance.rect,
    viewport,
    disabled: stacked || instance.mode === "maximized",
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

  if (!manifest || !appProps) {
    return null;
  }

  const rect = interaction.previewRect ?? instance.rect;
  const AppComponent = manifest.entry;
  const Icon = manifest.icon;
  const titleId = `window-title-${instance.id}`;

  const geometry = stacked
    ? { left: bounds.x, top: bounds.y, width: bounds.width, height: bounds.height }
    : { left: rect.x, top: rect.y, width: rect.width, height: rect.height };
  const zIndex = BASE_WINDOW_Z_INDEX + stackIndex;
  // Full screen means full screen: a rounded outline against the desktop edge
  // would only advertise a frame that is no longer there.
  const fullscreen = !stacked && instance.mode === "maximized";

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
        className={cn(
          "bg-surface pointer-events-auto absolute flex flex-col overflow-hidden border",
          fullscreen ? "rounded-none" : "rounded-window",
          focused ? "border-border-strong shadow-window-focused" : "border-border shadow-window",
          !interaction.isInteracting && "transition-[box-shadow,border-color] duration-150",
          !focused && "saturate-[0.92]",
        )}
        style={{ ...geometry, zIndex }}
        onPointerDownCapture={() => focusWindow(instance.id)}
      >
        <header
          className={cn(
            "border-border bg-surface-overlay flex h-10 shrink-0 items-center gap-2 border-b px-2.5 backdrop-blur-xl",
            !stacked && "cursor-grab active:cursor-grabbing",
          )}
          onDoubleClick={() => (stacked ? undefined : toggleMaximize(instance.id))}
          {...(stacked ? {} : interaction.dragHandleProps)}
        >
          <Icon className="text-primary size-4 shrink-0" strokeWidth={1.75} aria-hidden />
          <h2
            id={titleId}
            className="text-foreground shrink-0 text-[13px] font-semibold tracking-tight"
          >
            {instance.title}
          </h2>

          <div
            className="ml-auto flex shrink-0 items-center gap-0.5"
            onPointerDown={(event) => event.stopPropagation()}
          >
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label="最小化窗口"
              onClick={() => minimizeWindow(instance.id)}
            >
              <Minus />
            </Button>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={instance.mode === "maximized" ? "还原窗口" : "最大化窗口"}
              disabled={stacked}
              onClick={() => toggleMaximize(instance.id)}
            >
              {instance.mode === "maximized" ? <Minimize2 /> : <Maximize2 />}
            </Button>
            <Button
              size="icon-sm"
              variant="ghost"
              className="hover:bg-danger-surface hover:text-danger"
              aria-label="关闭窗口"
              onClick={() => closeWindow(instance.id)}
            >
              <X />
            </Button>
          </div>
        </header>

        <div className="min-h-0 flex-1 overflow-hidden">
          <AppErrorBoundary label={manifest.title}>
            <Suspense fallback={<LoadingState label="正在加载应用…" />}>
              <AppComponent {...appProps} />
            </Suspense>
          </AppErrorBoundary>
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
}
