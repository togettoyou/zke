import { useCallback, useMemo, useState, type ReactNode } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { FileCode, Pencil, Plus, Stethoscope } from "lucide-react";
import { toast } from "sonner";

import {
  isAutoscalerStale,
  useAutoscaler,
  useAutoscalerMetricTrend,
  useAutoscalers,
  useDeleteAutoscaler,
  type AutoscalerDetail,
  type AutoscalerSummary,
} from "@/api/queries/autoscaling";
import type { KubernetesHPABehavior, KubernetesHPAMetricView } from "@/api/types";
import { useAutoscalerDescribe } from "@/api/queries/describe";
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
import { RelativeTime } from "@/components/common/status";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { AutoscalerForm } from "./AutoscalerForm";
import {
  KEDAScaledObjectSection,
  VerticalPodAutoscalerSection,
} from "./AutoscalingExtensionSection";
import { DescribeView } from "./DescribeView";
import { useContinuePagination } from "./use-continue-pagination";
import type { ClusterSectionProps } from "./types";
import { YamlEditorView } from "./YamlEditorView";

const PAGE_SIZE = 50;

type AutoscalerSectionProps = ClusterSectionProps & {
  /** The Namespace every query and mutation in this section is scoped to. */
  namespace: string;
  /** Resource-kind switch, rendered only by a list view. */
  tabs?: ReactNode;
};

/**
 * HorizontalPodAutoscalers of one Namespace of one Cluster.
 *
 * An HPA owns its target's replica count, which is the one thing about this
 * page an operator has to keep in mind: once one exists, scaling the Deployment
 * by hand is undone by the controller within a cycle.
 */
export function AutoscalerSection(props: AutoscalerSectionProps) {
  const [kind, setKind] = useState<"hpa" | "vpa" | "keda">("hpa");
  const tabs = (
    <Tabs value={kind} onValueChange={(value) => setKind(value as typeof kind)}>
      <TabsList className="mb-3 w-fit">
        <TabsTrigger value="hpa">HPA</TabsTrigger>
        <TabsTrigger value="vpa">VPA</TabsTrigger>
        <TabsTrigger value="keda">KEDA</TabsTrigger>
      </TabsList>
    </Tabs>
  );

  const sectionProps = { ...props, tabs };
  if (kind === "vpa") return <VerticalPodAutoscalerSection {...sectionProps} />;
  if (kind === "keda") return <KEDAScaledObjectSection {...sectionProps} />;
  return <HorizontalPodAutoscalerSection {...sectionProps} />;
}

function HorizontalPodAutoscalerSection({
  clusterId,
  clusterName,
  namespace,
  tenantId,
  projectId,
  tabs,
}: AutoscalerSectionProps) {
  const { permissions } = useSessionContext();
  const pager = useContinuePagination(`${clusterId}/${namespace}`);
  const list = useAutoscalers(clusterId, namespace, {
    limit: PAGE_SIZE,
    ...(pager.token ? { continue: pager.token } : {}),
  });
  const [detailName, setDetailName] = useState<string | null>(null);
  const [yamlName, setYamlName] = useState<string | null>(null);
  const [describeName, setDescribeName] = useState<string | null>(null);
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
  const canDescribe = permissions.can("cluster.event.read", projectScope);

  // Both the row action and the detail view open the same confirmation, so it is
  // one callback rather than two copies of the reset sequence.
  const openDelete = useCallback(
    (item: AutoscalerSummary) => {
      setDeleteTarget(item);
      setDeletePreviewed(false);
      remove.reset();
    },
    [remove],
  );

  const columns = useMemo<ColumnDef<AutoscalerSummary, unknown>[]>(
    () => [
      {
        header: "名称",
        size: 260,
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground font-medium break-all">{row.original.name}</span>
            <span className="zke-mono text-subtle-foreground text-xs whitespace-nowrap">
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
        size: canDescribe ? 128 : 88,
        cell: ({ row }) => (
          <div className="flex justify-end gap-0.5" onClick={(event) => event.stopPropagation()}>
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
              <RowDeleteAction name={row.original.name} onDelete={() => openDelete(row.original)} />
            ) : null}
          </div>
        ),
      },
    ],
    [canUpdate, canDelete, canDescribe, openDelete],
  );

  // The dialogs live outside the branch that picks a view. They are opened from
  // the list and from the detail page alike, and JSX that exists only in the
  // list's branch cannot open over the detail — the operator would have to go
  // back before the dialog appeared, by which point it is confirming an object
  // they can no longer see.
  const dialogs = (
    <>
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
    </>
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

  if (describeName) {
    return (
      <AutoscalerDescribeView
        clusterId={clusterId}
        namespace={namespace}
        name={describeName}
        onBack={() => setDescribeName(null)}
      />
    );
  }

  // The form takes over the section rather than sitting over the list, like every
  // other typed form here: a few metrics with both scaling directions customised
  // is taller than a box laid over the table can show, and the table is of no use
  // while they are being filled in.
  if (creating || editing) {
    return (
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
    );
  }

  if (detailName) {
    return (
      <>
        <AutoscalerDetailView
          clusterId={clusterId}
          namespace={namespace}
          name={detailName}
          canUpdate={canUpdate}
          canDelete={canDelete}
          canDescribe={canDescribe}
          onEdit={setEditing}
          onOpenYaml={() => setYamlName(detailName)}
          onDescribe={() => setDescribeName(detailName)}
          onDelete={openDelete}
          onBack={() => setDetailName(null)}
        />
        {dialogs}
      </>
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
      {tabs}
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

      {dialogs}
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
  canDelete,
  canDescribe,
  onEdit,
  onOpenYaml,
  onDescribe,
  onDelete,
  onBack,
}: {
  clusterId: string;
  namespace: string;
  name: string;
  canUpdate: boolean;
  canDelete: boolean;
  canDescribe: boolean;
  onEdit: (item: AutoscalerSummary) => void;
  onOpenYaml: () => void;
  onDescribe: () => void;
  onDelete: (item: AutoscalerSummary) => void;
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
            {canDescribe ? (
              <Button size="sm" variant="secondary" onClick={onDescribe}>
                <Stethoscope />
                诊断
              </Button>
            ) : null}
            <Button size="sm" variant="secondary" onClick={onOpenYaml}>
              <FileCode />
              YAML
            </Button>
            {canUpdate && item ? (
              <Button size="sm" variant="secondary" onClick={() => onEdit(item)}>
                <Pencil />
                编辑
              </Button>
            ) : null}
            {/* Waits for the object: the deletion carries its UID and
                resourceVersion as preconditions, and neither exists yet. */}
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
        <AutoscalerDetailCards clusterId={clusterId} namespace={namespace} item={item} />
      )}
    </div>
  );
}

