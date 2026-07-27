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

export type AppManifest = {
  id: string;
  title: string;
  description: string;
  icon: LucideIcon;
  /** Entry is shown when the user holds any of these permissions anywhere. */
  requiredPermissions: Permission[];
  availability: AppAvailability;
  defaultSize: { width: number; height: number };
  entry: LazyExoticComponent<ComponentType<AppComponentProps>>;
};
