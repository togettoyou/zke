import { useCallback, useEffect, useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { FileCode, Pencil, Plus, Stethoscope } from "lucide-react";
import { toast } from "sonner";

import {
  isNamespacedPolicy,
  useDeletePolicyResource,
  usePolicyResource,
  usePolicyResources,
} from "@/api/queries/policies";
import { usePolicyDescribe, type PolicyDescribeResource } from "@/api/queries/describe";
import type {
  KubernetesNetworkPolicyRule,
  KubernetesPolicyResource,
  KubernetesPolicyResourceDetail,
  KubernetesPolicyResourceSummary,
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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/misc";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { POLICY_TYPES, policyIdentity, policyKindLabel } from "./policy-catalog";
import { PolicyForm } from "./PolicyForm";
import { DescribeView } from "./DescribeView";
import { useContinuePagination } from "./use-continue-pagination";
import type { ClusterSectionProps } from "./types";
import { namespaceMutationPermission } from "./namespace-permissions";
import { YamlEditorView } from "./YamlEditorView";

const PAGE_SIZE = 50;

type PolicySectionProps = ClusterSectionProps & {
  /** Every tab but PriorityClass is scoped by it. */
  namespace: string;
  /**
   * Told to the shell whenever the active tab changes, so the toolbar's
   * Namespace picker appears exactly while it scopes something.
   */
  onNamespaceScopeChange: (namespaced: boolean) => void;
};

/**
 * ResourceQuota, LimitRange, NetworkPolicy, PodDisruptionBudget and
 * PriorityClass.
 *
 * They are grouped because they answer one question — what is this cluster
 * allowed to let workloads do — and they do not share a scope: the first four
 * constrain one Namespace, PriorityClass ranks Pods across the whole Cluster,
 * which the Server enforces as two separate route families. The section follows
 * that rather than papering over it: the Namespace picker appears only on the
 * tabs it applies to.
 */
export function PolicySection({
  clusterId,
  clusterName,
  agentNamespace,
  namespace,
  tenantId,
  projectId,
  onNamespaceScopeChange,
}: PolicySectionProps) {
  const { permissions } = useSessionContext();
  const [resource, setResource] = useState<KubernetesPolicyResource>("resourcequotas");
  const namespaced = isNamespacedPolicy(resource);
  const pager = useContinuePagination(`${clusterId}/${namespaced ? namespace : ""}/${resource}`);
  const list = usePolicyResources(clusterId, namespace, resource, {
    limit: PAGE_SIZE,
    ...(pager.token ? { continue: pager.token } : {}),
  });
  const [detailName, setDetailName] = useState<string | null>(null);
  const [yamlName, setYamlName] = useState<string | null>(null);
  const [describeName, setDescribeName] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<KubernetesPolicyResourceSummary | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<KubernetesPolicyResourceSummary | null>(null);
  const [deletePreviewed, setDeletePreviewed] = useState(false);
  const deletePreviewKey = useSubmissionKey(deleteTarget !== null);
  const deleteApplyKey = useSubmissionKey(deleteTarget !== null);
  const remove = useDeletePolicyResource();

  useEffect(() => onNamespaceScopeChange(namespaced), [namespaced, onNamespaceScopeChange]);

  const projectScope = { type: "project" as const, tenantId, projectId };
  const mutationNamespace = namespaced ? namespace : "";
  const canCreate = permissions.can(
    namespaceMutationPermission(
      { namespace: mutationNamespace, agentNamespace },
      "cluster.resource.create",
    ),
    projectScope,
  );
  const canUpdate = permissions.can(
    namespaceMutationPermission(
      { namespace: mutationNamespace, agentNamespace },
      "cluster.resource.update",
    ),
    projectScope,
  );
  const canDelete = permissions.can(
    namespaceMutationPermission(
      { namespace: mutationNamespace, agentNamespace },
      "cluster.resource.delete",
    ),
    projectScope,
  );
  const canDescribe =
    supportsPolicyDescribe(resource) && permissions.can("cluster.event.read", projectScope);

  // Both the row action and the detail view open the same confirmation, so it is
  // one callback rather than two copies of the reset sequence.
  const openDelete = useCallback(
    (item: KubernetesPolicyResourceSummary) => {
      setDeleteTarget(item);
      setDeletePreviewed(false);
      remove.reset();
    },
    [remove],
  );

  const columns = useMemo<ColumnDef<KubernetesPolicyResourceSummary, unknown>[]>(
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
      ...typeColumns(resource),
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
    [resource, canUpdate, canDelete, canDescribe, openDelete],
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
        title={`删除 ${policyKindLabel(resource)}`}
        description={
          deletePreviewed
            ? "DryRun 预检已通过。再次确认将提交实际删除。"
            : "首次点击只执行服务端 DryRun 预检；通过后才能实际删除。"
        }
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          ...(namespaced ? [{ label: "命名空间", name: namespace }] : []),
          {
            label: policyKindLabel(resource),
            name: deleteTarget?.name ?? "",
            id: deleteTarget?.uid,
          },
        ]}
        impacts={deleteImpacts(resource)}
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
              resource,
              name: deleteTarget.name,
              uid: deleteTarget.uid,
              resourceVersion: deleteTarget.resource_version,
              dryRun,
              idempotencyKey: dryRun ? deletePreviewKey : deleteApplyKey,
            })
            .then(() => {
              if (dryRun) {
                setDeletePreviewed(true);
                toast.success("删除 DryRun 预检已通过");
                return;
              }
              toast.success(`${policyKindLabel(resource)} ${deleteTarget.name} 已提交删除`);
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
          ...policyIdentity(resource),
          ...(namespaced ? { namespace } : {}),
          name: yamlName,
        }}
        clusterName={clusterName}
        kindLabel={policyKindLabel(resource)}
        canUpdate={canUpdate}
        onBack={() => setYamlName(null)}
      />
    );
  }

  if (describeName && supportsPolicyDescribe(resource)) {
    return (
      <PolicyDescribeView
        clusterId={clusterId}
        namespace={namespace}
        resource={resource}
        name={describeName}
        onBack={() => setDescribeName(null)}
      />
    );
  }

  // The form takes over the section rather than sitting over the list, like every
  // other typed form here: a quota's every line, or a network policy's rules, is
  // taller than a box laid over the table can show, and the table is of no use
  // while they are being filled in.
  if (creating || editing) {
    return (
      <PolicyForm
        clusterId={clusterId}
        clusterName={clusterName}
        namespace={namespace}
        resource={resource}
        target={editing}
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
        <PolicyDetailView
          clusterId={clusterId}
          namespace={namespace}
          resource={resource}
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
  const waitingForNamespace = namespaced && namespace === "";

  return (
    <div className="flex h-full min-h-0 flex-col">
      <SectionToolbarActions>
        <RefreshAction isFetching={list.isFetching} onRefresh={() => void list.refetch()} />
        {canCreate && !waitingForNamespace ? (
          <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
            <Plus />
            创建 {policyKindLabel(resource)}
          </Button>
        ) : null}
      </SectionToolbarActions>
      <Tabs
        value={resource}
        onValueChange={(value) => {
          setResource(value as KubernetesPolicyResource);
          setDetailName(null);
          setDescribeName(null);
        }}
        className="flex min-h-0 flex-1 flex-col"
      >
        <TabsList className="w-fit">
          {POLICY_TYPES.map((type) => (
            <TabsTrigger key={type.resource} value={type.resource}>
              {type.label}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value={resource} className="flex min-h-0 flex-1 flex-col">
          {waitingForNamespace ? (
            <Alert tone="info">
              {policyKindLabel(resource)} 按命名空间定域，正在等待工具栏的命名空间选择器解析出一个
              可用的命名空间。若该集群没有当前身份可见的命名空间，这里会一直为空。
            </Alert>
          ) : (
            <DataTable
              columns={columns}
              data={list.data?.resources}
              isLoading={list.isLoading}
              isFetching={list.isFetching}
              error={list.error}
              onRetry={() => void list.refetch()}
              onRowClick={(item) => setDetailName(item.name)}
              rowKey={(item) => item.uid || item.name}
              emptyTitle={`该集群没有 ${policyKindLabel(resource)}`}
              emptyDescription={
                namespaced
                  ? `${namespace} 中没有可见的 ${policyKindLabel(resource)}。`
                  : `当前筛选范围内没有可见的 ${policyKindLabel(resource)}。`
              }
              continuePagination={{
                pageIndex: pager.pageIndex,
                nextToken,
                onPrevious: pager.goPrevious,
                onNext: pager.goNext,
              }}
            />
          )}
        </TabsContent>
      </Tabs>

      {dialogs}
    </div>
  );
}

/**
 * Deleting a policy is deleting a constraint, so each message says what stops
 * being enforced rather than what disappears.
 */
function deleteImpacts(resource: KubernetesPolicyResource): string[] {
  const precondition =
    "请求携带该对象当前的 UID 与 resourceVersion 前置条件，期间对象若已变化或被重建，删除会被拒绝。";
  if (resource === "resourcequotas") {
    return [
      "删除后该命名空间不再有这份额度限制，新建对象将不受它约束。",
      "已经创建的对象不受影响，但集群总量的保护随之消失。",
      precondition,
    ];
  }
  if (resource === "limitranges") {
    return [
      "删除后新建容器不再获得默认的 requests/limits，也不再校验上下限。",
      "已经运行的 Pod 保持原样。",
      precondition,
    ];
  }
  if (resource === "networkpolicies") {
    return [
      "删除后，若没有其他策略再选中这些 Pod，它们将回到默认放行的状态。",
      "这可能立即打开此前被拒绝的东西向流量。",
      precondition,
    ];
  }
  if (resource === "poddisruptionbudgets") {
    return ["删除后节点排空和驱逐 API 不再受该预算限制，可能一次性驱逐更多副本。", precondition];
  }
  return [
    "删除 PriorityClass 不会影响已经调度的 Pod，但引用它的新 Pod 会被拒绝。",
    "若它是集群默认优先级，删除后新 Pod 将不再获得默认优先级。",
    precondition,
  ];
}

/** The columns that only make sense for one type. */
function typeColumns(
  resource: KubernetesPolicyResource,
): ColumnDef<KubernetesPolicyResourceSummary, unknown>[] {
  if (resource === "resourcequotas") {
    return [
      {
        header: "额度与用量",
        cell: ({ row }) => {
          const quota = row.original.resource_quota;
          const keys = Object.keys(quota?.hard ?? {}).sort();
          if (keys.length === 0) {
            return <span className="text-muted-foreground text-xs">—</span>;
          }
          return (
            <div className="flex flex-col gap-0.5">
              {keys.slice(0, 3).map((key) => (
                <span key={key} className="text-muted-foreground text-xs break-all">
                  <span className="zke-mono">{key}</span> {quota?.used?.[key] ?? "0"} /{" "}
                  {quota?.hard?.[key]}
                </span>
              ))}
              {keys.length > 3 ? (
                <span className="text-subtle-foreground text-xs">
                  另有 {keys.length - 3} 项，详情见对象页
                </span>
              ) : null}
            </div>
          );
        },
      },
      {
        header: "作用范围",
        size: 200,
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs break-all">
            {row.original.resource_quota?.scopes.join(", ") || "全部对象"}
          </span>
        ),
      },
    ];
  }
  if (resource === "limitranges") {
    return [
      {
        header: "限制类型",
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs break-all">
            {row.original.limit_range?.types.join(", ") || "—"}
          </span>
        ),
      },
      {
        header: "限制项",
        size: 100,
        cell: ({ row }) => (
          <span className="zke-tnum text-muted-foreground text-xs">
            {row.original.limit_range?.item_count ?? 0}
          </span>
        ),
      },
    ];
  }
  if (resource === "networkpolicies") {
    return [
      {
        header: "作用对象",
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs break-all">
            {selectorText(row.original.network_policy?.pod_selector?.match_labels)}
          </span>
        ),
      },
      {
        header: "方向",
        size: 150,
        cell: ({ row }) => (
          <div className="flex flex-wrap gap-1">
            {(row.original.network_policy?.policy_types ?? []).map((type) => (
              <Badge key={type} tone={type === "Ingress" ? "info" : "neutral"}>
                {type}
              </Badge>
            ))}
          </div>
        ),
      },
      {
        header: "规则数",
        size: 120,
        cell: ({ row }) => (
          <span className="zke-tnum text-muted-foreground text-xs">
            入 {row.original.network_policy?.ingress_rules ?? 0} · 出{" "}
            {row.original.network_policy?.egress_rules ?? 0}
          </span>
        ),
      },
    ];
  }
  if (resource === "poddisruptionbudgets") {
    return [
      {
        header: "保护对象",
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs break-all">
            {selectorText(row.original.disruption_budget?.selector?.match_labels)}
          </span>
        ),
      },
      {
        header: "预算",
        size: 160,
        cell: ({ row }) => {
          const budget = row.original.disruption_budget;
          return (
            <span className="text-muted-foreground text-xs">
              {budget?.min_available
                ? `minAvailable ${budget.min_available}`
                : budget?.max_unavailable
                  ? `maxUnavailable ${budget.max_unavailable}`
                  : "—"}
            </span>
          );
        },
      },
      {
        header: "健康 / 期望",
        size: 140,
        cell: ({ row }) => {
          const budget = row.original.disruption_budget;
          return (
            <span className="zke-tnum text-muted-foreground text-xs">
              {budget?.current_healthy ?? 0} / {budget?.desired_healthy ?? 0}
            </span>
          );
        },
      },
      {
        header: "可中断",
        size: 100,
        cell: ({ row }) => {
          const allowed = row.original.disruption_budget?.disruptions_allowed ?? 0;
          return <Badge tone={allowed > 0 ? "success" : "warning"}>{allowed}</Badge>;
        },
      },
    ];
  }
  return [
    {
      header: "优先级",
      size: 130,
      cell: ({ row }) => (
        <span className="zke-tnum text-foreground text-xs">
          {row.original.priority_class?.value ?? 0}
        </span>
      ),
    },
    {
      header: "抢占策略",
      size: 190,
      cell: ({ row }) => (
        <span className="text-muted-foreground text-xs">
          {row.original.priority_class?.preemption_policy || "PreemptLowerPriority"}
        </span>
      ),
    },
    {
      header: "标记",
      size: 110,
      cell: ({ row }) =>
        row.original.priority_class?.global_default ? <Badge tone="primary">集群默认</Badge> : null,
    },
    {
      header: "描述",
      cell: ({ row }) => (
        <span className="text-muted-foreground text-xs break-all">
          {row.original.priority_class?.description || "—"}
        </span>
      ),
    },
  ];
}

