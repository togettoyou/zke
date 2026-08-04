import type { QuantityUnit } from "@/lib/quantity";

/** One row of the quota form: a ResourceQuota `hard` key under a readable name. */
export type QuotaField = {
  /** The `spec.hard` key this row writes. */
  key: string;
  label: string;
  unit: QuantityUnit;
  /** The unit shown after the input, in the operator's language. */
  unitLabel: string;
};

export type QuotaFieldGroup = {
  title: string;
  fields: QuotaField[];
};

/**
 * The quota keys this form models, grouped the way a Namespace budget is
 * actually decided: how much compute may be requested and used, how much storage
 * may be claimed, and how many objects of each kind may exist.
 *
 * Kubernetes accepts more keys than these — `count/<resource>.<group>` works for
 * any resource, and device plugins add their own `requests.<vendor>/<device>` —
 * so anything outside this list that an existing quota already carries is
 * preserved untouched rather than dropped by an update.
 */
export const NAMESPACE_QUOTA_GROUPS: QuotaFieldGroup[] = [
  {
    title: "计算资源配额",
    fields: [
      { key: "requests.cpu", label: "CPU Request", unit: "cores", unitLabel: "核" },
      { key: "limits.cpu", label: "CPU Limit", unit: "cores", unitLabel: "核" },
      { key: "requests.memory", label: "Memory Request", unit: "gib", unitLabel: "Gi" },
      { key: "limits.memory", label: "Memory Limit", unit: "gib", unitLabel: "Gi" },
    ],
  },
  {
    title: "存储资源限制",
    fields: [
      { key: "requests.storage", label: "存储总量", unit: "gib", unitLabel: "Gi" },
      { key: "persistentvolumeclaims", label: "PVC 总量", unit: "count", unitLabel: "个" },
    ],
  },
  {
    title: "其他资源限制",
    fields: [
      { key: "pods", label: "Pod 总量", unit: "count", unitLabel: "个" },
      { key: "services", label: "Service 总量", unit: "count", unitLabel: "个" },
      {
        key: "services.loadbalancers",
        label: "LoadBalancer 类型 Service 总量",
        unit: "count",
        unitLabel: "个",
      },
      {
        key: "services.nodeports",
        label: "NodePort 类型 Service 总量",
        unit: "count",
        unitLabel: "个",
      },
      { key: "count/statefulsets.apps", label: "StatefulSet 总量", unit: "count", unitLabel: "个" },
      { key: "count/deployments.apps", label: "Deployment 总量", unit: "count", unitLabel: "个" },
      { key: "count/jobs.batch", label: "Job 总量", unit: "count", unitLabel: "个" },
      { key: "count/cronjobs.batch", label: "CronJob 总量", unit: "count", unitLabel: "个" },
      { key: "secrets", label: "Secret 总量", unit: "count", unitLabel: "个" },
      { key: "configmaps", label: "ConfigMap 总量", unit: "count", unitLabel: "个" },
    ],
  },
];

export const NAMESPACE_QUOTA_FIELDS: QuotaField[] = NAMESPACE_QUOTA_GROUPS.flatMap(
  (group) => group.fields,
);

const MODELLED_KEYS = new Set(NAMESPACE_QUOTA_FIELDS.map((field) => field.key));

/** True for a `hard` key this form has no row for, and must therefore carry through. */
export function isUnmodelledQuotaKey(key: string): boolean {
  return !MODELLED_KEYS.has(key);
}

/**
 * The name given to the ResourceQuota this form creates.
 *
 * Only used when the Namespace has none: an existing quota is edited under
 * whatever name it already has, because renaming it would mean deleting the
 * object that is currently being enforced and creating another.
 */
export const NAMESPACE_QUOTA_OBJECT_NAME = "zke-namespace-quota";
