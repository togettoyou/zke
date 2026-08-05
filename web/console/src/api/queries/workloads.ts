import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";
import type {
  KubernetesCreateWorkloadRequest,
  KubernetesUpdateWorkloadRequest,
  KubernetesWorkloadResource,
} from "../types";

export type WorkloadListParams = {
  limit?: number;
  continue?: string;
  label_selector?: string;
  field_selector?: string;
};

/**
 * Reads one kind of workload out of one Namespace of one Cluster.
 *
 * Cluster, Namespace and workload type are all path segments rather than
 * filters, so a query can never widen itself: there is no request this hook can
 * make that spans Namespaces or mixes Deployments with Jobs.
 */
export function useWorkloads(
  clusterId: string | null,
  namespace: string | null,
  resource: KubernetesWorkloadResource,
  params: WorkloadListParams = {},
) {
  return useQuery({
    queryKey: queryKeys.workloads(clusterId ?? "", namespace ?? "", resource, params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}",
          {
            params: {
              path: {
                cluster_id: clusterId as string,
                namespace_name: namespace as string,
                workload_resource: resource,
              },
              query: params,
            },
            signal,
          },
        ),
      ),
    enabled: Boolean(clusterId && namespace),
    // Preserve a page only while paging the same scoped collection. Reusing a
    // Deployment page after changing Cluster, Namespace or workload type would
    // present old objects under a new scope until the replacement request ends.
    placeholderData: (previous, previousQuery) => {
      const previousKey = previousQuery?.queryKey;
      return previousKey?.[1] === clusterId &&
        previousKey[2] === namespace &&
        previousKey[3] === resource
        ? previous
        : undefined;
    },
  });
}

export function useWorkload(
  clusterId: string | null,
  namespace: string | null,
  resource: KubernetesWorkloadResource,
  name: string | null,
) {
  return useQuery({
    queryKey: queryKeys.workload(clusterId ?? "", namespace ?? "", resource, name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}",
          {
            params: {
              path: {
                cluster_id: clusterId as string,
                namespace_name: namespace as string,
                workload_resource: resource,
                workload_name: name as string,
              },
            },
            signal,
          },
        ),
      ),
    enabled: Boolean(clusterId && namespace && name),
  });
}

/** What every workload mutation needs to name its target and its submission. */
type WorkloadMutationInput = {
  clusterId: string;
  namespace: string;
  resource: KubernetesWorkloadResource;
  name: string;
  dryRun: boolean;
  idempotencyKey: string;
};

/**
 * Invalidates the list the workload belongs to and its own detail.
 *
 * A dry run changed nothing in the Cluster, so it invalidates nothing but the
 * audit trail — the Server records the attempt either way.
 */
function useWorkloadInvalidation() {
  const queryClient = useQueryClient();
  return async (input: WorkloadMutationInput, invalidateDetail = true) => {
    await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
    if (input.dryRun) {
      return;
    }
    const invalidations = [
      queryClient.invalidateQueries({
        queryKey: ["workloads", input.clusterId, input.namespace, input.resource],
      }),
    ];
    if (invalidateDetail) {
      invalidations.push(
        queryClient.invalidateQueries({
          queryKey: queryKeys.workload(
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

/** The body of a create request minus the two flags this hook owns. */
export type WorkloadCreateSpec = Omit<KubernetesCreateWorkloadRequest, "dry_run" | "confirm">;

/**
 * Creates one workload from the typed template.
 *
 * The caller sends only the fields its type accepts — the Server rejects a
 * `replicas` on a DaemonSet or a `schedule` on a Deployment rather than ignoring
 * it, so an unused field has to be absent, not empty. The Pod selector is not in
 * the body at all: the Server derives it for long-running controllers and lets
 * Kubernetes derive it from the controller UID for Jobs. The reserved
 * `zke.io/workload-id` label cannot be supplied by the client.
 */
export function useCreateWorkload() {
  const invalidate = useWorkloadInvalidation();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      namespace: string;
      resource: KubernetesWorkloadResource;
      spec: WorkloadCreateSpec;
      dryRun: boolean;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.POST(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}",
          {
            params: {
              path: {
                cluster_id: input.clusterId,
                namespace_name: input.namespace,
                workload_resource: input.resource,
              },
              header: idempotentHeaders(input.idempotencyKey),
            },
            body: { ...input.spec, dry_run: input.dryRun, confirm: !input.dryRun },
          },
        ),
      ),
    onSuccess: (_data, variables) =>
      invalidate(
        {
          clusterId: variables.clusterId,
          namespace: variables.namespace,
          resource: variables.resource,
          name: variables.spec.name,
          dryRun: variables.dryRun,
          idempotencyKey: variables.idempotencyKey,
        },
        // Nothing has ever read the new object's detail, so there is no cached
        // entry to invalidate — only the list it now belongs to.
        false,
      ),
  });
}

/** The body of an update minus the flags and preconditions this hook owns. */
export type WorkloadUpdateSpec = Omit<
  KubernetesUpdateWorkloadRequest,
  "dry_run" | "confirm" | "uid" | "resource_version"
>;

/**
 * Replaces the modeled part of one workload.
 *
 * The same fields a create sends, because the edit form is the create form on an
 * existing object. What it does not send is not cleared: affinity, topology
 * spread, container ports and the rest of the security context stay on the
 * object, and the Server merges onto what it reads back under these
 * preconditions rather than replacing the spec.
 *
 * `uid` and `resource_version` are the version the form was opened on. They are
 * fixed when the editor mounts and deliberately not refreshed while it is open:
 * taking a newer version would turn a conflict the Server should refuse into a
 * silent overwrite of whatever landed in between.
 */
export function useUpdateWorkload() {
  const invalidate = useWorkloadInvalidation();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      namespace: string;
      resource: KubernetesWorkloadResource;
      name: string;
      uid: string;
      resourceVersion: string;
      spec: WorkloadUpdateSpec;
      dryRun: boolean;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.PUT(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}",
          {
            params: {
              path: {
                cluster_id: input.clusterId,
                namespace_name: input.namespace,
                workload_resource: input.resource,
                workload_name: input.name,
              },
              header: idempotentHeaders(input.idempotencyKey),
            },
            body: {
              ...input.spec,
              uid: input.uid,
              resource_version: input.resourceVersion,
              dry_run: input.dryRun,
              confirm: !input.dryRun,
            },
          },
        ),
      ),
    onSuccess: (_data, variables) => invalidate(variables),
  });
}

