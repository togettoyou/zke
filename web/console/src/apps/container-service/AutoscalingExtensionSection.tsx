import { useCallback, useMemo, useState, type ReactNode } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { FileCode, Pencil, Plus, Stethoscope } from "lucide-react";
import { toast } from "sonner";

import {
  useDeleteKEDAScaledObject,
  useDeleteVerticalPodAutoscaler,
  useKEDAScaledObject,
  useKEDAScaledObjects,
  useVerticalPodAutoscaler,
  useVerticalPodAutoscalers,
} from "@/api/queries/autoscaling";
import { useResourceDescribe } from "@/api/queries/describe";
import type {
  KubernetesKEDADetail,
  KubernetesKEDASummary,
  KubernetesVPADetail,
  KubernetesVPASummary,
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
import { DetailDeleteAction, RowDeleteAction } from "@/components/common/delete-action";
import { RefreshAction } from "@/components/common/refresh-action";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { ErrorState, LoadingState } from "@/components/common/state";
import { RelativeTime } from "@/components/common/status";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/misc";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { AutoscalingExtensionForm } from "./AutoscalingExtensionForm";
import { useDiagnosticNavigation } from "./diagnostic-navigation-context";
import { DescribeView } from "./DescribeView";
import type { ClusterSectionProps } from "./types";
import { YamlEditorView } from "./YamlEditorView";

type Props = ClusterSectionProps & { namespace: string; tabs?: ReactNode };
type Kind = "vpa" | "keda";
type Summary = KubernetesVPASummary | KubernetesKEDASummary;
type Detail = KubernetesVPADetail | KubernetesKEDADetail;

export function VerticalPodAutoscalerSection(props: Props) {
  return <ExtensionSection kind="vpa" {...props} />;
}

export function KEDAScaledObjectSection(props: Props) {
  return <ExtensionSection kind="keda" {...props} />;
}

function ExtensionSection({
  kind,
  clusterId,
  clusterName,
  namespace,
  tenantId,
  projectId,
  tabs,
}: Props & { kind: Kind }) {
  const [detailName, setDetailName] = useState<string | null>(null);
  const [yamlName, setYamlName] = useState<string | null>(null);
  const [describeName, setDescribeName] = useState<string | null>(null);
  const [form, setForm] = useState<{ name: string | null; detail: Detail | null } | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Summary | null>(null);
  const [deletePreviewed, setDeletePreviewed] = useState(false);
  const { permissions } = useSessionContext();
  const scope = { type: "project" as const, tenantId, projectId };
  const canCreate = permissions.can("cluster.resource.create", scope);
  const canUpdate = permissions.can("cluster.resource.update", scope);
  const canDelete = permissions.can("cluster.resource.delete", scope);
  const canDescribe = permissions.can("cluster.event.read", scope);

  const vpaList = useVerticalPodAutoscalers(kind === "vpa" ? clusterId : null, namespace, {
    limit: 50,
  });
  const kedaList = useKEDAScaledObjects(kind === "keda" ? clusterId : null, namespace, {
    limit: 50,
  });
  const vpaDetail = useVerticalPodAutoscaler(
    kind === "vpa" ? clusterId : null,
    namespace,
    detailName,
  );
  const kedaDetail = useKEDAScaledObject(kind === "keda" ? clusterId : null, namespace, detailName);
  const removeVPA = useDeleteVerticalPodAutoscaler();
  const removeKEDA = useDeleteKEDAScaledObject();
  const list = kind === "vpa" ? vpaList : kedaList;
  const detail = kind === "vpa" ? vpaDetail : kedaDetail;
  const mutation = kind === "vpa" ? removeVPA : removeKEDA;
  const data = list.data;
  const available = data?.available;
  const items: Summary[] =
    kind === "vpa" ? (vpaList.data?.autoscalers ?? []) : (kedaList.data?.scaled_objects ?? []);
  const label = kind === "vpa" ? "VPA" : "KEDA ScaledObject";
  const previewKey = useSubmissionKey(deleteTarget !== null);
  const applyKey = useSubmissionKey(deleteTarget !== null);
  const openDelete = useCallback(
    (item: Summary) => {
      setDeleteTarget(item);
      setDeletePreviewed(false);
      mutation.reset();
    },
    [mutation],
  );

  const columns = useMemo<ColumnDef<Summary, unknown>[]>(
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
        size: 220,
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground break-all">{row.original.target.name}</span>
            <span className="text-subtle-foreground text-xs">{row.original.target.kind}</span>
          </div>
        ),
      },
      {
        header: kind === "vpa" ? "更新模式" : "副本范围",
        size: 130,
        cell: ({ row }) =>
          kind === "vpa" ? (
            (row.original as KubernetesVPASummary).update_mode || "Off"
          ) : (
            <span className="zke-tnum">
              {(row.original as KubernetesKEDASummary).min_replicas}–
              {(row.original as KubernetesKEDASummary).max_replicas}
            </span>
          ),
      },
      {
        header: "状态",
        size: 150,
        cell: ({ row }) => <ExtensionStatus kind={kind} item={row.original} />,
      },
      {
        header: "创建时间",
        size: 130,
        cell: ({ row }) => (
          <RelativeTime value={row.original.creation_timestamp} className="text-muted-foreground" />
        ),
      },
      {
        id: "actions",
        header: "",
        size: canDescribe ? 88 : 56,
        cell: ({ row }) => (
          <div className="flex justify-end gap-1" onClick={(event) => event.stopPropagation()}>
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
            {canDelete && row.original.uid ? (
              <RowDeleteAction name={row.original.name} onDelete={() => openDelete(row.original)} />
            ) : null}
          </div>
        ),
      },
    ],
    [canDelete, canDescribe, kind, openDelete],
  );

  const dialogs = (
    <SensitiveActionDialog
      open={deleteTarget !== null}
      onOpenChange={(open) => !open && setDeleteTarget(null)}
      title={`删除 ${label}`}
      description={
        deletePreviewed
          ? "DryRun 预检已通过。再次确认将提交实际删除。"
          : "首次点击只执行服务端 DryRun 预检；通过后才能实际删除。"
      }
      scopeLines={[
        { label: "集群", name: clusterName, id: clusterId },
        { label: "命名空间", name: namespace },
        { label, name: deleteTarget?.name ?? "", id: deleteTarget?.uid },
      ]}
      impacts={[
        "自动伸缩控制器将停止管理目标工作负载；工作负载本身不会被删除。",
        "请求携带当前 UID 与 resourceVersion，目标被重建或已变化时会拒绝删除。",
      ]}
      confirmationText={deletePreviewed ? deleteTarget?.name : undefined}
      confirmLabel={deletePreviewed ? "确认删除" : "执行 DryRun 预检"}
      destructive
      pending={mutation.isPending}
      error={mutation.error}
      onConfirm={() => {
        if (!deleteTarget) return;
        const dryRun = !deletePreviewed;
        void mutation
          .mutateAsync({
            clusterId,
            namespace,
            name: deleteTarget.name,
            uid: deleteTarget.uid,
            resourceVersion: deleteTarget.resource_version,
            dryRun,
            idempotencyKey: dryRun ? previewKey : applyKey,
          })
          .then(() => {
            if (dryRun) {
              setDeletePreviewed(true);
              toast.success(`${label} 删除 DryRun 预检已通过`);
            } else {
              if (detailName === deleteTarget.name) setDetailName(null);
              setDeleteTarget(null);
              toast.success(`${label} 已提交删除`);
            }
          })
          .catch(() => undefined);
      }}
    />
  );

  if (yamlName) {
    return (
      <YamlEditorView
        identity={{
          clusterId,
          group: kind === "vpa" ? "autoscaling.k8s.io" : "keda.sh",
          version: kind === "vpa" ? "v1" : "v1alpha1",
          resource: kind === "vpa" ? "verticalpodautoscalers" : "scaledobjects",
          namespace,
          name: yamlName,
        }}
        clusterName={clusterName}
        kindLabel={label}
        canUpdate={canUpdate}
        onBack={() => setYamlName(null)}
      />
    );
  }

  if (describeName) {
    return (
      <ExtensionDescribeView
        kind={kind}
        clusterId={clusterId}
        namespace={namespace}
        name={describeName}
        onBack={() => setDescribeName(null)}
      />
    );
  }

  if (form) {
    return (
      <AutoscalingExtensionForm
        kind={kind}
        clusterId={clusterId}
        clusterName={clusterName}
        namespace={namespace}
        existing={form.detail}
        onClose={() => setForm(null)}
      />
    );
  }

  if (detailName) {
    const item = detail.data as Detail | undefined;
    return (
      <>
        <div className="grid gap-3">
          <PageHeader
            title={detailName}
            onBack={() => setDetailName(null)}
            actions={
              <>
                {canDescribe ? (
                  <Button size="sm" variant="secondary" onClick={() => setDescribeName(detailName)}>
                    <Stethoscope />
                    诊断
                  </Button>
                ) : null}
                <Button size="sm" variant="secondary" onClick={() => setYamlName(detailName)}>
                  <FileCode />
                  YAML
                </Button>
                {canUpdate && item ? (
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => setForm({ name: detailName, detail: item })}
                  >
                    <Pencil />
                    编辑
                  </Button>
                ) : null}
                {canDelete && item?.uid ? (
                  <DetailDeleteAction name={detailName} onDelete={() => openDelete(item)} />
                ) : null}
              </>
            }
          />
          {detail.error ? (
            <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
          ) : detail.isLoading || !item ? (
            <LoadingState />
          ) : (
            <ExtensionDetail kind={kind} item={item} canDescribe={canDescribe} />
          )}
        </div>
        {dialogs}
      </>
    );
  }

  return (
    <>
      <div className="flex h-full min-h-0 flex-col">
        <SectionToolbarActions>
          <RefreshAction isFetching={list.isFetching} onRefresh={() => void list.refetch()} />
          {canCreate && available === true ? (
            <Button
              variant="primary"
              size="sm"
              onClick={() => setForm({ name: null, detail: null })}
            >
              <Plus />
              创建 {label}
            </Button>
          ) : null}
        </SectionToolbarActions>
        {tabs}
        {available === false ? (
          <Alert tone="info">
            <span className="font-medium">{label} 扩展未安装。</span>
            <span className="mt-1 block">
              当前集群未发现{" "}
              {kind === "vpa"
                ? "autoscaling.k8s.io/v1 VerticalPodAutoscaler"
                : "keda.sh/v1alpha1 ScaledObject"}{" "}
              CRD。安装对应控制器并更新 Agent 权限后即可使用，其他容器服务能力不受影响。
            </span>
          </Alert>
        ) : (
          <DataTable
            columns={columns}
            data={items}
            isLoading={list.isLoading}
            isFetching={list.isFetching}
            error={list.error}
            onRetry={() => void list.refetch()}
            onRowClick={(item) => setDetailName(item.name)}
            rowKey={(item) => item.uid || item.name}
            emptyTitle={`该命名空间没有 ${label}`}
            emptyDescription={`${namespace} 中没有可见的 ${label}。`}
          />
        )}
      </div>
      {dialogs}
    </>
  );
}

