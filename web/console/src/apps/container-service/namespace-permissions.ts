import type { Permission } from "@/api/types";
import type { CapabilityScope, PermissionChecker } from "@/auth/capabilities";

/**
 * A Namespace together with the Agent Namespace of the Cluster it belongs to.
 *
 * The Agent Namespace is fixed per Cluster when the Agent first enrolls, so
 * which name is protected is a property of the target Cluster and not a
 * constant: `zke-system` is only the default offered when a credential is
 * created. The two names travel together in one object because both are plain
 * strings — passing them as adjacent parameters would let a swapped pair
 * type-check and silently protect the wrong Namespace.
 *
 * The Server decides every request the same way from the Cluster's stored
 * value; these helpers only keep the Console from offering an entry the Server
 * will refuse, or hiding one it would allow.
 */
export type NamespaceTarget = {
  namespace: string;
  agentNamespace: string;
};

export function protectedNamespacePermission(target: NamespaceTarget): Permission | null {
  if (target.agentNamespace !== "" && target.namespace === target.agentNamespace) {
    return "cluster.agent_namespace.manage";
  }
  if (target.namespace.startsWith("kube-")) {
    return "cluster.system_namespace.manage";
  }
  return null;
}

export function namespaceMutationPermission(
  target: NamespaceTarget,
  ordinary: Permission,
): Permission {
  return protectedNamespacePermission(target) ?? ordinary;
}

export function canUseProtectedNamespace(
  permissions: PermissionChecker,
  target: NamespaceTarget,
  scope: CapabilityScope,
): boolean {
  const required = protectedNamespacePermission(target);
  return required === null || permissions.can(required, scope);
}

export function namespaceLifecyclePermission(target: NamespaceTarget): Permission {
  if (target.namespace === "default") {
    return "cluster.system_namespace.manage";
  }
  return namespaceMutationPermission(target, "cluster.namespace.manage");
}
