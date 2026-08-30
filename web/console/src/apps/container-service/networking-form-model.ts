import type {
  NetworkingDetail,
  NetworkingSummary,
  NetworkingSpecInput,
} from "@/api/queries/networking";
import type {
  KubernetesGatewaySpecInput,
  KubernetesEndpointSpecInput,
  KubernetesIngressSpecInput,
  KubernetesNetworkingResource,
  KubernetesServiceSpecInput,
} from "@/api/types";

import { SCRAPE_ANNOTATION_QUICK_FILL } from "@/lib/scrape-annotations";

import {
  buildGatewayRouteSpec,
  createGatewayRouteDraft,
  gatewayRouteDraftFromSpec,
  gatewayRouteDraftProblem,
  type GatewayRouteDraft,
} from "./gateway-route-form-model";

/** Radix Select cannot hold an empty value, so "leave it to Kubernetes" needs a name. */
export const DEFAULT_OPTION = "__default__";

const DNS_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;
const DNS_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
/** Service names are DNS-1035: a letter has to come first. */
const DNS1035_LABEL = /^[a-z]([-a-z0-9]*[a-z0-9])?$/;
const LABEL_NAME = /^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/;
const LABEL_VALUE = /^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$/;
/** Sequences Kubernetes refuses in an Ingress path. */
const INVALID_PATH_SEQUENCES = ["//", "/./", "/../", "%2f", "%2F"];

const MAX_DNS_LABEL_LENGTH = 63;
const MAX_SUBDOMAIN_LENGTH = 253;
const MAX_LABEL_VALUE_LENGTH = 63;
const MAX_PORT_NAME_LENGTH = 15;
/** Mirrors the Server's own ceilings, so the form refuses what it would refuse. */
const MAX_PORTS = 100;
const MAX_RULES = 256;
const MAX_PATHS = 256;
const MAX_LISTENERS = 64;
const MAX_CERTIFICATE_REFS = 64;
/** Annotation values are only bounded in total size, the way the Server bounds them. */
const MAX_ANNOTATION_BYTES = 256 * 1024;

export const MIN_PORT = 1;
export const MAX_PORT = 65535;
/** Said the same way wherever a port is asked for. */
export const PORT_RANGE = `${MIN_PORT}–${MAX_PORT}`;
/** No port is longer than this, so a sixth digit cannot be part of one. */
export const PORT_DIGITS = String(MAX_PORT).length;

/**
 * The port range kubeadm and most distributions allocate NodePorts from.
 *
 * Warned about rather than enforced: the real range is whatever the API Server
 * was started with (`--service-node-port-range`), so refusing everything outside
 * the usual one would reject valid ports on a cluster configured differently.
 */
const USUAL_NODE_PORT_MIN = 30000;
const USUAL_NODE_PORT_MAX = 32767;
export const USUAL_NODE_PORT_RANGE = `${USUAL_NODE_PORT_MIN}–${USUAL_NODE_PORT_MAX}`;

/**
 * Ports the cluster will probably refuse, which its DryRun will not say so.
 *
 * Kubernetes checks the NodePort range in the allocator, and a dry-run request
 * never reaches it: a NodePort outside the range passes the preflight this form
 * runs and then fails the actual create. Since the preflight cannot be relied on
 * here, the form says so beforehand — as a warning, because only the API Server
 * knows the real range and this one is merely the usual one.
 */
export function nodePortWarnings(draft: ServiceDraft): string[] {
  if (draft.type !== "NodePort" && draft.type !== "LoadBalancer") {
    return [];
  }
  const outside = draft.ports
    .map((port) => port.nodePort.trim())
    .filter(
      (nodePort) =>
        validPortNumber(nodePort) &&
        (Number(nodePort) < USUAL_NODE_PORT_MIN || Number(nodePort) > USUAL_NODE_PORT_MAX),
    );
  if (outside.length === 0) {
    return [];
  }
  return [
    `NodePort ${outside.join("、")} 不在通常的 ${USUAL_NODE_PORT_RANGE} 范围内。` +
      `实际范围由该集群 API Server 的 --service-node-port-range 决定：若不在其中，创建会被拒绝，` +
      `而 DryRun 预检不会发现——Kubernetes 在分配端口时才检查这个范围，预检不走分配这一步。`,
  ];
}

export type NetworkingSectionKey =
  | "basic"
  | "metadata"
  | "service"
  | "endpoint"
  | "ports"
  | "selector"
  | "ingress"
  | "rules"
  | "tls"
  | "gateway"
  | "listeners"
  | "route";

export type NetworkingProblem = { section: NetworkingSectionKey; message: string };

export type PortDraft = {
  name: string;
  port: string;
  targetPort: string;
  protocol: string;
  nodePort: string;
};
export type PairDraft = { key: string; value: string };
export type PathDraft = {
  path: string;
  pathType: string;
  backendName: string;
  backendPort: string;
};
export type RuleDraft = { host: string; paths: PathDraft[] };
export type TlsDraft = { hosts: string; secretName: string };
export type DefaultBackendDraft = { enabled: boolean; name: string; port: string };
export type ListenerDraft = {
  /** The name this listener had when the form opened, to match it to its object. */
  originalName: string | null;
  name: string;
  hostname: string;
  port: string;
  protocol: string;
  tlsMode: string;
  certificateSecrets: string;
  namespacesFrom: string;
};

