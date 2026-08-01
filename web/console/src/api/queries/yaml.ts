import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, idempotentHeaders, unwrapRaw } from "../client";
import { queryKeys, queryKeyPrefixes } from "../query-keys";

/**
 * Which object a YAML request is about.
 *
 * The Group/Version/Resource triple is what the Server routes on, and it is
 * spelled out by the caller rather than guessed: every section that offers YAML
 * already knows exactly what kind of object it is showing.
 */
export type ResourceIdentity = {
  clusterId: string;
  /** Empty for the core API group. */
  group?: string;
  version: string;
  resource: string;
  /** Empty for cluster-scoped resources. */
  namespace?: string;
  name: string;
};

export type ResourceYaml = {
  yaml: string;
  /** The object's identity at the moment it was read, for the update precondition. */
  uid: string;
  resourceVersion: string;
  dryRun: boolean;
};

const YAML_PATH = "/api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}/yaml";

function yamlParams(identity: ResourceIdentity) {
  return {
    path: { cluster_id: identity.clusterId, resource_name: identity.name },
    query: {
      version: identity.version,
      resource: identity.resource,
      ...(identity.group ? { group: identity.group } : {}),
      ...(identity.namespace ? { namespace: identity.namespace } : {}),
    },
  };
}

/**
 * The Server reports the object's identity in headers rather than in the body,
 * so that the YAML stays exactly what Kubernetes returned.
 */
function readIdentity(response: Response, yaml: string): ResourceYaml {
  return {
    yaml,
    uid: response.headers.get("X-ZKE-Resource-UID") ?? "",
    resourceVersion: response.headers.get("X-ZKE-Resource-Version") ?? "",
    dryRun: response.headers.get("X-ZKE-Dry-Run") === "true",
  };
}

/** Reads one object's complete YAML. */
export function useResourceYaml(identity: ResourceIdentity | null) {
  return useQuery({
    queryKey: queryKeys.resourceYaml(
      identity?.clusterId ?? "",
      identity?.namespace ?? "",
      `${identity?.group ?? ""}/${identity?.version ?? ""}/${identity?.resource ?? ""}`,
      identity?.name ?? "",
    ),
    queryFn: async ({ signal }) => {
      const result = await api.GET(YAML_PATH, {
        params: yamlParams(identity as ResourceIdentity),
        parseAs: "text",
        signal,
      });
      return readIdentity(result.response, unwrapRaw(result));
    },
    enabled: Boolean(identity),
    // A resourceVersion read long ago is a conflict waiting to happen, so the
    // editor always opens on a fresh read rather than on a cached one.
    staleTime: 0,
    gcTime: 0,
  });
}

/**
 * Replaces one object's YAML.
 *
 * The document itself carries the preconditions: the Server checks the
 * `apiVersion`, `kind`, name, UID and `resourceVersion` inside it against the
 * object it is about to overwrite, so an edit of a stale read is refused rather
 * than silently reverting whatever changed in between. Nothing here strips those
 * fields — removing them would be removing the safety.
 */
export function useUpdateResourceYaml() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: {
      identity: ResourceIdentity;
      yaml: string;
      dryRun: boolean;
      idempotencyKey: string;
    }) => {
      const params = yamlParams(input.identity);
      const result = await api.PUT(YAML_PATH, {
        params: {
          path: params.path,
          query: {
            ...params.query,
            dry_run: input.dryRun,
            ...(input.dryRun ? {} : { confirm: true }),
          },
          header: idempotentHeaders(input.idempotencyKey),
        },
        body: input.yaml,
        // openapi-fetch serialises bodies as JSON by default; this endpoint takes
        // the document as-is.
        bodySerializer: (body: string) => body,
        headers: { "Content-Type": "application/yaml" },
        parseAs: "text",
      });
      return readIdentity(result.response, unwrapRaw(result));
    },
    onSuccess: async (_data, variables) => {
      await queryClient.invalidateQueries({ queryKey: queryKeyPrefixes.auditEvents });
      if (variables.dryRun) {
        return;
      }
      // The object may be of any kind, and an arbitrary edit may have changed
      // anything about it, so every Kubernetes view of this Cluster is stale.
      await Promise.all(
        [
          queryKeyPrefixes.nodes,
          queryKeyPrefixes.node,
          queryKeyPrefixes.namespaces,
          queryKeyPrefixes.namespace,
          queryKeyPrefixes.workloads,
          queryKeyPrefixes.workload,
          queryKeyPrefixes.pods,
          queryKeyPrefixes.pod,
        ].map((prefix) => queryClient.invalidateQueries({ queryKey: prefix })),
      );
    },
  });
}
