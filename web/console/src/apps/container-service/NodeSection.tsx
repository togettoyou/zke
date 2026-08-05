import { useCallback, useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Ban, FileCode, PlayCircle } from "lucide-react";
import { toast } from "sonner";

import { useNode, useNodes, useSetNodeSchedulable } from "@/api/queries/nodes";
import type { KubernetesNodeDetail, KubernetesNodeSummary } from "@/api/types";
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
import { AddressValues, StatusBadge } from "@/components/common/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { YamlEditorView } from "./YamlEditorView";
import { useContinuePagination } from "./use-continue-pagination";
import type { ClusterSectionProps } from "./types";

const PAGE_SIZE = 50;

/** A Node the operator is about to stop or resume scheduling on. */
type SchedulingTarget = {
  name: string;
  unschedulable: boolean;
};

export function NodeSection({ clusterId, clusterName, tenantId, projectId }: ClusterSectionProps) {
  const { permissions } = useSessionContext();
  const pager = useContinuePagination(clusterId);
  const nodes = useNodes(clusterId, {
    limit: PAGE_SIZE,
    ...(pager.token ? { continue: pager.token } : {}),
  });
  // Drilling into a Node keeps the list's paging alive, so coming back lands on
  // the page the operator left rather than on page one.
  const [detailName, setDetailName] = useState<string | null>(null);
  // The YAML editor takes over the section: it is a document, not a field.
  const [yamlName, setYamlName] = useState<string | null>(null);
  const [schedulingTarget, setSchedulingTarget] = useState<SchedulingTarget | null>(null);
  const [schedulingPreviewed, setSchedulingPreviewed] = useState(false);
  const schedulingPreviewKey = useSubmissionKey(schedulingTarget !== null);
  const schedulingApplyKey = useSubmissionKey(schedulingTarget !== null);
  const setSchedulable = useSetNodeSchedulable();

  const projectScope = { type: "project" as const, tenantId, projectId };
  // Scheduling is a patch of the Node object, so it is an update rather than a
  // Node-specific permission.
  const canUpdate = permissions.can("cluster.resource.update", projectScope);

  // Both the row action and the detail view open the same confirmation, so it is
  // one callback rather than two copies of the reset sequence.
  const openScheduling = useCallback(
    (node: SchedulingTarget) => {
      setSchedulingTarget(node);
      setSchedulingPreviewed(false);
      setSchedulable.reset();
    },
    [setSchedulable],
  );

  const columns = useMemo<ColumnDef<KubernetesNodeSummary, unknown>[]>(
    () => [
      {
        header: "名称",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground font-medium">{row.original.name}</span>
            <span className="text-subtle-foreground text-xs">
              {row.original.roles.length === 0 ? "无角色标签" : row.original.roles.join(" · ")}
            </span>
          </div>
        ),
      },
      {
        header: "状态",
        size: 150,
        cell: ({ row }) => (
          <div className="flex flex-col items-start gap-0.5">
            <StatusBadge kind="node" value={row.original.status} />
            {row.original.unschedulable ? (
              <StatusBadge kind="scheduling" value="unschedulable" />
            ) : null}
          </div>
        ),
      },
      {
        header: "内网 IP",
        size: 140,
        cell: ({ row }) => (
          <AddressValues values={[row.original.internal_ip]} className="text-muted-foreground" />
        ),
      },
      {
        header: "版本",
        size: 130,
        cell: ({ row }) => (
          <span className="zke-mono text-muted-foreground text-xs">
            {row.original.kubernetes_version || "—"}
          </span>
        ),
      },
      {
        header: "可分配",
        size: 170,
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs">
            {row.original.cpu_allocatable || "—"} / {row.original.memory_allocatable || "—"}
          </span>
        ),
      },
      {
        id: "actions",
        header: "",
        size: 48,
        cell: ({ row }) =>
          canUpdate ? (
            <div onClick={(event) => event.stopPropagation()}>
              {/*
               * The two directions are one control in the same cell, so they are
               * told apart by colour as well as by icon — and by the colour the
               * section already uses for the state each one produces: the
               * scheduling badge is warning while a Node is stopped and success
               * while it is schedulable.
               */}
              <Button
                size="icon-sm"
                variant="ghost"
                className={
                  row.original.unschedulable
                    ? "text-success hover:text-success"
                    : "text-warning hover:text-warning"
                }
                aria-label={`${row.original.unschedulable ? "恢复调度" : "停止调度"} ${row.original.name}`}
                onClick={() =>
                  openScheduling({
                    name: row.original.name,
                    unschedulable: row.original.unschedulable,
                  })
                }
              >
                {row.original.unschedulable ? <PlayCircle /> : <Ban />}
              </Button>
            </div>
          ) : null,
      },
    ],
    [canUpdate, openScheduling],
  );

  const nextToken = nodes.data?.continue_token ?? "";
  // Stopping scheduling is the direction that changes where new Pods land, so it
  // is the one confirmed as destructive.
  const stopping = schedulingTarget !== null && !schedulingTarget.unschedulable;

  return (
    <>
      {yamlName ? (
        <YamlEditorView
          identity={{ clusterId, version: "v1", resource: "nodes", name: yamlName }}
          clusterName={clusterName}
          kindLabel="Node"
          canUpdate={canUpdate}
          onBack={() => setYamlName(null)}
        />
      ) : detailName ? (
        <NodeDetailView
          clusterId={clusterId}
          name={detailName}
          canUpdate={canUpdate}
          onBack={() => setDetailName(null)}
          onToggleScheduling={openScheduling}
          onOpenYaml={() => setYamlName(detailName)}
        />
      ) : (
        // No heading over the list: the navigation rail already says 节点 and the
        // toolbar already names the target Cluster, so a title repeating both
        // only costs the table a row of height.
        <div className="flex h-full min-h-0 flex-col">
          <SectionToolbarActions>
            <RefreshAction isFetching={nodes.isFetching} onRefresh={() => void nodes.refetch()} />
          </SectionToolbarActions>
          <DataTable
            columns={columns}
            data={nodes.data?.nodes}
            isLoading={nodes.isLoading}
            isFetching={nodes.isFetching}
            error={nodes.error}
            onRetry={() => void nodes.refetch()}
            onRowClick={(node) => setDetailName(node.name)}
            rowKey={(node) => node.uid || node.name}
            emptyTitle="该集群没有节点"
            emptyDescription="当前筛选范围内没有可见的 Node。"
            continuePagination={{
              pageIndex: pager.pageIndex,
              nextToken,
              onPrevious: pager.goPrevious,
              onNext: pager.goNext,
            }}
          />
        </div>
      )}

      <SensitiveActionDialog
        open={schedulingTarget !== null}
        onOpenChange={(open) => !open && setSchedulingTarget(null)}
        title={stopping ? "停止调度到该节点" : "恢复调度到该节点"}
        description={
          schedulingPreviewed
            ? "DryRun 已通过。再次确认将提交实际变更。"
            : "首次点击只执行服务端 DryRun；预检通过后才会实际修改节点。"
        }
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "节点", name: schedulingTarget?.name ?? "" },
        ]}
        impacts={
          stopping
            ? [
                "该节点将被标记为不可调度，新的 Pod 不会再被调度到它上面。",
                "已经运行在该节点上的 Pod 不受影响，也不会被驱逐——驱逐（drain）尚未支持。",
              ]
            : ["该节点将恢复为可调度，调度器可以再次把新的 Pod 放到它上面。"]
        }
        confirmLabel={schedulingPreviewed ? "确认执行" : "执行 DryRun 预检"}
        destructive={stopping}
        pending={setSchedulable.isPending}
        error={setSchedulable.error}
        onConfirm={() => {
          if (!schedulingTarget) return;
          const dryRun = !schedulingPreviewed;
          const unschedulable = !schedulingTarget.unschedulable;
          void setSchedulable
            .mutateAsync({
              clusterId,
              name: schedulingTarget.name,
              unschedulable,
              dryRun,
              idempotencyKey: dryRun ? schedulingPreviewKey : schedulingApplyKey,
            })
            .then(() => {
              if (dryRun) {
                setSchedulingPreviewed(true);
                toast.success("节点调度变更 DryRun 已通过");
                return;
              }
              toast.success(
                unschedulable
                  ? `节点 ${schedulingTarget.name} 已停止调度`
                  : `节点 ${schedulingTarget.name} 已恢复调度`,
              );
              setSchedulingTarget(null);
            })
            .catch(() => undefined);
        }}
      />
    </>
  );
}