export type ServiceDraft = {
  type: string;
  headless: boolean;
  externalName: string;
  sessionAffinity: string;
  externalPolicy: string;
  selector: PairDraft[];
  ports: PortDraft[];
};

export type EndpointAddressDraft = { ip: string; hostname: string; nodeName: string };
export type EndpointPortDraft = {
  name: string;
  port: string;
  protocol: string;
  appProtocol: string;
};
export type EndpointSubsetDraft = {
  addresses: EndpointAddressDraft[];
  notReadyAddresses: EndpointAddressDraft[];
  ports: EndpointPortDraft[];
};
export type EndpointDraft = { subsets: EndpointSubsetDraft[] };

export type IngressDraft = {
  className: string;
  defaultBackend: DefaultBackendDraft;
  rules: RuleDraft[];
  tls: TlsDraft[];
};

export type GatewayDraft = {
  className: string;
  listeners: ListenerDraft[];
};

/**
 * One draft holding all three shapes.
 *
 * Only the one belonging to the current resource is read, but keeping them in a
 * single value means one piece of state and one reset, and switching type in the
 * middle of filling a form does not throw away what was already typed.
 */
export type NetworkingDraft = {
  name: string;
  labels: PairDraft[];
  annotations: PairDraft[];
  service: ServiceDraft;
  endpoint: EndpointDraft;
  ingress: IngressDraft;
  gateway: GatewayDraft;
  gatewayRoute: GatewayRouteDraft;
};

export function emptyPort(): PortDraft {
  return { name: "", port: "", targetPort: "", protocol: DEFAULT_OPTION, nodePort: "" };
}

export function emptyPath(): PathDraft {
  return { path: "/", pathType: "Prefix", backendName: "", backendPort: "" };
}

export function emptyEndpointAddress(): EndpointAddressDraft {
  return { ip: "", hostname: "", nodeName: "" };
}

export function emptyEndpointPort(): EndpointPortDraft {
  return { name: "", port: "", protocol: DEFAULT_OPTION, appProtocol: "" };
}

export function emptyEndpointSubset(): EndpointSubsetDraft {
  return {
    addresses: [emptyEndpointAddress()],
    notReadyAddresses: [],
    ports: [emptyEndpointPort()],
  };
}

export function emptyRule(): RuleDraft {
  return { host: "", paths: [emptyPath()] };
}

export function emptyTls(): TlsDraft {
  return { hosts: "", secretName: "" };
}

export function emptyListener(): ListenerDraft {
  return {
    originalName: null,
    name: "",
    hostname: "",
    port: "80",
    protocol: "HTTP",
    tlsMode: DEFAULT_OPTION,
    certificateSecrets: "",
    namespacesFrom: DEFAULT_OPTION,
  };
}

/** The draft a form opens with: the existing object when editing, blank when creating. */
export function initialDraft(
  existing: NetworkingDetail | NetworkingSummary | null,
): NetworkingDraft {
  const service = existing?.service;
  const endpoint = existing?.endpoint;
  const ingress = existing?.ingress;
  const gateway = existing?.gateway;
  const defaultBackend = ingress?.spec.default_backend;
  return {
    name: existing?.name ?? "",
    labels: pairDrafts(existing?.labels),
    // Only a detail carries annotations. A summary is the list row, and a form
    // opened from one would submit an empty map over metadata it never read —
    // which is why the form waits for the detail before it offers this section.
    annotations: pairDrafts(
      existing && "annotations" in existing ? existing.annotations : undefined,
    ),
    service: {
      type: service?.spec.type || "ClusterIP",
      headless: service?.spec.headless ?? false,
      externalName: service?.spec.external_name ?? "",
      sessionAffinity: service?.spec.session_affinity || DEFAULT_OPTION,
      externalPolicy: service?.spec.external_traffic_policy || DEFAULT_OPTION,
      selector: Object.entries(service?.spec.selector ?? {}).map(([key, value]) => ({
        key,
        value,
      })),
      ports: (service?.spec.ports ?? []).map((port) => ({
        name: port.name,
        port: String(port.port),
        targetPort: port.target_port,
        protocol: port.protocol || DEFAULT_OPTION,
        nodePort: port.node_port ? String(port.node_port) : "",
      })),
    },
    endpoint: {
      subsets: (endpoint?.spec.subsets ?? []).map((subset) => ({
        addresses: subset.addresses.map((address) => ({
          ip: address.ip,
          hostname: address.hostname,
          nodeName: address.node_name,
        })),
        notReadyAddresses: subset.not_ready_addresses.map((address) => ({
          ip: address.ip,
          hostname: address.hostname,
          nodeName: address.node_name,
        })),
        ports: subset.ports.map((port) => ({
          name: port.name,
          port: String(port.port),
          protocol: port.protocol || DEFAULT_OPTION,
          appProtocol: port.app_protocol,
        })),
      })),
    },
    ingress: {
      className: ingress?.spec.ingress_class_name ?? "",
      defaultBackend: {
        // An absent backend and a `null` one both mean the Ingress has none.
        // The contract says the field is simply left out, and the Server now
        // does leave it out; a Server that still sends `null` must not be read
        // as "there is a default backend" and tick this box on every open.
        enabled: defaultBackend != null,
        name: defaultBackend?.name ?? "",
        port: defaultBackend?.port_name || String(defaultBackend?.port_number || ""),
      },
      rules: (ingress?.spec.rules ?? []).map((rule) => ({
        host: rule.host,
        paths: rule.paths.map((path) => ({
          path: path.path,
          pathType: path.path_type,
          backendName: path.backend.name,
          backendPort: path.backend.port_name || String(path.backend.port_number || ""),
        })),
      })),
      tls: (ingress?.spec.tls ?? []).map((entry) => ({
        hosts: entry.hosts.join(", "),
        secretName: entry.secret_name,
      })),
    },
    gateway: {
      className: gateway?.spec.gateway_class_name ?? "",
      listeners: (gateway?.spec.listeners ?? []).map((listener) => ({
        originalName: listener.name,
        name: listener.name,
        hostname: listener.hostname,
        port: String(listener.port),
        protocol: listener.protocol,
        tlsMode: listener.tls?.mode ?? DEFAULT_OPTION,
        certificateSecrets:
          listener.tls?.certificate_refs.map((reference) => reference.name).join(", ") ?? "",
        namespacesFrom: listener.allowed_routes.namespaces_from || DEFAULT_OPTION,
      })),
    },
    gatewayRoute: {
      ...gatewayRouteDraftFromSpec(existing?.gateway_route?.spec ?? {}),
    },
  };
}

