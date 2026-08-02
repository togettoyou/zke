import type { KubernetesStorageResource } from "@/api/types";

/**
 * The three storage types, in the order they nest: a StorageClass provisions a
 * PersistentVolume, which a PersistentVolumeClaim binds.
 */
export const STORAGE_TYPES: { resource: KubernetesStorageResource; label: string }[] = [
  { resource: "persistentvolumes", label: "PersistentVolume" },
  { resource: "persistentvolumeclaims", label: "PersistentVolumeClaim" },
  { resource: "storageclasses", label: "StorageClass" },
];

export function storageKindLabel(resource: KubernetesStorageResource): string {
  return STORAGE_TYPES.find((type) => type.resource === resource)?.label ?? resource;
}

/** The GVR each type lives at, for the YAML editor's generic route. */
export function storageIdentity(resource: KubernetesStorageResource): {
  group: string;
  version: string;
  resource: string;
} {
  return resource === "storageclasses"
    ? { group: "storage.k8s.io", version: "v1", resource: "storageclasses" }
    : { group: "", version: "v1", resource };
}

export const ACCESS_MODES = [
  "ReadWriteOnce",
  "ReadOnlyMany",
  "ReadWriteMany",
  "ReadWriteOncePod",
] as const;

export type AccessMode = (typeof ACCESS_MODES)[number];
