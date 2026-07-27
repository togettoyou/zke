import type { Capability, Permission } from "@/api/types";

/**
 * Scope a permission check is made against. It mirrors the Server's scope model
 * (`pkg/server/rbac`): a Project check needs the owning Tenant, because a
 * Tenant-scoped binding also covers that Tenant's Projects.
 */
export type CapabilityScope =
  | { type: "global" }
  | { type: "tenant"; tenantId: string | null }
  | { type: "project"; tenantId: string | null; projectId: string | null };

export const GLOBAL_SCOPE: CapabilityScope = { type: "global" };

/**
 * Mirrors `bindingApplies` in `pkg/server/rbac/service.go`:
 * - a global binding applies everywhere;
 * - a tenant binding applies to that Tenant and its Projects, never to global;
 * - a project binding applies to exactly one Project.
 */
function capabilityApplies(capability: Capability, scope: CapabilityScope): boolean {
  switch (capability.scope_type) {
    case "global":
      return true;
    case "tenant":
      if (scope.type === "global") {
        return false;
      }
      return Boolean(scope.tenantId) && capability.tenant_id === scope.tenantId;
    case "project":
      if (scope.type !== "project") {
        return false;
      }
      return (
        Boolean(scope.tenantId) &&
        Boolean(scope.projectId) &&
        capability.tenant_id === scope.tenantId &&
        capability.project_id === scope.projectId
      );
    default:
      return false;
  }
}

export type PermissionChecker = {
  /** True when the current user may perform `permission` in `scope`. */
  can: (permission: Permission, scope: CapabilityScope) => boolean;
  /** True when some binding grants `permission` in any scope. */
  canAnywhere: (permission: Permission) => boolean;
  /** True when the user holds a global admin binding. */
  isGlobalAdmin: boolean;
  capabilities: Capability[];
};

export function createPermissionChecker(capabilities: Capability[]): PermissionChecker {
  const can = (permission: Permission, scope: CapabilityScope): boolean =>
    capabilities.some(
      (capability) =>
        capability.permissions.includes(permission) && capabilityApplies(capability, scope),
    );

  const canAnywhere = (permission: Permission): boolean =>
    capabilities.some((capability) => capability.permissions.includes(permission));

  return {
    can,
    canAnywhere,
    isGlobalAdmin: capabilities.some(
      (capability) => capability.scope_type === "global" && capability.role === "admin",
    ),
    capabilities,
  };
}

export const EMPTY_PERMISSION_CHECKER: PermissionChecker = createPermissionChecker([]);

/**
 * UI gating only. The Server re-authorizes every request, so hiding an action
 * here is a usability decision, never a security control.
 */
export function describeCapability(capability: Capability): string {
  const role = capability.role === "admin" ? "管理员" : "只读";
  switch (capability.scope_type) {
    case "global":
      return `全局 ${role}`;
    case "tenant":
      return `租户 ${role}`;
    case "project":
      return `项目 ${role}`;
    default:
      return role;
  }
}
