import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type { HelmRepositoryRequest } from "../types";
import { api, csrfHeaders, idempotentHeaders, unwrap } from "../client";
import { queryKeyPrefixes, queryKeys } from "../query-keys";

/**
 * The chart catalogue and the release lifecycle.
 *
 * The read-only release views live in `helm-releases.ts`, which reads Helm's own
 * storage in one Namespace. Everything here is the other half: which charts may
 * be installed at all, and the four operations that change what is installed.
 *
 * Two permission families meet here and are worth keeping straight. The
 * catalogue is platform-wide and answers to `helm.repository.read` and
 * `helm.repository.manage`; a release change happens in one Cluster and answers
 * to `cluster.helm.manage` plus the object permissions the operation spends.
 * Choosing a chart is therefore possible for an operator who cannot install it
 * anywhere, and installing one is possible for an operator who cannot add the
 * repository it came from.
 */

const REPOSITORIES_PATH = "/api/v1/helm/repositories";
const REPOSITORY_PATH = "/api/v1/helm/repositories/{repository_id}";
const CHARTS_PATH = "/api/v1/helm/repositories/{repository_id}/charts";
const CHART_PATH = "/api/v1/helm/repositories/{repository_id}/charts/{chart_name}";
const CHART_VERSIONS_PATH =
  "/api/v1/helm/repositories/{repository_id}/charts/{chart_name}/versions";
const REFRESH_PATH = "/api/v1/helm/repositories/{repository_id}/index-refresh";
const RELEASES_PATH = "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/helm-releases";
const RELEASE_PATH =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/helm-releases/{release_name}";
const ROLLBACK_PATH =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/helm-releases/{release_name}/rollback";

export function useHelmRepositories(enabled = true) {
  return useQuery({
    queryKey: queryKeys.helmRepositories(),
    queryFn: async ({ signal }) => unwrap(await api.GET(REPOSITORIES_PATH, { signal })),
    enabled,
  });
}

/**
 * The charts one repository publishes.
 *
 * The Server holds the repository's index for a few minutes, so typing in a
 * search box costs one request rather than one download of the whole index. The
 * search itself is applied on the Server for the same reason — a public index
 * runs to thousands of charts, and shipping all of them to filter in the browser
 * would be the download this avoids.
 */
export function useHelmCharts(repositoryId: string | null, search: string) {
  return useQuery({
    queryKey: queryKeys.helmCharts(repositoryId ?? "", search),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(CHARTS_PATH, {
          params: {
            path: { repository_id: repositoryId as string },
            query: search ? { search } : {},
          },
          signal,
        }),
      ),
    enabled: Boolean(repositoryId),
    // Keeping the previous page while a new search resolves stops the list from
    // blanking on every keystroke, but only within one repository: charts from
    // another repository are a different catalogue, not a slower answer.
    placeholderData: (previous, previousQuery) =>
      previousQuery?.queryKey[1] === repositoryId ? previous : undefined,
  });
}

/**
 * Re-reads one repository's index and returns the refreshed listing.
 *
 * The Server holds the index for a few minutes, so an ordinary refetch answers
 * from that cache and a chart published a minute ago stays invisible. This is
 * the way to say "go and look again"; it writes nothing, and the result is
 * seeded into the listing's cache so the page updates without a second trip.
 */
export function useRefreshHelmCharts() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { repositoryId: string; search: string }) =>
      unwrap(
        await api.POST(REFRESH_PATH, {
          params: {
            path: { repository_id: input.repositoryId },
            query: input.search ? { search: input.search } : {},
            header: csrfHeaders(),
          },
        }),
      ),
    onSuccess: async (data, variables) => {
      queryClient.setQueryData(
        queryKeys.helmCharts(variables.repositoryId, variables.search),
        data,
      );
      // Chart detail and version lists were read from the same index.
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["helm-chart"] }),
        queryClient.invalidateQueries({ queryKey: ["helm-chart-versions"] }),
        queryClient.invalidateQueries({ queryKey: ["helm-charts"] }),
      ]);
    },
  });
}

