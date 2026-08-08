import type { KubernetesNetworkingResource } from "@/api/types";

const DEFAULT_OPTION = "__default__";
const PORT_RANGE = "1–65535";
const DNS_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;
const DNS_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const QUALIFIED_NAME = /^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/;
const HEADER_NAME = /^[A-Za-z0-9!#$%&'*+\-.^_`|~]+$/;
const GRPC_SERVICE = /^\.?[A-Za-z_][A-Za-z_0-9]*(\.[A-Za-z_][A-Za-z_0-9]*)*$/;
const GRPC_METHOD = /^[A-Za-z_][A-Za-z_0-9]*$/;
const MAX_PARENTS = 32;
const MAX_HOSTNAMES = 16;
const MAX_MATCHES = 64;
const MAX_MATCH_FIELDS = 16;
const MAX_BACKENDS_PER_RULE = 16;
const MAX_BACKENDS = 256;

type NativeObject = Record<string, unknown>;

export type RouteParentDraft = {
  group: string;
  kind: string;
  namespace: string;
  name: string;
  sectionName: string;
  port: string;
  source: NativeObject;
};

export type RouteBackendDraft = {
  group: string;
  kind: string;
  namespace: string;
  name: string;
  port: string;
  weight: string;
  source: NativeObject;
};

export type RouteValueMatchDraft = {
  type: string;
  name: string;
  value: string;
  source: NativeObject;
};

export type HTTPRouteMatchDraft = {
  pathType: string;
  pathValue: string;
  method: string;
  headers: RouteValueMatchDraft[];
  queryParams: RouteValueMatchDraft[];
  source: NativeObject;
};

export type GRPCRouteMatchDraft = {
  methodType: string;
  service: string;
  method: string;
  headers: RouteValueMatchDraft[];
  source: NativeObject;
};

export type GatewayRouteRuleDraft = {
  name: string;
  backends: RouteBackendDraft[];
  httpMatches: HTTPRouteMatchDraft[];
  grpcMatches: GRPCRouteMatchDraft[];
  source: NativeObject;
};

export type GatewayRouteDraft = {
  source: NativeObject;
  parents: RouteParentDraft[];
  hostnames: string;
  rules: GatewayRouteRuleDraft[];
};

export function emptyRouteParent(): RouteParentDraft {
  return {
    group: DEFAULT_OPTION,
    kind: DEFAULT_OPTION,
    namespace: "",
    name: "",
    sectionName: "",
    port: "",
    source: {},
  };
}

export function emptyRouteBackend(): RouteBackendDraft {
  return {
    group: DEFAULT_OPTION,
    kind: DEFAULT_OPTION,
    namespace: "",
    name: "",
    port: "80",
    weight: "",
    source: {},
  };
}

export function emptyRouteValueMatch(): RouteValueMatchDraft {
  return { type: "Exact", name: "", value: "", source: {} };
}

export function emptyHTTPRouteMatch(): HTTPRouteMatchDraft {
  return {
    pathType: "PathPrefix",
    pathValue: "/",
    method: DEFAULT_OPTION,
    headers: [],
    queryParams: [],
    source: {},
  };
}

export function emptyGRPCRouteMatch(): GRPCRouteMatchDraft {
  return {
    methodType: DEFAULT_OPTION,
    service: "",
    method: "",
    headers: [],
    source: {},
  };
}

export function emptyGatewayRouteRule(
  resource: KubernetesNetworkingResource,
): GatewayRouteRuleDraft {
  return {
    name: "",
    backends: [emptyRouteBackend()],
    httpMatches: resource === "httproutes" ? [emptyHTTPRouteMatch()] : [],
    grpcMatches: resource === "grpcroutes" ? [emptyGRPCRouteMatch()] : [],
    source: {},
  };
}

export function createGatewayRouteDraft(resource: KubernetesNetworkingResource): GatewayRouteDraft {
  return {
    source: {},
    parents: [{ ...emptyRouteParent(), name: "gateway-name" }],
    hostnames: resource === "tlsroutes" ? "example.com" : "",
    rules: [emptyGatewayRouteRule(resource)],
  };
}

export function gatewayRouteDraftFromSpec(spec: NativeObject): GatewayRouteDraft {
  return {
    source: spec,
    parents: objectList(spec.parentRefs).map(parentFromSpec),
    hostnames: stringList(spec.hostnames).join(", "),
    rules: objectList(spec.rules).map(ruleFromSpec),
  };
}

function parentFromSpec(source: NativeObject): RouteParentDraft {
  return {
    group: hasOwn(source, "group") ? stringValue(source.group) : DEFAULT_OPTION,
    kind: stringValue(source.kind) || DEFAULT_OPTION,
    namespace: stringValue(source.namespace),
    name: stringValue(source.name),
    sectionName: stringValue(source.sectionName),
    port: numberText(source.port),
    source,
  };
}

function backendFromSpec(source: NativeObject): RouteBackendDraft {
  return {
    group: hasOwn(source, "group") ? stringValue(source.group) : DEFAULT_OPTION,
    kind: stringValue(source.kind) || DEFAULT_OPTION,
    namespace: stringValue(source.namespace),
    name: stringValue(source.name),
    port: numberText(source.port),
    weight: numberText(source.weight),
    source,
  };
}

function valueMatchFromSpec(source: NativeObject): RouteValueMatchDraft {
  return {
    type: stringValue(source.type) || "Exact",
    name: stringValue(source.name),
    value: stringValue(source.value),
    source,
  };
}

function httpMatchFromSpec(source: NativeObject): HTTPRouteMatchDraft {
  const path = objectValue(source.path);
  return {
    pathType: path ? stringValue(path.type) || "PathPrefix" : DEFAULT_OPTION,
    pathValue: path ? stringValue(path.value) || "/" : "",
    method: stringValue(source.method) || DEFAULT_OPTION,
    headers: objectList(source.headers).map(valueMatchFromSpec),
    queryParams: objectList(source.queryParams).map(valueMatchFromSpec),
    source,
  };
}

function grpcMatchFromSpec(source: NativeObject): GRPCRouteMatchDraft {
  const method = objectValue(source.method);
  return {
    methodType: method ? stringValue(method.type) || "Exact" : DEFAULT_OPTION,
    service: method ? stringValue(method.service) : "",
    method: method ? stringValue(method.method) : "",
    headers: objectList(source.headers).map(valueMatchFromSpec),
    source,
  };
}

function ruleFromSpec(source: NativeObject): GatewayRouteRuleDraft {
  return {
    name: stringValue(source.name),
    backends: objectList(source.backendRefs).map(backendFromSpec),
    httpMatches: objectList(source.matches).map(httpMatchFromSpec),
    grpcMatches: objectList(source.matches).map(grpcMatchFromSpec),
    source,
  };
}

export function gatewayRouteDraftProblem(
  draft: GatewayRouteDraft,
  resource: KubernetesNetworkingResource,
): string | null {
  if (draft.parents.length > MAX_PARENTS) {
    return `父级引用不能超过 ${MAX_PARENTS} 个。`;
  }
  for (const [index, parent] of draft.parents.entries()) {
    const problem = referenceProblem(parent, false);
    if (problem) return `第 ${index + 1} 个父级引用${problem}`;
    if (parent.sectionName.trim() && !validSectionName(parent.sectionName.trim())) {
      return `第 ${index + 1} 个父级引用的监听器/Section 名称不合法。`;
    }
  }

  const hostnames = splitCommaList(draft.hostnames);
  if (hostnames.length > MAX_HOSTNAMES) {
    return `主机名不能超过 ${MAX_HOSTNAMES} 个。`;
  }
  const invalidHostname = hostnames.find((hostname) => !validHostname(hostname));
  if (invalidHostname) return `主机名 ${invalidHostname} 不合法。`;
  if (resource === "tlsroutes" && hostnames.length === 0) {
    return "TLSRoute 至少需要一个 SNI 主机名。";
  }

  if (draft.rules.length === 0) return "至少需要一条路由规则。";
  const maxRules = resource === "tlsroutes" ? 1 : 16;
  if (draft.rules.length > maxRules)
    return `${routeKind(resource)} 的路由规则不能超过 ${maxRules} 条。`;
  let backendCount = 0;
  const ruleNames = new Set<string>();
  for (const [ruleIndex, rule] of draft.rules.entries()) {
    const position = `第 ${ruleIndex + 1} 条规则`;
    const name = rule.name.trim();
    if (name && !validSectionName(name)) return `${position}的名称不合法。`;
    if (name && ruleNames.has(name)) return `规则名称 ${name} 重复。`;
    if (name) ruleNames.add(name);
    const maxBackends = MAX_BACKENDS_PER_RULE;
    if (rule.backends.length > maxBackends) {
      return `${position}的后端不能超过 ${maxBackends} 个。`;
    }
    backendCount += rule.backends.length;
    if (backendCount > MAX_BACKENDS) return `全部规则的后端合计不能超过 ${MAX_BACKENDS} 个。`;
    const filters = objectList(rule.source.filters);
    if (rule.backends.length === 0 && filters.length === 0) {
      return `${position}至少需要一个后端；仅当已有过滤器能够直接响应时才可省略后端。`;
    }
    for (const [backendIndex, backend] of rule.backends.entries()) {
      const problem = referenceProblem(backend, true);
      if (problem) return `${position}的第 ${backendIndex + 1} 个后端${problem}`;
    }
    if (resource === "httproutes") {
      const problem = httpMatchesProblem(rule.httpMatches, position);
      if (problem) return problem;
    }
    if (resource === "grpcroutes") {
      const problem = grpcMatchesProblem(rule.grpcMatches, position);
      if (problem) return problem;
    }
  }
  return null;
}

function referenceProblem(
  reference: RouteParentDraft | RouteBackendDraft,
  backend: boolean,
): string | null {
  const name = reference.name.trim();
  if (!DNS_SUBDOMAIN.test(name) || name.length > 253) return "的资源名称不合法。";
  const namespace = reference.namespace.trim();
  if (namespace && (!DNS_LABEL.test(namespace) || namespace.length > 63)) {
    return "的命名空间不合法。";
  }
  const group = reference.group === DEFAULT_OPTION ? "" : reference.group.trim();
  if (group && (!DNS_SUBDOMAIN.test(group) || group.length > 253)) return "的 API Group 不合法。";
  const kind = reference.kind === DEFAULT_OPTION ? "" : reference.kind.trim();
  if (kind && !qualifiedName(kind)) return "的 Kind 不合法。";
  if (reference.port.trim() && !validPortNumber(reference.port)) {
    return `的端口必须是 ${PORT_RANGE}。`;
  }
  if (backend) {
    const backendReference = reference as RouteBackendDraft;
    if (backendReference.weight.trim()) {
      const weight = Number(backendReference.weight);
      if (!/^\d+$/.test(backendReference.weight.trim()) || weight > 1_000_000) {
        return "的权重必须是 0–1000000 的整数。";
      }
    }
    const service = (kind || "Service") === "Service" && group === "";
    if (service && !reference.port.trim()) return "引用 Service 时必须填写端口。";
  }
  return null;
}

function httpMatchesProblem(matches: HTTPRouteMatchDraft[], position: string): string | null {
  if (matches.length > MAX_MATCHES) return `${position}的匹配条件不能超过 ${MAX_MATCHES} 个。`;
  for (const [index, match] of matches.entries()) {
    const where = `${position}的第 ${index + 1} 个匹配条件`;
    if (match.pathType !== DEFAULT_OPTION) {
      const value = match.pathValue.trim();
      if (!value) return `${where}的路径不能为空。`;
      if (value.length > 1024) return `${where}的路径不能超过 1024 个字符。`;
      if (match.pathType !== "RegularExpression" && !value.startsWith("/")) {
        return `${where}的路径必须以 / 开头。`;
      }
    }
    const problem = valueMatchesProblem(match.headers, `${where}的 Header`, 4096);
    if (problem) return problem;
    const queryProblem = valueMatchesProblem(match.queryParams, `${where}的查询参数`, 1024);
    if (queryProblem) return queryProblem;
  }
  return null;
}

function grpcMatchesProblem(matches: GRPCRouteMatchDraft[], position: string): string | null {
  if (matches.length > MAX_MATCHES) return `${position}的匹配条件不能超过 ${MAX_MATCHES} 个。`;
  for (const [index, match] of matches.entries()) {
    const where = `${position}的第 ${index + 1} 个匹配条件`;
    if (match.methodType !== DEFAULT_OPTION && !match.service.trim() && !match.method.trim()) {
      return `${where}选择了方法匹配方式，但 Service 与 Method 至少填写一个。`;
    }
    if (match.service.length > 1024 || match.method.length > 1024) {
      return `${where}的 Service 与 Method 不能超过 1024 个字符。`;
    }
    if (match.methodType === "Exact" && match.service && !GRPC_SERVICE.test(match.service)) {
      return `${where}的 gRPC Service 名称不合法。`;
    }
    if (match.methodType === "Exact" && match.method && !GRPC_METHOD.test(match.method)) {
      return `${where}的 gRPC Method 名称不合法。`;
    }
    const problem = valueMatchesProblem(match.headers, `${where}的 Header`, 4096);
    if (problem) return problem;
  }
  return null;
}

function valueMatchesProblem(
  matches: RouteValueMatchDraft[],
  label: string,
  maxValueLength: number,
): string | null {
  if (matches.length > MAX_MATCH_FIELDS) return `${label}不能超过 ${MAX_MATCH_FIELDS} 个。`;
  const names = new Set<string>();
  for (const [index, match] of matches.entries()) {
    const name = match.name.trim();
    if (!name) return `${label}第 ${index + 1} 项缺少名称。`;
    if (name.length > 256 || !HEADER_NAME.test(name)) return `${label}名称 ${name} 不合法。`;
    if (!match.value) return `${label} ${name} 缺少匹配值。`;
    if (match.value.length > maxValueLength) {
      return `${label} ${name} 的匹配值不能超过 ${maxValueLength} 个字符。`;
    }
    const normalized = name.toLowerCase();
    if (names.has(normalized)) return `${label}名称 ${name} 重复。`;
    names.add(normalized);
  }
  return null;
}

export function buildGatewayRouteSpec(
  draft: GatewayRouteDraft,
  resource: KubernetesNetworkingResource,
): NativeObject {
  const spec = { ...draft.source };
  setList(spec, "parentRefs", draft.parents.map(buildParent));
  if (resource === "httproutes" || resource === "grpcroutes" || resource === "tlsroutes") {
    setList(spec, "hostnames", splitCommaList(draft.hostnames));
  } else {
    delete spec.hostnames;
  }
  spec.rules = draft.rules.map((rule) => buildRule(rule, resource));
  return spec;
}

function buildParent(draft: RouteParentDraft): NativeObject {
  const result = { ...draft.source };
  if (draft.group === DEFAULT_OPTION) delete result.group;
  else result.group = draft.group.trim();
  setOptional(result, "kind", draft.kind === DEFAULT_OPTION ? "" : draft.kind.trim());
  setOptional(result, "namespace", draft.namespace.trim());
  result.name = draft.name.trim();
  setOptional(result, "sectionName", draft.sectionName.trim());
  setOptionalNumber(result, "port", draft.port);
  return result;
}

function buildBackend(draft: RouteBackendDraft): NativeObject {
  const result = { ...draft.source };
  if (draft.group === DEFAULT_OPTION) delete result.group;
  else result.group = draft.group.trim();
  setOptional(result, "kind", draft.kind === DEFAULT_OPTION ? "" : draft.kind.trim());
  setOptional(result, "namespace", draft.namespace.trim());
  result.name = draft.name.trim();
  setOptionalNumber(result, "port", draft.port);
  setOptionalNumber(result, "weight", draft.weight);
  return result;
}

function buildRule(
  draft: GatewayRouteRuleDraft,
  resource: KubernetesNetworkingResource,
): NativeObject {
  const result = { ...draft.source };
  setOptional(result, "name", draft.name.trim());
  setList(result, "backendRefs", draft.backends.map(buildBackend));
  if (resource === "httproutes") setList(result, "matches", draft.httpMatches.map(buildHTTPMatch));
  else if (resource === "grpcroutes")
    setList(result, "matches", draft.grpcMatches.map(buildGRPCMatch));
  else delete result.matches;
  return result;
}

function buildHTTPMatch(draft: HTTPRouteMatchDraft): NativeObject {
  const result = { ...draft.source };
  if (draft.pathType === DEFAULT_OPTION) delete result.path;
  else result.path = { type: draft.pathType, value: draft.pathValue.trim() };
  setOptional(result, "method", draft.method === DEFAULT_OPTION ? "" : draft.method);
  setList(result, "headers", draft.headers.map(buildValueMatch));
  setList(result, "queryParams", draft.queryParams.map(buildValueMatch));
  return result;
}

function buildGRPCMatch(draft: GRPCRouteMatchDraft): NativeObject {
  const result = { ...draft.source };
  if (draft.methodType === DEFAULT_OPTION) delete result.method;
  else {
    const method: NativeObject = { type: draft.methodType };
    setOptional(method, "service", draft.service.trim());
    setOptional(method, "method", draft.method.trim());
    result.method = method;
  }
  setList(result, "headers", draft.headers.map(buildValueMatch));
  return result;
}

function buildValueMatch(draft: RouteValueMatchDraft): NativeObject {
  return { ...draft.source, type: draft.type, name: draft.name.trim(), value: draft.value };
}

function setOptional(target: NativeObject, key: string, value: string) {
  if (value) target[key] = value;
  else delete target[key];
}

function setOptionalNumber(target: NativeObject, key: string, value: string) {
  const trimmed = value.trim();
  if (trimmed) target[key] = Number(trimmed);
  else delete target[key];
}

function setList(target: NativeObject, key: string, value: unknown[]) {
  if (value.length) target[key] = value;
  else delete target[key];
}

function objectValue(value: unknown): NativeObject | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as NativeObject)
    : null;
}

