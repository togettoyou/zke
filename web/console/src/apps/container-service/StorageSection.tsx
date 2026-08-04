import { useEffect, useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { FileCode, Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import {
  isNamespacedStorage,
  useDeleteStorageResource,
  useStorageResource,
  useStorageResources,
} from "@/api/queries/storage";
import type {
  KubernetesStorageResource,
  KubernetesStorageResourceDetail,
  KubernetesStorageResourceSummary,
} from "@/api/types";
import { PageHeader, SectionToolbarActions } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
import {
  DetailCard,
  DetailConditions,
  DetailKeyValues,
  DetailRow,
} from "@/components/common/detail";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { RefreshAction } from "@/components/common/refresh-action";
import { ErrorState, LoadingState } from "@/components/common/state";
import { StatusBadge } from "@/components/common/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/misc";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { StorageCreateForm } from "./StorageCreateForm";
import { StorageUpdateDialog } from "./StorageUpdateDialog";
import { useContinuePagination } from "./use-continue-pagination";
import type { ClusterSectionProps } from "./types";
import { YamlEditorView } from "./YamlEditorView";
import { STORAGE_TYPES, storageIdentity, storageKindLabel } from "./storage-catalog";

const PAGE_SIZE = 50;

type StorageSectionProps = ClusterSectionProps & {
  /** Only the PersistentVolumeClaim tab is scoped by it. */
  namespace: string;
  /**
   * Told to the shell whenever the active tab changes, so the toolbar's
   * Namespace picker appears exactly while it scopes something.
   */
  onNamespaceScopeChange: (namespaced: boolean) => void;
};

/**
 * PersistentVolumes, PersistentVolumeClaims and StorageClasses.
 *
 * The three do not share a scope: PV and StorageClass are cluster objects and
 * PVC is namespaced, which the Server enforces as two separate route families.
 * The section follows that rather than papering over it — the Namespace picker
 * appears only on the tab it applies to.
 */
export function StorageSection({
  clusterId,
  clusterName,
  namespace,
  tenantId,
  projectId,
  onNamespaceScopeChange,
}: StorageSectionProps) {
  const { permissions } = useSessionContext();
  const [resource, setResource] = useState<KubernetesStorageResource>("persistentvolumes");
  const namespaced = isNamespacedStorage(resource);
  const pager = useContinuePagination(`${clusterId}/${namespaced ? namespace : ""}/${resource}`);
  const list = useStorageResources(clusterId, namespace, resource, {
    limit: PAGE_SIZE,
    ...(pager.token ? { continue: pager.token } : {}),
  });
  const [detailName, setDetailName] = useState<string | null>(null);
  const [yamlName, setYamlName] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<KubernetesStorageResourceSummary | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<KubernetesStorageResourceSummary | null>(null);
  const [deletePreviewed, setDeletePreviewed] = useState(false);
  const deletePreviewKey = useSubmissionKey(deleteTarget !== null);
  const deleteApplyKey = useSubmissionKey(deleteTarget !== null);
  const remove = useDeleteStorageResource();

  useEffect(() => onNamespaceScopeChange(namespaced), [namespaced, onNamespaceScopeChange]);

  const projectScope = { type: "project" as const, tenantId, projectId };
  const canCreate = permissions.can("cluster.resource.create", projectScope);
  const canUpdate = permissions.can("cluster.resource.update", projectScope);
  const canDelete = permissions.can("cluster.resource.delete", projectScope);

  const columns = useMemo<ColumnDef<KubernetesStorageResourceSummary, unknown>[]>(
    () => [
      {
        header: "名称",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground font-medium break-all">{row.original.name}</span>
            <span className="zke-mono text-subtle-foreground text-xs">
              {row.original.uid || "UID 尚未分配"}
            </span>
          </div>
        ),
      },
      ...typeColumns(resource),
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
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={`编辑 ${row.original.name}`}
                onClick={() => setEditing(row.original)}
              >
                <Pencil />
              </Button>
            ) : null}
            {canDelete && row.original.uid ? (
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={`删除 ${row.original.name}`}
                onClick={() => {
                  setDeleteTarget(row.original);
                  setDeletePreviewed(false);
                  remove.reset();
                }}
              >
                <Trash2 />
              </Button>
            ) : null}
          </div>
        ),
      },
    ],
    [resource, canUpdate, canDelete, remove],
  );

  if (yamlName) {
    return (
      <YamlEditorView
        identity={{
          clusterId,
          ...storageIdentity(resource),
          ...(namespaced ? { namespace } : {}),
          name: yamlName,
        }}
        clusterName={clusterName}
        kindLabel={storageKindLabel(resource)}
        canUpdate={canUpdate}
        onBack={() => setYamlName(null)}
      />
    );
  }

  if (detailName) {
    return (
      <StorageDetailView
        clusterId={clusterId}
        namespace={namespace}
        resource={resource}
        name={detailName}
        canUpdate={canUpdate}
        onEdit={setEditing}
        onOpenYaml={() => setYamlName(detailName)}
        onBack={() => setDetailName(null)}
      />
    );
  }

  const nextToken = list.data?.continue_token ?? "";
  const waitingForNamespace = namespaced && namespace === "";

  return (
    <div className="flex h-full min-h-0 flex-col">
      <SectionToolbarActions>
        <RefreshAction isFetching={list.isFetching} onRefresh={() => void list.refetch()} />
        {canCreate && !waitingForNamespace ? (
          <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
            <Plus />
            创建 {storageKindLabel(resource)}
          </Button>
        ) : null}
      </SectionToolbarActions>
      <Tabs
        value={resource}
        onValueChange={(value) => {
          setResource(value as KubernetesStorageResource);
          setDetailName(null);
        }}
        className="flex min-h-0 flex-1 flex-col"
      >
        <TabsList className="w-fit">
          {STORAGE_TYPES.map((type) => (
            <TabsTrigger key={type.resource} value={type.resource}>
              {type.label}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value={resource} className="flex min-h-0 flex-1 flex-col">
          {waitingForNamespace ? (
            <Alert tone="info">
              PersistentVolumeClaim 按命名空间定域，正在等待工具栏的命名空间选择器解析出一个可用的
              命名空间。若该集群没有当前身份可见的命名空间，这里会一直为空。
            </Alert>
          ) : (
            <DataTable
              columns={columns}
              data={list.data?.resources}
              isLoading={list.isLoading}
              isFetching={list.isFetching}
              error={list.error}
              onRetry={() => void list.refetch()}
              onRowClick={(item) => setDetailName(item.name)}
              rowKey={(item) => item.uid || item.name}
              emptyTitle={`该集群没有 ${storageKindLabel(resource)}`}
              emptyDescription={
                namespaced
                  ? `${namespace} 中没有可见的 ${storageKindLabel(resource)}。`
                  : `当前筛选范围内没有可见的 ${storageKindLabel(resource)}。`
              }
              continuePagination={{
                pageIndex: pager.pageIndex,
                nextToken,
                onPrevious: pager.goPrevious,
                onNext: pager.goNext,
              }}
            />
          )}
        </TabsContent>
      </Tabs>

      {creating ? (
        <StorageCreateForm
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          resource={resource}
          onClose={() => setCreating(false)}
        />
      ) : null}

      {editing ? (
        <StorageUpdateDialog
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          resource={resource}
          target={editing}
          onClose={() => setEditing(null)}
        />
      ) : null}

      <SensitiveActionDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title={`删除 ${storageKindLabel(resource)}`}
        description={
          deletePreviewed
            ? "DryRun 已通过。再次确认将提交实际删除。"
            : "首次点击只执行服务端 DryRun；预检通过后才能实际删除。"
        }
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          ...(namespaced ? [{ label: "命名空间", name: namespace }] : []),
          {
            label: storageKindLabel(resource),
            name: deleteTarget?.name ?? "",
            id: deleteTarget?.uid,
          },
        ]}
        impacts={deleteImpacts(resource)}
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
              resource,
              name: deleteTarget.name,
              uid: deleteTarget.uid,
              resourceVersion: deleteTarget.resource_version,
              dryRun,
              idempotencyKey: dryRun ? deletePreviewKey : deleteApplyKey,
            })
            .then(() => {
              if (dryRun) {
                setDeletePreviewed(true);
                toast.success("删除 DryRun 已通过");
                return;
              }
              toast.success(`${storageKindLabel(resource)} ${deleteTarget.name} 已提交删除`);
              if (detailName === deleteTarget.name) {
                setDetailName(null);
              }
              setDeleteTarget(null);
            })
            .catch(() => undefined);
        }}
      />
    </div>
  );
}

