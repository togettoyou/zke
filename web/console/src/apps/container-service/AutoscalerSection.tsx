import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { FileCode, Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import {
  isAutoscalerStale,
  useAutoscaler,
  useAutoscalers,
  useDeleteAutoscaler,
  type AutoscalerDetail,
  type AutoscalerSummary,
} from "@/api/queries/autoscaling";
import type { KubernetesHPABehavior, KubernetesHPAMetricView } from "@/api/types";
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
import { RelativeTime } from "@/components/common/status";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { AutoscalerForm } from "./AutoscalerForm";
import { useContinuePagination } from "./use-continue-pagination";
import type { ClusterSectionProps } from "./types";
import { YamlEditorView } from "./YamlEditorView";

const PAGE_SIZE = 50;

type AutoscalerSectionProps = ClusterSectionProps & {
  /** The Namespace every query and mutation in this section is scoped to. */
  namespace: string;
};

/**
 * HorizontalPodAutoscalers of one Namespace of one Cluster.
 *
 * An HPA owns its target's replica count, which is the one thing about this
 * page an operator has to keep in mind: once one exists, scaling the Deployment
 * by hand is undone by the controller within a cycle.
 */
export function AutoscalerSection({
  clusterId,
  clusterName,
  namespace,
  tenantId,
  projectId,
}: AutoscalerSectionProps) {
  const { permissions } = useSessionContext();
  const pager = useContinuePagination(`${clusterId}/${namespace}`);
  const list = useAutoscalers(clusterId, namespace, {
    limit: PAGE_SIZE,
    ...(pager.token ? { continue: pager.token } : {}),
  });
  const [detailName, setDetailName] = useState<string | null>(null);
  const [yamlName, setYamlName] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<AutoscalerSummary | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<AutoscalerSummary | null>(null);
  const [deletePreviewed, setDeletePreviewed] = useState(false);
  const deletePreviewKey = useSubmissionKey(deleteTarget !== null);
  const deleteApplyKey = useSubmissionKey(deleteTarget !== null);
  const remove = useDeleteAutoscaler();

  const projectScope = { type: "project" as const, tenantId, projectId };
  const canCreate = permissions.can("cluster.resource.create", projectScope);
  const canUpdate = permissions.can("cluster.resource.update", projectScope);
  const canDelete = permissions.can("cluster.resource.delete", projectScope);

  const columns = useMemo<ColumnDef<AutoscalerSummary, unknown>[]>(
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
      {
        header: "目标",
        size: 200,
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground break-all">{row.original.target.name}</span>
            <span className="text-subtle-foreground text-xs">{row.original.target.kind}</span>
          </div>
        ),
      },
      {
        header: "副本",
        size: 130,
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="zke-tnum text-foreground">
              {row.original.current_replicas} → {row.original.desired_replicas}
            </span>
            <span className="text-subtle-foreground zke-tnum text-xs">
              区间 {row.original.min_replicas}–{row.original.max_replicas}
            </span>
          </div>
        ),
      },
      {
        header: "指标",
        size: 80,
        cell: ({ row }) => <span className="zke-tnum">{row.original.metric_count}</span>,
      },
      {
        header: "状态",
        size: 170,
        cell: ({ row }) => <AutoscalerStatus item={row.original} />,
      },
      {
        header: "最近伸缩",
        size: 130,
        cell: ({ row }) => (
          <RelativeTime value={row.original.last_scale_time} className="text-muted-foreground" />
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
    [canUpdate, canDelete, remove],
  );

  if (yamlName) {
    return (
      <YamlEditorView
        identity={{
          clusterId,
          group: "autoscaling",
          version: "v2",
          resource: "horizontalpodautoscalers",
          namespace,
          name: yamlName,
        }}
        clusterName={clusterName}
        kindLabel="HorizontalPodAutoscaler"
        canUpdate={canUpdate}
        onBack={() => setYamlName(null)}
      />
    );
  }

  if (detailName) {
    return (
      <AutoscalerDetailView
        clusterId={clusterId}
        namespace={namespace}
        name={detailName}
        canUpdate={canUpdate}
        onEdit={setEditing}
        onOpenYaml={() => setYamlName(detailName)}
        onBack={() => setDetailName(null)}
      />
    );
  }

  const nextToken = list.data?.continue_token ?? "";

  return (
    <div className="flex h-full min-h-0 flex-col">
      <SectionToolbarActions>
        <RefreshAction isFetching={list.isFetching} onRefresh={() => void list.refetch()} />
        {canCreate ? (
          <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
            <Plus />
            创建 HPA
          </Button>
        ) : null}
      </SectionToolbarActions>
      <DataTable
        columns={columns}
        data={list.data?.autoscalers}
        isLoading={list.isLoading}
        isFetching={list.isFetching}
        error={list.error}
        onRetry={() => void list.refetch()}
        onRowClick={(item) => setDetailName(item.name)}
        rowKey={(item) => item.uid || item.name}
        emptyTitle="该命名空间没有 HorizontalPodAutoscaler"
        emptyDescription={`${namespace} 中没有可见的 HPA。`}
        continuePagination={{
          pageIndex: pager.pageIndex,
          nextToken,
          onPrevious: pager.goPrevious,
          onNext: pager.goNext,
        }}
      />

      {creating || editing ? (
        <AutoscalerForm
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          existingName={editing?.name ?? null}
          onClose={() => {
            setCreating(false);
            setEditing(null);
          }}
        />
      ) : null}

      <SensitiveActionDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title="删除 HorizontalPodAutoscaler"
        description={
          deletePreviewed
            ? "DryRun 已通过。再次确认将提交实际删除。"
            : "首次点击只执行服务端 DryRun；预检通过后才能实际删除。"
        }
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          { label: "HPA", name: deleteTarget?.name ?? "", id: deleteTarget?.uid },
        ]}
        impacts={[
          `${deleteTarget?.target.kind ?? "目标工作负载"} ${deleteTarget?.target.name ?? ""} 的副本数将停留在删除时的值，不再随指标变化。`,
          "工作负载本身不受影响，之后可以手动伸缩或重新创建 HPA。",
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
                toast.success("HPA 删除 DryRun 已通过");
                return;
              }
              toast.success(`HPA ${deleteTarget.name} 已提交删除`);
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

/**
 * The three status conditions the Server reduces, plus whether the controller
 * has caught up with the current spec.
 *
 * Only the states that need attention are shown: a healthy HPA is
 * able-to-scale, active and not limited, and saying so three times per row would
 * bury the one that is not.
 */
function AutoscalerStatus({ item }: { item: AutoscalerSummary }) {
  if (isAutoscalerStale(item)) {
    return (
      <Badge tone="info">
        <StatusDot tone="info" />
        尚未同步
      </Badge>
    );
  }
  const problems = [
    !item.able_to_scale ? { label: "无法伸缩", tone: "danger" as const } : null,
    !item.scaling_active ? { label: "指标不可用", tone: "warning" as const } : null,
    item.scaling_limited ? { label: "已触达上下限", tone: "warning" as const } : null,
  ].filter((entry) => entry !== null);

  if (problems.length === 0) {
    return (
      <Badge tone="success">
        <StatusDot tone="success" />
        正常
      </Badge>
    );
  }
  return (
    <div className="flex flex-col items-start gap-0.5">
      {problems.map((problem) => (
        <Badge key={problem.label} tone={problem.tone}>
          <StatusDot tone={problem.tone} />
          {problem.label}
        </Badge>
      ))}
    </div>
  );
}

function AutoscalerDetailView({
  clusterId,
  namespace,
  name,
  canUpdate,
  onEdit,
  onOpenYaml,
  onBack,
}: {
  clusterId: string;
  namespace: string;
  name: string;
  canUpdate: boolean;
  onEdit: (item: AutoscalerSummary) => void;
  onOpenYaml: () => void;
  onBack: () => void;
}) {
  const detail = useAutoscaler(clusterId, namespace, name);
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
        <AutoscalerDetailCards item={item} />
      )}
    </div>
  );
}

