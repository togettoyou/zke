import { useCallback, useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { useQueryClient } from "@tanstack/react-query";
import { Download, RefreshCcw } from "lucide-react";
import { toast } from "sonner";

import { isApiError } from "@/api/errors";
import { useClusters } from "@/api/queries/clusters";
import {
  useInstallMetricsCollector,
  useMetricsCollectorStates,
  useUninstallMetricsCollector,
} from "@/api/queries/observability";
import { queryKeys } from "@/api/query-keys";
import {
  DEFAULT_PAGE_SIZE,
  type ClusterAggregate,
  type MetricsCollectorState,
  type MetricsComponentState,
} from "@/api/types";
import { SectionTitle } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
import { RowDeleteAction } from "@/components/common/delete-action";
import { RefreshAction } from "@/components/common/refresh-action";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { RelativeTime, StatusBadge } from "@/components/common/status";
import { Button } from "@/components/ui/button";
import { useWindowVisible } from "@/desktop/window-visibility";
import { useScopeStore } from "@/scope/scope-store";

/** What one row knows about its Cluster's collector. */
type CollectorRow = {
  /** Absent while the Cluster is still being asked, or when it refused. */
  state?: MetricsCollectorState;
  status: string;
  pending: boolean;
};

/**
 * Turning collection on and off, one row per Cluster.
 *
 * A table rather than a Cluster picker with one panel under it: enabling
 * collection is a rollout across a fleet, and "which of my Clusters are
 * reporting" is not a question a control showing one answer at a time can
 * answer. It also removes the picker's worst property, that a Cluster with no
 * collector looked exactly like a Cluster nobody had selected yet.
 *
 * Each row's state is read from that Cluster through its Agent, not from a
 * Server record, so a collector somebody removed by hand shows up as removed.
 * Installing is the Agent's work: nothing here writes Kubernetes objects, and
 * no credential ever reaches the browser.
 */
export function CollectionSection() {
  const live = useWindowVisible();
  const queryClient = useQueryClient();
  const { permissions } = useSessionContext();
  const scope = useScopeStore((state) => state.scope);
  const projectId = scope.projectId;
  const [offset, setOffset] = useState(0);
  const clusters = useClusters(projectId, {
    status: "active",
    limit: DEFAULT_PAGE_SIZE,
    offset,
  });
  const rows = useMemo(() => clusters.data?.clusters ?? [], [clusters.data]);
  const clusterIds = useMemo(() => rows.map((cluster) => cluster.id), [rows]);
  const collectors = useMetricsCollectorStates(clusterIds, { live });
  const install = useInstallMetricsCollector();
  const uninstall = useUninstallMetricsCollector();
  const [removalTarget, setRemovalTarget] = useState<ClusterAggregate | null>(null);
  const [installTarget, setInstallTarget] = useState<ClusterAggregate | null>(null);

  const canManage = permissions.can("cluster.metrics.manage", {
    type: "project",
    tenantId: scope.tenantId,
    projectId,
  });

  // Keyed by Cluster rather than positional, so a row reads its own answer even
  // while the page beneath it is being replaced.
  const byCluster = useMemo(
    () =>
      new Map<string, CollectorRow>(
        clusterIds.map((clusterId, index) => {
          const query = collectors[index];
          return [clusterId, describeCollector(query?.data, query?.error, query?.isPending)];
        }),
      ),
    [clusterIds, collectors],
  );

  const refresh = useCallback(() => {
    void clusters.refetch();
    for (const clusterId of clusterIds) {
      void queryClient.invalidateQueries({ queryKey: queryKeys.metricsCollector(clusterId) });
    }
  }, [clusterIds, clusters, queryClient]);

  // Opening a dialog clears what the previous attempt left behind: a mutation
  // holds its error until it is reset, and nothing about picking a new target
  // touches the mutation behind it.
  const openInstall = useCallback(
    (cluster: ClusterAggregate) => {
      install.reset();
      setInstallTarget(cluster);
    },
    [install],
  );
  const openRemoval = useCallback(
    (cluster: ClusterAggregate) => {
      uninstall.reset();
      setRemovalTarget(cluster);
    },
    [uninstall],
  );

  const installState = installTarget ? byCluster.get(installTarget.id)?.state : undefined;

  const columns = useMemo<ColumnDef<ClusterAggregate, unknown>[]>(
    () => [
      {
        header: "集群",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground font-medium">{row.original.name}</span>
            <span className="text-subtle-foreground zke-mono text-xs">
              {byCluster.get(row.original.id)?.state?.namespace || "—"}
            </span>
          </div>
        ),
      },
      {
        header: "采集状态",
        size: 150,
        cell: ({ row }) => {
          const item = byCluster.get(row.original.id);
          if (item?.pending) {
            return <span className="text-subtle-foreground text-xs">探测中…</span>;
          }
          const state = item?.state;
          return (
            <div className="flex flex-col items-start gap-0.5">
              <StatusBadge kind="metrics_collector" value={item?.status ?? "unreadable"} />
              {state?.installed ? (
                <span className="text-subtle-foreground text-xs">
                  {state.ready_replicas}/{state.desired_replicas} 就绪
                </span>
              ) : null}
            </div>
          );
        },
      },
      {
        // All three components, not just the collector. Two of them are the
        // difference between a usage curve and a usable one — without the object
        // exporter there is no denominator for any utilisation view, and without
        // the node exporter there is no disk or network at all — so a row that
        // showed only the collector would call a half-installed pipeline healthy.
        header: "组件",
        size: 320,
        cell: ({ row }) => {
          const state = byCluster.get(row.original.id)?.state;
          if (!state) {
            return <span className="text-subtle-foreground text-xs">—</span>;
          }
          if (state.components.length === 0) {
            // An Agent too old to report components installed the collector
            // alone; showing three rows would name two workloads that are not in
            // that Cluster.
            return (
              <div className="flex flex-col gap-0.5">
                <span className="zke-mono text-muted-foreground text-xs">
                  {state.installed ? state.image : state.desired_image}
                </span>
                <span className="text-subtle-foreground text-xs">
                  该集群 Agent 版本不报告组件明细
                </span>
              </div>
            );
          }
          return (
            <div className="flex flex-col gap-1">
              {state.components.map((component) => (
                <ComponentLine key={component.component} component={component} />
              ))}
            </div>
          );
        },
      },
      {
        header: "摄取凭证",
        size: 130,
        cell: ({ row }) => {
          const state = byCluster.get(row.original.id)?.state;
          return (
            <span className="text-muted-foreground text-xs">
              {state ? (state.credential_ready ? "已就绪（集群内）" : "未生成") : "—"}
            </span>
          );
        },
      },
      {
        // The Server's own verdict on this Cluster's stream. It has to be here:
        // a gap caused by the Server refusing batches looks identical to a
        // broken collector, and an operator reading only the collector column
        // would go and reinstall something that is working.
        header: "摄取预算",
        size: 170,
        cell: ({ row }) => {
          const state = byCluster.get(row.original.id)?.state;
          if (!state) {
            return <span className="text-subtle-foreground text-xs">—</span>;
          }
          return (
            <div className="flex flex-col items-start gap-0.5">
              <StatusBadge kind="metrics_ingest" value={ingestStatus(state)} />
              {state.max_active_series > 0 ? (
                <span className="text-subtle-foreground text-xs">
                  约 {formatSeriesCount(state.active_series)} /{" "}
                  {formatSeriesCount(state.max_active_series)} 序列
                </span>
              ) : null}
              {!state.throttled && state.last_throttled_at ? (
                <span className="text-warning text-xs">
                  曾于 <RelativeTime value={state.last_throttled_at} /> 被限流
                </span>
              ) : null}
            </div>
          );
        },
      },
      {
        id: "actions",
        header: "",
        size: 88,
        cell: ({ row }) => {
          const state = byCluster.get(row.original.id)?.state;
          // Without a reading from the Cluster there is nothing to act on: an
          // install offered next to 「Agent 未连接」 would only fail on arrival.
          if (!state || !canManage) {
            return null;
          }
          return (
            <div
              className="flex items-center justify-end"
              onClick={(event) => event.stopPropagation()}
            >
              <Button
                size="icon-sm"
                variant="ghost"
                className={state.installed ? undefined : "text-success hover:text-success"}
                aria-label={`${installLabel(state)} ${row.original.name}`}
                title={installLabel(state)}
                disabled={install.isPending}
                onClick={() => openInstall(row.original)}
              >
                {state.installed ? <RefreshCcw /> : <Download />}
              </Button>
              {state.installed ? (
                <RowDeleteAction
                  name={`${row.original.name} 的采集组件`}
                  hint="卸载采集组件"
                  disabled={uninstall.isPending}
                  onDelete={() => openRemoval(row.original)}
                />
              ) : null}
            </div>
          );
        },
      },
    ],
    [byCluster, canManage, install.isPending, openInstall, openRemoval, uninstall.isPending],
  );

  // No scope guard here: the application does not open its shell without a
  // Project, so this section only ever renders with one.
  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* The rail already says 采集接入, so the heading is here only for the one
          sentence an operator needs before installing anything into a Cluster
          they run. */}
      <SectionTitle
        description="三个采集组件由每个集群自己的 Agent 一并安装到其 Agent Namespace，也一并卸载；数据经 Agent 已有的连接回传，摄取凭证在集群内生成，不经过 Server。"
        actions={
          <RefreshAction
            isFetching={clusters.isFetching || collectors.some((query) => query.isFetching)}
            onRefresh={refresh}
          />
        }
      />

      <DataTable
        columns={columns}
        data={rows}
        isLoading={clusters.isLoading}
        isFetching={clusters.isFetching}
        error={clusters.error}
        onRetry={() => void clusters.refetch()}
        rowKey={(cluster) => cluster.id}
        emptyTitle="该项目下没有已接入的集群"
        emptyDescription="先在「集群接入管理」中接入集群，再为其启用指标采集。"
        pagination={{ value: clusters.data?.pagination, onOffsetChange: setOffset }}
      />

      {/* Installing is a write into a Cluster somebody else runs — a workload,
          its RBAC and a credential — so it is confirmed like every other one.
          Reinstalling is the case that most needs it: the row already says
          「运行中」, and the Deployment is replaced with Recreate, so the
          Cluster stops reporting for as long as the new Pod takes to start. */}
      <SensitiveActionDialog
        open={installTarget !== null}
        onOpenChange={(open) => !open && setInstallTarget(null)}
        title={installState ? installLabel(installState) : "安装采集组件"}
        description={
          installState?.installed
            ? "采集组件将按当前平台配置重新部署到目标集群。"
            : "目标集群的 Agent 将在其 Agent Namespace 中部署采集组件。"
        }
        scopeLines={
          installTarget
            ? [
                { label: "集群", name: installTarget.name, id: installTarget.id },
                { label: "Namespace", name: installState?.namespace ?? "" },
                { label: "镜像", name: installState?.desired_image ?? "" },
              ]
            : []
        }
        impacts={
          installState?.installed
            ? [
                "三个组件按平台配置当前的镜像与资源取值重新应用",
                "采集组件按 Recreate 策略重建，重建期间该集群暂停上报，曲线会出现一段空洞",
                "已有的摄取凭证保持不变，不需要重新授权",
                isOutdated(installState)
                  ? "运行中的镜像将被替换为平台配置当前指定的版本"
                  : "镜像与资源取值按平台配置当前的值重新应用",
              ]
            : [
                "在该集群的 Agent Namespace 中安装三个组件：采集组件（vmagent）、对象指标导出器（kube-state-metrics）与节点指标导出器（node-exporter），以及它们各自的 ServiceAccount 与 RBAC",
                "对象指标导出器只读取 Node、Pod、Namespace 与工作负载对象，不包含 Secret",
                "节点指标导出器以 DaemonSet 运行在每个节点上，需要 host 网络与 hostPath；运行在 baseline 或 restricted Pod Security 级别的 Namespace 会拒绝它，此时其余组件照常安装并在列表中说明原因",
                "摄取凭证由 Agent 在集群内生成，不经过 Server，也不会出现在浏览器里",
                "组件会在一个采集周期内开始上报，指标随即出现在「集群总览」等图表分区中",
              ]
        }
        confirmLabel={installState?.installed ? "重新安装" : "安装"}
        pending={install.isPending}
        error={install.error}
        onConfirm={() => {
          if (!installTarget) {
            return;
          }
          const installed = Boolean(installState?.installed);
          install.mutate(installTarget.id, {
            onSuccess: () => {
              setInstallTarget(null);
              toast.success(installed ? "采集组件已重新安装" : "采集组件已安装");
            },
            // The refusal is rendered by the dialog itself, through `error`.
          });
        }}
      />

      {/* Removing collection deletes a workload and its credential from a real
          Cluster, so it goes through the same confirmation as other destructive
          Cluster operations rather than a bare button. */}
      <SensitiveActionDialog
        open={removalTarget !== null}
        onOpenChange={(open) => !open && setRemovalTarget(null)}
        title="卸载指标采集组件"
        description="将从目标集群删除三个采集组件、它们的 RBAC 与摄取凭证。已经写入存储的历史指标不受影响。"
        scopeLines={
          removalTarget ? [{ label: "集群", name: removalTarget.name, id: removalTarget.id }] : []
        }
        confirmationText={removalTarget?.name}
        destructive
        confirmLabel="卸载"
        impacts={[
          "该集群停止上报指标，各图表分区中该集群的曲线会出现空洞",
          "三个组件与它们的 ServiceAccount、ClusterRole、ClusterRoleBinding 一并删除，包括每个节点上的 node-exporter",
          "摄取凭证被删除；重新安装会生成新的凭证",
        ]}
        pending={uninstall.isPending}
        error={uninstall.error}
        onConfirm={() => {
          if (!removalTarget) {
            return;
          }
          uninstall.mutate(removalTarget.id, {
            onSuccess: () => {
              setRemovalTarget(null);
              toast.success("采集组件已卸载");
            },
            // The refusal is rendered by the dialog itself, through `error`.
          });
        }}
      />
    </div>
  );
}

