import { useCallback, useMemo, useState, type ReactNode } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Eye, EyeOff, FileCode, Lock, Pencil, Plus } from "lucide-react";
import { toast } from "sonner";

import { useSecret, useSecrets, useDeleteSecret } from "@/api/queries/secrets";
import type { KubernetesSecretDetail, KubernetesSecretSummary } from "@/api/types";
import { PageHeader, SectionToolbarActions } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
import { DetailDeleteAction, RowDeleteAction } from "@/components/common/delete-action";
import { DetailCard, DetailKeyValues, DetailRow } from "@/components/common/detail";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { RefreshAction } from "@/components/common/refresh-action";
import { ErrorState, LoadingState } from "@/components/common/state";
import { CopyButton } from "@/components/common/status";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/misc";
import { HintTooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/cn";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { SecretForm } from "./SecretForm";
import { YamlEditorView } from "./YamlEditorView";
import { useContinuePagination } from "./use-continue-pagination";
import type { ClusterSectionProps } from "./types";

const PAGE_SIZE = 50;

type SecretSectionProps = ClusterSectionProps & {
  /** The Namespace every query and mutation in this section is scoped to. */
  namespace: string;
  /**
   * The strip that switches between ConfigMaps and Secrets, rendered by this
   * section rather than around it.
   *
   * It belongs to the list: the detail, the form and the YAML view each take
   * the section over, and a switch that stayed on screen above an open object
   * would offer to change what the list shows while the list is not there.
   */
  tabs?: ReactNode;
};

/**
 * Secrets of one Namespace of one Cluster.
 *
 * Secrets are deliberately absent: the Agent is not granted access to them, and
 * this section must not read as though a Secret view is merely missing.
 */
export function SecretSection({
  clusterId,
  clusterName,
  namespace,
  tenantId,
  projectId,
  tabs,
}: SecretSectionProps) {
  const { permissions } = useSessionContext();
  const pager = useContinuePagination(`${clusterId}/${namespace}`);
  const list = useSecrets(clusterId, namespace, {
    limit: PAGE_SIZE,
    ...(pager.token ? { continue: pager.token } : {}),
  });
  const [detailName, setDetailName] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [editingName, setEditingName] = useState<string | null>(null);
  const [yamlTarget, setYamlTarget] = useState<KubernetesSecretSummary | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<KubernetesSecretSummary | null>(null);
  const [deletePreviewed, setDeletePreviewed] = useState(false);
  const deletePreviewKey = useSubmissionKey(deleteTarget !== null);
  const deleteApplyKey = useSubmissionKey(deleteTarget !== null);
  const remove = useDeleteSecret();

  const projectScope = { type: "project" as const, tenantId, projectId };
  // Managing a credential is one permission, not three: `cluster.secret.manage`
  // covers create, update and delete, and neither it nor `cluster.secret.read`
  // is implied by the general `cluster.resource.*` permissions.
  const canManage = permissions.can("cluster.secret.manage", projectScope);
  const canCreate = canManage;
  const canUpdate = canManage;
  const canDelete = canManage;

  // Both the row action and the detail view open the same confirmation, so it is
  // one callback rather than two copies of the reset sequence.
  const openDelete = useCallback(
    (item: KubernetesSecretSummary) => {
      setDeleteTarget(item);
      setDeletePreviewed(false);
      remove.reset();
    },
    [remove],
  );

  const columns = useMemo<ColumnDef<KubernetesSecretSummary, unknown>[]>(
    () => [
      {
        header: "名称",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground font-medium">{row.original.name}</span>
            <span className="zke-mono text-subtle-foreground text-xs">
              {row.original.uid || "UID 尚未分配"}
            </span>
          </div>
        ),
      },
      {
        header: "键",
        cell: ({ row }) => <KeysCell item={row.original} />,
      },
      {
        header: "类型",
        size: 200,
        cell: ({ row }) => (
          <span className="zke-mono text-muted-foreground text-xs break-all">
            {row.original.type || "Opaque"}
          </span>
        ),
      },
      {
        header: "大小",
        size: 120,
        cell: ({ row }) => (
          <span className="zke-tnum text-muted-foreground text-xs">
            {formatBytes(row.original.data_bytes)}
          </span>
        ),
      },
      {
        header: "不可变",
        size: 100,
        cell: ({ row }) =>
          row.original.immutable ? (
            <Badge tone="warning">
              <StatusDot tone="warning" />
              不可变
            </Badge>
          ) : (
            <span className="text-subtle-foreground text-xs">否</span>
          ),
      },
      {
        header: "创建时间",
        size: 170,
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs">
            {formatAbsolute(row.original.creation_timestamp)}
          </span>
        ),
      },
      {
        id: "actions",
        header: "",
        size: 88,
        cell: ({ row }) => (
          <div className="flex justify-end gap-0.5" onClick={(event) => event.stopPropagation()}>
            {canUpdate ? (
              // An immutable Secret cannot be edited at all — Kubernetes
              // rejects the write. Showing a live button that always fails would
              // be worse than showing why it is unavailable.
              <HintTooltip label={row.original.immutable ? "不可变的 Secret 无法修改内容" : "编辑"}>
                <span>
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={`编辑 ${row.original.name}`}
                    disabled={row.original.immutable}
                    onClick={() => setEditingName(row.original.name)}
                  >
                    {row.original.immutable ? <Lock /> : <Pencil />}
                  </Button>
                </span>
              </HintTooltip>
            ) : null}
            {canDelete && row.original.uid ? (
              <RowDeleteAction name={row.original.name} onDelete={() => openDelete(row.original)} />
            ) : null}
          </div>
        ),
      },
    ],
    [canUpdate, canDelete, openDelete],
  );

  // The confirmation lives outside the branch that picks a view. It is opened
  // from the list and from the detail page alike, and JSX that exists only in
  // the list's branch cannot open over the detail — the operator would have to
  // go back before the dialog appeared, by which point it is confirming an
  // object they can no longer see.
  const deleteDialog = (
    <SensitiveActionDialog
      open={deleteTarget !== null}
      onOpenChange={(open) => !open && setDeleteTarget(null)}
      title="删除 Secret"
      description={
        deletePreviewed
          ? "DryRun 预检已通过。再次确认将提交实际删除。"
          : "首次点击只执行服务端 DryRun 预检；通过后才能实际删除。"
      }
      scopeLines={[
        { label: "集群", name: clusterName, id: clusterId },
        { label: "命名空间", name: namespace },
        { label: "Secret", name: deleteTarget?.name ?? "", id: deleteTarget?.uid },
      ]}
      impacts={[
        "引用该 Secret 的 Pod 在重启或重新调度前通常不受影响，但之后会因缺少配置而无法启动。",
        "以 Volume 方式挂载的内容会在 kubelet 下一次同步时消失。",
        "请求携带该对象当前的 UID 与 resourceVersion 前置条件，期间对象若已变化或被重建，删除会被拒绝。",
      ]}
      confirmationText={deletePreviewed ? deleteTarget?.name : undefined}
      confirmLabel={deletePreviewed ? "确认删除" : "执行 DryRun 预检"}
      destructive
      pending={remove.isPending}
      error={remove.error}
      onConfirm={() => {
        if (!deleteTarget) return;
        const dryRun = !deletePreviewed;
        void remove
          .mutateAsync({
            clusterId,
            namespace,
            name: deleteTarget.name,
            uid: deleteTarget.uid,
            resourceVersion: deleteTarget.resource_version,
            dryRun,
            idempotencyKey: dryRun ? deletePreviewKey : deleteApplyKey,
          })
          .then(() => {
            if (dryRun) {
              setDeletePreviewed(true);
              toast.success("Secret 删除 DryRun 预检已通过");
              return;
            }
            toast.success(`Secret ${deleteTarget.name} 已提交删除`);
            if (detailName === deleteTarget.name) {
              setDetailName(null);
            }
            setDeleteTarget(null);
          })
          .catch(() => undefined);
      }}
    />
  );

  if (yamlTarget) {
    return (
      <YamlEditorView
        identity={{ kind: "secret", clusterId, namespace, name: yamlTarget.name }}
        clusterName={clusterName}
        kindLabel="Secret"
        canUpdate={canUpdate}
        readOnlyReason={yamlTarget.immutable ? "该 Secret 不可变，内容只能删除后重建。" : undefined}
        impacts={[
          "整份文档会替换集群中的现有 Secret，未在文档中出现的键将被移除。",
          "服务端会核对文档内的 UID 与 resourceVersion；期间对象若已变化，本次更新会被拒绝而不是覆盖。",
          "引用该 Secret 的 Pod 在重启或重新调度前通常不受影响；以 Volume 挂载的内容会在 kubelet 下一次同步时更新。",
        ]}
        onBack={() => setYamlTarget(null)}
      />
    );
  }

  // The form takes over the section rather than sitting over the list: the list
  // is of no use while a configuration is being written.
  if (creating || editingName) {
    return (
      <SecretForm
        clusterId={clusterId}
        clusterName={clusterName}
        namespace={namespace}
        editingName={editingName}
        onClose={() => {
          setCreating(false);
          setEditingName(null);
        }}
      />
    );
  }

  if (detailName) {
    return (
      <>
        <SecretDetailView
          clusterId={clusterId}
          namespace={namespace}
          name={detailName}
          canUpdate={canUpdate}
          canDelete={canDelete}
          // The detail stays open underneath, so leaving the form returns to the
          // object that was being read rather than to the list.
          onEdit={() => setEditingName(detailName)}
          onOpenYaml={setYamlTarget}
          onDelete={openDelete}
          onBack={() => setDetailName(null)}
        />
        {deleteDialog}
      </>
    );
  }

  const nextToken = list.data?.continue_token ?? "";

  return (
    <div className="flex h-full min-h-0 flex-col">
      {tabs}
      <SectionToolbarActions>
        <RefreshAction isFetching={list.isFetching} onRefresh={() => void list.refetch()} />
        {canCreate ? (
          <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
            <Plus />
            创建 Secret
          </Button>
        ) : null}
      </SectionToolbarActions>
      <DataTable
        columns={columns}
        data={list.data?.secrets}
        isLoading={list.isLoading}
        isFetching={list.isFetching}
        error={list.error}
        onRetry={() => void list.refetch()}
        onRowClick={(item) => setDetailName(item.name)}
        rowKey={(item) => item.uid || item.name}
        emptyTitle="该命名空间没有 Secret"
        emptyDescription={`${namespace} 中没有可见的 Secret。属于 ZKE 安装本身的 Secret 不在此列出。`}
        continuePagination={{
          pageIndex: pager.pageIndex,
          nextToken,
          onPrevious: pager.goPrevious,
          onNext: pager.goNext,
        }}
      />

      {deleteDialog}
    </div>
  );
}

