import type { components } from "./schema";

type Schemas = components["schemas"];

export type UserIdentity = Schemas["UserIdentity"];
export type ManagedUser = Schemas["ManagedUser"];
export type UserStatus = ManagedUser["status"];
export type CurrentSession = Schemas["CurrentSession"];
export type Capability = Schemas["Capability"];
export type Permission = Capability["permissions"][number];
// The name a binding stores. A plain string since roles became operator-defined:
// the contract no longer enumerates them, and a Console type that did would
// reject a role the Server accepted.
export type RoleName = Capability["role"];
export type Role = Schemas["Role"];
export type PermissionDescriptor = Schemas["PermissionDescriptor"];
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
export type AgentEndpointProfile = Schemas["AgentEndpointProfile"];
export type PlatformSettings = Schemas["PlatformSettings"];
export type PlatformSettingsUpdate = Schemas["PlatformSettingsUpdate"];
export type WorkloadSettings = Schemas["WorkloadSettings"];
export type AIModelSettings = Schemas["AIModelSettings"];
export type AIModelSettingsUpdate = Schemas["AIModelSettingsUpdate"];
export type AIModelTestResult = Schemas["AIModelTestResult"];
export type AISession = Schemas["AISession"];
export type AITrajectoryEntry = Schemas["AITrajectoryEntry"];
export type AIEvidence = Schemas["AIEvidence"];
export type AIAttachment = Schemas["AIAttachment"];
export type AITool = Schemas["AITool"];
export type AIContextUsage = {
  used_tokens: number;
  context_window_tokens: number;
  threshold_tokens: number;
  system_tokens: number;
  tools_tokens: number;
  message_tokens: number;
  measured: boolean;
};
export type AITrajectoryKind = NonNullable<AITrajectoryEntry["kind"]>;
export type ClusterConnectionRevocation = Schemas["ClusterConnectionRevocation"];
export type MetricsQueryCatalog = Schemas["MetricsQueryCatalog"];
export type MetricsQueryDefinition = MetricsQueryCatalog["queries"][number];
export type MetricsQueryResult = Schemas["MetricsQueryResult"];
export type MetricsQuerySeries = MetricsQueryResult["series"][number];
export type MetricsQueryIssue = MetricsQueryResult["issues"][number];
export type MetricsCollectorState = Schemas["MetricsCollectorState"];
export type MetricsComponentState = MetricsCollectorState["components"][number];
export type KubernetesClusterOverview = Schemas["KubernetesClusterOverview"];
export type KubernetesClusterOverviewIssue = Schemas["KubernetesClusterOverviewIssue"];
export type KubernetesOverviewStatusCounts = Schemas["KubernetesOverviewStatusCounts"];
export type KubernetesNodeSummary = Schemas["KubernetesNodeSummary"];
export type KubernetesNodeDetail = Schemas["KubernetesNodeDetail"];
export type KubernetesNodePage = Schemas["KubernetesNodePage"];
export type KubernetesNodeDrainRequest = Schemas["KubernetesNodeDrainRequest"];
export type KubernetesNodeDrainResult = Schemas["KubernetesNodeDrainResult"];
export type KubernetesNodeDrainPod = Schemas["KubernetesNodeDrainPod"];
export type KubernetesNodeMetric = Schemas["KubernetesNodeMetric"];
export type KubernetesPodMetric = Schemas["KubernetesPodMetric"];
export type KubernetesNodeMetricsSnapshot = Schemas["KubernetesNodeMetricsSnapshot"];
export type KubernetesPodMetricsSnapshot = Schemas["KubernetesPodMetricsSnapshot"];
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
export type KubernetesUpdateWorkloadRequest = Schemas["KubernetesUpdateWorkloadRequest"];
export type KubernetesWorkloadContainerTemplate = Schemas["KubernetesWorkloadContainerTemplate"];
export type KubernetesWorkloadRevision = Schemas["KubernetesWorkloadRevision"];
export type KubernetesWorkloadRevisionPage = Schemas["KubernetesWorkloadRevisionPage"];
export type KubernetesWorkloadStatus = KubernetesWorkloadSummary["status"];
export type KubernetesAuthorizationResource = Schemas["KubernetesAuthorizationResource"];
export type KubernetesAuthorizationResourceSummary =
  Schemas["KubernetesAuthorizationResourceSummary"];
export type KubernetesAuthorizationResourceDetail =
  Schemas["KubernetesAuthorizationResourceDetail"];
