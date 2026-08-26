import type { Permission } from "@/api/types";

import { namespaceMutationPermission, type NamespaceTarget } from "./namespace-permissions";

/** The GVR a generic resource request names. */
export type ResourceTypeIdentity = {
  group: string;
  version: string;
  resource: string;
};

/**
 * Writing the Node object itself — its YAML, its labels, its taints, its
 * `spec.unschedulable` — answers to `cluster.node.manage` rather than to the
 * ordinary resource permissions.
 *
 * A Node is not one object among many: its labels and taints decide where every
 * workload in the Cluster may run, and the Console reaches it through the same
 * generic CRUD route it uses for a ConfigMap. The Server replaces the permission
 * for exactly these requests (`effectiveClusterPermission` in
 * `pkg/server/httpapi/middleware/authorization.go`); this mirrors that so the
 * Console does not offer an action every request would come back refused from.
 * Evicting the Pods already on the Node stays `cluster.node.drain`.
 */
export const NODE_MUTATION_PERMISSION: Permission = "cluster.node.manage";

export function isNodeResourceType(type: ResourceTypeIdentity): boolean {
  return type.group === "" && type.version === "v1" && type.resource === "nodes";
}

/**
 * The permission one generic write answers to: the Node permission for a Node,
 * otherwise the protected-Namespace permission where the target is one, and the
 * ordinary resource permission everywhere else.
 *
 * Namespace objects keep their own resolution at the call site, because
 * creating and deleting one is decided by the object's *name* rather than by
 * the Namespace it sits in.
 */
export function resourceMutationPermission(
  type: ResourceTypeIdentity,
  target: NamespaceTarget,
  ordinary: Permission,
): Permission {
  if (isNodeResourceType(type)) {
    return NODE_MUTATION_PERMISSION;
  }
  return namespaceMutationPermission(target, ordinary);
}