function NodeDetailView({
  clusterId,
  name,
  canUpdate,
  onBack,
  onToggleScheduling,
  onOpenYaml,
}: {
  clusterId: string;
  name: string;
  canUpdate: boolean;
  onBack: () => void;
  onToggleScheduling: (node: SchedulingTarget) => void;
  onOpenYaml: () => void;
}) {
  const detail = useNode(clusterId, name);

  return (
    <div className="grid gap-3">
      <PageHeader
        title={name}
        onBack={onBack}
        actions={
          <>
            {canUpdate && detail.data ? (
              // Same colour rule as the list action: warning for the direction
              // that stops scheduling, success for the one that restores it.
              <Button
                size="sm"
                variant="secondary"
                className={detail.data.unschedulable ? "text-success" : "text-warning"}
                onClick={() =>
                  onToggleScheduling({
                    name: detail.data.name,
                    unschedulable: detail.data.unschedulable,
                  })
                }
              >
                {detail.data.unschedulable ? <PlayCircle /> : <Ban />}
                {detail.data.unschedulable ? "恢复调度" : "停止调度"}
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
      ) : detail.isLoading || !detail.data ? (
        <LoadingState />
      ) : (
        <NodeDetailCards node={detail.data} />
      )}
    </div>
  );
}

function NodeDetailCards({ node }: { node: KubernetesNodeDetail }) {
  return (
    <div className="grid gap-3 md:grid-cols-2">
      <DetailCard title="概览">
        <DetailRow label="名称" value={node.name} />
        <DetailRow
          label="UID"
          value={<span className="zke-mono text-xs break-all">{node.uid || "—"}</span>}
        />
        <DetailRow label="状态" value={<StatusBadge kind="node" value={node.status} />} />
        <DetailRow
          label="调度"
          value={
            <StatusBadge
              kind="scheduling"
              value={node.unschedulable ? "unschedulable" : "schedulable"}
            />
          }
        />
        <DetailRow
          label="角色"
          value={
            node.roles.length === 0 ? (
              "—"
            ) : (
              <span className="flex flex-wrap gap-1">
                {node.roles.map((role) => (
                  <Badge key={role} tone="info">
                    {role}
                  </Badge>
                ))}
              </span>
            )
          }
        />
        <DetailRow label="创建时间" value={formatAbsolute(node.creation_timestamp)} />
        <DetailRow
          label="Provider"
          value={<span className="zke-mono text-xs break-all">{node.provider_id || "—"}</span>}
        />
      </DetailCard>

      <DetailCard title="运行环境">
        <DetailRow
          label="Kubernetes"
          value={<span className="zke-mono text-xs">{node.kubernetes_version || "—"}</span>}
        />
        <DetailRow
          label="容器运行时"
          value={
            <span className="zke-mono text-xs break-all">{node.container_runtime || "—"}</span>
          }
        />
        <DetailRow label="操作系统" value={node.operating_system || "—"} />
        <DetailRow label="系统镜像" value={node.os_image || "—"} />
        <DetailRow
          label="内核"
          value={<span className="zke-mono text-xs break-all">{node.kernel_version || "—"}</span>}
        />
        <DetailRow label="架构" value={node.architecture || "—"} />
      </DetailCard>

      <DetailCard title="容量与可分配">
        <DetailRow
          label="CPU"
          value={`${node.cpu_allocatable || "—"} / ${node.cpu_capacity || "—"}`}
        />
        <DetailRow
          label="内存"
          value={`${node.memory_allocatable || "—"} / ${node.memory_capacity || "—"}`}
        />
        <DetailRow
          label="Pod"
          value={`${node.pods_allocatable || "—"} / ${node.pods_capacity || "—"}`}
        />
      </DetailCard>

      <DetailCard title="网络">
        <DetailRow label="内网 IP" value={<AddressValues values={[node.internal_ip]} />} />
        {/* Hostnames are copied for the same reasons addresses are: they are
            what the next command is pointed at. */}
        {node.addresses.map((address) => (
          <DetailRow
            key={`${address.type}/${address.address}`}
            label={address.type}
            value={<AddressValues values={[address.address]} />}
          />
        ))}
        <DetailRow
          label="Pod CIDR"
          value={
            <span className="zke-mono text-xs break-all">
              {node.pod_cidrs.length > 0 ? node.pod_cidrs.join(", ") : node.pod_cidr || "—"}
            </span>
          }
        />
      </DetailCard>

      <DetailCard title="条件">
        <DetailConditions conditions={node.conditions} />
      </DetailCard>

      <DetailCard title="污点">
        {node.taints.length === 0 ? (
          <DetailRow label="污点" value="—" />
        ) : (
          node.taints.map((taint) => (
            <DetailRow
              key={`${taint.key}/${taint.effect}`}
              label={taint.effect}
              value={
                <span className="zke-mono text-xs break-all">
                  {taint.key}
                  {taint.value ? `=${taint.value}` : ""}
                </span>
              }
            />
          ))
        )}
      </DetailCard>

      <DetailCard title="标签">
        <DetailKeyValues entries={node.labels} />
      </DetailCard>

      <DetailCard title="注解">
        <DetailKeyValues entries={node.annotations} />
      </DetailCard>
    </div>
  );
}
