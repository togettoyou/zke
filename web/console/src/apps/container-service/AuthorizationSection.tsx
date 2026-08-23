import { useCallback, useEffect, useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { FileCode, Lock, Pencil, Plus } from "lucide-react";
import { toast } from "sonner";

import {
  isNamespacedAuthorization,
  useAuthorizationResource,
  useAuthorizationResources,
  useDeleteAuthorizationResource,
} from "@/api/queries/authorization";
import type {
  KubernetesAuthorizationResource,
  KubernetesAuthorizationResourceDetail,
  KubernetesAuthorizationResourceSummary,
} from "@/api/types";
import { PageHeader, SectionToolbarActions } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
import { DetailDeleteAction, RowDeleteAction } from "@/components/common/delete-action";
import { DetailCard, DetailKeyValues, DetailRow } from "@/components/common/detail";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { RefreshAction } from "@/components/common/refresh-action";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/misc";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { HintTooltip } from "@/components/ui/tooltip";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { AuthorizationForm } from "./AuthorizationForm";
import { YamlEditorView } from "./YamlEditorView";
import { useContinuePagination } from "./use-continue-pagination";
import type { ClusterSectionProps } from "./types";
import { canUseProtectedNamespace } from "./namespace-permissions";
import {
  AUTHORIZATION_TYPES,
  authorizationKindLabel,
  hasRules,
  isBinding,
} from "./authorization-catalog";

const PAGE_SIZE = 50;

type AuthorizationSectionProps = ClusterSectionProps & {
  /** Only the ServiceAccount, Role and RoleBinding tabs are scoped by it. */
  namespace: string;
  onNamespaceScopeChange: (namespaced: boolean) => void;
};

/**
 * The RBAC objects of one Cluster: identities, permission sets and the grants
 * between them.
 *
 * Reading and writing these is its own permission — `cluster.rbac.read` and
 * `cluster.rbac.manage` — rather than part of reading or writing Cluster
 * resources, because a grant is the thing that decides what every other
 * permission is worth.
 */
export function AuthorizationSection({
  clusterId,
  clusterName,
  agentNamespace,
  namespace,
  tenantId,
  projectId,
  onNamespaceScopeChange,
}: AuthorizationSectionProps) {
  const { permissions } = useSessionContext();
  const [resource, setResource] = useState<KubernetesAuthorizationResource>("serviceaccounts");
  const namespaced = isNamespacedAuthorization(resource);
  const pager = useContinuePagination(`${clusterId}/${namespaced ? namespace : ""}/${resource}`);
  const list = useAuthorizationResources(clusterId, namespace, resource, {
    limit: PAGE_SIZE,
    ...(pager.token ? { continue: pager.token } : {}),
  });
  const [detailName, setDetailName] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<KubernetesAuthorizationResourceSummary | null>(null);
  const [yamlTarget, setYamlTarget] = useState<KubernetesAuthorizationResourceSummary | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<KubernetesAuthorizationResourceSummary | null>(
    null,
  );
  const [deletePreviewed, setDeletePreviewed] = useState(false);
  const deletePreviewKey = useSubmissionKey(deleteTarget !== null);
  const deleteApplyKey = useSubmissionKey(deleteTarget !== null);
  const remove = useDeleteAuthorizationResource();

  useEffect(() => onNamespaceScopeChange(namespaced), [namespaced, onNamespaceScopeChange]);

  const projectScope = {
    type: "project",
    tenantId,
    projectId,
  } as const;
  const canManage =
    permissions.can("cluster.rbac.manage", projectScope) &&
    (!namespaced ||
      canUseProtectedNamespace(permissions, { namespace, agentNamespace }, projectScope));

  // Both the row action and the detail view open the same confirmation, so it is
  // one callback rather than two copies of the reset sequence.
  const openDelete = useCallback(
    (item: KubernetesAuthorizationResourceSummary) => {
      setDeleteTarget(item);
      setDeletePreviewed(false);
      remove.reset();
    },
    [remove],
  );

  const columns = useMemo<ColumnDef<KubernetesAuthorizationResourceSummary, unknown>[]>(
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
        header: "标记",
        size: 130,
        cell: ({ row }) => <Markers item={row.original} />,
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
        size: 88,
        cell: ({ row }) => {
          const locked = lockReason(row.original, namespaced);
          return (
            <div className="flex justify-end gap-0.5" onClick={(event) => event.stopPropagation()}>
              {canManage ? (
                <HintTooltip label={locked ?? "编辑"}>
                  <span>
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      aria-label={`编辑 ${row.original.name}`}
                      disabled={locked !== null}
                      onClick={() => setEditing(row.original)}
                    >
                      {locked ? <Lock /> : <Pencil />}
                    </Button>
                  </span>
                </HintTooltip>
              ) : null}
              {canManage && row.original.uid ? (
                <RowDeleteAction
                  name={row.original.name}
                  hint={!namespaced && row.original.managed_by_zke ? PROTECTED_HINT : "删除"}
                  disabled={!namespaced && row.original.managed_by_zke}
                  onDelete={() => openDelete(row.original)}
                />
              ) : null}
            </div>
          );
        },
      },
    ],
    [resource, namespaced, canManage, openDelete],
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
        title={`删除 ${authorizationKindLabel(resource)}`}
        description={
          deletePreviewed
            ? "DryRun 预检已通过。再次确认将提交实际删除。"
            : "首次点击只执行服务端 DryRun 预检；通过后才能实际删除。"
        }
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          ...(namespaced ? [{ label: "命名空间", name: namespace }] : []),
          {
            label: authorizationKindLabel(resource),
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
              toast.success(`${authorizationKindLabel(resource)} ${deleteTarget.name} 已提交删除`);
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

  if (yamlTarget) {
    return (
      <YamlEditorView
        identity={
          isNamespacedAuthorization(resource)
            ? { kind: "authorization", clusterId, namespace, resource, name: yamlTarget.name }
            : { kind: "authorization", clusterId, resource, name: yamlTarget.name }
        }
        clusterName={clusterName}
        kindLabel={authorizationKindLabel(resource)}
        canUpdate={canManage}
        readOnlyReason={
          !namespaced && yamlTarget.managed_by_zke ? `${PROTECTED_HINT}。` : undefined
        }
        impacts={[
          "整份文档会替换集群中的现有对象，未在文档中出现的字段将被移除。",
          "服务端会核对文档内的 UID 与 resourceVersion；期间对象若已变化，本次更新会被拒绝而不是覆盖。",
          "授权变更立即生效：规则或主体一旦改变，对应身份的访问能力随之改变。",
          "escalate、bind、impersonate 以及 secrets、serviceaccounts/token 仍会被拒绝，roleRef 不可改写。",
        ]}
        onBack={() => setYamlTarget(null)}
      />
    );
  }

  // The form takes over the section rather than sitting over the list, like every
  // other typed form here: a role's rules or a binding's subjects are taller than
  // a box laid over the table can show, and the table is of no use while they are
  // being filled in.
  if (creating || editing) {
    return (
      <AuthorizationForm
        clusterId={clusterId}
        clusterName={clusterName}
        namespace={namespace}
        resource={resource}
        editingName={editing?.name ?? null}
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
        <AuthorizationDetailView
          clusterId={clusterId}
          namespace={namespace}
          resource={resource}
          name={detailName}
          canManage={canManage}
          onEdit={setEditing}
          onOpenYaml={setYamlTarget}
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
        {canManage && !waitingForNamespace ? (
          <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
            <Plus />
            创建 {authorizationKindLabel(resource)}
          </Button>
        ) : null}
      </SectionToolbarActions>
      <Tabs
        value={resource}
        onValueChange={(value) => {
          setResource(value as KubernetesAuthorizationResource);
          setDetailName(null);
        }}
        className="flex min-h-0 flex-1 flex-col"
      >
        <TabsList className="w-fit">
          {AUTHORIZATION_TYPES.map((type) => (
            <TabsTrigger key={type.resource} value={type.resource}>
              {type.label}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value={resource} className="flex min-h-0 flex-1 flex-col">
          {waitingForNamespace ? (
            <Alert tone="info">
              {authorizationKindLabel(resource)}{" "}
              按命名空间定域，正在等待工具栏的命名空间选择器解析出一个可用的命名空间。
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
              emptyTitle={`没有 ${authorizationKindLabel(resource)}`}
              emptyDescription={
                namespaced
                  ? `${namespace} 中没有可见的 ${authorizationKindLabel(resource)}。`
                  : `该集群中没有可见的 ${authorizationKindLabel(resource)}。`
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

const PROTECTED_HINT = "ZKE 管理的集群级授权对象受保护，不能在此修改或删除";

/** Why an object cannot be edited through the typed form, if it cannot. */
function lockReason(
  item: KubernetesAuthorizationResourceSummary,
  namespaced: boolean,
): string | null {
  if (!namespaced && item.managed_by_zke) {
    return PROTECTED_HINT;
  }
  if (item.aggregated) {
    return "聚合 ClusterRole 的规则由控制器合成，请用 YAML 编辑";
  }
  return null;
}

function Markers({ item }: { item: KubernetesAuthorizationResourceSummary }) {
  const markers = [
    item.managed_by_zke ? { label: "ZKE 管理", tone: "primary" as const } : null,
    item.aggregated ? { label: "聚合", tone: "info" as const } : null,
  ].filter((entry) => entry !== null);
  if (markers.length === 0) {
    return <span className="text-subtle-foreground text-xs">—</span>;
  }
  return (
    <div className="flex flex-col items-start gap-0.5">
      {markers.map((marker) => (
        <Badge key={marker.label} tone={marker.tone}>
          <StatusDot tone={marker.tone} />
          {marker.label}
        </Badge>
      ))}
    </div>
  );
}

function deleteImpacts(resource: KubernetesAuthorizationResource): string[] {
  const precondition =
    "请求携带该对象当前的 UID 与 resourceVersion 前置条件，期间对象若已变化或被重建，删除会被拒绝。";
  if (resource === "serviceaccounts") {
    return [
      "使用该 ServiceAccount 的 Pod 将失去其身份：已运行的 Pod 中的 Token 会失效，新 Pod 无法启动。",
      "引用它的 RoleBinding 和 ClusterRoleBinding 不会被清理，会指向一个不存在的主体。",
      precondition,
    ];
  }
  if (hasRules(resource)) {
    return [
      "引用该角色的所有绑定会立即失去授权，对应主体将被拒绝访问。",
      "绑定本身不会被删除，会指向一个不存在的角色。",
      precondition,
    ];
  }
  return [
    "该绑定授予的权限会立即撤销，其中的主体将失去对应访问能力。",
    "角色和主体本身不受影响。",
    precondition,
  ];
}

function typeColumns(
  resource: KubernetesAuthorizationResource,
): ColumnDef<KubernetesAuthorizationResourceSummary, unknown>[] {
  if (resource === "serviceaccounts") {
    return [
      {
        header: "自动挂载 Token",
        size: 150,
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs">
            {row.original.automount_service_account_token === undefined
              ? "默认"
              : row.original.automount_service_account_token
                ? "是"
                : "否"}
          </span>
        ),
      },
      {
        header: "Secret 引用",
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs">
            {/* Counts only. The endpoint never returns Secret names or bodies,
                and this page must not imply that it could. */}
            {row.original.secret_reference_count} 个（不展示名称）
          </span>
        ),
      },
    ];
  }
  if (hasRules(resource)) {
    return [
      {
        header: "规则数",
        size: 100,
        cell: ({ row }) => <span className="zke-tnum">{row.original.rule_count}</span>,
      },
    ];
  }
  return [
    {
      header: "角色",
      cell: ({ row }) => (
        <span className="text-muted-foreground text-xs break-all">
          {row.original.role_ref
            ? `${row.original.role_ref.kind}/${row.original.role_ref.name}`
            : "—"}
        </span>
      ),
    },
    {
      header: "主体数",
      size: 100,
      cell: ({ row }) => <span className="zke-tnum">{row.original.subject_count}</span>,
    },
  ];
}

function AuthorizationDetailView({
  clusterId,
  namespace,
  resource,
  name,
  canManage,
  onEdit,
  onOpenYaml,
  onDelete,
  onBack,
}: {
  clusterId: string;
  namespace: string;
  resource: KubernetesAuthorizationResource;
  name: string;
  canManage: boolean;
  onEdit: (item: KubernetesAuthorizationResourceSummary) => void;
  onOpenYaml: (item: KubernetesAuthorizationResourceSummary) => void;
  onDelete: (item: KubernetesAuthorizationResourceSummary) => void;
  onBack: () => void;
}) {
  const detail = useAuthorizationResource(clusterId, namespace, resource, name);
  const item = detail.data;
  const namespaced = isNamespacedAuthorization(resource);
  const locked = item ? lockReason(item, namespaced) : null;

  return (
    <div className="grid gap-3">
      <PageHeader
        title={name}
        onBack={onBack}
        actions={
          <>
            {/* The rules are checked on the way out either way: the YAML route
                runs the same guard the form does. */}
            {item ? (
              <Button size="sm" variant="secondary" onClick={() => onOpenYaml(item)}>
                <FileCode />
                YAML
              </Button>
            ) : null}
            {canManage && item && locked === null ? (
              <Button size="sm" variant="secondary" onClick={() => onEdit(item)}>
                <Pencil />
                编辑
              </Button>
            ) : null}
            {canManage && item?.uid ? (
              <DetailDeleteAction
                name={name}
                hint={!namespaced && item.managed_by_zke ? PROTECTED_HINT : undefined}
                disabled={!namespaced && item.managed_by_zke}
                onDelete={() => onDelete(item)}
              />
            ) : null}
          </>
        }
      />
      {detail.error ? (
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      ) : detail.isLoading || !item ? (
        <LoadingState />
      ) : (
        <AuthorizationDetailCards item={item} locked={locked} />
      )}
    </div>
  );
}

function AuthorizationDetailCards({
  item,
  locked,
}: {
  item: KubernetesAuthorizationResourceDetail;
  locked: string | null;
}) {
  return (
    <div className="grid gap-3">
      {locked ? <Alert tone="warning">{locked}。</Alert> : null}

      <div className="grid gap-3 @md:grid-cols-2">
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
          {item.role_ref ? (
            <DetailRow
              label="引用角色"
              value={
                <span className="break-all">
                  {item.role_ref.kind}/{item.role_ref.name}
                  <span className="text-subtle-foreground ml-2 text-xs">创建后不可更改</span>
                </span>
              }
            />
          ) : null}
          {item.resource === "serviceaccounts" ? (
            <>
              <DetailRow
                label="自动挂载 Token"
                value={
                  item.automount_service_account_token === undefined
                    ? "默认"
                    : item.automount_service_account_token
                      ? "是"
                      : "否"
                }
              />
              <DetailRow
                label="Secret 引用"
                value={
                  <span>
                    {item.secret_reference_count} 个
                    <span className="text-subtle-foreground ml-2 text-xs">
                      接口不返回 Secret 名称或正文
                    </span>
                  </span>
                }
              />
            </>
          ) : null}
          <DetailRow label="创建时间" value={formatAbsolute(item.creation_timestamp)} />
        </DetailCard>

        <DetailCard title="标签">
          <DetailKeyValues entries={item.labels} />
        </DetailCard>
        <DetailCard title="注解">
          <DetailKeyValues entries={item.annotations} />
        </DetailCard>
      </div>

      {hasRules(item.resource) ? (
        <DetailCard title="规则">
          {item.rules.length === 0 ? (
            <DetailRow label="规则" value="—" />
          ) : (
            <div className="grid gap-2 py-1">
              {item.rules.map((rule, index) => (
                <div
                  key={index}
                  className="border-border/50 grid gap-0.5 border-b pb-2 text-[13px] last:border-b-0 last:pb-0"
                >
                  <span className="zke-mono text-foreground text-xs break-all">
                    {rule.verbs.join(", ") || "—"}
                  </span>
                  <span className="text-muted-foreground text-xs break-all">
                    {ruleTargets(rule)}
                  </span>
                </div>
              ))}
            </div>
          )}
        </DetailCard>
      ) : null}

      {isBinding(item.resource) ? (
        <DetailCard title="主体">
          {item.subjects.length === 0 ? (
            <DetailRow label="主体" value="—" />
          ) : (
            item.subjects.map((subject, index) => (
              <DetailRow
                key={`${subject.kind}/${subject.namespace}/${subject.name}/${index}`}
                label={subject.kind}
                value={
                  <span className="break-all">
                    {subject.namespace ? `${subject.namespace}/` : ""}
                    {subject.name}
                  </span>
                }
              />
            ))
          )}
        </DetailCard>
      ) : null}
    </div>
  );
}

/** What one policy rule applies to, in the order Kubernetes reads it. */
function ruleTargets(rule: KubernetesAuthorizationResourceDetail["rules"][number]): string {
  if (rule.non_resource_urls.length > 0) {
    return `非资源路径 ${rule.non_resource_urls.join(", ")}`;
  }
  const groups = rule.api_groups.map((group) => group || "core").join(", ") || "—";
  const names =
    rule.resource_names.length > 0 ? `（限定名称 ${rule.resource_names.join(", ")}）` : "";
  return `${groups} / ${rule.resources.join(", ") || "—"}${names}`;
}
