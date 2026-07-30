import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { ArrowLeft, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import {
  useCreateNamespace,
  useDeleteNamespace,
  useNamespace,
  useNamespaces,
} from "@/api/queries/namespaces";
import type { KubernetesNamespaceDetail, KubernetesNamespaceSummary } from "@/api/types";
import { SectionTitle } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
import { DetailCard, DetailKeyValues, DetailRow } from "@/components/common/detail";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert } from "@/components/ui/misc";
import { errorMessage } from "@/api/errors";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { ContinuePager } from "./ContinuePager";
import { useContinuePagination } from "./use-continue-pagination";
import type { ClusterSectionProps } from "./types";

const PAGE_SIZE = 50;

export function NamespaceSection({
  clusterId,
  clusterName,
  tenantId,
  projectId,
}: ClusterSectionProps) {
  const { permissions } = useSessionContext();
  const pager = useContinuePagination(clusterId);
  const namespaces = useNamespaces(clusterId, {
    limit: PAGE_SIZE,
    ...(pager.token ? { continue: pager.token } : {}),
  });
  // Drilling into a Namespace keeps the list's paging and dialogs alive, so
  // coming back lands on the page the operator left rather than on page one.
  const [detailName, setDetailName] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [createName, setCreateName] = useState("");
  const [createPreview, setCreatePreview] = useState<KubernetesNamespaceDetail | null>(null);
  const create = useCreateNamespace();
  const createPreviewKey = useSubmissionKey(createOpen);
  const createApplyKey = useSubmissionKey(createPreview !== null);
  const [deleteTarget, setDeleteTarget] = useState<KubernetesNamespaceSummary | null>(null);
  const [deletePreviewed, setDeletePreviewed] = useState(false);
  const deletePreviewKey = useSubmissionKey(deleteTarget !== null);
  const deleteApplyKey = useSubmissionKey(deleteTarget !== null);
  const remove = useDeleteNamespace();

  const projectScope = { type: "project" as const, tenantId, projectId };
  const canCreate = permissions.can("cluster.resource.create", projectScope);
  const canDelete = permissions.can("cluster.resource.delete", projectScope);

  const columns = useMemo<ColumnDef<KubernetesNamespaceSummary, unknown>[]>(
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
        header: "状态",
        size: 120,
        cell: ({ row }) => (
          <Badge tone={row.original.phase === "Active" ? "success" : "neutral"}>
            <StatusDot tone={row.original.phase === "Active" ? "success" : "neutral"} />
            {row.original.phase || "Unknown"}
          </Badge>
        ),
      },
      {
        header: "标签",
        cell: ({ row }) => {
          const labels = Object.entries(row.original.labels);
          return (
            <span className="text-muted-foreground text-xs">
              {labels.length === 0
                ? "—"
                : labels
                    .slice(0, 2)
                    .map(([key, value]) => `${key}=${value}`)
                    .join(" · ")}
              {labels.length > 2 ? ` +${labels.length - 2}` : ""}
            </span>
          );
        },
      },
      {
        header: "创建时间",
        size: 180,
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs">
            {formatAbsolute(row.original.creation_timestamp)}
          </span>
        ),
      },
      {
        id: "actions",
        header: "",
        size: 48,
        cell: ({ row }) =>
          canDelete ? (
            <div onClick={(event) => event.stopPropagation()}>
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
            </div>
          ) : null,
      },
    ],
    [canDelete, remove],
  );

  const nextToken = namespaces.data?.continue_token ?? "";

  return (
    <>
      {detailName ? (
        <NamespaceDetailView
          clusterId={clusterId}
          clusterName={clusterName}
          name={detailName}
          onBack={() => setDetailName(null)}
        />
      ) : (
        <div className="flex h-full min-h-0 flex-col">
          <SectionTitle
            title={`命名空间 · ${clusterName}`}
            description="Namespace 是集群级对象；创建和删除先执行 Kubernetes 服务端 DryRun，再由操作者确认。"
            actions={
              canCreate ? (
                <Button
                  variant="primary"
                  size="sm"
                  onClick={() => {
                    setCreateName("");
                    setCreatePreview(null);
                    create.reset();
                    setCreateOpen(true);
                  }}
                >
                  <Plus />
                  创建命名空间
                </Button>
              ) : null
            }
          />
          <DataTable
            columns={columns}
            data={namespaces.data?.namespaces}
            isLoading={namespaces.isLoading}
            isFetching={namespaces.isFetching}
            error={namespaces.error}
            onRetry={() => void namespaces.refetch()}
            onRowClick={(namespace) => setDetailName(namespace.name)}
            rowKey={(namespace) => namespace.uid || namespace.name}
            emptyTitle="该集群没有命名空间"
            emptyDescription="当前筛选范围内没有可见的 Namespace。"
            toolbar={
              <ContinuePager
                pageIndex={pager.pageIndex}
                nextToken={nextToken}
                onPrevious={pager.goPrevious}
                onNext={pager.goNext}
              />
            }
          />
        </div>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>创建 Namespace</DialogTitle>
            <DialogDescription>
              第一步只执行服务端 DryRun，不会在集群中持久化对象。
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="namespace-name">名称</Label>
            <Input
              id="namespace-name"
              value={createName}
              autoComplete="off"
              spellCheck={false}
              placeholder="例如 model-serving"
              onChange={(event) => setCreateName(event.target.value)}
            />
          </div>
          <Alert tone="info" className="mt-3">
            目标集群：{clusterName}（{clusterId}）
          </Alert>
          {create.error ? (
            <Alert tone="danger" className="mt-3">
              {errorMessage(create.error)}
            </Alert>
          ) : null}
          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => setCreateOpen(false)}
              disabled={create.isPending}
            >
              取消
            </Button>
            <Button
              variant="primary"
              disabled={!createName.trim() || create.isPending}
              onClick={() => {
                void create
                  .mutateAsync({
                    clusterId,
                    name: createName.trim(),
                    dryRun: true,
                    idempotencyKey: createPreviewKey,
                  })
                  .then((result) => {
                    setCreateOpen(false);
                    setCreatePreview(result.namespace);
                  })
                  .catch(() => undefined);
              }}
            >
              {create.isPending ? "预检中…" : "执行 DryRun 预检"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <SensitiveActionDialog
        open={createPreview !== null}
        onOpenChange={(open) => !open && setCreatePreview(null)}
        title="确认创建 Namespace"
        description="DryRun 已通过。确认后将向同一集群发送实际创建请求。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "Namespace", name: createPreview?.name ?? createName },
        ]}
        impacts={[
          "将在目标集群持久化一个新的 Namespace。",
          "后续工作负载和权限可在该 Namespace 中定域。",
        ]}
        confirmLabel="确认创建"
        pending={create.isPending}
        error={create.error}
        onConfirm={() => {
          void create
            .mutateAsync({
              clusterId,
              name: createPreview?.name ?? createName.trim(),
              dryRun: false,
              idempotencyKey: createApplyKey,
            })
            .then((result) => {
              toast.success(`Namespace ${result.namespace.name} 已创建`);
              setCreatePreview(null);
            })
            .catch(() => undefined);
        }}
      />

      <SensitiveActionDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title="删除 Namespace"
        description={
          deletePreviewed
            ? "DryRun 已通过。再次确认将提交实际删除。"
            : "首次点击只执行服务端 DryRun；预检通过后才能实际删除。"
        }
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "Namespace", name: deleteTarget?.name ?? "", id: deleteTarget?.uid },
        ]}
        impacts={[
          "实际删除会终止并清理该 Namespace 中的 namespaced 资源。",
          "请求携带该 Namespace 当前的 UID 前置条件，避免误删同名重建的对象。",
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
              name: deleteTarget.name,
              uid: deleteTarget.uid,
              dryRun,
              idempotencyKey: dryRun ? deletePreviewKey : deleteApplyKey,
            })
            .then(() => {
              if (dryRun) {
                setDeletePreviewed(true);
                toast.success("Namespace 删除 DryRun 已通过");
                return;
              }
              toast.success(`Namespace ${deleteTarget.name} 已提交删除`);
              if (detailName === deleteTarget.name) {
                setDetailName(null);
              }
              setDeleteTarget(null);
            })
            .catch(() => undefined);
        }}
      />
    </>
  );
}

