import type { ComponentType, LazyExoticComponent } from "react";
import type { LucideIcon } from "lucide-react";

import type { Permission } from "@/api/types";

export type AppAvailability =
  { state: "available" } | { state: "planned"; phase: number; plannedCapabilities: string[] };

/**
 * Applications read the selected Project from `useScopeStore` rather than
 * receiving it here: there is one Console-wide Project, not one per window.
 */
export type AppComponentProps = {
  windowId: string;
  manifest: AppManifest;
  openApp: (appId: string, options?: { title?: string }) => void;
};

/**
 * An available application's icon colour. Colour is what makes a launcher read
 * as a row of applications rather than a row of buttons, so it belongs to the
 * application's identity — not to the component that happens to draw it.
 *
 * Planned applications deliberately have none: on a launcher where every real
 * application is saturated, an unlit tile says "not yet" before any caption
 * under it does.
 */
export type AppAccent =
  "blue" | "cyan" | "violet" | "emerald" | "amber" | "rose" | "slate" | "steel";

export type AppManifest = {
  id: string;
  title: string;
  description: string;
  icon: LucideIcon;
  accent?: AppAccent;
  /** Entry is shown when the user holds any of these permissions anywhere. */
  requiredPermissions: Permission[];
  /**
   * Entry is shown only to a global administrator.
   *
   * Asked as a role rather than through `requiredPermissions` because the
   * Server guards these routes with `RequireGlobalAdministrator`, and the
   * Server reserves that role to the people who already hold it — a custom role
   * carrying every permission is still not a global administrator. Gating by
   * permission would put an application on the desktop whose every request the
   * Server refuses.
   */
  requiresGlobalAdmin?: boolean;
  availability: AppAvailability;
  defaultSize: { width: number; height: number };
  entry: LazyExoticComponent<ComponentType<AppComponentProps>>;
};