function AutoscalerDescribeView({
  clusterId,
  namespace,
  name,
  onBack,
}: {
  clusterId: string;
  namespace: string;
  name: string;
  onBack: () => void;
}) {
  const describe = useAutoscalerDescribe(clusterId, namespace, name);
  return (
    <DescribeView
      name={name}
      kindLabel="HorizontalPodAutoscaler"
      data={describe.data}
      isLoading={describe.isLoading}
      isFetching={describe.isFetching}
      error={describe.error}
      onRetry={() => void describe.refetch()}
      onBack={onBack}
    />
  );
}

function AutoscalerDetailCards({
  clusterId,
  namespace,
  item,
}: {
  clusterId: string;
  namespace: string;
  item: AutoscalerDetail;
}) {
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

      <AutoscalerTrendCard clusterId={clusterId} namespace={namespace} name={item.name} />

      <DetailCard title="标签">
        <DetailKeyValues entries={item.labels} />
      </DetailCard>
      <DetailCard title="注解">
        <DetailKeyValues entries={item.annotations} />
      </DetailCard>
    </div>
  );
}

function AutoscalerTrendCard({
  clusterId,
  namespace,
  name,
}: {
  clusterId: string;
  namespace: string;
  name: string;
}) {
  const trend = useAutoscalerMetricTrend(clusterId, namespace, name);
  const points = trend.data?.points ?? [];
  const values = points.flatMap((point) => [point.current_replicas, point.desired_replicas]);
  const maximum = Math.max(1, ...values);
  const polyline = (field: "current_replicas" | "desired_replicas") =>
    points
      .map((point, index) => {
        const x = points.length < 2 ? 50 : (index / (points.length - 1)) * 100;
        const y = 28 - (point[field] / maximum) * 24;
        return `${x},${y}`;
      })
      .join(" ");

  return (
    <div className="md:col-span-2">
      <DetailCard title="指标趋势">
        {trend.error ? (
          <ErrorState error={trend.error} onRetry={() => void trend.refetch()} />
        ) : points.length === 0 ? (
          <DetailRow label="趋势" value="正在采集第一个样本" />
        ) : (
          <div className="grid gap-3">
            <div className="flex items-center gap-4 text-xs">
              <span className="text-primary">— 当前副本</span>
              <span className="text-warning-foreground">— 期望副本</span>
              <span className="text-muted-foreground ml-auto">
                最近 1 小时 · 15 秒刷新 · Server 运行时窗口
              </span>
            </div>
            <svg
              viewBox="0 0 100 32"
              className="bg-muted/30 h-28 w-full rounded"
              preserveAspectRatio="none"
              role="img"
              aria-label="HPA 当前和期望副本趋势"
            >
              <polyline
                points={polyline("current_replicas")}
                fill="none"
                stroke="currentColor"
                strokeWidth="1.2"
                className="text-primary"
                vectorEffect="non-scaling-stroke"
              />
              <polyline
                points={polyline("desired_replicas")}
                fill="none"
                stroke="currentColor"
                strokeWidth="1.2"
                className="text-warning-foreground"
                vectorEffect="non-scaling-stroke"
              />
            </svg>
            <div className="overflow-x-auto">
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-muted-foreground border-border border-b text-left">
                    <th className="py-1 pr-3">时间</th>
                    <th className="py-1 pr-3">副本</th>
                    <th className="py-1">当前指标</th>
                  </tr>
                </thead>
                <tbody>
                  {points
                    .slice(-8)
                    .reverse()
                    .map((point) => (
                      <tr key={point.timestamp} className="border-border/60 border-b">
                        <td className="py-1 pr-3">{formatAbsolute(point.timestamp)}</td>
                        <td className="zke-tnum py-1 pr-3">
                          {point.current_replicas} → {point.desired_replicas}
                        </td>
                        <td className="py-1">
                          {point.metrics
                            .map(
                              (metric) =>
                                `${metricLabel(metric)}=${targetLabel(metric.current) || "—"}`,
                            )
                            .join(" · ") || "指标不可用"}
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>
          </div>
        )}
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