function NamespaceDetailView({
  clusterId,
  clusterName,
  name,
  onBack,
}: {
  clusterId: string;
  clusterName: string;
  name: string;
  onBack: () => void;
}) {
  const detail = useNamespace(clusterId, name);

  return (
    <div className="grid gap-3">
      <SectionTitle
        title={name}
        description={`读取自集群 ${clusterName}，仅展示 Kubernetes 返回的当前状态。`}
        actions={
          <Button size="sm" variant="secondary" onClick={onBack}>
            <ArrowLeft />
            返回列表
          </Button>
        }
      />
      {detail.error ? (
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      ) : detail.isLoading || !detail.data ? (
        <LoadingState />
      ) : (
        <div className="grid gap-3 md:grid-cols-2">
          <DetailCard title="概览">
            <DetailRow label="名称" value={detail.data.name} />
            <DetailRow
              label="UID"
              value={<span className="zke-mono text-xs break-all">{detail.data.uid || "—"}</span>}
            />
            <DetailRow
              label="版本"
              value={
                <span className="zke-mono text-xs">{detail.data.resource_version || "—"}</span>
              }
            />
            <DetailRow
              label="状态"
              value={
                <Badge tone={detail.data.phase === "Active" ? "success" : "neutral"}>
                  <StatusDot tone={detail.data.phase === "Active" ? "success" : "neutral"} />
                  {detail.data.phase || "Unknown"}
                </Badge>
              }
            />
            <DetailRow label="创建时间" value={formatAbsolute(detail.data.creation_timestamp)} />
            <DetailRow
              label="Finalizers"
              value={
                detail.data.finalizers.length === 0 ? (
                  "—"
                ) : (
                  <span className="zke-mono text-xs break-all">
                    {detail.data.finalizers.join(", ")}
                  </span>
                )
              }
            />
          </DetailCard>

          <DetailCard title="标签">
            <DetailKeyValues entries={detail.data.labels} />
          </DetailCard>

          <DetailCard title="注解">
            <DetailKeyValues entries={detail.data.annotations} />
          </DetailCard>
        </div>
      )}
    </div>
  );
}
