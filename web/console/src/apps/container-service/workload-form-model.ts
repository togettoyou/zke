import type { KubernetesCreateWorkloadRequest, KubernetesWorkloadResource } from "@/api/types";
import type { WorkloadCreateSpec } from "@/api/queries/workloads";

/*
 * The drafts the create form edits, and the one function that turns them into a
 * request body.
 *
 * Every rule below is enforced again by the Server, which stays the authority;
 * these exist so the form can say what is wrong without a round trip. The
 * mapping is the more interesting half: the Server rejects a field its type does
 * not accept rather than ignoring it, so "not set" has to mean absent from the
 * body — never an empty string, and never a zero standing in for one.
 */

const DNS_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const DNS_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;
/** A Service name is an RFC 1035 label: it has to start with a letter. */
const DNS_1035_LABEL = /^[a-z]([-a-z0-9]*[a-z0-9])?$/;
const LABEL_NAME = /^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/;
const LABEL_VALUE = /^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$/;
const ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;
/** The key of one entry inside a ConfigMap or a Secret. */
const OBJECT_KEY = /^[-._A-Za-z0-9]+$/;
const DECIMAL = /^\d+\.?\d*$|^\.\d+$/;
const INTEGER = /^\d+$/;
/** The shorthands Kubernetes' Cron parser accepts instead of five fields. */
const CRON_DESCRIPTOR = /^@(yearly|annually|monthly|weekly|daily|midnight|hourly)$/;
const CRON_EVERY = /^@every\s+\S+$/;
const CRON_FIELD = /^[-*?,/\dA-Za-z]+$/;

/** The Server owns these keys and refuses a request that carries them. */
export const RESERVED_LABEL_KEY = "zke.io/workload-id";
export const RESERVED_ANNOTATION_KEY = "zke.io/description";

const INT32_MAX = 2_147_483_647;
const MAX_CONTAINERS = 100;
const MAX_ENV_VARS = 100;
const MAX_VOLUMES = 50;
const MAX_VOLUME_MOUNTS = 50;
const MAX_TOLERATIONS = 50;
const MAX_NODE_SELECTORS = 50;
const MAX_IMAGE_PULL_SECRETS = 20;
const MAX_COMMAND_ENTRIES = 100;
const MAX_IMAGE_BYTES = 2048;
const MAX_PATH_BYTES = 4096;
/** The cap the Server puts on one env value and one annotation value. */
const MAX_VALUE_BYTES = 32 * 1024;
const MAX_DNS_LABEL_LENGTH = 63;
const MAX_LABEL_VALUE_LENGTH = 63;
const MAX_SUBDOMAIN_LENGTH = 253;
const MAX_PORT_NAME_LENGTH = 15;
const MAX_CRON_SCHEDULE_BYTES = 512;
const MAX_CRON_TIME_ZONE_BYTES = 128;
/** Counted in characters, the way the API contract states it. */
export const MAX_DESCRIPTION_LENGTH = 1000;
/** Kubernetes caps a CronJob's name to leave room for the Job names it derives. */
const MAX_CRON_JOB_NAME_LENGTH = 52;

/** Radix Select cannot hold an empty value, so "unset" needs a name of its own. */
export const DEFAULT_OPTION = "__default__";

export type KeyValueDraft = { key: string; value: string };

export type EnvSource = "value" | "config_map" | "secret";

export type EnvDraft = {
  name: string;
  source: EnvSource;
  value: string;
  refName: string;
  refKey: string;
};

export type VolumeMountDraft = {
  name: string;
  mountPath: string;
  subPath: string;
  readOnly: boolean;
};

export type ProbeKind = "http_get" | "tcp_socket" | "exec";

export type ProbeDraft = {
  enabled: boolean;
  kind: ProbeKind;
  path: string;
  port: string;
  scheme: string;
  command: string;
  initialDelaySeconds: string;
  periodSeconds: string;
  timeoutSeconds: string;
  successThreshold: string;
  failureThreshold: string;
};

export type HookKind = "exec" | "http_get";

export type HookDraft = {
  enabled: boolean;
  kind: HookKind;
  command: string;
  path: string;
  port: string;
  scheme: string;
};

export type ContainerDraft = {
  /** Stable across reorders, so the tab strip keeps its identity. */
  id: string;
  name: string;
  image: string;
  tag: string;
  policy: string;
  /** An init container is the same template, run before the main ones. */
  init: boolean;
  privileged: boolean;
  workingDir: string;
  command: string[];
  args: string[];
  env: EnvDraft[];
  cpuRequest: string;
  cpuLimit: string;
  memoryRequest: string;
  memoryLimit: string;
  gpuLimit: string;
  mounts: VolumeMountDraft[];
  liveness: ProbeDraft;
  readiness: ProbeDraft;
  postStart: HookDraft;
  preStop: HookDraft;
};

export type VolumeKind =
  "empty_dir" | "host_path" | "config_map" | "secret" | "persistent_volume_claim" | "nfs";

export type VolumeDraft = {
  id: string;
  name: string;
  kind: VolumeKind;
  medium: string;
  sizeLimit: string;
  hostPath: string;
  hostPathType: string;
  /** ConfigMap name, Secret name or PersistentVolumeClaim name, by kind. */
  refName: string;
  optional: boolean;
  readOnly: boolean;
  nfsServer: string;
  nfsPath: string;
};

