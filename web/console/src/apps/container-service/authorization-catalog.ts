import type { KubernetesAuthorizationResource } from "@/api/types";

/**
 * The five RBAC types, grouped the way they relate: an identity, the two kinds
 * of permission set, and the two kinds of grant that connect them.
 */
export const AUTHORIZATION_TYPES: {
  resource: KubernetesAuthorizationResource;
  label: string;
}[] = [
  { resource: "serviceaccounts", label: "ServiceAccount" },
  { resource: "roles", label: "Role" },
  { resource: "clusterroles", label: "ClusterRole" },
  { resource: "rolebindings", label: "RoleBinding" },
  { resource: "clusterrolebindings", label: "ClusterRoleBinding" },
];

export function authorizationKindLabel(resource: KubernetesAuthorizationResource): string {
  return AUTHORIZATION_TYPES.find((type) => type.resource === resource)?.label ?? resource;
}

/** Roles and ClusterRoles carry rules; the rest do not. */
export function hasRules(resource: KubernetesAuthorizationResource): boolean {
  return resource === "roles" || resource === "clusterroles";
}

/** Bindings carry subjects and a roleRef. */
export function isBinding(resource: KubernetesAuthorizationResource): boolean {
  return resource === "rolebindings" || resource === "clusterrolebindings";
}
