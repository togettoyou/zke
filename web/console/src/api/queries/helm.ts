import { useEffect, useRef } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type { HelmReleaseOperation, HelmRepositoryRequest } from "../types";
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
const CHART_FILE_PATH = "/api/v1/helm/repositories/{repository_id}/charts/{chart_name}/file";
const REFRESH_PATH = "/api/v1/helm/repositories/{repository_id}/index-refresh";
const RELEASES_PATH = "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/helm-releases";
const RELEASE_PATH =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/helm-releases/{release_name}";
const ROLLBACK_PATH =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/helm-releases/{release_name}/rollback";
const OPERATIONS_PATH = "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/helm-operations";
const OPERATION_PATH =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/helm-operations/{operation_id}";

/**
 * How often a running operation is asked what it is doing.
 *
 * A second is fast enough that Helm's log reads as it is written and slow
 * enough that a five-minute rollout costs three hundred requests rather than
 * three thousand. Polling stops the moment the operation finishes — a finished
 * account never changes again.
 *
 * This is the one polling query in the Console that does not stop while its
 * window is hidden, and the rule it is breaking is worth restating: polling
 * pauses because every request is ultimately executed by a Cluster's Agent.
 * This one is not. The account lives in the Server's own memory and reading it
 * costs a map lookup, while pausing would freeze a deployment log at whatever
 * it happened to say when the window went away — and a minimised window is
 * exactly what an operator does while they wait for a rollout.
 */
const OPERATION_POLL_MS = 1_000;

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
        queryClient.invalidateQueries({ queryKey: ["helm-chart-file"] }),
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

/**
 * One file out of a chart archive.
 *
 * The chart detail already lists what the archive holds; this reads one of
 * them. Separate requests because a chart with a packaged subchart carries
 * hundreds of files and a reader opens a handful — and because the Server holds
 * the parsed archive for a few minutes, so clicking through a tree costs one
 * download rather than one per file.
 *
 * A file already read stays read: the contents of a published chart version do
 * not change, so going back to a file is not worth a round trip.
 */
export function useHelmChartFile(
  repositoryId: string | null,
  chart: string | null,
  version: string,
  path: string | null,
) {
  return useQuery({
    queryKey: queryKeys.helmChartFile(repositoryId ?? "", chart ?? "", version, path ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(CHART_FILE_PATH, {
          params: {
            path: { repository_id: repositoryId as string, chart_name: chart as string },
            query: { path: path as string, ...(version ? { version } : {}) },
          },
          signal,
        }),
      ),
    enabled: Boolean(repositoryId && chart && path),
    staleTime: Infinity,
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
 * Cluster and reports the manifest that would be applied, without writing
 * anything. Every form here submits the same body twice — once to preview, once
 * to apply — so what the operator approved is what gets sent.
 *
 * None of the four returns a release. They return an operation: a release
 * change takes as long as the rollout it waits for, so the Server starts one
 * and answers with its identity, and what happened is read from
 * {@link useHelmOperation} while it happens.
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
  /**
   * One key per submission attempt, not one per form.
   *
   * The Server claims the key for the operation it starts, so presenting it
   * again is what makes a retried submission a retry rather than a second
   * install. Presenting it for a *different* request is refused — which is why
   * the preview and the apply, whose bodies differ by that one flag, must not
   * share one.
   */
  idempotencyKey: string;
};

/** What every release write answers with: the operation it started. */
type HelmOperationResult = { operation: HelmReleaseOperation };

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

/**
 * Starting a release change.
 *
 * Nothing is invalidated here, because nothing has happened yet: the request
 * that returns is the one that was accepted, and the objects it will create do
 * not exist for as long as the rollout takes. What is stale, and when, is
 * decided by {@link useHelmOperation} when the operation finishes.
 */
export function useInstallHelmRelease() {
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
      ) as HelmOperationResult,
  });
}

export function useUpgradeHelmRelease() {
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
      ) as HelmOperationResult,
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
      ) as HelmOperationResult,
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
      ) as HelmOperationResult,
  });
}

/**
 * One release change, watched until it stops.
 *
 * This is where a deployment becomes something an operator can see. The Server
 * writes the account as the change happens — which chart it resolved, what the
 * Cluster is creating, which objects it is waiting for — and this reads it
 * every second until the operation finishes, then stops.
 *
 * It is also where the rest of the Console finds out that something changed.
 * The mutation that started the operation could not say so: at the moment it
 * returned, nothing had happened yet.
 */
export function useHelmOperation(
  clusterId: string | null,
  namespace: string | null,
  operationId: string | null,
) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: queryKeys.helmOperation(clusterId ?? "", namespace ?? "", operationId ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(OPERATION_PATH, {
          params: {
            path: {
              cluster_id: clusterId as string,
              namespace_name: namespace as string,
              operation_id: operationId as string,
            },
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId && namespace && operationId),
    refetchInterval: (query) =>
      query.state.data?.operation.status === "running" ? OPERATION_POLL_MS : false,
    // A finished operation is a historical record: it cannot change again, so
    // remounting the view that shows it must not cost a round trip.
    staleTime: Infinity,
  });

  // Settled once per operation. The query keeps handing back the same finished
  // account on every render, and invalidating the whole Namespace each time
  // would put the Console into a refetch loop of its own making.
  const settled = useRef<string | null>(null);
  const operation = query.data?.operation;
  useEffect(() => {
    if (!operation || operation.status === "running" || settled.current === operation.id) {
      return;
    }
    settled.current = operation.id;
    void queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
    // A dry run changed nothing, and a failed operation may have changed
    // something — a chart that failed halfway leaves behind what it had already
    // applied, so what is on screen is stale either way.
    if (operation.dry_run) {
      return;
    }
    void invalidateReleases(queryClient, {
      clusterId: operation.cluster_id,
      namespace: operation.namespace,
    });
  }, [operation, queryClient]);

  return query;
}

/**
 * This operator's release changes in one Namespace, newest first.
 *
 * It answers one question: is something already running here? A Console that
 * was closed or reloaded mid-deployment has lost the operation's identity, and
 * without this there would be no way back to it — the deployment would carry on
 * invisibly and the page would show a release list that quietly changed under
 * it.
 */
export function useHelmOperations(clusterId: string | null, namespace: string | null) {
  return useQuery({
    queryKey: queryKeys.helmOperations(clusterId ?? "", namespace ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(OPERATIONS_PATH, {
          params: {
            path: { cluster_id: clusterId as string, namespace_name: namespace as string },
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId && namespace),
    refetchInterval: (query) =>
      (query.state.data?.operations ?? []).some((item) => item.status === "running")
        ? OPERATION_POLL_MS
        : false,
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
    queryClient.invalidateQueries({ queryKey: ["helm-chart-file"] }),
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents }),
  ]);
}

/**
 * What a finished release change made stale.
 *
 * The whole Namespace's releases rather than the one that changed: one chart
 * owns many objects, an uninstall removes a release from the list entirely, and
 * a rollback moves the revision the detail view is pinned to.
 */
async function invalidateReleases(
  queryClient: ReturnType<typeof useQueryClient>,
  variables: { clusterId: string; namespace: string },
) {
  await Promise.all([
    queryClient.invalidateQueries({
      queryKey: [...queryKeyPrefixes.helmReleases, variables.clusterId, variables.namespace],
    }),
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.helmRelease }),
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.helmReleaseRevisions }),
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.helmOperations }),
    // A chart creates and replaces ordinary Kubernetes objects, so the views
    // that list them are stale too.
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.workloads }),
    queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.pods }),
  ]);
}
