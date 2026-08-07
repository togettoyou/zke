import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Building2, ChevronRight, FolderKanban, MoreHorizontal, PlusCircle } from "lucide-react";
import { toast } from "sonner";

import { errorMessage, errorRequestId } from "@/api/errors";
import {
  useCreateProject,
  useCreateTenant,
  useDeleteProject,
  useDeleteTenant,
  useProjects,
  useTenants,
  useUpdateProject,
  useUpdateTenant,
} from "@/api/queries/resources";
import { DEFAULT_PAGE_SIZE, type Project, type ResourceStatus, type Tenant } from "@/api/types";
import { AppShell, SectionTitle, type AppNavItem } from "@/apps/AppShell";
import type { AppComponentProps } from "@/apps/types";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
import { EmptyState } from "@/components/common/state";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { IdentifierLabel, RelativeTime, StatusBadge } from "@/components/common/status";
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
import { Label } from "@/components/ui/label";
import { Alert } from "@/components/ui/misc";
import { useScopeStore } from "@/scope/scope-store";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useDebouncedValue } from "@/lib/use-debounced-value";
import { useSubmissionKey } from "@/lib/use-submission-key";

const NAV: AppNavItem[] = [
  { id: "tenants", label: "租户", icon: Building2 },
  { id: "projects", label: "项目", icon: FolderKanban },
];

const STATUS_FILTERS: Array<{ value: string; label: string }> = [
  { value: "all", label: "全部状态" },
  { value: "active", label: "启用" },
  { value: "suspended", label: "已停用" },
];

/**
 * Organization management administers the whole Tenant/Project tree, including
 * Tenants that hold no Project yet and could therefore never be reached through
 * the Project picker. The Tenant being browsed is navigation state owned here,
 * not the Console-wide scope.
 */
export function ResourcesApp({ openApp }: AppComponentProps) {
  const [section, setSection] = useState<string>("tenants");
  const [tenant, setTenant] = useState<Tenant | null>(null);
  const setScope = useScopeStore((state) => state.setScope);

  return (
    <AppShell nav={NAV} activeId={section} onNavigate={setSection}>
      {section === "tenants" ? (
        <TenantSection
          onOpenProjects={(selected) => {
            setTenant(selected);
            setSection("projects");
          }}
        />
      ) : (
        <ProjectSection
          tenantId={tenant?.id ?? null}
          tenantName={tenant?.name ?? null}
          onBack={() => setSection("tenants")}
          onSelectProject={(project) =>
            setScope({
              tenantId: project.tenant_id,
              tenantName: tenant?.name ?? null,
              projectId: project.id,
              projectName: project.name,
            })
          }
          onOpenClusters={(project) => {
            // Cluster access works in the selected Project, so opening it from
            // here selects that Project first.
            setScope({
              tenantId: project.tenant_id,
              tenantName: tenant?.name ?? null,
              projectId: project.id,
              projectName: project.name,
            });
            openApp("cluster-access", { title: `集群接入管理 · ${project.name}` });
          }}
        />
      )}
    </AppShell>
  );
}

type NameDialogState = {
  mode: "create" | "rename";
  target?: { id: string; name: string; status: ResourceStatus };
} | null;

