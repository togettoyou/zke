import { useCallback, useMemo, useState, type ReactNode } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { FileCode, Lock, Pencil, Plus } from "lucide-react";
import { toast } from "sonner";

import { useConfigMap, useConfigMaps, useDeleteConfigMap } from "@/api/queries/configmaps";
import type { KubernetesConfigMapDetail, KubernetesConfigMapSummary } from "@/api/types";
import { PageHeader, SectionToolbarActions } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
import { DetailDeleteAction, RowDeleteAction } from "@/components/common/delete-action";
import { DetailCard, DetailKeyValues, DetailRow } from "@/components/common/detail";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { RefreshAction } from "@/components/common/refresh-action";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/misc";
import { HintTooltip } from "@/components/ui/tooltip";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { ConfigMapForm } from "./ConfigMapForm";
import { useContinuePagination } from "./use-continue-pagination";
import type { ClusterSectionProps } from "./types";
import { namespaceMutationPermission } from "./namespace-permissions";
import { YamlEditorView } from "./YamlEditorView";

const PAGE_SIZE = 50;

type ConfigMapSectionProps = ClusterSectionProps & {
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
 * ConfigMaps of one Namespace of one Cluster.
 *
 * Secrets are deliberately absent: the Agent is not granted access to them, and
 * this section must not read as though a Secret view is merely missing.
 */
export function ConfigMapSection({
  clusterId,
  clusterName,
  namespace,
  tenantId,
  projectId,
  tabs,
}: ConfigMapSectionProps) {
  const { permissions } = useSessionContext();
  const pager = useContinuePagination(`${clusterId}/${namespace}`);
  const list = useConfigMaps(clusterId, namespace, {
    limit: PAGE_SIZE,
    ...(pager.token ? { continue: pager.token } : {}),
  });
  const [detailName, setDetailName] = useState<string | null>(null);
  const [yamlName, setYamlName] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [editingName, setEditingName] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<KubernetesConfigMapSummary | null>(null);
  const [deletePreviewed, setDeletePreviewed] = useState(false);
  const deletePreviewKey = useSubmissionKey(deleteTarget !== null);
  const deleteApplyKey = useSubmissionKey(deleteTarget !== null);
  const remove = useDeleteConfigMap();

  const projectScope = { type: "project" as const, tenantId, projectId };
  const canCreate = permissions.can(
    namespaceMutationPermission(namespace, "cluster.resource.create"),
    projectScope,
  );
  const canUpdate = permissions.can(
    namespaceMutationPermission(namespace, "cluster.resource.update"),
    projectScope,
  );
  const canDelete = permissions.can(
    namespaceMutationPermission(namespace, "cluster.resource.delete"),
    projectScope,
  );

  // Both the row action and the detail view open the same confirmation, so it is
  // one callback rather than two copies of the reset sequence.
  const openDelete = useCallback(
    (item: KubernetesConfigMapSummary) => {
      setDeleteTarget(item);
      setDeletePreviewed(false);
      remove.reset();
    },
    [remove],
  );

  const columns = useMemo<ColumnDef<KubernetesConfigMapSummary, unknown>[]>(
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
        header: "大小",
        size: 120,
        cell: ({ row }) => (
          <span className="zke-tnum text-muted-foreground text-xs">
            {formatBytes(row.original.data_bytes + row.original.binary_data_bytes)}
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
              // An immutable ConfigMap cannot be edited at all — Kubernetes
              // rejects the write. Showing a live button that always fails would
              // be worse than showing why it is unavailable.
              <HintTooltip
                label={row.original.immutable ? "不可变的 ConfigMap 无法修改内容" : "编辑"}
              >
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
      title="删除 ConfigMap"
      description={
        deletePreviewed
          ? "DryRun 预检已通过。再次确认将提交实际删除。"
          : "首次点击只执行服务端 DryRun 预检；通过后才能实际删除。"
      }
      scopeLines={[
        { label: "集群", name: clusterName, id: clusterId },
        { label: "命名空间", name: namespace },
        { label: "ConfigMap", name: deleteTarget?.name ?? "", id: deleteTarget?.uid },
      ]}
      impacts={[
        "引用该 ConfigMap 的 Pod 在重启或重新调度前通常不受影响，但之后会因缺少配置而无法启动。",
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
              toast.success("ConfigMap 删除 DryRun 预检已通过");
              return;
            }
            toast.success(`ConfigMap ${deleteTarget.name} 已提交删除`);
            if (detailName === deleteTarget.name) {
              setDetailName(null);
            }
            setDeleteTarget(null);
          })
          .catch(() => undefined);
      }}
    />
  );

  if (yamlName) {
    return (
      <YamlEditorView
        identity={{ clusterId, version: "v1", resource: "configmaps", namespace, name: yamlName }}
        clusterName={clusterName}
        kindLabel="ConfigMap"
        canUpdate={canUpdate}
        onBack={() => setYamlName(null)}
      />
    );
  }

  // The form takes over the section rather than sitting over the list: the list
  // is of no use while a configuration is being written.
  if (creating || editingName) {
    return (
      <ConfigMapForm
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
        <ConfigMapDetailView
          clusterId={clusterId}
          namespace={namespace}
          name={detailName}
          canUpdate={canUpdate}
          canDelete={canDelete}
          // The detail stays open underneath, so leaving the form returns to the
          // object that was being read rather than to the list.
          onEdit={() => setEditingName(detailName)}
          onOpenYaml={() => setYamlName(detailName)}
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
            创建 ConfigMap
          </Button>
        ) : null}
      </SectionToolbarActions>
      <DataTable
        columns={columns}
        data={list.data?.config_maps}
        isLoading={list.isLoading}
        isFetching={list.isFetching}
        error={list.error}
        onRetry={() => void list.refetch()}
        onRowClick={(item) => setDetailName(item.name)}
        rowKey={(item) => item.uid || item.name}
        emptyTitle="该命名空间没有 ConfigMap"
        emptyDescription={`${namespace} 中没有可见的 ConfigMap。`}
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
function KeysCell({ item }: { item: KubernetesConfigMapSummary }) {
  const keys = [...item.data_keys, ...item.binary_data_keys];
  if (keys.length === 0) {
    return <span className="text-subtle-foreground text-xs">无键</span>;
  }
  return (
    <div className="flex flex-col gap-0.5">
      <span className="zke-mono text-muted-foreground text-xs break-all">
        {keys.slice(0, 4).join(" · ")}
        {keys.length > 4 ? ` +${keys.length - 4}` : ""}
      </span>
      {item.binary_data_keys.length > 0 ? (
        <span className="text-subtle-foreground text-xs">
          其中 {item.binary_data_keys.length} 个为二进制
        </span>
      ) : null}
    </div>
  );
}

