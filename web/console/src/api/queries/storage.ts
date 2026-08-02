import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";
import type {
  KubernetesPersistentVolumeClaimCreateInput,
  KubernetesPersistentVolumeCreateInput,
  KubernetesStorageClassCreateInput,
  KubernetesStorageResource,
} from "../types";

export type StorageListParams = {
  limit?: number;
  continue?: string;
  label_selector?: string;
  field_selector?: string;
};

/** The one type-specific block a create request carries. */
export type StorageCreateSpec = {
  persistent_volume?: KubernetesPersistentVolumeCreateInput;
  persistent_volume_claim?: KubernetesPersistentVolumeClaimCreateInput;
  storage_class?: KubernetesStorageClassCreateInput;
};

/**
 * The one field each type allows changing after creation.
 *
 * The Server models exactly this and nothing more: a PV's reclaim policy, a
 * PVC's requested size (upwards only) and a StorageClass's expansion switch.
 * Everything else about these objects is immutable in Kubernetes.
 */
export type StorageUpdateSpec = {
  persistent_volume?: { reclaim_policy: "Retain" | "Delete" };
  persistent_volume_claim?: { requested_capacity: string };
  storage_class?: { allow_volume_expansion: boolean };
};

const CLUSTER_LIST = "/api/v1/clusters/{cluster_id}/storage/{storage_resource}";
const CLUSTER_ITEM = "/api/v1/clusters/{cluster_id}/storage/{storage_resource}/{storage_name}";
const NAMESPACED_LIST =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/storage/{storage_resource}";
const NAMESPACED_ITEM =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/storage/{storage_resource}/{storage_name}";

/**
 * PVC is the only namespaced one; PV and StorageClass are cluster objects.
 *
 * Written as a type predicate so the narrowed type reaches the path parameters:
 * the Server exposes the two scopes as different routes, and the generated types
 * accept only the resources each route actually serves.
 */
export function isNamespacedStorage(
  resource: KubernetesStorageResource,
): resource is "persistentvolumeclaims" {
  return resource === "persistentvolumeclaims";
}

/**
 * Lists one kind of storage object.
 *
 * The Server routes cluster-scoped and namespaced storage through different
 * paths and rejects the wrong one, so the scope is chosen here from the type
 * rather than passed in and hoped for.
 */
export function useStorageResources(
  clusterId: string | null,
  namespace: string | null,
  resource: KubernetesStorageResource,
  params: StorageListParams = {},
) {
  const namespaced = isNamespacedStorage(resource);
  return useQuery({
    queryKey: queryKeys.storageResources(
      clusterId ?? "",
      namespaced ? (namespace ?? "") : "",
      resource,
      params,
    ),
    queryFn: async ({ signal }) =>
      namespaced
        ? unwrap(
            await api.GET(NAMESPACED_LIST, {
              params: {
                path: {
                  cluster_id: clusterId as string,
                  namespace_name: namespace as string,
                  storage_resource: resource,
                },
                query: params,
              },
              signal,
            }),
          )
        : unwrap(
            await api.GET(CLUSTER_LIST, {
              params: {
                path: { cluster_id: clusterId as string, storage_resource: resource },
                query: params,
              },
              signal,
            }),
          ),
    enabled: Boolean(clusterId) && (!namespaced || Boolean(namespace)),
    placeholderData: (previous, previousQuery) => {
      const previousKey = previousQuery?.queryKey;
      const scopedNamespace = namespaced ? (namespace ?? "") : "";
      return previousKey?.[1] === clusterId &&
        previousKey[2] === scopedNamespace &&
        previousKey[3] === resource
        ? previous
        : undefined;
    },
  });
}

export function useStorageResource(
  clusterId: string | null,
  namespace: string | null,
  resource: KubernetesStorageResource,
  name: string | null,
) {
  const namespaced = isNamespacedStorage(resource);
  return useQuery({
    queryKey: queryKeys.storageResource(
      clusterId ?? "",
      namespaced ? (namespace ?? "") : "",
      resource,
      name ?? "",
    ),
    queryFn: async ({ signal }) =>
      namespaced
        ? unwrap(
            await api.GET(NAMESPACED_ITEM, {
              params: {
                path: {
                  cluster_id: clusterId as string,
                  namespace_name: namespace as string,
                  storage_resource: resource,
                  storage_name: name as string,
                },
              },
              signal,
            }),
          )
        : unwrap(
            await api.GET(CLUSTER_ITEM, {
              params: {
                path: {
                  cluster_id: clusterId as string,
                  storage_resource: resource,
                  storage_name: name as string,
                },
              },
              signal,
            }),
          ),
    enabled: Boolean(clusterId && name) && (!namespaced || Boolean(namespace)),
  });
}

