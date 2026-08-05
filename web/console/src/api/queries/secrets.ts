import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";

export type SecretListParams = {
  limit?: number;
  continue?: string;
  label_selector?: string;
  field_selector?: string;
};

const LIST_PATH = "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/secrets";
const ITEM_PATH = "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/secrets/{secret_name}";

/**
 * Lists the Secrets of one Namespace of one Cluster.
 *
 * The list carries key names and sizes but no values: a credential is read one
 * object at a time, through the detail endpoint, by someone who asked for that
 * object. Secrets belonging to the ZKE installation are not in the list at all,
 * so a page can be shorter than the requested limit.
 *
 * These calls require `cluster.secret.read` and `cluster.secret.manage` rather
 * than the general cluster permissions.
 */
export function useSecrets(
  clusterId: string | null,
  namespace: string | null,
  params: SecretListParams = {},
) {
  return useQuery({
    queryKey: queryKeys.secrets(clusterId ?? "", namespace ?? "", params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(LIST_PATH, {
          params: {
            path: { cluster_id: clusterId as string, namespace_name: namespace as string },
            query: params,
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId && namespace),
    placeholderData: (previous, previousQuery) => {
      const previousKey = previousQuery?.queryKey;
      return previousKey?.[1] === clusterId && previousKey[2] === namespace ? previous : undefined;
    },
  });
}

export function useSecret(
  clusterId: string | null,
  namespace: string | null,
  name: string | null,
  editor = false,
) {
  return useQuery({
    queryKey: queryKeys.secret(clusterId ?? "", namespace ?? "", name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(ITEM_PATH, {
          params: {
            path: {
              cluster_id: clusterId as string,
              namespace_name: namespace as string,
              secret_name: name as string,
            },
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId && namespace && name),
    // The editor opens on a fresh read: an update carries the resourceVersion it
    // was read at, and a cached one is a conflict waiting to happen.
    staleTime: 0,
    refetchOnMount: "always",
    // Once an editor has pinned the fetched UID/resourceVersion and copied the
    // body into local form state, a focus/reconnect refetch must not replace the
    // parent query state and tear down an in-progress edit.
    refetchOnWindowFocus: !editor,
    refetchOnReconnect: !editor,
  });
}

type MutationTarget = {
  clusterId: string;
  namespace: string;
  dryRun: boolean;
  idempotencyKey: string;
};

function useSecretInvalidation() {
  const queryClient = useQueryClient();
  return async (input: MutationTarget & { name?: string }) => {
    await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
    if (input.dryRun) {
      return;
    }
    const invalidations = [
      queryClient.invalidateQueries({
        queryKey: [...queryKeyPrefixes.secrets, input.clusterId, input.namespace],
      }),
    ];
    if (input.name) {
      invalidations.push(
        queryClient.invalidateQueries({
          queryKey: queryKeys.secret(input.clusterId, input.namespace, input.name),
        }),
      );
    }
    await Promise.all(invalidations);
  };
}

export function useCreateSecret() {
  const invalidate = useSecretInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & {
        name: string;
        labels?: Record<string, string>;
        annotations?: Record<string, string>;
        type?: string;
        /** Standard padded Base64, as the detail returns them. */
        data: Record<string, string>;
        immutable: boolean;
      },
    ) =>
      unwrap(
        await api.POST(LIST_PATH, {
          params: {
            path: { cluster_id: input.clusterId, namespace_name: input.namespace },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            name: input.name,
            ...(input.type ? { type: input.type as "Opaque" } : {}),
            ...(input.labels ? { labels: input.labels } : {}),
            ...(input.annotations ? { annotations: input.annotations } : {}),
            data: input.data,
            immutable: input.immutable,
            dry_run: input.dryRun,
            confirm: !input.dryRun,
          },
        }),
      ),
    onSuccess: (_data, variables) => invalidate(variables),
  });
}

/**
 * Replaces a Secret's contents.
 *
 * `data` is sent in full every time — this is a replacement, not a merge, so a
 * key left out of the request is a key removed from the object. UID and
 * resourceVersion are the preconditions: an edit of a stale read is refused
 * rather than applied over whatever changed in between.
 *
 * `type` is not in the request: Kubernetes fixes it at creation. An immutable
 * Secret cannot be updated at all, and `immutable` cannot be turned back off.
 */
export function useUpdateSecret() {
  const invalidate = useSecretInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & {
        name: string;
        uid: string;
        resourceVersion: string;
        data: Record<string, string>;
        immutable?: boolean;
      },
    ) =>
      unwrap(
        await api.PUT(ITEM_PATH, {
          params: {
            path: {
              cluster_id: input.clusterId,
              namespace_name: input.namespace,
              secret_name: input.name,
            },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            uid: input.uid,
            resource_version: input.resourceVersion,
            data: input.data,
            ...(input.immutable === undefined ? {} : { immutable: input.immutable }),
            dry_run: input.dryRun,
            confirm: !input.dryRun,
          },
        }),
      ),
    onSuccess: (_data, variables) => invalidate(variables),
  });
}

export function useDeleteSecret() {
  const queryClient = useQueryClient();
  const invalidate = useSecretInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & { name: string; uid: string; resourceVersion: string },
    ) =>
      unwrap(
        await api.DELETE(ITEM_PATH, {
          params: {
            path: {
              cluster_id: input.clusterId,
              namespace_name: input.namespace,
              secret_name: input.name,
            },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            uid: input.uid,
            resource_version: input.resourceVersion,
            dry_run: input.dryRun,
            confirm: !input.dryRun,
          },
        }),
      ),
    onSuccess: async (_data, variables) => {
      if (!variables.dryRun) {
        queryClient.removeQueries({
          queryKey: queryKeys.secret(variables.clusterId, variables.namespace, variables.name),
        });
      }
      await invalidate(variables);
    },
  });
}