function TenantSection({ onOpenProjects }: { onOpenProjects: (tenant: Tenant) => void }) {
  const { permissions } = useSessionContext();
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebouncedValue(search);
  const [status, setStatus] = useState<string>("all");
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
  const [nameDialog, setNameDialog] = useState<NameDialogState>(null);
  const [statusTarget, setStatusTarget] = useState<Tenant | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Tenant | null>(null);

  const query = useTenants({
    limit: DEFAULT_PAGE_SIZE,
    offset,
    ...(debouncedSearch ? { q: debouncedSearch } : {}),
    ...(status === "all" ? {} : { status: status as ResourceStatus }),
  });

  const createTenant = useCreateTenant();
  const updateTenant = useUpdateTenant();
  const deleteTenant = useDeleteTenant();

  const canCreate = permissions.can("tenant.create", { type: "global" });

  const columns = useMemo<ColumnDef<Tenant, unknown>[]>(
    () => [
      {
        header: "名称",
        accessorKey: "name",
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="text-foreground font-medium">{row.original.name}</span>
            <IdentifierLabel value={row.original.id} />
          </div>
        ),
      },
      {
        header: "状态",
        accessorKey: "status",
        size: 110,
        cell: ({ row }) => <StatusBadge kind="resource" value={row.original.status} />,
      },
      {
        header: "创建时间",
        accessorKey: "created_at",
        size: 140,
        cell: ({ row }) => <RelativeTime value={row.original.created_at} />,
      },
      {
        header: "更新时间",
        accessorKey: "updated_at",
        size: 140,
        cell: ({ row }) => <RelativeTime value={row.original.updated_at} />,
      },
      {
        id: "actions",
        header: "",
        size: 56,
        /*
         * One menu, and the row itself carries the drill-down.
         *
         * Four buttons on every line mixed two different kinds of thing:
         * "查看项目" navigates into the tenant, the rest change it. Navigation
         * belongs to the row — a tenant containing projects behaves like a
         * folder — and the changes belong behind a menu, where the destructive
         * one can sit below a separator instead of being the same control in a
         * different colour.
         */
        cell: ({ row }) => {
          const tenant = row.original;
          const canManage = permissions.can("tenant.manage", {
            type: "tenant",
            tenantId: tenant.id,
          });
          return (
            <div
              className="flex justify-end"
              // The row opens the tenant; the menu must not, or every management
              // action would navigate away from the thing it just changed.
              onClick={(event) => event.stopPropagation()}
            >
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button size="icon-sm" variant="ghost" aria-label={`${tenant.name} 的操作`}>
                    <MoreHorizontal />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-40">
                  <DropdownMenuItem onSelect={() => onOpenProjects(tenant)}>
                    查看项目
                  </DropdownMenuItem>
                  {canManage ? (
                    <>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem
                        onSelect={() =>
                          setNameDialog({
                            mode: "rename",
                            target: {
                              id: tenant.id,
                              name: tenant.name,
                              status: tenant.status as ResourceStatus,
                            },
                          })
                        }
                      >
                        重命名
                      </DropdownMenuItem>
                      <DropdownMenuItem onSelect={() => setStatusTarget(tenant)}>
                        {tenant.status === "active" ? "停用" : "恢复"}
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="danger" onSelect={() => setDeleteTarget(tenant)}>
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
    [onOpenProjects, permissions],
  );

  return (
    <>
      {/* A full-height column: without it the table grows to fit its rows, the
          whole view scrolls instead, and the sticky header sticks to nothing. */}
      <div className="flex h-full min-h-0 flex-col">
        <SectionTitle
          title="租户"
          description="租户是权限与资源的顶层边界；停用会影响其下全部项目与集群"
          actions={
            canCreate ? (
              <Button size="sm" variant="primary" onClick={() => setNameDialog({ mode: "create" })}>
                <PlusCircle />
                新建租户
              </Button>
            ) : null
          }
        />

        <DataTable
          columns={columns}
          data={query.data?.tenants}
          isLoading={query.isLoading}
          isFetching={query.isFetching}
          error={query.error}
          onRetry={() => void query.refetch()}
          rowKey={(row) => row.id}
          onRowClick={onOpenProjects}
          emptyTitle="没有可见的租户"
          emptyDescription="当前账号的权限范围内没有租户，或筛选条件过窄。"
          toolbar={
            <>
              <Input
                className="max-w-56"
                placeholder="按名称搜索"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
              />
              <Select
                value={status}
                onValueChange={(value) => {
                  setStatus(value);
                  setOffset(0);
                }}
              >
                <SelectTrigger className="w-36">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {STATUS_FILTERS.map((item) => (
                    <SelectItem key={item.value} value={item.value}>
                      {item.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </>
          }
          pagination={{ value: query.data?.pagination, onOffsetChange: setOffset }}
        />
      </div>

      <NameDialog
        state={nameDialog}
        title={nameDialog?.mode === "create" ? "新建租户" : "重命名租户"}
        pending={createTenant.isPending || updateTenant.isPending}
        error={createTenant.error ?? updateTenant.error}
        onClose={() => {
          setNameDialog(null);
          // Reset so the next time the dialog opens it does not start out
          // showing the previous attempt's failure.
          createTenant.reset();
          updateTenant.reset();
        }}
        onSubmit={async (name, idempotencyKey) => {
          if (nameDialog?.mode === "create") {
            await createTenant.mutateAsync({ name, idempotencyKey });
            toast.success(`租户 ${name} 已创建`);
          } else if (nameDialog?.target) {
            await updateTenant.mutateAsync({
              tenantId: nameDialog.target.id,
              name,
              status: nameDialog.target.status,
            });
            toast.success("租户已重命名");
          }
          setNameDialog(null);
        }}
      />

      <SensitiveActionDialog
        open={Boolean(statusTarget)}
        onOpenChange={(open) => !open && setStatusTarget(null)}
        title={statusTarget?.status === "active" ? "停用租户" : "恢复租户"}
        destructive={statusTarget?.status === "active"}
        scopeLines={[{ label: "租户", name: statusTarget?.name ?? "", id: statusTarget?.id }]}
        impacts={
          statusTarget?.status === "active"
            ? [
                "租户及其下的项目、集群暂停使用，已连接的 Agent 被断开",
                "不撤销任何接入身份或凭证，恢复后 Agent 以原身份自动重连",
                "停用期间租户名称仍被占用",
              ]
            : ["租户恢复为启用状态", "其下 Agent 将自动重新连接"]
        }
        confirmationText={statusTarget?.status === "active" ? statusTarget?.name : undefined}
        pending={updateTenant.isPending}
        error={updateTenant.error}
        onConfirm={async () => {
          if (!statusTarget) {
            return;
          }
          try {
            await updateTenant.mutateAsync({
              tenantId: statusTarget.id,
              name: statusTarget.name,
              status: statusTarget.status === "active" ? "suspended" : "active",
            });
            toast.success("租户状态已更新");
            setStatusTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />

      <SensitiveActionDialog
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => {
          if (!open) {
            setDeleteTarget(null);
            // Same reason as the name dialog above: without this the refusal
            // stays on screen the next time the dialog opens.
            deleteTenant.reset();
          }
        }}
        title="删除租户"
        destructive
        description="删除会连同其下的项目、集群、Agent 身份与凭证一并移除。记录不再存在，无法恢复。"
        scopeLines={[{ label: "租户", name: deleteTarget?.name ?? "", id: deleteTarget?.id }]}
        impacts={[
          "租户及其下的项目、集群、Agent 身份与凭证被永久删除",
          "已连接的 Agent 立即断开，且无法再次接入",
          "租户名称随之释放，可被重新使用",
          "审计记录保留，并保存删除时的名称",
          "若只是临时冻结，请改用「停用」——停用不删除任何内容",
        ]}
        confirmationText={deleteTarget?.name}
        confirmLabel="确认删除"
        pending={deleteTenant.isPending}
        error={deleteTenant.error}
        onConfirm={async () => {
          if (!deleteTarget) {
            return;
          }
          try {
            await deleteTenant.mutateAsync({ tenantId: deleteTarget.id });
            toast.success("租户已删除");
            setDeleteTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />
    </>
  );
}

function ProjectSection({
  tenantId,
  tenantName,
  onBack,
  onSelectProject,
  onOpenClusters,
}: {
  tenantId: string | null;
  tenantName: string | null;
  /** Returns to the tenant list, which is the parent of this view. */
  onBack: () => void;
  onSelectProject: (project: Project) => void;
  onOpenClusters: (project: Project) => void;
}) {
  const { permissions } = useSessionContext();
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebouncedValue(search);
  const [status, setStatus] = useState("all");
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
  const [nameDialog, setNameDialog] = useState<NameDialogState>(null);
  const [statusTarget, setStatusTarget] = useState<Project | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Project | null>(null);

  const query = useProjects(tenantId, {
    limit: DEFAULT_PAGE_SIZE,
    offset,
    ...(debouncedSearch ? { q: debouncedSearch } : {}),
    ...(status === "all" ? {} : { status: status as ResourceStatus }),
  });

  const createProject = useCreateProject();
  const updateProject = useUpdateProject();
  const deleteProject = useDeleteProject();

  const canCreate = permissions.can("project.create", { type: "tenant", tenantId });

  const columns = useMemo<ColumnDef<Project, unknown>[]>(
    () => [
      {
        header: "名称",
        accessorKey: "name",
        cell: ({ row }) => (
          <div className="flex flex-col">
            <span className="text-foreground font-medium">{row.original.name}</span>
            <IdentifierLabel value={row.original.id} />
          </div>
        ),
      },
      {
        header: "状态",
        accessorKey: "status",
        size: 110,
        cell: ({ row }) => <StatusBadge kind="resource" value={row.original.status} />,
      },
      {
        header: "创建时间",
        accessorKey: "created_at",
        size: 140,
        cell: ({ row }) => <RelativeTime value={row.original.created_at} />,
      },
      {
        header: "更新时间",
        accessorKey: "updated_at",
        size: 140,
        cell: ({ row }) => <RelativeTime value={row.original.updated_at} />,
      },
      {
        id: "actions",
        header: "",
        size: 56,
        cell: ({ row }) => {
          const project = row.original;
          const canManage = permissions.can("project.manage", {
            type: "project",
            tenantId: project.tenant_id,
            projectId: project.id,
          });
          return (
            <div className="flex justify-end">
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button size="icon-sm" variant="ghost" aria-label={`${project.name} 的操作`}>
                    <MoreHorizontal />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-40">
                  <DropdownMenuItem onSelect={() => onSelectProject(project)}>
                    设为当前项目
                  </DropdownMenuItem>
                  <DropdownMenuItem onSelect={() => onOpenClusters(project)}>
                    打开集群接入
                  </DropdownMenuItem>
                  {canManage ? (
                    <>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem
                        onSelect={() =>
                          setNameDialog({
                            mode: "rename",
                            target: {
                              id: project.id,
                              name: project.name,
                              status: project.status as ResourceStatus,
                            },
                          })
                        }
                      >
                        重命名
                      </DropdownMenuItem>
                      <DropdownMenuItem onSelect={() => setStatusTarget(project)}>
                        {project.status === "active" ? "停用" : "恢复"}
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem variant="danger" onSelect={() => setDeleteTarget(project)}>
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
    [onOpenClusters, onSelectProject, permissions],
  );

  if (!tenantId) {
    return (
      <EmptyState
        title="请先选择租户"
        description="在租户列表中打开一个租户，即可管理它下面的项目。"
        action={
          <Button size="sm" variant="secondary" onClick={onBack}>
            查看租户列表
          </Button>
        }
      />
    );
  }

  return (
    <>
      <div className="flex h-full min-h-0 flex-col">
        {/* Which tenant's projects these are is the whole context of this view, so
          it gets a trail back rather than only a title suffix. */}
        <nav
          aria-label="层级"
          className="text-muted-foreground mb-3 flex items-center gap-1 text-xs"
        >
          <button
            type="button"
            onClick={onBack}
            className="zke-focus hover:text-foreground rounded-control -mx-1 border border-transparent px-1 py-0.5 transition-colors"
          >
            租户
          </button>
          <ChevronRight className="size-3 shrink-0" aria-hidden />
          <span className="text-foreground max-w-60 truncate font-medium">
            {tenantName ?? tenantId}
          </span>
        </nav>

        <SectionTitle
          title="项目"
          description="项目是集群接入与资源操作的授权边界"
          actions={
            canCreate ? (
              <Button size="sm" variant="primary" onClick={() => setNameDialog({ mode: "create" })}>
                <PlusCircle />
                新建项目
              </Button>
            ) : null
          }
        />

        <DataTable
          columns={columns}
          data={query.data?.projects}
          isLoading={query.isLoading}
          isFetching={query.isFetching}
          error={query.error}
          onRetry={() => void query.refetch()}
          rowKey={(row) => row.id}
          emptyTitle="该租户下没有可见的项目"
          toolbar={
            <>
              <Input
                className="max-w-56"
                placeholder="按名称搜索"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
              />
              <Select
                value={status}
                onValueChange={(value) => {
                  setStatus(value);
                  setOffset(0);
                }}
              >
                <SelectTrigger className="w-36">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {STATUS_FILTERS.map((item) => (
                    <SelectItem key={item.value} value={item.value}>
                      {item.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </>
          }
          pagination={{ value: query.data?.pagination, onOffsetChange: setOffset }}
        />
      </div>

      <NameDialog
        state={nameDialog}
        title={nameDialog?.mode === "create" ? "新建项目" : "重命名项目"}
        pending={createProject.isPending || updateProject.isPending}
        error={createProject.error ?? updateProject.error}
        onClose={() => {
          setNameDialog(null);
          createProject.reset();
          updateProject.reset();
        }}
        onSubmit={async (name, idempotencyKey) => {
          if (nameDialog?.mode === "create") {
            await createProject.mutateAsync({
              tenantId,
              name,
              idempotencyKey,
            });
            toast.success(`项目 ${name} 已创建`);
          } else if (nameDialog?.target) {
            await updateProject.mutateAsync({
              projectId: nameDialog.target.id,
              name,
              status: nameDialog.target.status,
            });
            toast.success("项目已重命名");
          }
          setNameDialog(null);
        }}
      />

      <SensitiveActionDialog
        open={Boolean(statusTarget)}
        onOpenChange={(open) => !open && setStatusTarget(null)}
        title={statusTarget?.status === "active" ? "停用项目" : "恢复项目"}
        destructive={statusTarget?.status === "active"}
        scopeLines={[
          { label: "租户", name: tenantName ?? "", id: tenantId },
          { label: "项目", name: statusTarget?.name ?? "", id: statusTarget?.id },
        ]}
        impacts={
          statusTarget?.status === "active"
            ? [
                "项目及其下的集群暂停使用，已连接的 Agent 被断开",
                "不撤销任何接入身份或凭证，恢复后 Agent 以原身份自动重连",
                "停用期间项目名称仍被占用",
              ]
            : ["项目恢复为启用状态", "其下 Agent 将自动重新连接"]
        }
        confirmationText={statusTarget?.status === "active" ? statusTarget?.name : undefined}
        pending={updateProject.isPending}
        error={updateProject.error}
        onConfirm={async () => {
          if (!statusTarget) {
            return;
          }
          try {
            await updateProject.mutateAsync({
              projectId: statusTarget.id,
              name: statusTarget.name,
              status: statusTarget.status === "active" ? "suspended" : "active",
            });
            toast.success("项目状态已更新");
            setStatusTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />

      <SensitiveActionDialog
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => {
          if (!open) {
            setDeleteTarget(null);
            deleteProject.reset();
          }
        }}
        title="删除项目"
        destructive
        description="删除会连同其下的集群、Agent 身份与凭证一并移除。记录不再存在，无法恢复。"
        scopeLines={[
          { label: "租户", name: tenantName ?? "", id: tenantId },
          { label: "项目", name: deleteTarget?.name ?? "", id: deleteTarget?.id },
        ]}
        impacts={[
          "项目及其下的集群、Agent 身份与凭证被永久删除",
          "已连接的 Agent 立即断开，且无法再次接入",
          "项目名称在该租户内随之释放",
          "审计记录保留，并保存删除时的名称",
          "若只是临时冻结，请改用「停用」——停用不删除任何内容",
        ]}
        confirmationText={deleteTarget?.name}
        confirmLabel="确认删除"
        pending={deleteProject.isPending}
        error={deleteProject.error}
        onConfirm={async () => {
          if (!deleteTarget) {
            return;
          }
          try {
            await deleteProject.mutateAsync({ projectId: deleteTarget.id });
            toast.success("项目已删除");
            setDeleteTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />
    </>
  );
}

function NameDialog({
  state,
  title,
  pending,
  error,
  onClose,
  onSubmit,
}: {
  state: NameDialogState;
  title: string;
  pending: boolean;
  /** Rendered in place; a rejected submit must never fail silently. */
  error?: unknown;
  onClose: () => void;
  onSubmit: (name: string, idempotencyKey: string) => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [initialized, setInitialized] = useState<string | null>(null);
  const idempotencyKey = useSubmissionKey(Boolean(state));

  const currentKey = state ? `${state.mode}:${state.target?.id ?? "new"}` : null;
  if (currentKey && initialized !== currentKey) {
    setInitialized(currentKey);
    setName(state?.target?.name ?? "");
  }
  if (!currentKey && initialized !== null) {
    setInitialized(null);
  }

  return (
    <Dialog open={Boolean(state)} onOpenChange={(open) => !open && onClose()}>
      <DialogContent aria-describedby={undefined}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <div className="grid gap-1.5">
          <Label htmlFor="resource-name">名称</Label>
          <Input
            id="resource-name"
            value={name}
            maxLength={253}
            autoFocus
            onChange={(event) => setName(event.target.value)}
          />
        </div>

        {error ? (
          <Alert tone="danger" className="mt-3">
            {errorMessage(error)}
            {errorRequestId(error) ? (
              <span className="zke-mono mt-1 block text-xs opacity-80">
                请求 ID：{errorRequestId(error)}
              </span>
            ) : null}
          </Alert>
        ) : null}

        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={pending}>
            取消
          </Button>
          <Button
            variant="primary"
            disabled={pending || name.trim().length === 0}
            onClick={() => {
              // The rejection is already surfaced through `error`; swallowing it
              // here only stops an unhandled promise rejection.
              void onSubmit(name.trim(), idempotencyKey).catch(() => undefined);
            }}
          >
            {pending ? "提交中…" : "确认"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