/** Creating a Service starts with one port row; an empty list can never be valid. */
function pairDrafts(entries: Record<string, string> | undefined): PairDraft[] {
  return Object.entries(entries ?? {}).map(([key, value]) => ({ key, value }));
}

/**
 * Whether this kind of object is one the metrics collector discovers targets
 * from. Offering the annotations anywhere else would put a key on an object
 * nothing reads it from, which reads as collection that is not happening.
 */
export function supportsScrapeAnnotations(resource: KubernetesNetworkingResource): boolean {
  return resource === "services" || resource === "endpoints";
}

/** Adds the metrics annotations that are not already on the object. */
export function withScrapeAnnotations(rows: PairDraft[]): PairDraft[] {
  const present = new Set(rows.map((row) => row.key.trim()));
  const blank = rows.every((row) => row.key.trim() === "" && row.value.trim() === "");
  return [
    ...(blank ? [] : rows),
    ...SCRAPE_ANNOTATION_QUICK_FILL.filter((entry) => !present.has(entry.key)),
  ];
}

export function createDraft(resource: KubernetesNetworkingResource): NetworkingDraft {
  const draft = initialDraft(null);
  if (resource === "services") {
    return { ...draft, service: { ...draft.service, ports: [emptyPort()] } };
  }
  if (resource === "endpoints") {
    return { ...draft, endpoint: { subsets: [emptyEndpointSubset()] } };
  }
  if (resource === "ingresses") {
    return { ...draft, ingress: { ...draft.ingress, rules: [emptyRule()] } };
  }
  if (resource === "gateways") {
    return { ...draft, gateway: { ...draft.gateway, listeners: [emptyListener()] } };
  }
  return { ...draft, gatewayRoute: createGatewayRouteDraft(resource) };
}

/**
 * The reason the form cannot be submitted, and which section carries it.
 *
 * Sections are checked in the order they are read, so the message always belongs
 * to the topmost unfinished one. Every rule here has a counterpart on the Server;
 * the point is to say what is wrong next to the field rather than to let the
 * request come back as one flat rejection.
 */
export function networkingProblem(
  draft: NetworkingDraft,
  resource: KubernetesNetworkingResource,
  editing: boolean,
): NetworkingProblem | null {
  if (!editing) {
    const name = draft.name.trim();
    if (resource === "services") {
      if (!DNS1035_LABEL.test(name) || name.length > MAX_DNS_LABEL_LENGTH) {
        return at(
          "basic",
          `Service 名称必须以小写字母开头，只包含小写字母、数字和 -，最长 ${MAX_DNS_LABEL_LENGTH} 个字符。`,
        );
      }
    } else if (!DNS_SUBDOMAIN.test(name) || name.length > MAX_SUBDOMAIN_LENGTH) {
      return at("basic", `名称必须是合法的 DNS 子域名，最长 ${MAX_SUBDOMAIN_LENGTH} 个字符。`);
    }
  }
  const metadataProblem =
    pairsProblem(draft.labels, "标签", "label") ?? pairsProblem(draft.annotations, "注解", "text");
  if (metadataProblem) {
    return at("metadata", metadataProblem);
  }
  if (resource === "services") {
    return serviceProblem(draft.service);
  }
  if (resource === "endpoints") {
    return endpointProblem(draft.endpoint);
  }
  if (resource === "ingresses") {
    return ingressProblem(draft.ingress);
  }
  if (resource === "gateways") {
    return gatewayProblem(draft.gateway);
  }
  const routeProblem = gatewayRouteDraftProblem(draft.gatewayRoute, resource);
  return routeProblem ? at("route", routeProblem) : null;
}

/**
 * The rules the Server applies to metadata, applied here so the form says which
 * row is wrong instead of the request coming back as a flat 400.
 *
 * Label values are constrained to what Kubernetes accepts as a label value;
 * annotation values are free text and only bounded in total size.
 */
