import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { KeyRound, MoreHorizontal, RefreshCw, Server, ServerCog } from "lucide-react";
import { toast } from "sonner";

import {
  useCluster,
  useClusters,
  useDeleteCluster,
  useReenrollCluster,
  useRevokeClusterConnection,
  useUpdateCluster,
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
import { DetailCard, DetailRow } from "@/components/common/detail";
import { SecretReveal } from "@/components/common/secret-reveal";
import { notifyFailure } from "@/components/common/notify";
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { FieldHint, Label } from "@/components/ui/label";
import { useDebouncedValue } from "@/lib/use-debounced-value";
import { useSubmissionKey } from "@/lib/use-submission-key";
import { formatDuration } from "@/lib/time";

const NAV: AppNavItem[] = [
  { id: "clusters", label: "集群", icon: Server },
  { id: "enrollments", label: "接入凭证", icon: KeyRound },
  { id: "detail", label: "集群详情", icon: ServerCog },
];

export function ClusterAccessApp(_props: AppComponentProps) {
  const scope = useScopeStore((state) => state.scope);
  const [section, setSection] = useState("clusters");
  // Which Cluster the detail view shows is navigation state, not an
  // authorization scope: Clusters live inside a Project and carry no
  // RoleBinding of their own.
  const [clusterId, setClusterId] = useState<string | null>(null);

  return (
    <AppShell nav={NAV} activeId={section} onNavigate={setSection}>
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

function ClusterSection({
  scope,
  onSelect,
}: {
  scope: ScopeSelection;
  onSelect: (cluster: ClusterAggregate) => void;
}) {
  const { permissions } = useSessionContext();
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebouncedValue(search);
  const [offset, setOffset] = useState(0);
  /*
   * Paging restarts when the settled search does, not on each keystroke. The
   * two used to be reset together, which — now that the term reaches the query
   * a moment later — asked the Server for page one of the *previous* term on
   * the way past.
   */
  const [appliedSearch, setAppliedSearch] = useState(debouncedSearch);
  if (appliedSearch !== debouncedSearch) {
    setAppliedSearch(debouncedSearch);
    setOffset(0);
  }
  const [renameTarget, setRenameTarget] = useState<ClusterAggregate | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [revokeTarget, setRevokeTarget] = useState<ClusterAggregate | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ClusterAggregate | null>(null);
  const [statusTarget, setStatusTarget] = useState<ClusterAggregate | null>(null);
  const [reenrollTarget, setReenrollTarget] = useState<ClusterAggregate | null>(null);
  const reenrollKey = useSubmissionKey(reenrollTarget !== null);
  const [reenrollResult, setReenrollResult] = useState<{ token: string; expiresAt: string } | null>(
    null,
  );

  const query = useClusters(scope.projectId, {
    limit: DEFAULT_PAGE_SIZE,
    offset,
    ...(debouncedSearch ? { q: debouncedSearch } : {}),
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
        size: 56,
        /*
         * One menu, and the row itself opens the detail.
         *
         * Five buttons per line mixed navigation with three different kinds of
         * change — renaming, cutting a live connection, and retiring — at one
         * weight. Opening a cluster is what a row is for; the rest belong behind
         * a menu, where the two that break a running connection can sit apart
         * from the one that only edits a label.
         */
        cell: ({ row }) => {
          const cluster = row.original;
          /*
           * 撤销连接 and 重新接入 are the two ends of one lifecycle, not two
           * choices to weigh against each other.
           *
           * `agents.lifecycle_status` is the column the Server reads for both:
           * a re-enrollment is refused with `resource_state_conflict` while any
           * Agent of the Cluster is still unrevoked, and a revocation of an
           * already-revoked Agent is a no-op that reports `already_revoked`. So
           * exactly one of the two can do anything at any moment, and the menu
           * offers that one.
           *
           * A suspended Cluster is excluded from re-enrollment by the same
           * query. 恢复 sits directly below in this menu, which is the step that
           * makes it available again.
           */
          const revoked = cluster.connection.lifecycle_status === "revoked";
          const revocable = canRevoke && !revoked;
          const reenrollable = canEnroll && revoked && cluster.status !== "suspended";
          return (
            <div
              className="flex justify-end"
              // The row opens the cluster; the menu must not, or every action
              // would navigate away from the thing it just changed.
              onClick={(event) => event.stopPropagation()}
            >
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button size="icon-sm" variant="ghost" aria-label={`${cluster.name} 的操作`}>
                    <MoreHorizontal />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-40">
                  <DropdownMenuItem onSelect={() => onSelect(cluster)}>详情</DropdownMenuItem>
                  {canManage ? (
                    <DropdownMenuItem
                      onSelect={() => {
                        setRenameTarget(cluster);
                        setRenameValue(cluster.name);
                      }}
                    >
                      重命名
                    </DropdownMenuItem>
                  ) : null}
                  {revocable || reenrollable ? <DropdownMenuSeparator /> : null}
                  {revocable ? (
                    <DropdownMenuItem onSelect={() => setRevokeTarget(cluster)}>
                      撤销连接
                    </DropdownMenuItem>
                  ) : null}
                  {reenrollable ? (
                    <DropdownMenuItem onSelect={() => setReenrollTarget(cluster)}>
                      重新接入
                    </DropdownMenuItem>
                  ) : null}
                  {canManage ? (
                    <>
                      <DropdownMenuItem onSelect={() => setStatusTarget(cluster)}>
                        {cluster.status === "suspended" ? "恢复" : "停用"}
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="danger" onSelect={() => setDeleteTarget(cluster)}>
                        删除
                      </DropdownMenuItem>
                    </>
                  ) : null}
                </DropdownMenuContent>
              </DropdownMenu>
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
      <div className="flex h-full min-h-0 flex-col">
        <SectionTitle
          title={`集群 · ${scope.projectName ?? scope.projectId}`}
          description="集群与其中的 ZKE Agent 是同一个管理共同体，操作以 cluster_id 为目标"
        />

        <DataTable
          columns={columns}
          data={query.data?.clusters}
          isLoading={query.isLoading}
          isFetching={query.isFetching}
          error={query.error}
          onRetry={() => void query.refetch()}
          rowKey={(row) => row.id}
          onRowClick={onSelect}
          emptyTitle="该项目还没有集群"
          emptyDescription="可在「接入凭证」中创建一次性凭证或一键安装命令，让集群中的 ZKE Agent 主动接入。"
          toolbar={
            <Input
              className="max-w-56"
              placeholder="按名称搜索"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          }
          pagination={{ value: query.data?.pagination, onOffsetChange: setOffset }}
        />
      </div>

      <Dialog open={Boolean(renameTarget)} onOpenChange={(open) => !open && setRenameTarget(null)}>
        <DialogContent aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>重命名集群</DialogTitle>
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
                    // A rename must not move the Cluster in or out of
                    // suspension, so it restates whichever it already is.
                    status: renameTarget.status === "suspended" ? "suspended" : "active",
                  });
                  toast.success("集群已重命名");
                  setRenameTarget(null);
                } catch (error) {
                  notifyFailure("重命名集群失败", error);
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
          { label: "租户", name: scope.tenantName ?? "", id: scope.tenantId },
          { label: "项目", name: scope.projectName ?? "", id: scope.projectId },
          { label: "集群", name: revokeTarget?.name ?? "", id: revokeTarget?.id },
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
        open={Boolean(statusTarget)}
        onOpenChange={(open) => !open && setStatusTarget(null)}
        title={statusTarget?.status === "suspended" ? "恢复集群" : "停用集群"}
        destructive={statusTarget?.status !== "suspended"}
        scopeLines={[
          { label: "租户", name: scope.tenantName ?? "", id: scope.tenantId },
          { label: "项目", name: scope.projectName ?? "", id: scope.projectId },
          { label: "集群", name: statusTarget?.name ?? "", id: statusTarget?.id },
        ]}
        impacts={
          statusTarget?.status === "suspended"
            ? ["集群恢复为可用状态", "Agent 将以原有身份自动重新连接"]
            : [
                "已连接的 Agent 立即断开",
                "不撤销 Agent 身份或凭证，恢复后可直接重连",
                "停用期间集群名称仍被占用",
              ]
        }
        confirmationText={statusTarget?.status === "suspended" ? undefined : statusTarget?.name}
        confirmLabel={statusTarget?.status === "suspended" ? "确认恢复" : "确认停用"}
        pending={updateCluster.isPending}
        error={updateCluster.error}
        onConfirm={async () => {
          if (!statusTarget) {
            return;
          }
          try {
            await updateCluster.mutateAsync({
              clusterId: statusTarget.id,
              name: statusTarget.name,
              status: statusTarget.status === "suspended" ? "active" : "suspended",
            });
            toast.success(statusTarget.status === "suspended" ? "集群已恢复" : "集群已停用");
            setStatusTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />

      <SensitiveActionDialog
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title="删除集群"
        destructive
        scopeLines={[
          { label: "租户", name: scope.tenantName ?? "", id: scope.tenantId },
          { label: "项目", name: scope.projectName ?? "", id: scope.projectId },
          { label: "集群", name: deleteTarget?.name ?? "", id: deleteTarget?.id },
        ]}
        impacts={[
          "集群记录及其 Agent 身份、凭证被永久删除",
          "已连接的 Agent 立即断开，且无法再次接入",
          "集群名称随之释放，可被重新使用",
          "审计记录保留，并保存删除时的名称",
          "若只是临时冻结，请改用「停用」——停用不删除任何内容",
        ]}
        confirmationText={deleteTarget?.name}
        confirmLabel="确认删除"
        pending={deleteCluster.isPending}
        error={deleteCluster.error}
        onConfirm={async () => {
          if (!deleteTarget) {
            return;
          }
          try {
            await deleteCluster.mutateAsync({ clusterId: deleteTarget.id });
            toast.success("集群已删除");
            setDeleteTarget(null);
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
          { label: "项目", name: scope.projectName ?? "", id: scope.projectId },
          { label: "集群", name: reenrollTarget?.name ?? "", id: reenrollTarget?.id },
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
              idempotencyKey: reenrollKey,
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
  const enrollmentKey = useSubmissionKey(createOpen);
  const installationKey = useSubmissionKey(installOpen);
  // Named for the wire field it becomes — the enrollment's `cluster_name` —
  // which both dialogs present as the credential's own name, because that is
  // what it stays after the Cluster it creates is renamed.
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
        /*
         * The credential's name, not the Cluster's.
         *
         * `cluster_name` is written onto the enrollment when it is issued and
         * never touched again: a first enrollment carries the name its Cluster
         * will be created under, a re-enrollment carries a snapshot of the name
         * the Cluster had at the time. Renaming the Cluster afterwards changes
         * neither, so a column headed 集群名称 was reporting a name that may no
         * longer belong to anything — most visibly for a re-enrollment issued
         * before a rename.
         *
         * What the value actually identifies is this credential, which is also
         * how the revocation dialog already labels it.
         */
        header: "凭证名称",
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
      <div className="flex h-full min-h-0 flex-col">
        <SectionTitle
          title="接入凭证"
          description="一次性凭证由集群内的 ZKE Agent 使用，Agent 主动连接 Server 完成注册；名称在签发时固定，集群之后可以单独重命名"
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
      </div>

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>创建一次性接入凭证</DialogTitle>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="enrollment-name">凭证名称</Label>
            <Input
              id="enrollment-name"
              value={clusterName}
              maxLength={253}
              autoFocus
              onChange={(event) => setClusterName(event.target.value)}
            />
            <FieldHint>
              名称在签发时绑定到本凭证，之后不再变化；Agent
              注册成功时以该名称创建集群，集群可以随时重命名。凭证未使用前会占用同名集群名称。
            </FieldHint>
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
                    idempotencyKey: enrollmentKey,
                  });
                  setCreateOpen(false);
                  setTokenResult({ token: result.token, expiresAt: result.expires_at });
                } catch (error) {
                  notifyFailure("创建接入凭证失败", error);
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
            <Label htmlFor="installation-name">凭证名称</Label>
            <Input
              id="installation-name"
              value={clusterName}
              maxLength={253}
              autoFocus
              onChange={(event) => setClusterName(event.target.value)}
            />
            <FieldHint>
              名称在签发时绑定到本凭证；Agent
              注册成功时以该名称创建集群，集群可以随时重命名。安装命令中包含一次性安装凭证，等同于接入凭证，必须按机密信息处理。
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
                    idempotencyKey: installationKey,
                  });
                  setInstallOpen(false);
                  setInstallResult({
                    command: result.install_command,
                    manifestUrl: result.manifest_url,
                    expiresAt: result.expires_at,
                  });
                } catch (error) {
                  notifyFailure("生成安装命令失败", error);
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
          { label: "项目", name: scope.projectName ?? "", id: scope.projectId },
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
        description="在集群列表中打开一个集群，即可查看它的连接与证书详情。"
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
        <DetailCard title="接入">
          <DetailRow label="集群 ID" value={<IdentifierLabel value={cluster.id} />} />
          <DetailRow
            label="接入状态"
            value={<StatusBadge kind="cluster" value={cluster.status} />}
          />
          <DetailRow
            label="连接状态"
            value={<StatusBadge kind="connection" value={connection.status} />}
          />
          <DetailRow
            label="生命周期"
            value={<StatusBadge kind="lifecycle" value={connection.lifecycle_status} />}
          />
          <DetailRow
            label="健康状态"
            value={<StatusBadge kind="health" value={connection.health_status} />}
          />
          <DetailRow
            label="Agent 版本"
            value={<span className="zke-mono text-xs">{connection.version || "—"}</span>}
          />
          <DetailRow
            label="协议版本"
            value={<span className="zke-mono text-xs">{connection.protocol_version || "—"}</span>}
          />
        </DetailCard>

        <DetailCard title="证书">
          <DetailRow
            label="证书状态"
            value={<StatusBadge kind="certificate" value={connection.certificate_status} />}
          />
          <DetailRow
            label="剩余有效期"
            value={formatDuration(connection.certificate_remaining_seconds)}
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
        </DetailCard>

        <DetailCard title="连接历史">
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
          <DetailRow label="断开原因" value={connection.last_disconnect_reason || "—"} />
        </DetailCard>

        <DetailCard title="所属">
          <DetailRow label="租户" value={<IdentifierLabel value={cluster.tenant_id} />} />
          <DetailRow label="项目" value={<IdentifierLabel value={cluster.project_id} />} />
          <DetailRow label="创建时间" value={<AbsoluteTime value={cluster.created_at} />} />
          <DetailRow label="更新时间" value={<AbsoluteTime value={cluster.updated_at} />} />
        </DetailCard>
      </div>

      <p className="text-subtle-foreground text-xs">
        集群与其中的 Agent 对外是同一个管理共同体，界面不单独暴露内部 Agent 身份。
      </p>
    </div>
  );
}
