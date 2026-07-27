import { useMemo } from "react";
import { ChevronDown, ChevronUp } from "lucide-react";

import { findAppManifest } from "@/apps/registry";
import { HintTooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/cn";

import { useWindowStore, type WindowInstance } from "./window-store";

/**
 * Dock for open windows.
 *
 * One button per window, and since an application owns at most one window, one
 * button per running application: it focuses that window, minimizes it when it
 * is already focused, and restores it when it is minimized.
 *
 * A handle above it collapses and restores the whole bar on demand. Hiding is
 * always the operator's decision — never pointer proximity, and never a side
 * effect of a window going full screen — so the Dock can neither disappear
 * under the cursor nor pop up over the work area unasked.
 */
export function Dock({
  visible,
  onToggleVisible,
}: {
  visible: boolean;
  onToggleVisible: () => void;
}) {
  const windows = useWindowStore((state) => state.windows);
  const order = useWindowStore((state) => state.order);
  const focusedId = useWindowStore((state) => state.focusedId);
  const focusWindow = useWindowStore((state) => state.focusWindow);
  const minimizeWindow = useWindowStore((state) => state.minimizeWindow);
  const restoreWindow = useWindowStore((state) => state.restoreWindow);

  const items = useMemo(
    () =>
      order
        .map((id) => windows[id])
        .filter((instance): instance is WindowInstance => Boolean(instance)),
    [order, windows],
  );

  if (items.length === 0) {
    return null;
  }

  return (
    <div className="pointer-events-none absolute inset-x-0 bottom-0 z-500 flex flex-col items-center gap-1.5 pb-3">
      <button
        type="button"
        onClick={onToggleVisible}
        aria-expanded={visible}
        aria-label={visible ? "隐藏任务栏" : "显示任务栏"}
        className="border-border bg-surface-overlay text-subtle-foreground hover:text-foreground pointer-events-auto flex h-6 w-16 items-center justify-center rounded-full border backdrop-blur-md"
      >
        {visible ? (
          <ChevronDown className="size-3.5" aria-hidden />
        ) : (
          <ChevronUp className="size-3.5" aria-hidden />
        )}
      </button>

      {/* Unmounted rather than hidden: the bar's own `flex` would win over the
          `hidden` attribute and leave it on screen. */}
      {!visible ? null : (
        <nav
          aria-label="运行中的应用"
          className="border-border bg-surface-overlay shadow-window pointer-events-auto flex max-w-[calc(100vw-2rem)] items-center gap-1 overflow-x-auto rounded-2xl border p-1.5 backdrop-blur-md"
        >
          {items.map((instance) => {
            const manifest = findAppManifest(instance.appId);
            if (!manifest) {
              return null;
            }
            const Icon = manifest.icon;
            const focused = instance.id === focusedId;
            const minimized = instance.mode === "minimized";

            return (
              <HintTooltip key={instance.id} label={instance.title}>
                <button
                  type="button"
                  aria-label={instance.title}
                  aria-current={focused ? "true" : undefined}
                  className={cn(
                    "relative flex size-11 flex-col items-center justify-center rounded-xl transition-colors",
                    focused ? "bg-primary-surface text-primary" : "text-muted-foreground",
                    "hover:bg-surface-muted hover:text-foreground",
                  )}
                  onClick={() => {
                    if (minimized) {
                      restoreWindow(instance.id);
                    } else if (focused) {
                      minimizeWindow(instance.id);
                    } else {
                      focusWindow(instance.id);
                    }
                  }}
                >
                  <Icon className="size-5" aria-hidden />
                  <span
                    aria-hidden
                    className={cn(
                      "absolute bottom-1 h-1 rounded-full transition-all",
                      minimized ? "bg-subtle-foreground w-1" : "bg-primary w-3",
                    )}
                  />
                </button>
              </HintTooltip>
            );
          })}
        </nav>
      )}
    </div>
  );
}
