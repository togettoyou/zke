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
 * An available application's icon colour, named after the application that owns
 * it rather than after the hue. Colour is what makes a launcher read as a row of
 * applications rather than a row of buttons, so it belongs to the application's
 * identity — not to the component that happens to draw it, and not to a hue that
 * the next retune can invalidate.
 *
 * It stays a field of its own instead of being read off `id` because the fill
 * classes have to exist as literal strings for Tailwind to generate them, and a
 * declared accent is what makes a missing one a type error rather than an
 * untinted tile.
 *
 * Planned applications deliberately have none: on a launcher where every real
 * application is saturated, an unlit tile says "not yet" before any caption
 * under it does.
 */
export type AppAccent =
  | "cluster-access"
  | "container-service"
  | "helm"
  | "resources"
  | "access-audit"
  | "monitoring"
  | "platform"
  | "settings"
  | "aiops"
  | "terminal";

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
  /**
   * Entry is shown only where the deployment has this capability switched on.
   *
   * Distinct from `requiredPermissions`, which is about the person: this is
   * about whether the platform has anything behind the icon at all. An
   * application the administrator has not configured is left off the desktop
   * rather than opened onto a window that can only report its own absence.
   */
  requiresPlatformFeature?: "aiops";
  availability: AppAvailability;
  defaultSize: { width: number; height: number };
  entry: LazyExoticComponent<ComponentType<AppComponentProps>>;
};
