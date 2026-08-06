import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import type { KubernetesAuthorizationResource } from "../types";
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
  kind?: "resource";
  clusterId: string;
  /** Empty for the core API group. */
  group?: string;
  version: string;
  resource: string;
  /** Empty for cluster-scoped resources. */
  namespace?: string;
  name: string;
};

/**
 * A Secret, which the generic endpoint refuses.
 *
 * It has an endpoint of its own because it has permissions of its own —
 * `cluster.secret.read` and `cluster.secret.manage` — and routing it through
 * the generic one would hand a Secret to everyone holding the resource
 * permissions instead.
 */
export type SecretYamlTarget = {
  kind: "secret";
  clusterId: string;
  namespace: string;
  name: string;
};

/**
 * One of the five authorization families, refused by the generic endpoint for
 * the same reason: they answer to `cluster.rbac.read` and `cluster.rbac.manage`.
 *
 * Split by scope because the two endpoints are: naming a ClusterRole inside a
 * Namespace describes no object, and a type that allowed it would leave that to
 * be discovered from a rejection.
 */
export type AuthorizationYamlTarget =
  | {
      kind: "authorization";
      clusterId: string;
      namespace: string;
      resource: Extract<
        KubernetesAuthorizationResource,
        "serviceaccounts" | "roles" | "rolebindings"
      >;
      name: string;
    }
  | {
      kind: "authorization";
      clusterId: string;
      namespace?: undefined;
      resource: Extract<KubernetesAuthorizationResource, "clusterroles" | "clusterrolebindings">;
      name: string;
    };

export type YamlTarget = ResourceIdentity | SecretYamlTarget | AuthorizationYamlTarget;

export type ResourceYaml = {
  yaml: string;
  /** The object's identity at the moment it was read, for the update precondition. */
  uid: string;
  resourceVersion: string;
  dryRun: boolean;
};

const RESOURCE_PATH = "/api/v1/clusters/{cluster_id}/kubernetes/resources/{resource_name}/yaml";
const SECRET_PATH =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/secrets/{secret_name}/yaml";
const NAMESPACED_AUTHORIZATION_PATH =
  "/api/v1/clusters/{cluster_id}/namespaces/{namespace_name}/authorization/{authorization_resource}/{authorization_name}/yaml";
const CLUSTER_AUTHORIZATION_PATH =
  "/api/v1/clusters/{cluster_id}/authorization/{authorization_resource}/{authorization_name}/yaml";

/** Query parameters a YAML write takes; the object itself is named by the URL. */
type WriteOptions = { dryRun: boolean; idempotencyKey: string };

function writeQuery(options: WriteOptions) {
  return {
    dry_run: options.dryRun,
    ...(options.dryRun ? {} : { confirm: true as const }),
  };
}

