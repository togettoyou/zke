import { cn } from "@/lib/cn";

import type { AppAccent, AppManifest } from "./types";

/**
 * A fill and a white glyph, one key per application.
 *
 * One key per application literally: `blue` is what an application without an
 * accent falls back to, so it is taken as well. The keys are stable identifiers
 * rather than colour names — the values behind several of them have been
 * reassigned since, so `fuchsia` is a blue and `amber` a neutral grey. The hex
 * lives in `theme.css`; nothing here should be read as a claim about the hue.
 *
 * Tiles in the same pale tint with the same accent glyph do not read as separate
 * applications — they read as one placeholder repeated, which is the language of
 * a wireframe. Colour is what tells them apart at a glance.
 */
const ACCENT_FILL: Record<AppAccent, string> = {
  blue: "bg-linear-to-b from-[var(--app-blue-from)] to-[var(--app-blue-to)]",
  cyan: "bg-linear-to-b from-[var(--app-cyan-from)] to-[var(--app-cyan-to)]",
  violet: "bg-linear-to-b from-[var(--app-violet-from)] to-[var(--app-violet-to)]",
  emerald: "bg-linear-to-b from-[var(--app-emerald-from)] to-[var(--app-emerald-to)]",
  amber: "bg-linear-to-b from-[var(--app-amber-from)] to-[var(--app-amber-to)]",
  rose: "bg-linear-to-b from-[var(--app-rose-from)] to-[var(--app-rose-to)]",
  slate: "bg-linear-to-b from-[var(--app-slate-from)] to-[var(--app-slate-to)]",
  steel: "bg-linear-to-b from-[var(--app-steel-from)] to-[var(--app-steel-to)]",
  fuchsia: "bg-linear-to-b from-[var(--app-fuchsia-from)] to-[var(--app-fuchsia-to)]",
};

/**
 * `drop-shadow` rather than `box-shadow` on purpose: the tiles already carry a
 * box shadow for their lit inner edge, and a hover rule setting `box-shadow`
 * would replace that outright instead of adding to it.
 */
const ACCENT_GLOW: Record<AppAccent, string> = {
  blue: "group-hover:drop-shadow-[0_9px_16px_var(--app-blue-glow)]",
  cyan: "group-hover:drop-shadow-[0_9px_16px_var(--app-cyan-glow)]",
  violet: "group-hover:drop-shadow-[0_9px_16px_var(--app-violet-glow)]",
  emerald: "group-hover:drop-shadow-[0_9px_16px_var(--app-emerald-glow)]",
  amber: "group-hover:drop-shadow-[0_9px_16px_var(--app-amber-glow)]",
  rose: "group-hover:drop-shadow-[0_9px_16px_var(--app-rose-glow)]",
  slate: "group-hover:drop-shadow-[0_9px_16px_var(--app-slate-glow)]",
  steel: "group-hover:drop-shadow-[0_9px_16px_var(--app-steel-glow)]",
  fuchsia: "group-hover:drop-shadow-[0_9px_16px_var(--app-fuchsia-glow)]",
};

/**
 * The application's own face, wherever it is drawn — the launcher and the Dock
 * alike. It lives here rather than in either of them because an icon that
 * changes between the two places is not an identity: the whole reason a Dock
 * works is that the thing running in it is recognisably the thing that was
 * clicked.
 *
 * Callers supply size, radius and interaction; this supplies fill and ink.
 */
export function appFaceClass(manifest: AppManifest): string {
  if (manifest.availability.state === "planned") {
    return "border-border bg-surface-muted/60 text-subtle-foreground border";
  }
  return cn("text-white", ACCENT_FILL[manifest.accent ?? "blue"]);
}

/**
 * The response to the pointer: the icon stays exactly where it is and gains
 * presence instead of position.
 *
 * A hover that translates the icon upwards is the most literal reading of
 * "respond", and it is the wrong one — an icon has a place, and lifting it out
 * of that place reads as a hop rather than as attention. Growing very slightly
 * and blooming in its own colour reads as coming forward under a light, which is
 * what is actually happening.
 *
 * Expects a plain `group` on the element being hovered.
 */
export function appHoverClass(manifest: AppManifest): string {
  return cn(
    "ease-lift transition-[transform,filter] duration-250",
    "group-hover:scale-[1.07] group-active:scale-[0.98]",
    manifest.availability.state === "planned" ? null : ACCENT_GLOW[manifest.accent ?? "blue"],
  );
}
