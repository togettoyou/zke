import { lazy, Suspense, useCallback, useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { FileCode, ScrollText, SquareTerminal } from "lucide-react";
import { toast } from "sonner";

import { useDeletePod, usePod, usePods } from "@/api/queries/pods";
import type {
  KubernetesPodContainer,
  KubernetesPodDetail,
  KubernetesPodOwnerReference,
  KubernetesPodSummary,
} from "@/api/types";
import { PageHeader, SectionToolbarActions } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
import { DetailDeleteAction, RowDeleteAction } from "@/components/common/delete-action";
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
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { PodLogsView } from "./PodLogsView";
import { YamlEditorView } from "./YamlEditorView";
import { useContinuePagination } from "./use-continue-pagination";
import type { ClusterSectionProps } from "./types";

/**
 * The terminal is loaded on demand.
 *
 * It carries a full terminal emulator — around 300 kB — for a capability only
 * admins hold and few sessions use. Bundling it into the application would make
 * every operator who never opens a shell pay for it.
 */
const PodTerminalView = lazy(async () => ({
  default: (await import("./PodTerminalView")).PodTerminalView,
}));

const PAGE_SIZE = 50;

type PodSectionProps = ClusterSectionProps & {
  /** The Namespace every query and deletion in this section is scoped to. */
  namespace: string;
};

type PodLogTarget = Pick<KubernetesPodSummary, "name" | "uid">;

/**
 * Pods of one Namespace of one Cluster.
 *
 * Reading, reading logs and deleting is the whole of it. Logs travel over their
 * own protocol with their own permission rather than through the Resource
 * stream, which rejects subresources outright; exec and eviction have no such
 * path yet, so this section offers no control that would need one.
 */
export function PodSection({
  clusterId,
  clusterName,
  namespace,
  tenantId,
  projectId,
}: PodSectionProps) {
  const { permissions } = useSessionContext();
  const pager = useContinuePagination(`${clusterId}/${namespace}`);
  const pods = usePods(clusterId, namespace, {
    limit: PAGE_SIZE,
    ...(pager.token ? { continue: pager.token } : {}),
  });
  const [detailName, setDetailName] = useState<string | null>(null);
  // The log reader takes over the whole section: a log is read, not glanced at.
  const [logsTarget, setLogsTarget] = useState<PodLogTarget | null>(null);
  // The YAML editor takes over the section the same way, and for the same
  // reason: it is a document, not a field.
  const [yamlName, setYamlName] = useState<string | null>(null);
  // So does the terminal, which additionally must not be a dialog the operator
  // can dismiss by accident while a shell is attached.
  const [terminalTarget, setTerminalTarget] = useState<PodLogTarget | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<KubernetesPodSummary | null>(null);
  const [deletePreviewed, setDeletePreviewed] = useState(false);
  const deletePreviewKey = useSubmissionKey(deleteTarget !== null);
  const deleteApplyKey = useSubmissionKey(deleteTarget !== null);
  const remove = useDeletePod();

  const projectScope = { type: "project" as const, tenantId, projectId };
  const canDelete = permissions.can("cluster.resource.delete", projectScope);
  // Reading logs is its own permission rather than part of reading the Cluster:
  // log bodies carry whatever the application decided to print.
  const canReadLogs = permissions.can("cluster.pod.logs.read", projectScope);
  const canUpdate = permissions.can("cluster.resource.update", projectScope);
  // Opening a shell is its own permission, granted to admin only by default.
  const canExec = permissions.can("cluster.pod.exec", projectScope);

  const onOpenLogs = useCallback(
    (pod: PodLogTarget) => setLogsTarget({ name: pod.name, uid: pod.uid }),
    [],
  );

  const onOpenTerminal = useCallback(
    (pod: PodLogTarget) => setTerminalTarget({ name: pod.name, uid: pod.uid }),
    [],
  );

  // Both the row action and the detail view open the same confirmation, so it is
  // one callback rather than two copies of the reset sequence.
  const openDelete = useCallback(
    (pod: KubernetesPodSummary) => {
      setDeleteTarget(pod);
      setDeletePreviewed(false);
      remove.reset();
    },
    [remove],
  );

  const columns = useMemo<ColumnDef<KubernetesPodSummary, unknown>[]>(
    () => [
      {
        header: "名称",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground font-medium">{row.original.name}</span>
            <span className="text-subtle-foreground text-xs">
              {row.original.controller
                ? `${row.original.controller.kind}/${row.original.controller.name}`
                : "无控制器"}
            </span>
          </div>
        ),
      },
      {
        header: "状态",
        size: 150,
        cell: ({ row }) => <PodStatusCell pod={row.original} />,
      },
      {
        header: "节点",
        size: 150,
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs break-all">
            {row.original.node_name || "尚未调度"}
          </span>
        ),
      },
      {
        header: "Pod IP",
        size: 130,
        cell: ({ row }) => (
          <AddressValues values={[row.original.pod_ip]} className="text-muted-foreground" />
        ),
      },
      {
        header: "重启",
        size: 70,
        cell: ({ row }) => <span className="zke-tnum">{row.original.restart_count}</span>,
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
        size: 120,
        cell: ({ row }) => (
          <div className="flex justify-end gap-0.5" onClick={(event) => event.stopPropagation()}>
            {/* Both actions are pinned to the Pod's UID, so a row that somehow
                arrived without one gets neither. */}
            {canReadLogs && row.original.uid ? (
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={`查看 ${row.original.name} 的日志`}
                onClick={() => onOpenLogs(row.original)}
              >
                <ScrollText />
              </Button>
            ) : null}
            {canExec && row.original.uid ? (
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={`打开 ${row.original.name} 的终端`}
                onClick={() => onOpenTerminal(row.original)}
              >
                <SquareTerminal />
              </Button>
            ) : null}
            {canDelete && row.original.uid ? (
              <RowDeleteAction name={row.original.name} onDelete={() => openDelete(row.original)} />
            ) : null}
          </div>
        ),
      },
    ],
    [canDelete, canReadLogs, canExec, onOpenLogs, onOpenTerminal, openDelete],
  );

  const nextToken = pods.data?.continue_token ?? "";

  return (
    <>
      {terminalTarget ? (
        <Suspense fallback={<LoadingState />}>
          <PodTerminalView
            clusterId={clusterId}
            clusterName={clusterName}
            namespace={namespace}
            podName={terminalTarget.name}
            podUid={terminalTarget.uid}
            onBack={() => setTerminalTarget(null)}
          />
        </Suspense>
      ) : yamlName ? (
        <YamlEditorView
          identity={{ clusterId, version: "v1", resource: "pods", namespace, name: yamlName }}
          clusterName={clusterName}
          kindLabel="Pod"
          canUpdate={canUpdate}
          onBack={() => setYamlName(null)}
        />
      ) : logsTarget ? (
        <PodLogsView
          clusterId={clusterId}
          namespace={namespace}
          podName={logsTarget.name}
          podUid={logsTarget.uid}
          onBack={() => setLogsTarget(null)}
        />
      ) : detailName ? (
        <PodDetailView
          clusterId={clusterId}
          namespace={namespace}
          name={detailName}
          canDelete={canDelete}
          canReadLogs={canReadLogs}
          canExec={canExec}
          onDelete={openDelete}
          onOpenLogs={onOpenLogs}
          onOpenTerminal={onOpenTerminal}
          onOpenYaml={() => setYamlName(detailName)}
          onBack={() => setDetailName(null)}
        />
      ) : (
        <div className="flex h-full min-h-0 flex-col">
          <SectionToolbarActions>
            <RefreshAction isFetching={pods.isFetching} onRefresh={() => void pods.refetch()} />
          </SectionToolbarActions>
          <DataTable
            columns={columns}
            data={pods.data?.pods}
            isLoading={pods.isLoading}
            isFetching={pods.isFetching}
            error={pods.error}
            onRetry={() => void pods.refetch()}
            onRowClick={(pod) => setDetailName(pod.name)}
            rowKey={(pod) => pod.uid || pod.name}
            emptyTitle="该命名空间没有 Pod"
            emptyDescription={`${namespace} 中没有可见的 Pod。`}
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
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title="删除 Pod"
        description={
          deletePreviewed
            ? "DryRun 已通过。再次确认将提交实际删除。"
            : "首次点击只执行服务端 DryRun；预检通过后才能实际删除。"
        }
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          { label: "Pod", name: deleteTarget?.name ?? "", id: deleteTarget?.uid },
        ]}
        impacts={deleteImpacts(deleteTarget)}
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
              dryRun,
              idempotencyKey: dryRun ? deletePreviewKey : deleteApplyKey,
            })
            .then(() => {
              if (dryRun) {
                setDeletePreviewed(true);
                toast.success("Pod 删除 DryRun 已通过");
                return;
              }
              toast.success(`Pod ${deleteTarget.name} 已提交删除`);
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

function deleteImpacts(pod: KubernetesPodSummary | null): string[] {
  const impacts = [
    "Pod 将被终止，其中所有容器都会停止运行。",
    "这是删除而不是驱逐：不会执行 PodDisruptionBudget 语义。",
    "请求携带该 Pod 当前的 UID 前置条件，避免误删同名重建的对象。",
  ];
  if (pod?.controller) {
    // The single most surprising outcome here: deleting a controller-managed Pod
    // usually accomplishes nothing except a restart.
    impacts.splice(
      1,
      0,
      `该 Pod 由 ${pod.controller.kind}/${pod.controller.name} 管理，删除后控制器通常会重新创建一个。`,
    );
  }
  return impacts;
}

/** Phase, plus the two things phase alone does not say: readiness and deletion. */
function PodStatusCell({ pod }: { pod: KubernetesPodSummary }) {
  return (
    <div className="flex flex-col items-start gap-0.5">
      <StatusBadge kind="pod" value={pod.phase || "Unknown"} />
      {pod.deletion_timestamp ? (
        <Badge tone="warning">
          <StatusDot tone="warning" />
          删除中
        </Badge>
      ) : pod.phase === "Running" && !pod.ready ? (
        <Badge tone="warning">
          <StatusDot tone="warning" />
          未就绪
        </Badge>
      ) : null}
      {pod.reason ? (
        <span className="text-subtle-foreground text-xs break-words">{pod.reason}</span>
      ) : null}
    </div>
  );
}

function PodDetailView({
  clusterId,
  namespace,
  name,
  canDelete,
  canReadLogs,
  canExec,
  onDelete,
  onOpenLogs,
  onOpenTerminal,
  onOpenYaml,
  onBack,
}: {
  clusterId: string;
  namespace: string;
  name: string;
  canDelete: boolean;
  canReadLogs: boolean;
  canExec: boolean;
  onDelete: (pod: KubernetesPodSummary) => void;
  onOpenLogs: (pod: PodLogTarget) => void;
  onOpenTerminal: (pod: PodLogTarget) => void;
  onOpenYaml: () => void;
  onBack: () => void;
}) {
  const detail = usePod(clusterId, namespace, name);
  // Deletion is pinned to a UID, so it cannot be offered before the object that
  // carries one has loaded.
  const pod = detail.data;

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
            {canReadLogs && pod?.uid ? (
              <Button size="sm" variant="secondary" onClick={() => onOpenLogs(pod)}>
                <ScrollText />
                日志
              </Button>
            ) : null}
            {canExec && pod?.uid ? (
              <Button size="sm" variant="secondary" onClick={() => onOpenTerminal(pod)}>
                <SquareTerminal />
                终端
              </Button>
            ) : null}
            {canDelete && pod?.uid ? (
              <DetailDeleteAction name={name} onDelete={() => onDelete(pod)} />
            ) : null}
          </>
        }
      />
      {detail.error ? (
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      ) : detail.isLoading || !pod ? (
        <LoadingState />
      ) : (
        <PodDetailCards pod={pod} />
      )}
    </div>
  );
}