/** Key names, which is all the list carries — the values stay on the server. */
function KeysCell({ item }: { item: KubernetesSecretSummary }) {
  const keys = item.data_keys;
  if (keys.length === 0) {
    return <span className="text-subtle-foreground text-xs">无键</span>;
  }
  return (
    <div className="flex flex-col gap-0.5">
      <span className="zke-mono text-muted-foreground text-xs break-all">
        {keys.slice(0, 4).join(" · ")}
        {keys.length > 4 ? ` +${keys.length - 4}` : ""}
      </span>
    </div>
  );
}

function SecretDetailView({
  clusterId,
  namespace,
  name,
  canUpdate,
  canDelete,
  onEdit,
  onOpenYaml,
  onDelete,
  onBack,
}: {
  clusterId: string;
  namespace: string;
  name: string;
  canUpdate: boolean;
  canDelete: boolean;
  onEdit: () => void;
  onOpenYaml: (item: KubernetesSecretSummary) => void;
  onDelete: (item: KubernetesSecretSummary) => void;
  onBack: () => void;
}) {
  const detail = useSecret(clusterId, namespace, name);
  const item = detail.data;

  return (
    <div className="grid gap-3">
      <PageHeader
        title={name}
        onBack={onBack}
        actions={
          <>
            {item ? (
              <Button size="sm" variant="secondary" onClick={() => onOpenYaml(item)}>
                <FileCode />
                YAML
              </Button>
            ) : null}
            {canUpdate && item && !item.immutable ? (
              <Button size="sm" variant="secondary" onClick={onEdit}>
                <Pencil />
                编辑
              </Button>
            ) : null}
            {/* An immutable Secret cannot be changed but can be removed: that
                is the only way to replace one. */}
            {canDelete && item?.uid ? (
              <DetailDeleteAction name={name} onDelete={() => onDelete(item)} />
            ) : null}
          </>
        }
      />
      {detail.error ? (
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      ) : detail.isLoading || !item ? (
        <LoadingState />
      ) : (
        <SecretCards item={item} />
      )}
    </div>
  );
}

