import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { FileCode, Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { errorCode } from "@/api/errors";
import {
  useDeleteNetworkingResource,
  useNetworkingResource,
  useNetworkingResources,
  type NetworkingSummary,
} from "@/api/queries/networking";
import type { KubernetesNetworkingResource } from "@/api/types";
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
import { AddressValues, CopyableValue } from "@/components/common/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/misc";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { NetworkingFormView } from "./NetworkingFormView";
import { useContinuePagination } from "./use-continue-pagination";
import type { ClusterSectionProps } from "./types";
import { YamlEditorView } from "./YamlEditorView";
import {
  GATEWAY_UNAVAILABLE_CODE,
  NETWORKING_TYPES,
  networkingIdentity,
  networkingKindLabel,
} from "./networking-catalog";

const PAGE_SIZE = 50;

type NetworkingSectionProps = ClusterSectionProps & {
  /** The Namespace every query and mutation in this section is scoped to. */
  namespace: string;
};

/**
 * Services, Ingresses and Gateways of one Namespace of one Cluster.
 *
 * Each type has its own shape, so each gets its own columns and its own detail
 * cards rather than a lowest-common-denominator table that would show a Service
 * and a Gateway as if they were the same thing.
 */
export function NetworkingSection({
  clusterId,
  clusterName,
  namespace,
  tenantId,
  projectId,
}: NetworkingSectionProps) {
  const { permissions } = useSessionContext();
  const [resource, setResource] = useState<KubernetesNetworkingResource>("services");
  const pager = useContinuePagination(`${clusterId}/${namespace}/${resource}`);
  const list = useNetworkingResources(clusterId, namespace, resource, {
    limit: PAGE_SIZE,
    ...(pager.token ? { continue: pager.token } : {}),
  });
  const [detailName, setDetailName] = useState<string | null>(null);
  const [yamlName, setYamlName] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<NetworkingSummary | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<NetworkingSummary | null>(null);
  const [deletePreviewed, setDeletePreviewed] = useState(false);
  const deletePreviewKey = useSubmissionKey(deleteTarget !== null);
  const deleteApplyKey = useSubmissionKey(deleteTarget !== null);
  const remove = useDeleteNetworkingResource();

  const projectScope = { type: "project" as const, tenantId, projectId };
  const canCreate = permissions.can("cluster.resource.create", projectScope);
  const canUpdate = permissions.can("cluster.resource.update", projectScope);
  const canDelete = permissions.can("cluster.resource.delete", projectScope);

  const columns = useMemo<ColumnDef<NetworkingSummary, unknown>[]>(
    () => [
      {
        header: "名称",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground font-medium">{row.original.name}</span>
            <span className="zke-mono text-subtle-foreground text-xs">
              {row.original.api_version}
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
    [resource, canUpdate, canDelete, remove],
  );

  if (yamlName) {
    return (
      <YamlEditorView
        identity={{ clusterId, ...networkingIdentity(resource), namespace, name: yamlName }}
        clusterName={clusterName}
        kindLabel={networkingKindLabel(resource)}
        canUpdate={canUpdate}
        onBack={() => setYamlName(null)}
      />
    );
  }

  // The form takes over the section rather than sitting over the list: a
  // Gateway's listeners or an Ingress's routes are taller than a box laid over
  // the table can show, and the table is of no use while they are being filled
  // in.
  if (creating || editing) {
    return (
      <NetworkingFormView
        clusterId={clusterId}
        clusterName={clusterName}
        namespace={namespace}
        resource={resource}
        existing={editing}
        onClose={() => {
          setCreating(false);
          setEditing(null);
        }}
      />
    );
  }

  if (detailName) {
    return (
      <NetworkingDetailView
        clusterId={clusterId}
        namespace={namespace}
        resource={resource}
        name={detailName}
        canUpdate={canUpdate}
        // The detail stays open underneath, so leaving the form returns to the
        // object that was being read rather than to the list — the same way the
        // YAML view below returns here.
        onEdit={setEditing}
        onOpenYaml={() => setYamlName(detailName)}
        onBack={() => setDetailName(null)}
      />
    );
  }

  const gatewayMissing =
    resource === "gateways" && errorCode(list.error) === GATEWAY_UNAVAILABLE_CODE;
  const nextToken = list.data?.continue_token ?? "";

  return (
    <div className="flex h-full min-h-0 flex-col">
      <SectionToolbarActions>
        <RefreshAction isFetching={list.isFetching} onRefresh={() => void list.refetch()} />
        {canCreate && !gatewayMissing ? (
          <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
            <Plus />
            创建 {networkingKindLabel(resource)}
          </Button>
        ) : null}
      </SectionToolbarActions>
      <Tabs
        value={resource}
        onValueChange={(value) => {
          setResource(value as KubernetesNetworkingResource);
          setDetailName(null);
        }}
        className="flex min-h-0 flex-1 flex-col"
      >
        <TabsList className="w-fit">
          {NETWORKING_TYPES.map((type) => (
            <TabsTrigger key={type.resource} value={type.resource}>
              {type.label}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value={resource} className="flex min-h-0 flex-1 flex-col">
          {gatewayMissing ? (
            <Alert tone="info" className="flex items-center justify-between gap-3">
              <span>
                该集群没有安装 Gateway API（
                <code className="zke-mono">gateway.networking.k8s.io/v1</code>）。ZKE
                不会自动安装它；需要由集群管理员先部署 Gateway API CRD 与 Controller。
              </span>
              <Button size="sm" variant="secondary" onClick={() => void list.refetch()}>
                重新检测
              </Button>
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
              emptyTitle={`该命名空间没有 ${networkingKindLabel(resource)}`}
              emptyDescription={`${namespace} 中没有可见的 ${networkingKindLabel(resource)}。`}
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

      <SensitiveActionDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title={`删除 ${networkingKindLabel(resource)}`}
        description={
          deletePreviewed
            ? "DryRun 已通过。再次确认将提交实际删除。"
            : "首次点击只执行服务端 DryRun；预检通过后才能实际删除。"
        }
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          {
            label: networkingKindLabel(resource),
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
                toast.success("删除 DryRun 已通过");
                return;
              }
              toast.success(`${networkingKindLabel(resource)} ${deleteTarget.name} 已提交删除`);
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

function deleteImpacts(resource: KubernetesNetworkingResource): string[] {
  const shared = [
    "请求携带该对象当前的 UID 与 resourceVersion 前置条件，期间对象若已变化或被重建，删除会被拒绝。",
  ];
  if (resource === "services") {
    return [
      "删除后该 Service 提供的 DNS 与访问入口会失效，指向它的流量将中断。",
      "已经分配的 ClusterIP 或 NodePort 会被释放；重新创建时可能获得不同的地址和端口。",
      ...shared,
    ];
  }
  if (resource === "ingresses") {
    return ["删除后 Ingress Controller 会撤销对应的对外路由规则。", ...shared];
  }
  return ["删除后该 Gateway 的监听器会被撤销，绑定到它的 Route 将失去接入点。", ...shared];
}

/** The columns that only make sense for one type. */
function typeColumns(
  resource: KubernetesNetworkingResource,
): ColumnDef<NetworkingSummary, unknown>[] {
  if (resource === "services") {
    return [
      {
        header: "类型",
        size: 130,
        cell: ({ row }) => {
          const view = row.original.service;
          return (
            <div className="flex flex-col items-start gap-0.5">
              <Badge tone="info">{view?.spec.type || "ClusterIP"}</Badge>
              {view?.spec.headless ? <Badge tone="neutral">Headless</Badge> : null}
            </div>
          );
        },
      },
      {
        header: "ClusterIP",
        size: 140,
        cell: ({ row }) => (
          <AddressValues
            values={row.original.service?.cluster_ips ?? []}
            className="text-muted-foreground"
          />
        ),
      },
      {
        header: "端口",
        cell: ({ row }) => (
          <span className="zke-mono text-muted-foreground text-xs break-all">
            {(row.original.service?.spec.ports ?? [])
              .map(
                (port) =>
                  `${port.port}${port.node_port ? `:${port.node_port}` : ""}/${port.protocol || "TCP"}`,
              )
              .join(" · ") || "—"}
          </span>
        ),
      },
    ];
  }
  if (resource === "ingresses") {
    return [
      {
        header: "IngressClass",
        size: 140,
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs">
            {row.original.ingress?.spec.ingress_class_name || "默认"}
          </span>
        ),
      },
      {
        header: "主机",
        cell: ({ row }) => {
          const hosts = (row.original.ingress?.spec.rules ?? [])
            .map((rule) => rule.host)
            .filter(Boolean);
          return (
            <span className="zke-mono text-muted-foreground text-xs break-all">
              {hosts.length > 0 ? hosts.join(" · ") : "任意主机"}
            </span>
          );
        },
      },
      {
        header: "地址",
        size: 160,
        cell: ({ row }) => (
          <AddressValues
            values={addressValues(row.original.ingress?.load_balancer_ingress)}
            empty="尚未分配"
            className="text-muted-foreground"
          />
        ),
      },
    ];
  }
  return [
    {
      header: "GatewayClass",
      size: 150,
      cell: ({ row }) => (
        <span className="text-muted-foreground text-xs break-all">
          {row.original.gateway?.spec.gateway_class_name || "—"}
        </span>
      ),
    },
    {
      header: "监听器",
      cell: ({ row }) => (
        <span className="zke-mono text-muted-foreground text-xs break-all">
          {(row.original.gateway?.spec.listeners ?? [])
            .map((listener) => `${listener.name}:${listener.port}/${listener.protocol}`)
            .join(" · ") || "—"}
        </span>
      ),
    },
    {
      header: "地址",
      size: 160,
      cell: ({ row }) => (
        <AddressValues
          values={(row.original.gateway?.addresses ?? []).map((address) => address.value)}
          empty="尚未分配"
          className="text-muted-foreground"
        />
      ),
    },
  ];
}

/** A LoadBalancer ingress entry carries either an IP or a hostname. */
function addressValues(entries: { ip: string; hostname: string }[] | undefined): string[] {
  return (entries ?? []).map((entry) => entry.ip || entry.hostname).filter(Boolean);
}

function NetworkingDetailView({
  clusterId,
  namespace,
  resource,
  name,
  canUpdate,
  onEdit,
  onOpenYaml,
  onBack,
}: {
  clusterId: string;
  namespace: string;
  resource: KubernetesNetworkingResource;
  name: string;
  canUpdate: boolean;
  onEdit: (item: NetworkingSummary) => void;
  onOpenYaml: () => void;
  onBack: () => void;
}) {
  const detail = useNetworkingResource(clusterId, namespace, resource, name);
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
            <DetailRow label="命名空间" value={item.namespace} />
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

          {item.service ? <ServiceCards view={item.service} /> : null}
          {item.ingress ? <IngressCards view={item.ingress} /> : null}
          {item.gateway ? <GatewayCards view={item.gateway} /> : null}

          <DetailCard title="标签">
            <DetailKeyValues entries={item.labels} />
          </DetailCard>
          <DetailCard title="注解">
            <DetailKeyValues entries={item.annotations} />
          </DetailCard>
        </div>
      )}
    </div>
  );
}

function ServiceCards({ view }: { view: NonNullable<NetworkingSummary["service"]> }) {
  // What an operator pastes into a shell is an address and a port together, so
  // the port copies the pair when the Service has an address to pair it with.
  // A headless Service has none — `cluster_ips` is `None` — and no cluster
  // domain is assumed in its place: the DNS name a headless Service is reached
  // by depends on the cluster's own domain, which this response does not carry.
  const address = view.cluster_ips.find((value) => value !== "" && value !== "None");

  return (
    <>
      <DetailCard title="Service">
        <DetailRow label="类型" value={view.spec.type || "ClusterIP"} />
        <DetailRow label="Headless" value={view.spec.headless ? "是" : "否"} />
        <DetailRow label="ClusterIP" value={<AddressValues values={view.cluster_ips} />} />
        <DetailRow label="IP 协议族" value={view.ip_families.join(", ") || "—"} />
        <DetailRow label="IP 分配策略" value={view.ip_family_policy || "—"} />
        {view.spec.external_name ? (
          <DetailRow label="ExternalName" value={view.spec.external_name} />
        ) : null}
        <DetailRow label="会话保持" value={view.spec.session_affinity || "None"} />
        <DetailRow label="外部流量策略" value={view.spec.external_traffic_policy || "—"} />
        <DetailRow label="内部流量策略" value={view.spec.internal_traffic_policy || "—"} />
        <DetailRow
          label="LoadBalancer"
          value={<AddressValues values={addressValues(view.load_balancer_ingress)} />}
        />
      </DetailCard>

      <DetailCard title="端口">
        {view.spec.ports.length === 0 ? (
          <DetailRow label="端口" value="—" />
        ) : (
          view.spec.ports.map((port) => (
            <DetailRow
              key={`${port.port}/${port.protocol}/${port.name}`}
              label={port.name || String(port.port)}
              value={
                <span className="zke-mono text-xs break-all">
                  <CopyableValue
                    value={address ? `${address}:${port.port}` : String(port.port)}
                    className="zke-mono text-xs"
                  >
                    {port.port}
                  </CopyableValue>{" "}
                  → {port.target_port || port.port} / {port.protocol || "TCP"}
                  {port.node_port ? (
                    <>
                      {" · NodePort "}
                      <CopyableValue value={String(port.node_port)} className="zke-mono text-xs" />
                    </>
                  ) : null}
                  {port.app_protocol ? ` · ${port.app_protocol}` : ""}
                </span>
              }
            />
          ))
        )}
      </DetailCard>

      <DetailCard title="选择器">
        <DetailKeyValues entries={view.spec.selector} />
      </DetailCard>
    </>
  );
}

function IngressCards({ view }: { view: NonNullable<NetworkingSummary["ingress"]> }) {
  return (
    <>
      <DetailCard title="Ingress">
        <DetailRow label="IngressClass" value={view.spec.ingress_class_name || "默认"} />
        <DetailRow
          label="默认后端"
          value={view.spec.default_backend ? backendLabel(view.spec.default_backend) : "—"}
        />
        <DetailRow
          label="地址"
          value={
            <AddressValues values={addressValues(view.load_balancer_ingress)} empty="尚未分配" />
          }
        />
      </DetailCard>

      <DetailCard title="规则">
        {view.spec.rules.length === 0 ? (
          <DetailRow label="规则" value="—" />
        ) : (
          view.spec.rules.map((rule, ruleIndex) => (
            <DetailRow
              key={`${rule.host}/${ruleIndex}`}
              label={rule.host || "任意主机"}
              value={
                <div className="grid gap-0.5">
                  {rule.paths.map((path, pathIndex) => (
                    <span key={`${path.path}/${pathIndex}`} className="zke-mono text-xs break-all">
                      {path.path || "/"} [{path.path_type}] → {backendLabel(path.backend)}
                    </span>
                  ))}
                </div>
              }
            />
          ))
        )}
      </DetailCard>

      {view.spec.tls.length > 0 ? (
        <DetailCard title="TLS">
          {view.spec.tls.map((entry, index) => (
            <DetailRow
              key={`${entry.secret_name}/${index}`}
              label={entry.secret_name || "未指定 Secret"}
              value={
                <span className="zke-mono text-xs break-all">{entry.hosts.join(", ") || "—"}</span>
              }
            />
          ))}
        </DetailCard>
      ) : null}
    </>
  );
}

function backendLabel(backend: { name: string; port_name: string; port_number: number }): string {
  const port = backend.port_name || (backend.port_number ? String(backend.port_number) : "");
  return port ? `${backend.name}:${port}` : backend.name;
}

function GatewayCards({ view }: { view: NonNullable<NetworkingSummary["gateway"]> }) {
  const statusByListener = new Map(view.listeners.map((entry) => [entry.name, entry]));
  return (
    <>
      <DetailCard title="Gateway">
        <DetailRow label="GatewayClass" value={view.spec.gateway_class_name || "—"} />
        <DetailRow
          label="地址"
          value={
            <AddressValues
              values={view.addresses.map((address) => address.value)}
              empty="尚未分配"
            />
          }
        />
      </DetailCard>

      <DetailCard title="监听器">
        {view.spec.listeners.length === 0 ? (
          <DetailRow label="监听器" value="—" />
        ) : (
          view.spec.listeners.map((listener) => {
            const status = statusByListener.get(listener.name);
            return (
              <DetailRow
                key={listener.name}
                label={listener.name}
                value={
                  <div className="grid gap-0.5">
                    <span className="zke-mono text-xs break-all">
                      {listener.hostname || "任意主机"} · {listener.port}/{listener.protocol}
                      {listener.tls ? ` · TLS ${listener.tls.mode}` : ""}
                    </span>
                    <span className="text-subtle-foreground text-xs">
                      允许来自 {listener.allowed_routes.namespaces_from || "Same"} 命名空间的 Route
                      {status ? ` · 已绑定 ${status.attached_routes} 条` : ""}
                    </span>
                  </div>
                }
              />
            );
          })
        )}
      </DetailCard>

      <DetailCard title="状态">
        <DetailConditions conditions={view.conditions} />
      </DetailCard>
    </>
  );
}
