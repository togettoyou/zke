import { useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Undo2 } from "lucide-react";
import { toast } from "sonner";

import { useRollbackWorkload, useWorkload, useWorkloadRevisions } from "@/api/queries/workloads";
import type { KubernetesWorkloadResource, KubernetesWorkloadRevision } from "@/api/types";
import { PageHeader } from "@/apps/AppShell";
import { DataTable } from "@/components/common/data-table";
import { RefreshAction } from "@/components/common/refresh-action";
import { SensitiveActionDialog, type ScopeLine } from "@/components/common/sensitive-action-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/misc";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { kindLabel } from "./workload-catalog";

/** The version of the workload a rollback was chosen against. */
type RollbackTarget = {
  revision: KubernetesWorkloadRevision;
  uid: string;
  resourceVersion: string;
};

/**
 * The Pod templates a Deployment, StatefulSet or DaemonSet has recorded, and the
 * rollback that restores one of them.
 *
 * The history is not part of the workload: it is the ReplicaSets or
 * ControllerRevisions the workload owns, which is why this is its own view and
 * its own read. A revision exists exactly because the Pod template changed, so
 * the rows show what a reader is comparing — the images — rather than a
 * summary of the whole object, which for these rows is identical.
 */
export function WorkloadRevisionsView({
  clusterId,
  clusterName,
  namespace,
  resource,
  name,
  canUpdate,
  onBack,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  resource: KubernetesWorkloadResource;
  name: string;
  canUpdate: boolean;
  onBack: () => void;
}) {
  const revisions = useWorkloadRevisions(clusterId, namespace, resource, name);
  // The preconditions a rollback is written with. They come from the workload
  // itself rather than from a revision, because a rollback is a write to the
  // workload and the revision objects are only read.
  const workload = useWorkload(clusterId, namespace, resource, name);
  const [target, setTarget] = useState<RollbackTarget | null>(null);

  const rollbackable = canUpdate && Boolean(workload.data?.uid);
  const columns: ColumnDef<KubernetesWorkloadRevision, unknown>[] = [
    {
      header: "修订",
      size: 190,
      cell: ({ row }) => (
        <div className="flex flex-col gap-0.5">
          <div className="flex items-baseline gap-1.5">
            <span className="zke-tnum text-foreground font-medium">#{row.original.revision}</span>
            {row.original.current ? <Badge tone="success">当前</Badge> : null}
          </div>
          {/* The object the revision was read from, so it can be looked at with
              kubectl without first working out which one it is. */}
          <span className="zke-mono text-subtle-foreground text-xs break-all">
            {row.original.name}
          </span>
        </div>
      ),
    },
    {
      header: "容器镜像",
      cell: ({ row }) => <RevisionImages revision={row.original} />,
    },
    {
      header: "变更说明",
      size: 200,
      cell: ({ row }) =>
        row.original.change_cause ? (
          <span className="text-muted-foreground text-xs break-all">
            {row.original.change_cause}
          </span>
        ) : (
          <span className="text-subtle-foreground text-xs">—</span>
        ),
    },
    {
      header: "记录时间",
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
      size: 96,
      cell: ({ row }) =>
        // The running template has nothing to restore, and the Server refuses a
        // rollback to it. A button that is always refused is not offered.
        rollbackable && !row.original.current ? (
          <div className="flex justify-end">
            <Button
              size="sm"
              variant="secondary"
              onClick={() =>
                setTarget({
                  revision: row.original,
                  uid: workload.data?.uid ?? "",
                  resourceVersion: workload.data?.resource_version ?? "",
                })
              }
            >
              <Undo2 />
              回滚
            </Button>
          </div>
        ) : null,
    },
  ];

  return (
    <div className="grid min-h-0 gap-3">
      <PageHeader
        title={`${name} · 历史版本`}
        onBack={onBack}
        actions={
          <RefreshAction
            isFetching={revisions.isFetching}
            onRefresh={() => void revisions.refetch()}
          />
        }
      />
      {revisions.data?.truncated ? (
        <Alert tone="warning">
          该工作负载的修订对象超过单页上限，下面不是全部修订。可按 Pod Selector
          在「资源对象浏览器」中查看完整列表。
        </Alert>
      ) : null}
      <DataTable
        columns={columns}
        data={revisions.data?.revisions}
        isLoading={revisions.isLoading}
        isFetching={revisions.isFetching}
        error={revisions.error}
        onRetry={() => void revisions.refetch()}
        rowKey={(revision) => revision.uid || String(revision.revision)}
        emptyTitle="没有历史版本"
        emptyDescription={`${kindLabel(resource)} ${name} 还没有可回滚的历史 Pod 模板。`}
      />
      {target ? (
        <RollbackDialog
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          resource={resource}
          name={name}
          target={target}
          onClose={() => setTarget(null)}
        />
      ) : null}
    </div>
  );
}