function SecretCards({ item }: { item: KubernetesSecretDetail }) {
  const entries = Object.entries(item.data);

  return (
    <div className="grid gap-3">
      {item.immutable ? (
        <Alert tone="warning">
          该 Secret 被标记为不可变：Kubernetes 不允许修改它的内容，也不允许把 immutable 改回 false。
          需要变更时只能删除后重建。
        </Alert>
      ) : null}

      <div className="grid gap-3 md:grid-cols-2">
        <DetailCard title="概览">
          <DetailRow label="名称" value={item.name} />
          <DetailRow label="命名空间" value={item.namespace} />
          <DetailRow label="类型" value={item.type || "Opaque"} />
          <DetailRow
            label="UID"
            value={<span className="zke-mono text-xs break-all">{item.uid || "—"}</span>}
          />
          <DetailRow
            label="版本"
            value={<span className="zke-mono text-xs">{item.resource_version || "—"}</span>}
          />
          <DetailRow label="不可变" value={item.immutable ? "是" : "否"} />
          <DetailRow
            label="大小"
            value={<span className="zke-tnum">{formatBytes(item.data_bytes)}</span>}
          />
          <DetailRow label="创建时间" value={formatAbsolute(item.creation_timestamp)} />
        </DetailCard>

        <DetailCard title="标签">
          <DetailKeyValues entries={item.labels} />
        </DetailCard>
        <DetailCard title="注解">
          <DetailKeyValues entries={item.annotations} />
        </DetailCard>
      </div>

      <DetailCard title="数据">
        {entries.length === 0 ? (
          <DetailRow label="数据" value="—" />
        ) : (
          <div className="grid gap-4 py-1">
            {entries.map(([key, value]) => (
              <SecretValue key={key} name={key} value={value} />
            ))}
          </div>
        )}
      </DetailCard>
    </div>
  );
}