function selectorText(labels: Record<string, string> | undefined): string {
  const entries = Object.entries(labels ?? {});
  if (entries.length === 0) {
    return "命名空间中的所有 Pod";
  }
  return entries.map(([key, value]) => `${key}=${value}`).join(", ");
}

function PolicyDetailView({
  clusterId,
  namespace,
  resource,
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
  resource: KubernetesPolicyResource;
  name: string;
  canUpdate: boolean;
  canDelete: boolean;
  canDescribe: boolean;
  onEdit: (item: KubernetesPolicyResourceSummary) => void;
  onOpenYaml: () => void;
  onDescribe: () => void;
  onDelete: (item: KubernetesPolicyResourceSummary) => void;
  onBack: () => void;
}) {
  const detail = usePolicyResource(clusterId, namespace, resource, name);
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
        <PolicyDetailCards item={item} />
      )}
    </div>
  );
}

function PolicyDescribeView({
  clusterId,
  namespace,
  resource,
  name,
  onBack,
}: {
  clusterId: string;
  namespace: string;
  resource: PolicyDescribeResource;
  name: string;
  onBack: () => void;
}) {
  const describe = usePolicyDescribe(clusterId, namespace, resource, name);
  return (
    <DescribeView
      name={name}
      kindLabel={policyKindLabel(resource)}
      data={describe.data}
      isLoading={describe.isLoading}
      isFetching={describe.isFetching}
      error={describe.error}
      onRetry={() => void describe.refetch()}
      onBack={onBack}
    />
  );
}

