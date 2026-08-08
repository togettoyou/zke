import { useQuery } from "@tanstack/react-query";

import { api, unwrap } from "../client";
import { queryKeys } from "../query-keys";

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