function ConfigMapDetailView({
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
  onOpenYaml: () => void;
  onDelete: (item: KubernetesConfigMapSummary) => void;
  onBack: () => void;
}) {
  const detail = useConfigMap(clusterId, namespace, name);
  const item = detail.data;

  return (
    <div className="grid gap-3">
      <PageHeader
        title={name}
        onBack={onBack}
        actions={
          <>
            <Button size="sm" variant="secondary" onClick={onOpenYaml}>
              <FileCode />
              YAML
            </Button>
            {canUpdate && item && !item.immutable ? (
              <Button size="sm" variant="secondary" onClick={onEdit}>
                <Pencil />
                编辑
              </Button>
            ) : null}
            {/* An immutable ConfigMap cannot be changed but can be removed:
                that is the only way to replace one. */}
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
        <ConfigMapCards item={item} />
      )}
    </div>
  );
}

function ConfigMapCards({ item }: { item: KubernetesConfigMapDetail }) {
  const dataEntries = Object.entries(item.data);
  const binaryEntries = Object.entries(item.binary_data);

  return (
    <div className="grid gap-3">
      {item.immutable ? (
        <Alert tone="warning">
          该 ConfigMap 被标记为不可变：Kubernetes 不允许修改它的内容，也不允许把 immutable 改回
          false。 需要变更时只能删除后重建。
        </Alert>
      ) : null}

      <div className="grid gap-3 md:grid-cols-2">
        <DetailCard title="概览">
          <DetailRow label="名称" value={item.name} />
          <DetailRow label="命名空间" value={item.namespace} />
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
            value={
              <span className="zke-tnum">
                {formatBytes(item.data_bytes + item.binary_data_bytes)}
                <span className="text-subtle-foreground ml-2 text-xs">
                  文本 {formatBytes(item.data_bytes)} · 二进制 {formatBytes(item.binary_data_bytes)}
                </span>
              </span>
            }
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
        {dataEntries.length === 0 ? (
          <DetailRow label="数据" value="—" />
        ) : (
          <div className="grid gap-3 py-1">
            {dataEntries.map(([key, value]) => (
              <div key={key} className="grid gap-1">
                <div className="flex items-baseline justify-between gap-2">
                  <span className="zke-mono text-foreground text-xs break-all">{key}</span>
                  <span className="text-subtle-foreground zke-tnum text-xs">
                    {formatBytes(new Blob([value]).size)}
                  </span>
                </div>
                {/* Values are shown as written: a config file is indentation and
                    line breaks, and soft-wrapping it would misrepresent both. */}
                <pre className="border-border bg-surface-muted rounded-panel zke-mono text-muted-foreground max-h-64 overflow-auto border p-2 text-xs leading-relaxed whitespace-pre">
                  {value}
                </pre>
              </div>
            ))}
          </div>
        )}
      </DetailCard>

      {binaryEntries.length > 0 ? (
        <DetailCard title="二进制数据">
          <div className="grid gap-1 py-1">
            {/* Binary values are not rendered: they are bytes, and any text the
                browser produced from them would be a guess. The size is the
                useful fact; the bytes themselves are in the YAML view. */}
            {binaryEntries.map(([key, value]) => (
              <DetailRow
                key={key}
                label={key}
                value={
                  <span className="text-muted-foreground text-xs">
                    {formatBytes(base64Bytes(value))} · Base64 编码，未在此处渲染
                  </span>
                }
              />
            ))}
          </div>
        </DetailCard>
      ) : null}
    </div>
  );
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