function ExtensionDescribeView({
  kind,
  clusterId,
  namespace,
  name,
  onBack,
}: {
  kind: Kind;
  clusterId: string;
  namespace: string;
  name: string;
  onBack: () => void;
}) {
  const describe = useResourceDescribe({
    clusterId,
    group: kind === "vpa" ? "autoscaling.k8s.io" : "keda.sh",
    version: kind === "vpa" ? "v1" : "v1alpha1",
    resource: kind === "vpa" ? "verticalpodautoscalers" : "scaledobjects",
    namespace,
    name,
  });
  return (
    <DescribeView
      name={name}
      kindLabel={kind === "vpa" ? "VerticalPodAutoscaler" : "KEDA ScaledObject"}
      data={describe.data}
      isLoading={describe.isLoading}
      isFetching={describe.isFetching}
      error={describe.error}
      onRetry={() => void describe.refetch()}
      onBack={onBack}
    />
  );
}

function ExtensionStatus({ kind, item }: { kind: Kind; item: Summary }) {
  if (kind === "vpa") {
    const vpa = item as KubernetesVPASummary;
    if (vpa.observed_generation < vpa.generation) {
      return (
        <Badge tone="info">
          <StatusDot tone="info" />
          尚未同步
        </Badge>
      );
    }
    const problem = (vpa.conditions ?? []).find(
      (condition) =>
        (condition.type === "RecommendationProvided" && condition.status !== "True") ||
        (["ConfigUnsupported", "NoPodsMatched", "LowConfidence"].includes(condition.type) &&
          condition.status === "True"),
    );
    if (problem) {
      return (
        <Badge tone="warning">
          <StatusDot tone="warning" />
          {problem.reason || problem.type}
        </Badge>
      );
    }
    return vpa.recommendation_count === 0 ? (
      <Badge tone="neutral">
        <StatusDot tone="neutral" />
        等待建议
      </Badge>
    ) : (
      <Badge tone="success">
        <StatusDot tone="success" />
        可用
      </Badge>
    );
  }
  const keda = item as KubernetesKEDASummary;
  if (keda.fallback)
    return (
      <Badge tone="warning">
        <StatusDot tone="warning" />
        回退中
      </Badge>
    );
  if (keda.paused)
    return (
      <Badge tone="info">
        <StatusDot tone="info" />
        已暂停
      </Badge>
    );
  if (!keda.ready)
    return (
      <Badge tone="warning">
        <StatusDot tone="warning" />
        未就绪
      </Badge>
    );
  return (
    <Badge tone={keda.active ? "success" : "neutral"}>
      <StatusDot tone={keda.active ? "success" : "neutral"} />
      {keda.active ? "活跃" : "待触发"}
    </Badge>
  );
}