/*
 * One value, hidden until asked for.
 *
 * Masked by default rather than shown: this page is opened to check a name or a
 * size far more often than to read a credential, and a screen that renders
 * every password of a namespace the moment it loads is one that leaks them to
 * whoever is standing behind the operator. Revealing is per key and resets when
 * the page is left; nothing is written to storage or the URL.
 *
 * Values whose bytes are not UTF-8 text — certificates, key files — are not
 * rendered at all. Any text the browser produced from those bytes would be a
 * guess, and the size is the useful fact.
 */
function SecretValue({ name, value }: { name: string; value: string }) {
  const [revealed, setRevealed] = useState(false);
  const text = useMemo(() => decodeUtf8Base64(value), [value]);

  return (
    <div className="grid gap-1">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <span className="zke-mono text-foreground text-xs break-all">{name}</span>
        <div className="flex items-center gap-2">
          <span className="text-subtle-foreground zke-tnum text-xs">
            {formatBytes(base64Bytes(value))}
          </span>
          {text === null ? null : (
            <Button
              size="sm"
              variant="secondary"
              aria-pressed={revealed}
              onClick={() => setRevealed((current) => !current)}
            >
              {revealed ? <EyeOff /> : <Eye />}
              {revealed ? "隐藏" : "显示"}
            </Button>
          )}
          <CopyButton value={text ?? value} label={text === null ? "复制 Base64" : "复制"} />
        </div>
      </div>
      {text === null ? (
        <span className="text-muted-foreground text-xs">
          取值不是 UTF-8 文本，未在此处渲染；「复制 Base64」可取回原始内容。
        </span>
      ) : (
        <pre
          className={cn(
            "border-border bg-surface-muted rounded-panel zke-mono text-muted-foreground max-h-64 overflow-auto border p-2 text-xs leading-relaxed whitespace-pre",
            !revealed &&
              "text-transparent select-none [text-shadow:0_0_8px_var(--muted-foreground)]",
          )}
          aria-label={revealed ? name : `${name}（已遮蔽）`}
        >
          {text}
        </pre>
      )}
    </div>
  );
}

/** The text a Base64 value holds, or null when its bytes are not UTF-8 text. */
function decodeUtf8Base64(value: string): string | null {
  try {
    const binary = atob(value);
    const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0));
    const decoded = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
    // A control character means the bytes are data that happens to decode, and
    // a <pre> would neither show it nor give it back unchanged. Checked by code
    // point rather than by a regular expression, which cannot carry these
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

/** Decoded length of a standard padded Base64 string, without decoding it. */
function base64Bytes(value: string): number {
  const trimmed = value.trim();
  if (trimmed === "") {
    return 0;
  }
  const padding = trimmed.endsWith("==") ? 2 : trimmed.endsWith("=") ? 1 : 0;
  return Math.max(0, (trimmed.length * 3) / 4 - padding);
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) {
    return `${Math.round(bytes)} B`;
  }
  if (bytes < 1024 * 1024) {
    return `${(bytes / 1024).toFixed(1)} KiB`;
  }
  return `${(bytes / (1024 * 1024)).toFixed(2)} MiB`;
}
