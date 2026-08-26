import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import type { KubernetesNodeDrainRequest } from "../types";
import { queryKeys, queryKeyPrefixes } from "../query-keys";

export type NodeListParams = {
  limit?: number;
  continue?: string;
  label_selector?: string;
  field_selector?: string;
};

export function useNodes(clusterId: string | null, params: NodeListParams = {}) {
  return useQuery({
    queryKey: queryKeys.nodes(clusterId ?? "", params),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster_id}/nodes", {
          params: {
            path: { cluster_id: clusterId as string },
            query: params,
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId),
    // A continuation page may keep the previous page in place while loading;
    // a different Cluster may not inherit it.
    placeholderData: (previous, previousQuery) =>
      previousQuery?.queryKey[1] === clusterId ? previous : undefined,
  });
}

export function useNode(clusterId: string | null, name: string | null) {
  return useQuery({
    queryKey: queryKeys.node(clusterId ?? "", name ?? ""),
    queryFn: async ({ signal }) =>
      unwrap(
        await api.GET("/api/v1/clusters/{cluster_id}/nodes/{node_name}", {
          params: {
            path: { cluster_id: clusterId as string, node_name: name as string },
          },
          signal,
        }),
      ),
    enabled: Boolean(clusterId && name),
  });
}

/**
 * Marks a Node schedulable or unschedulable — cordon and uncordon.
 *
 * There is no typed endpoint for this and none is needed: it is a merge patch of
 * `spec.unschedulable` on a primary resource, which the controlled generic CRUD
 * route already covers. The patch names only that one field, so it cannot carry
 * an unrelated change to the Node along with it.
 *
 * Draining is a separate, more sensitive operation below: it has its own
 * permission and the Agent accepts only the exact pods/eviction request shape.
 */
export function useSetNodeSchedulable() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      name: string;
      unschedulable: boolean;
      dryRun: boolean;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.PATCH("/api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}", {
          params: {
            path: { cluster_id: input.clusterId, resource_name: input.name },
            query: { version: "v1", resource: "nodes" },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            patch_type: "merge",
            patch: { spec: { unschedulable: input.unschedulable } },
            options: { dry_run: input.dryRun, force: false },
            confirm: !input.dryRun,
          },
        }),
      ),
    onSuccess: async (_data, variables) => {
      await queryClient.invalidateQueries({
        queryKey: queryKeyPrefixes.auditEvents,
      });
      if (!variables.dryRun) {
        await Promise.all([
          queryClient.invalidateQueries({
            queryKey: ["nodes", variables.clusterId],
          }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.node(variables.clusterId, variables.name),
          }),
        ]);
      }
    },
  });
}

/**
 * Adds, changes and removes labels on one Node.
 *
 * Like cordon above, a merge patch through the controlled generic CRUD route
 * rather than an endpoint of its own: the patch names only `metadata.labels`,
 * so it cannot carry an unrelated change to the Node along with it.
 *
 * The body holds only the keys this edit actually touched — a removed one as
 * `null`, an untouched one not at all — which is the request `kubectl label`
 * makes. Sending the whole map instead would revert any label a controller or
 * another operator set between the read this form opened on and the write.
 */
export function useUpdateNodeLabels() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      name: string;
      /** Only the touched keys; `null` removes one. */
      labels: Record<string, string | null>;
      dryRun: boolean;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.PATCH("/api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}", {
          params: {
            path: { cluster_id: input.clusterId, resource_name: input.name },
            query: { version: "v1", resource: "nodes" },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            patch_type: "merge",
            patch: { metadata: { labels: input.labels } },
            options: { dry_run: input.dryRun, force: false },
            confirm: !input.dryRun,
          },
        }),
      ),
    onSuccess: async (_data, variables) => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
      if (!variables.dryRun) {
        await Promise.all([
          // The list is invalidated as well as the detail: a Node's roles are
          // read out of its `node-role.kubernetes.io/*` and `kubernetes.io/role`
          // labels, so a label edit can change what the list shows.
          queryClient.invalidateQueries({ queryKey: ["nodes", variables.clusterId] }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.node(variables.clusterId, variables.name),
          }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.nodeDescribe(variables.clusterId, variables.name),
          }),
        ]);
      }
    },
  });
}

/** One taint exactly as Kubernetes stores it on `spec.taints`. */
export type NodeTaint = {
  key: string;
  value?: string;
  effect: string;
  timeAdded?: string;
};

/**
 * Replaces the taints on one Node.
 *
 * A JSON Patch rather than the merge patch labels use, because taints are a
 * list: a merge patch replaces a list wholesale with no way to say what it was
 * replacing, so the whole list would be written over whatever is there now.
 *
 * The concurrency guard is a `test` on the list itself rather than on
 * `metadata.resourceVersion`. A Node's resourceVersion changes with every
 * kubelet status heartbeat — every few seconds, on every Node — so a
 * resourceVersion precondition here would fail almost every time for reasons
 * that have nothing to do with taints. Testing `spec.taints` fails exactly when
 * someone else changed the taints, which is the collision worth refusing.
 *
 * A Node with no taints at all has no `/spec/taints` to test, and JSON Patch has
 * no way to assert a path is absent; that one case is written without the guard.
 */
export function useUpdateNodeTaints() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      name: string;
      uid: string;
      /** The taints as they were read, verbatim; absent when the Node had none. */
      baseline: NodeTaint[] | undefined;
      /** The complete list to store; empty removes every taint. */
      taints: NodeTaint[];
      dryRun: boolean;
      idempotencyKey: string;
    }) => {
      const operations: Record<string, unknown>[] = [
        { op: "test", path: "/metadata/uid", value: input.uid },
      ];
      if (input.baseline !== undefined) {
        operations.push({ op: "test", path: "/spec/taints", value: input.baseline });
      }
      operations.push(
        input.taints.length > 0
          ? { op: "add", path: "/spec/taints", value: input.taints }
          : { op: "remove", path: "/spec/taints" },
      );
      return unwrap(
        await api.PATCH("/api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}", {
          params: {
            path: { cluster_id: input.clusterId, resource_name: input.name },
            query: { version: "v1", resource: "nodes" },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: {
            patch_type: "json",
            patch: operations,
            options: { dry_run: input.dryRun, force: false },
            confirm: !input.dryRun,
          },
        }),
      );
    },
    onSuccess: async (_data, variables) => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
      if (!variables.dryRun) {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ["nodes", variables.clusterId] }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.node(variables.clusterId, variables.name),
          }),
          queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.genericResource }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.nodeDescribe(variables.clusterId, variables.name),
          }),
        ]);
      }
    },
  });
}

export function useDrainNode() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      clusterId: string;
      name: string;
      request: KubernetesNodeDrainRequest;
      idempotencyKey: string;
    }) =>
      unwrap(
        await api.POST("/api/v1/clusters/{cluster_id}/nodes/{node_name}/drain", {
          params: {
            path: { cluster_id: input.clusterId, node_name: input.name },
            header: idempotentHeaders(input.idempotencyKey),
          },
          body: input.request,
        }),
      ),
    onSuccess: async (_data, variables) => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
      if (!variables.request.dry_run) {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ["nodes", variables.clusterId] }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.node(variables.clusterId, variables.name),
          }),
          queryClient.invalidateQueries({
            queryKey: queryKeys.nodeDescribe(variables.clusterId, variables.name),
          }),
        ]);
      }
    },
  });
}