function resourceQuery(target: ResourceIdentity) {
  return {
    version: target.version,
    resource: target.resource,
    ...(target.group ? { group: target.group } : {}),
    ...(target.namespace ? { namespace: target.namespace } : {}),
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

/**
 * How a target is addressed in the cache.
 *
 * The three endpoints can name the same Namespace and the same object name, so
 * the family is part of the key: a Role and a Secret called `api` in one
 * Namespace are different documents.
 */
function targetKey(target: YamlTarget): string {
  switch (target.kind) {
    case "secret":
      return "secret//v1/secrets";
    case "authorization":
      return `authorization/${target.resource}`;
    default:
      return `resource/${target.group ?? ""}/${target.version}/${target.resource}`;
  }
}

async function readYaml(target: YamlTarget, signal: AbortSignal): Promise<ResourceYaml> {
  const cluster_id = target.clusterId;
  if (target.kind === "secret") {
    const result = await api.GET(SECRET_PATH, {
      params: {
        path: { cluster_id, namespace_name: target.namespace, secret_name: target.name },
      },
      parseAs: "text",
      signal,
    });
    return readIdentity(result.response, unwrapRaw(result));
  }
  if (target.kind === "authorization") {
    if (target.namespace !== undefined) {
      const result = await api.GET(NAMESPACED_AUTHORIZATION_PATH, {
        params: {
          path: {
            cluster_id,
            namespace_name: target.namespace,
            authorization_resource: target.resource,
            authorization_name: target.name,
          },
        },
        parseAs: "text",
        signal,
      });
      return readIdentity(result.response, unwrapRaw(result));
    }
    const result = await api.GET(CLUSTER_AUTHORIZATION_PATH, {
      params: {
        path: {
          cluster_id,
          authorization_resource: target.resource,
          authorization_name: target.name,
        },
      },
      parseAs: "text",
      signal,
    });
    return readIdentity(result.response, unwrapRaw(result));
  }
  const result = await api.GET(RESOURCE_PATH, {
    params: {
      path: { cluster_id, resource_name: target.name },
      query: resourceQuery(target),
    },
    parseAs: "text",
    signal,
  });
  return readIdentity(result.response, unwrapRaw(result));
}

async function writeYaml(
  target: YamlTarget,
  yaml: string,
  options: WriteOptions,
): Promise<ResourceYaml> {
  const cluster_id = target.clusterId;
  // openapi-fetch serialises bodies as JSON by default; these endpoints take the
  // document as-is.
  const body = {
    body: yaml,
    bodySerializer: (value: string) => value,
    headers: { "Content-Type": "application/yaml" },
    parseAs: "text" as const,
  };
  const header = idempotentHeaders(options.idempotencyKey);
  if (target.kind === "secret") {
    const result = await api.PUT(SECRET_PATH, {
      params: {
        path: { cluster_id, namespace_name: target.namespace, secret_name: target.name },
        query: writeQuery(options),
        header,
      },
      ...body,
    });
    return readIdentity(result.response, unwrapRaw(result));
  }
  if (target.kind === "authorization") {
    if (target.namespace !== undefined) {
      const result = await api.PUT(NAMESPACED_AUTHORIZATION_PATH, {
        params: {
          path: {
            cluster_id,
            namespace_name: target.namespace,
            authorization_resource: target.resource,
            authorization_name: target.name,
          },
          query: writeQuery(options),
          header,
        },
        ...body,
      });
      return readIdentity(result.response, unwrapRaw(result));
    }
    const result = await api.PUT(CLUSTER_AUTHORIZATION_PATH, {
      params: {
        path: {
          cluster_id,
          authorization_resource: target.resource,
          authorization_name: target.name,
        },
        query: writeQuery(options),
        header,
      },
      ...body,
    });
    return readIdentity(result.response, unwrapRaw(result));
  }
  const result = await api.PUT(RESOURCE_PATH, {
    params: {
      path: { cluster_id, resource_name: target.name },
      query: { ...resourceQuery(target), ...writeQuery(options) },
      header,
    },
    ...body,
  });
  return readIdentity(result.response, unwrapRaw(result));
}

/** Reads one object's complete YAML. */
export function useResourceYaml(target: YamlTarget | null) {
  return useQuery({
    queryKey: queryKeys.resourceYaml(
      target?.clusterId ?? "",
      target?.namespace ?? "",
      target ? targetKey(target) : "",
      target?.name ?? "",
    ),
    queryFn: ({ signal }) => readYaml(target as YamlTarget, signal),
    enabled: Boolean(target),
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
    mutationFn: (input: {
      target: YamlTarget;
      yaml: string;
      dryRun: boolean;
      idempotencyKey: string;
    }) =>
      writeYaml(input.target, input.yaml, {
        dryRun: input.dryRun,
        idempotencyKey: input.idempotencyKey,
      }),
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
          queryKeyPrefixes.configMaps,
          queryKeyPrefixes.configMap,
          queryKeyPrefixes.secrets,
          queryKeyPrefixes.secret,
          queryKeyPrefixes.authorizationResources,
          queryKeyPrefixes.authorizationResource,
          queryKeyPrefixes.networkingResources,
          queryKeyPrefixes.networkingResource,
          queryKeyPrefixes.pods,
          queryKeyPrefixes.pod,
        ].map((prefix) => queryClient.invalidateQueries({ queryKey: prefix })),
      );
    },
  });
}