const COMPONENT_LABELS: Record<string, string> = {
  collector: "采集组件",
  "kube-state-metrics": "对象指标",
  "node-exporter": "节点指标",
};

/**
 * One component of the bundle, with the thing about it an operator has to act on.
 *
 * A refused node exporter is the case this row exists for: it is not a failure
 * of the install and not something waiting to become ready, so it reads as
 * neither. Saying only "未安装" would leave the operator reinstalling until they
 * gave up.
 */
function ComponentLine({ component }: { component: MetricsComponentState }) {
  const label = COMPONENT_LABELS[component.component] ?? component.component;
  return (
    <div className="flex flex-col gap-0.5">
      <div className="flex items-baseline gap-2">
        <span className="text-foreground text-xs">{label}</span>
        {component.installed ? (
          <span className="text-subtle-foreground text-xs">
            {component.ready_replicas}/{component.desired_replicas} 就绪
          </span>
        ) : (
          <span
            className={
              component.unavailable_reason
                ? "text-danger text-xs"
                : "text-subtle-foreground text-xs"
            }
          >
            {component.unavailable_reason ? "已被集群拒绝" : "未安装"}
          </span>
        )}
      </div>
      {component.installed && component.image ? (
        <span className="zke-mono text-subtle-foreground text-[11px]">{component.image}</span>
      ) : null}
      {isComponentOutdated(component) ? (
        <span className="text-warning text-[11px]">平台配置已更新为 {component.desired_image}</span>
      ) : null}
      {component.unavailable_message ? (
        <span className="text-danger text-[11px] leading-relaxed">
          {component.unavailable_message}
        </span>
      ) : null}
    </div>
  );
}

