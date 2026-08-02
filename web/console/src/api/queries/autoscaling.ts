import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";
import type { KubernetesHPADetail, KubernetesHPASpecInput, KubernetesHPASummary } from "../types";

export type AutoscalerListParams = {
  limit?: number;
  continue?: string;
  label_selector?: string;
  field_selector?: string;
};

export type AutoscalerSummary = KubernetesHPASummary;
export type AutoscalerDetail = KubernetesHPADetail;

/** True when the controller has not yet acted on the current spec. */
export function isAutoscalerStale(
  item: Pick<KubernetesHPASummary, "generation" | "observed_generation">,
): boolean {
  return item.observed_generation === null || item.observed_generation < item.generation;
}

const LIST_PATH =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/autoscaling/horizontalpodautoscalers";
const ITEM_PATH =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/autoscaling/horizontalpodautoscalers/{hpa_name}";

export function useAutoscalers(
  clusterId: string | null,
  namespace: string | null,
  params: AutoscalerListParams = {},
) {
  return useQuery({
    queryKey: queryKeys.autoscalers(clusterId ?? "", namespace ?? "", params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(LIST_PATH, {
          params: {
            path: { cluster_id: clusterId as string, namespace_name: namespace as string },
            query: params,
          },
          signal,
        }),
      ) as { autoscalers: AutoscalerSummary[]; continue_token: string },
    enabled: Boolean(clusterId && namespace),
    placeholderData: (previous, previousQuery) => {
      const previousKey = previousQuery?.queryKey;
      return previousKey?.[1] === clusterId && previousKey[2] === namespace ? previous : undefined;
    },
  });
}

export function useAutoscaler(
  clusterId: string | null,
  namespace: string | null,
  name: string | null,
) {
  return useQuery({
    queryKey: queryKeys.autoscaler(clusterId ?? "", namespace ?? "", name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(ITEM_PATH, {
          params: {
            path: {
              cluster_id: clusterId as string,
              namespace_name: namespace as string,
              hpa_name: name as string,
            },
          },
          signal,
        }),
      ) as AutoscalerDetail,
    enabled: Boolean(clusterId && namespace && name),
  });
}

type MutationTarget = {
  clusterId: string;
  namespace: string;
  dryRun: boolean;
  idempotencyKey: string;
};

function useAutoscalerInvalidation() {
  const queryClient = useQueryClient();
  return async (input: MutationTarget & { name?: string }) => {
    await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
    if (input.dryRun) {
      return;
    }
    await queryClient.invalidateQueries({
      queryKey: ["autoscalers", input.clusterId, input.namespace],
    });
    // An HPA owns its target's replica count, so the workload views are stale
    // the moment one is created, retargeted or removed.
    await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.workloads });
    if (input.name) {
      await queryClient.invalidateQueries({
        queryKey: queryKeys.autoscaler(input.clusterId, input.namespace, input.name),
      });
    }
  };
}

export function useCreateAutoscaler() {
  const invalidate = useAutoscalerInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & {
        name: string;
        labels?: Record<string, string>;
        spec: KubernetesHPASpecInput;
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
            ...(input.labels ? { labels: input.labels } : {}),
            spec: input.spec,
            dry_run: input.dryRun,
            confirm: !input.dryRun,
          },
        }),
      ),
    onSuccess: (_data, variables) => invalidate(variables),
  });
}

/**
 * Replaces an autoscaler's spec.
 *
 * The whole spec goes every time — target, bounds, metrics and behaviour — so a
 * metric left out of the request is a metric removed from the object. UID and
 * resourceVersion are the preconditions the Server checks against a fresh read.
 */
export function useUpdateAutoscaler() {
  const invalidate = useAutoscalerInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & {
        name: string;
        uid: string;
        resourceVersion: string;
        spec: KubernetesHPASpecInput;
      },
    ) =>
      unwrap(
        await api.PUT(ITEM_PATH, {
          params: {
            path: {
              cluster_id: input.clusterId,
              namespace_name: input.namespace,
              hpa_name: input.name,
            },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            uid: input.uid,
            resource_version: input.resourceVersion,
            spec: input.spec,
            dry_run: input.dryRun,
            confirm: !input.dryRun,
          },
        }),
      ),
    onSuccess: (_data, variables) => invalidate(variables),
  });
}

export function useDeleteAutoscaler() {
  const queryClient = useQueryClient();
  const invalidate = useAutoscalerInvalidation();
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
              hpa_name: input.name,
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
          queryKey: queryKeys.autoscaler(variables.clusterId, variables.namespace, variables.name),
        });
      }
      await invalidate(variables);
    },
  });
}
