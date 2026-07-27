import { useMemo, useState, type ReactNode } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Globe2, KeyRound, RefreshCw, Server, ServerCog } from "lucide-react";
import { toast } from "sonner";

import { newIdempotencyKey } from "@/api/client";
import {
  useCluster,
  useClusterOverview,
  useClusters,
  useDeleteCluster,
  useReenrollCluster,
  useRevokeClusterConnection,
  useUpdateCluster,
  type ClusterOverviewEntry,
} from "@/api/queries/clusters";
import {
  useClusterEnrollments,
  useCreateClusterEnrollment,
  useCreateClusterInstallation,
  useRevokeClusterEnrollment,
} from "@/api/queries/enrollments";
import {
  DEFAULT_PAGE_SIZE,
  type ClusterAggregate,
  type ClusterEnrollmentRecord,
  type ScopeSelection,
} from "@/api/types";
import { AppShell, ScopeRequired, SectionTitle, type AppNavItem } from "@/apps/AppShell";
import type { AppComponentProps } from "@/apps/types";
import { useSessionContext } from "@/auth/session-context";
import { useScopeStore } from "@/scope/scope-store";
import { DataTable } from "@/components/common/data-table";
import { SecretReveal } from "@/components/common/secret-reveal";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import {
  AbsoluteTime,
  IdentifierLabel,
  RelativeTime,
  StatusBadge,
} from "@/components/common/status";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { FieldHint, Label } from "@/components/ui/label";
import { Alert, Card, CardTitle, Separator } from "@/components/ui/misc";
import { formatDuration } from "@/lib/time";

const NAV: AppNavItem[] = [
  { id: "overview", label: "全局概览", icon: Globe2 },
  { id: "clusters", label: "集群", icon: Server },
  { id: "enrollments", label: "接入凭证", icon: KeyRound },
  { id: "detail", label: "集群详情", icon: ServerCog },
];

export function ClusterAccessApp(_props: AppComponentProps) {
  const scope = useScopeStore((state) => state.scope);
  const setScope = useScopeStore((state) => state.setScope);
  const [section, setSection] = useState(scope.projectId ? "clusters" : "overview");
  // Which Cluster the detail view shows is navigation state, not an
  // authorization scope: Clusters live inside a Project and carry no
  // RoleBinding of their own.
  const [clusterId, setClusterId] = useState<string | null>(null);

  return (
    <AppShell nav={NAV} activeId={section} onNavigate={setSection}>
      {section === "overview" ? (
        <OverviewSection
          onSelect={(entry) => {
            setScope({
              tenantId: entry.tenantId,
              tenantName: entry.tenantName,
              projectId: entry.projectId,
              projectName: entry.projectName,
            });
            setClusterId(entry.cluster.id);
            setSection("detail");
          }}
        />
      ) : null}

      {section === "clusters" ? (
        <ClusterSection
          scope={scope}
          onSelect={(cluster) => {
            setClusterId(cluster.id);
            setSection("detail");
          }}
        />
      ) : null}

      {section === "enrollments" ? <EnrollmentSection scope={scope} /> : null}

      {section === "detail" ? (
        <ClusterDetailSection clusterId={clusterId} onBack={() => setSection("clusters")} />
      ) : null}
    </AppShell>
  );
}

