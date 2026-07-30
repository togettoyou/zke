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
  nodes: (clusterId: string, params: Record<string, unknown> = {}) =>
    ["nodes", clusterId, params] as const,
  node: (clusterId: string, name: string) => ["node", clusterId, name] as const,
  namespaces: (clusterId: string, params: Record<string, unknown> = {}) =>
    ["namespaces", clusterId, params] as const,
  namespace: (clusterId: string, name: string) => ["namespace", clusterId, name] as const,

  enrollments: (projectId: string, params: EnrollmentListParams = {}) =>
    ["enrollments", projectId, params] as const,

  users: (params: UserListParams = {}) => ["users", params] as const,
  user: (userId: string) => ["user", userId] as const,

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
  namespaces: ["namespaces"] as const,
  enrollments: ["enrollments"] as const,
  users: ["users"] as const,
  roleBindings: ["role-bindings"] as const,
  auditEvents: ["audit-events"] as const,
} as const;