export type TolerationDraft = {
  key: string;
  operator: string;
  value: string;
  effect: string;
  tolerationSeconds: string;
};

export type WorkloadFormDraft = {
  name: string;
  description: string;
  labels: KeyValueDraft[];
  annotations: KeyValueDraft[];
  containers: ContainerDraft[];
  volumes: VolumeDraft[];
  imagePullSecrets: string[];
  nodeSelector: KeyValueDraft[];
  tolerations: TolerationDraft[];

  replicas: string;
  serviceName: string;

  parallelism: string;
  completions: string;
  backoffLimit: string;
  ttlSeconds: string;

  schedule: string;
  timeZone: string;
  concurrencyPolicy: string;
  startingDeadline: string;
  successfulHistory: string;
  failedHistory: string;
  suspend: boolean;
};

let sequence = 0;

function nextId(prefix: string): string {
  sequence += 1;
  return `${prefix}-${sequence}`;
}

export function emptyProbe(): ProbeDraft {
  return {
    enabled: false,
    kind: "http_get",
    path: "/",
    port: "",
    scheme: DEFAULT_OPTION,
    command: "",
    initialDelaySeconds: "",
    periodSeconds: "",
    timeoutSeconds: "",
    successThreshold: "",
    failureThreshold: "",
  };
}

export function emptyHook(): HookDraft {
  return { enabled: false, kind: "exec", command: "", path: "/", port: "", scheme: DEFAULT_OPTION };
}

export function emptyContainer(index: number): ContainerDraft {
  return {
    id: nextId("container"),
    name: `container-${index}`,
    image: "",
    tag: "",
    policy: DEFAULT_OPTION,
    init: false,
    privileged: false,
    workingDir: "",
    command: [],
    args: [],
    env: [],
    cpuRequest: "",
    cpuLimit: "",
    memoryRequest: "",
    memoryLimit: "",
    gpuLimit: "",
    mounts: [],
    liveness: emptyProbe(),
    readiness: emptyProbe(),
    postStart: emptyHook(),
    preStop: emptyHook(),
  };
}

export function emptyVolume(): VolumeDraft {
  return {
    id: nextId("volume"),
    name: "",
    kind: "empty_dir",
    medium: DEFAULT_OPTION,
    sizeLimit: "",
    hostPath: "",
    hostPathType: DEFAULT_OPTION,
    refName: "",
    optional: false,
    readOnly: false,
    nfsServer: "",
    nfsPath: "",
  };
}

export function emptyToleration(): TolerationDraft {
  return { key: "", operator: "Equal", value: "", effect: DEFAULT_OPTION, tolerationSeconds: "" };
}