function AutoscalerDetailCards({ item }: { item: AutoscalerDetail }) {
  return (
    <div className="grid gap-3 md:grid-cols-2">
      <DetailCard title="概览">
        <DetailRow label="名称" value={item.name} />
        <DetailRow label="命名空间" value={item.namespace} />
        <DetailRow
          label="目标"
          value={
            <span className="break-all">
              {item.target.kind}/{item.target.name}
              <span className="zke-mono text-subtle-foreground ml-2 text-xs">
                {item.target.api_version}
              </span>
            </span>
          }
        />
        <DetailRow
          label="副本区间"
          value={
            <span className="zke-tnum">
              {item.min_replicas} – {item.max_replicas}
            </span>
          }
        />
        <DetailRow
          label="当前 / 期望"
          value={
            <span className="zke-tnum">
              {item.current_replicas} → {item.desired_replicas}
            </span>
          }
        />
        <DetailRow label="状态" value={<AutoscalerStatus item={item} />} />
        <DetailRow
          label="Generation"
          value={
            <span className="zke-tnum">
              {item.generation}（已观察 {item.observed_generation ?? "—"}）
            </span>
          }
        />
        <DetailRow
          label="最近伸缩"
          value={item.last_scale_time ? formatAbsolute(item.last_scale_time) : "尚未发生"}
        />
        <DetailRow label="创建时间" value={formatAbsolute(item.creation_timestamp)} />
      </DetailCard>

      <DetailCard title="指标">
        {item.metrics.length === 0 ? (
          <DetailRow label="指标" value="—" />
        ) : (
          item.metrics.map((metric, index) => (
            <DetailRow
              key={`${metric.type}/${metric.name}/${metric.container}/${index}`}
              label={metricLabel(metric)}
              value={
                <div className="grid gap-0.5">
                  <span>目标 {targetLabel(metric.target)}</span>
                  <span className="text-muted-foreground text-xs">
                    当前 {targetLabel(currentFor(item.current_metrics, metric)) || "指标不可用"}
                  </span>
                </div>
              }
            />
          ))
        )}
      </DetailCard>

      {item.behavior ? <BehaviorCard behavior={item.behavior} /> : null}

      <DetailCard title="条件">
        <DetailConditions conditions={item.conditions} />
      </DetailCard>

      <DetailCard title="标签">
        <DetailKeyValues entries={item.labels} />
      </DetailCard>
      <DetailCard title="注解">
        <DetailKeyValues entries={item.annotations} />
      </DetailCard>
    </div>
  );
}

