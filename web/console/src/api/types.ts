import type { components } from "./schema";

type Schemas = components["schemas"];

export type UserIdentity = Schemas["UserIdentity"];
export type ManagedUser = Schemas["ManagedUser"];
export type UserStatus = ManagedUser["status"];
export type CurrentSession = Schemas["CurrentSession"];
export type Capability = Schemas["Capability"];
export type Permission = Capability["permissions"][number];
export type Role = Capability["role"];
export type ScopeType = Capability["scope_type"];

export type RoleBinding = Schemas["RoleBinding"];
export type Pagination = Schemas["Pagination"];

export type Tenant = Schemas["Tenant"];
export type Project = Schemas["Project"];
export type Cluster = Schemas["Cluster"];
export type ClusterAggregate = Schemas["ClusterAggregate"];
export type ClusterConnection = Schemas["ClusterConnection"];
export type ClusterEnrollment = Schemas["ClusterEnrollment"];
export type ClusterEnrollmentRecord = Schemas["ClusterEnrollmentRecord"];
export type ClusterInstallation = Schemas["ClusterInstallation"];
export type ClusterConnectionRevocation = Schemas["ClusterConnectionRevocation"];
export type KubernetesNodeSummary = Schemas["KubernetesNodeSummary"];
export type KubernetesNodeDetail = Schemas["KubernetesNodeDetail"];
export type KubernetesNodePage = Schemas["KubernetesNodePage"];
export type KubernetesNodeStatus = KubernetesNodeSummary["status"];
export type KubernetesNamespaceSummary = Schemas["KubernetesNamespaceSummary"];
export type KubernetesNamespaceDetail = Schemas["KubernetesNamespaceDetail"];
export type KubernetesNamespacePage = Schemas["KubernetesNamespacePage"];
export type KubernetesNamespaceMutationResult = Schemas["KubernetesNamespaceMutationResult"];
export type KubernetesWorkloadResource = Schemas["KubernetesWorkloadResource"];
export type KubernetesWorkloadSummary = Schemas["KubernetesWorkloadSummary"];
export type KubernetesWorkloadDetail = Schemas["KubernetesWorkloadDetail"];
export type KubernetesWorkloadPage = Schemas["KubernetesWorkloadPage"];
export type KubernetesWorkloadMutationResult = Schemas["KubernetesWorkloadMutationResult"];
export type KubernetesCreateWorkloadRequest = Schemas["KubernetesCreateWorkloadRequest"];
export type KubernetesWorkloadContainerTemplate = Schemas["KubernetesWorkloadContainerTemplate"];
export type KubernetesWorkloadStatus = KubernetesWorkloadSummary["status"];
export type KubernetesDeleteResult = Schemas["KubernetesDeleteResult"];

export type AuditEvent = Schemas["AuditEvent"];
export type AuditEventPage = Schemas["AuditEventPage"];
export type AuditAction = Schemas["AuditAction"];

export type ResourceStatus = "active" | "suspended";
export type ClusterStatus = "pending" | "active" | "suspended";
/** The two states an operator may set; `pending` follows the Agent connection. */
export type ClusterLifecycleStatus = "active" | "suspended";
export type EnrollmentStatus = "active" | "consumed" | "expired" | "revoked";
export type ConnectionStatus = ClusterConnection["status"];
export type CertificateStatus = ClusterConnection["certificate_status"];

/** Page position shared by every list endpoint. */
export type PageParams = {
  limit?: number;
  offset?: number;
};

/**
 * Page position plus free-text search. Endpoints reject filters they do not
 * implement rather than ignoring them, so a list that has no search must use
 * {@link PageParams} instead of widening this type.
 */
export type ListParams = PageParams & {
  q?: string;
};

export const DEFAULT_PAGE_SIZE = 20;

/**
 * Authorization scope carried by desktop windows and audit filters.
 *
 * It mirrors the RoleBinding levels the Server actually enforces — Global,
 * Tenant and Project — so a selected scope always maps onto a real permission
 * boundary. A Project implies its Tenant, so both are stored together and are
 * only ever set as a pair. Cluster and Namespace are deliberately absent: they
 * are resources inside a Project, not authorization scopes, and picking one is
 * navigation state owned by whichever application needs it.
 */
export type ScopeSelection = {
  tenantId: string | null;
  tenantName: string | null;
  projectId: string | null;
  projectName: string | null;
};

export const EMPTY_SCOPE: ScopeSelection = {
  tenantId: null,
  tenantName: null,
  projectId: null,
  projectName: null,
};