function RevisionImages({ revision }: { revision: KubernetesWorkloadRevision }) {
  const containers = [
    ...revision.init_containers.map((container) => ({ ...container, init: true })),
    ...revision.containers.map((container) => ({ ...container, init: false })),
  ];
  if (containers.length === 0) {
    return <span className="text-subtle-foreground text-xs">—</span>;
  }
  return (
    <div className="grid gap-0.5">
      {containers.map((container) => (
        <div
          key={`${container.init ? "init/" : ""}${container.name}`}
          className="flex items-baseline gap-1.5"
        >
          <span className="text-subtle-foreground shrink-0 text-xs">
            {container.init ? `Init · ${container.name}` : container.name}
          </span>
          <span className="zke-mono text-muted-foreground text-xs break-all">
            {container.image || "—"}
          </span>
        </div>
      ))}
    </div>
  );
}

/**
 * Two steps, as every other workload write in this section: a server-side
 * DryRun first, then the confirmed request.
 *
 * The UID and resourceVersion are frozen when the dialog opens, so a background
 * refetch cannot quietly move the write onto a version of the object the
 * operator never saw — a rollback submitted against a newer version is refused
 * with a conflict, which is the correct answer.
 */
function RollbackDialog({
  clusterId,
  clusterName,
  namespace,
  resource,
  name,
  target,
  onClose,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  resource: KubernetesWorkloadResource;
  name: string;
  target: RollbackTarget;
  onClose: () => void;
}) {
  const rollback = useRollbackWorkload();
  const [previewed, setPreviewed] = useState(false);
  const previewKey = useSubmissionKey(true);
  const applyKey = useSubmissionKey(true);
  const kind = kindLabel(resource);

  const scopeLines: ScopeLine[] = [
    { label: "集群", name: clusterName, id: clusterId },
    { label: "命名空间", name: namespace },
    { label: kind, name, id: target.uid },
    { label: "目标修订", name: `#${target.revision.revision}`, id: target.revision.name },
  ];

  return (
    <SensitiveActionDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={`回滚 ${kind} 到修订 #${target.revision.revision}`}
      description={
        previewed
          ? "DryRun 预检已通过。再次确认将提交实际回滚。"
          : "首次点击只执行服务端 DryRun 预检；通过后才会真正写入。"
      }
      scopeLines={scopeLines}
      impacts={[
        "只恢复该修订记录的 Pod 模板；副本数、更新策略以及对象自身的标签和注解不属于这次修订，保持不变。",
        "控制器会按该工作负载的更新策略滚动替换全部 Pod。",
        "请求携带打开本页时的 UID 与 resourceVersion，期间该对象被其他人改动则本次回滚被拒绝而不是覆盖。",
      ]}
      confirmLabel={previewed ? "确认回滚" : "执行 DryRun 预检"}
      destructive
      pending={rollback.isPending}
      error={rollback.error}
      onConfirm={() => {
        const dryRun = !previewed;
        void rollback
          .mutateAsync({
            clusterId,
            namespace,
            resource,
            name,
            revision: target.revision.revision,
            uid: target.uid,
            resourceVersion: target.resourceVersion,
            dryRun,
            idempotencyKey: dryRun ? previewKey : applyKey,
          })
          .then(() => {
            if (dryRun) {
              setPreviewed(true);
              toast.success("回滚 DryRun 预检已通过");
              return;
            }
            toast.success(`${name} 已提交回滚到修订 #${target.revision.revision}`);
            onClose();
          })
          .catch(() => undefined);
      }}
    />
  );
}