function PodDetailCards({ pod }: { pod: KubernetesPodDetail }) {
  return (
    <div className="grid gap-3 md:grid-cols-2">
      <DetailCard title="概览">
        <DetailRow label="名称" value={pod.name} />
        <DetailRow label="命名空间" value={pod.namespace} />
        <DetailRow
          label="UID"
          value={<span className="zke-mono text-xs break-all">{pod.uid || "—"}</span>}
        />
        <DetailRow
          label="版本"
          value={<span className="zke-mono text-xs">{pod.resource_version || "—"}</span>}
        />
        <DetailRow label="状态" value={<StatusBadge kind="pod" value={pod.phase || "Unknown"} />} />
        <DetailRow label="就绪" value={pod.ready ? "是" : "否"} />
        <DetailRow label="重启次数" value={<span className="zke-tnum">{pod.restart_count}</span>} />
        <DetailRow label="QoS" value={pod.qos_class || "—"} />
        {pod.reason || pod.message ? (
          <DetailRow
            label="原因"
            value={
              <span className="break-words">
                {[pod.reason, pod.message].filter(Boolean).join(" · ")}
              </span>
            }
          />
        ) : null}
        <DetailRow label="创建时间" value={formatAbsolute(pod.creation_timestamp)} />
        <DetailRow label="启动时间" value={formatAbsolute(pod.start_time)} />
        {pod.deletion_timestamp ? (
          <DetailRow label="删除时间" value={formatAbsolute(pod.deletion_timestamp)} />
        ) : null}
      </DetailCard>

      <DetailCard title="调度与网络">
        <DetailRow label="节点" value={pod.node_name || "尚未调度"} />
        {pod.nominated_node_name ? (
          <DetailRow label="提名节点" value={pod.nominated_node_name} />
        ) : null}
        <DetailRow
          label="Pod IP"
          // `pod_ips` is the dual-stack list and `pod_ip` its first entry; a
          // Pod that has one but not the other is a Kubernetes version
          // difference, not a Pod without an address.
          value={<AddressValues values={pod.pod_ips.length > 0 ? pod.pod_ips : [pod.pod_ip]} />}
        />
        <DetailRow label="宿主 IP" value={<AddressValues values={pod.host_ips} />} />
        <DetailRow label="主机网络" value={pod.host_network ? "是" : "否"} />
        <DetailRow label="ServiceAccount" value={pod.service_account_name || "—"} />
        <DetailRow label="调度器" value={pod.scheduler_name || "—"} />
        <DetailRow label="优先级类" value={pod.priority_class_name || "—"} />
        <DetailRow label="Runtime Class" value={pod.runtime_class_name || "—"} />
        <DetailRow label="重启策略" value={pod.restart_policy || "—"} />
        <DetailRow label="DNS 策略" value={pod.dns_policy || "—"} />
      </DetailCard>

      <ContainerCard title="容器" containers={pod.containers} />
      {pod.init_containers.length > 0 ? (
        <ContainerCard title="初始化容器" containers={pod.init_containers} />
      ) : null}
      {pod.ephemeral_containers.length > 0 ? (
        <ContainerCard title="临时容器" containers={pod.ephemeral_containers} />
      ) : null}

      <DetailCard title="Owner">
        {pod.owner_references.length === 0 ? (
          <DetailRow label="Owner" value="—" />
        ) : (
          pod.owner_references.map((owner) => (
            <DetailRow
              key={owner.uid || `${owner.kind}/${owner.name}`}
              label={owner.kind}
              value={<OwnerValue owner={owner} />}
            />
          ))
        )}
      </DetailCard>

      <DetailCard title="条件">
        <DetailConditions conditions={pod.conditions} />
      </DetailCard>

      <DetailCard title="标签">
        <DetailKeyValues entries={pod.labels} />
      </DetailCard>

      <DetailCard title="注解">
        <DetailKeyValues entries={pod.annotations} />
      </DetailCard>
    </div>
  );
}

