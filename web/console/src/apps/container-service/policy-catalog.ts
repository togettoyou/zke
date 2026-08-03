import type { KubernetesPolicyResource } from "@/api/types";

/**
 * The five policy objects, ordered by what they constrain: how much may be
 * consumed, what a container's limits default to, which traffic reaches a Pod,
 * how many Pods may be disrupted at once, and how Pods rank against each other
 * when the cluster runs short.
 */
export const POLICY_TYPES: { resource: KubernetesPolicyResource; label: string }[] = [
  { resource: "resourcequotas", label: "ResourceQuota" },
  { resource: "limitranges", label: "LimitRange" },
  { resource: "networkpolicies", label: "NetworkPolicy" },
  { resource: "poddisruptionbudgets", label: "PodDisruptionBudget" },
  { resource: "priorityclasses", label: "PriorityClass" },
];

export function policyKindLabel(resource: KubernetesPolicyResource): string {
  return POLICY_TYPES.find((type) => type.resource === resource)?.label ?? resource;
}

/** The GVR each type lives at, for the YAML editor's generic route. */
export function policyIdentity(resource: KubernetesPolicyResource): {
  group: string;
  version: string;
  resource: string;
} {
  switch (resource) {
    case "networkpolicies":
      return { group: "networking.k8s.io", version: "v1", resource };
    case "poddisruptionbudgets":
      return { group: "policy", version: "v1", resource };
    case "priorityclasses":
      return { group: "scheduling.k8s.io", version: "v1", resource };
    default:
      return { group: "", version: "v1", resource };
  }
}

/** The scopes a ResourceQuota may be narrowed to. */
export const QUOTA_SCOPES = [
  "Terminating",
  "NotTerminating",
  "BestEffort",
  "NotBestEffort",
  "PriorityClass",
  "CrossNamespacePodAffinity",
] as const;

export type QuotaScope = (typeof QUOTA_SCOPES)[number];

/**
 * The quota keys worth offering as suggestions.
 *
 * Not a closed list — Kubernetes accepts `count/<resource>.<group>` for
 * anything, and the field stays free text — but these are the ones a Namespace
 * quota is actually written with.
 */
export const QUOTA_RESOURCE_SUGGESTIONS = [
  "requests.cpu",
  "requests.memory",
  "limits.cpu",
  "limits.memory",
  "requests.storage",
  "requests.nvidia.com/gpu",
  "persistentvolumeclaims",
  "pods",
  "services",
  "count/deployments.apps",
] as const;

export const LIMIT_RANGE_TYPES = ["Container", "Pod", "PersistentVolumeClaim"] as const;

export type LimitRangeType = (typeof LIMIT_RANGE_TYPES)[number];

export const NETWORK_PROTOCOLS = ["TCP", "UDP", "SCTP"] as const;
