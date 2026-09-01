import { cn } from "@/lib/cn";

import type { AppAccent, AppManifest } from "./types";

/**
 * A fill and a white glyph, one key per application.
 *
 * The keys are the applications themselves rather than colour names. They used
 * to be hues, and the palette was then reassigned without renaming them, which
 * left `fuchsia` holding a blue and `amber` a neutral grey — a name that has to
 * be disbelieved is worse than no name. An application's identity outlives its
 * colour, so the identity is what the key is.
 *
 * The hex lives in `theme.css`; this file only says which token each application
 * wears.
 *
 * Tiles in the same pale tint with the same accent glyph do not read as separate
 * applications — they read as one placeholder repeated, which is the language of
 * a wireframe. Colour is what tells them apart at a glance.
 */
const ACCENT_FILL: Record<AppAccent, string> = {
  "cluster-access":
    "bg-linear-to-b from-[var(--app-cluster-access-from)] to-[var(--app-cluster-access-to)]",
  "container-service":
    "bg-linear-to-b from-[var(--app-container-service-from)] to-[var(--app-container-service-to)]",
  helm: "bg-linear-to-b from-[var(--app-helm-from)] to-[var(--app-helm-to)]",
  resources: "bg-linear-to-b from-[var(--app-resources-from)] to-[var(--app-resources-to)]",
  "access-audit":
    "bg-linear-to-b from-[var(--app-access-audit-from)] to-[var(--app-access-audit-to)]",
  monitoring: "bg-linear-to-b from-[var(--app-monitoring-from)] to-[var(--app-monitoring-to)]",
  platform: "bg-linear-to-b from-[var(--app-platform-from)] to-[var(--app-platform-to)]",
  settings: "bg-linear-to-b from-[var(--app-settings-from)] to-[var(--app-settings-to)]",
  aiops: "bg-linear-to-b from-[var(--app-aiops-from)] to-[var(--app-aiops-to)]",
  terminal: "bg-linear-to-b from-[var(--app-terminal-from)] to-[var(--app-terminal-to)]",
  "custom-apps": "bg-linear-to-b from-[var(--app-custom-apps-from)] to-[var(--app-custom-apps-to)]",
};

/**
 * `drop-shadow` rather than `box-shadow` on purpose: the tiles already carry a
 * box shadow for their lit inner edge, and a hover rule setting `box-shadow`
 * would replace that outright instead of adding to it.
 */
const ACCENT_GLOW: Record<AppAccent, string> = {
  "cluster-access": "group-hover:drop-shadow-[0_9px_16px_var(--app-cluster-access-glow)]",
  "container-service": "group-hover:drop-shadow-[0_9px_16px_var(--app-container-service-glow)]",
  helm: "group-hover:drop-shadow-[0_9px_16px_var(--app-helm-glow)]",
  resources: "group-hover:drop-shadow-[0_9px_16px_var(--app-resources-glow)]",
  "access-audit": "group-hover:drop-shadow-[0_9px_16px_var(--app-access-audit-glow)]",
  monitoring: "group-hover:drop-shadow-[0_9px_16px_var(--app-monitoring-glow)]",
  platform: "group-hover:drop-shadow-[0_9px_16px_var(--app-platform-glow)]",
  settings: "group-hover:drop-shadow-[0_9px_16px_var(--app-settings-glow)]",
  aiops: "group-hover:drop-shadow-[0_9px_16px_var(--app-aiops-glow)]",
  terminal: "group-hover:drop-shadow-[0_9px_16px_var(--app-terminal-glow)]",
  "custom-apps": "group-hover:drop-shadow-[0_9px_16px_var(--app-custom-apps-glow)]",
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
  if (manifest.customApplication?.logo_url) {
    return "border-app-logo-border bg-app-logo-surface border";
  }
  return cn("text-white", ACCENT_FILL[manifest.accent ?? "cluster-access"]);
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
    manifest.availability.state === "planned"
      ? null
      : manifest.customApplication?.logo_url
        ? "group-hover:drop-shadow-[0_9px_16px_var(--app-logo-glow)]"
        : ACCENT_GLOW[manifest.accent ?? "cluster-access"],
  );
}
