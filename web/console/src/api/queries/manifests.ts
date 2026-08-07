import { useMutation, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrap } from "../client";
import { queryKeyPrefixes } from "../query-keys";

const APPLY_PATH = "/api/v1/clusters/{cluster_id}/kubernetes/manifests/apply";
const DELETE_PATH = "/api/v1/clusters/{cluster_id}/kubernetes/manifests/delete";

/** The Server refuses anything larger, so the view says so before the round trip. */
export const MAX_MANIFEST_BYTES = 4 * 1024 * 1024;

export type ManifestOperation = "apply" | "delete";

/**
 * What one document turned out to mean for the object it names, decided by
 * reading the Cluster rather than guessed from the document.
 */
export type ManifestAction = "create" | "update" | "delete" | "absent" | "unknown";

/**
 * How far one document got.
 *
 * `planned` only appears in a dry run. `not_attempted` is the one worth reading
 * carefully: Kubernetes has no transaction, so a manifest that failed halfway
 * left the documents before it written, and this is how the rest are named.
 */
export type ManifestStatus =
  "planned" | "refused" | "invalid" | "succeeded" | "skipped" | "failed" | "not_attempted";

export type ManifestDocument = {
  index: number;
  api_version: string;
  kind: string;
  namespace: string;
  name: string;
  action: ManifestAction;
  status: ManifestStatus;
  /**
   * Whether Kubernetes itself saw this document.
   *
   * False on a dry-run document that could not be submitted: a manifest that
   * creates a Namespace and then fills it cannot have the contents validated,
   * because the dry-run Namespace never exists. Those documents are still applied
   * for real — but their "预检通过" means "nothing could be checked", not "the API
   * Server said yes".
   */
  previewed: boolean;
  /** The ZKE permission this document answers to; empty when it did not resolve. */
  permission: string;
  uid: string;
  resource_version: string;
  error_code: string;
  error_message: string;
};

export type ManifestResult = {
  dry_run: boolean;
  allowed: boolean;
  failed: boolean;
  catalog_partial: boolean;
  documents: ManifestDocument[];
};

type ManifestInput = {
  clusterId: string;
  manifest: string;
  /** Fills in documents that name no Namespace, the way `kubectl -n` does. */
  namespace?: string;
  operation: ManifestOperation;
  dryRun: boolean;
  /** Server-side Apply conflict override. Meaningless for a delete. */
  force?: boolean;
  idempotencyKey: string;
};

function manifestQuery(input: ManifestInput) {
  return {
    ...(input.namespace ? { namespace: input.namespace } : {}),
    dry_run: input.dryRun,
    // A dry run needs no confirmation — nothing is written. The Server refuses a
    // write that carries neither.
    ...(input.dryRun ? {} : { confirm: true as const }),
    ...(input.operation === "apply" && input.force ? { force: true as const } : {}),
  };
}

async function submitManifest(input: ManifestInput): Promise<ManifestResult> {
  // openapi-fetch serialises bodies as JSON by default; these endpoints take the
  // document as-is.
  const body = {
    body: input.manifest,
    bodySerializer: (value: string) => value,
    headers: { "Content-Type": "application/yaml" },
  };
  const params = {
    path: { cluster_id: input.clusterId },
    query: manifestQuery(input),
    header: idempotentHeaders(input.idempotencyKey),
  };
  const result =
    input.operation === "apply"
      ? await api.POST(APPLY_PATH, { params, ...body })
      : await api.POST(DELETE_PATH, { params, ...body });
  return unwrap(result) as ManifestResult;
}

/**
 * Plans or executes a whole manifest.
 *
 * The two are the same request with `dry_run` flipped, which is deliberate: the
 * plan an operator confirms has to be produced by the code path that then runs
 * it, or the confirmation is about something other than what happens.
 *
 * A dry run always resolves — including when a document is refused, which is how
 * the view can show which permission each document needs. An execution whose
 * documents are not all covered fails with 403 and writes nothing.
 */
export function useSubmitManifest() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: submitManifest,
    onSuccess: async (_result, variables) => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
      if (variables.dryRun) {
        return;
      }
      // A manifest may hold objects of any kind, so every Kubernetes view of this
      // Cluster is stale — including after a partial failure, where some of the
      // documents did land.
      await Promise.all(
        [
          queryKeyPrefixes.nodes,
          queryKeyPrefixes.node,
          queryKeyPrefixes.namespaces,
          queryKeyPrefixes.namespace,
          queryKeyPrefixes.workloads,
          queryKeyPrefixes.workload,
          queryKeyPrefixes.configMaps,
          queryKeyPrefixes.configMap,
          queryKeyPrefixes.secrets,
          queryKeyPrefixes.secret,
          queryKeyPrefixes.authorizationResources,
          queryKeyPrefixes.authorizationResource,
          queryKeyPrefixes.networkingResources,
          queryKeyPrefixes.networkingResource,
          queryKeyPrefixes.storageResources,
          queryKeyPrefixes.storageResource,
          queryKeyPrefixes.policyResources,
          queryKeyPrefixes.policyResource,
          queryKeyPrefixes.genericResources,
          queryKeyPrefixes.pods,
          queryKeyPrefixes.pod,
        ].map((prefix) => queryClient.invalidateQueries({ queryKey: prefix })),
      );
    },
  });
}
