import { useState, type ReactNode } from "react";
import { Plus, X } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import { useSecret, useCreateSecret, useUpdateSecret } from "@/api/queries/secrets";
import type { KubernetesSecretDetail } from "@/api/types";
import { PageHeader } from "@/apps/AppShell";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import { CREDENTIAL_MANAGER_IGNORE, Input, Textarea } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Label } from "@/components/ui/label";
import { Alert, Checkbox } from "@/components/ui/misc";
import { useSubmissionKey } from "@/lib/use-submission-key";

const DNS_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;
/** A Secret key: alphanumerics, `-`, `_` and `.`. */
const CONFIG_KEY = /^[-._a-zA-Z0-9]+$/;
const BASE64 = /^[A-Za-z0-9+/]*={0,2}$/;
const BASE64_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
/** Kubernetes refuses a Secret larger than this, so the form does too. */
const MAX_TOTAL_BYTES = 1024 * 1024;

const DOCKER_CONFIG_JSON_TYPE = "kubernetes.io/dockerconfigjson";
/** The only key Kubernetes accepts for that type, hence not one to be typed. */
const DOCKER_CONFIG_JSON_KEY = ".dockerconfigjson";

type EntryDraft = {
  key: string;
  value: string;
  /**
   * Set on a key Kubernetes fixes for the selected type. The name is not the
   * operator's to choose, so it is shown and not editable — and the row cannot
   * be removed, because removing it only produces an object the API Server will
   * refuse for missing it.
   */
  fixed?: boolean;
};

/** One `auths` entry of a docker config document. */
type RegistryDraft = {
  registry: string;
  username: string;
  password: string;
  email: string;
  /**
   * Members of this entry the form does not model — `identitytoken`,
   * `registrytoken` — kept exactly as they were read. Somebody set them; an
   * edit that never mentioned them should not delete them.
   */
  extra: Record<string, unknown>;
};

type SecretTypeSpec = {
  value: string;
  label: string;
  /** Keys Kubernetes requires for this type. */
  requiredKeys: string[];
  /** True when all of them are required; false when any one suffices. */
  requiresEveryKey: boolean;
  /** Required keys whose value Kubernetes additionally refuses to be empty. */
  nonEmptyKeys: string[];
  /** What this type actually costs the operator, shown under the picker. */
  hint: string;
};

/** Kubernetes defaults an omitted type to Opaque; so does the form. */
const DEFAULT_SECRET_TYPE = "Opaque";

const OPAQUE_TYPE: SecretTypeSpec = {
  value: DEFAULT_SECRET_TYPE,
  label: "Opaque（任意键值）",
  requiredKeys: [],
  requiresEveryKey: true,
  nonEmptyKeys: [],
  hint: "键名和取值都由你决定，Kubernetes 不做额外要求。",
};

/*
 * The types this form creates, and what each one costs.
 *
 * The type is not only a label on the object: Kubernetes requires particular
 * keys per type, and for `dockerconfigjson` it parses the value as well. Saying
 * that here is the difference between a form that fills itself in and one that
 * lets an operator discover the rules from a rejection.
 */
const SECRET_TYPES: SecretTypeSpec[] = [
  OPAQUE_TYPE,
  {
    value: DOCKER_CONFIG_JSON_TYPE,
    label: "镜像仓库凭证（dockerconfigjson）",
    requiredKeys: [DOCKER_CONFIG_JSON_KEY],
    requiresEveryKey: true,
    nonEmptyKeys: [DOCKER_CONFIG_JSON_KEY],
    hint: "Kubernetes 要求键 `.dockerconfigjson`，并且会把它的取值当 JSON 解析。该键由下面的「镜像仓库」分区生成，不需要自行填写。",
  },
  {
    value: "kubernetes.io/basic-auth",
    label: "基础认证（basic-auth）",
    requiredKeys: ["username", "password"],
    requiresEveryKey: false,
    nonEmptyKeys: [],
    hint: "Kubernetes 要求 `username` 与 `password` 至少存在一个，取值可以为空。",
  },
  {
    value: "kubernetes.io/ssh-auth",
    label: "SSH 凭证（ssh-auth）",
    requiredKeys: ["ssh-privatekey"],
    requiresEveryKey: true,
    nonEmptyKeys: ["ssh-privatekey"],
    hint: "Kubernetes 要求键 `ssh-privatekey`，且取值不能为空。",
  },
  {
    value: "kubernetes.io/tls",
    label: "TLS 证书（tls）",
    requiredKeys: ["tls.crt", "tls.key"],
    requiresEveryKey: true,
    nonEmptyKeys: [],
    hint: "Kubernetes 要求 `tls.crt` 与 `tls.key` 同时存在，取值可以为空。",
  },
];

function specFor(type: string): SecretTypeSpec {
  return SECRET_TYPES.find((spec) => spec.value === type) ?? OPAQUE_TYPE;
}

type SectionKey = "basic" | "registries" | "data" | "binary";

/** The titles the sections are rendered with, to point at one from elsewhere. */
const SECTION_LABELS: Record<SectionKey, string> = {
  basic: "基本信息",
  registries: "镜像仓库",
  data: "数据",
  binary: "二进制数据",
};

