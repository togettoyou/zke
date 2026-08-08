import type { KubernetesNetworkingResource } from "@/api/types";

/**
 * The networking types exposed by the scoped typed API.
 *
 * Gateway is last because it is the one that may not exist: the Gateway API is a
 * set of CRDs a Cluster either has or does not, and ZKE does not install it.
 */
export const NETWORKING_TYPES: { resource: KubernetesNetworkingResource; label: string }[] = [
  { resource: "services", label: "Service" },
  { resource: "ingresses", label: "Ingress" },
  { resource: "gateways", label: "Gateway" },
  { resource: "httproutes", label: "HTTPRoute" },
  { resource: "grpcroutes", label: "GRPCRoute" },
  { resource: "tlsroutes", label: "TLSRoute" },
  { resource: "tcproutes", label: "TCPRoute" },
  { resource: "udproutes", label: "UDPRoute" },
];

export function networkingKindLabel(resource: KubernetesNetworkingResource): string {
  return NETWORKING_TYPES.find((type) => type.resource === resource)?.label ?? resource;
}

/** The GVR each type lives at, for the YAML editor's generic route. */
export function networkingIdentity(resource: KubernetesNetworkingResource): {
  group: string;
  version: string;
  resource: string;
} {
  switch (resource) {
    case "services":
      return { group: "", version: "v1", resource: "services" };
    case "ingresses":
      return { group: "networking.k8s.io", version: "v1", resource: "ingresses" };
    case "gateways":
      return { group: "gateway.networking.k8s.io", version: "v1", resource: "gateways" };
    case "httproutes":
      return { group: "gateway.networking.k8s.io", version: "v1", resource: "httproutes" };
    case "grpcroutes":
      return { group: "gateway.networking.k8s.io", version: "v1", resource: "grpcroutes" };
    case "tlsroutes":
      return { group: "gateway.networking.k8s.io", version: "v1", resource: "tlsroutes" };
    case "tcproutes":
      return {
        group: "gateway.networking.k8s.io",
        version: "v1alpha2",
        resource: "tcproutes",
      };
    case "udproutes":
      return {
        group: "gateway.networking.k8s.io",
        version: "v1alpha2",
        resource: "udproutes",
      };
  }
}

export function isGatewayRouteResource(resource: KubernetesNetworkingResource): boolean {
  return resource.endsWith("routes") && resource !== "ingresses";
}

/**
 * The Server answers 409 `gateway_api_unavailable` when the target Cluster has
 * no Gateway API CRDs. That is a fact about the Cluster rather than a failed
 * request, and it reads very differently to an operator.
 */
export const GATEWAY_UNAVAILABLE_CODE = "gateway_api_unavailable";
