import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import {
  ChevronDown,
  ChevronRight,
  FileCode,
  Folder,
  LayoutGrid,
  RotateCw,
  Search,
} from "lucide-react";
import { toast } from "sonner";

import { useNamespaces } from "@/api/queries/namespaces";
import {
  groupResourceTypes,
  useDeleteGenericResource,
  useGenericResources,
  useResourceTypes,
  type GenericResourceIdentity,
  type UnstructuredObject,
} from "@/api/queries/kubernetes-resources";
import type { KubernetesResourceType } from "@/api/types";
import { SectionToolbarActions } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
import { RowDeleteAction } from "@/components/common/delete-action";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { RefreshAction } from "@/components/common/refresh-action";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert, Checkbox } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/cn";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { useContinuePagination } from "./use-continue-pagination";
import type { ClusterSectionProps } from "./types";
import { YamlEditorView } from "./YamlEditorView";

const PAGE_SIZE = 50;
/** The Namespace picker reads one page at the endpoint's maximum. */
const NAMESPACE_LIMIT = 500;
const ALL_NAMESPACES = "__all__";

/**
 * Browses every Kubernetes resource the target Cluster's Agent can see.
 *
 * The typed sections answer "manage this kind"; this one answers "what is in
 * this cluster at all", including CRDs and the objects operators install
 * themselves. It is therefore built on the generic Discovery and Resource
 * endpoints rather than on any kind-specific model: the tree is whatever the
 * Cluster's own API discovery reports, and a row is whatever Kubernetes
 * returned, read through `metadata` alone.
 *
 * Reads are `cluster.read`; the two writes it offers — YAML edit and delete —
 * go through the same guarded paths the typed sections use.
 */
