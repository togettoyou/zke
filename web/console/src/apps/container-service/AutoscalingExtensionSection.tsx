import { useMemo, useState, type ReactNode } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { FileCode, Pencil, Plus } from "lucide-react";
import { toast } from "sonner";

import {
  useCreateKEDAScaledObject,
  useCreateVerticalPodAutoscaler,
  useDeleteKEDAScaledObject,
  useDeleteVerticalPodAutoscaler,
  useKEDAScaledObject,
  useKEDAScaledObjects,
  useUpdateKEDAScaledObject,
  useUpdateVerticalPodAutoscaler,
  useVerticalPodAutoscaler,
  useVerticalPodAutoscalers,
} from "@/api/queries/autoscaling";
import type {
  KubernetesKEDADetail,
  KubernetesKEDASpecInput,
  KubernetesKEDASummary,
  KubernetesVPADetail,
  KubernetesVPASpecInput,
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
import { RowDeleteAction } from "@/components/common/delete-action";
import { RefreshAction } from "@/components/common/refresh-action";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { ErrorState, LoadingState } from "@/components/common/state";
import { RelativeTime } from "@/components/common/status";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, NumericInput, Textarea } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

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
  const [form, setForm] = useState<{ name: string | null; detail: Detail | null } | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Summary | null>(null);
  const [deletePreviewed, setDeletePreviewed] = useState(false);
  const { permissions } = useSessionContext();
  const scope = { type: "project" as const, tenantId, projectId };
  const canCreate = permissions.can("cluster.resource.create", scope);
  const canUpdate = permissions.can("cluster.resource.update", scope);
  const canDelete = permissions.can("cluster.resource.delete", scope);

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
        size: 56,
        cell: ({ row }) => (
          <div className="flex justify-end gap-1" onClick={(event) => event.stopPropagation()}>
            {canDelete && row.original.uid ? (
              <RowDeleteAction
                name={row.original.name}
                onDelete={() => {
                  setDeleteTarget(row.original);
                  setDeletePreviewed(false);
                  mutation.reset();
                }}
              />
            ) : null}
          </div>
        ),
      },
    ],
    [canDelete, kind, mutation],
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

  if (form) {
    return (
      <ExtensionForm
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
      <div className="grid gap-3">
        <PageHeader
          title={detailName}
          onBack={() => setDetailName(null)}
          actions={
            <>
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
            </>
          }
        />
        {detail.error ? (
          <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
        ) : detail.isLoading || !item ? (
          <LoadingState />
        ) : (
          <ExtensionDetail kind={kind} item={item} />
        )}
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <SectionToolbarActions>
        <RefreshAction isFetching={list.isFetching} onRefresh={() => void list.refetch()} />
        {canCreate && available === true ? (
          <Button variant="primary" size="sm" onClick={() => setForm({ name: null, detail: null })}>
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
                setDeleteTarget(null);
                toast.success(`${label} 已提交删除`);
              }
            })
            .catch(() => undefined);
        }}
      />
    </div>
  );
}

function ExtensionStatus({ kind, item }: { kind: Kind; item: Summary }) {
  if (kind === "vpa") {
    const vpa = item as KubernetesVPASummary;
    const problem = vpa.conditions.find((condition) => condition.status === "False");
    return problem ? (
      <Badge tone="warning">
        <StatusDot tone="warning" />
        {problem.reason || problem.type}
      </Badge>
    ) : (
      <Badge tone="success">
        <StatusDot tone="success" />
        可用
      </Badge>
    );
  }
  const keda = item as KubernetesKEDASummary;
  if (!keda.ready)
    return (
      <Badge tone="warning">
        <StatusDot tone="warning" />
        未就绪
      </Badge>
    );
  if (keda.paused)
    return (
      <Badge tone="info">
        <StatusDot tone="info" />
        已暂停
      </Badge>
    );
  return (
    <Badge tone={keda.active ? "success" : "neutral"}>
      <StatusDot tone={keda.active ? "success" : "neutral"} />
      {keda.active ? "活跃" : "待触发"}
    </Badge>
  );
}