function OverviewSection({ onSelect }: { onSelect: (entry: ClusterOverviewEntry) => void }) {
  const overview = useClusterOverview();

  const columns = useMemo<ColumnDef<ClusterOverviewEntry, unknown>[]>(
    () => [
      {
        header: "Cluster",
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="text-foreground font-medium">{row.original.cluster.name}</span>
            <IdentifierLabel value={row.original.cluster.id} />
          </div>
        ),
      },
      {
        header: "作用域",
        cell: ({ row }) => (
          <span className="text-muted-foreground text-[13px]">
            {row.original.tenantName} › {row.original.projectName}
          </span>
        ),
      },
      {
        header: "接入状态",
        size: 110,
        cell: ({ row }) => <StatusBadge kind="cluster" value={row.original.cluster.status} />,
      },
      {
        header: "连接",
        size: 110,
        cell: ({ row }) => (
          <StatusBadge kind="connection" value={row.original.cluster.connection.status} />
        ),
      },
      {
        header: "证书",
        size: 140,
        cell: ({ row }) => (
          <StatusBadge
            kind="certificate"
            value={row.original.cluster.connection.certificate_status}
          />
        ),
      },
      {
        header: "最近在线",
        size: 130,
        cell: ({ row }) => <RelativeTime value={row.original.cluster.connection.last_seen_at} />,
      },
    ],
    [],
  );

  return (
    <>
      <SectionTitle
        title="全局集群概览"
        description="按当前权限范围聚合 Tenant → Project → Cluster；Server 暂未提供跨 Project 的集群列表接口"
        actions={
          <Button size="sm" variant="secondary" onClick={() => void overview.refetch()}>
            <RefreshCw />
            刷新
          </Button>
        }
      />

      {overview.data?.truncated ? (
        <Alert tone="warning" className="mb-3">
          结果已截断：可见资源数量超过单次聚合上限，列表并不完整。请使用 Project
          视图查看完整集群列表。
        </Alert>
      ) : null}

      {overview.data && overview.data.failures.length > 0 ? (
        <Alert tone="warning" className="mb-3">
          以下范围聚合失败，结果不完整：
          <ul className="mt-1 list-disc pl-5">
            {overview.data.failures.map((failure) => (
              <li key={failure.scope}>
                {failure.scope}：{failure.message}
              </li>
            ))}
          </ul>
        </Alert>
      ) : null}

      <DataTable
        columns={columns}
        data={overview.data?.entries}
        isLoading={overview.isLoading}
        isFetching={overview.isFetching}
        error={overview.error}
        onRetry={() => void overview.refetch()}
        rowKey={(row) => row.cluster.id}
        onRowClick={onSelect}
        emptyTitle="没有可见的集群"
        emptyDescription="当前账号的权限范围内还没有接入任何 Kubernetes 集群。"
      />
    </>
  );
}

