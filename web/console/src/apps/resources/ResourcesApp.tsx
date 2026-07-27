import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Building2, FolderKanban, PlusCircle, Server } from "lucide-react";
import { toast } from "sonner";

import { newIdempotencyKey } from "@/api/client";
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
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useScopeStore } from "@/scope/scope-store";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

const NAV: AppNavItem[] = [
  { id: "tenants", label: "Tenant", icon: Building2 },
  { id: "projects", label: "Project", icon: FolderKanban },
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
  const [status, setStatus] = useState<string>("all");
  const [offset, setOffset] = useState(0);
  const [nameDialog, setNameDialog] = useState<NameDialogState>(null);
  const [statusTarget, setStatusTarget] = useState<Tenant | null>(null);
  const [retireTarget, setRetireTarget] = useState<Tenant | null>(null);

  const query = useTenants({
    limit: DEFAULT_PAGE_SIZE,
    offset,
    ...(search ? { q: search } : {}),
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
        size: 300,
        cell: ({ row }) => {
          const tenant = row.original;
          const canManage = permissions.can("tenant.manage", {
            type: "tenant",
            tenantId: tenant.id,
          });
          return (
            <div className="flex flex-wrap justify-end gap-1">
              <Button size="sm" variant="ghost" onClick={() => onOpenProjects(tenant)}>
                查看 Project
              </Button>
              {canManage ? (
                <>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() =>
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
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setStatusTarget(tenant)}>
                    {tenant.status === "active" ? "停用" : "恢复"}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-danger"
                    onClick={() => setRetireTarget(tenant)}
                  >
                    退役
                  </Button>
                </>
              ) : null}
            </div>
          );
        },
      },
    ],
    [onOpenProjects, permissions],
  );

  return (
    <>
      <SectionTitle
        title="Tenant"
        description="租户是权限与资源的顶层边界；停用会影响其下全部 Project 与 Cluster"
        actions={
          canCreate ? (
            <Button size="sm" variant="primary" onClick={() => setNameDialog({ mode: "create" })}>
              <PlusCircle />
              新建 Tenant
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
        emptyTitle="没有可见的 Tenant"
        emptyDescription="当前账号的权限范围内没有 Tenant，或筛选条件过窄。"
        toolbar={
          <>
            <Input
              className="max-w-56"
              placeholder="按名称搜索"
              value={search}
              onChange={(event) => {
                setSearch(event.target.value);
                setOffset(0);
              }}
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

      <NameDialog
        state={nameDialog}
        title={nameDialog?.mode === "create" ? "新建 Tenant" : "重命名 Tenant"}
        pending={createTenant.isPending || updateTenant.isPending}
        onClose={() => setNameDialog(null)}
        onSubmit={async (name) => {
          if (nameDialog?.mode === "create") {
            await createTenant.mutateAsync({ name, idempotencyKey: newIdempotencyKey() });
            toast.success(`Tenant ${name} 已创建`);
          } else if (nameDialog?.target) {
            await updateTenant.mutateAsync({
              tenantId: nameDialog.target.id,
              name,
              status: nameDialog.target.status,
            });
            toast.success("Tenant 已重命名");
          }
          setNameDialog(null);
        }}
      />

      <SensitiveActionDialog
        open={Boolean(statusTarget)}
        onOpenChange={(open) => !open && setStatusTarget(null)}
        title={statusTarget?.status === "active" ? "停用 Tenant" : "恢复 Tenant"}
        destructive={statusTarget?.status === "active"}
        scopeLines={[{ label: "Tenant", name: statusTarget?.name ?? "", id: statusTarget?.id }]}
        impacts={
          statusTarget?.status === "active"
            ? [
                "该 Tenant 及其下的 Project 将被标记为停用",
                "停用状态下不能创建新的 Project 与集群接入凭证",
              ]
            : ["该 Tenant 恢复为启用状态"]
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
            toast.success("Tenant 状态已更新");
            setStatusTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />

      <SensitiveActionDialog
        open={Boolean(retireTarget)}
        onOpenChange={(open) => !open && setRetireTarget(null)}
        title="退役 Tenant"
        destructive
        description="退役会停用 Tenant，并撤销其下全部集群的接入身份与未使用凭证。"
        scopeLines={[{ label: "Tenant", name: retireTarget?.name ?? "", id: retireTarget?.id }]}
        impacts={[
          "Tenant 及其下 Project 被停用",
          "其下全部 Cluster 的 Agent 接入身份被撤销，已连接的 Agent 将断开",
          "未使用的接入凭证全部失效",
          "该操作会写入审计记录，且不可自动回滚",
        ]}
        confirmationText={retireTarget?.name}
        confirmLabel="确认退役"
        pending={deleteTenant.isPending}
        error={deleteTenant.error}
        onConfirm={async () => {
          if (!retireTarget) {
            return;
          }
          try {
            await deleteTenant.mutateAsync({ tenantId: retireTarget.id });
            toast.success("Tenant 已退役");
            setRetireTarget(null);
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
  onSelectProject,
  onOpenClusters,
}: {
  tenantId: string | null;
  tenantName: string | null;
  onSelectProject: (project: Project) => void;
  onOpenClusters: (project: Project) => void;
}) {
  const { permissions } = useSessionContext();
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("all");
  const [offset, setOffset] = useState(0);
  const [nameDialog, setNameDialog] = useState<NameDialogState>(null);
  const [statusTarget, setStatusTarget] = useState<Project | null>(null);
  const [retireTarget, setRetireTarget] = useState<Project | null>(null);

  const query = useProjects(tenantId, {
    limit: DEFAULT_PAGE_SIZE,
    offset,
    ...(search ? { q: search } : {}),
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
        header: "更新时间",
        accessorKey: "updated_at",
        size: 140,
        cell: ({ row }) => <RelativeTime value={row.original.updated_at} />,
      },
      {
        id: "actions",
        header: "",
        size: 320,
        cell: ({ row }) => {
          const project = row.original;
          const canManage = permissions.can("project.manage", {
            type: "project",
            tenantId: project.tenant_id,
            projectId: project.id,
          });
          return (
            <div className="flex flex-wrap justify-end gap-1">
              <Button size="sm" variant="ghost" onClick={() => onSelectProject(project)}>
                设为当前项目
              </Button>
              <Button size="sm" variant="ghost" onClick={() => onOpenClusters(project)}>
                <Server />
                集群
              </Button>
              {canManage ? (
                <>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() =>
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
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setStatusTarget(project)}>
                    {project.status === "active" ? "停用" : "恢复"}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-danger"
                    onClick={() => setRetireTarget(project)}
                  >
                    退役
                  </Button>
                </>
              ) : null}
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
        title="请先选择 Tenant"
        description="在 Tenant 列表中打开一个 Tenant，即可管理它下面的 Project。"
      />
    );
  }

  return (
    <>
      <SectionTitle
        title={`Project · ${tenantName ?? tenantId}`}
        description="Project 是集群接入与资源操作的授权边界"
        actions={
          canCreate ? (
            <Button size="sm" variant="primary" onClick={() => setNameDialog({ mode: "create" })}>
              <PlusCircle />
              新建 Project
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
        emptyTitle="该 Tenant 下没有可见的 Project"
        toolbar={
          <>
            <Input
              className="max-w-56"
              placeholder="按名称搜索"
              value={search}
              onChange={(event) => {
                setSearch(event.target.value);
                setOffset(0);
              }}
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

      <NameDialog
        state={nameDialog}
        title={nameDialog?.mode === "create" ? "新建 Project" : "重命名 Project"}
        pending={createProject.isPending || updateProject.isPending}
        onClose={() => setNameDialog(null)}
        onSubmit={async (name) => {
          if (nameDialog?.mode === "create") {
            await createProject.mutateAsync({
              tenantId,
              name,
              idempotencyKey: newIdempotencyKey(),
            });
            toast.success(`Project ${name} 已创建`);
          } else if (nameDialog?.target) {
            await updateProject.mutateAsync({
              projectId: nameDialog.target.id,
              name,
              status: nameDialog.target.status,
            });
            toast.success("Project 已重命名");
          }
          setNameDialog(null);
        }}
      />

      <SensitiveActionDialog
        open={Boolean(statusTarget)}
        onOpenChange={(open) => !open && setStatusTarget(null)}
        title={statusTarget?.status === "active" ? "停用 Project" : "恢复 Project"}
        destructive={statusTarget?.status === "active"}
        scopeLines={[
          { label: "Tenant", name: tenantName ?? "", id: tenantId },
          { label: "Project", name: statusTarget?.name ?? "", id: statusTarget?.id },
        ]}
        impacts={
          statusTarget?.status === "active"
            ? ["停用后不能创建集群接入凭证", "已有集群保持现状，但不可再新增接入"]
            : ["Project 恢复为启用状态"]
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
            toast.success("Project 状态已更新");
            setStatusTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />

      <SensitiveActionDialog
        open={Boolean(retireTarget)}
        onOpenChange={(open) => !open && setRetireTarget(null)}
        title="退役 Project"
        destructive
        description="退役会停用 Project，并撤销其下集群的接入身份与未使用凭证。"
        scopeLines={[
          { label: "Tenant", name: tenantName ?? "", id: tenantId },
          { label: "Project", name: retireTarget?.name ?? "", id: retireTarget?.id },
        ]}
        impacts={[
          "Project 被停用",
          "其下全部 Cluster 的 Agent 接入身份被撤销，已连接的 Agent 将断开",
          "未使用的接入凭证全部失效",
          "该操作会写入审计记录，且不可自动回滚",
        ]}
        confirmationText={retireTarget?.name}
        confirmLabel="确认退役"
        pending={deleteProject.isPending}
        error={deleteProject.error}
        onConfirm={async () => {
          if (!retireTarget) {
            return;
          }
          try {
            await deleteProject.mutateAsync({ projectId: retireTarget.id });
            toast.success("Project 已退役");
            setRetireTarget(null);
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
  onClose,
  onSubmit,
}: {
  state: NameDialogState;
  title: string;
  pending: boolean;
  onClose: () => void;
  onSubmit: (name: string) => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [initialized, setInitialized] = useState<string | null>(null);

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
        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={pending}>
            取消
          </Button>
          <Button
            variant="primary"
            disabled={pending || name.trim().length === 0}
            onClick={() => {
              void onSubmit(name.trim()).catch(() => undefined);
            }}
          >
            {pending ? "提交中…" : "确认"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
