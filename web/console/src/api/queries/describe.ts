import { useQuery } from "@tanstack/react-query";

import { api, unwrap } from "../client";
import type { KubernetesWorkloadResource } from "../types";
import { queryKeys } from "../query-keys";

export function useNodeDescribe(clusterId: string | null, name: string | null, enabled = true) {
  return useQuery({
    queryKey: queryKeys.nodeDescribe(clusterId ?? "", name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster_id}/nodes/{node_name}/describe", {
          params: {
            path: {
              cluster_id: clusterId as string,
              node_name: name as string,
            },
          },
          signal,
        }),
      ),
    enabled: enabled && Boolean(clusterId && name),
  });
}

export function usePersistentVolumeClaimDescribe(
  clusterId: string | null,
  namespace: string | null,
  name: string | null,
  enabled = true,
) {
  return useQuery({
    queryKey: queryKeys.persistentVolumeClaimDescribe(clusterId ?? "", namespace ?? "", name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/storage/{storage_resource}/{storage_name}/describe",
          {
            params: {
              path: {
                cluster_id: clusterId as string,
                namespace_name: namespace as string,
                storage_resource: "persistentvolumeclaims",
                storage_name: name as string,
              },
            },
            signal,
          },
        ),
      ),
    enabled: enabled && Boolean(clusterId && namespace && name),
  });
}

export function useServiceDescribe(
  clusterId: string | null,
  namespace: string | null,
  name: string | null,
  enabled = true,
) {
  return useQuery({
    queryKey: queryKeys.serviceDescribe(clusterId ?? "", namespace ?? "", name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/networking/{network_resource}/{network_name}/describe",
          {
            params: {
              path: {
                cluster_id: clusterId as string,
                namespace_name: namespace as string,
                network_resource: "services",
                network_name: name as string,
              },
            },
            signal,
          },
        ),
      ),
    enabled: enabled && Boolean(clusterId && namespace && name),
  });
}

export function useIngressDescribe(
  clusterId: string | null,
  namespace: string | null,
  name: string | null,
  enabled = true,
) {
  return useQuery({
    queryKey: queryKeys.ingressDescribe(clusterId ?? "", namespace ?? "", name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/networking/{network_resource}/{network_name}/describe",
          {
            params: {
              path: {
                cluster_id: clusterId as string,
                namespace_name: namespace as string,
                network_resource: "ingresses",
                network_name: name as string,
              },
            },
            signal,
          },
        ),
      ),
    enabled: enabled && Boolean(clusterId && namespace && name),
  });
}

export function useGatewayDescribe(
  clusterId: string | null,
  namespace: string | null,
  name: string | null,
  enabled = true,
) {
  return useQuery({
    queryKey: queryKeys.gatewayDescribe(clusterId ?? "", namespace ?? "", name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/networking/{network_resource}/{network_name}/describe",
          {
            params: {
              path: {
                cluster_id: clusterId as string,
                namespace_name: namespace as string,
                network_resource: "gateways",
                network_name: name as string,
              },
            },
            signal,
          },
        ),
      ),
    enabled: enabled && Boolean(clusterId && namespace && name),
  });
}

export function useAutoscalerDescribe(
  clusterId: string | null,
  namespace: string | null,
  name: string | null,
  enabled = true,
) {
  return useQuery({
    queryKey: queryKeys.autoscalerDescribe(clusterId ?? "", namespace ?? "", name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/autoscaling/horizontalpodautoscalers/{hpa_name}/describe",
          {
            params: {
              path: {
                cluster_id: clusterId as string,
                namespace_name: namespace as string,
                hpa_name: name as string,
              },
            },
            signal,
          },
        ),
      ),
    enabled: enabled && Boolean(clusterId && namespace && name),
  });
}

/**
 * Describe joins an object with the Kubernetes Events that name it, and reports
 * the findings the two together support.
 *
 * It is a separate read from the object's own detail rather than part of it,
 * because it answers to a second permission: reading Events is
 * `cluster.event.read`, and the Server requires both. A caller holding only
 * `cluster.read` is refused, so every entry point to this must be gated on
 * both — otherwise the Console offers a button that can only fail.
 */
export function usePodDescribe(
  clusterId: string | null,
  namespace: string | null,
  name: string | null,
  enabled = true,
) {
  return useQuery({
    queryKey: queryKeys.podDescribe(clusterId ?? "", namespace ?? "", name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/pods/{pod_name}/describe",
          {
            params: {
              path: {
                cluster_id: clusterId as string,
                namespace_name: namespace as string,
                pod_name: name as string,
              },
            },
            signal,
          },
        ),
      ),
    enabled: enabled && Boolean(clusterId && namespace && name),
  });
}

/**
 * A workload's describe, which is about more than the workload.
 *
 * The Server walks down to the objects it owns — the ReplicaSet that could not
 * create a Pod, the Pods that were created and did not start — because that is
 * where a workload's failure actually is. It is the same endpoint shape and the
 * same two permissions as the others.
 */
export function useWorkloadDescribe(
  clusterId: string | null,
  namespace: string | null,
  resource: KubernetesWorkloadResource | null,
  name: string | null,
  enabled = true,
) {
  return useQuery({
    queryKey: queryKeys.workloadDescribe(
      clusterId ?? "",
      namespace ?? "",
      resource ?? "",
      name ?? "",
    ),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}/describe",
          {
            params: {
              path: {
                cluster_id: clusterId as string,
                namespace_name: namespace as string,
                workload_resource: resource as KubernetesWorkloadResource,
                workload_name: name as string,
              },
            },
            signal,
          },
        ),
      ),
    enabled: enabled && Boolean(clusterId && namespace && resource && name),
  });
}

/** Which object a generic describe is about, in the terms the Server routes on. */
export type DescribeResourceTarget = {
  clusterId: string;
  /** Empty for the core API group. */
  group?: string;
  version: string;
  resource: string;
  /** Empty for cluster-scoped resources, which carry no Events. */
  namespace?: string;
  name: string;
};

export function useResourceDescribe(target: DescribeResourceTarget | null, enabled = true) {
  const gvr = target ? `${target.group ?? ""}/${target.version}/${target.resource}` : "";
  return useQuery({
    queryKey: queryKeys.resourceDescribe(
      target?.clusterId ?? "",
      target?.namespace ?? "",
      gvr,
      target?.name ?? "",
    ),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(
          "/api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}/describe",
          {
            params: {
              path: {
                cluster_id: (target as DescribeResourceTarget).clusterId,
                resource_name: (target as DescribeResourceTarget).name,
              },
              query: {
                ...(target?.group ? { group: target.group } : {}),
                version: target?.version as string,
                resource: target?.resource as string,
                ...(target?.namespace ? { namespace: target.namespace } : {}),
              },
            },
            signal,
          },
        ),
      ),
    enabled: enabled && Boolean(target),
  });
}