function objectList(value: unknown): NativeObject[] {
  return Array.isArray(value)
    ? value.map(objectValue).filter((entry): entry is NativeObject => entry !== null)
    : [];
}

function stringList(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((entry): entry is string => typeof entry === "string")
    : [];
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function numberText(value: unknown): string {
  return typeof value === "number" && Number.isFinite(value) ? String(value) : "";
}

function hasOwn(value: NativeObject, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(value, key);
}

function qualifiedName(value: string): boolean {
  const slash = value.indexOf("/");
  if (slash === -1) return QUALIFIED_NAME.test(value) && value.length <= 63;
  const prefix = value.slice(0, slash);
  const name = value.slice(slash + 1);
  return (
    DNS_SUBDOMAIN.test(prefix) &&
    prefix.length <= 253 &&
    QUALIFIED_NAME.test(name) &&
    name.length <= 63
  );
}

function validSectionName(value: string): boolean {
  return value.length <= 253 && DNS_SUBDOMAIN.test(value);
}

function routeKind(resource: KubernetesNetworkingResource): string {
  const names: Partial<Record<KubernetesNetworkingResource, string>> = {
    httproutes: "HTTPRoute",
    grpcroutes: "GRPCRoute",
    tlsroutes: "TLSRoute",
    tcproutes: "TCPRoute",
    udproutes: "UDPRoute",
  };
  return names[resource] ?? "Route";
}

function validPortNumber(value: string): boolean {
  const trimmed = value.trim();
  return /^\d+$/.test(trimmed) && Number(trimmed) >= 1 && Number(trimmed) <= 65535;
}

function validHostname(value: string): boolean {
  const hostname = value.trim();
  if (!hostname || hostname.length > 253) return hostname === "";
  return hostname.startsWith("*.")
    ? DNS_SUBDOMAIN.test(hostname.slice(2))
    : DNS_SUBDOMAIN.test(hostname);
}

function splitCommaList(value: string): string[] {
  return value
    .split(",")
    .map((entry) => entry.trim())
    .filter(Boolean);
}
