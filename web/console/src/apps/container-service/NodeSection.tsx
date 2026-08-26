import { useCallback, useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Ban, FileCode, PlayCircle, ServerOff, Stethoscope, Tags } from "lucide-react";
import { toast } from "sonner";

import { useDrainNode, useNode, useNodes, useSetNodeSchedulable } from "@/api/queries/nodes";
import { useNodeDescribe } from "@/api/queries/describe";
import type {
  KubernetesNodeDetail,
  KubernetesNodeDrainResult,
  KubernetesNodeSummary,
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
import { AddressValues, StatusBadge } from "@/components/common/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Alert, Checkbox } from "@/components/ui/misc";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { NODE_MUTATION_PERMISSION } from "./resource-permissions";
import { YamlEditorView } from "./YamlEditorView";
import { DescribeView } from "./DescribeView";
import { NodeLabelsView } from "./NodeLabelsView";
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
  const [describeName, setDescribeName] = useState<string | null>(null);
  // The YAML editor takes over the section: it is a document, not a field.
  const [yamlName, setYamlName] = useState<string | null>(null);
  // Labels are a list of their own length, so they get a page as well.
  const [labelsName, setLabelsName] = useState<string | null>(null);
  const [schedulingTarget, setSchedulingTarget] = useState<SchedulingTarget | null>(null);
  const [schedulingPreviewed, setSchedulingPreviewed] = useState(false);
  const schedulingPreviewKey = useSubmissionKey(schedulingTarget !== null);
  const schedulingApplyKey = useSubmissionKey(schedulingTarget !== null);
  const setSchedulable = useSetNodeSchedulable();
  const drain = useDrainNode();
  const [drainTarget, setDrainTarget] = useState<Pick<
    KubernetesNodeSummary,
    "name" | "uid"
  > | null>(null);
  const [drainPreview, setDrainPreview] = useState<KubernetesNodeDrainResult | null>(null);
  const [forceUnmanaged, setForceUnmanaged] = useState(false);
  const [deleteEmptyDirData, setDeleteEmptyDirData] = useState(false);
  const drainPreviewKey = useSubmissionKey(drainTarget !== null);
  const drainApplyKey = useSubmissionKey(drainTarget !== null);

  const projectScope = { type: "project" as const, tenantId, projectId };
  // Labels, the YAML and the scheduling switch are all writes to the Node object
  // itself, so all three answer to the Node permission rather than to the
  // ordinary resource one. Draining keeps its own permission below.
  const canUpdate = permissions.can(NODE_MUTATION_PERMISSION, projectScope);
  const canDescribe = permissions.can("cluster.event.read", projectScope);
  const canDrain = permissions.can("cluster.node.drain", projectScope);

  const openDrain = useCallback(
    (node: Pick<KubernetesNodeSummary, "name" | "uid">) => {
      setDrainTarget(node);
      setDrainPreview(null);
      setForceUnmanaged(false);
      setDeleteEmptyDirData(false);
      drain.reset();
    },
    [drain],
  );

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
        size: 88,
        cell: ({ row }) =>
          canDescribe || canUpdate || canDrain ? (
            <div
              className="flex items-center justify-end"
              onClick={(event) => event.stopPropagation()}
            >
              {canDescribe ? (
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label={`诊断 ${row.original.name}`}
                  onClick={() => setDescribeName(row.original.name)}
                >
                  <Stethoscope />
                </Button>
              ) : null}
              {canUpdate ? (
                <>
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
                </>
              ) : null}
              {canDrain && row.original.uid ? (
                <Button
                  size="icon-sm"
                  variant="ghost"
                  className="text-danger hover:text-danger"
                  aria-label={`排空节点 ${row.original.name}`}
                  onClick={() => openDrain(row.original)}
                >
                  <ServerOff />
                </Button>
              ) : null}
            </div>
          ) : null,
      },
    ],
    [canDescribe, canUpdate, canDrain, openDrain, openScheduling],
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
          writePermission={NODE_MUTATION_PERMISSION}
          onBack={() => setYamlName(null)}
        />
      ) : describeName ? (
        <NodeDescribeView
          clusterId={clusterId}
          name={describeName}
          onBack={() => setDescribeName(null)}
        />
      ) : labelsName ? (
        <NodeLabelsView
          clusterId={clusterId}
          clusterName={clusterName}
          name={labelsName}
          onBack={() => setLabelsName(null)}
        />
      ) : detailName ? (
        <NodeDetailView
          clusterId={clusterId}
          name={detailName}
          canUpdate={canUpdate}
          canDescribe={canDescribe}
          canDrain={canDrain}
          onBack={() => setDetailName(null)}
          onToggleScheduling={openScheduling}
          onOpenYaml={() => setYamlName(detailName)}
          onOpenLabels={() => setLabelsName(detailName)}
          onOpenDescribe={() => setDescribeName(detailName)}
          onDrain={openDrain}
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
            ? "DryRun 预检已通过。再次确认将提交实际变更。"
            : "首次点击只执行服务端 DryRun 预检；通过后才会实际修改节点。"
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
                toast.success("节点调度变更 DryRun 预检已通过");
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

      <SensitiveActionDialog
        open={drainTarget !== null}
        onOpenChange={(open) => !open && setDrainTarget(null)}
        title="排空节点"
        description={
          drainPreview
            ? drainPreviewReady(drainPreview)
              ? "DryRun 预检已通过。输入节点名称后可提交实际 Drain。"
              : "DryRun 预检发现阻断项或当前 PDB 不允许驱逐。调整选项或等待副本恢复后重新预检。"
            : "先执行完整清单与 Kubernetes DryRun 预检，不会在预检阶段修改节点或 Pod。"
        }
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "节点", name: drainTarget?.name ?? "", id: drainTarget?.uid },
        ]}
        impacts={[
          "节点会先停止调度；受控制器管理的普通 Pod 随后通过 Eviction API 驱逐并由控制器重建。",
          "PodDisruptionBudget 会被遵守；预算不足时对应 Pod 保留在节点上，操作明确报告为未完成。",
          "Mirror Pod、DaemonSet Pod 和已终止中的 Pod 不会被驱逐。",
        ]}
        confirmationText={drainPreviewReady(drainPreview) ? drainTarget?.name : undefined}
        confirmLabel={drainConfirmLabel(drainPreview)}
        confirmDisabled={drainPreviewHasStaticBlockers(drainPreview)}
        destructive
        pending={drain.isPending}
        error={drain.error}
        onConfirm={() => {
          if (!drainTarget) return;
          const dryRun = !drainPreviewReady(drainPreview);
          void drain
            .mutateAsync({
              clusterId,
              name: drainTarget.name,
              idempotencyKey: dryRun ? drainPreviewKey : drainApplyKey,
              request: {
                uid: drainTarget.uid,
                dry_run: dryRun,
                confirm: !dryRun,
                force_unmanaged: forceUnmanaged,
                delete_empty_dir_data: deleteEmptyDirData,
              },
            })
            .then((result) => {
              if (dryRun) {
                setDrainPreview(result);
                toast.success(
                  drainPreviewReady(result)
                    ? "节点排空 DryRun 预检已通过"
                    : "节点排空 DryRun 预检已完成，存在阻断项",
                );
                return;
              }
              if (drainPreviewReady(result)) {
                const accepted = result.pods.filter((pod) => pod.result === "evicted").length;
                toast.success(`节点已停止调度，已提交 ${accepted} 个 Pod 驱逐请求`);
                setDrainTarget(null);
              } else {
                const remaining = result.pods.filter(
                  (pod) => pod.result === "pdb_blocked" || pod.result === "failed",
                ).length;
                toast.warning(
                  `节点已停止调度，仍有 ${remaining} 个 Pod 未完成驱逐，请重新打开后重试`,
                );
                setDrainTarget(null);
              }
            })
            .catch(() => undefined);
        }}
      >
        <div className="grid gap-3">
          <label className="flex items-start gap-2 text-[13px]">
            <Checkbox
              checked={forceUnmanaged}
              onCheckedChange={(checked) => {
                setForceUnmanaged(checked === true);
                setDrainPreview(null);
              }}
            />
            <span>
              <span className="text-foreground block font-medium">驱逐无控制器 Pod</span>
              <span className="text-muted-foreground">这类 Pod 不会被控制器自动重建。</span>
            </span>
          </label>
          <label className="flex items-start gap-2 text-[13px]">
            <Checkbox
              checked={deleteEmptyDirData}
              onCheckedChange={(checked) => {
                setDeleteEmptyDirData(checked === true);
                setDrainPreview(null);
              }}
            />
            <span>
              <span className="text-foreground block font-medium">接受 emptyDir 数据丢失</span>
              <span className="text-muted-foreground">Pod 重建后 emptyDir 中的数据无法恢复。</span>
            </span>
          </label>
          {drainPreview ? <DrainPreview result={drainPreview} /> : null}
        </div>
      </SensitiveActionDialog>
    </>
  );
}