function pairsProblem(rows: PairDraft[], label: string, values: "label" | "text"): string | null {
  const keys = new Set<string>();
  let total = 0;
  for (const row of rows) {
    const key = row.key.trim();
    const value = values === "label" ? row.value.trim() : row.value;
    if (key === "" && value === "") {
      continue;
    }
    if (key === "") {
      return `${label}的键不能为空。`;
    }
    if (!qualifiedName(key)) {
      return `${label}键 ${key} 不是合法的 Kubernetes 键名。`;
    }
    if (keys.has(key)) {
      return `${label}键 ${key} 重复。`;
    }
    keys.add(key);
    if (values === "label") {
      if (!LABEL_VALUE.test(value) || value.length > MAX_LABEL_VALUE_LENGTH) {
        return `${label} ${key} 的值必须是最长 ${MAX_LABEL_VALUE_LENGTH} 个字符的字母、数字、-、_ 或 .。`;
      }
      continue;
    }
    total += byteLength(key) + byteLength(value);
    if (total > MAX_ANNOTATION_BYTES) {
      return `${label}的总长度不能超过 ${MAX_ANNOTATION_BYTES / 1024} KiB。`;
    }
  }
  return null;
}

/** Annotations are bounded in bytes, and Chinese text is three of them a character. */
function byteLength(value: string): number {
  return new TextEncoder().encode(value).length;
}

function endpointProblem(draft: EndpointDraft): NetworkingProblem | null {
  if (draft.subsets.length > 100) {
    return at("endpoint", "Endpoint 子集不能超过 100 个。");
  }
  let addressCount = 0;
  let portCount = 0;
  for (const [subsetIndex, subset] of draft.subsets.entries()) {
    const subsetLabel = `第 ${subsetIndex + 1} 个子集`;
    addressCount += subset.addresses.length + subset.notReadyAddresses.length;
    portCount += subset.ports.length;
    if (addressCount > 1000) {
      return at("endpoint", "所有子集合计不能超过 1000 个地址。");
    }
    if (portCount > 100) {
      return at("endpoint", "所有子集合计不能超过 100 个端口。");
    }
    for (const [addressIndex, address] of [
      ...subset.addresses.map((value) => ({ value, readiness: "就绪" })),
      ...subset.notReadyAddresses.map((value) => ({ value, readiness: "未就绪" })),
    ].entries()) {
      const label = `${subsetLabel}的第 ${addressIndex + 1} 个${address.readiness}地址`;
      if (!validIPAddress(address.value.ip.trim())) {
        return at("endpoint", `${label}必须是合法的 IPv4 或 IPv6 地址。`);
      }
      for (const [field, value] of [
        ["主机名", address.value.hostname],
        ["节点名", address.value.nodeName],
      ] as const) {
        const trimmed = value.trim();
        if (trimmed && (!DNS_SUBDOMAIN.test(trimmed) || trimmed.length > MAX_SUBDOMAIN_LENGTH)) {
          return at("endpoint", `${label}的${field}必须是合法的 DNS 子域名。`);
        }
      }
    }
    const keys = new Set<string>();
    for (const [portIndex, port] of subset.ports.entries()) {
      const label = `${subsetLabel}的第 ${portIndex + 1} 个端口`;
      if (!validPortNumber(port.port)) {
        return at("endpoint", `${label}必须是 ${PORT_RANGE} 之间的数字。`);
      }
      if (port.name.trim() && !validPortName(port.name)) {
        return at("endpoint", `${label}的名称不合法：${PORT_NAME_RULE}`);
      }
      if (port.appProtocol.trim() && !qualifiedName(port.appProtocol.trim())) {
        return at("endpoint", `${label}的 AppProtocol 必须是合法的 Kubernetes 限定名称。`);
      }
      const protocol = port.protocol === DEFAULT_OPTION ? "TCP" : port.protocol;
      const key = `${protocol}/${port.port.trim()}`;
      if (keys.has(key)) {
        return at("endpoint", `${subsetLabel}中的端口 ${port.port.trim()}/${protocol} 重复。`);
      }
      keys.add(key);
    }
  }
  return null;
}

function validIPAddress(value: string): boolean {
  const ipv4 = value.split(".");
  if (ipv4.length === 4 && ipv4.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255)) {
    return true;
  }
  return value.includes(":") && /^[0-9a-f:.]+$/i.test(value);
}