export function useHelmChartVersions(repositoryId: string | null, chart: string | null) {
  return useQuery({
    queryKey: queryKeys.helmChartVersions(repositoryId ?? "", chart ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(CHART_VERSIONS_PATH, {
          params: {
            path: { repository_id: repositoryId as string, chart_name: chart as string },
          },
          signal,
        }),
      ),
    enabled: Boolean(repositoryId && chart),
  });
}

/**
 * One chart version, with its own values.yaml and README.
 *
 * This downloads the chart on the Server, so it is requested when an operator
 * opens a chart rather than for every row of a listing.
 */
export function useHelmChart(repositoryId: string | null, chart: string | null, version: string) {
  return useQuery({
    queryKey: queryKeys.helmChart(repositoryId ?? "", chart ?? "", version),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(CHART_PATH, {
          params: {
            path: { repository_id: repositoryId as string, chart_name: chart as string },
            ...(version ? { query: { version } } : {}),
          },
          signal,
        }),
      ),
    enabled: Boolean(repositoryId && chart),
  });
}

export function useCreateHelmRepository() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: HelmRepositoryRequest) =>
      unwrap(
        await api.POST(REPOSITORIES_PATH, {
          params: { header: csrfHeaders() },
          body: input,
        }),
      ),
    onSuccess: () => invalidateCatalogue(queryClient),
  });
}

export function useUpdateHelmRepository() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: { repositoryId: string; body: HelmRepositoryRequest }) =>
      unwrap(
        await api.PUT(REPOSITORY_PATH, {
          params: {
            path: { repository_id: input.repositoryId },
            header: csrfHeaders(),
          },
          body: input.body,
        }),
      ),
    onSuccess: () => invalidateCatalogue(queryClient),
  });
}

export function useDeleteHelmRepository() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (repositoryId: string) =>
      unwrap(
        await api.DELETE(REPOSITORY_PATH, {
          params: {
            path: { repository_id: repositoryId },
            header: csrfHeaders(),
          },
        }),
      ),
    onSuccess: () => invalidateCatalogue(queryClient),
  });
}

/**
 * Everything a release write can carry.
 *
 * `dryRun` is what makes one request a preview: it renders the chart against the
 * Cluster and returns the manifest that would be applied, without writing
 * anything. Every form here submits the same body twice — once to preview, once
 * to apply — so what the operator approved is what gets sent.
 */
export type HelmReleaseWriteInput = {
  clusterId: string;
  namespace: string;
  name: string;
  repositoryId: string;
  chart: string;
  version?: string;
  values?: string;
  createNamespace?: boolean;
  wait?: boolean;
  atomic?: boolean;
  disableHooks?: boolean;
  timeoutSeconds?: number;
  maxHistory?: number;
  description?: string;
  resetValues?: boolean;
  reuseValues?: boolean;
  dryRun: boolean;
  idempotencyKey: string;
};

function releaseBody(input: HelmReleaseWriteInput) {
  // Every switch is sent explicitly rather than omitted. The Server reads a
  // missing boolean as false, but so would a bug that dropped one, and a
  // release that quietly installed without waiting is exactly the difference
  // nobody notices until the rollout is already half done.
  return {
    repository_id: input.repositoryId,
    chart: input.chart,
    version: input.version || undefined,
    values: input.values || undefined,
    create_namespace: input.createNamespace ?? false,
    wait: input.wait ?? false,
    atomic: input.atomic ?? false,
    disable_hooks: input.disableHooks ?? false,
    timeout_seconds: input.timeoutSeconds,
    max_history: input.maxHistory,
    description: input.description || undefined,
    dry_run: input.dryRun,
    confirm: !input.dryRun,
  };
}

export function useInstallHelmRelease() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: HelmReleaseWriteInput) =>
      unwrap(
        await api.POST(RELEASES_PATH, {
          params: {
            path: { cluster_id: input.clusterId, namespace_name: input.namespace },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: { name: input.name, ...releaseBody(input) },
        }),
      ),
    onSuccess: (_data, variables) => invalidateReleases(queryClient, variables),
  });
}