export function emptyDraft(): WorkloadFormDraft {
  return {
    name: "",
    description: "",
    labels: [],
    annotations: [],
    containers: [emptyContainer(1)],
    volumes: [],
    imagePullSecrets: [],
    nodeSelector: [],
    tolerations: [],
    replicas: "",
    serviceName: "",
    parallelism: "",
    completions: "",
    backoffLimit: "",
    ttlSeconds: "",
    schedule: "",
    timeZone: "",
    concurrencyPolicy: DEFAULT_OPTION,
    startingDeadline: "",
    successfulHistory: "",
    failedHistory: "",
    suspend: false,
  };
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

/** The name length Kubernetes allows a workload of this type. */
export function nameLimit(resource: KubernetesWorkloadResource): number {
  switch (resource) {
    // The controller derives Job names from it and needs the remaining room.
    case "cronjobs":
      return MAX_CRON_JOB_NAME_LENGTH;
    // The name is copied onto every Pod as the `job-name` label value.
    case "jobs":
      return MAX_LABEL_VALUE_LENGTH;
    default:
      return MAX_SUBDOMAIN_LENGTH;
  }
}

/**
 * The section of the form a problem belongs to.
 *
 * A message about the name is no use at the bottom of a form that is ten screens
 * long, so a problem travels with the section that can fix it and is rendered
 * there.
 */
export type FormSectionKey =
  | "basic"
  | "labels"
  | "annotations"
  | "volumes"
  | "containers"
  | "job"
  | "schedule"
  | "imagePullSecrets"
  | "nodeSelector"
  | "tolerations";

export type DraftProblem = { section: FormSectionKey; message: string };

/** The reason the form cannot be submitted, and where it is, or null when it can. */
export function draftProblem(
  draft: WorkloadFormDraft,
  resource: KubernetesWorkloadResource,
): DraftProblem | null {
  const name = draft.name.trim();
  if (!DNS_SUBDOMAIN.test(name) || name.length > nameLimit(resource)) {
    return at("basic", `名称必须是合法的 DNS 子域名，最长 ${nameLimit(resource)} 个字符。`);
  }
  // Counted in characters rather than UTF-16 units, so one emoji is one of them.
  if ([...draft.description].length > MAX_DESCRIPTION_LENGTH) {
    return at("basic", `描述不能超过 ${MAX_DESCRIPTION_LENGTH} 个字符。`);
  }
  if (resource === "statefulsets") {
    const service = draft.serviceName.trim();
    if (!DNS_1035_LABEL.test(service) || service.length > MAX_DNS_LABEL_LENGTH) {
      return at("basic", "StatefulSet 需要一个同命名空间中已存在的 Service 名称，以字母开头。");
    }
  }

  const labelProblem = keyValueProblem(draft.labels, {
    label: "标签",
    reservedKey: RESERVED_LABEL_KEY,
    values: "label",
  });
  if (labelProblem) {
    return at("labels", labelProblem);
  }
  const annotationProblem = keyValueProblem(draft.annotations, {
    label: "注解",
    reservedKey: RESERVED_ANNOTATION_KEY,
    values: "text",
  });
  if (annotationProblem) {
    return at("annotations", annotationProblem);
  }

  const volumeProblem = volumesProblem(draft.volumes);
  if (volumeProblem) {
    return at("volumes", volumeProblem);
  }

  const containerProblem = containersProblem(draft.containers, draft.volumes);
  if (containerProblem) {
    return at("containers", containerProblem);
  }

  // Only the counts this type accepts: the others are not in the request, so a
  // stale value behind a hidden field must not block the form either.
  const counts: [section: FormSectionKey, label: string, value: string][] = [];
  if (resource === "deployments" || resource === "statefulsets") {
    counts.push(["basic", "实例数量", draft.replicas]);
  }
  if (resource === "jobs" || resource === "cronjobs") {
    counts.push(
      ["job", "并行度", draft.parallelism],
      ["job", "完成数", draft.completions],
      ["job", "失败重试上限", draft.backoffLimit],
      ["job", "完成后保留秒数", draft.ttlSeconds],
    );
  }
  if (resource === "cronjobs") {
    counts.push(
      ["schedule", "启动截止秒数", draft.startingDeadline],
      ["schedule", "成功历史保留", draft.successfulHistory],
      ["schedule", "失败历史保留", draft.failedHistory],
    );
  }
  for (const [section, label, value] of counts) {
    if (!optionalCount(value)) {
      return at(section, `${label}必须是 0 到 ${INT32_MAX} 之间的整数。`);
    }
  }

  if (resource === "cronjobs") {
    const schedule = draft.schedule.trim();
    if (schedule === "" || byteLength(schedule) > MAX_CRON_SCHEDULE_BYTES) {
      return at("schedule", "CronJob 需要一个 Cron 表达式。");
    }
    if (!validSchedule(schedule)) {
      return at("schedule", "Cron 表达式必须是五段式（分 时 日 月 周），或 @daily 这样的简写。");
    }
    const timeZone = draft.timeZone.trim();
    if (byteLength(timeZone) > MAX_CRON_TIME_ZONE_BYTES) {
      return at("schedule", "时区名称过长。");
    }
    if (timeZone !== "" && /\s/.test(timeZone)) {
      return at("schedule", "时区必须是 IANA 时区名称，例如 Asia/Shanghai。");
    }
  }

  const secrets = draft.imagePullSecrets
    .map((secret) => secret.trim())
    .filter((secret) => secret !== "");
  if (secrets.length > MAX_IMAGE_PULL_SECRETS) {
    return at("imagePullSecrets", `镜像访问凭证不能超过 ${MAX_IMAGE_PULL_SECRETS} 个。`);
  }
  for (const secret of secrets) {
    if (!DNS_SUBDOMAIN.test(secret) || secret.length > MAX_SUBDOMAIN_LENGTH) {
      return at("imagePullSecrets", `镜像访问凭证 ${secret} 不是合法的 Secret 名称。`);
    }
  }

  const selectorProblem = keyValueProblem(draft.nodeSelector, {
    label: "节点标签",
    values: "label",
    max: MAX_NODE_SELECTORS,
  });
  if (selectorProblem) {
    return at("nodeSelector", selectorProblem);
  }

  const tolerationProblem = tolerationsProblem(draft.tolerations);
  if (tolerationProblem) {
    return at("tolerations", tolerationProblem);
  }
  return null;
}

function at(section: FormSectionKey, message: string): DraftProblem {
  return { section, message };
}

/**
 * The first problem of each container, keyed by container id.
 *
 * One container is visible at a time, so a problem in a container that is not
 * the open one has to be findable from the tab strip rather than only from the
 * message at the bottom of the form.
 */
export function containerProblems(
  containers: ContainerDraft[],
  volumes: VolumeDraft[],
): Map<string, string> {
  const volumeNames = new Set(
    volumes.map((volume) => volume.name.trim()).filter((name) => name !== ""),
  );
  const names = new Set<string>();
  const problems = new Map<string, string>();
  for (const container of containers) {
    const name = container.name.trim();
    let problem = containerProblem(container, volumeNames);
    if (!problem && names.has(name)) {
      problem = `容器名称 ${name} 重复。`;
    }
    names.add(name);
    if (problem) {
      problems.set(container.id, problem);
    }
  }
  return problems;
}

type KeyValueRules = {
  label: string;
  /** A key the Server owns and refuses to accept from a request. */
  reservedKey?: string;
  /** Label values are constrained; annotation values are only bounded in size. */
  values: "label" | "text";
  max?: number;
};

function keyValueProblem(rows: KeyValueDraft[], rules: KeyValueRules): string | null {
  const keys = new Set<string>();
  for (const row of rows) {
    const key = row.key.trim();
    if (key === "") {
      continue;
    }
    if (key === rules.reservedKey) {
      return `${key} 是 ZKE 保留键，不能在此设置。`;
    }
    if (!qualifiedName(key)) {
      return `${rules.label}键 ${key} 不是合法的 Kubernetes 键名。`;
    }
    if (rules.values === "label") {
      const value = row.value.trim();
      if (!LABEL_VALUE.test(value) || value.length > MAX_LABEL_VALUE_LENGTH) {
        return `${rules.label} ${key} 的值必须是最长 ${MAX_LABEL_VALUE_LENGTH} 个字符的字母、数字、-、_ 或 .。`;
      }
    } else if (byteLength(row.value) > MAX_VALUE_BYTES) {
      return `${rules.label} ${key} 的值过长。`;
    }
    if (keys.has(key)) {
      return `${rules.label}键 ${key} 重复。`;
    }
    keys.add(key);
  }
  if (rules.max !== undefined && keys.size > rules.max) {
    return `${rules.label}不能超过 ${rules.max} 项。`;
  }
  return null;
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

function volumesProblem(volumes: VolumeDraft[]): string | null {
  if (volumes.length > MAX_VOLUMES) {
    return `数据卷不能超过 ${MAX_VOLUMES} 个。`;
  }
  const names = new Set<string>();
  for (const volume of volumes) {
    const name = volume.name.trim();
    if (!DNS_LABEL.test(name) || name.length > MAX_DNS_LABEL_LENGTH) {
      return "数据卷名称必须是合法的 DNS label。";
    }
    if (names.has(name)) {
      return `数据卷名称 ${name} 重复。`;
    }
    names.add(name);
    const problem = volumeSourceProblem(volume, name);
    if (problem) {
      return problem;
    }
  }
  return null;
}

function volumeSourceProblem(volume: VolumeDraft, name: string): string | null {
  switch (volume.kind) {
    case "empty_dir": {
      const sizeLimit = volume.sizeLimit.trim();
      if (sizeLimit !== "" && (!DECIMAL.test(sizeLimit) || Number(sizeLimit) <= 0)) {
        return `数据卷 ${name} 的容量上限必须是大于 0 的数字。`;
      }
      return null;
    }
    case "host_path":
      return absolutePath(volume.hostPath.trim())
        ? null
        : `数据卷 ${name} 的主机路径必须是绝对路径，最长 ${MAX_PATH_BYTES} 字节。`;
    case "nfs": {
      const server = volume.nfsServer.trim();
      if (server === "" || /\s/.test(server) || byteLength(server) > MAX_PATH_BYTES) {
        return `数据卷 ${name} 需要一个 NFS 服务器地址。`;
      }
      return absolutePath(volume.nfsPath.trim())
        ? null
        : `数据卷 ${name} 的 NFS 路径必须是绝对路径。`;
    }
    default: {
      const refName = volume.refName.trim();
      return DNS_SUBDOMAIN.test(refName) && refName.length <= MAX_SUBDOMAIN_LENGTH
        ? null
        : `数据卷 ${name} 需要一个合法的对象名称。`;
    }
  }
}

function containersProblem(containers: ContainerDraft[], volumes: VolumeDraft[]): string | null {
  const main = containers.filter((container) => !container.init);
  if (main.length === 0) {
    return "至少需要一个非初始化容器。";
  }
  if (main.length > MAX_CONTAINERS || containers.length - main.length > MAX_CONTAINERS) {
    return `主容器和初始化容器各自不能超过 ${MAX_CONTAINERS} 个。`;
  }
  const problems = containerProblems(containers, volumes);
  for (const container of containers) {
    const problem = problems.get(container.id);
    if (problem) {
      return problem;
    }
  }
  return null;
}

function containerProblem(container: ContainerDraft, volumeNames: Set<string>): string | null {
  const name = container.name.trim();
  if (!DNS_LABEL.test(name) || name.length > MAX_DNS_LABEL_LENGTH) {
    return "容器名称必须是合法的 DNS label。";
  }

  const image = container.image.trim();
  if (image === "" || /\s/.test(image) || byteLength(image) > MAX_IMAGE_BYTES) {
    return `容器 ${name} 需要一个镜像地址。`;
  }
  if (/\s|:/.test(container.tag.trim())) {
    return `容器 ${name} 的镜像版本不合法。`;
  }
  if (byteLength(containerImage(container)) > MAX_IMAGE_BYTES) {
    return `容器 ${name} 的镜像地址过长。`;
  }

  const workingDir = container.workingDir.trim();
  if (byteLength(workingDir) > MAX_PATH_BYTES) {
    return `容器 ${name} 的工作目录过长。`;
  }

  for (const [label, lines] of [
    ["运行命令", container.command],
    ["运行参数", container.args],
  ] as [string, string[]][]) {
    const problem = commandProblem(commandLines(lines), `容器 ${name} 的${label}`);
    if (problem) {
      return problem;
    }
  }

  const envProblem = containerEnvProblem(container, name);
  if (envProblem) {
    return envProblem;
  }

  const resourceProblem = containerResourceProblem(container, name);
  if (resourceProblem) {
    return resourceProblem;
  }

  const mountProblem = containerMountProblem(container, name, volumeNames);
  if (mountProblem) {
    return mountProblem;
  }

  // Kubernetes runs an init container to completion before the Pod starts, so it
  // has nothing to probe and no lifecycle to hook into, and rejects both.
  if (container.init) {
    if (container.liveness.enabled || container.readiness.enabled) {
      return `初始化容器 ${name} 不能配置存活或就绪检查。`;
    }
    if (container.postStart.enabled || container.preStop.enabled) {
      return `初始化容器 ${name} 不能配置生命周期钩子。`;
    }
  }
  for (const [label, probe] of [
    ["存活检查", container.liveness],
    ["就绪检查", container.readiness],
  ] as [string, ProbeDraft][]) {
    const problem = probeProblem(probe, `容器 ${name} 的${label}`);
    if (problem) {
      return problem;
    }
  }
  for (const [label, hook] of [
    ["启动后执行", container.postStart],
    ["结束前执行", container.preStop],
  ] as [string, HookDraft][]) {
    const problem = hookProblem(hook, `容器 ${name} 的${label}`);
    if (problem) {
      return problem;
    }
  }
  return null;
}

function containerEnvProblem(container: ContainerDraft, name: string): string | null {
  if (container.env.length > MAX_ENV_VARS) {
    return `容器 ${name} 的环境变量不能超过 ${MAX_ENV_VARS} 个。`;
  }
  const envNames = new Set<string>();
  for (const variable of container.env) {
    const envName = variable.name.trim();
    if (!ENV_NAME.test(envName)) {
      return `容器 ${name} 的环境变量名 ${envName || "(空)"} 不合法。`;
    }
    if (envNames.has(envName)) {
      return `容器 ${name} 的环境变量 ${envName} 重复。`;
    }
    envNames.add(envName);
    if (variable.source === "value") {
      if (byteLength(variable.value) > MAX_VALUE_BYTES) {
        return `容器 ${name} 的环境变量 ${envName} 的值过长。`;
      }
      continue;
    }
    const refName = variable.refName.trim();
    if (!DNS_SUBDOMAIN.test(refName) || refName.length > MAX_SUBDOMAIN_LENGTH) {
      return `容器 ${name} 的环境变量 ${envName} 需要一个合法的对象名称。`;
    }
    const refKey = variable.refKey.trim();
    if (!OBJECT_KEY.test(refKey) || refKey === "." || refKey === "..") {
      return `容器 ${name} 的环境变量 ${envName} 需要一个合法的键：字母、数字、-、_ 或 .。`;
    }
  }
  return null;
}

function containerResourceProblem(container: ContainerDraft, name: string): string | null {
  for (const value of [
    container.cpuRequest,
    container.cpuLimit,
    container.memoryRequest,
    container.memoryLimit,
  ]) {
    if (value.trim() !== "" && !DECIMAL.test(value.trim())) {
      return `容器 ${name} 的资源限制必须是数字。`;
    }
  }
  // Kubernetes refuses a container that asks for more than it may use.
  if (exceedsLimit(container.cpuRequest, container.cpuLimit)) {
    return `容器 ${name} 的 CPU request 不能大于 limit。`;
  }
  if (exceedsLimit(container.memoryRequest, container.memoryLimit)) {
    return `容器 ${name} 的内存 request 不能大于 limit。`;
  }
  const gpu = container.gpuLimit.trim();
  if (gpu !== "" && (!INTEGER.test(gpu) || Number(gpu) > INT32_MAX)) {
    return `容器 ${name} 的 GPU 卡数必须是整数。`;
  }
  return null;
}

function containerMountProblem(
  container: ContainerDraft,
  name: string,
  volumeNames: Set<string>,
): string | null {
  if (container.mounts.length > MAX_VOLUME_MOUNTS) {
    return `容器 ${name} 的数据卷挂载不能超过 ${MAX_VOLUME_MOUNTS} 个。`;
  }
  const paths = new Set<string>();
  for (const mount of container.mounts) {
    const mountName = mount.name.trim();
    if (!volumeNames.has(mountName)) {
      return `容器 ${name} 挂载了未声明的数据卷 ${mountName || "(空)"}。`;
    }
    const mountPath = mount.mountPath.trim();
    if (!absolutePath(mountPath)) {
      return `容器 ${name} 的挂载路径必须是绝对路径，最长 ${MAX_PATH_BYTES} 字节。`;
    }
    // Kubernetes reads a colon in a mount path as a separator and rejects it.
    if (mountPath.includes(":")) {
      return `容器 ${name} 的挂载路径不能包含冒号。`;
    }
    if (paths.has(mountPath)) {
      return `容器 ${name} 在 ${mountPath} 上挂载了多个数据卷。`;
    }
    paths.add(mountPath);

    const subPath = mount.subPath.trim();
    if (byteLength(subPath) > MAX_PATH_BYTES) {
      return `容器 ${name} 的子路径过长。`;
    }
    // A subPath selects inside the volume it is mounted from; leaving it is what
    // an absolute path or a `..` segment would do.
    if (subPath.startsWith("/")) {
      return `容器 ${name} 的子路径不能是绝对路径。`;
    }
    if (subPath.split("/").includes("..")) {
      return `容器 ${name} 的子路径不能包含 ..。`;
    }
  }
  return null;
}

function probeProblem(probe: ProbeDraft, subject: string): string | null {
  if (!probe.enabled) {
    return null;
  }
  if (probe.kind === "exec") {
    const command = splitCommand(probe.command);
    if (command.length === 0) {
      return `${subject}需要一条执行命令。`;
    }
    const problem = commandProblem(command, subject);
    if (problem) {
      return problem;
    }
  } else {
    if (!validPort(probe.port.trim())) {
      return `${subject}需要一个合法端口：1-65535，或一个已命名容器端口。`;
    }
    if (probe.kind === "http_get" && byteLength(probe.path.trim()) > MAX_PATH_BYTES) {
      return `${subject}的路径过长。`;
    }
  }
  if (!optionalCount(probe.initialDelaySeconds)) {
    return `${subject}的初始延迟必须是非负整数。`;
  }
  for (const value of [
    probe.periodSeconds,
    probe.timeoutSeconds,
    probe.successThreshold,
    probe.failureThreshold,
  ]) {
    const trimmed = value.trim();
    if (
      trimmed !== "" &&
      (!INTEGER.test(trimmed) || Number(trimmed) < 1 || Number(trimmed) > INT32_MAX)
    ) {
      return `${subject}的间隔、超时和阈值必须大于 0。`;
    }
  }
  return null;
}

function hookProblem(hook: HookDraft, subject: string): string | null {
  if (!hook.enabled) {
    return null;
  }
  if (hook.kind === "exec") {
    const command = splitCommand(hook.command);
    if (command.length === 0) {
      return `${subject}需要一条执行命令。`;
    }
    return commandProblem(command, subject);
  }
  if (!validPort(hook.port.trim())) {
    return `${subject}需要一个合法端口：1-65535，或一个已命名容器端口。`;
  }
  return byteLength(hook.path.trim()) > MAX_PATH_BYTES ? `${subject}的路径过长。` : null;
}

function commandProblem(entries: string[], subject: string): string | null {
  if (entries.length > MAX_COMMAND_ENTRIES) {
    return `${subject}不能超过 ${MAX_COMMAND_ENTRIES} 条。`;
  }
  for (const entry of entries) {
    if (byteLength(entry) > MAX_VALUE_BYTES) {
      return `${subject}中有一条过长。`;
    }
  }
  return null;
}

function tolerationsProblem(tolerations: TolerationDraft[]): string | null {
  if (tolerations.length > MAX_TOLERATIONS) {
    return `容忍不能超过 ${MAX_TOLERATIONS} 条。`;
  }
  for (const toleration of tolerations) {
    const key = toleration.key.trim();
    if (key !== "" && !qualifiedName(key)) {
      return `容忍键 ${key} 不是合法的 Kubernetes 键名。`;
    }
    const value = toleration.value.trim();
    if (toleration.operator === "Exists") {
      if (value !== "") {
        return "Exists 匹配任意取值，不能同时填写值。";
      }
    } else {
      if (key === "") {
        return "留空的容忍键只在 Exists 下有效。";
      }
      if (!LABEL_VALUE.test(value) || value.length > MAX_LABEL_VALUE_LENGTH) {
        return `容忍键 ${key} 的值必须是最长 ${MAX_LABEL_VALUE_LENGTH} 个字符的字母、数字、-、_ 或 .。`;
      }
    }
    if (toleration.tolerationSeconds.trim() !== "") {
      if (toleration.effect !== "NoExecute") {
        return "只有 NoExecute 会驱逐 Pod，因此只有它接受容忍时长。";
      }
      if (!optionalCount(toleration.tolerationSeconds)) {
        return "容忍时长必须是非负整数。";
      }
    }
  }
  return null;
}

/** A port number in range, or the name of a container port. */
function validPort(value: string): boolean {
  if (INTEGER.test(value)) {
    const port = Number(value);
    return port > 0 && port <= 65535;
  }
  // A port name is a short DNS label that carries a letter and no `--`.
  return (
    DNS_LABEL.test(value) &&
    value.length <= MAX_PORT_NAME_LENGTH &&
    /[a-z]/.test(value) &&
    !value.includes("--")
  );
}

/** Five whitespace-separated fields, or one of Kubernetes' own shorthands. */
function validSchedule(value: string): boolean {
  if (CRON_DESCRIPTOR.test(value) || CRON_EVERY.test(value)) {
    return true;
  }
  const fields = value.split(/\s+/);
  return fields.length === 5 && fields.every((field) => CRON_FIELD.test(field));
}

function absolutePath(value: string): boolean {
  return value.startsWith("/") && byteLength(value) <= MAX_PATH_BYTES;
}

function exceedsLimit(request: string, limit: string): boolean {
  const requested = request.trim();
  const limited = limit.trim();
  return requested !== "" && limited !== "" && Number(requested) > Number(limited);
}

function optionalCount(value: string): boolean {
  const trimmed = value.trim();
  return trimmed === "" || (INTEGER.test(trimmed) && Number(trimmed) <= INT32_MAX);
}

function byteLength(value: string): number {
  return new TextEncoder().encode(value).length;
}

// ---------------------------------------------------------------------------
// Request mapping
// ---------------------------------------------------------------------------

type ContainerTemplate = KubernetesCreateWorkloadRequest["containers"][number];
type ProbeRequest = NonNullable<ContainerTemplate["liveness_probe"]>;
type VolumeRequest = NonNullable<KubernetesCreateWorkloadRequest["volumes"]>[number];

export function buildWorkloadSpec(
  draft: WorkloadFormDraft,
  resource: KubernetesWorkloadResource,
): WorkloadCreateSpec {
  const main = draft.containers.filter((container) => !container.init);
  const init = draft.containers.filter((container) => container.init);
  const spec: WorkloadCreateSpec = {
    name: draft.name.trim(),
    containers: main.map(containerTemplate),
  };
  if (init.length > 0) {
    spec.init_containers = init.map(containerTemplate);
  }
  const labels = keyValueRecord(draft.labels);
  if (labels) {
    spec.labels = labels;
  }
  const annotations = keyValueRecord(draft.annotations);
  if (annotations) {
    spec.annotations = annotations;
  }
  if (draft.description.trim() !== "") {
    spec.description = draft.description.trim();
  }
  if (draft.volumes.length > 0) {
    spec.volumes = draft.volumes.map(volumeRequest);
  }
  const pullSecrets = draft.imagePullSecrets
    .map((secret) => secret.trim())
    .filter((secret) => secret !== "");
  if (pullSecrets.length > 0) {
    spec.image_pull_secrets = pullSecrets;
  }
  const nodeSelector = keyValueRecord(draft.nodeSelector);
  if (nodeSelector) {
    spec.node_selector = nodeSelector;
  }
  if (draft.tolerations.length > 0) {
    spec.tolerations = draft.tolerations.map((toleration) => ({
      key: toleration.key.trim(),
      operator: toleration.operator as "Equal" | "Exists",
      ...(toleration.operator === "Exists" ? {} : { value: toleration.value.trim() }),
      ...(toleration.effect === DEFAULT_OPTION
        ? {}
        : { effect: toleration.effect as "NoSchedule" | "PreferNoSchedule" | "NoExecute" }),
      ...(toleration.tolerationSeconds.trim() === ""
        ? {}
        : { toleration_seconds: Number(toleration.tolerationSeconds.trim()) }),
    }));
  }

  const replicated = resource === "deployments" || resource === "statefulsets";
  const jobLike = resource === "jobs" || resource === "cronjobs";
  if (replicated) {
    assignCount(spec, "replicas", draft.replicas);
  }
  if (resource === "statefulsets") {
    spec.service_name = draft.serviceName.trim();
  }
  if (jobLike) {
    assignCount(spec, "parallelism", draft.parallelism);
    assignCount(spec, "completions", draft.completions);
    assignCount(spec, "backoff_limit", draft.backoffLimit);
    assignCount(spec, "ttl_seconds_after_finished", draft.ttlSeconds);
  }
  if (resource === "cronjobs") {
    spec.schedule = draft.schedule.trim();
    if (draft.timeZone.trim() !== "") {
      spec.time_zone = draft.timeZone.trim();
    }
    if (draft.concurrencyPolicy !== DEFAULT_OPTION) {
      spec.concurrency_policy = draft.concurrencyPolicy as "Allow" | "Forbid" | "Replace";
    }
    if (draft.suspend) {
      spec.suspend = true;
    }
    assignCount(spec, "starting_deadline_seconds", draft.startingDeadline);
    assignCount(spec, "successful_jobs_history_limit", draft.successfulHistory);
    assignCount(spec, "failed_jobs_history_limit", draft.failedHistory);
  }
  return spec;
}

/** The image as Kubernetes wants it: one string, tag included. */
export function containerImage(container: ContainerDraft): string {
  const image = container.image.trim();
  const tag = container.tag.trim();
  return tag === "" ? image : `${image}:${tag}`;
}

function containerTemplate(container: ContainerDraft): ContainerTemplate {
  const template: ContainerTemplate = {
    name: container.name.trim(),
    image: containerImage(container),
  };
  if (container.policy !== DEFAULT_OPTION) {
    template.image_pull_policy = container.policy as "Always" | "IfNotPresent" | "Never";
  }
  // An entry the operator left blank is a line in a textarea, not an argument:
  // sending it would override the image's ENTRYPOINT with an empty argv entry.
  const command = commandLines(container.command);
  if (command.length > 0) {
    template.command = command;
  }
  const args = commandLines(container.args);
  if (args.length > 0) {
    template.args = args;
  }
  if (container.workingDir.trim() !== "") {
    template.working_dir = container.workingDir.trim();
  }
  if (container.env.length > 0) {
    template.env = container.env.map((variable) => {
      const name = variable.name.trim();
      switch (variable.source) {
        case "config_map":
          return {
            name,
            config_map_key_ref: {
              name: variable.refName.trim(),
              key: variable.refKey.trim(),
            },
          };
        case "secret":
          return {
            name,
            secret_key_ref: {
              name: variable.refName.trim(),
              key: variable.refKey.trim(),
            },
          };
        default:
          return { name, value: variable.value };
      }
    });
  }
  const requests: Record<string, string> = {};
  const limits: Record<string, string> = {};
  assignQuantity(requests, "cpu", container.cpuRequest, "");
  assignQuantity(limits, "cpu", container.cpuLimit, "");
  assignQuantity(requests, "memory", container.memoryRequest, "Mi");
  assignQuantity(limits, "memory", container.memoryLimit, "Mi");
  // Extended resources are only ever set as a limit; Kubernetes derives the
  // matching request itself, and the two are not allowed to differ.
  assignQuantity(limits, "nvidia.com/gpu", container.gpuLimit, "");
  if (Object.keys(requests).length > 0 || Object.keys(limits).length > 0) {
    template.resources = {
      ...(Object.keys(requests).length > 0 ? { requests } : {}),
      ...(Object.keys(limits).length > 0 ? { limits } : {}),
    };
  }
  if (container.mounts.length > 0) {
    template.volume_mounts = container.mounts.map((mount) => ({
      name: mount.name.trim(),
      mount_path: mount.mountPath.trim(),
      ...(mount.subPath.trim() === "" ? {} : { sub_path: mount.subPath.trim() }),
      ...(mount.readOnly ? { read_only: true } : {}),
    }));
  }
  if (container.liveness.enabled) {
    template.liveness_probe = probeRequest(container.liveness);
  }
  if (container.readiness.enabled) {
    template.readiness_probe = probeRequest(container.readiness);
  }
  if (container.postStart.enabled || container.preStop.enabled) {
    template.lifecycle = {
      ...(container.postStart.enabled ? { post_start: hookRequest(container.postStart) } : {}),
      ...(container.preStop.enabled ? { pre_stop: hookRequest(container.preStop) } : {}),
    };
  }
  // Only ever true: `privileged: false` is the default said out loud, and it
  // would make an ordinary container look deliberately configured.
  if (container.privileged) {
    template.privileged = true;
  }
  return template;
}

function probeRequest(probe: ProbeDraft): ProbeRequest {
  const request: ProbeRequest = {};
  switch (probe.kind) {
    case "exec":
      request.exec = { command: splitCommand(probe.command) };
      break;
    case "tcp_socket":
      request.tcp_socket = { port: probe.port.trim() };
      break;
    default:
      request.http_get = {
        port: probe.port.trim(),
        ...(probe.path.trim() === "" ? {} : { path: probe.path.trim() }),
        ...(probe.scheme === DEFAULT_OPTION ? {} : { scheme: probe.scheme as "HTTP" | "HTTPS" }),
      };
  }
  assignCount(request, "initial_delay_seconds", probe.initialDelaySeconds);
  assignCount(request, "period_seconds", probe.periodSeconds);
  assignCount(request, "timeout_seconds", probe.timeoutSeconds);
  assignCount(request, "success_threshold", probe.successThreshold);
  assignCount(request, "failure_threshold", probe.failureThreshold);
  return request;
}

function hookRequest(hook: HookDraft) {
  if (hook.kind === "exec") {
    return { exec: { command: splitCommand(hook.command) } };
  }
  return {
    http_get: {
      port: hook.port.trim(),
      ...(hook.path.trim() === "" ? {} : { path: hook.path.trim() }),
      ...(hook.scheme === DEFAULT_OPTION ? {} : { scheme: hook.scheme as "HTTP" | "HTTPS" }),
    },
  };
}

function volumeRequest(volume: VolumeDraft): VolumeRequest {
  const name = volume.name.trim();
  switch (volume.kind) {
    case "host_path":
      return {
        name,
        host_path: {
          path: volume.hostPath.trim(),
          ...(volume.hostPathType === DEFAULT_OPTION
            ? {}
            : { type: volume.hostPathType as "Directory" }),
        },
      };
    case "config_map":
      return {
        name,
        config_map: {
          name: volume.refName.trim(),
          ...(volume.optional ? { optional: true } : {}),
        },
      };
    case "secret":
      return {
        name,
        secret: {
          secret_name: volume.refName.trim(),
          ...(volume.optional ? { optional: true } : {}),
        },
      };
    case "persistent_volume_claim":
      return {
        name,
        persistent_volume_claim: {
          claim_name: volume.refName.trim(),
          ...(volume.readOnly ? { read_only: true } : {}),
        },
      };
    case "nfs":
      return {
        name,
        nfs: {
          server: volume.nfsServer.trim(),
          path: volume.nfsPath.trim(),
          ...(volume.readOnly ? { read_only: true } : {}),
        },
      };
    default:
      return {
        name,
        empty_dir: {
          ...(volume.medium === DEFAULT_OPTION ? {} : { medium: "Memory" as const }),
          ...(volume.sizeLimit.trim() === "" ? {} : { size_limit: `${volume.sizeLimit.trim()}Mi` }),
        },
      };
  }
}

/** One command per line, the way the reference forms take them. */
export function splitCommand(value: string): string[] {
  return commandLines(value.split("\n"));
}

/** The lines that carry an argument; a blank one is editing, not an entry. */
function commandLines(values: string[]): string[] {
  return values.map((line) => line.trim()).filter((line) => line !== "");
}

function keyValueRecord(rows: KeyValueDraft[]): Record<string, string> | null {
  const entries = rows.filter((row) => row.key.trim() !== "");
  if (entries.length === 0) {
    return null;
  }
  return Object.fromEntries(entries.map((row) => [row.key.trim(), row.value.trim()]));
}

function assignQuantity(
  target: Record<string, string>,
  key: string,
  value: string,
  suffix: string,
): void {
  const trimmed = value.trim();
  if (trimmed !== "") {
    target[key] = `${trimmed}${suffix}`;
  }
}

function assignCount<T, K extends keyof T>(target: T, key: K, value: string): void {
  const trimmed = value.trim();
  if (trimmed !== "") {
    target[key] = Number(trimmed) as T[K];
  }
}