function ExtensionDetail({ kind, item }: { kind: Kind; item: Detail }) {
  if (kind === "vpa") {
    const vpa = item as KubernetesVPADetail;
    return (
      <div className="grid gap-3 md:grid-cols-2">
        <DetailCard title="概览">
          <DetailRow label="目标" value={`${vpa.target.kind}/${vpa.target.name}`} />
          <DetailRow label="更新模式" value={vpa.update_mode || "Off"} />
          <DetailRow
            label="Generation"
            value={`${vpa.generation}（已观察 ${vpa.observed_generation}）`}
          />
          <DetailRow label="创建时间" value={formatAbsolute(vpa.creation_timestamp)} />
        </DetailCard>
        <DetailCard title="建议资源">
          {vpa.recommendations.length === 0 ? (
            <DetailRow label="建议" value="控制器尚未生成建议" />
          ) : (
            vpa.recommendations.map((entry) => (
              <DetailRow
                key={entry.container_name}
                label={entry.container_name}
                value={`目标 ${quantityMap(entry.target)} · 下界 ${quantityMap(entry.lower_bound)} · 上界 ${quantityMap(entry.upper_bound)}`}
              />
            ))
          )}
        </DetailCard>
        <DetailCard title="容器策略">
          {vpa.container_policies.length === 0 ? (
            <DetailRow label="策略" value="使用控制器默认值" />
          ) : (
            vpa.container_policies.map((policy) => (
              <DetailRow
                key={policy.container_name}
                label={policy.container_name}
                value={`${policy.controlled_values || "默认"} · 最小 ${quantityMap(policy.min_allowed)} · 最大 ${quantityMap(policy.max_allowed)}`}
              />
            ))
          )}
        </DetailCard>
        <DetailCard title="条件">
          <DetailConditions conditions={vpa.conditions} />
        </DetailCard>
        <DetailCard title="标签">
          <DetailKeyValues entries={vpa.labels} />
        </DetailCard>
        <DetailCard title="注解">
          <DetailKeyValues entries={vpa.annotations} />
        </DetailCard>
      </div>
    );
  }
  const keda = item as KubernetesKEDADetail;
  return (
    <div className="grid gap-3 md:grid-cols-2">
      <DetailCard title="概览">
        <DetailRow label="目标" value={`${keda.target.kind}/${keda.target.name}`} />
        <DetailRow label="副本范围" value={`${keda.min_replicas}–${keda.max_replicas}`} />
        <DetailRow
          label="轮询 / 冷却"
          value={`${keda.polling_interval}s / ${keda.cooldown_period}s`}
        />
        <DetailRow label="生成的 HPA" value={keda.hpa_name || "尚未生成"} />
        <DetailRow label="创建时间" value={formatAbsolute(keda.creation_timestamp)} />
      </DetailCard>
      <DetailCard title="触发器">
        {keda.triggers.map((trigger, index) => (
          <DetailRow
            key={`${trigger.type}/${trigger.name}/${index}`}
            label={trigger.name || trigger.type}
            value={
              <div className="grid gap-1">
                <span>
                  {trigger.type} · 认证引用 {trigger.authentication_ref_name || "无"}
                </span>
                <DetailKeyValues entries={trigger.metadata} />
                {trigger.redacted_metadata_keys.length > 0 ? (
                  <span className="text-warning-foreground text-xs">
                    已脱敏：{trigger.redacted_metadata_keys.join("、")}
                  </span>
                ) : null}
              </div>
            }
          />
        ))}
      </DetailCard>
      <DetailCard title="外部指标">
        <DetailRow label="指标" value={keda.external_metric_names.join("、") || "尚未生成"} />
      </DetailCard>
      <DetailCard title="条件">
        <DetailConditions conditions={keda.conditions} />
      </DetailCard>
      <DetailCard title="标签">
        <DetailKeyValues entries={keda.labels} />
      </DetailCard>
      <DetailCard title="注解">
        <DetailKeyValues entries={keda.annotations} />
      </DetailCard>
    </div>
  );
}

function quantityMap(values: Record<string, string>): string {
  const items = Object.entries(values).map(([key, value]) => `${key}=${value}`);
  return items.length > 0 ? items.join(", ") : "—";
}

