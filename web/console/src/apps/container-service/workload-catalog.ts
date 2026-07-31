import type { KubernetesWorkloadResource } from "@/api/types";

/**
 * The five workload types the Server exposes a typed read for. Labelled with the
 * Kubernetes kind rather than a translation: it is what the operator sees in
 * kubectl, in YAML and in every other console, and inventing a Chinese name for
 * it would only make the two harder to line up.
 */
export const WORKLOAD_TYPES: { resource: KubernetesWorkloadResource; label: string }[] = [
  { resource: "deployments", label: "Deployment" },
  { resource: "statefulsets", label: "StatefulSet" },
  { resource: "daemonsets", label: "DaemonSet" },
  { resource: "jobs", label: "Job" },
  { resource: "cronjobs", label: "CronJob" },
];

export function kindLabel(resource: KubernetesWorkloadResource): string {
  return WORKLOAD_TYPES.find((type) => type.resource === resource)?.label ?? resource;
}

/*
 * Which action applies to which type, mirroring what the Server accepts. The
 * Console offers an action only where the endpoint would carry it out, so an
 * operator is never handed a control that answers 400 — but these are a UI
 * affordance, not the check: the Server validates the pairing itself.
 */

/** Scaling sets `spec.replicas`, which only these two controllers have. */
export function supportsScale(resource: KubernetesWorkloadResource): boolean {
  return resource === "deployments" || resource === "statefulsets";
}

/** Rolling restart patches the Pod template, so it needs a controller that owns one. */
export function supportsRestart(resource: KubernetesWorkloadResource): boolean {
  return resource === "deployments" || resource === "statefulsets" || resource === "daemonsets";
}

/** Suspension is `spec.suspend` on a CronJob. */
export function supportsSuspension(resource: KubernetesWorkloadResource): boolean {
  return resource === "cronjobs";
}