/** The one thing currently blocking submission, and where it can be fixed. */
type FormProblem = { section: SectionKey; message: string };

/*
 * The first problem in the form, read top to bottom.
 *
 * One at a time, and named where it can be fixed: a list of every fault at the
 * bottom of the page is a list an operator has to map back onto fields, and the
 * page is longer than the screen. Reported in the order the sections appear, so
 * fixing what is reported moves down the form rather than around it.
 */
function secretProblem(draft: {
  name: string;
  editing: boolean;
  spec: SecretTypeSpec;
  data: EntryDraft[];
  binary: EntryDraft[];
  registries: RegistryDraft[];
  registryEditorActive: boolean;
}): FormProblem | null {
  const { name, editing, spec, data, binary, registries, registryEditorActive } = draft;
  if (!editing) {
    const trimmed = name.trim();
    if (trimmed === "") {
      return { section: "basic", message: "请填写名称。" };
    }
    if (trimmed.length > 253) {
      return { section: "basic", message: "名称最长 253 个字符。" };
    }
    if (!DNS_SUBDOMAIN.test(trimmed)) {
      return {
        section: "basic",
        message:
          "名称必须是合法的 DNS 子域名：只能包含小写字母、数字、连字符和点，并以字母或数字开头和结尾。",
      };
    }
  }
  if (registryEditorActive) {
    const registryFault = registryProblem(registries);
    if (registryFault) {
      return registryFault;
    }
  }
  /*
   * Shared across both value sections, and seeded with the key the registry
   * editor generates.
   *
   * Kubernetes keeps text and binary values in one namespace, so a key used in
   * either place is taken in both — and a hand-added `.dockerconfigjson` has to
   * collide with the generated one here rather than silently lose to whichever
   * of them the submitted object spread wrote last.
   */
  const seen = new Set(registryEditorActive ? [DOCKER_CONFIG_JSON_KEY] : []);
  const entryFault =
    entryProblem(data, seen, "data", false) ?? entryProblem(binary, seen, "binary", true);
  if (entryFault) {
    return entryFault;
  }
  return typeProblem(spec, data, binary, seen, registryEditorActive);
}

function registryProblem(registries: RegistryDraft[]): FormProblem | null {
  const seen = new Set<string>();
  for (const [index, row] of registries.entries()) {
    const registry = row.registry.trim();
    const where = `第 ${index + 1} 个仓库`;
    if (registry === "") {
      return { section: "registries", message: `${where}缺少地址。` };
    }
    if (/\s/.test(row.registry)) {
      return { section: "registries", message: `${where}的地址不能包含空格。` };
    }
    if (seen.has(registry)) {
      return {
        section: "registries",
        message: `仓库地址「${registry}」重复；docker config 按地址索引凭证，重复的地址只有一条会生效。`,
      };
    }
    seen.add(registry);
    if (row.username === "") {
      return { section: "registries", message: `${where}缺少用户名。` };
    }
    // A registry that authenticates by token carries it in a field this form
    // preserves rather than shows, and then needs no password.
    if (row.password === "" && typeof row.extra.identitytoken !== "string") {
      return { section: "registries", message: `${where}缺少密码。` };
    }
  }
  return null;
}

function entryProblem(
  rows: EntryDraft[],
  seen: Set<string>,
  section: SectionKey,
  base64: boolean,
): FormProblem | null {
  for (const [index, row] of rows.entries()) {
    const key = row.key.trim();
    const where = `第 ${index + 1} 项`;
    if (key === "") {
      return { section, message: `${where}缺少键名。` };
    }
    if (key === "." || key === "..") {
      return { section, message: `${where}的键不能是单个点或两个点。` };
    }
    if (key.length > 253) {
      return { section, message: `${where}的键最长 253 个字符。` };
    }
    if (!CONFIG_KEY.test(key)) {
      return {
        section,
        message: `${where}的键「${key}」只能包含字母、数字、连字符、下划线和点。`,
      };
    }
    if (seen.has(key)) {
      return {
        section,
        message:
          key === DOCKER_CONFIG_JSON_KEY
            ? "「镜像仓库」分区已经生成了 .dockerconfigjson，请移除这一项。"
            : `键「${key}」重复；同一个 Secret 内文本键与二进制键也不能重名。`,
      };
    }
    seen.add(key);
    if (base64 && strictBase64Bytes(row.value.trim()) === null) {
      return { section, message: `${where}的值不是合法的标准带填充 Base64。` };
    }
  }
  return null;
}

/*
 * Kubernetes' own per-type rules, checked here so a missing key is a field to
 * fill rather than a rejection to read.
 */