function isComponentOutdated(component: MetricsComponentState): boolean {
  return Boolean(
    component.installed &&
    component.image &&
    component.desired_image &&
    component.image !== component.desired_image,
  );
}

/** A Cluster running an older collector than the platform setting now names. */
function isOutdated(state: MetricsCollectorState): boolean {
  return Boolean(
    state.installed && state.image && state.desired_image && state.image !== state.desired_image,
  );
}

/**
 * Which ingest budget, if any, this Server is currently refusing the Cluster on.
 *
 * A Cluster the Server has never heard from is 尚未上报 rather than 正常接收:
 * saying "accepted" about a stream that has never arrived would report a
 * misconfigured collector as healthy.
 */
function ingestStatus(state: MetricsCollectorState): string {
  if (state.throttled) {
    return state.throttle_reason === "cardinality" ? "throttled_cardinality" : "throttled_rate";
  }
  return state.max_active_series > 0 ? "accepted" : "idle";
}

/**
 * The series count is an estimate from a fixed-size sketch, so it is rounded to
 * the precision it actually has. Printing 483,912 would claim a count nobody
 * measured.
 */
function formatSeriesCount(value: number): string {
  if (value >= 1_000_000) {
    return `${(value / 1_000_000).toFixed(1)}M`;
  }
  if (value >= 1_000) {
    return `${Math.round(value / 1_000)}k`;
  }
  return String(value);
}

function installLabel(state: MetricsCollectorState): string {
  if (!state.installed) {
    return "安装采集组件";
  }
  return isOutdated(state) ? "更新到当前镜像" : "重新安装";
}

/**
 * One status word per row, whether or not the Cluster answered.
 *
 * A failure is a state of the row rather than an error banner over the table:
 * one offline Agent must not hide what the other Clusters just reported.
 */
function describeCollector(
  state: MetricsCollectorState | undefined,
  error: unknown,
  pending: boolean | undefined,
): CollectorRow {
  if (pending) {
    return { status: "unreadable", pending: true };
  }
  if (error || !state) {
    return { status: unreadableStatus(error), pending: false };
  }
  if (!state.installed) {
    return { state, status: "not_installed", pending: false };
  }
  return { state, status: state.ready_replicas > 0 ? "running" : "starting", pending: false };
}

function unreadableStatus(error: unknown): string {
  if (isApiError(error)) {
    if (error.code === "agent_unavailable") {
      return "agent_unavailable";
    }
    if (error.status === 403) {
      return "forbidden";
    }
  }
  return "unreadable";
}