function deleteImpacts(resource: KubernetesStorageResource): string[] {
  const precondition =
    "请求携带该对象当前的 UID 与 resourceVersion 前置条件，期间对象若已变化或被重建，删除会被拒绝。";
  if (resource === "persistentvolumes") {
    return [
      "删除 PV 后，底层存储是否一并销毁取决于它的回收策略：Delete 会销毁数据，Retain 会保留但不再被 Kubernetes 管理。",
      "仍被 PVC 绑定的 PV 会保持 Terminating，直到绑定解除。",
      precondition,
    ];
  }
  if (resource === "persistentvolumeclaims") {
    return [
      "删除 PVC 会解除与 PV 的绑定；PV 的回收策略决定数据是被保留还是被销毁。",
      "仍在被 Pod 使用的 PVC 会保持 Terminating，直到使用它的 Pod 结束。",
      precondition,
    ];
  }
  return [
    "删除 StorageClass 不影响已经创建的 PV 和 PVC，但之后不能再用它动态制备新卷。",
    "引用该 StorageClass 且尚未绑定的 PVC 将一直处于 Pending。",
    precondition,
  ];
}

/** The columns that only make sense for one type. */
function typeColumns(
  resource: KubernetesStorageResource,
): ColumnDef<KubernetesStorageResourceSummary, unknown>[] {
  if (resource === "persistentvolumes") {
    return [
      {
        header: "状态",
        size: 110,
        cell: ({ row }) => (
          <StatusBadge kind="volume" value={row.original.persistent_volume?.phase ?? ""} />
        ),
      },
      {
        header: "容量",
        size: 100,
        cell: ({ row }) => (
          <span className="zke-tnum text-muted-foreground text-xs">
            {row.original.persistent_volume?.capacity || "—"}
          </span>
        ),
      },
      {
        header: "访问模式",
        size: 150,
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs break-all">
            {row.original.persistent_volume?.access_modes.join(", ") || "—"}
          </span>
        ),
      },
      {
        header: "回收策略",
        size: 100,
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs">
            {row.original.persistent_volume?.reclaim_policy || "—"}
          </span>
        ),
      },
      {
        header: "绑定",
        cell: ({ row }) => {
          const claim = row.original.persistent_volume?.claim_ref;
          return (
            <span className="text-muted-foreground text-xs break-all">
              {claim?.name ? `${claim.namespace}/${claim.name}` : "未绑定"}
            </span>
          );
        },
      },
    ];
  }
  if (resource === "persistentvolumeclaims") {
    return [
      {
        header: "状态",
        size: 110,
        cell: ({ row }) => (
          <StatusBadge kind="volume" value={row.original.persistent_volume_claim?.phase ?? ""} />
        ),
      },
      {
        header: "容量",
        size: 140,
        cell: ({ row }) => {
          const claim = row.original.persistent_volume_claim;
          return (
            <div className="flex flex-col gap-0.5">
              <span className="zke-tnum text-foreground">{claim?.capacity || "尚未分配"}</span>
              <span className="text-subtle-foreground text-xs">
                申请 {claim?.requested_capacity || "—"}
              </span>
            </div>
          );
        },
      },
      {
        header: "StorageClass",
        size: 150,
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs break-all">
            {row.original.persistent_volume_claim?.storage_class_name ?? "默认"}
          </span>
        ),
      },
      {
        header: "卷",
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs break-all">
            {row.original.persistent_volume_claim?.volume_name || "未绑定"}
          </span>
        ),
      },
    ];
  }
  return [
    {
      header: "Provisioner",
      cell: ({ row }) => (
        <span className="zke-mono text-muted-foreground text-xs break-all">
          {row.original.storage_class?.provisioner || "—"}
        </span>
      ),
    },
    {
      header: "绑定模式",
      size: 170,
      cell: ({ row }) => (
        <span className="text-muted-foreground text-xs">
          {row.original.storage_class?.volume_binding_mode || "—"}
        </span>
      ),
    },
    {
      header: "回收策略",
      size: 100,
      cell: ({ row }) => (
        <span className="text-muted-foreground text-xs">
          {row.original.storage_class?.reclaim_policy || "—"}
        </span>
      ),
    },
    {
      header: "标记",
      size: 140,
      cell: ({ row }) => (
        <div className="flex flex-col items-start gap-0.5">
          {row.original.storage_class?.default ? <Badge tone="primary">默认</Badge> : null}
          {row.original.storage_class?.allow_volume_expansion ? (
            <Badge tone="info">可扩容</Badge>
          ) : null}
        </div>
      ),
    },
  ];
}