function ExtensionDetail({
  kind,
  item,
  canDescribe,
}: {
  kind: Kind;
  item: Detail;
  canDescribe: boolean;
}) {
  const diagnosticNavigation = useDiagnosticNavigation();
  if (kind === "vpa") {
    const vpa = item as KubernetesVPADetail;
    return (
      <div className="grid gap-3 md:grid-cols-2">
        <DetailCard title="概览">
          <DetailRow label="名称" value={vpa.name} />
          <DetailRow label="命名空间" value={vpa.namespace} />
          <DetailRow
            label="目标"
            value={
              <span className="break-all">
                {vpa.target.kind}/{vpa.target.name}
                <span className="zke-mono text-subtle-foreground ml-2 text-xs">
                  {vpa.target.api_version}
                </span>
              </span>
            }
          />
          <DetailRow label="状态" value={<ExtensionStatus kind="vpa" item={vpa} />} />
          <DetailRow label="更新模式" value={vpa.update_mode || "Off"} />
          <DetailRow
            label="Generation"
            value={
              <span className="zke-tnum">
                {vpa.generation}（已观察 {vpa.observed_generation || "—"}）
              </span>
            }
          />
          <DetailRow label="创建时间" value={formatAbsolute(vpa.creation_timestamp)} />
        </DetailCard>
        <DetailCard title="建议资源">
          {(vpa.recommendations ?? []).length === 0 ? (
            <DetailRow label="建议" value="控制器尚未生成建议" />
          ) : (
            (vpa.recommendations ?? []).map((entry) => (
              <DetailRow
                key={entry.container_name}
                label={entry.container_name}
                value={
                  <div className="grid gap-0.5 text-xs">
                    <span>目标：{quantityMap(entry.target)}</span>
                    <span className="text-muted-foreground">
                      下界：{quantityMap(entry.lower_bound)}
                    </span>
                    <span className="text-muted-foreground">
                      上界：{quantityMap(entry.upper_bound)}
                    </span>
                    <span className="text-subtle-foreground">
                      未限幅：{quantityMap(entry.uncapped_target)}
                    </span>
                  </div>
                }
              />
            ))
          )}
        </DetailCard>
        <DetailCard title="容器策略">
          {(vpa.container_policies ?? []).length === 0 ? (
            <DetailRow label="策略" value="使用控制器默认值" />
          ) : (
            (vpa.container_policies ?? []).map((policy) => (
              <DetailRow
                key={policy.container_name}
                label={policy.container_name}
                value={
                  <div className="grid gap-0.5 text-xs">
                    <span>
                      模式 {policy.mode || "默认"} · 调整范围 {policy.controlled_values || "默认"}
                    </span>
                    <span className="text-muted-foreground">
                      受控资源 {(policy.controlled_resources ?? []).join("、") || "控制器默认"}
                    </span>
                    <span className="text-muted-foreground">
                      最小 {quantityMap(policy.min_allowed)} · 最大{" "}
                      {quantityMap(policy.max_allowed)}
                    </span>
                  </div>
                }
              />
            ))
          )}
        </DetailCard>
        <DetailCard title="条件">
          <DetailConditions conditions={vpa.conditions ?? []} />
        </DetailCard>
        <DetailCard title="标签">
          <DetailKeyValues entries={vpa.labels ?? {}} />
        </DetailCard>
        <DetailCard title="注解">
          <DetailKeyValues entries={vpa.annotations ?? {}} />
        </DetailCard>
      </div>
    );
  }
  const keda = item as KubernetesKEDADetail;
  return (
    <div className="grid gap-3 md:grid-cols-2">
      <DetailCard title="概览">
        <DetailRow label="名称" value={keda.name} />
        <DetailRow label="命名空间" value={keda.namespace} />
        <DetailRow
          label="目标"
          value={
            <span className="break-all">
              {keda.target.kind}/{keda.target.name}
              <span className="zke-mono text-subtle-foreground ml-2 text-xs">
                {keda.target.api_version}
              </span>
            </span>
          }
        />
        <DetailRow label="状态" value={<ExtensionStatus kind="keda" item={keda} />} />
        <DetailRow
          label="副本范围"
          value={
            <span className="zke-tnum">
              {keda.min_replicas}–{keda.max_replicas}
            </span>
          }
        />
        <DetailRow
          label="轮询 / 冷却"
          value={
            <span className="zke-tnum">
              {keda.polling_interval}s / {keda.cooldown_period}s
            </span>
          }
        />
        <DetailRow
          label="生成的 HPA"
          value={
            keda.hpa_name ? (
              <div className="flex flex-wrap items-center gap-2">
                <span className="break-all">{keda.hpa_name}</span>
                {diagnosticNavigation && canDescribe ? (
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() =>
                      diagnosticNavigation.open({
                        view: "describe",
                        type: "autoscaler",
                        namespace: keda.namespace,
                        name: keda.hpa_name,
                      })
                    }
                  >
                    <Stethoscope />
                    诊断 HPA
                  </Button>
                ) : null}
              </div>
            ) : (
              "尚未生成"
            )
          }
        />
        <DetailRow label="Generation" value={<span className="zke-tnum">{keda.generation}</span>} />
        <DetailRow label="创建时间" value={formatAbsolute(keda.creation_timestamp)} />
      </DetailCard>
      <DetailCard title="控制器状态">
        <DetailRow
          label="Ready"
          value={<BooleanState value={keda.ready} trueLabel="已就绪" falseLabel="未就绪" />}
        />
        <DetailRow
          label="Active"
          value={
            <BooleanState
              value={keda.active}
              trueLabel="正在伸缩"
              falseLabel="等待触发"
              neutralFalse
            />
          }
        />
        <DetailRow
          label="Fallback"
          value={
            <BooleanState
              value={keda.fallback}
              trueLabel="回退中"
              falseLabel="未回退"
              warningTrue
            />
          }
        />
        <DetailRow
          label="Paused"
          value={
            <BooleanState value={keda.paused} trueLabel="已暂停" falseLabel="未暂停" neutralFalse />
          }
        />
      </DetailCard>
      <DetailCard title="触发器">
        {(keda.triggers ?? []).length === 0 ? (
          <DetailRow label="触发器" value="—" />
        ) : (
          (keda.triggers ?? []).map((trigger, index) => (
            <DetailRow
              key={`${trigger.type}/${trigger.name}/${index}`}
              label={trigger.name || trigger.type}
              value={
                <div className="grid gap-1">
                  <span>
                    {trigger.type} · 缓存指标 {trigger.use_cached_metrics ? "是" : "否"}
                  </span>
                  <span className="text-muted-foreground text-xs">
                    TriggerAuthentication：{trigger.authentication_ref_name || "无"}
                  </span>
                  <DetailKeyValues entries={trigger.metadata ?? {}} />
                  {(trigger.redacted_metadata_keys ?? []).length > 0 ? (
                    <span className="text-warning-foreground text-xs">
                      已脱敏：{(trigger.redacted_metadata_keys ?? []).join("、")}
                    </span>
                  ) : null}
                </div>
              }
            />
          ))
        )}
      </DetailCard>
      <DetailCard title="外部指标">
        <DetailRow
          label="指标"
          value={(keda.external_metric_names ?? []).join("、") || "尚未生成"}
        />
      </DetailCard>
      <DetailCard title="条件">
        <DetailConditions conditions={keda.conditions ?? []} />
      </DetailCard>
      <DetailCard title="标签">
        <DetailKeyValues entries={keda.labels ?? {}} />
      </DetailCard>
      <DetailCard title="注解">
        <DetailKeyValues entries={keda.annotations ?? {}} />
      </DetailCard>
    </div>
  );
}

function BooleanState({
  value,
  trueLabel,
  falseLabel,
  warningTrue = false,
  neutralFalse = false,
}: {
  value: boolean;
  trueLabel: string;
  falseLabel: string;
  warningTrue?: boolean;
  neutralFalse?: boolean;
}) {
  const tone = value ? (warningTrue ? "warning" : "success") : neutralFalse ? "neutral" : "warning";
  return (
    <Badge tone={tone}>
      <StatusDot tone={tone} />
      {value ? trueLabel : falseLabel}
    </Badge>
  );
}

function quantityMap(values?: Record<string, string> | null): string {
  const items = Object.entries(values ?? {}).map(([key, value]) => `${key}=${value}`);
  return items.length > 0 ? items.join(", ") : "—";
}