function BehaviorCard({ behavior }: { behavior: KubernetesHPABehavior }) {
  const describe = (rules: NonNullable<KubernetesHPABehavior["scale_up"]>) => (
    <div className="grid gap-0.5">
      <span className="text-xs">
        稳定窗口 {rules.stabilization_window_seconds ?? "默认"} 秒 · 策略选择{" "}
        {rules.select_policy ?? "默认"}
      </span>
      {rules.policies.map((policy, index) => (
        <span key={index} className="zke-mono text-muted-foreground text-xs">
          {policy.type} {policy.value} / {policy.period_seconds}s
        </span>
      ))}
    </div>
  );

  return (
    <DetailCard title="伸缩行为">
      {behavior.scale_up ? <DetailRow label="扩容" value={describe(behavior.scale_up)} /> : null}
      {behavior.scale_down ? (
        <DetailRow label="缩容" value={describe(behavior.scale_down)} />
      ) : null}
      {!behavior.scale_up && !behavior.scale_down ? (
        <DetailRow label="行为" value="使用 Kubernetes 默认策略" />
      ) : null}
    </DetailCard>
  );
}

function metricLabel(metric: KubernetesHPAMetricView): string {
  if (metric.type === "ContainerResource") {
    return `${metric.name}（容器 ${metric.container}）`;
  }
  if (metric.type === "Object" && metric.described_object) {
    return `${metric.metric?.name ?? metric.type}（${metric.described_object.kind}/${metric.described_object.name}）`;
  }
  return metric.name || metric.metric?.name || metric.type;
}

/** Matches a current reading to the target it belongs to. */
function currentFor(
  current: KubernetesHPAMetricView[],
  metric: KubernetesHPAMetricView,
): KubernetesHPAMetricView["target"] | undefined {
  return current.find(
    (entry) =>
      entry.type === metric.type &&
      entry.name === metric.name &&
      entry.container === metric.container &&
      entry.metric?.name === metric.metric?.name &&
      entry.described_object?.api_version === metric.described_object?.api_version &&
      entry.described_object?.kind === metric.described_object?.kind &&
      entry.described_object?.name === metric.described_object?.name,
  )?.current;
}

function targetLabel(target: KubernetesHPAMetricView["target"] | undefined): string {
  if (!target) {
    return "";
  }
  if (target.average_utilization !== undefined) {
    return `${target.average_utilization}%`;
  }
  return target.average_value || target.value || "";
}