export type KubernetesAuthorizationPolicyRule = Schemas["KubernetesAuthorizationPolicyRule"];
export type KubernetesAuthorizationSubject = Schemas["KubernetesAuthorizationSubject"];
export type KubernetesAuthorizationRoleRef = Schemas["KubernetesAuthorizationRoleRef"];
export type KubernetesHPASummary = Schemas["KubernetesHPASummary"];
export type KubernetesHPADetail = Schemas["KubernetesHPADetail"];
export type KubernetesHPASpecInput = Schemas["KubernetesHPASpecInput"];
export type KubernetesHPAMetricView = Schemas["KubernetesHPAMetricView"];
export type KubernetesHPABehavior = Schemas["KubernetesHPABehavior"];
export type KubernetesHPAMetricTrend = Schemas["KubernetesHPAMetricTrend"];
export type KubernetesVPASummary = Schemas["KubernetesVPASummary"];
export type KubernetesVPADetail = Schemas["KubernetesVPADetail"];
export type KubernetesVPASpecInput = Schemas["KubernetesVPASpecInput"];
export type KubernetesVPAContainerPolicy = Schemas["KubernetesVPAContainerPolicy"];
export type KubernetesKEDASummary = Schemas["KubernetesKEDASummary"];
export type KubernetesKEDADetail = Schemas["KubernetesKEDADetail"];
export type KubernetesKEDASpecInput = Schemas["KubernetesKEDASpecInput"];
export type KubernetesKEDATrigger = Schemas["KubernetesKEDATrigger"];
export type KubernetesResourceCatalog = Schemas["KubernetesResourceCatalog"];
export type KubernetesResourceType = Schemas["KubernetesResourceType"];
export type KubernetesPolicyResource = Schemas["KubernetesPolicyResource"];
export type KubernetesPolicyResourceSummary = Schemas["KubernetesPolicyResourceSummary"];
export type KubernetesPolicyResourceDetail = Schemas["KubernetesPolicyResourceDetail"];
export type KubernetesLimitRangeItem = Schemas["KubernetesLimitRangeItem"];
export type KubernetesNetworkPolicyRule = Schemas["KubernetesNetworkPolicyRule"];
export type KubernetesNetworkPolicyPeer = Schemas["KubernetesNetworkPolicyPeer"];
export type KubernetesNetworkPolicyPort = Schemas["KubernetesNetworkPolicyPort"];
export type KubernetesPolicyScopeSelectorRequirement =
  Schemas["KubernetesPolicyScopeSelectorRequirement"];
export type KubernetesResourceQuotaSpecInput = Schemas["KubernetesResourceQuotaSpecInput"];
export type KubernetesLimitRangeSpecInput = Schemas["KubernetesLimitRangeSpecInput"];
export type KubernetesNetworkPolicySpecInput = Schemas["KubernetesNetworkPolicySpecInput"];
export type KubernetesDisruptionBudgetSpecInput = Schemas["KubernetesDisruptionBudgetSpecInput"];
export type KubernetesPriorityClassSpecInput = Schemas["KubernetesPriorityClassSpecInput"];
export type KubernetesPriorityClassUpdateInput = Schemas["KubernetesPriorityClassUpdateInput"];
export type KubernetesStorageResource = Schemas["KubernetesStorageResource"];
export type KubernetesStorageResourceSummary = Schemas["KubernetesStorageResourceSummary"];
export type KubernetesStorageResourceDetail = Schemas["KubernetesStorageResourceDetail"];
export type KubernetesPersistentVolumeCreateInput =
  Schemas["KubernetesPersistentVolumeCreateInput"];
export type KubernetesPersistentVolumeClaimCreateInput =
  Schemas["KubernetesPersistentVolumeClaimCreateInput"];
export type KubernetesStorageClassCreateInput = Schemas["KubernetesStorageClassCreateInput"];
export type KubernetesConfigMapSummary = Schemas["KubernetesConfigMapSummary"];
export type KubernetesConfigMapDetail = Schemas["KubernetesConfigMapDetail"];
export type KubernetesSecretSummary = Schemas["KubernetesSecretSummary"];
export type KubernetesSecretDetail = Schemas["KubernetesSecretDetail"];
export type KubernetesNetworkingResource = Schemas["KubernetesNetworkingResource"];
export type KubernetesNetworkingResourceSummary = Schemas["KubernetesNetworkingResourceSummary"];
export type KubernetesNetworkingResourceDetail = Schemas["KubernetesNetworkingResourceDetail"];
export type KubernetesNetworkingResourcePage = Schemas["KubernetesNetworkingResourcePage"];
export type KubernetesServiceView = Schemas["KubernetesServiceView"];
export type KubernetesServiceSpecInput = Schemas["KubernetesServiceSpecInput"];
export type KubernetesIngressView = Schemas["KubernetesIngressView"];
export type KubernetesIngressSpecInput = Schemas["KubernetesIngressSpecInput"];
export type KubernetesGatewayView = Schemas["KubernetesGatewayView"];
export type KubernetesGatewaySpecInput = Schemas["KubernetesGatewaySpecInput"];
export type KubernetesGatewayRouteView = Schemas["KubernetesGatewayRouteView"];
export type KubernetesGatewayRouteSpecInput = Schemas["KubernetesGatewayRouteSpecInput"];
export type KubernetesPodSummary = Schemas["KubernetesPodSummary"];
export type KubernetesPodDetail = Schemas["KubernetesPodDetail"];
export type KubernetesPodPage = Schemas["KubernetesPodPage"];
export type KubernetesPodContainer = Schemas["KubernetesPodContainer"];
export type KubernetesPodOwnerReference = Schemas["KubernetesPodOwnerReference"];
export type KubernetesPodPhase = KubernetesPodSummary["phase"];
export type KubernetesPodTerminalRecording = Schemas["KubernetesPodTerminalRecording"];
export type KubernetesPodTerminalRecordingFrame = Schemas["KubernetesPodTerminalRecordingFrame"];
export type KubernetesDeleteResult = Schemas["KubernetesDeleteResult"];

export type KubernetesDescribe = Schemas["KubernetesDescribe"];
export type KubernetesDescribeTarget = Schemas["KubernetesDescribeTarget"];
export type KubernetesDescribeEvent = Schemas["KubernetesDescribeEvent"];
export type KubernetesDescribeFinding = Schemas["KubernetesDescribeFinding"];
export type KubernetesDescribeFindingCode = KubernetesDescribeFinding["code"];
export type KubernetesDescribeEvidence = Schemas["KubernetesDescribeEvidence"];
export type KubernetesDescribeRelated = Schemas["KubernetesDescribeRelated"];
export type KubernetesDescribeRelatedObject = Schemas["KubernetesDescribeRelatedObject"];

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