function typeProblem(
  spec: SecretTypeSpec,
  data: EntryDraft[],
  binary: EntryDraft[],
  presentKeys: Set<string>,
  registryEditorActive: boolean,
): FormProblem | null {
  const absent = spec.requiredKeys.filter((key) => !presentKeys.has(key));
  const satisfied = spec.requiresEveryKey
    ? absent.length === 0
    : absent.length < spec.requiredKeys.length;
  if (!satisfied) {
    return {
      section: "data",
      message: spec.requiresEveryKey
        ? `${spec.value} 类型要求键 ${absent.join("、")}，Kubernetes 会拒绝缺少它们的对象。`
        : `${spec.value} 类型要求 ${spec.requiredKeys.join(" 或 ")} 至少存在一个。`,
    };
  }
  for (const key of spec.nonEmptyKeys) {
    const text = data.find((entry) => entry.key.trim() === key);
    if (text && text.value === "") {
      return { section: "data", message: `${spec.value} 类型不接受 ${key} 为空值。` };
    }
    const bytes = binary.find((entry) => entry.key.trim() === key);
    if (bytes && bytes.value.trim() === "") {
      return { section: "binary", message: `${spec.value} 类型不接受 ${key} 为空值。` };
    }
  }
  /*
   * The raw `.dockerconfigjson` an operator can still reach, on the object the
   * registry editor stepped aside for. Kubernetes parses this value, so a form
   * that let it through unchecked would spend a round trip to say so.
   */
  if (spec.value === DOCKER_CONFIG_JSON_TYPE && !registryEditorActive) {
    const raw = data.find((entry) => entry.key.trim() === DOCKER_CONFIG_JSON_KEY);
    if (raw && parseDockerConfigJson(raw.value) === null) {
      return {
        section: "data",
        message: looksAlreadyEncoded(raw.value)
          ? ".dockerconfigjson 的取值看起来已经是 Base64：本分区会再编码一次，Kubernetes 解码后拿到的仍是 Base64 文本。请填入解码后的 JSON，或把这段 Base64 移到「二进制数据」分区。"
          : ".dockerconfigjson 的取值必须是合法的 JSON 对象。",
      };
    }
  }
  return null;
}

/*
 * Splits stored values into the two ways a form can show them.
 *
 * Every Secret value is bytes, and the API carries all of them as Base64. A
 * value whose bytes are valid UTF-8 text is editable as text; anything else — a
 * certificate, a key file — is shown as Base64 and submitted unchanged, because
 * any text the browser produced from those bytes would be a guess.
 */
function splitSecretData(data: Record<string, string>): [EntryDraft[], EntryDraft[]] {
  const text: EntryDraft[] = [];
  const binary: EntryDraft[] = [];
  for (const [key, value] of Object.entries(data)) {
    const decoded = decodeUtf8Base64(value);
    if (decoded === null) {
      binary.push({ key, value });
      continue;
    }
    text.push({ key, value: decoded });
  }
  return [text, binary];
}

/** The text a Base64 value holds, or null when its bytes are not UTF-8 text. */
function decodeUtf8Base64(value: string): string | null {
  try {
    const binary = atob(value);
    const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0));
    const decoded = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
    // A control character means the bytes are data that happens to decode, and
    // a textarea would neither show it nor give it back unchanged. Checked by
    // code point rather than by a regular expression, which cannot carry these
    // characters literally without being unreadable.
    for (const character of decoded) {
      const code = character.codePointAt(0) ?? 0;
      if (code < 0x20 && code !== 0x09 && code !== 0x0a && code !== 0x0d) {
        return null;
      }
    }
    return decoded;
  } catch {
    return null;
  }
}

/** UTF-8 bytes of the entered text, in standard padded Base64. */
function encodeBase64(value: string): string {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary);
}

/**
 * Whether a text value is Base64 of something that was itself text.
 *
 * The 数据 section encodes what it is given, so a value pasted in the form it
 * appears in a YAML manifest gets encoded a second time — and Kubernetes, which
 * decodes once, is then handed Base64 where it expected content. That failure
 * arrives as a parse error about a character the operator never typed.
 *
 * Only a decode into JSON or PEM counts. Both are shapes nobody stores as one
 * opaque Base64 line by intent, and requiring one of them keeps a password that
 * merely looks like Base64 from being second-guessed.
 */
function looksAlreadyEncoded(value: string): boolean {
  const trimmed = value.trim();
  if (trimmed.length < 24 || strictBase64Bytes(trimmed) === null) {
    return false;
  }
  const decoded = decodeUtf8Base64(trimmed);
  if (decoded === null) {
    return false;
  }
  const start = decoded.trimStart();
  return start.startsWith("{") || start.startsWith("-----BEGIN");
}

function emptyRegistry(): RegistryDraft {
  return { registry: "", username: "", password: "", email: "", extra: {} };
}

/** The username and password inside a docker `auth` field, which is their pair. */
function credentialsFromAuth(auth: unknown): { username: string; password: string } | null {
  if (typeof auth !== "string") {
    return null;
  }
  const decoded = decodeUtf8Base64(auth);
  if (decoded === null) {
    return null;
  }
  const separator = decoded.indexOf(":");
  if (separator < 0) {
    return null;
  }
  return {
    username: decoded.slice(0, separator),
    password: decoded.slice(separator + 1),
  };
}

function isJsonObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Reads a docker config document into rows this form can show.
 *
 * Returns null when the document is not one — which is the answer that decides
 * whether the registry editor is used at all. An object it cannot read is left
 * to the generic section untouched rather than replaced by an editor that would
 * have to guess at it.
 */
