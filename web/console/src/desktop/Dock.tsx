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
    <div className="group pointer-events-none absolute inset-x-0 bottom-0 z-500 flex flex-col items-center gap-1.5 pb-3">
      {/*
       * The handle recedes while the Dock is up and the pointer is elsewhere —
       * a control for a thing already on screen does not need to be on screen
       * too. It keeps its space rather than being unmounted, so revealing it
       * cannot nudge the Dock, and it returns on keyboard focus as well as on
       * hover. While the Dock is hidden it is the only way back, so it stays.
       */}
      <button
        type="button"
        onClick={onToggleVisible}
        aria-expanded={visible}
        aria-label={visible ? "隐藏任务栏" : "显示任务栏"}
        className={cn(
          "zke-focus zke-dock border-border/60 text-subtle-foreground hover:text-foreground pointer-events-auto flex h-6 w-16 items-center justify-center rounded-full border transition-[color,opacity] duration-200",
          visible ? "opacity-0 group-hover:opacity-100 focus-visible:opacity-100" : "opacity-100",
        )}
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
          className="zke-dock border-border/60 pointer-events-auto flex max-w-[calc(100vw-2rem)] items-center gap-1 overflow-x-auto rounded-[18px] border p-2"
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
                    "zke-focus relative grid size-11 place-items-center rounded-[14px] transition-[transform,background-color,color] duration-150",
                    "hover:bg-surface/50 hover:-translate-y-0.5 active:translate-y-0",
                    // Which window is focused is the indicator's job, below.
                    // Giving the icon a filled chip as well says it twice, and
                    // the chip is the louder of the two — it punches a solid
                    // shape through glass that the rest of the Dock keeps clear.
                    focused ? "text-primary" : "text-muted-foreground hover:text-foreground",
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
                  <Icon className="size-5" strokeWidth={1.75} aria-hidden />
                  {/* Three states in one mark: put away, open, and the one being
                      worked in. Length and weight carry all three, so it reads
                      the same to anyone who cannot separate the two colours. */}
                  <span
                    aria-hidden
                    className={cn(
                      "absolute bottom-0.5 h-[3px] rounded-full transition-all duration-200",
                      minimized
                        ? "bg-subtle-foreground w-[3px]"
                        : focused
                          ? "bg-primary w-4"
                          : "bg-muted-foreground/60 w-2.5",
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