function ExtensionForm({
  kind,
  clusterId,
  clusterName,
  namespace,
  existing,
  onClose,
}: {
  kind: Kind;
  clusterId: string;
  clusterName: string;
  namespace: string;
  existing: Detail | null;
  onClose: () => void;
}) {
  const vpa = kind === "vpa" ? (existing as KubernetesVPADetail | null) : null;
  const keda = kind === "keda" ? (existing as KubernetesKEDADetail | null) : null;
  const [name, setName] = useState(existing?.name ?? "");
  const [targetKind, setTargetKind] = useState<string>(existing?.target.kind ?? "Deployment");
  const [targetName, setTargetName] = useState(existing?.target.name ?? "");
  const [updateMode, setUpdateMode] = useState(vpa?.update_mode || "Off");
  const [minimum, setMinimum] = useState(String(keda?.min_replicas ?? 0));
  const [maximum, setMaximum] = useState(String(keda?.max_replicas ?? 10));
  const [polling, setPolling] = useState(String(keda?.polling_interval ?? 30));
  const [cooldown, setCooldown] = useState(String(keda?.cooldown_period ?? 300));
  const [advanced, setAdvanced] = useState(
    JSON.stringify(
      kind === "vpa"
        ? (vpa?.container_policies ?? [])
        : (keda?.triggers ?? [
            {
              type: "prometheus",
              name: "",
              use_cached_metrics: false,
              metadata: {
                serverAddress: "http://prometheus:9090",
                query: "sum(queue_depth)",
                threshold: "10",
              },
              redacted_metadata_keys: [],
              authentication_ref_name: "",
            },
          ]),
      null,
      2,
    ),
  );
  const [previewed, setPreviewed] = useState(false);
  const createVPA = useCreateVerticalPodAutoscaler();
  const updateVPA = useUpdateVerticalPodAutoscaler();
  const createKEDA = useCreateKEDAScaledObject();
  const updateKEDA = useUpdateKEDAScaledObject();
  const mutation =
    kind === "vpa" ? (existing ? updateVPA : createVPA) : existing ? updateKEDA : createKEDA;
  const previewKey = useSubmissionKey(true);
  const applyKey = useSubmissionKey(true);
  const label = kind === "vpa" ? "VPA" : "KEDA ScaledObject";

  const submit = (dryRun: boolean) => {
    let parsed: unknown;
    try {
      parsed = JSON.parse(advanced);
      if (!Array.isArray(parsed)) throw new Error();
    } catch {
      toast.error(kind === "vpa" ? "容器策略必须是 JSON 数组" : "触发器必须是 JSON 数组");
      return;
    }
    if (!name.trim() || !targetName.trim()) {
      toast.error("请填写名称和目标工作负载");
      return;
    }
    const common = {
      clusterId,
      namespace,
      name: name.trim(),
      dryRun,
      idempotencyKey: dryRun ? previewKey : applyKey,
    };
    let promise: Promise<unknown>;
    if (kind === "vpa") {
      const spec = {
        target: {
          api_version: "apps/v1" as const,
          kind: targetKind as "Deployment" | "StatefulSet" | "DaemonSet",
          name: targetName.trim(),
        },
        update_mode: updateMode as KubernetesVPASpecInput["update_mode"],
        container_policies: parsed as KubernetesVPASpecInput["container_policies"],
      };
      promise = existing
        ? updateVPA.mutateAsync({
            ...common,
            uid: existing.uid,
            resourceVersion: existing.resource_version,
            spec,
          })
        : createVPA.mutateAsync({ ...common, spec });
    } else {
      const numbers = [minimum, maximum, polling, cooldown].map(Number);
      if (numbers.some((value) => !Number.isInteger(value))) {
        toast.error("副本数与时间必须是整数");
        return;
      }
      if (
        numbers[0]! < 0 ||
        numbers[1]! < 1 ||
        numbers[1]! > 1_000_000 ||
        numbers[0]! > numbers[1]! ||
        numbers[2]! < 1 ||
        numbers[2]! > 3_600 ||
        numbers[3]! < 0 ||
        numbers[3]! > 86_400
      ) {
        toast.error("副本范围、轮询间隔（1–3600 秒）或冷却时间（0–86400 秒）无效");
        return;
      }
      const spec = {
        target: {
          api_version: "apps/v1" as const,
          kind: targetKind as "Deployment" | "StatefulSet",
          name: targetName.trim(),
        },
        min_replicas: numbers[0]!,
        max_replicas: numbers[1]!,
        polling_interval: numbers[2]!,
        cooldown_period: numbers[3]!,
        triggers: parsed as KubernetesKEDASpecInput["triggers"],
      };
      promise = existing
        ? updateKEDA.mutateAsync({
            ...common,
            uid: existing.uid,
            resourceVersion: existing.resource_version,
            spec,
          })
        : createKEDA.mutateAsync({ ...common, spec });
    }
    void promise
      .then(() => {
        if (dryRun) {
          setPreviewed(true);
          toast.success(`${label} DryRun 预检已通过`);
        } else {
          toast.success(`${label} 已保存`);
          onClose();
        }
      })
      .catch(() => undefined);
  };

  return (
    <div className="grid gap-4">
      <PageHeader
        title={`${existing ? "编辑" : "创建"} ${label} · ${clusterName} · ${namespace}`}
        onBack={onClose}
      />
      <div className="grid gap-4 px-4 pb-6 md:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor="extension-name">名称</Label>
          <Input
            id="extension-name"
            value={name}
            disabled={Boolean(existing) || mutation.isPending}
            onChange={(event) => {
              setName(event.target.value);
              setPreviewed(false);
            }}
          />
        </div>
        <div className="grid gap-2">
          <Label>目标类型</Label>
          <Select
            value={targetKind}
            onValueChange={(value) => {
              setTargetKind(value);
              setPreviewed(false);
            }}
          >
            <SelectTrigger disabled={mutation.isPending}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="Deployment">Deployment</SelectItem>
              <SelectItem value="StatefulSet">StatefulSet</SelectItem>
              {kind === "vpa" ? <SelectItem value="DaemonSet">DaemonSet</SelectItem> : null}
            </SelectContent>
          </Select>
        </div>
        <div className="grid gap-2 md:col-span-2">
          <Label htmlFor="target-name">目标名称</Label>
          <Input
            id="target-name"
            value={targetName}
            disabled={mutation.isPending}
            onChange={(event) => {
              setTargetName(event.target.value);
              setPreviewed(false);
            }}
          />
        </div>
        {kind === "vpa" ? (
          <div className="grid gap-2">
            <Label>更新模式</Label>
            <Select
              value={updateMode}
              onValueChange={(value) => {
                setUpdateMode(value);
                setPreviewed(false);
              }}
            >
              <SelectTrigger disabled={mutation.isPending}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {["Off", "Initial", "Recreate", "InPlaceOrRecreate", "InPlace"].map((mode) => (
                  <SelectItem key={mode} value={mode}>
                    {mode}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        ) : (
          <>
            <NumberField
              label="最小副本"
              value={minimum}
              disabled={mutation.isPending}
              setValue={(value) => {
                setMinimum(value);
                setPreviewed(false);
              }}
            />
            <NumberField
              label="最大副本"
              value={maximum}
              disabled={mutation.isPending}
              setValue={(value) => {
                setMaximum(value);
                setPreviewed(false);
              }}
            />
            <NumberField
              label="轮询间隔（秒）"
              value={polling}
              disabled={mutation.isPending}
              setValue={(value) => {
                setPolling(value);
                setPreviewed(false);
              }}
            />
            <NumberField
              label="冷却时间（秒）"
              value={cooldown}
              disabled={mutation.isPending}
              setValue={(value) => {
                setCooldown(value);
                setPreviewed(false);
              }}
            />
          </>
        )}
        <div className="grid gap-2 md:col-span-2">
          <Label htmlFor="extension-advanced">
            {kind === "vpa" ? "容器策略（JSON 数组）" : "触发器（JSON 数组）"}
          </Label>
          <Textarea
            id="extension-advanced"
            className="zke-mono min-h-72"
            value={advanced}
            disabled={mutation.isPending}
            onChange={(event) => {
              setAdvanced(event.target.value);
              setPreviewed(false);
            }}
          />
          {kind === "keda" ? (
            <Alert tone="warning">
              不要在 metadata 中填写密码、Token、Secret 或连接串；请设置 authentication_ref_name
              引用同命名空间的 TriggerAuthentication。
            </Alert>
          ) : null}
        </div>
        <div className="flex justify-end gap-2 md:col-span-2">
          <Button variant="secondary" onClick={onClose}>
            取消
          </Button>
          <Button
            variant={previewed ? "danger" : "primary"}
            disabled={mutation.isPending}
            onClick={() => submit(!previewed)}
          >
            {previewed ? "确认应用" : "执行 DryRun 预检"}
          </Button>
        </div>
      </div>
    </div>
  );
}

function NumberField({
  label,
  value,
  disabled,
  setValue,
}: {
  label: string;
  value: string;
  disabled: boolean;
  setValue: (value: string) => void;
}) {
  return (
    <div className="grid gap-2">
      <Label>{label}</Label>
      <NumericInput value={value} disabled={disabled} onValueChange={setValue} />
    </div>
  );
}