function parseDockerConfigJson(
  text: string,
): { registries: RegistryDraft[]; extra: Record<string, unknown> } | null {
  let document: unknown;
  try {
    document = JSON.parse(text);
  } catch {
    return null;
  }
  if (!isJsonObject(document)) {
    return null;
  }
  const { auths, ...extra } = document;
  if (auths !== undefined && !isJsonObject(auths)) {
    return null;
  }
  const registries: RegistryDraft[] = [];
  for (const [registry, entry] of Object.entries(auths ?? {})) {
    if (!isJsonObject(entry)) {
      return null;
    }
    const { username, password, email, auth, ...rest } = entry;
    // A registry that stored only `auth` still holds a username and password —
    // that field is exactly the two joined by a colon — so both are shown rather
    // than an entry the form appears not to understand.
    const paired = credentialsFromAuth(auth);
    registries.push({
      registry,
      username: typeof username === "string" ? username : (paired?.username ?? ""),
      password: typeof password === "string" ? password : (paired?.password ?? ""),
      email: typeof email === "string" ? email : "",
      extra: rest,
    });
  }
  return { registries, extra };
}

/** The `.dockerconfigjson` value for the entered registries. */
function buildDockerConfigJson(
  registries: RegistryDraft[],
  extra: Record<string, unknown>,
): string {
  const auths: Record<string, unknown> = {};
  for (const row of registries) {
    const registry = row.registry.trim();
    if (registry === "") {
      continue;
    }
    const entry: Record<string, unknown> = {
      ...row.extra,
      username: row.username,
      password: row.password,
      // Docker's own field and the one most registries read: the same pair, in
      // Base64. Recomputed rather than carried across, so changing a password
      // cannot leave the previous credential working behind it.
      auth: encodeBase64(`${row.username}:${row.password}`),
    };
    if (row.email.trim() === "") {
      delete entry.email;
    } else {
      entry.email = row.email.trim();
    }
    auths[registry] = entry;
  }
  return JSON.stringify({ ...extra, auths });
}

function markFixedKeys(rows: EntryDraft[], spec: SecretTypeSpec): EntryDraft[] {
  return rows.map((row) =>
    spec.requiredKeys.includes(row.key) ? { ...row, fixed: true } : { ...row, fixed: false },
  );
}

type InitialState = {
  data: EntryDraft[];
  binary: EntryDraft[];
  registries: RegistryDraft[];
  dockerExtra: Record<string, unknown>;
  /**
   * Set when the object's type is `dockerconfigjson` but its value could not be
   * read as one. Kubernetes validates that value on write, so this only happens
   * to an object that got in another way — and its credential is then worth more
   * than the nicer editor.
   */
  unreadableDockerConfig: boolean;
};

function initialState(existing: KubernetesSecretDetail | null): InitialState {
  // A new Secret starts as Opaque, so the dockerconfigjson branch below is
  // reached only by an edit; a create switches into the registry editor through
  // the type picker instead.
  const spec = specFor(existing?.type || DEFAULT_SECRET_TYPE);
  const [text, binary] = splitSecretData(existing?.data ?? {});
  if (spec.value !== DOCKER_CONFIG_JSON_TYPE) {
    const present = new Set([...text, ...binary].map((row) => row.key));
    const absent = spec.requiredKeys.filter((key) => !present.has(key));
    /*
     * Prefilled only while the type's requirement is unmet, which on a create is
     * always and on an edit almost never.
     *
     * `basic-auth` needs one of two keys, so an object holding only `username`
     * already satisfies it. Scaffolding an empty `password` row there would add
     * a key to the object — the update replaces its contents wholesale — and an
     * edit nobody asked for is the kind that gets noticed later.
     */
    const satisfied = spec.requiresEveryKey
      ? absent.length === 0
      : absent.length < spec.requiredKeys.length;
    const missing = satisfied ? [] : absent.map((key) => ({ key, value: "", fixed: true }));
    return {
      data: [...markFixedKeys(text, spec), ...missing],
      binary: markFixedKeys(binary, spec),
      registries: [emptyRegistry()],
      dockerExtra: {},
      unreadableDockerConfig: false,
    };
  }
  const stored = text.find((row) => row.key === DOCKER_CONFIG_JSON_KEY);
  const parsed = stored ? parseDockerConfigJson(stored.value) : null;
  if (parsed === null) {
    return {
      data: markFixedKeys(text, spec),
      binary: markFixedKeys(binary, spec),
      registries: [emptyRegistry()],
      dockerExtra: {},
      unreadableDockerConfig: true,
    };
  }
  return {
    data: text.filter((row) => row.key !== DOCKER_CONFIG_JSON_KEY),
    binary,
    registries: parsed.registries.length > 0 ? parsed.registries : [emptyRegistry()],
    dockerExtra: parsed.extra,
    unreadableDockerConfig: false,
  };
}

/**
 * Creates or replaces one Secret.
 *
 * Editing loads the object first: the update is a replacement carrying the UID
 * and resourceVersion it was read at, and the list deliberately does not return
 * values, so there is nothing to edit until the detail has arrived.
 *
 * A page rather than a dialog, for the same reason the workload and networking
 * forms are pages: a configuration file is as long as it is, and reading one
 * through a box laid over the list is worse than leaving the list — which is of
 * no use while the form is open anyway.
 */
