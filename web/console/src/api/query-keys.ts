import type { ListParams } from "./types";

type TenantListParams = ListParams & { status?: string };
type ProjectListParams = TenantListParams;
type ClusterListParams = TenantListParams;
type EnrollmentListParams = TenantListParams;
type UserListParams = TenantListParams;
type RoleBindingListParams = ListParams & { role?: string; scope_type?: string };

/**
 * Every cached server resource is addressed through these keys so SSE events
 * and mutations can invalidate exactly what changed.
 */
export const queryKeys = {
  session: () => ["session"] as const,
  health: () => ["health"] as const,

  tenants: (params: TenantListParams = {}) => ["tenants", params] as const,
  tenant: (tenantId: string) => ["tenant", tenantId] as const,

  projects: (tenantId: string, params: ProjectListParams = {}) =>
    ["projects", tenantId, params] as const,
  project: (projectId: string) => ["project", projectId] as const,

  clusters: (projectId: string, params: ClusterListParams = {}) =>
    ["clusters", projectId, params] as const,
  cluster: (clusterId: string) => ["cluster", clusterId] as const,
  clusterOverview: (clusterId: string) => ["cluster-overview", clusterId] as const,
  nodes: (clusterId: string, params: Record<string, unknown> = {}) =>
    ["nodes", clusterId, params] as const,
  node: (clusterId: string, name: string) => ["node", clusterId, name] as const,
  namespaces: (clusterId: string, params: Record<string, unknown> = {}) =>
    ["namespaces", clusterId, params] as const,
  namespace: (clusterId: string, name: string) => ["namespace", clusterId, name] as const,
  // Workloads are addressed by Cluster, Namespace and workload type, because
  // those three are what the endpoint is scoped by: a Deployment named `api` in
  // one Namespace has nothing to do with a Job of the same name in another.
  workloads: (
    clusterId: string,
    namespace: string,
    resource: string,
    params: Record<string, unknown> = {},
  ) => ["workloads", clusterId, namespace, resource, params] as const,
  workload: (clusterId: string, namespace: string, resource: string, name: string) =>
    ["workload", clusterId, namespace, resource, name] as const,
  // The revision history is its own read: it is the ReplicaSets or
  // ControllerRevisions the workload owns, not a part of the workload itself,
  // and a write to the workload changes both.
  workloadRevisions: (clusterId: string, namespace: string, resource: string, name: string) =>
    ["workload-revisions", clusterId, namespace, resource, name] as const,
  // Keyed by everything that identifies the object, because the YAML endpoint
  // serves every kind through one route.
  resourceYaml: (clusterId: string, namespace: string, gvr: string, name: string) =>
    ["resource-yaml", clusterId, namespace, gvr, name] as const,
  authorizationResources: (
    clusterId: string,
    namespace: string,
    resource: string,
    params: Record<string, unknown> = {},
  ) => ["authorization-resources", clusterId, namespace, resource, params] as const,
  authorizationResource: (clusterId: string, namespace: string, resource: string, name: string) =>
    ["authorization-resource", clusterId, namespace, resource, name] as const,
  autoscalers: (clusterId: string, namespace: string, params: Record<string, unknown> = {}) =>
    ["autoscalers", clusterId, namespace, params] as const,
  autoscaler: (clusterId: string, namespace: string, name: string) =>
    ["autoscaler", clusterId, namespace, name] as const,
  storageResources: (
    clusterId: string,
    namespace: string,
    resource: string,
    params: Record<string, unknown> = {},
  ) => ["storage-resources", clusterId, namespace, resource, params] as const,
  storageResource: (clusterId: string, namespace: string, resource: string, name: string) =>
    ["storage-resource", clusterId, namespace, resource, name] as const,
  resourceTypes: (clusterId: string) => ["resource-types", clusterId] as const,
  genericResources: (
    clusterId: string,
    /** `group/version/resource`, because the browser addresses every kind alike. */
    gvr: string,
    namespace: string,
    params: Record<string, unknown> = {},
  ) => ["generic-resources", clusterId, gvr, namespace, params] as const,
  policyResources: (
    clusterId: string,
    namespace: string,
    resource: string,
    params: Record<string, unknown> = {},
  ) => ["policy-resources", clusterId, namespace, resource, params] as const,
  policyResource: (clusterId: string, namespace: string, resource: string, name: string) =>
    ["policy-resource", clusterId, namespace, resource, name] as const,
  configMaps: (clusterId: string, namespace: string, params: Record<string, unknown> = {}) =>
    ["config-maps", clusterId, namespace, params] as const,
  configMap: (clusterId: string, namespace: string, name: string) =>
    ["config-map", clusterId, namespace, name] as const,
  secrets: (clusterId: string, namespace: string, params: Record<string, unknown> = {}) =>
    ["secrets", clusterId, namespace, params] as const,
  secret: (clusterId: string, namespace: string, name: string) =>
    ["secret", clusterId, namespace, name] as const,
  networkingResources: (
    clusterId: string,
    namespace: string,
    resource: string,
    params: Record<string, unknown> = {},
  ) => ["networking-resources", clusterId, namespace, resource, params] as const,
  networkingResource: (clusterId: string, namespace: string, resource: string, name: string) =>
    ["networking-resource", clusterId, namespace, resource, name] as const,
  pods: (clusterId: string, namespace: string, params: Record<string, unknown> = {}) =>
    ["pods", clusterId, namespace, params] as const,
  pod: (clusterId: string, namespace: string, name: string) =>
    ["pod", clusterId, namespace, name] as const,
  // Describe is its own read rather than part of the object's: it joins the
  // object with the Events naming it, answers to a second permission, and goes
  // stale on a different clock — the Events move while the object stands still.
  podDescribe: (clusterId: string, namespace: string, name: string) =>
    ["pod-describe", clusterId, namespace, name] as const,
  nodeDescribe: (clusterId: string, name: string) => ["node-describe", clusterId, name] as const,
  persistentVolumeClaimDescribe: (clusterId: string, namespace: string, name: string) =>
    ["persistent-volume-claim-describe", clusterId, namespace, name] as const,
  serviceDescribe: (clusterId: string, namespace: string, name: string) =>
    ["service-describe", clusterId, namespace, name] as const,
  ingressDescribe: (clusterId: string, namespace: string, name: string) =>
    ["ingress-describe", clusterId, namespace, name] as const,
  gatewayDescribe: (clusterId: string, namespace: string, name: string) =>
    ["gateway-describe", clusterId, namespace, name] as const,
  autoscalerDescribe: (clusterId: string, namespace: string, name: string) =>
    ["autoscaler-describe", clusterId, namespace, name] as const,
  policyDescribe: (clusterId: string, namespace: string, resource: string, name: string) =>
    ["policy-describe", clusterId, namespace, resource, name] as const,
  resourceDescribe: (clusterId: string, namespace: string, gvr: string, name: string) =>
    ["resource-describe", clusterId, namespace, gvr, name] as const,
  workloadDescribe: (clusterId: string, namespace: string, resource: string, name: string) =>
    ["workload-describe", clusterId, namespace, resource, name] as const,

  enrollments: (projectId: string, params: EnrollmentListParams = {}) =>
    ["enrollments", projectId, params] as const,

  users: (params: UserListParams = {}) => ["users", params] as const,
  user: (userId: string) => ["user", userId] as const,

  roles: (params: Record<string, unknown> = {}) => ["roles", params] as const,
  role: (roleId: string) => ["role", roleId] as const,
  permissions: () => ["permissions"] as const,

  roleBindings: (params: RoleBindingListParams = {}) => ["role-bindings", params] as const,

  auditEvents: (params: Record<string, unknown> = {}) => ["audit-events", params] as const,
  auditActions: () => ["audit-actions"] as const,
} as const;

