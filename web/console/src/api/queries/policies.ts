import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";
import type {
  KubernetesDisruptionBudgetSpecInput,
  KubernetesLimitRangeSpecInput,
  KubernetesNetworkPolicySpecInput,
  KubernetesPolicyResource,
  KubernetesPriorityClassSpecInput,
  KubernetesPriorityClassUpdateInput,
  KubernetesResourceQuotaSpecInput,
} from "../types";

export type PolicyListParams = {
  limit?: number;
  continue?: string;
  label_selector?: string;
  field_selector?: string;
};

/** The one type-specific block a create request carries. */
export type PolicyCreateSpec = {
  resource_quota?: KubernetesResourceQuotaSpecInput;
  limit_range?: KubernetesLimitRangeSpecInput;
  network_policy?: KubernetesNetworkPolicySpecInput;
  disruption_budget?: KubernetesDisruptionBudgetSpecInput;
  priority_class?: KubernetesPriorityClassSpecInput;
};

/**
 * What an update may change.
 *
 * Four kinds take their whole managed spec back, because a policy is read as
 * one statement and editing it field by field would leave the parts nobody
 * touched looking deliberate when they are merely old. PriorityClass is the
 * exception: Kubernetes freezes its value at creation, so only the description
 * and the cluster-default switch are offered.
 */
export type PolicyUpdateSpec = {
  resource_quota?: KubernetesResourceQuotaSpecInput;
  limit_range?: KubernetesLimitRangeSpecInput;
  network_policy?: KubernetesNetworkPolicySpecInput;
  disruption_budget?: KubernetesDisruptionBudgetSpecInput;
  priority_class?: KubernetesPriorityClassUpdateInput;
};

const CLUSTER_LIST = "/api/v1/clusters/{cluster_id}/policies/{policy_resource}";
const CLUSTER_ITEM = "/api/v1/clusters/{cluster_id}/policies/{policy_resource}/{policy_name}";
const NAMESPACED_LIST =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/policies/{policy_resource}";
const NAMESPACED_ITEM =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/policies/{policy_resource}/{policy_name}";

/**
 * PriorityClass ranks Pods across the whole Cluster; the other four constrain
 * one Namespace.
 *
 * Written as a type predicate so the narrowed type reaches the path parameters:
 * the Server exposes the two scopes as different routes and rejects the wrong
 * one, and the generated types accept only the resources each route serves.
 */
export function isNamespacedPolicy(
  resource: KubernetesPolicyResource,
): resource is "resourcequotas" | "limitranges" | "networkpolicies" | "poddisruptionbudgets" {
  return resource !== "priorityclasses";
}

export function usePolicyResources(
  clusterId: string | null,
  namespace: string | null,
  resource: KubernetesPolicyResource,
  params: PolicyListParams = {},
) {
  const namespaced = isNamespacedPolicy(resource);
  return useQuery({
    queryKey: queryKeys.policyResources(
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
                  policy_resource: resource,
                },
                query: params,
              },
              signal,
            }),
          )
        : unwrap(
            await api.GET(CLUSTER_LIST, {
              params: {
                path: { cluster_id: clusterId as string, policy_resource: resource },
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

export function usePolicyResource(
  clusterId: string | null,
  namespace: string | null,
  resource: KubernetesPolicyResource,
  name: string | null,
) {
  const namespaced = isNamespacedPolicy(resource);
  return useQuery({
    queryKey: queryKeys.policyResource(
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
                  policy_resource: resource,
                  policy_name: name as string,
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
                  policy_resource: resource,
                  policy_name: name as string,
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
  resource: KubernetesPolicyResource;
  dryRun: boolean;
  idempotencyKey: string;
};

function usePolicyInvalidation() {
  const queryClient = useQueryClient();
  return async (input: MutationTarget & { name?: string }) => {
    await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
    if (input.dryRun) {
      return;
    }
    await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.policyResources });
    if (input.name) {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.policyResource });
    }
  };
}

export function useCreatePolicyResource() {
  const invalidate = usePolicyInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & {
        name: string;
        labels?: Record<string, string>;
        spec: PolicyCreateSpec;
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
      return isNamespacedPolicy(resource)
        ? unwrap(
            await api.POST(NAMESPACED_LIST, {
              params: {
                path: {
                  cluster_id: input.clusterId,
                  namespace_name: input.namespace,
                  policy_resource: resource,
                },
                header: idempotentHeaders(input.idempotencyKey),
              },
              body,
            }),
          )
        : unwrap(
            await api.POST(CLUSTER_LIST, {
              params: {
                path: { cluster_id: input.clusterId, policy_resource: resource },
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
 * Replaces the managed spec of one policy object.
 *
 * UID and resourceVersion are preconditions the Server checks against a fresh
 * read, so an edit of a stale view is refused rather than applied over whatever
 * changed in between — which for a policy means a constraint someone else
 * tightened would otherwise be silently reverted.
 */
export function useUpdatePolicyResource() {
  const invalidate = usePolicyInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & {
        name: string;
        uid: string;
        resourceVersion: string;
        spec: PolicyUpdateSpec;
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
      return isNamespacedPolicy(resource)
        ? unwrap(
            await api.PUT(NAMESPACED_ITEM, {
              params: {
                path: {
                  cluster_id: input.clusterId,
                  namespace_name: input.namespace,
                  policy_resource: resource,
                  policy_name: input.name,
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
                  policy_resource: resource,
                  policy_name: input.name,
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

export function useDeletePolicyResource() {
  const invalidate = usePolicyInvalidation();
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
      return isNamespacedPolicy(resource)
        ? unwrap(
            await api.DELETE(NAMESPACED_ITEM, {
              params: {
                path: {
                  cluster_id: input.clusterId,
                  namespace_name: input.namespace,
                  policy_resource: resource,
                  policy_name: input.name,
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
                  policy_resource: resource,
                  policy_name: input.name,
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
