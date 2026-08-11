import type { Permission } from "@/api/types";
import type { CapabilityScope, PermissionChecker } from "@/auth/capabilities";

export const AGENT_NAMESPACE = "zke-system";

export function protectedNamespacePermission(namespace: string): Permission | null {
  if (namespace === AGENT_NAMESPACE) {
    return "cluster.agent_namespace.manage";
  }
  if (namespace.startsWith("kube-")) {
    return "cluster.system_namespace.manage";
  }
  return null;
}

export function namespaceMutationPermission(namespace: string, ordinary: Permission): Permission {
  return protectedNamespacePermission(namespace) ?? ordinary;
}

export function canUseProtectedNamespace(
  permissions: PermissionChecker,
  namespace: string,
  scope: CapabilityScope,
): boolean {
  const required = protectedNamespacePermission(namespace);
  return required === null || permissions.can(required, scope);
}

export function namespaceLifecyclePermission(namespace: string): Permission {
  if (namespace === "default") {
    return "cluster.system_namespace.manage";
  }
  return namespaceMutationPermission(namespace, "cluster.namespace.manage");
}
