import { useQuery, type QueryClient } from "@tanstack/react-query";

import { api, unwrap } from "../client";
import { queryKeys } from "../query-keys";

const LIST_PATH = "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/helm-releases";
const ITEM_PATH =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/helm-releases/{release_name}";
const REVISIONS_PATH =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/helm-releases/{release_name}/revisions";

/**
 * The Helm releases installed in one Namespace, each at its newest revision.
 *
 * These three are the reading half. Installing, upgrading, rolling back and
 * uninstalling live in `helm.ts` together with the chart catalogue: they are
 * executed by the Cluster's Agent running Helm's own engine, and they answer to
 * a longer permission stack than a read does.
 *
 * A release lives in a Secret, so every one of these needs `cluster.secret.read`
 * as well as `cluster.read`, and the Server audits them the way it audits a
 * Secret read.
 */
export function useHelmReleases(clusterId: string | null, namespace: string | null) {
  return useQuery({
    queryKey: queryKeys.helmReleases(clusterId ?? "", namespace ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(LIST_PATH, {
          params: {
            path: { cluster_id: clusterId as string, namespace_name: namespace as string },
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId && namespace),
  });
}

/**
 * One revision of one release, values included.
 *
 * `revision` selects an older one; omitting it reads whichever revision storage
 * currently holds as newest.
 */
export function useHelmRelease(
  clusterId: string | null,
  namespace: string | null,
  name: string | null,
  revision?: number,
) {
  return useQuery({
    ...helmReleaseQuery(clusterId ?? "", namespace ?? "", name ?? "", revision),
    enabled: Boolean(clusterId && namespace && name),
  });
}

function helmReleaseQuery(clusterId: string, namespace: string, name: string, revision?: number) {
  return {
    queryKey: queryKeys.helmRelease(clusterId, namespace, name, revision ?? 0),
    queryFn: async ({ signal }: { signal: AbortSignal }) =>
      unwrap(
        await api.GET(ITEM_PATH, {
          params: {
            path: {
              cluster_id: clusterId,
              namespace_name: namespace,
              release_name: name,
            },
            ...(revision ? { query: { revision } } : {}),
          },
          signal,
        }),
      ),
  };
}

/**
 * Read one release on demand, outside a render.
 *
 * A listing carries only what the release Secrets' labels say — decompressing
 * every release to draw a table would be a page of Secrets read for four
 * columns — so an action that needs the values, such as opening the upgrade
 * form, asks for the one release it is about at the moment it is clicked. It
 * goes through the same cache as the hook, so opening a release that was just
 * read costs nothing.
 */
export function fetchHelmRelease(
  queryClient: QueryClient,
  clusterId: string,
  namespace: string,
  name: string,
) {
  return queryClient.fetchQuery(helmReleaseQuery(clusterId, namespace, name));
}

/**
 * The revisions storage still holds for one release.
 *
 * Helm keeps a Secret per revision and trims them by `--history-max`, so this is
 * what is actually retained rather than everything that ever happened.
 */
export function useHelmReleaseRevisions(
  clusterId: string | null,
  namespace: string | null,
  name: string | null,
) {
  return useQuery({
    queryKey: queryKeys.helmReleaseRevisions(clusterId ?? "", namespace ?? "", name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(REVISIONS_PATH, {
          params: {
            path: {
              cluster_id: clusterId as string,
              namespace_name: namespace as string,
              release_name: name as string,
            },
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId && namespace && name),
  });
}
