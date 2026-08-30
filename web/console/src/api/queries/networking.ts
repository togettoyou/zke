import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";
import type {
  KubernetesGatewaySpecInput,
  KubernetesGatewayRouteSpecInput,
  KubernetesEndpointSpecInput,
  KubernetesIngressSpecInput,
  KubernetesNetworkingResource,
  KubernetesNetworkingResourceDetail,
  KubernetesNetworkingResourcePage,
  KubernetesNetworkingResourceSummary,
  KubernetesServiceSpecInput,
} from "../types";

export type NetworkingSummary = KubernetesNetworkingResourceSummary;
export type NetworkingDetail = KubernetesNetworkingResourceDetail;
export type NetworkingPage = KubernetesNetworkingResourcePage;

export type NetworkingListParams = {
  limit?: number;
  continue?: string;
  label_selector?: string;
  field_selector?: string;
};

/** The one type-specific block a create or update request carries. */
export type NetworkingSpecInput = {
  service?: KubernetesServiceSpecInput;
  endpoint?: KubernetesEndpointSpecInput;
  ingress?: KubernetesIngressSpecInput;
  gateway?: KubernetesGatewaySpecInput;
  gateway_route?: KubernetesGatewayRouteSpecInput;
};

const LIST_PATH =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/networking/{network_resource}";
const ITEM_PATH =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/networking/{network_resource}/{network_name}";

/**
 * Reads one kind of networking object out of one Namespace of one Cluster.
 *
 * Cluster, Namespace and type are path segments rather than filters, so a query
 * cannot widen itself. A Cluster without the Gateway API CRDs answers `409
 * gateway_api_unavailable` for `gateways` — which is a fact about the Cluster,
 * not a failure of the request, and the section says so rather than retrying.
 */
export function useNetworkingResources(
  clusterId: string | null,
  namespace: string | null,
  resource: KubernetesNetworkingResource,
  params: NetworkingListParams = {},
) {
  return useQuery({
    queryKey: queryKeys.networkingResources(clusterId ?? "", namespace ?? "", resource, params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(LIST_PATH, {
          params: {
            path: {
              cluster_id: clusterId as string,
              namespace_name: namespace as string,
              network_resource: resource,
            },
            query: params,
          },
          signal,
        }),
      ) as NetworkingPage,
    enabled: Boolean(clusterId && namespace),
    // Hold a page only while paging the same scoped collection.
    placeholderData: (previous, previousQuery) => {
      const previousKey = previousQuery?.queryKey;
      return previousKey?.[1] === clusterId &&
        previousKey[2] === namespace &&
        previousKey[3] === resource
        ? previous
        : undefined;
    },
    // A Cluster that has no Gateway API will keep answering 409, and a Service
    // list that failed once is better retried by the operator than by a loop.
    retry: false,
  });
}

export function useNetworkingResource(
  clusterId: string | null,
  namespace: string | null,
  resource: KubernetesNetworkingResource,
  name: string | null,
) {
  return useQuery({
    queryKey: queryKeys.networkingResource(clusterId ?? "", namespace ?? "", resource, name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(ITEM_PATH, {
          params: {
            path: {
              cluster_id: clusterId as string,
              namespace_name: namespace as string,
              network_resource: resource,
              network_name: name as string,
            },
          },
          signal,
        }),
      ) as NetworkingDetail,
    enabled: Boolean(clusterId && namespace && name),
    retry: false,
  });
}

type MutationTarget = {
  clusterId: string;
  namespace: string;
  resource: KubernetesNetworkingResource;
  dryRun: boolean;
  idempotencyKey: string;
};

function useNetworkingInvalidation() {
  const queryClient = useQueryClient();
  return async (input: MutationTarget & { name?: string }) => {
    await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
    if (input.dryRun) {
      return;
    }
    const invalidations = [
      queryClient.invalidateQueries({
        queryKey: ["networking-resources", input.clusterId, input.namespace, input.resource],
      }),
    ];
    if (input.name) {
      invalidations.push(
        queryClient.invalidateQueries({
          queryKey: queryKeys.networkingResource(
            input.clusterId,
            input.namespace,
            input.resource,
            input.name,
          ),
        }),
      );
    }
    await Promise.all(invalidations);
  };
}

export function useCreateNetworkingResource() {
  const invalidate = useNetworkingInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & {
        name: string;
        labels?: Record<string, string>;
        annotations?: Record<string, string>;
        spec: NetworkingSpecInput;
      },
    ) =>
      unwrap(
        await api.POST(LIST_PATH, {
          params: {
            path: {
              cluster_id: input.clusterId,
              namespace_name: input.namespace,
              network_resource: input.resource,
            },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            name: input.name,
            ...(input.labels ? { labels: input.labels } : {}),
            ...(input.annotations ? { annotations: input.annotations } : {}),
            ...input.spec,
            dry_run: input.dryRun,
            confirm: !input.dryRun,
          },
        }),
      ),
    onSuccess: (_data, variables) => invalidate(variables),
  });
}

/**
 * Replaces the modelled part of one object's configuration.
 *
 * UID and resourceVersion are required rather than optional: the Server re-reads
 * the object and refuses both a same-named replacement and a stale edit. Fields
 * the API does not model — a Service's assigned ClusterIP and NodePorts, a
 * Gateway's CRD extensions — are preserved by the Server, so an update here
 * cannot silently drop what this Console never showed.
 */
export function useUpdateNetworkingResource() {
  const invalidate = useNetworkingInvalidation();
  return useMutation({
    mutationFn: async (
      input: MutationTarget & {
        name: string;
        uid: string;
        resourceVersion: string;
        spec: NetworkingSpecInput;
      },
    ) =>
      unwrap(
        await api.PUT(ITEM_PATH, {
          params: {
            path: {
              cluster_id: input.clusterId,
              namespace_name: input.namespace,
              network_resource: input.resource,
              network_name: input.name,
            },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            uid: input.uid,
            resource_version: input.resourceVersion,
            ...input.spec,
            dry_run: input.dryRun,
            confirm: !input.dryRun,
          },
        }),
      ),
    onSuccess: (_data, variables) => invalidate(variables),
  });
}

export function useDeleteNetworkingResource() {
  const queryClient = useQueryClient();
  const invalidate = useNetworkingInvalidation();
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
              network_resource: input.resource,
              network_name: input.name,
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
          queryKey: queryKeys.networkingResource(
            variables.clusterId,
            variables.namespace,
            variables.resource,
            variables.name,
          ),
        });
      }
      await invalidate(variables);
    },
  });
}
