import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";
import type {
  KubernetesAuthorizationPolicyRule,
  KubernetesAuthorizationResource,
  KubernetesAuthorizationRoleRef,
  KubernetesAuthorizationSubject,
} from "../types";

export type AuthorizationListParams = {
  limit?: number;
  continue?: string;
  label_selector?: string;
  field_selector?: string;
};

const CLUSTER_LIST = "/api/v1/clusters/{cluster_id}/authorization/{authorization_resource}";
const CLUSTER_ITEM =
  "/api/v1/clusters/{cluster_id}/authorization/{authorization_resource}/{authorization_name}";
const NAMESPACED_LIST =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/authorization/{authorization_resource}";
const NAMESPACED_ITEM =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/authorization/{authorization_resource}/{authorization_name}";

/**
 * ClusterRole and ClusterRoleBinding are cluster objects; the other three live
 * in a Namespace. Written as a type predicate so the narrowed type reaches the
 * path parameters — the Server exposes the two scopes as different routes and
 * the generated types accept only what each route serves.
 */
export function isNamespacedAuthorization(
  resource: KubernetesAuthorizationResource,
): resource is "serviceaccounts" | "roles" | "rolebindings" {
  return resource !== "clusterroles" && resource !== "clusterrolebindings";
}

export function useAuthorizationResources(
  clusterId: string | null,
  namespace: string | null,
  resource: KubernetesAuthorizationResource,
  params: AuthorizationListParams = {},
) {
  const namespaced = isNamespacedAuthorization(resource);
  return useQuery({
    queryKey: queryKeys.authorizationResources(
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
                  authorization_resource: resource,
                },
                query: params,
              },
              signal,
            }),
          )
        : unwrap(
            await api.GET(CLUSTER_LIST, {
              params: {
                path: { cluster_id: clusterId as string, authorization_resource: resource },
                query: params,
              },
              signal,
            }),
          ),
    enabled: Boolean(clusterId) && (!namespaced || Boolean(namespace)),
    placeholderData: (previous, previousQuery) => {
      const previousKey = previousQuery?.queryKey;
      return previousKey?.[1] === clusterId && previousKey[3] === resource ? previous : undefined;
    },
  });
}

export function useAuthorizationResource(
  clusterId: string | null,
  namespace: string | null,
  resource: KubernetesAuthorizationResource,
  name: string | null,
) {
  const namespaced = isNamespacedAuthorization(resource);
  return useQuery({
    queryKey: queryKeys.authorizationResource(
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
                  authorization_resource: resource,
                  authorization_name: name as string,
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
                  authorization_resource: resource,
                  authorization_name: name as string,
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
  resource: KubernetesAuthorizationResource;
  dryRun: boolean;
  idempotencyKey: string;
};

function useAuthorizationInvalidation() {
  const queryClient = useQueryClient();
  return async (input: MutationTarget & { name?: string }) => {
    await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
    if (input.dryRun) {
      return;
    }
    // A Role and the bindings that reference it are read from different lists,
    // so any write here can change what more than one of them shows.
    await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.authorizationResources });
    if (input.name) {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.authorizationResource });
    }
  };
}

/** The type-specific parts of a create request. */
export type AuthorizationCreateSpec = {
  automount_service_account_token?: boolean;
  rules?: KubernetesAuthorizationPolicyRule[];
  subjects?: KubernetesAuthorizationSubject[];
  role_ref?: KubernetesAuthorizationRoleRef;
};

/** `role_ref` is absent: Kubernetes freezes a binding's roleRef at creation. */
export type AuthorizationUpdateSpec = Omit<AuthorizationCreateSpec, "role_ref">;

export function useCreateAuthorizationResource() {
  const invalidate = useAuthorizationInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & {
        name: string;
        labels?: Record<string, string>;
        spec: AuthorizationCreateSpec;
      },
    ) => {
      const resource = input.resource;
      const body = {
        name: input.name,
        ...(input.labels ? { labels: input.labels } : {}),
        ...input.spec,
        dry_run: input.dryRun,
        confirm: !input.dryRun,
      };
      return isNamespacedAuthorization(resource)
        ? unwrap(
            await api.POST(NAMESPACED_LIST, {
              params: {
                path: {
                  cluster_id: input.clusterId,
                  namespace_name: input.namespace,
                  authorization_resource: resource,
                },
                header: idempotentHeaders(input.idempotencyKey),
              },
              body,
            }),
          )
        : unwrap(
            await api.POST(CLUSTER_LIST, {
              params: {
                path: { cluster_id: input.clusterId, authorization_resource: resource },
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
 * Replaces the rules or subjects of one authorization object.
 *
 * A binding's `roleRef` is not in the request because Kubernetes will not change
 * it: pointing a binding at a different Role means deleting and recreating it,
 * which is deliberate — silently re-aiming an existing grant is exactly the
 * change nobody notices.
 */
export function useUpdateAuthorizationResource() {
  const invalidate = useAuthorizationInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & {
        name: string;
        uid: string;
        resourceVersion: string;
        spec: AuthorizationUpdateSpec;
      },
    ) => {
      const resource = input.resource;
      const body = {
        uid: input.uid,
        resource_version: input.resourceVersion,
        ...input.spec,
        dry_run: input.dryRun,
        confirm: !input.dryRun,
      };
      return isNamespacedAuthorization(resource)
        ? unwrap(
            await api.PUT(NAMESPACED_ITEM, {
              params: {
                path: {
                  cluster_id: input.clusterId,
                  namespace_name: input.namespace,
                  authorization_resource: resource,
                  authorization_name: input.name,
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
                  authorization_resource: resource,
                  authorization_name: input.name,
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

export function useDeleteAuthorizationResource() {
  const invalidate = useAuthorizationInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & { name: string; uid: string; resourceVersion: string },
    ) => {
      const resource = input.resource;
      const body = {
        uid: input.uid,
        resource_version: input.resourceVersion,
        dry_run: input.dryRun,
        confirm: !input.dryRun,
      };
      return isNamespacedAuthorization(resource)
        ? unwrap(
            await api.DELETE(NAMESPACED_ITEM, {
              params: {
                path: {
                  cluster_id: input.clusterId,
                  namespace_name: input.namespace,
                  authorization_resource: resource,
                  authorization_name: input.name,
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
                  authorization_resource: resource,
                  authorization_name: input.name,
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