function ClusterSection({
  scope,
  onSelect,
}: {
  scope: ScopeSelection;
  onSelect: (cluster: ClusterAggregate) => void;
}) {
  const { permissions } = useSessionContext();
  const [search, setSearch] = useState("");
  const [offset, setOffset] = useState(0);
  const [renameTarget, setRenameTarget] = useState<ClusterAggregate | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [revokeTarget, setRevokeTarget] = useState<ClusterAggregate | null>(null);
  const [retireTarget, setRetireTarget] = useState<ClusterAggregate | null>(null);
  const [reenrollTarget, setReenrollTarget] = useState<ClusterAggregate | null>(null);
  const [reenrollResult, setReenrollResult] = useState<{ token: string; expiresAt: string } | null>(
    null,
  );

  const query = useClusters(scope.projectId, {
    limit: DEFAULT_PAGE_SIZE,
    offset,
    ...(search ? { q: search } : {}),
  });
  const updateCluster = useUpdateCluster();
  const deleteCluster = useDeleteCluster();
  const revokeConnection = useRevokeClusterConnection();
  const reenroll = useReenrollCluster();

  const projectScope = {
    type: "project" as const,
    tenantId: scope.tenantId,
    projectId: scope.projectId,
  };
  const canManage = permissions.can("cluster.manage", projectScope);
  const canRevoke = permissions.can("cluster.connection.revoke", projectScope);
  const canEnroll = permissions.can("cluster.enrollment.create", projectScope);

  const columns = useMemo<ColumnDef<ClusterAggregate, unknown>[]>(
    () => [
      {
        header: "名称",
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="text-foreground font-medium">{row.original.name}</span>
            <IdentifierLabel value={row.original.id} />
          </div>
        ),
      },
      {
        header: "接入状态",
        size: 100,
        cell: ({ row }) => <StatusBadge kind="cluster" value={row.original.status} />,
      },
      {
        header: "连接",
        size: 100,
        cell: ({ row }) => <StatusBadge kind="connection" value={row.original.connection.status} />,
      },
      {
        header: "证书剩余",
        size: 140,
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <StatusBadge kind="certificate" value={row.original.connection.certificate_status} />
            <span className="text-subtle-foreground text-xs">
              {formatDuration(row.original.connection.certificate_remaining_seconds)}
            </span>
          </div>
        ),
      },
      {
        header: "Agent 版本",
        size: 120,
        cell: ({ row }) => (
          <span className="zke-mono text-muted-foreground text-xs">
            {row.original.connection.version || "—"}
          </span>
        ),
      },
      {
        id: "actions",
        header: "",
        size: 300,
        cell: ({ row }) => {
          const cluster = row.original;
          return (
            <div className="flex flex-wrap justify-end gap-1">
              <Button size="sm" variant="ghost" onClick={() => onSelect(cluster)}>
                详情
              </Button>
              {canManage ? (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => {
                    setRenameTarget(cluster);
                    setRenameValue(cluster.name);
                  }}
                >
                  重命名
                </Button>
              ) : null}
              {canRevoke && cluster.connection.certificate_status !== "revoked" ? (
                <Button size="sm" variant="ghost" onClick={() => setRevokeTarget(cluster)}>
                  撤销连接
                </Button>
              ) : null}
              {canEnroll ? (
                <Button size="sm" variant="ghost" onClick={() => setReenrollTarget(cluster)}>
                  重新接入
                </Button>
              ) : null}
              {canManage ? (
                <Button
                  size="sm"
                  variant="ghost"
                  className="text-danger"
                  onClick={() => setRetireTarget(cluster)}
                >
                  退役
                </Button>
              ) : null}
            </div>
          );
        },
      },
    ],
    [canEnroll, canManage, canRevoke, onSelect],
  );

  if (!scope.projectId) {
    return <ScopeRequired />;
  }

  return (
    <>
      <SectionTitle
        title={`集群 · ${scope.projectName ?? scope.projectId}`}
        description="Cluster 与其中的 ZKE Agent 是同一个管理共同体，操作以 cluster_id 为目标"
      />

      <DataTable
        columns={columns}
        data={query.data?.clusters}
        isLoading={query.isLoading}
        isFetching={query.isFetching}
        error={query.error}
        onRetry={() => void query.refetch()}
        rowKey={(row) => row.id}
        emptyTitle="该 Project 还没有集群"
        emptyDescription="可在「接入凭证」中创建一次性凭证或一键安装命令，让集群中的 ZKE Agent 主动接入。"
        toolbar={
          <Input
            className="max-w-56"
            placeholder="按名称搜索"
            value={search}
            onChange={(event) => {
              setSearch(event.target.value);
              setOffset(0);
            }}
          />
        }
        pagination={{ value: query.data?.pagination, onOffsetChange: setOffset }}
      />

      <Dialog open={Boolean(renameTarget)} onOpenChange={(open) => !open && setRenameTarget(null)}>
        <DialogContent aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>重命名 Cluster</DialogTitle>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="cluster-name">名称</Label>
            <Input
              id="cluster-name"
              value={renameValue}
              maxLength={253}
              onChange={(event) => setRenameValue(event.target.value)}
            />
            <FieldHint>名称仅用于展示，集群身份始终由 cluster_id 决定。</FieldHint>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRenameTarget(null)}>
              取消
            </Button>
            <Button
              variant="primary"
              disabled={updateCluster.isPending || renameValue.trim().length === 0}
              onClick={async () => {
                if (!renameTarget) {
                  return;
                }
                try {
                  await updateCluster.mutateAsync({
                    clusterId: renameTarget.id,
                    name: renameValue.trim(),
                  });
                  toast.success("Cluster 已重命名");
                  setRenameTarget(null);
                } catch {
                  toast.error("重命名失败");
                }
              }}
            >
              确认
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <SensitiveActionDialog
        open={Boolean(revokeTarget)}
        onOpenChange={(open) => !open && setRevokeTarget(null)}
        title="撤销集群连接身份"
        destructive
        description="撤销后当前 Agent 证书立即失效，连接会被服务端关闭。"
        scopeLines={[
          { label: "Tenant", name: scope.tenantName ?? "", id: scope.tenantId },
          { label: "Project", name: scope.projectName ?? "", id: scope.projectId },
          { label: "Cluster", name: revokeTarget?.name ?? "", id: revokeTarget?.id },
        ]}
        impacts={[
          "该集群当前 Agent 的客户端证书被撤销",
          "已建立的 QUIC/mTLS 连接立即断开",
          "集群将保持离线，直到使用新凭证重新接入",
          "操作写入安全审计记录",
        ]}
        confirmationText={revokeTarget?.name}
        confirmLabel="确认撤销"
        pending={revokeConnection.isPending}
        error={revokeConnection.error}
        onConfirm={async () => {
          if (!revokeTarget) {
            return;
          }
          try {
            const result = await revokeConnection.mutateAsync({ clusterId: revokeTarget.id });
            toast.success(result.already_revoked ? "该连接身份此前已被撤销" : "连接身份已撤销");
            setRevokeTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />

      <SensitiveActionDialog
        open={Boolean(retireTarget)}
        onOpenChange={(open) => !open && setRetireTarget(null)}
        title="退役 Cluster"
        destructive
        scopeLines={[
          { label: "Tenant", name: scope.tenantName ?? "", id: scope.tenantId },
          { label: "Project", name: scope.projectName ?? "", id: scope.projectId },
          { label: "Cluster", name: retireTarget?.name ?? "", id: retireTarget?.id },
        ]}
        impacts={[
          "Cluster 标记为已退役，不再接受接入",
          "内部 Agent 身份与未使用的接入凭证被撤销",
          "已连接的 Agent 立即断开",
          "操作写入审计记录，且不可自动回滚",
        ]}
        confirmationText={retireTarget?.name}
        confirmLabel="确认退役"
        pending={deleteCluster.isPending}
        error={deleteCluster.error}
        onConfirm={async () => {
          if (!retireTarget) {
            return;
          }
          try {
            await deleteCluster.mutateAsync({ clusterId: retireTarget.id });
            toast.success("Cluster 已退役");
            setRetireTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />

      <SensitiveActionDialog
        open={Boolean(reenrollTarget)}
        onOpenChange={(open) => {
          if (!open) {
            setReenrollTarget(null);
          }
        }}
        title="为集群签发新的接入凭证"
        description="用于集群重新接入：复用原 cluster_id，创建新的内部 Agent 身份。"
        scopeLines={[
          { label: "Project", name: scope.projectName ?? "", id: scope.projectId },
          { label: "Cluster", name: reenrollTarget?.name ?? "", id: reenrollTarget?.id },
        ]}
        impacts={[
          "生成一枚一次性接入凭证，只在本次响应中明文返回",
          "原有接入身份在新 Agent 完成注册后失效",
          "凭证在有效期内可被任何持有者使用，请通过安全渠道传递",
        ]}
        confirmLabel="签发凭证"
        pending={reenroll.isPending}
        error={reenroll.error}
        onConfirm={async () => {
          if (!reenrollTarget) {
            return;
          }
          try {
            const result = await reenroll.mutateAsync({
              clusterId: reenrollTarget.id,
              idempotencyKey: newIdempotencyKey(),
            });
            setReenrollResult({ token: result.token, expiresAt: result.expires_at });
            setReenrollTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />

      <Dialog
        open={Boolean(reenrollResult)}
        onOpenChange={(open) => !open && setReenrollResult(null)}
      >
        <DialogContent aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>一次性接入凭证</DialogTitle>
          </DialogHeader>
          {reenrollResult ? (
            <SecretReveal
              label="Enrollment Token"
              value={reenrollResult.token}
              hint={`有效期至 ${reenrollResult.expiresAt}。`}
            />
          ) : null}
          <DialogFooter>
            <Button variant="primary" onClick={() => setReenrollResult(null)}>
              我已安全保存
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function EnrollmentSection({ scope }: { scope: ScopeSelection }) {
  const { permissions } = useSessionContext();
  const [offset, setOffset] = useState(0);
  const [createOpen, setCreateOpen] = useState(false);
  const [installOpen, setInstallOpen] = useState(false);
  const [clusterName, setClusterName] = useState("");
  const [tokenResult, setTokenResult] = useState<{ token: string; expiresAt: string } | null>(null);
  const [installResult, setInstallResult] = useState<{
    command: string;
    manifestUrl: string;
    expiresAt: string;
  } | null>(null);
  const [revokeTarget, setRevokeTarget] = useState<ClusterEnrollmentRecord | null>(null);

  const query = useClusterEnrollments(scope.projectId, { limit: DEFAULT_PAGE_SIZE, offset });
  const createEnrollment = useCreateClusterEnrollment();
  const createInstallation = useCreateClusterInstallation();
  const revokeEnrollment = useRevokeClusterEnrollment();

  const projectScope = {
    type: "project" as const,
    tenantId: scope.tenantId,
    projectId: scope.projectId,
  };
  const canCreate = permissions.can("cluster.enrollment.create", projectScope);
  const canRevoke = permissions.can("cluster.enrollment.revoke", projectScope);

  const columns = useMemo<ColumnDef<ClusterEnrollmentRecord, unknown>[]>(
    () => [
      {
        header: "集群名称",
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="text-foreground font-medium">{row.original.cluster_name}</span>
            <IdentifierLabel value={row.original.id} />
          </div>
        ),
      },
      {
        header: "状态",
        size: 100,
        cell: ({ row }) => <StatusBadge kind="enrollment" value={row.original.status} />,
      },
      {
        header: "过期时间",
        size: 170,
        cell: ({ row }) => <AbsoluteTime value={row.original.expires_at} />,
      },
      {
        header: "创建时间",
        size: 130,
        cell: ({ row }) => <RelativeTime value={row.original.created_at} />,
      },
      {
        id: "actions",
        header: "",
        size: 100,
        cell: ({ row }) =>
          canRevoke && row.original.status === "active" ? (
            <div className="flex justify-end">
              <Button
                size="sm"
                variant="ghost"
                className="text-danger"
                onClick={() => setRevokeTarget(row.original)}
              >
                撤销
              </Button>
            </div>
          ) : null,
      },
    ],
    [canRevoke],
  );

  if (!scope.projectId) {
    return <ScopeRequired />;
  }

  return (
    <>
      <SectionTitle
        title="接入凭证"
        description="一次性凭证由集群内的 ZKE Agent 使用，Agent 主动连接 Server 完成注册"
        actions={
          canCreate ? (
            <>
              <Button
                size="sm"
                variant="secondary"
                onClick={() => {
                  setClusterName("");
                  setInstallOpen(true);
                }}
              >
                一键安装命令
              </Button>
              <Button
                size="sm"
                variant="primary"
                onClick={() => {
                  setClusterName("");
                  setCreateOpen(true);
                }}
              >
                <KeyRound />
                创建凭证
              </Button>
            </>
          ) : null
        }
      />

      <DataTable
        columns={columns}
        data={query.data?.cluster_enrollments}
        isLoading={query.isLoading}
        isFetching={query.isFetching}
        error={query.error}
        onRetry={() => void query.refetch()}
        rowKey={(row) => row.id}
        emptyTitle="没有接入凭证"
        emptyDescription="创建一次性凭证后，在目标集群部署 ZKE Agent 即可完成接入。"
        pagination={{ value: query.data?.pagination, onOffsetChange: setOffset }}
      />

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>创建一次性接入凭证</DialogTitle>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="enrollment-cluster-name">集群名称</Label>
            <Input
              id="enrollment-cluster-name"
              value={clusterName}
              maxLength={253}
              autoFocus
              onChange={(event) => setClusterName(event.target.value)}
            />
            <FieldHint>名称会绑定到凭证；Agent 注册成功后即以该名称创建 Cluster。</FieldHint>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setCreateOpen(false)}>
              取消
            </Button>
            <Button
              variant="primary"
              disabled={createEnrollment.isPending || clusterName.trim().length === 0}
              onClick={async () => {
                try {
                  const result = await createEnrollment.mutateAsync({
                    projectId: scope.projectId as string,
                    clusterName: clusterName.trim(),
                    idempotencyKey: newIdempotencyKey(),
                  });
                  setCreateOpen(false);
                  setTokenResult({ token: result.token, expiresAt: result.expires_at });
                } catch {
                  toast.error("创建接入凭证失败");
                }
              }}
            >
              {createEnrollment.isPending ? "创建中…" : "创建"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={installOpen} onOpenChange={setInstallOpen}>
        <DialogContent aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>生成一键安装命令</DialogTitle>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="installation-cluster-name">集群名称</Label>
            <Input
              id="installation-cluster-name"
              value={clusterName}
              maxLength={253}
              autoFocus
              onChange={(event) => setClusterName(event.target.value)}
            />
            <FieldHint>
              安装命令中包含一次性安装凭证，等同于接入凭证，必须按机密信息处理。
            </FieldHint>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setInstallOpen(false)}>
              取消
            </Button>
            <Button
              variant="primary"
              disabled={createInstallation.isPending || clusterName.trim().length === 0}
              onClick={async () => {
                try {
                  const result = await createInstallation.mutateAsync({
                    projectId: scope.projectId as string,
                    clusterName: clusterName.trim(),
                    idempotencyKey: newIdempotencyKey(),
                  });
                  setInstallOpen(false);
                  setInstallResult({
                    command: result.install_command,
                    manifestUrl: result.manifest_url,
                    expiresAt: result.expires_at,
                  });
                } catch {
                  toast.error("生成安装命令失败；请确认 Server 已启用一键安装并配置公网入口");
                }
              }}
            >
              {createInstallation.isPending ? "生成中…" : "生成"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(tokenResult)} onOpenChange={(open) => !open && setTokenResult(null)}>
        <DialogContent aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>一次性接入凭证</DialogTitle>
          </DialogHeader>
          {tokenResult ? (
            <SecretReveal
              label="Enrollment Token"
              value={tokenResult.token}
              hint={`有效期至 ${tokenResult.expiresAt}。`}
            />
          ) : null}
          <DialogFooter>
            <Button variant="primary" onClick={() => setTokenResult(null)}>
              我已安全保存
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={Boolean(installResult)}
        onOpenChange={(open) => !open && setInstallResult(null)}
      >
        <DialogContent aria-describedby={undefined} className="w-[min(720px,calc(100vw-2rem))]">
          <DialogHeader>
            <DialogTitle>一键安装命令</DialogTitle>
          </DialogHeader>
          {installResult ? (
            <div className="grid gap-3">
              <SecretReveal
                label="在目标集群执行"
                value={installResult.command}
                hint={`凭证有效期至 ${installResult.expiresAt}。`}
              />
              <p className="text-subtle-foreground text-xs">
                Manifest 地址：
                <span className="zke-mono break-all"> {installResult.manifestUrl}</span>
              </p>
            </div>
          ) : null}
          <DialogFooter>
            <Button variant="primary" onClick={() => setInstallResult(null)}>
              我已安全保存
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <SensitiveActionDialog
        open={Boolean(revokeTarget)}
        onOpenChange={(open) => !open && setRevokeTarget(null)}
        title="撤销接入凭证"
        destructive
        scopeLines={[
          { label: "Project", name: scope.projectName ?? "", id: scope.projectId },
          { label: "凭证", name: revokeTarget?.cluster_name ?? "", id: revokeTarget?.id },
        ]}
        impacts={["该凭证立即失效，使用它的 Agent 注册请求将被拒绝", "操作写入审计记录"]}
        confirmLabel="确认撤销"
        pending={revokeEnrollment.isPending}
        error={revokeEnrollment.error}
        onConfirm={async () => {
          if (!revokeTarget) {
            return;
          }
          try {
            await revokeEnrollment.mutateAsync({
              projectId: scope.projectId as string,
              enrollmentId: revokeTarget.id,
            });
            toast.success("接入凭证已撤销");
            setRevokeTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />
    </>
  );
}

function ClusterDetailSection({
  clusterId,
  onBack,
}: {
  clusterId: string | null;
  onBack: () => void;
}) {
  const query = useCluster(clusterId);

  if (!clusterId) {
    return (
      <EmptyState
        title="请先选择集群"
        description="在集群列表或全局概览中打开一个集群，即可查看它的连接与证书详情。"
        action={
          <Button size="sm" variant="secondary" onClick={onBack}>
            查看集群列表
          </Button>
        }
      />
    );
  }
  if (query.isLoading) {
    return <LoadingState />;
  }
  if (query.error) {
    return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  }
  if (!query.data) {
    return null;
  }

  const cluster = query.data;
  const connection = cluster.connection;

  return (
    <div className="grid gap-3">
      <SectionTitle
        title={cluster.name}
        description="连接与证书状态由 Server 实时汇总，并通过事件流推送更新"
        actions={
          <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>
            <RefreshCw />
            刷新
          </Button>
        }
      />

      <div className="grid gap-3 md:grid-cols-2">
        <Card className="grid gap-2">
          <CardTitle>接入</CardTitle>
          <Separator />
          <DetailRow label="Cluster ID" value={<IdentifierLabel value={cluster.id} />} />
          <DetailRow
            label="接入状态"
            value={<StatusBadge kind="cluster" value={cluster.status} />}
          />
          <DetailRow
            label="连接状态"
            value={<StatusBadge kind="connection" value={connection.status} />}
          />
          <DetailRow label="生命周期" value={<span>{connection.lifecycle_status}</span>} />
          <DetailRow label="健康状态" value={<span>{connection.health_status}</span>} />
          <DetailRow
            label="Agent 版本"
            value={<span className="zke-mono text-xs">{connection.version || "—"}</span>}
          />
          <DetailRow
            label="协议版本"
            value={<span className="zke-mono text-xs">{connection.protocol_version || "—"}</span>}
          />
        </Card>

        <Card className="grid gap-2">
          <CardTitle>证书</CardTitle>
          <Separator />
          <DetailRow
            label="证书状态"
            value={<StatusBadge kind="certificate" value={connection.certificate_status} />}
          />
          <DetailRow
            label="剩余有效期"
            value={<span>{formatDuration(connection.certificate_remaining_seconds)}</span>}
          />
          <DetailRow
            label="到期时间"
            value={<AbsoluteTime value={connection.certificate_expires_at} />}
          />
          <DetailRow
            label="证书序列号"
            value={
              <span className="zke-mono text-xs break-all">
                {connection.certificate_serial || "—"}
              </span>
            }
          />
        </Card>

        <Card className="grid gap-2">
          <CardTitle>连接历史</CardTitle>
          <Separator />
          <DetailRow label="连接建立" value={<RelativeTime value={connection.connected_at} />} />
          <DetailRow
            label="最近心跳"
            value={<RelativeTime value={connection.last_heartbeat_at} />}
          />
          <DetailRow label="最近在线" value={<RelativeTime value={connection.last_seen_at} />} />
          <DetailRow
            label="最近断开"
            value={<RelativeTime value={connection.last_disconnected_at} />}
          />
          <DetailRow
            label="断开原因"
            value={<span>{connection.last_disconnect_reason || "—"}</span>}
          />
        </Card>

        <Card className="grid gap-2">
          <CardTitle>作用域</CardTitle>
          <Separator />
          <DetailRow label="Tenant" value={<IdentifierLabel value={cluster.tenant_id} />} />
          <DetailRow label="Project" value={<IdentifierLabel value={cluster.project_id} />} />
          <DetailRow label="创建时间" value={<AbsoluteTime value={cluster.created_at} />} />
          <DetailRow label="更新时间" value={<AbsoluteTime value={cluster.updated_at} />} />
        </Card>
      </div>

      <p className="text-subtle-foreground text-xs">
        Cluster 与其中的 Agent 对外是同一个管理共同体，界面不单独暴露内部 Agent 身份。
      </p>
    </div>
  );
}

function DetailRow({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3 text-[13px]">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-foreground text-right">{value}</span>
    </div>
  );
}