function serviceProblem(draft: ServiceDraft): NetworkingProblem | null {
  if (draft.type === "ExternalName") {
    const externalName = draft.externalName.trim();
    if (!DNS_SUBDOMAIN.test(externalName) || externalName.length > MAX_SUBDOMAIN_LENGTH) {
      return at("service", "ExternalName 必须是合法的 DNS 域名，例如 example.com。");
    }
    return null;
  }

  if (draft.ports.length === 0) {
    return at("ports", "非 ExternalName 类型的 Service 至少需要一个端口。");
  }
  if (draft.ports.length > MAX_PORTS) {
    return at("ports", `端口不能超过 ${MAX_PORTS} 个。`);
  }
  const exposesNodePort = draft.type === "NodePort" || draft.type === "LoadBalancer";
  const names = new Set<string>();
  const keys = new Set<string>();
  for (const [index, port] of draft.ports.entries()) {
    const position = `第 ${index + 1} 个端口`;
    const name = port.name.trim();
    if (draft.ports.length > 1 && name === "") {
      return at("ports", `${position}缺少名称：多个端口时 Kubernetes 要求每个都有名称。`);
    }
    if (name !== "" && !validPortName(name)) {
      return at("ports", `${position}的名称不合法：${PORT_NAME_RULE}`);
    }
    if (name !== "" && names.has(name)) {
      return at("ports", `端口名称 ${name} 重复。`);
    }
    names.add(name);

    if (!validPortNumber(port.port)) {
      return at("ports", `${position}必须是 ${PORT_RANGE} 之间的数字。`);
    }
    const targetPort = port.targetPort.trim();
    if (targetPort !== "" && !validPortNumber(targetPort) && !validPortName(targetPort)) {
      return at(
        "ports",
        `${position}的目标端口必须是 ${PORT_RANGE} 之间的数字，或容器端口的名称（${PORT_NAME_RULE}）。`,
      );
    }
    if (port.nodePort.trim() !== "") {
      if (!exposesNodePort) {
        return at(
          "ports",
          `${position}设置了 NodePort，但只有 NodePort 和 LoadBalancer 类型使用它。`,
        );
      }
      if (!validPortNumber(port.nodePort)) {
        return at("ports", `${position}的 NodePort 必须是 ${PORT_RANGE} 之间的数字。`);
      }
    }

    const protocol = port.protocol === DEFAULT_OPTION ? "TCP" : port.protocol;
    const key = `${protocol}/${port.port.trim()}`;
    if (keys.has(key)) {
      return at("ports", `端口 ${port.port.trim()}/${protocol} 重复。`);
    }
    keys.add(key);
  }

  const selectorKeys = new Set<string>();
  for (const pair of draft.selector) {
    const key = pair.key.trim();
    const value = pair.value.trim();
    if (key === "" && value === "") {
      continue;
    }
    if (key === "") {
      return at("selector", "选择器的键不能为空。");
    }
    if (!qualifiedName(key)) {
      return at("selector", `选择器键 ${key} 不是合法的 Kubernetes 键名。`);
    }
    if (!LABEL_VALUE.test(value) || value.length > MAX_LABEL_VALUE_LENGTH) {
      return at(
        "selector",
        `选择器 ${key} 的值必须是最长 ${MAX_LABEL_VALUE_LENGTH} 个字符的字母、数字、-、_ 或 .。`,
      );
    }
    if (selectorKeys.has(key)) {
      return at("selector", `选择器键 ${key} 重复。`);
    }
    selectorKeys.add(key);
  }
  return null;
}

function ingressProblem(draft: IngressDraft): NetworkingProblem | null {
  const className = draft.className.trim();
  if (
    className !== "" &&
    (!DNS_SUBDOMAIN.test(className) || className.length > MAX_SUBDOMAIN_LENGTH)
  ) {
    return at("ingress", "IngressClass 必须是合法的 DNS 子域名。");
  }
  if (draft.defaultBackend.enabled) {
    const problem = backendProblem(
      draft.defaultBackend.name,
      draft.defaultBackend.port,
      "默认后端",
    );
    if (problem) {
      return at("ingress", problem);
    }
  }
  if (!draft.defaultBackend.enabled && draft.rules.length === 0) {
    return at("rules", "默认后端和转发规则至少要配置一项，否则 Ingress 不会转发任何流量。");
  }
  if (draft.rules.length > MAX_RULES) {
    return at("rules", `转发规则不能超过 ${MAX_RULES} 条。`);
  }

  const seen = new Set<string>();
  for (const [ruleIndex, rule] of draft.rules.entries()) {
    const position = `第 ${ruleIndex + 1} 条规则`;
    const host = rule.host.trim();
    if (!validHostname(host)) {
      return at("rules", `${position}的主机不合法：可以留空、写域名，或以 *. 开头的泛域名。`);
    }
    if (rule.paths.length === 0) {
      return at("rules", `${position}至少需要一个路径。`);
    }
    if (rule.paths.length > MAX_PATHS) {
      return at("rules", `${position}的路径不能超过 ${MAX_PATHS} 条。`);
    }
    for (const [pathIndex, path] of rule.paths.entries()) {
      const where = `${position}的第 ${pathIndex + 1} 个路径`;
      const value = path.path.trim();
      const exactOrPrefix = path.pathType === "Exact" || path.pathType === "Prefix";
      if (exactOrPrefix && !value.startsWith("/")) {
        return at("rules", `${where}必须以 / 开头，${path.pathType} 匹配只接受绝对路径。`);
      }
      if (value !== "" && !value.startsWith("/")) {
        return at("rules", `${where}必须以 / 开头。`);
      }
      const invalid = INVALID_PATH_SEQUENCES.find((sequence) => value.includes(sequence));
      if (invalid !== undefined) {
        return at("rules", `${where}不能包含 ${invalid}。`);
      }
      const problem = backendProblem(path.backendName, path.backendPort, `${where}的后端`);
      if (problem) {
        return at("rules", problem);
      }
      // Two identical routes are accepted by Kubernetes and then only one of
      // them takes effect, with nothing to say which.
      const key = `${host}\n${path.pathType}\n${value}`;
      if (seen.has(key)) {
        return at("rules", `${where}与前面的规则完全相同，其中只有一条会生效。`);
      }
      seen.add(key);
    }
  }

  for (const [index, entry] of draft.tls.entries()) {
    const position = `第 ${index + 1} 项 TLS`;
    const secretName = entry.secretName.trim();
    const hosts = splitCommaList(entry.hosts);
    if (secretName === "" && hosts.length === 0) {
      continue;
    }
    if (
      secretName !== "" &&
      (!DNS_SUBDOMAIN.test(secretName) || secretName.length > MAX_SUBDOMAIN_LENGTH)
    ) {
      return at("tls", `${position}的 Secret 名称必须是合法的 DNS 子域名。`);
    }
    const badHost = hosts.find((host) => !validHostname(host));
    if (badHost !== undefined) {
      return at("tls", `${position}的主机 ${badHost} 不合法。`);
    }
  }
  return null;
}