export function ResourceBrowserSection({
  clusterId,
  clusterName,
  tenantId,
  projectId,
}: ClusterSectionProps) {
  const { permissions } = useSessionContext();
  const catalog = useResourceTypes(clusterId);
  const [customOnly, setCustomOnly] = useState(false);
  const [typeQuery, setTypeQuery] = useState("");
  const [selected, setSelected] = useState<KubernetesResourceType | null>(null);

  const projectScope = { type: "project" as const, tenantId, projectId };
  const canUpdate = permissions.can("cluster.resource.update", projectScope);
  const canDelete = permissions.can("cluster.resource.delete", projectScope);

  // The fallback is memoised with the catalog rather than written inline: a new
  // empty array on every render would re-filter and re-group the whole catalog
  // each time, and a large cluster's catalog is thousands of entries.
  const types = useMemo(() => catalog.data?.resources ?? [], [catalog.data]);
  const customResourcesKnown = catalog.data?.custom_resources_known ?? false;
  const filtered = useMemo(() => {
    const needle = typeQuery.trim().toLowerCase();
    return types.filter((type) => {
      if (customOnly && !type.custom_resource) {
        return false;
      }
      if (needle === "") {
        return true;
      }
      return (
        type.resource.toLowerCase().includes(needle) ||
        type.kind.toLowerCase().includes(needle) ||
        type.group.toLowerCase().includes(needle)
      );
    });
  }, [types, typeQuery, customOnly]);
  const tree = useMemo(() => groupResourceTypes(filtered), [filtered]);

  // The selected kind survives a filter change only while it still matches, so
  // the table never shows rows the tree no longer offers.
  const active = selected && filtered.some((type) => sameType(type, selected)) ? selected : null;

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      <SectionToolbarActions>
        <RefreshAction isFetching={catalog.isFetching} onRefresh={() => void catalog.refetch()} />
      </SectionToolbarActions>
      <div className="grid min-h-0 flex-1 gap-3 lg:grid-cols-[300px_minmax(0,1fr)]">
        <ResourceTypeTree
          tree={tree}
          total={types.length}
          loading={catalog.isLoading}
          error={catalog.error}
          onRetry={() => void catalog.refetch()}
          partial={catalog.data?.partial ?? false}
          customOnly={customOnly}
          onCustomOnlyChange={setCustomOnly}
          customResourcesKnown={customResourcesKnown}
          query={typeQuery}
          onQueryChange={setTypeQuery}
          selected={active}
          onSelect={setSelected}
        />

        {active ? (
          <ResourceObjectPanel
            key={typeKey(active)}
            clusterId={clusterId}
            clusterName={clusterName}
            type={active}
            canUpdate={canUpdate}
            canDelete={canDelete}
          />
        ) : (
          <div className="border-border rounded-panel bg-surface flex items-center justify-center border p-6">
            <div className="max-w-sm text-center">
              <p className="text-foreground text-sm font-medium">从左侧选择一个资源类型</p>
              <p className="text-muted-foreground mt-1 text-[13px]">
                资源树来自目标集群 Agent 的 API Discovery，安装 Operator 或 CRD
                后刷新即可看到新类型。
              </p>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function ResourceTypeTree({
  tree,
  total,
  loading,
  error,
  onRetry,
  partial,
  customOnly,
  onCustomOnlyChange,
  customResourcesKnown,
  query,
  onQueryChange,
  selected,
  onSelect,
}: {
  tree: { group: string; versions: { version: string; resources: KubernetesResourceType[] }[] }[];
  total: number;
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  partial: boolean;
  customOnly: boolean;
  onCustomOnlyChange: (value: boolean) => void;
  customResourcesKnown: boolean;
  query: string;
  onQueryChange: (value: string) => void;
  selected: KubernetesResourceType | null;
  onSelect: (type: KubernetesResourceType) => void;
}) {
  // Nothing is open until it is opened, at either level. A cluster with a few
  // operators installed has more groups than fit on a screen, and a search or
  // the CRD filter does not open them either — the match count on each row is
  // what says where the hits are, so filtering narrows the tree instead of
  // unfolding it.
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const filtering = query.trim() !== "" || customOnly;
  const toggle = (key: string) => setExpanded((current) => ({ ...current, [key]: !current[key] }));

  return (
    <div className="border-border rounded-panel bg-surface flex min-h-0 flex-col border">
      <div className="border-border flex items-center justify-between gap-2 border-b p-3">
        <span className="text-foreground text-[13px] font-medium">资源类型</span>
        <label className="flex items-center gap-2 text-[13px]">
          <Checkbox
            checked={customOnly}
            onCheckedChange={(checked) => onCustomOnlyChange(checked === true)}
          />
          仅显示 CRD
        </label>
      </div>

      <div className="border-border border-b p-3">
        <div className="relative">
          <Search className="text-subtle-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
          <Input
            value={query}
            onChange={(event) => onQueryChange(event.target.value)}
            placeholder="请输入资源对象名称"
            aria-label="筛选资源类型"
            autoComplete="off"
            spellCheck={false}
            className="pl-8"
          />
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-2">
        {error ? (
          <ErrorState error={error} onRetry={onRetry} />
        ) : loading ? (
          <LoadingState />
        ) : (
          <>
            {customOnly && !customResourcesKnown ? (
              <Alert tone="warning" className="mb-2">
                Agent 无法读取该集群的 CustomResourceDefinition 列表，因此无法判定哪些资源来自
                CRD。请确认 Agent ServiceAccount 拥有 `apiextensions.k8s.io/v1
                customresourcedefinitions` 的 `get`、`list`。
              </Alert>
            ) : null}
            {partial ? (
              <Alert tone="info" className="mb-2">
                部分 API Group 发现失败，资源树只包含成功的部分。
              </Alert>
            ) : null}

            <div className="text-muted-foreground flex items-center gap-2 px-1 py-1.5 text-[13px]">
              <LayoutGrid className="size-4" />
              全部资源
              <span className="text-subtle-foreground zke-tnum text-xs">
                {tree.reduce(
                  (count, group) =>
                    count +
                    group.versions.reduce((inner, version) => inner + version.resources.length, 0),
                  0,
                )}
                {customOnly || query.trim() !== "" ? ` / ${total}` : ""}
              </span>
            </div>

            {tree.length === 0 ? (
              <p className="text-muted-foreground px-1 py-2 text-xs">
                {customOnly ? "该集群没有匹配的自定义资源。" : "没有匹配的资源类型。"}
              </p>
            ) : null}

            {tree.map((group) => {
              const groupOpen = expanded[group.group] === true;
              const groupCount = group.versions.reduce(
                (count, version) => count + version.resources.length,
                0,
              );
              return (
                <div key={group.group}>
                  <button
                    type="button"
                    aria-expanded={groupOpen}
                    className="hover:bg-surface-muted rounded-control flex w-full items-center gap-1.5 px-1 py-1.5 text-left text-[13px]"
                    onClick={() => toggle(group.group)}
                  >
                    {groupOpen ? (
                      <ChevronDown className="text-subtle-foreground size-3.5 shrink-0" />
                    ) : (
                      <ChevronRight className="text-subtle-foreground size-3.5 shrink-0" />
                    )}
                    <Folder className="text-subtle-foreground size-4 shrink-0" />
                    <span className="text-foreground truncate">{group.group}</span>
                    {/* While a filter is on, the count is what tells a closed
                        group it holds matches. */}
                    {filtering ? (
                      <span className="zke-tnum text-subtle-foreground ml-auto shrink-0 text-xs">
                        {groupCount}
                      </span>
                    ) : null}
                  </button>
                  {groupOpen
                    ? group.versions.map((version) => {
                        const versionKey = `${group.group}/${version.version}`;
                        const versionOpen = expanded[versionKey] === true;
                        return (
                          <div key={version.version} className="pl-4">
                            <button
                              type="button"
                              aria-expanded={versionOpen}
                              className="hover:bg-surface-muted rounded-control text-muted-foreground flex w-full items-center gap-1.5 px-1 py-1 text-left text-xs"
                              onClick={() => toggle(versionKey)}
                            >
                              {versionOpen ? (
                                <ChevronDown className="text-subtle-foreground size-3 shrink-0" />
                              ) : (
                                <ChevronRight className="text-subtle-foreground size-3 shrink-0" />
                              )}
                              <Folder className="text-subtle-foreground size-3.5 shrink-0" />
                              <span className="truncate">{version.version}</span>
                              {filtering ? (
                                <span className="zke-tnum text-subtle-foreground ml-auto shrink-0">
                                  {version.resources.length}
                                </span>
                              ) : null}
                            </button>
                            {versionOpen ? (
                              <div className="pl-4">
                                {version.resources.map((type) => {
                                  const isSelected = selected !== null && sameType(selected, type);
                                  return (
                                    <button
                                      key={typeKey(type)}
                                      type="button"
                                      className={cn(
                                        "rounded-control flex w-full items-center gap-1.5 px-2 py-1.5 text-left text-[13px]",
                                        isSelected
                                          ? "bg-primary-surface text-primary"
                                          : "text-muted-foreground hover:bg-surface-muted",
                                      )}
                                      onClick={() => onSelect(type)}
                                    >
                                      <span className="truncate">{type.resource}</span>
                                      {type.custom_resource ? (
                                        <Badge tone="info" className="ml-auto shrink-0">
                                          CRD
                                        </Badge>
                                      ) : null}
                                    </button>
                                  );
                                })}
                              </div>
                            ) : null}
                          </div>
                        );
                      })
                    : null}
                </div>
              );
            })}
          </>
        )}
      </div>
    </div>
  );
}

function ResourceObjectPanel({
  clusterId,
  clusterName,
  type,
  canUpdate,
  canDelete,
}: {
  clusterId: string;
  clusterName: string;
  type: KubernetesResourceType;
  canUpdate: boolean;
  canDelete: boolean;
}) {
  const identity: GenericResourceIdentity = {
    group: type.group,
    version: type.version,
    resource: type.resource,
  };
  const [namespace, setNamespace] = useState(ALL_NAMESPACES);
  const [nameQuery, setNameQuery] = useState("");
  const [yamlTarget, setYamlTarget] = useState<UnstructuredObject | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<UnstructuredObject | null>(null);
  const [deletePreviewed, setDeletePreviewed] = useState(false);
  const deletePreviewKey = useSubmissionKey(deleteTarget !== null);
  const deleteApplyKey = useSubmissionKey(deleteTarget !== null);
  const remove = useDeleteGenericResource();

  const scopedNamespace = type.namespaced && namespace !== ALL_NAMESPACES ? namespace : "";
  const pager = useContinuePagination(`${clusterId}/${typeKey(type)}/${scopedNamespace}`);
  const list = useGenericResources(clusterId, identity, scopedNamespace, {
    limit: PAGE_SIZE,
    ...(pager.token ? { continue: pager.token } : {}),
  });
  const namespaces = useNamespaces(type.namespaced ? clusterId : null, { limit: NAMESPACE_LIMIT });

  const listable = type.verbs.includes("list");
  const deletable = canDelete && type.verbs.includes("delete");
  // The YAML editor reads through the same generic route, so it needs the verbs
  // that route uses.
  const readable = type.verbs.includes("get");

  const items = (list.data?.items ?? []) as UnstructuredObject[];
  const needle = nameQuery.trim().toLowerCase();
  // Filtered in the browser on purpose: Kubernetes field selectors match a name
  // exactly, and a substring search server-side would silently become an exact
  // one. The scope of the filter is stated next to the input.
  const rows = needle
    ? items.filter((item) => (item.metadata?.name ?? "").toLowerCase().includes(needle))
    : items;

  const columns = useMemo<ColumnDef<UnstructuredObject, unknown>[]>(() => {
    const base: ColumnDef<UnstructuredObject, unknown>[] = [
      {
        header: "名称",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground font-medium break-all">
              {row.original.metadata?.name ?? "—"}
            </span>
            <span className="zke-mono text-subtle-foreground text-xs break-all">
              {row.original.metadata?.uid ?? "UID 尚未分配"}
            </span>
          </div>
        ),
      },
    ];
    if (type.namespaced) {
      base.push({
        header: "命名空间",
        size: 200,
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs break-all">
            {row.original.metadata?.namespace ?? "—"}
          </span>
        ),
      });
    }
    base.push({
      header: "创建时间",
      size: 180,
      cell: ({ row }) => (
        <span className="text-muted-foreground text-xs">
          {row.original.metadata?.creationTimestamp
            ? formatAbsolute(row.original.metadata.creationTimestamp)
            : "—"}
        </span>
      ),
    });
    base.push({
      id: "actions",
      header: "操作",
      size: 150,
      cell: ({ row }) => (
        <div className="flex justify-end gap-0.5" onClick={(event) => event.stopPropagation()}>
          {readable ? (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setYamlTarget(row.original)}
              aria-label={`查看 ${row.original.metadata?.name ?? ""} 的 YAML`}
            >
              <FileCode />
              YAML
            </Button>
          ) : null}
          {deletable && row.original.metadata?.uid ? (
            <RowDeleteAction
              name={row.original.metadata?.name ?? ""}
              onDelete={() => {
                setDeleteTarget(row.original);
                setDeletePreviewed(false);
                remove.reset();
              }}
            />
          ) : null}
        </div>
      ),
    });
    return base;
  }, [type.namespaced, readable, deletable, remove]);

  if (yamlTarget) {
    const targetNamespace = yamlTarget.metadata?.namespace ?? "";
    return (
      <YamlEditorView
        identity={{
          clusterId,
          group: type.group,
          version: type.version,
          resource: type.resource,
          ...(targetNamespace ? { namespace: targetNamespace } : {}),
          name: yamlTarget.metadata?.name ?? "",
        }}
        clusterName={clusterName}
        kindLabel={type.kind}
        canUpdate={canUpdate && type.verbs.includes("update")}
        onBack={() => setYamlTarget(null)}
      />
    );
  }

  const namespaceNames = (namespaces.data?.namespaces ?? []).map((item) => item.name);
  const nextToken = list.data?.continue_token ?? "";

  return (
    <div className="flex min-h-0 flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-baseline gap-1.5 text-[13px]">
          <span className="text-muted-foreground">API:</span>
          <span className="zke-mono text-muted-foreground">{type.group || "core"}</span>
          <span className="text-subtle-foreground">/</span>
          <span className="zke-mono text-muted-foreground">{type.version}</span>
          <span className="text-subtle-foreground">/</span>
          <span className="text-foreground font-medium">{type.kind}</span>
          {type.custom_resource ? <Badge tone="info">CRD</Badge> : null}
          {type.namespaced ? null : <Badge tone="neutral">集群级</Badge>}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {type.namespaced ? (
            <>
              <span className="text-muted-foreground text-xs">命名空间</span>
              <Select value={namespace} onValueChange={setNamespace}>
                <SelectTrigger className="w-56" aria-label="命名空间">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL_NAMESPACES}>所有命名空间</SelectItem>
                  {namespaceNames.map((name) => (
                    <SelectItem key={name} value={name}>
                      {name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </>
          ) : null}
          <div className="relative">
            <Search className="text-subtle-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
            <Input
              value={nameQuery}
              onChange={(event) => setNameQuery(event.target.value)}
              placeholder="在当前页中筛选名称"
              aria-label="筛选资源名称"
              autoComplete="off"
              spellCheck={false}
              className="w-56 pl-8"
            />
          </div>
          <Button
            size="icon-sm"
            variant="secondary"
            aria-label="刷新"
            disabled={list.isFetching}
            onClick={() => void list.refetch()}
          >
            <RotateCw />
          </Button>
        </div>
      </div>

      {listable ? null : (
        <Alert tone="info">
          该资源类型不支持 list，Kubernetes Discovery 声明的 Verb 中没有它，因此无法在此列出对象。
        </Alert>
      )}

      {listable ? (
        <DataTable
          columns={columns}
          data={rows}
          isLoading={list.isLoading}
          isFetching={list.isFetching}
          error={list.error}
          onRetry={() => void list.refetch()}
          rowKey={(item) =>
            item.metadata?.uid || `${item.metadata?.namespace ?? ""}/${item.metadata?.name ?? ""}`
          }
          emptyTitle={`没有 ${type.kind} 对象`}
          emptyDescription={
            needle
              ? "当前页中没有匹配该名称的对象；名称筛选只作用于已加载的这一页。"
              : type.namespaced && namespace !== ALL_NAMESPACES
                ? `${namespace} 中没有可见的 ${type.kind}。`
                : `该集群中没有当前身份可见的 ${type.kind}。`
          }
          continuePagination={{
            pageIndex: pager.pageIndex,
            nextToken,
            onPrevious: pager.goPrevious,
            onNext: pager.goNext,
          }}
        />
      ) : null}

      <SensitiveActionDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title={`删除 ${type.kind}`}
        description={
          deletePreviewed
            ? "DryRun 已通过。再次确认将提交实际删除。"
            : "首次点击只执行服务端 DryRun；预检通过后才能实际删除。"
        }
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          ...(deleteTarget?.metadata?.namespace
            ? [{ label: "命名空间", name: deleteTarget.metadata.namespace }]
            : []),
          {
            label: type.kind,
            name: deleteTarget?.metadata?.name ?? "",
            id: deleteTarget?.metadata?.uid,
          },
        ]}
        impacts={[
          `将从集群中删除 ${type.group || "core"}/${type.version} 的 ${type.kind} 对象，删除传播策略为 Background。`,
          "该对象的控制器或依赖它的对象可能因此被级联清理；浏览器不解析具体类型的语义，请确认后果。",
          "请求携带该对象当前的 UID 与 resourceVersion 前置条件，期间对象若已变化或被重建，删除会被拒绝。",
        ]}
        confirmationText={deletePreviewed ? (deleteTarget?.metadata?.name ?? "") : undefined}
        confirmLabel={deletePreviewed ? "确认删除" : "执行 DryRun 预检"}
        destructive
        pending={remove.isPending}
        error={remove.error}
        onConfirm={() => {
          const metadata = deleteTarget?.metadata;
          if (!metadata?.name || !metadata.uid || !metadata.resourceVersion) {
            return;
          }
          const dryRun = !deletePreviewed;
          void remove
            .mutateAsync({
              clusterId,
              identity,
              namespace: metadata.namespace ?? "",
              name: metadata.name,
              uid: metadata.uid,
              resourceVersion: metadata.resourceVersion,
              dryRun,
              idempotencyKey: dryRun ? deletePreviewKey : deleteApplyKey,
            })
            .then(() => {
              if (dryRun) {
                setDeletePreviewed(true);
                toast.success("删除 DryRun 已通过");
                return;
              }
              toast.success(`${type.kind} ${metadata.name} 已提交删除`);
              setDeleteTarget(null);
            })
            .catch(() => undefined);
        }}
      />
    </div>
  );
}

function typeKey(type: KubernetesResourceType): string {
  return `${type.group}/${type.version}/${type.resource}`;
}

function sameType(left: KubernetesResourceType, right: KubernetesResourceType): boolean {
  return typeKey(left) === typeKey(right);
}
