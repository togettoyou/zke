import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { ChevronLeft, ChevronRight, FolderTree, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import {
  useCreateNamespace,
  useDeleteNamespace,
  useNamespace,
  useNamespaces,
} from "@/api/queries/namespaces";
import { useClusters } from "@/api/queries/clusters";
import type { KubernetesNamespaceDetail, KubernetesNamespaceSummary } from "@/api/types";
import { AppShell, ScopeRequired, SectionTitle } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { ErrorState, LoadingState } from "@/components/common/state";
import { StatusBadge } from "@/components/common/status";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { errorMessage } from "@/api/errors";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";
import { useScopeStore } from "@/scope/scope-store";

const NAV = [{ id: "namespaces", label: "命名空间", icon: FolderTree }];
const NAMESPACE_PAGE_SIZE = 50;

export function ContainerServiceApp() {
  const scope = useScopeStore((state) => state.scope);
  const clusters = useClusters(scope.projectId, {
    limit: 100,
    offset: 0,
    status: "active",
  });
  const [selectedClusterId, setSelectedClusterId] = useState("");
  const projectClusters = clusters.data?.clusters ?? [];
  // Only an online Agent can execute anything here, so only an online cluster
  // can be the current target. Offline clusters still appear in the picker —
  // disabled — because an operator looking for one needs to see that it is
  // known and offline rather than find it silently missing.
  const onlineClusters = projectClusters.filter(
    (cluster) => cluster.connection.status === "online",
  );
  const clusterId = onlineClusters.some((cluster) => cluster.id === selectedClusterId)
    ? selectedClusterId
    : (onlineClusters[0]?.id ?? "");

  if (!scope.projectId) {
    return <ScopeRequired />;
  }

  return (
    <AppShell
      nav={NAV}
      activeId="namespaces"
      onNavigate={() => undefined}
      toolbar={
        <>
          <span className="text-muted-foreground text-xs">目标集群</span>
          <Select
            value={clusterId}
            onValueChange={setSelectedClusterId}
            disabled={clusters.isLoading}
          >
            <SelectTrigger className="w-64" aria-label="目标集群">
              <SelectValue placeholder={clusters.isLoading ? "加载集群…" : "选择集群"} />
            </SelectTrigger>
            <SelectContent>
              {projectClusters.map((cluster) => {
                const online = cluster.connection.status === "online";
                return (
                  <SelectItem key={cluster.id} value={cluster.id} disabled={!online}>
                    <span className="flex items-center gap-2">
                      {cluster.name}
                      {online ? null : (
                        <StatusBadge kind="connection" value={cluster.connection.status} />
                      )}
                    </span>
                  </SelectItem>
                );
              })}
            </SelectContent>
          </Select>
          {clusterId ? (
            <span className="zke-mono text-subtle-foreground text-xs">{clusterId}</span>
          ) : null}
        </>
      }
      statusBar={
        clusterId ? "所有查询与变更均由所选集群的在线 Agent 定域执行" : "没有可执行请求的在线集群"
      }
    >
      {clusters.error ? (
        <ErrorState error={clusters.error} onRetry={() => void clusters.refetch()} />
      ) : clusters.isLoading ? (
        <LoadingState />
      ) : projectClusters.length === 0 ? (
        <NoTargetCluster
          title="该项目没有已接入的集群"
          description="请先在「集群接入管理」中接入并启用集群。"
        />
      ) : clusterId === "" ? (
        <NoTargetCluster
          title="该项目没有在线集群"
          description="容器服务的每个查询和变更都由目标集群的 Agent 定域执行，需要至少一个 Agent 处于在线状态。"
        />
      ) : (
        <NamespaceSection
          clusterId={clusterId}
          clusterName={
            projectClusters.find((cluster) => cluster.id === clusterId)?.name ?? clusterId
          }
          tenantId={scope.tenantId}
          projectId={scope.projectId}
        />
      )}
    </AppShell>
  );
}

function NoTargetCluster({ title, description }: { title: string; description: string }) {
  return (
    <div className="flex h-full items-center justify-center text-center">
      <div className="max-w-sm">
        <p className="text-foreground text-sm font-medium">{title}</p>
        <p className="text-muted-foreground mt-1 text-[13px]">{description}</p>
      </div>
    </div>
  );
}

function NamespaceSection({
  clusterId,
  clusterName,
  tenantId,
  projectId,
}: {
  clusterId: string;
  clusterName: string;
  tenantId: string | null;
  projectId: string | null;
}) {
  const { permissions } = useSessionContext();
  const [tokens, setTokens] = useState<string[]>([""]);
  const [pageIndex, setPageIndex] = useState(0);
  const [tokenCluster, setTokenCluster] = useState(clusterId);
  if (tokenCluster !== clusterId) {
    setTokenCluster(clusterId);
    setTokens([""]);
    setPageIndex(0);
  }

  const namespaces = useNamespaces(clusterId, {
    limit: NAMESPACE_PAGE_SIZE,
    ...(tokens[pageIndex] ? { continue: tokens[pageIndex] } : {}),
  });
  const [detailName, setDetailName] = useState<string | null>(null);
  const detail = useNamespace(clusterId, detailName);
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
            <div className="ml-auto flex items-center gap-1.5">
              <span className="zke-mono text-subtle-foreground mr-1 text-xs">
                第 {pageIndex + 1} 页
              </span>
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label="上一页"
                disabled={pageIndex === 0}
                onClick={() => setPageIndex((value) => Math.max(0, value - 1))}
              >
                <ChevronLeft />
              </Button>
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label="下一页"
                disabled={!nextToken}
                onClick={() => {
                  if (!nextToken) return;
                  setTokens((current) => [...current.slice(0, pageIndex + 1), nextToken]);
                  setPageIndex((value) => value + 1);
                }}
              >
                <ChevronRight />
              </Button>
            </div>
          }
        />
      </div>

      <Dialog open={Boolean(detailName)} onOpenChange={(open) => !open && setDetailName(null)}>
        <DialogContent aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>Namespace 详情</DialogTitle>
            <DialogDescription>
              读取自集群 {clusterName}，仅展示 Kubernetes 返回的当前状态。
            </DialogDescription>
          </DialogHeader>
          {detail.error ? (
            <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
          ) : detail.isLoading || !detail.data ? (
            <LoadingState />
          ) : (
            <NamespaceDetailView namespace={detail.data} />
          )}
        </DialogContent>
      </Dialog>

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
              setDeleteTarget(null);
            })
            .catch(() => undefined);
        }}
      />
    </>
  );
}

function NamespaceDetailView({ namespace }: { namespace: KubernetesNamespaceDetail }) {
  return (
    <dl className="grid gap-2 text-[13px]">
      <DetailLine label="名称" value={namespace.name} />
      <DetailLine label="UID" value={namespace.uid || "—"} mono />
      <DetailLine label="ResourceVersion" value={namespace.resource_version || "—"} mono />
      <DetailLine label="状态" value={namespace.phase || "Unknown"} />
      <DetailLine label="创建时间" value={formatAbsolute(namespace.creation_timestamp)} />
      <DetailLine
        label="标签"
        value={
          Object.entries(namespace.labels)
            .map(([key, value]) => `${key}=${value}`)
            .join("\n") || "—"
        }
        mono
      />
      <DetailLine label="Finalizers" value={namespace.finalizers.join("\n") || "—"} mono />
    </dl>
  );
}

function DetailLine({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="border-border grid grid-cols-[120px_1fr] gap-3 border-b py-2 last:border-0">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={mono ? "zke-mono break-all whitespace-pre-wrap" : "text-foreground"}>
        {value}
      </dd>
    </div>
  );
}