function NodeDetailView({
  clusterId,
  name,
  canUpdate,
  canDescribe,
  canDrain,
  onBack,
  onToggleScheduling,
  onOpenYaml,
  onOpenLabels,
  onOpenDescribe,
  onDrain,
}: {
  clusterId: string;
  name: string;
  canUpdate: boolean;
  canDescribe: boolean;
  canDrain: boolean;
  onBack: () => void;
  onToggleScheduling: (node: SchedulingTarget) => void;
  onOpenYaml: () => void;
  onOpenLabels: () => void;
  onOpenDescribe: () => void;
  onDrain: (node: Pick<KubernetesNodeSummary, "name" | "uid">) => void;
}) {
  const detail = useNode(clusterId, name);

  return (
    <div className="grid gap-3">
      <PageHeader
        title={name}
        onBack={onBack}
        // Every detail header in this app reads the same way: YAML first, then
        // the actions that change the object, with the one that cannot be taken
        // back at the far end. A Node has no deletion here, so the scheduling
        // switch — the action with a blast radius — takes that place.
        actions={
          <>
            {canDescribe ? (
              <Button size="sm" variant="secondary" onClick={onOpenDescribe}>
                <Stethoscope />
                诊断
              </Button>
            ) : null}
            <Button size="sm" variant="secondary" onClick={onOpenYaml}>
              <FileCode />
              YAML
            </Button>
            {canUpdate ? (
              <Button size="sm" variant="secondary" onClick={onOpenLabels}>
                <Tags />
                标签
              </Button>
            ) : null}
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
            {canDrain && detail.data?.uid ? (
              <Button
                size="sm"
                variant="danger"
                onClick={() => onDrain({ name: detail.data.name, uid: detail.data.uid })}
              >
                <ServerOff />
                排空节点
              </Button>
            ) : null}
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

function drainPreviewReady(result: KubernetesNodeDrainResult | null): boolean {
  return Boolean(
    result &&
    !result.blocked &&
    result.pods.every(
      (pod) => pod.result !== "failed" && pod.result !== "pdb_blocked" && pod.result !== "blocked",
    ),
  );
}

function drainPreviewHasStaticBlockers(result: KubernetesNodeDrainResult | null): boolean {
  return Boolean(result?.blocked);
}

function drainConfirmLabel(result: KubernetesNodeDrainResult | null): string {
  if (drainPreviewReady(result)) {
    return "确认排空";
  }
  if (drainPreviewHasStaticBlockers(result)) {
    return "请先处理阻断项";
  }
  return result ? "重新执行 DryRun 预检" : "执行 DryRun 预检";
}

function DrainPreview({ result }: { result: KubernetesNodeDrainResult }) {
  const evict = result.pods.filter((pod) => pod.decision === "evict");
  const skipped = result.pods.filter((pod) => pod.decision === "skip");
  const blocked = result.pods.filter(
    (pod) => pod.decision === "block" || pod.result === "pdb_blocked" || pod.result === "failed",
  );
  return (
    <div className="grid gap-2">
      <div className="flex flex-wrap gap-1.5">
        <Badge tone={blocked.length > 0 ? "warning" : "success"}>计划驱逐 {evict.length}</Badge>
        <Badge tone="neutral">跳过 {skipped.length}</Badge>
        <Badge tone={blocked.length > 0 ? "danger" : "neutral"}>阻断 {blocked.length}</Badge>
      </div>
      {blocked.length > 0 ? (
        <Alert tone="warning">
          {result.blocked ? (
            <p className="mb-2 text-xs">
              清单存在静态阻断，本次 DryRun 预检已停止；节点未停止调度，也没有向任何 Pod
              发送驱逐请求。
            </p>
          ) : null}
          <div className="grid max-h-36 gap-1 overflow-y-auto">
            {blocked.map((pod) => (
              <div key={pod.uid} className="text-xs break-words">
                <span className="zke-mono text-foreground">
                  {pod.namespace}/{pod.name}
                </span>{" "}
                · {drainReasonLabel(pod.reason)}
                {drainPodResolution(pod.reason, pod.message)}
              </div>
            ))}
          </div>
        </Alert>
      ) : (
        <Alert tone="success">
          DryRun 预检未发现静态阻断项；实际执行时 PDB 与 Pod 状态仍会再次校验。
        </Alert>
      )}
    </div>
  );
}

function drainPodResolution(reason: string, message: string): string {
  const resolutions: Record<string, string> = {
    UnmanagedPod: "：勾选“驱逐无控制器 Pod”后重新预检",
    EmptyDirData: "：勾选“接受 emptyDir 数据丢失”后重新预检",
    TooManyRequests: "：等待 PodDisruptionBudget 预算恢复后重新预检",
  };
  return resolutions[reason] ?? (message ? `：${message}` : "");
}

function drainReasonLabel(reason: string): string {
  const labels: Record<string, string> = {
    UnmanagedPod: "无控制器 Pod",
    EmptyDirData: "包含 emptyDir 数据",
    DaemonSetPod: "DaemonSet Pod",
    MirrorPod: "Mirror Pod",
    Terminating: "正在终止",
    TooManyRequests: "PodDisruptionBudget 暂不允许驱逐",
  };
  return labels[reason] ?? (reason || "未知原因");
}

function NodeDescribeView({
  clusterId,
  name,
  onBack,
}: {
  clusterId: string;
  name: string;
  onBack: () => void;
}) {
  const describe = useNodeDescribe(clusterId, name);
  return (
    <DescribeView
      name={name}
      kindLabel="Node"
      data={describe.data}
      isLoading={describe.isLoading}
      isFetching={describe.isFetching}
      error={describe.error}
      onRetry={() => void describe.refetch()}
      onBack={onBack}
    />
  );
}

function NodeDetailCards({ node }: { node: KubernetesNodeDetail }) {
  return (
    <div className="grid gap-3 @md:grid-cols-2">
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
