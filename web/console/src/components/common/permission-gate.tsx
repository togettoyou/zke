import type { ReactNode } from "react";

import type { Permission } from "@/api/types";
import { useSessionContext } from "@/auth/session-context";
import type { CapabilityScope } from "@/auth/capabilities";

/**
 * Hides UI that the current user cannot use.
 *
 * This is a usability filter only. The Server authorizes every request again,
 * so a bypassed gate cannot grant access — it would only produce a 403.
 */
export function PermissionGate({
  permission,
  scope,
  children,
  fallback = null,
}: {
  permission: Permission;
  scope: CapabilityScope;
  children: ReactNode;
  fallback?: ReactNode;
}) {
  const { permissions } = useSessionContext();
  return <>{permissions.can(permission, scope) ? children : fallback}</>;
}