/** Prefixes used for coarse invalidation after a mutation. */
export const queryKeyPrefixes = {
  tenants: ["tenants"] as const,
  projects: ["projects"] as const,
  clusters: ["clusters"] as const,
  nodes: ["nodes"] as const,
  node: ["node"] as const,
  namespaces: ["namespaces"] as const,
  namespace: ["namespace"] as const,
  workloads: ["workloads"] as const,
  workload: ["workload"] as const,
  workloadRevisions: ["workload-revisions"] as const,
  configMaps: ["config-maps"] as const,
  configMap: ["config-map"] as const,
  secrets: ["secrets"] as const,
  secret: ["secret"] as const,
  networkingResources: ["networking-resources"] as const,
  networkingResource: ["networking-resource"] as const,
  pods: ["pods"] as const,
  pod: ["pod"] as const,
  enrollments: ["enrollments"] as const,
  users: ["users"] as const,
  roles: ["roles"] as const,
  role: ["role"] as const,
  roleBindings: ["role-bindings"] as const,
  authorizationResources: ["authorization-resources"] as const,
  authorizationResource: ["authorization-resource"] as const,
  storageResources: ["storage-resources"] as const,
  storageResource: ["storage-resource"] as const,
  genericResources: ["generic-resources"] as const,
  policyResources: ["policy-resources"] as const,
  policyResource: ["policy-resource"] as const,
  auditEvents: ["audit-events"] as const,
} as const;