function gatewayProblem(draft: GatewayDraft): NetworkingProblem | null {
  const className = draft.className.trim();
  if (!DNS_SUBDOMAIN.test(className) || className.length > MAX_SUBDOMAIN_LENGTH) {
    return at("gateway", "GatewayClass 必填，且必须是合法的 DNS 子域名。");
  }
  if (draft.listeners.length === 0) {
    return at("listeners", "Gateway 至少需要一个监听器。");
  }
  if (draft.listeners.length > MAX_LISTENERS) {
    return at("listeners", `监听器不能超过 ${MAX_LISTENERS} 个。`);
  }
  const names = new Set<string>();
  for (const [index, listener] of draft.listeners.entries()) {
    const position = `第 ${index + 1} 个监听器`;
    const name = listener.name.trim();
    if (!DNS_LABEL.test(name) || name.length > MAX_DNS_LABEL_LENGTH) {
      return at(
        "listeners",
        `${position}的名称必须是小写字母、数字和 -，最长 ${MAX_DNS_LABEL_LENGTH} 个字符。`,
      );
    }
    if (names.has(name)) {
      return at("listeners", `监听器名称 ${name} 重复。`);
    }
    names.add(name);
    if (!validPortNumber(listener.port)) {
      return at("listeners", `${position}的端口必须是 ${PORT_RANGE} 之间的数字。`);
    }
    if (!validHostname(listener.hostname.trim())) {
      return at("listeners", `${position}的主机不合法：可以留空、写域名，或以 *. 开头的泛域名。`);
    }

    const certificates = splitCommaList(listener.certificateSecrets);
    if (certificates.length > MAX_CERTIFICATE_REFS) {
      return at("listeners", `${position}的证书不能超过 ${MAX_CERTIFICATE_REFS} 个。`);
    }
    const badCertificate = certificates.find(
      (certificate) =>
        !DNS_SUBDOMAIN.test(certificate) || certificate.length > MAX_SUBDOMAIN_LENGTH,
    );
    if (badCertificate !== undefined) {
      return at("listeners", `${position}的证书 Secret 名称 ${badCertificate} 不合法。`);
    }
    if (listener.protocol === "HTTPS") {
      if (listener.tlsMode !== "Terminate") {
        return at("listeners", `${position}使用 HTTPS，TLS 模式必须是 Terminate。`);
      }
      if (certificates.length === 0) {
        return at("listeners", `${position}使用 Terminate，至少需要一个证书 Secret。`);
      }
    } else if (listener.protocol === "TLS") {
      if (listener.tlsMode === DEFAULT_OPTION) {
        return at("listeners", `${position}使用 TLS，需要选择 Terminate 或 Passthrough。`);
      }
      if (listener.tlsMode === "Terminate" && certificates.length === 0) {
        return at("listeners", `${position}使用 Terminate，至少需要一个证书 Secret。`);
      }
      if (listener.tlsMode === "Passthrough" && certificates.length > 0) {
        return at("listeners", `${position}使用 Passthrough，由后端自行终止 TLS，不接受证书。`);
      }
    }
  }
  return null;
}

const PORT_NAME_RULE =
  "最长 15 个字符的小写字母、数字和 -，至少含一个字母，不能以 - 开头或结尾，也不能连续两个 -。";

function backendProblem(name: string, port: string, label: string): string | null {
  const trimmedName = name.trim();
  if (!DNS1035_LABEL.test(trimmedName) || trimmedName.length > MAX_DNS_LABEL_LENGTH) {
    return `${label}的 Service 名称必须以小写字母开头，只包含小写字母、数字和 -。`;
  }
  const trimmedPort = port.trim();
  if (!validPortNumber(trimmedPort) && !validPortName(trimmedPort)) {
    return `${label}的端口必须是 ${PORT_RANGE} 之间的数字，或该 Service 端口的名称。`;
  }
  return null;
}

function at(section: NetworkingSectionKey, message: string): NetworkingProblem {
  return { section, message };
}

export function validPortNumber(value: string): boolean {
  const trimmed = value.trim();
  if (!/^\d+$/.test(trimmed)) {
    return false;
  }
  const port = Number(trimmed);
  return port >= MIN_PORT && port <= MAX_PORT;
}