function StorageDetailView({
  clusterId,
  namespace,
  resource,
  name,
  canUpdate,
  onEdit,
  onOpenYaml,
  onBack,
}: {
  clusterId: string;
  namespace: string;
  resource: KubernetesStorageResource;
  name: string;
  canUpdate: boolean;
  onEdit: (item: KubernetesStorageResourceSummary) => void;
  onOpenYaml: () => void;
  onBack: () => void;
}) {
  const detail = useStorageResource(clusterId, namespace, resource, name);
  const item = detail.data;

  return (
    <div className="grid gap-3">
      <PageHeader
        title={name}
        onBack={onBack}
        actions={
          <>
            {canUpdate && item ? (
              <Button size="sm" variant="secondary" onClick={() => onEdit(item)}>
                <Pencil />
                编辑
              </Button>
            ) : null}
            <Button size="sm" variant="secondary" onClick={onOpenYaml}>
              <FileCode />
              YAML
            </Button>
          </>
        }
      />
      {detail.error ? (
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      ) : detail.isLoading || !item ? (
        <LoadingState />
      ) : (
        <StorageDetailCards item={item} />
      )}
    </div>
  );
}

function StorageDetailCards({ item }: { item: KubernetesStorageResourceDetail }) {
  const volume = item.persistent_volume;
  const volumeDetail = item.persistent_volume_detail;
  const claim = item.persistent_volume_claim;
  const claimDetail = item.persistent_volume_claim_detail;
  const storageClass = item.storage_class;
  const classDetail = item.storage_class_detail;

  return (
    <div className="grid gap-3 md:grid-cols-2">
      <DetailCard title="概览">
        <DetailRow label="名称" value={item.name} />
        <DetailRow
          label="类型"
          value={
            <span className="zke-mono text-xs">
              {item.api_version} · {item.kind}
            </span>
          }
        />
        {item.namespace ? <DetailRow label="命名空间" value={item.namespace} /> : null}
        <DetailRow
          label="UID"
          value={<span className="zke-mono text-xs break-all">{item.uid || "—"}</span>}
        />
        <DetailRow
          label="版本"
          value={<span className="zke-mono text-xs">{item.resource_version || "—"}</span>}
        />
        <DetailRow label="创建时间" value={formatAbsolute(item.creation_timestamp)} />
      </DetailCard>

      {volume ? (
        <DetailCard title="PersistentVolume">
          <DetailRow label="状态" value={<StatusBadge kind="volume" value={volume.phase} />} />
          <DetailRow label="容量" value={volume.capacity || "—"} />
          <DetailRow label="访问模式" value={volume.access_modes.join(", ") || "—"} />
          <DetailRow label="回收策略" value={volume.reclaim_policy || "—"} />
          <DetailRow label="卷模式" value={volume.volume_mode || "—"} />
          <DetailRow label="StorageClass" value={volume.storage_class_name || "—"} />
          <DetailRow label="来源类型" value={volume.source_type || "—"} />
          <DetailRow
            label="绑定"
            value={
              volume.claim_ref?.name
                ? `${volume.claim_ref.namespace}/${volume.claim_ref.name}`
                : "未绑定"
            }
          />
          {volumeDetail?.reason || volumeDetail?.message ? (
            <DetailRow
              label="原因"
              value={
                <span className="break-words">
                  {[volumeDetail.reason, volumeDetail.message].filter(Boolean).join(" · ")}
                </span>
              }
            />
          ) : null}
          {volumeDetail?.mount_options.length ? (
            <DetailRow label="挂载选项" value={volumeDetail.mount_options.join(", ")} />
          ) : null}
        </DetailCard>
      ) : null}

      {volumeDetail?.source ? (
        <DetailCard title="卷来源">
          <DetailRow label="类型" value={volumeDetail.source.type} />
          {volumeDetail.source.csi ? (
            <>
              <DetailRow label="驱动" value={volumeDetail.source.csi.driver} />
              <DetailRow
                label="卷句柄"
                value={
                  <span className="zke-mono text-xs break-all">
                    {volumeDetail.source.csi.volume_handle}
                  </span>
                }
              />
              <DetailRow label="文件系统" value={volumeDetail.source.csi.fs_type || "—"} />
              <DetailRow label="只读" value={volumeDetail.source.csi.read_only ? "是" : "否"} />
            </>
          ) : null}
          {volumeDetail.source.nfs ? (
            <>
              <DetailRow label="服务器" value={volumeDetail.source.nfs.server} />
              <DetailRow
                label="路径"
                value={
                  <span className="zke-mono text-xs break-all">{volumeDetail.source.nfs.path}</span>
                }
              />
              <DetailRow label="只读" value={volumeDetail.source.nfs.read_only ? "是" : "否"} />
            </>
          ) : null}
          {volumeDetail.source.local ? (
            <>
              <DetailRow
                label="路径"
                value={
                  <span className="zke-mono text-xs break-all">
                    {volumeDetail.source.local.path}
                  </span>
                }
              />
              <DetailRow label="文件系统" value={volumeDetail.source.local.fs_type || "—"} />
            </>
          ) : null}
        </DetailCard>
      ) : null}

      {claim ? (
        <DetailCard title="PersistentVolumeClaim">
          <DetailRow label="状态" value={<StatusBadge kind="volume" value={claim.phase} />} />
          <DetailRow label="申请容量" value={claim.requested_capacity || "—"} />
          <DetailRow label="已分配容量" value={claim.capacity || "尚未分配"} />
          <DetailRow label="访问模式" value={claim.access_modes.join(", ") || "—"} />
          <DetailRow label="卷模式" value={claim.volume_mode || "—"} />
          <DetailRow label="StorageClass" value={claim.storage_class_name ?? "默认"} />
          <DetailRow label="绑定的卷" value={claim.volume_name || "未绑定"} />
        </DetailCard>
      ) : null}

      {claimDetail?.conditions.length ? (
        <DetailCard title="条件">
          <DetailConditions conditions={claimDetail.conditions} />
        </DetailCard>
      ) : null}

      {storageClass ? (
        <DetailCard title="StorageClass">
          <DetailRow
            label="Provisioner"
            value={<span className="zke-mono text-xs break-all">{storageClass.provisioner}</span>}
          />
          <DetailRow label="回收策略" value={storageClass.reclaim_policy || "—"} />
          <DetailRow label="绑定模式" value={storageClass.volume_binding_mode || "—"} />
          <DetailRow label="允许扩容" value={storageClass.allow_volume_expansion ? "是" : "否"} />
          <DetailRow label="集群默认" value={storageClass.default ? "是" : "否"} />
          {classDetail?.mount_options.length ? (
            <DetailRow label="挂载选项" value={classDetail.mount_options.join(", ")} />
          ) : null}
        </DetailCard>
      ) : null}

      {classDetail && Object.keys(classDetail.parameters).length > 0 ? (
        <DetailCard title="参数">
          <DetailKeyValues entries={classDetail.parameters} />
        </DetailCard>
      ) : null}

      <DetailCard title="标签">
        <DetailKeyValues entries={item.labels} />
      </DetailCard>
      <DetailCard title="注解">
        <DetailKeyValues entries={item.annotations} />
      </DetailCard>
    </div>
  );
}
