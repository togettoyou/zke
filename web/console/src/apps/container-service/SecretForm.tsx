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
import { Input, Textarea } from "@/components/ui/input";
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

type EntryDraft = { key: string; value: string };

/** Kubernetes defaults an omitted type to Opaque; so does the form. */
const DEFAULT_SECRET_TYPE = "Opaque";

const SECRET_TYPES = [
  { value: "Opaque", label: "Opaque（任意键值）" },
  { value: "kubernetes.io/dockerconfigjson", label: "镜像仓库凭证（dockerconfigjson）" },
  { value: "kubernetes.io/basic-auth", label: "基础认证（basic-auth）" },
  { value: "kubernetes.io/ssh-auth", label: "SSH 凭证（ssh-auth）" },
  { value: "kubernetes.io/tls", label: "TLS 证书（tls）" },
];

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
  // can show as text, and values it can only show as Base64.
  const [initialText, initialBinary] = useState(() => splitSecretData(existing?.data ?? {}))[0];
  const [data, setData] = useState<EntryDraft[]>(initialText);
  const [binary, setBinary] = useState<EntryDraft[]>(initialBinary);
  // Only offered on creation: Kubernetes does not allow turning immutability
  // back off, and an immutable Secret cannot be edited at all.
  const [immutable, setImmutable] = useState(false);
  // Fixed at creation by Kubernetes, so it is shown read-only on an edit.
  const [secretType, setSecretType] = useState(existing?.type || DEFAULT_SECRET_TYPE);

  const entries = [...data, ...binary];
  const binarySizes = binary.map((entry) => strictBase64Bytes(entry.value.trim()));
  const totalBytes =
    data.reduce((sum, entry) => sum + new Blob([entry.value]).size, 0) +
    binarySizes.reduce<number>((sum, size) => sum + (size ?? 0), 0);
  const duplicateKeys = new Set(entries.map((entry) => entry.key.trim())).size !== entries.length;
  const keysValid = entries.every((entry) => {
    const key = entry.key.trim();
    return key.length <= 253 && key !== "." && key !== ".." && CONFIG_KEY.test(key);
  });
  const binaryValid = binarySizes.every((size) => size !== null);
  const trimmedName = name.trim();
  const nameValid =
    existing !== null || (DNS_SUBDOMAIN.test(trimmedName) && trimmedName.length <= 253);
  const valid =
    nameValid && keysValid && binaryValid && !duplicateKeys && totalBytes <= MAX_TOTAL_BYTES;

  // One map on the wire: text values are encoded here, Base64 values are
  // already in the form the API takes.
  const toSecretData = () => ({
    ...Object.fromEntries(data.map((row) => [row.key.trim(), encodeBase64(row.value)])),
    ...Object.fromEntries(binary.map((row) => [row.key.trim(), row.value.trim()])),
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
          <FormSection title="基本信息">
            <div className="grid content-start gap-1.5">
              <Label htmlFor="secret-type-readonly">类型</Label>
              <Input
                id="secret-type-readonly"
                value={existing.type || DEFAULT_SECRET_TYPE}
                readOnly
                disabled
              />
              <span className="text-subtle-foreground text-xs">
                Kubernetes 在创建时固定类型，之后不可修改；需要另一种类型请新建一个 Secret。
              </span>
            </div>
          </FormSection>
        ) : (
          <FormSection title="基本信息">
            <div className="grid gap-3 sm:grid-cols-2">
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
                <Select value={secretType} onValueChange={setSecretType}>
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
                <span className="text-subtle-foreground text-xs">
                  创建后不可修改。类型只决定 Kubernetes 对键名的校验，不改变取值的存储方式。
                </span>
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

        <FormSection
          title="数据"
          hint="键可包含字母、数字、`-`、`_` 和 `.`；提交时取值按 UTF-8 编码为 Base64"
        >
          <EntryList
            rows={data}
            onChange={setData}
            addLabel="添加键"
            multiline
            valuePlaceholder="值"
          />
        </FormSection>

        <FormSection
          title="二进制数据"
          hint="值必须是标准带填充 Base64；证书、密钥文件等无法按文本显示的取值在这里"
        >
          <EntryList
            rows={binary}
            onChange={setBinary}
            addLabel="添加二进制键"
            valuePlaceholder="Base64 值"
          />
        </FormSection>

        {totalBytes > MAX_TOTAL_BYTES ? (
          <Alert tone="danger">Secret 超过 1 MiB，Kubernetes 不会接受。</Alert>
        ) : null}
        {duplicateKeys ? (
          <Alert tone="danger">存在重复的键；同一个 Secret 内文本键与二进制键也不能重名。</Alert>
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
            {mutation.isPending ? "预检中…" : "执行 DryRun 预检"}
          </Button>
        </div>
      </div>

      <SensitiveActionDialog
        open={previewed}
        onOpenChange={(open) => !open && setPreviewed(false)}
        title={existing ? "确认更新 Secret" : "确认创建 Secret"}
        description="DryRun 已通过。确认后将向同一集群提交实际变更。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          { label: "Secret", name: existing?.name ?? name.trim(), id: existing?.uid },
        ]}
        impacts={
          existing
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
              ]
        }
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
  children,
}: {
  title: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <section>
      <div className="mb-2 flex items-baseline gap-2">
        <h4 className="text-foreground text-[13px] font-medium">{title}</h4>
        {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
      </div>
      {children}
    </section>
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
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label={`移除第 ${index + 1} 项`}
            onClick={() => onChange(rows.filter((_, position) => position !== index))}
          >
            <X />
          </Button>
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