export function SecretForm({
  clusterId,
  clusterName,
  namespace,
  editingName,
  onClose,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  /** Set when editing an existing Secret; null when creating. */
  editingName: string | null;
  onClose: () => void;
}) {
  const existing = useSecret(clusterId, namespace, editingName, true);

  // A cached detail is intentionally not enough for an edit. Wait for the
  // mount-time fetch, then pin exactly the body and identity returned together.
  if (editingName && !existing.isFetchedAfterMount) {
    return (
      <>
        <PageHeader title={`编辑 Secret · ${editingName}`} onBack={onClose} />
        <LoadingState />
      </>
    );
  }
  if (editingName && (existing.error || !existing.data)) {
    return (
      <>
        <PageHeader title={`编辑 Secret · ${editingName}`} onBack={onClose} />
        <ErrorState error={existing.error} onRetry={() => void existing.refetch()} />
      </>
    );
  }

  return (
    <SecretEditor
      clusterId={clusterId}
      clusterName={clusterName}
      namespace={namespace}
      existing={editingName ? (existing.data as KubernetesSecretDetail) : null}
      onClose={onClose}
    />
  );
}

function SecretEditor({
  clusterId,
  clusterName,
  namespace,
  existing,
  onClose,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  existing: KubernetesSecretDetail | null;
  onClose: () => void;
}) {
  const create = useCreateSecret();
  const update = useUpdateSecret();
  const mutation = existing ? update : create;
  const [previewed, setPreviewed] = useState(false);
  const previewKey = useSubmissionKey(!previewed);
  const applyKey = useSubmissionKey(previewed);

  // Pinned at editor mount rather than read again at submit time. Taking a
  // fresher resourceVersion than the contents copied into local form state
  // would turn a conflict the Server should catch into a silent overwrite.
  const [pinned] = useState(() =>
    existing ? { uid: existing.uid, resourceVersion: existing.resource_version } : null,
  );
  const [name, setName] = useState(existing?.name ?? "");
  // Kubernetes stores every value as bytes, so the API carries all of them as
  // Base64. The form splits them the way an operator thinks of them: values it
  // can show as text, values it can only show as Base64, and — for a registry
  // credential — the four fields the one required key is assembled from.
  const [initial] = useState(() => initialState(existing));
  const [data, setData] = useState<EntryDraft[]>(initial.data);
  const [binary, setBinary] = useState<EntryDraft[]>(initial.binary);
  const [registries, setRegistries] = useState<RegistryDraft[]>(initial.registries);
  // Only offered on creation: Kubernetes does not allow turning immutability
  // back off, and an immutable Secret cannot be edited at all.
  const [immutable, setImmutable] = useState(false);
  // Fixed at creation by Kubernetes, so it is shown read-only on an edit.
  const [secretType, setSecretType] = useState(existing?.type || DEFAULT_SECRET_TYPE);
  const spec = specFor(secretType);

  /*
   * Whether `.dockerconfigjson` is owned by the registry editor.
   *
   * It is, for every Secret of that type this form created, and for every one
   * Kubernetes accepted — that value is validated as JSON on write. The one
   * exception keeps the raw value in the generic section, where it round-trips
   * unread rather than being replaced by four fields nobody can fill from it.
   */
  const registryEditorActive =
    secretType === DOCKER_CONFIG_JSON_TYPE && !initial.unreadableDockerConfig;
  const dockerConfigText = registryEditorActive
    ? buildDockerConfigJson(registries, initial.dockerExtra)
    : null;

  const binarySizes = binary.map((entry) => strictBase64Bytes(entry.value.trim()));
  const totalBytes =
    data.reduce((sum, entry) => sum + new Blob([entry.value]).size, 0) +
    binarySizes.reduce<number>((sum, size) => sum + (size ?? 0), 0) +
    (dockerConfigText === null ? 0 : new Blob([dockerConfigText]).size);
  const filledRegistries = registries
    .map((row) => row.registry.trim())
    .filter((registry) => registry !== "");

  const problem = secretProblem({
    name,
    editing: existing !== null,
    spec,
    data,
    binary,
    registries,
    registryEditorActive,
  });
  const problemIn = (section: SectionKey) =>
    problem?.section === section ? problem.message : undefined;
  // The size is the object's rather than any one section's, and the running
  // total next to the button already shows it, so it stays out of the problem
  // above and is reported where it is measured.
  const oversized = totalBytes > MAX_TOTAL_BYTES;
  const valid = problem === null && !oversized;

  // A warning rather than a blocker: this is a guess about intent, and a value
  // that decodes into JSON can still be exactly what was meant.
  const doubleEncodedKeys = data
    .filter((entry) => looksAlreadyEncoded(entry.value))
    .map((entry) => entry.key.trim() || "未命名的键");

  /*
   * Changes the type of a Secret being created.
   *
   * The prefilled keys travel with the type: a `tls.crt` row left behind after a
   * switch to `basic-auth` is a key that type does not want, and the row this
   * type does want would be missing.
   */
  const changeType = (next: string) => {
    const nextSpec = specFor(next);
    setSecretType(next);
    setData((rows) => {
      // A prefilled row the new type does not want is dropped, unless something
      // was typed into it: the scaffolding belongs to the type, but the content
      // belongs to the operator, and Opaque accepts any key name anyway.
      const kept = rows.filter(
        (row) => !row.fixed || nextSpec.requiredKeys.includes(row.key) || row.value !== "",
      );
      const present = new Set(kept.map((row) => row.key.trim()));
      const added = nextSpec.requiredKeys
        // Owned by the registry editor rather than entered as a key/value pair.
        .filter((key) => key !== DOCKER_CONFIG_JSON_KEY && !present.has(key))
        .map((key) => ({ key, value: "", fixed: true }));
      return [...markFixedKeys(kept, nextSpec), ...added];
    });
  };

  // One map on the wire: text values are encoded here, Base64 values are
  // already in the form the API takes, and the registry document is assembled
  // and then encoded like any other text value.
  const toSecretData = () => ({
    ...Object.fromEntries(data.map((row) => [row.key.trim(), encodeBase64(row.value)])),
    ...Object.fromEntries(binary.map((row) => [row.key.trim(), row.value.trim()])),
    ...(dockerConfigText === null
      ? {}
      : { [DOCKER_CONFIG_JSON_KEY]: encodeBase64(dockerConfigText) }),
  });

  const submit = (dryRun: boolean) => {
    const shared = {
      clusterId,
      namespace,
      data: toSecretData(),
      dryRun,
      idempotencyKey: dryRun ? previewKey : applyKey,
    };
    const request = existing
      ? update.mutateAsync({
          ...shared,
          name: existing.name,
          uid: pinned?.uid ?? existing.uid,
          resourceVersion: pinned?.resourceVersion ?? existing.resource_version,
        })
      : create.mutateAsync({
          ...shared,
          name: name.trim(),
          ...(secretType === DEFAULT_SECRET_TYPE ? {} : { type: secretType }),
          immutable,
        });
    void request
      .then(() => {
        if (dryRun) {
          setPreviewed(true);
          return;
        }
        toast.success(`Secret ${existing?.name ?? name.trim()} 已${existing ? "更新" : "创建"}`);
        onClose();
      })
      .catch(() => undefined);
  };

  return (
    <>
      <div className="grid gap-3">
        <PageHeader
          title={existing ? `编辑 Secret · ${existing.name}` : `创建 Secret · ${namespace}`}
          onBack={onClose}
          backDisabled={mutation.isPending}
        />

        {existing ? (
          <FormSection title={SECTION_LABELS.basic}>
            <div className="grid content-start gap-1.5">
              <Label htmlFor="secret-type-readonly">类型</Label>
              <Input
                id="secret-type-readonly"
                value={existing.type || DEFAULT_SECRET_TYPE}
                readOnly
                disabled
              />
              <span className="text-subtle-foreground text-xs">
                创建后不可修改，换类型需新建
                {spec.requiredKeys.length > 0 ? `。${spec.hint}` : ""}
              </span>
            </div>
          </FormSection>
        ) : (
          <FormSection title={SECTION_LABELS.basic} problem={problemIn("basic")}>
            <div className="grid gap-3 @md:grid-cols-2">
              <div className="grid content-start gap-1.5">
                <Label htmlFor="secret-name">名称</Label>
                <Input
                  id="secret-name"
                  value={name}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="例如 registry-credential"
                  onChange={(event) => setName(event.target.value)}
                />
              </div>
              <div className="grid content-start gap-1.5">
                <Label htmlFor="secret-type">类型</Label>
                <Select value={secretType} onValueChange={changeType}>
                  <SelectTrigger id="secret-type">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {SECRET_TYPES.map((type) => (
                      <SelectItem key={type.value} value={type.value}>
                        {type.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <span className="text-subtle-foreground text-xs">创建后不可修改。{spec.hint}</span>
              </div>
            </div>
            <label className="mt-3 flex items-center gap-2 text-[13px]">
              <Checkbox
                checked={immutable}
                onCheckedChange={(checked) => setImmutable(checked === true)}
              />
              标记为不可变（创建后无法修改内容，也无法改回可变）
            </label>
          </FormSection>
        )}

        {registryEditorActive ? (
          <FormSection
            title={SECTION_LABELS.registries}
            hint="自动生成 `.dockerconfigjson`，不需要自行填写或编码"
            problem={problemIn("registries")}
          >
            <RegistryList rows={registries} onChange={setRegistries} />
          </FormSection>
        ) : null}

        <FormSection
          title={SECTION_LABELS.data}
          hint={
            registryEditorActive
              ? "此处填写镜像仓库凭证之外的其他键；提交时取值按 UTF-8 编码为 Base64，请填原文而不是 Base64"
              : "键可包含字母、数字、`-`、`_` 和 `.`；提交时取值按 UTF-8 编码为 Base64，请填原文而不是 Base64"
          }
          problem={problemIn("data")}
        >
          {initial.unreadableDockerConfig ? (
            <Alert tone="warning" className="mb-2">
              该 Secret 的 .dockerconfigjson 取值无法按 docker config JSON
              读取，因此没有使用「镜像仓库」表单，而是在这里按原样保留。 改动其他键不会影响它。
            </Alert>
          ) : null}
          <EntryList
            rows={data}
            onChange={setData}
            addLabel="添加键"
            multiline
            valuePlaceholder="值"
          />
          {doubleEncodedKeys.length > 0 ? (
            <Alert tone="warning" className="mt-2">
              {doubleEncodedKeys.join("、")} 的取值看起来已经是 Base64（解码后是 JSON 或 PEM
              文本）。本分区会把它再编码一次；如果你想原样提交这段
              Base64，请改用「二进制数据」分区。
            </Alert>
          ) : null}
        </FormSection>

        <FormSection
          title={SECTION_LABELS.binary}
          hint="值必须是标准带填充 Base64；证书、密钥等非文本取值放这里"
          problem={problemIn("binary")}
        >
          <EntryList
            rows={binary}
            onChange={setBinary}
            addLabel="添加二进制键"
            valuePlaceholder="Base64 值"
          />
        </FormSection>

        {oversized ? <Alert tone="danger">Secret 超过 1 MiB，Kubernetes 不会接受。</Alert> : null}
        {/*
         * The message itself is up in the section that can fix it; down here,
         * next to the button it disables, what is missing is where to look.
         */}
        {problem ? (
          <Alert tone="warning">「{SECTION_LABELS[problem.section]}」中还有需要修正的项。</Alert>
        ) : null}
        {mutation.error ? <Alert tone="danger">{errorMessage(mutation.error)}</Alert> : null}

        <div className="flex flex-wrap items-center justify-end gap-3 pb-2">
          <span className="text-subtle-foreground zke-tnum text-xs">
            合计 {(totalBytes / 1024).toFixed(1)} KiB / 1024 KiB
          </span>
          {existing ? (
            <span className="text-subtle-foreground text-xs">
              更新会整体替换内容：本表单中不存在的键将从对象中移除。
            </span>
          ) : null}
          <Button
            variant="primary"
            size="sm"
            disabled={!valid || mutation.isPending}
            onClick={() => submit(true)}
          >
            {mutation.isPending ? "DryRun 预检中…" : "执行 DryRun 预检"}
          </Button>
        </div>
      </div>

      <SensitiveActionDialog
        open={previewed}
        onOpenChange={(open) => !open && setPreviewed(false)}
        title={existing ? "确认更新 Secret" : "确认创建 Secret"}
        description="DryRun 预检已通过。确认后将向同一集群提交实际变更。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          { label: "Secret", name: existing?.name ?? name.trim(), id: existing?.uid },
        ]}
        impacts={[
          ...(registryEditorActive
            ? [
                `凭证将写入 ${filledRegistries.join("、")}；引用该 Secret 的工作负载会用它拉取镜像。`,
              ]
            : []),
          ...(existing
            ? [
                "内容会被整体替换：本次未提交的键将从对象中移除。",
                "取值是凭证：变更后请一并考虑轮换使用它的凭据，旧值可能已被引用它的工作负载缓存。",
                "以 Volume 挂载的 Pod 会在 kubelet 下一次同步后看到新内容；以环境变量注入的 Pod 需要重启才会生效。",
                "请求携带该对象当前的 UID 与 resourceVersion，期间对象若已变化，更新会被拒绝而不是覆盖。",
              ]
            : [
                "将在目标集群持久化一个新的 Secret。",
                ...(immutable
                  ? ["标记为不可变后，Kubernetes 不允许再修改它的内容，只能删除重建。"]
                  : []),
              ]),
        ]}
        confirmLabel={existing ? "确认更新" : "确认创建"}
        destructive={existing !== null}
        pending={mutation.isPending}
        error={mutation.error}
        onConfirm={() => submit(false)}
      />
    </>
  );
}

/**
 * Validates standard padded Base64 and returns its decoded byte length.
 *
 * The final alphabet bits must be zero when padding is present, matching Go's
 * strict standard decoder on the Server without allocating a decoded copy in
 * the browser merely to calculate the Secret size.
 */
function strictBase64Bytes(value: string): number | null {
  if (value === "") {
    return 0;
  }
  if (value.length % 4 !== 0 || !BASE64.test(value)) {
    return null;
  }
  const padding = value.endsWith("==") ? 2 : value.endsWith("=") ? 1 : 0;
  const lastIndex = BASE64_ALPHABET.indexOf(value[value.length - padding - 1] ?? "");
  if (lastIndex < 0 || (padding === 1 && (lastIndex & 0b11) !== 0)) {
    return null;
  }
  if (padding === 2 && (lastIndex & 0b1111) !== 0) {
    return null;
  }
  return (value.length / 4) * 3 - padding;
}

function FormSection({
  title,
  hint,
  problem,
  children,
}: {
  title: string;
  hint?: string;
  /** The current blocking problem, when it is this section that carries it. */
  problem?: string;
  children: ReactNode;
}) {
  return (
    <section>
      <div className="mb-2 flex flex-wrap items-baseline gap-2">
        <h4 className="text-foreground text-[13px] font-medium">{title}</h4>
        {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
      </div>
      {problem ? (
        <Alert tone="warning" className="mb-2">
          {problem}
        </Alert>
      ) : null}
      {children}
    </section>
  );
}

/**
 * The registries a docker config document holds.
 *
 * Four fields rather than one blob, because the blob is the reason this form
 * existed to be got wrong: `.dockerconfigjson` appears in the world as Base64,
 * so a field asking for its content invites the Base64 and encodes it twice.
 * Nothing here is Base64, and the key name is not asked for at all.
 */
function RegistryList({
  rows,
  onChange,
}: {
  rows: RegistryDraft[];
  onChange: (rows: RegistryDraft[]) => void;
}) {
  const update = (index: number, patch: Partial<RegistryDraft>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...patch } : row)));

  return (
    <div className="grid gap-2">
      {rows.map((row, index) => (
        <div
          key={index}
          className="border-border rounded-control grid grid-cols-[1fr_auto] items-start gap-2 border p-2"
        >
          <div className="grid gap-2">
            <div className="grid content-start gap-1.5">
              <Label htmlFor={`registry-host-${index}`}>仓库地址</Label>
              <Input
                id={`registry-host-${index}`}
                value={row.registry}
                autoComplete="off"
                spellCheck={false}
                className="zke-mono text-xs"
                placeholder="例如 registry.example.com 或 registry.example.com:5000"
                onChange={(event) => update(index, { registry: event.target.value })}
              />
            </div>
            <div className="grid gap-2 @2xl:grid-cols-3">
              <div className="grid content-start gap-1.5">
                <Label htmlFor={`registry-username-${index}`}>用户名</Label>
                <Input
                  id={`registry-username-${index}`}
                  value={row.username}
                  autoComplete="off"
                  spellCheck={false}
                  {...CREDENTIAL_MANAGER_IGNORE}
                  onChange={(event) => update(index, { username: event.target.value })}
                />
              </div>
              <div className="grid content-start gap-1.5">
                <Label htmlFor={`registry-password-${index}`}>密码</Label>
                <Input
                  id={`registry-password-${index}`}
                  value={row.password}
                  autoComplete="off"
                  spellCheck={false}
                  {...CREDENTIAL_MANAGER_IGNORE}
                  onChange={(event) => update(index, { password: event.target.value })}
                />
              </div>
              <div className="grid content-start gap-1.5">
                <Label htmlFor={`registry-email-${index}`}>邮箱（可选）</Label>
                <Input
                  id={`registry-email-${index}`}
                  value={row.email}
                  autoComplete="off"
                  spellCheck={false}
                  onChange={(event) => update(index, { email: event.target.value })}
                />
              </div>
            </div>
            {Object.keys(row.extra).length > 0 ? (
              <span className="text-subtle-foreground text-xs">
                该仓库还带有本表单未建模的字段（{Object.keys(row.extra).sort().join("、")}
                ），提交时按当前取值原样保留。
              </span>
            ) : null}
          </div>
          {rows.length > 1 ? (
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={`移除第 ${index + 1} 个仓库`}
              onClick={() => onChange(rows.filter((_, position) => position !== index))}
            >
              <X />
            </Button>
          ) : (
            <span />
          )}
        </div>
      ))}
      <div>
        <Button size="sm" variant="secondary" onClick={() => onChange([...rows, emptyRegistry()])}>
          <Plus />
          添加仓库
        </Button>
      </div>
    </div>
  );
}