/** Kubernetes' IsValidPortName, which is stricter than a DNS label. */
export function validPortName(value: string): boolean {
  const trimmed = value.trim();
  return (
    trimmed.length > 0 &&
    trimmed.length <= MAX_PORT_NAME_LENGTH &&
    /^[a-z0-9-]+$/.test(trimmed) &&
    /[a-z]/.test(trimmed) &&
    !trimmed.startsWith("-") &&
    !trimmed.endsWith("-") &&
    !trimmed.includes("--")
  );
}

/** `[prefix/]name`, the shape Kubernetes calls a qualified name. */
function qualifiedName(value: string): boolean {
  const slash = value.indexOf("/");
  if (slash === -1) {
    return LABEL_NAME.test(value) && value.length <= MAX_DNS_LABEL_LENGTH;
  }
  const prefix = value.slice(0, slash);
  const name = value.slice(slash + 1);
  return (
    DNS_SUBDOMAIN.test(prefix) &&
    prefix.length <= MAX_SUBDOMAIN_LENGTH &&
    LABEL_NAME.test(name) &&
    name.length <= MAX_DNS_LABEL_LENGTH
  );
}

export function validHostname(value: string): boolean {
  const hostname = value.trim();
  if (hostname === "") {
    return true;
  }
  if (hostname.length > MAX_SUBDOMAIN_LENGTH) {
    return false;
  }
  if (hostname.startsWith("*.")) {
    return DNS_SUBDOMAIN.test(hostname.slice(2));
  }
  return DNS_SUBDOMAIN.test(hostname);
}

export function splitCommaList(value: string): string[] {
  return value
    .split(",")
    .map((entry) => entry.trim())
    .filter(Boolean);
}

/** A backend port is either a number or a Service port name; the API takes them apart. */
function backendInput(name: string, port: string) {
  return /^\d+$/.test(port) ? { name, port_number: Number(port) } : { name, port_name: port };
}

/**
 * The metadata a create or update carries beside the typed spec.
 *
 * Always both maps, never omitted: the form showed every row the object has, so
 * an empty one is somebody having removed them rather than a caller that never
 * looked. The Server tells those two apart, and this is the side that means the
 * first one.
 */
export function buildNetworkingMetadata(draft: NetworkingDraft): {
  labels: Record<string, string>;
  annotations: Record<string, string>;
} {
  return { labels: pairRecord(draft.labels), annotations: pairRecord(draft.annotations) };
}

function pairRecord(rows: PairDraft[]): Record<string, string> {
  const result: Record<string, string> = {};
  for (const row of rows) {
    const key = row.key.trim();
    if (key !== "") {
      result[key] = row.value;
    }
  }
  return result;
}

export function buildNetworkingSpec(
  draft: NetworkingDraft,
  resource: KubernetesNetworkingResource,
  existing: NetworkingSummary | null,
): NetworkingSpecInput {
  if (resource === "services") {
    return { service: buildServiceSpec(draft.service, existing) };
  }
  if (resource === "endpoints") {
    return { endpoint: buildEndpointSpec(draft.endpoint) };
  }
  if (resource === "ingresses") {
    return { ingress: buildIngressSpec(draft.ingress) };
  }
  if (resource === "gateways") {
    return { gateway: buildGatewaySpec(draft.gateway, existing) };
  }
  return {
    gateway_route: { spec: buildGatewayRouteSpec(draft.gatewayRoute, resource) },
  };
}

function buildEndpointSpec(draft: EndpointDraft): KubernetesEndpointSpecInput {
  const address = (value: EndpointAddressDraft) => ({
    ip: value.ip.trim(),
    hostname: value.hostname.trim(),
    node_name: value.nodeName.trim(),
  });
  return {
    subsets: draft.subsets.map((subset) => ({
      addresses: subset.addresses.map(address),
      not_ready_addresses: subset.notReadyAddresses.map(address),
      ports: subset.ports.map((port) => ({
        name: port.name.trim(),
        port: Number(port.port.trim()),
        protocol: (port.protocol === DEFAULT_OPTION ? "TCP" : port.protocol) as
          "TCP" | "UDP" | "SCTP",
        app_protocol: port.appProtocol.trim(),
      })),
    })),
  };
}

function buildServiceSpec(
  draft: ServiceDraft,
  existing: NetworkingSummary | null,
): KubernetesServiceSpecInput {
  const view = existing?.service;
  const externalNameService = draft.type === "ExternalName";
  const exposesNodePort = draft.type === "NodePort" || draft.type === "LoadBalancer";
  return {
    type: draft.type as "ClusterIP" | "NodePort" | "LoadBalancer" | "ExternalName",
    headless: draft.type === "ClusterIP" ? draft.headless : false,
    selector: externalNameService
      ? {}
      : Object.fromEntries(
          draft.selector
            .filter((pair) => pair.key.trim() !== "")
            .map((pair) => [pair.key.trim(), pair.value.trim()]),
        ),
    ports: externalNameService
      ? []
      : draft.ports.map((port) => ({
          ...(port.name.trim() ? { name: port.name.trim() } : {}),
          port: Number(port.port.trim()),
          ...(port.targetPort.trim() ? { target_port: port.targetPort.trim() } : {}),
          ...(port.protocol === DEFAULT_OPTION
            ? {}
            : { protocol: port.protocol as "TCP" | "UDP" | "SCTP" }),
          ...(exposesNodePort && port.nodePort.trim()
            ? { node_port: Number(port.nodePort.trim()) }
            : {}),
        })),
    ...(externalNameService ? { external_name: draft.externalName.trim() } : {}),
    ...(!externalNameService && draft.sessionAffinity !== DEFAULT_OPTION
      ? { session_affinity: draft.sessionAffinity as "None" | "ClientIP" }
      : {}),
    ...(exposesNodePort && draft.externalPolicy !== DEFAULT_OPTION
      ? { external_traffic_policy: draft.externalPolicy as "Cluster" | "Local" }
      : {}),
    // Fields this form does not model are carried across an update rather than
    // dropped: leaving them out would remove them from the object.
    ...(!externalNameService && view?.spec.internal_traffic_policy
      ? { internal_traffic_policy: view.spec.internal_traffic_policy }
      : {}),
    ...(!externalNameService && view?.spec.publish_not_ready_addresses
      ? { publish_not_ready_addresses: true }
      : {}),
    ...(draft.type === "LoadBalancer" && view?.spec.allocate_load_balancer_node_ports !== undefined
      ? { allocate_load_balancer_node_ports: view.spec.allocate_load_balancer_node_ports }
      : {}),
  };
}