type MutationTarget = {
  clusterId: string;
  namespace: string;
  resource: KubernetesStorageResource;
  dryRun: boolean;
  idempotencyKey: string;
};

function useStorageInvalidation() {
  const queryClient = useQueryClient();
  return async (input: MutationTarget & { name?: string }) => {
    await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
    if (input.dryRun) {
      return;
    }
    // A PVC binds a PV and a StorageClass provisions one, so a change to any of
    // them can change what the other two show.
    await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.storageResources });
    if (input.name) {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.storageResource });
    }
  };
}

export function useCreateStorageResource() {
  const invalidate = useStorageInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & {
        name: string;
        labels?: Record<string, string>;
        spec: StorageCreateSpec;
      },
    ) => {
      const body = {
        name: input.name,
        ...(input.labels ? { labels: input.labels } : {}),
        ...input.spec,
        dry_run: input.dryRun,
        confirm: !input.dryRun,
      };
      const resource = input.resource;
      return isNamespacedStorage(resource)
        ? unwrap(
            await api.POST(NAMESPACED_LIST, {
              params: {
                path: {
                  cluster_id: input.clusterId,
                  namespace_name: input.namespace,
                  storage_resource: resource,
                },
                header: idempotentHeaders(input.idempotencyKey),
              },
              body,
            }),
          )
        : unwrap(
            await api.POST(CLUSTER_LIST, {
              params: {
                path: { cluster_id: input.clusterId, storage_resource: resource },
                header: idempotentHeaders(input.idempotencyKey),
              },
              body,
            }),
          );
    },
    onSuccess: (_data, variables) => invalidate(variables),
  });
}

/**
 * Applies the single mutable field of one storage object.
 *
 * UID and resourceVersion are preconditions the Server checks against a fresh
 * read, so an edit of a stale view is refused rather than applied over whatever
 * changed in between.
 */
export function useUpdateStorageResource() {
  const invalidate = useStorageInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & {
        name: string;
        uid: string;
        resourceVersion: string;
        spec: StorageUpdateSpec;
      },
    ) => {
      const body = {
        uid: input.uid,
        resource_version: input.resourceVersion,
        ...input.spec,
        dry_run: input.dryRun,
        confirm: !input.dryRun,
      };
      const resource = input.resource;
      return isNamespacedStorage(resource)
        ? unwrap(
            await api.PUT(NAMESPACED_ITEM, {
              params: {
                path: {
                  cluster_id: input.clusterId,
                  namespace_name: input.namespace,
                  storage_resource: resource,
                  storage_name: input.name,
                },
                header: idempotentHeaders(input.idempotencyKey),
              },
              body,
            }),
          )
        : unwrap(
            await api.PUT(CLUSTER_ITEM, {
              params: {
                path: {
                  cluster_id: input.clusterId,
                  storage_resource: resource,
                  storage_name: input.name,
                },
                header: idempotentHeaders(input.idempotencyKey),
              },
              body,
            }),
          );
    },
    onSuccess: (_data, variables) => invalidate(variables),
  });
}

export function useDeleteStorageResource() {
  const invalidate = useStorageInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & { name: string; uid: string; resourceVersion: string },
    ) => {
      const body = {
        uid: input.uid,
        resource_version: input.resourceVersion,
        dry_run: input.dryRun,
        confirm: !input.dryRun,
      };
      const resource = input.resource;
      return isNamespacedStorage(resource)
        ? unwrap(
            await api.DELETE(NAMESPACED_ITEM, {
              params: {
                path: {
                  cluster_id: input.clusterId,
                  namespace_name: input.namespace,
                  storage_resource: resource,
                  storage_name: input.name,
                },
                header: idempotentHeaders(input.idempotencyKey),
              },
              body,
            }),
          )
        : unwrap(
            await api.DELETE(CLUSTER_ITEM, {
              params: {
                path: {
                  cluster_id: input.clusterId,
                  storage_resource: resource,
                  storage_name: input.name,
                },
                header: idempotentHeaders(input.idempotencyKey),
              },
              body,
            }),
          );
    },
    onSuccess: (_data, variables) => invalidate(variables),
  });
}