export function useUpgradeHelmRelease() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: HelmReleaseWriteInput) =>
      unwrap(
        await api.PUT(RELEASE_PATH, {
          params: {
            path: {
              cluster_id: input.clusterId,
              namespace_name: input.namespace,
              release_name: input.name,
            },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            ...releaseBody(input),
            reset_values: input.resetValues ?? false,
            reuse_values: input.reuseValues ?? false,
          },
        }),
      ),
    onSuccess: (_data, variables) => invalidateReleases(queryClient, variables),
  });
}

export type HelmRollbackInput = {
  clusterId: string;
  namespace: string;
  name: string;
  revision: number;
  wait?: boolean;
  disableHooks?: boolean;
  timeoutSeconds?: number;
  description?: string;
  dryRun: boolean;
  idempotencyKey: string;
};

export function useRollbackHelmRelease() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: HelmRollbackInput) =>
      unwrap(
        await api.POST(ROLLBACK_PATH, {
          params: {
            path: {
              cluster_id: input.clusterId,
              namespace_name: input.namespace,
              release_name: input.name,
            },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            revision: input.revision,
            wait: input.wait ?? false,
            disable_hooks: input.disableHooks ?? false,
            timeout_seconds: input.timeoutSeconds,
            description: input.description || undefined,
            dry_run: input.dryRun,
            confirm: !input.dryRun,
          },
        }),
      ),
    onSuccess: (_data, variables) => invalidateReleases(queryClient, variables),
  });
}

export type HelmUninstallInput = {
  clusterId: string;
  namespace: string;
  name: string;
  keepHistory: boolean;
  wait?: boolean;
  disableHooks?: boolean;
  timeoutSeconds?: number;
  description?: string;
  dryRun: boolean;
  idempotencyKey: string;
};

export function useUninstallHelmRelease() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: HelmUninstallInput) =>
      unwrap(
        await api.DELETE(RELEASE_PATH, {
          params: {
            path: {
              cluster_id: input.clusterId,
              namespace_name: input.namespace,
              release_name: input.name,
            },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            keep_history: input.keepHistory,
            wait: input.wait ?? false,
            disable_hooks: input.disableHooks ?? false,
            timeout_seconds: input.timeoutSeconds,
            description: input.description || undefined,
            dry_run: input.dryRun,
            confirm: !input.dryRun,
          },
        }),
      ),
    onSuccess: (_data, variables) => invalidateReleases(queryClient, variables),
  });
}

async function invalidateCatalogue(queryClient: ReturnType<typeof useQueryClient>) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.helmRepositories }),
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.helmRepository }),
    // A changed repository points somewhere else, or authenticates differently.
    // Its charts are a different catalogue from this moment on.
    queryClient.invalidateQueries({ queryKey: ["helm-charts"] }),
    queryClient.invalidateQueries({ queryKey: ["helm-chart"] }),
    queryClient.invalidateQueries({ queryKey: ["helm-chart-versions"] }),
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents }),
  ]);
}

/**
 * A dry run changed nothing, so it invalidates nothing: refetching the release
 * list after a preview would replace what the operator is reading with the same
 * content, and cost a Cluster round trip to do it.
 *
 * A real write invalidates the whole Namespace's releases rather than the one
 * that changed. One chart owns many objects, an uninstall removes a release
 * from the list entirely, and a rollback moves the revision the detail view is
 * pinned to.
 */
async function invalidateReleases(
  queryClient: ReturnType<typeof useQueryClient>,
  variables: { clusterId: string; namespace: string; dryRun: boolean },
) {
  await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
  if (variables.dryRun) {
    return;
  }
  await Promise.all([
    queryClient.invalidateQueries({
      queryKey: [...queryKeyPrefixes.helmReleases, variables.clusterId, variables.namespace],
    }),
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.helmRelease }),
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.helmReleaseRevisions }),
    // A chart creates and replaces ordinary Kubernetes objects, so the views
    // that list them are stale too.
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.workloads }),
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.pods }),
  ]);
}