function buildIngressSpec(draft: IngressDraft): KubernetesIngressSpecInput {
  return {
    ...(draft.className.trim() ? { ingress_class_name: draft.className.trim() } : {}),
    ...(draft.defaultBackend.enabled
      ? {
          default_backend: backendInput(
            draft.defaultBackend.name.trim(),
            draft.defaultBackend.port.trim(),
          ),
        }
      : {}),
    rules: draft.rules.map((rule) => ({
      ...(rule.host.trim() ? { host: rule.host.trim() } : {}),
      paths: rule.paths.map((path) => ({
        ...(path.path.trim() ? { path: path.path.trim() } : {}),
        path_type: path.pathType as "Exact" | "Prefix" | "ImplementationSpecific",
        backend: backendInput(path.backendName.trim(), path.backendPort.trim()),
      })),
    })),
    tls: draft.tls
      .filter((entry) => entry.secretName.trim() !== "" || entry.hosts.trim() !== "")
      .map((entry) => ({
        ...(entry.secretName.trim() ? { secret_name: entry.secretName.trim() } : {}),
        hosts: splitCommaList(entry.hosts),
      })),
  };
}

function buildGatewaySpec(
  draft: GatewayDraft,
  existing: NetworkingSummary | null,
): KubernetesGatewaySpecInput {
  const view = existing?.gateway;
  return {
    gateway_class_name: draft.className.trim(),
    ...(view?.spec.addresses.length
      ? {
          addresses: view.spec.addresses.map((address) => ({
            ...(address.type ? { type: address.type } : {}),
            value: address.value,
          })),
        }
      : {}),
    listeners: draft.listeners.map((listener) => {
      const original = view?.spec.listeners.find(
        (candidate) => candidate.name === listener.originalName,
      );
      const protocol = listener.protocol as "HTTP" | "HTTPS" | "TLS" | "TCP" | "UDP";
      const tlsCapable = protocol === "HTTPS" || protocol === "TLS";
      const certificateNames = splitCommaList(listener.certificateSecrets);
      const originalCertificateNames =
        original?.tls?.certificate_refs.map((reference) => reference.name).join(", ") ?? "";
      // An untouched certificate list keeps its full references — group, kind and
      // namespace included — instead of being flattened to bare names.
      const preserveCertificateReferences =
        listener.certificateSecrets.trim() === originalCertificateNames &&
        listener.tlsMode === original?.tls?.mode;
      const certificateRefs = preserveCertificateReferences
        ? original?.tls?.certificate_refs.map((reference) => ({
            ...(reference.group ? { group: reference.group } : {}),
            ...(reference.kind ? { kind: reference.kind } : {}),
            name: reference.name,
            ...(reference.namespace ? { namespace: reference.namespace } : {}),
          }))
        : certificateNames.map((name) => ({ name }));
      const routeKinds =
        original?.allowed_routes.kinds.map((kind) => ({
          ...(kind.group ? { group: kind.group } : {}),
          kind: kind.kind,
        })) ?? [];
      const includeAllowedRoutes =
        listener.namespacesFrom !== DEFAULT_OPTION || routeKinds.length > 0;

      return {
        name: listener.name.trim(),
        ...(listener.hostname.trim() ? { hostname: listener.hostname.trim() } : {}),
        port: Number(listener.port.trim()),
        protocol,
        ...(tlsCapable && listener.tlsMode !== DEFAULT_OPTION
          ? {
              tls: {
                mode: listener.tlsMode as "Terminate" | "Passthrough",
                ...(listener.tlsMode === "Terminate" ? { certificate_refs: certificateRefs } : {}),
              },
            }
          : {}),
        ...(includeAllowedRoutes
          ? {
              allowed_routes: {
                ...(listener.namespacesFrom !== DEFAULT_OPTION
                  ? { namespaces_from: listener.namespacesFrom as "Same" | "All" | "Selector" }
                  : {}),
                ...(listener.namespacesFrom === "Selector" && original?.allowed_routes.selector
                  ? { selector: original.allowed_routes.selector }
                  : {}),
                ...(routeKinds.length > 0 ? { kinds: routeKinds } : {}),
              },
            }
          : {}),
      };
    }),
  };
}