function EntryList({
  rows,
  onChange,
  addLabel,
  valuePlaceholder,
  multiline = false,
}: {
  rows: EntryDraft[];
  onChange: (rows: EntryDraft[]) => void;
  addLabel: string;
  valuePlaceholder: string;
  /** Text values are usually whole config files, so they get a resizable box. */
  multiline?: boolean;
}) {
  const update = (index: number, patch: Partial<EntryDraft>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...patch } : row)));

  return (
    <div className="grid gap-2">
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[1fr_auto] items-start gap-2">
          <div className="grid content-start gap-1.5">
            <Input
              value={row.key}
              aria-label={`第 ${index + 1} 个键`}
              placeholder="键"
              autoComplete="off"
              spellCheck={false}
              // Fixed by the Secret's type: shown so the operator can see which
              // key they are filling, but not editable, because the only name
              // Kubernetes accepts here is the one already in it.
              readOnly={row.fixed}
              disabled={row.fixed}
              onChange={(event) => update(index, { key: event.target.value })}
            />
            {multiline ? (
              <Textarea
                value={row.value}
                aria-label={`第 ${index + 1} 个值`}
                placeholder={valuePlaceholder}
                spellCheck={false}
                autoComplete="off"
                className="zke-mono min-h-24 text-xs leading-relaxed"
                onChange={(event) => update(index, { value: event.target.value })}
              />
            ) : (
              <Input
                value={row.value}
                aria-label={`第 ${index + 1} 个值`}
                placeholder={valuePlaceholder}
                autoComplete="off"
                spellCheck={false}
                className="zke-mono text-xs"
                onChange={(event) => update(index, { value: event.target.value })}
              />
            )}
          </div>
          {row.fixed ? (
            <span />
          ) : (
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={`移除第 ${index + 1} 项`}
              onClick={() => onChange(rows.filter((_, position) => position !== index))}
            >
              <X />
            </Button>
          )}
        </div>
      ))}
      <div>
        <Button
          size="sm"
          variant="secondary"
          onClick={() => onChange([...rows, { key: "", value: "" }])}
        >
          <Plus />
          {addLabel}
        </Button>
      </div>
    </div>
  );
}