function OwnerValue({ owner }: { owner: KubernetesPodOwnerReference }) {
  return (
    <div className="grid gap-0.5">
      <span className="break-all">
        {owner.name}
        {owner.controller ? (
          <Badge tone="info" className="ml-1.5">
            控制器
          </Badge>
        ) : null}
      </span>
      <span className="zke-mono text-subtle-foreground text-xs break-all">
        {owner.api_version} · {owner.uid || "—"}
      </span>
    </div>
  );
}

function ContainerCard({
  title,
  containers,
}: {
  title: string;
  containers: KubernetesPodContainer[];
}) {
  return (
    <DetailCard title={title}>
      {containers.length === 0 ? (
        <DetailRow label={title} value="—" />
      ) : (
        containers.map((container) => (
          <DetailRow
            key={container.name}
            label={container.name}
            value={<ContainerValue container={container} />}
          />
        ))
      )}
    </DetailCard>
  );
}

function ContainerValue({ container }: { container: KubernetesPodContainer }) {
  const resources = [
    ...formatResources("requests", container.requests),
    ...formatResources("limits", container.limits),
  ];
  return (
    <div className="grid gap-1">
      <span className="zke-mono text-xs break-all">{container.image || "—"}</span>
      <div className="flex flex-wrap items-center gap-1.5">
        <StatusBadge kind="containerState" value={container.state.type} />
        {container.ready ? null : (
          <Badge tone="warning">
            <StatusDot tone="warning" />
            未就绪
          </Badge>
        )}
        {container.restart_count > 0 ? (
          <span className="zke-tnum text-subtle-foreground text-xs">
            重启 {container.restart_count} 次
          </span>
        ) : null}
      </div>
      <StateLine label="当前" state={container.state} />
      {container.last_state && container.last_state.type !== "unknown" ? (
        <StateLine label="上次" state={container.last_state} />
      ) : null}
      {resources.length > 0 ? (
        <span className="text-subtle-foreground text-xs break-all">{resources.join(" · ")}</span>
      ) : null}
    </div>
  );
}

/** Reason, exit code and timing of one container state, when Kubernetes has them. */
function StateLine({ label, state }: { label: string; state: KubernetesPodContainer["state"] }) {
  const parts = [state.reason];
  if (state.exit_code !== undefined) {
    parts.push(`退出码 ${state.exit_code}`);
  }
  if (state.signal !== undefined && state.signal !== 0) {
    parts.push(`信号 ${state.signal}`);
  }
  const timestamp = state.finished_at ?? state.started_at;
  if (timestamp) {
    parts.push(formatAbsolute(timestamp));
  }
  const text = parts.filter(Boolean).join(" · ");
  if (!text) {
    return null;
  }
  return (
    <span className="text-muted-foreground text-xs break-words">
      {label}：{text}
    </span>
  );
}

function formatResources(label: string, resources: Record<string, string>): string[] {
  const entries = Object.entries(resources);
  if (entries.length === 0) {
    return [];
  }
  return [`${label} ${entries.map(([key, value]) => `${key}=${value}`).join(", ")}`];
}
