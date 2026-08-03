import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";
import type { KubernetesResourceType } from "../types";

/** The Group/Version/Resource triple the generic endpoints route on. */
export type GenericResourceIdentity = {
  /** Empty for the core API group. */
  group: string;
  version: string;
  resource: string;
};

export type GenericResourceListParams = {
  limit?: number;
  continue?: string;
  label_selector?: string;
  field_selector?: string;
};

/**
 * One object as Kubernetes returned it.
 *
 * The browser shows arbitrary kinds, including CRs whose shape nothing in this
 * codebase knows, so the item stays unstructured and only `metadata` is read.
 */
export type UnstructuredObject = {
  apiVersion?: string;
  kind?: string;
  metadata?: {
    name?: string;
    namespace?: string;
    uid?: string;
    resourceVersion?: string;
    creationTimestamp?: string;
    labels?: Record<string, string>;
  };
  [key: string]: unknown;
};

const CATALOG_PATH = "/api/v1/clusters/{cluster_id}/kubernetes/resource-types";
const LIST_PATH = "/api/v1/clusters/{cluster_id}/kubernetes/resources";
const ITEM_PATH = "/api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}";

/**
 * The Cluster's API surface as its Agent currently sees it.
 *
 * Discovery is a live property of the Cluster — installing an operator changes
 * it — so this is read per Cluster and not cached beyond the session's normal
 * staleness rules.
 */
export function useResourceTypes(clusterId: string | null) {
  return useQuery({
    queryKey: queryKeys.resourceTypes(clusterId ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(CATALOG_PATH, {
          params: { path: { cluster_id: clusterId as string } },
          signal,
        }),
      ),
    enabled: Boolean(clusterId),
  });
}

/**
 * Lists objects of one kind.
 *
 * An empty Namespace means every Namespace, which is what the endpoint does
 * with the parameter absent; for a cluster-scoped kind it is the only valid
 * scope.
 */
export function useGenericResources(
  clusterId: string | null,
  identity: GenericResourceIdentity | null,
  namespace: string,
  params: GenericResourceListParams = {},
) {
  return useQuery({
    queryKey: queryKeys.genericResources(
      clusterId ?? "",
      identity ? `${identity.group}/${identity.version}/${identity.resource}` : "",
      namespace,
      params,
    ),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(LIST_PATH, {
          params: {
            path: { cluster_id: clusterId as string },
            query: {
              version: identity?.version as string,
              resource: identity?.resource as string,
              ...(identity?.group ? { group: identity.group } : {}),
              ...(namespace ? { namespace } : {}),
              ...params,
            },
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId && identity),
    // Switching Namespace or page keeps the previous rows on screen only while
    // the kind is unchanged: rows from another kind have different columns and
    // would read as this kind's data.
    placeholderData: (previous, previousQuery) => {
      const previousKey = previousQuery?.queryKey;
      const gvr = identity ? `${identity.group}/${identity.version}/${identity.resource}` : "";
      return previousKey?.[1] === clusterId && previousKey[2] === gvr ? previous : undefined;
    },
  });
}

/**
 * Deletes one object through the generic endpoint.
 *
 * The browser reaches kinds no typed section models, so the object's own UID and
 * resourceVersion are sent as Kubernetes preconditions: a delete built on a
 * stale row must not land on a same-named object created since.
 */
export function useDeleteGenericResource() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      identity: GenericResourceIdentity;
      namespace: string;
      name: string;
      uid: string;
      resourceVersion: string;
      dryRun: boolean;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.DELETE(ITEM_PATH, {
          params: {
            path: { cluster_id: input.clusterId, resource_name: input.name },
            query: {
              version: input.identity.version,
              resource: input.identity.resource,
              ...(input.identity.group ? { group: input.identity.group } : {}),
              ...(input.namespace ? { namespace: input.namespace } : {}),
            },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            dry_run: input.dryRun,
            confirm: !input.dryRun,
            propagation_policy: "background",
            preconditions: { uid: input.uid, resource_version: input.resourceVersion },
          },
        }),
      ),
    onSuccess: async (_data, variables) => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
      if (!variables.dryRun) {
        await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.genericResources });
      }
    },
  });
}

/** Sorts a catalog into `group → version → resource`, the way the tree shows it. */
export function groupResourceTypes(
  types: KubernetesResourceType[],
): { group: string; versions: { version: string; resources: KubernetesResourceType[] }[] }[] {
  const groups = new Map<string, Map<string, KubernetesResourceType[]>>();
  for (const type of types) {
    // The core group has no name in Kubernetes; the tree needs one to show.
    const group = type.group || "core";
    const versions = groups.get(group) ?? new Map<string, KubernetesResourceType[]>();
    const resources = versions.get(type.version) ?? [];
    resources.push(type);
    versions.set(type.version, resources);
    groups.set(group, versions);
  }
  return [...groups.entries()]
    .map(([group, versions]) => ({
      group,
      versions: [...versions.entries()]
        .map(([version, resources]) => ({
          version,
          resources: [...resources].sort((left, right) =>
            left.resource.localeCompare(right.resource),
          ),
        }))
        .sort((left, right) => left.version.localeCompare(right.version)),
    }))
    .sort((left, right) => left.group.localeCompare(right.group));
}