/** Sets `spec.replicas` on a Deployment or StatefulSet. */
export function useScaleWorkload() {
  const invalidate = useWorkloadInvalidation();
  return useMutation({
    mutationFn: async (input: WorkloadMutationInput & { replicas: number }) =>
      unwrap(
        await api.POST(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}/scale",
          {
            params: {
              path: workloadPath(input),
              header: idempotentHeaders(input.idempotencyKey),
            },
            body: {
              replicas: input.replicas,
              dry_run: input.dryRun,
              confirm: !input.dryRun,
            },
          },
        ),
      ),
    onSuccess: (_data, variables) => invalidate(variables),
  });
}

/**
 * Triggers a rolling restart of a Deployment, StatefulSet or DaemonSet.
 *
 * The Server derives the restart marker from the idempotency key, so a retry of
 * one submission writes the byte-identical annotation and the controller sees no
 * new revision. That property only holds while the key is stable across the
 * retry, which is what {@link useSubmissionKey} guarantees at the call site.
 */
export function useRestartWorkload() {
  const invalidate = useWorkloadInvalidation();
  return useMutation({
    mutationFn: async (input: WorkloadMutationInput) =>
      unwrap(
        await api.POST(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}/restart",
          {
            params: {
              path: workloadPath(input),
              header: idempotentHeaders(input.idempotencyKey),
            },
            body: { dry_run: input.dryRun, confirm: !input.dryRun },
          },
        ),
      ),
    onSuccess: (_data, variables) => invalidate(variables),
  });
}

/** Suspends or resumes a CronJob — `spec.suspend`, through two routes. */
export function useSetCronJobSuspension() {
  const invalidate = useWorkloadInvalidation();
  return useMutation({
    mutationFn: async (input: WorkloadMutationInput & { suspended: boolean }) => {
      const options = {
        params: {
          path: workloadPath(input),
          header: idempotentHeaders(input.idempotencyKey),
        },
        body: { dry_run: input.dryRun, confirm: !input.dryRun },
      };
      return unwrap(
        input.suspended
          ? await api.POST(
              "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}/suspend",
              options,
            )
          : await api.POST(
              "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}/resume",
              options,
            ),
      );
    },
    onSuccess: (_data, variables) => invalidate(variables),
  });
}

/**
 * Deletes a workload, pinned to the UID the caller read it at.
 *
 * The endpoint requires the UID rather than accepting it, so a same-named object
 * recreated between reading the list and confirming the deletion cannot be the
 * one that gets deleted. `resource_version` is deliberately not sent: any
 * unrelated status update between the read and the confirmation would turn an
 * intended deletion into a conflict.
 */
export function useDeleteWorkload() {
  const invalidate = useWorkloadInvalidation();
  return useMutation({
    mutationFn: async (input: WorkloadMutationInput & { uid: string }) =>
      unwrap(
        await api.DELETE(
          "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/workloads/{workload_resource}/{workload_name}",
          {
            params: {
              path: workloadPath(input),
              header: idempotentHeaders(input.idempotencyKey),
            },
            body: { dry_run: input.dryRun, confirm: !input.dryRun, uid: input.uid },
          },
        ),
      ),
    onSuccess: async (_data, variables) => {
      // A successfully deleted object has no detail to refetch. Invalidating
      // that active query would issue a guaranteed 404 before the detail view
      // can navigate back to the list.
      await invalidate(variables, false);
    },
  });
}

function workloadPath(input: WorkloadMutationInput) {
  return {
    cluster_id: input.clusterId,
    namespace_name: input.namespace,
    workload_resource: input.resource,
    workload_name: input.name,
  };
}
