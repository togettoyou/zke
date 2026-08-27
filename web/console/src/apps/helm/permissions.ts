import type { Permission } from "@/api/types";
import type { CapabilityScope, PermissionChecker } from "@/auth/capabilities";
import { canUseProtectedNamespace } from "@/apps/container-service/namespace-permissions";

/**
 * What a release change costs, as permissions.
 *
 * The Server requires all of these together, and the reason each one is there
 * is different: `cluster.helm.manage` says releases may be changed at all, the
 * object permissions are what the operation actually spends, and
 * `cluster.secret.manage` is required because Helm's own release storage is a
 * Secret. Listing them here rather than checking one keeps the Console from
 * offering a button whose every press comes back refused.
 *
 * A Namespace-level check is still not the whole answer: whether the chart may
 * create objects that no Namespace contains is decided by the Server from
 * `cluster.manage`, after the chart has been rendered. That is why
 * {@link canInstallClusterScoped} is reported separately and shown as a note
 * rather than used to hide anything.
 */
const WRITE_PERMISSIONS: Permission[] = [
  "cluster.helm.manage",
  "cluster.resource.create",
  "cluster.resource.update",
  "cluster.secret.manage",
];

const UNINSTALL_PERMISSIONS: Permission[] = [
  "cluster.helm.manage",
  "cluster.resource.delete",
  "cluster.secret.manage",
];

export type HelmNamespaceTarget = {
  namespace: string;
  agentNamespace: string;
};

export type HelmAccess = {
  /** Reading a release hands back its values, which is a Secret read. */
  canRead: boolean;
  canInstall: boolean;
  canUninstall: boolean;
  /** Whether a chart may create CustomResourceDefinitions, ClusterRoles and the like. */
  canInstallClusterScoped: boolean;
  /** Browsing the chart catalogue at all. */
  canBrowseCharts: boolean;
  /** Adding, editing and removing repositories. */
  canManageRepositories: boolean;
};

export function helmAccess(
  permissions: PermissionChecker,
  target: HelmNamespaceTarget,
  scope: CapabilityScope,
): HelmAccess {
  // kube-* and the Agent's own Namespace need their own grant on top of
  // everything else, exactly as they do for a Secret written there.
  const protectedAccess = canUseProtectedNamespace(permissions, target, scope);
  const holdsAll = (required: Permission[]) =>
    protectedAccess && required.every((permission) => permissions.can(permission, scope));
  return {
    canRead: protectedAccess && permissions.can("cluster.secret.read", scope),
    canInstall: holdsAll(WRITE_PERMISSIONS),
    canUninstall: holdsAll(UNINSTALL_PERMISSIONS),
    canInstallClusterScoped: permissions.can("cluster.manage", scope),
    // The catalogue is platform-wide, so it is checked globally rather than in
    // the Project scope the Cluster permissions are evaluated in.
    canBrowseCharts: permissions.can("helm.repository.read", { type: "global" }),
    canManageRepositories: permissions.can("helm.repository.manage", { type: "global" }),
  };
}