function supportsPolicyDescribe(
  resource: KubernetesPolicyResource,
): resource is PolicyDescribeResource {
  return resource === "resourcequotas" || resource === "poddisruptionbudgets";
}

function PolicyDetailCards({ item }: { item: KubernetesPolicyResourceDetail }) {
  const quota = item.resource_quota;
  const limitRange = item.limit_range_detail;
  const network = item.network_policy;
  const networkDetail = item.network_policy_detail;
  const budget = item.disruption_budget;
  const budgetDetail = item.disruption_budget_detail;
  const priorityClass = item.priority_class;

  return (
    <div className="grid gap-3 md:grid-cols-2">
      <DetailCard title="概览">
        <DetailRow label="名称" value={item.name} />
        <DetailRow
          label="类型"
          value={
            <span className="zke-mono text-xs">
              {item.api_version} · {item.kind}
            </span>
          }
        />
        {item.namespace ? <DetailRow label="命名空间" value={item.namespace} /> : null}
        <DetailRow
          label="UID"
          value={<span className="zke-mono text-xs break-all">{item.uid || "—"}</span>}
        />
        <DetailRow
          label="版本"
          value={<span className="zke-mono text-xs">{item.resource_version || "—"}</span>}
        />
        <DetailRow label="创建时间" value={formatAbsolute(item.creation_timestamp)} />
      </DetailCard>

      {quota ? (
        <DetailCard title="额度与用量">
          {Object.keys(quota.hard).length === 0 ? (
            <DetailRow label="额度" value="—" />
          ) : (
            Object.keys(quota.hard)
              .sort()
              .map((key) => (
                <DetailRow
                  key={key}
                  label={key}
                  value={
                    <span className="zke-tnum">
                      {quota.used?.[key] ?? "0"} / {quota.hard[key]}
                    </span>
                  }
                />
              ))
          )}
          <DetailRow label="作用范围" value={quota.scopes.join(", ") || "全部对象"} />
        </DetailCard>
      ) : null}

      {quota && quota.scope_selector.length > 0 ? (
        <DetailCard title="scopeSelector">
          {quota.scope_selector.map((requirement, index) => (
            <DetailRow
              key={index}
              label={requirement.scope_name}
              value={
                <span className="text-xs break-all">
                  {requirement.operator} {requirement.values.join(", ")}
                </span>
              }
            />
          ))}
        </DetailCard>
      ) : null}

      {limitRange
        ? limitRange.items.map((limit, index) => (
            <DetailCard key={index} title={`限制项 · ${limit.type}`}>
              {(
                [
                  ["默认限制", limit.default],
                  ["默认请求", limit.default_request],
                  ["上限", limit.max],
                  ["下限", limit.min],
                  ["限制/请求比值上限", limit.max_limit_request_ratio],
                ] as const
              ).map(([label, values]) =>
                values && Object.keys(values).length > 0 ? (
                  <DetailRow
                    key={label}
                    label={label}
                    value={
                      <span className="text-xs break-all">
                        {Object.entries(values)
                          .map(([key, value]) => `${key}=${value}`)
                          .join(", ")}
                      </span>
                    }
                  />
                ) : null,
              )}
            </DetailCard>
          ))
        : null}

      {network ? (
        <DetailCard title="NetworkPolicy">
          <DetailRow label="作用对象" value={selectorText(network.pod_selector?.match_labels)} />
          <DetailRow label="方向" value={network.policy_types.join(", ") || "—"} />
          <DetailRow
            label="规则数"
            value={`入站 ${network.ingress_rules} · 出站 ${network.egress_rules}`}
          />
        </DetailCard>
      ) : null}

      {networkDetail && networkDetail.ingress.length > 0 ? (
        <DetailCard title="入站规则">
          {networkDetail.ingress.map((rule, index) => (
            <DetailRow key={index} label={`规则 ${index + 1}`} value={ruleText(rule, "来源")} />
          ))}
        </DetailCard>
      ) : null}

      {networkDetail && networkDetail.egress.length > 0 ? (
        <DetailCard title="出站规则">
          {networkDetail.egress.map((rule, index) => (
            <DetailRow key={index} label={`规则 ${index + 1}`} value={ruleText(rule, "目标")} />
          ))}
        </DetailCard>
      ) : null}

      {budget ? (
        <DetailCard title="PodDisruptionBudget">
          <DetailRow label="保护对象" value={selectorText(budget.selector?.match_labels)} />
          <DetailRow
            label="预算"
            value={
              budget.min_available
                ? `minAvailable ${budget.min_available}`
                : budget.max_unavailable
                  ? `maxUnavailable ${budget.max_unavailable}`
                  : "—"
            }
          />
          <DetailRow label="当前健康" value={String(budget.current_healthy)} />
          <DetailRow label="期望健康" value={String(budget.desired_healthy)} />
          <DetailRow label="允许中断" value={String(budget.disruptions_allowed)} />
          <DetailRow label="覆盖 Pod" value={String(budget.expected_pods)} />
          {budgetDetail?.unhealthy_pod_eviction_policy ? (
            <DetailRow
              label="不健康 Pod 驱逐策略"
              value={budgetDetail.unhealthy_pod_eviction_policy}
            />
          ) : null}
        </DetailCard>
      ) : null}

      {budgetDetail && budgetDetail.conditions.length > 0 ? (
        <DetailCard title="条件">
          <DetailConditions conditions={budgetDetail.conditions} />
        </DetailCard>
      ) : null}

      {priorityClass ? (
        <DetailCard title="PriorityClass">
          <DetailRow label="优先级值" value={String(priorityClass.value)} />
          <DetailRow label="集群默认" value={priorityClass.global_default ? "是" : "否"} />
          <DetailRow
            label="抢占策略"
            value={priorityClass.preemption_policy || "PreemptLowerPriority"}
          />
          <DetailRow label="描述" value={priorityClass.description || "—"} />
        </DetailCard>
      ) : null}

      <DetailCard title="标签">
        <DetailKeyValues entries={item.labels} />
      </DetailCard>
      <DetailCard title="注解">
        <DetailKeyValues entries={item.annotations} />
      </DetailCard>
    </div>
  );
}

function ruleText(rule: KubernetesNetworkPolicyRule, direction: string): string {
  const peers = (rule.peers ?? []).map((peer) => {
    if (peer.ip_block) {
      const except = peer.ip_block.except ?? [];
      return except.length > 0
        ? `${peer.ip_block.cidr}（排除 ${except.join("、")}）`
        : peer.ip_block.cidr;
    }
    const parts = [
      peer.namespace_selector
        ? `命名空间 ${selectorText(peer.namespace_selector.match_labels)}`
        : "",
      peer.pod_selector ? `Pod ${selectorText(peer.pod_selector.match_labels)}` : "",
    ].filter(Boolean);
    return parts.join(" 中的 ") || "任意对象";
  });
  const ports = (rule.ports ?? []).map((port) => {
    const protocol = port.protocol ?? "TCP";
    if (!port.port) {
      return `${protocol} 全部端口`;
    }
    return port.end_port ? `${protocol} ${port.port}-${port.end_port}` : `${protocol} ${port.port}`;
  });
  return [
    peers.length > 0 ? `${direction}：${peers.join("；")}` : `${direction}：任意`,
    ports.length > 0 ? `端口：${ports.join("、")}` : "端口：全部",
  ].join("　");
}
