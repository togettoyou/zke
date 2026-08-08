import { useCallback, useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import {
  Check,
  ChevronDown,
  ClipboardList,
  KeyRound,
  MoreHorizontal,
  Search,
  ShieldCheck,
  ShieldHalf,
  UserPlus,
  Users,
} from "lucide-react";
import { toast } from "sonner";

import { useAuditActions, useAuditEvents, type AuditFilters } from "@/api/queries/audit";
import { useProjects, useTenants } from "@/api/queries/resources";
import {
  useCreateRoleBinding,
  useCreateUser,
  useDeleteRoleBinding,
  useDeleteUser,
  useResetUserPassword,
  useRoleBindings,
  useSetUserStatus,
  useUnlockUser,
  useUpdateUser,
  useUsers,
} from "@/api/queries/access";
import {
  useCreateRole,
  useDeleteRole,
  usePermissions,
  useRoles,
  useUpdateRole,
} from "@/api/queries/roles";
import {
  DEFAULT_PAGE_SIZE,
  type AuditEvent,
  type ManagedUser,
  type PermissionDescriptor,
  type Role,
  type RoleBinding,
  type RoleName,
  type ScopeType,
  type UserStatus,
} from "@/api/types";
import { AppShell, SectionTitle, type AppNavItem } from "@/apps/AppShell";
import type { AppComponentProps } from "@/apps/types";
import { BUILTIN_ADMIN_ROLE, roleLabel } from "@/auth/capabilities";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
import { errorMessage } from "@/api/errors";
import { notifyFailure } from "@/components/common/notify";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import {
  AbsoluteTime,
  IdentifierLabel,
  RelativeTime,
  StatusBadge,
} from "@/components/common/status";
import { Badge } from "@/components/ui/badge";
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
import { Alert } from "@/components/ui/misc";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/cn";
import { useDebouncedValue } from "@/lib/use-debounced-value";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

const NAV: AppNavItem[] = [
  { id: "users", label: "用户", icon: Users },
  // Roles sit before the bindings that point at them: an operator defines what a
  // permission set is, then decides who holds it.
  { id: "roles", label: "角色", icon: ShieldHalf },
  { id: "role-bindings", label: "权限绑定", icon: ShieldCheck },
  { id: "audit", label: "审计事件", icon: ClipboardList },
];

const GLOBAL = { type: "global" } as const;

/** One spelling of the scope names, so the table, the filter and the
 *  confirmation dialog cannot drift apart. */
/*
 * Group labels for the audit action filter. The names themselves stay in their
 * raw form — the table renders `action` verbatim, so translating it in the
 * picker would mean choosing one string and reading another.
 *
 * Labels only. The order and the set of groups come from the Server's response,
 * because a list held here would be a second definition: a group the Server adds
 * would be absent from it, and every action in that group would silently vanish
 * from the picker — the exact failure the endpoint exists to prevent. An
 * unlabelled group falls back to its raw name, which is worse-looking than a
 * translation and better than an invisible one.
 */
const ACTION_GROUP_LABELS: Record<string, string> = {
  auth: "认证",
  user: "用户",
  role: "角色",
  role_binding: "权限绑定",
  tenant: "租户",
  project: "项目",
  cluster: "集群",
  kubernetes_resource: "Kubernetes 资源",
  denied: "权限拒绝",
};

/*
 * Permission labels.
 *
 * Names only — the set itself comes from the Server, because a list held here
 * would be a second definition and a permission added to the Server would be one
 * no role could be given, with nothing reporting it. An unlabelled permission
 * falls back to its raw name, which is what the API, the audit trail and the
 * docs call it anyway.
 *
 * A label says what the permission opens, not what it is most dangerous for —
 * that belongs in PERMISSION_WARNINGS, where an operator reads it next to the
 * checkbox. Naming the narrowest capability instead understates the grant.
 *
 * The three families that carry their own Cluster permissions — resource, rbac
 * and secret — are labelled "Kubernetes X", so a role editor shows at a glance
 * which entries are about objects in the target cluster rather than about ZKE's
 * own tenants, projects, users and roles. Pod logs and the Pod terminal are left
 * unqualified: nothing on the ZKE side is called a Pod, so there is nothing to
 * tell them apart from.
 */
const PERMISSION_LABELS: Record<string, string> = {
  "tenant.create": "创建租户",
  "tenant.read": "查看租户",
  "tenant.manage": "管理租户",
  "project.create": "创建项目",
  "project.read": "查看项目",
  "project.manage": "管理项目",
  "cluster.enrollment.create": "创建集群注册凭证",
  "cluster.enrollment.read": "查看集群注册凭证",
  "cluster.enrollment.revoke": "吊销集群注册凭证",
  "cluster.read": "查看集群",
  "cluster.pod.logs.read": "查看 Pod 日志",
  "cluster.pod.exec": "进入 Pod 终端",
  "cluster.pod.terminal_recording.create": "录制 Pod 终端输出",
  "cluster.pod.terminal_recording.read": "查看 Pod 终端录制",
  "cluster.pod.port_forward": "转发 Pod 端口",
  "cluster.node.drain": "排空节点",
  "cluster.event.read": "查看集群事件",
  "cluster.manage": "管理集群",
  "cluster.namespace.manage": "创建和删除 Kubernetes 命名空间",
  "cluster.resource.create": "创建 Kubernetes 资源",
  "cluster.resource.update": "修改 Kubernetes 资源",
  "cluster.resource.delete": "删除 Kubernetes 资源",
  "cluster.rbac.read": "查看 Kubernetes 授权资源",
  "cluster.rbac.manage": "管理 Kubernetes 授权资源",
  "cluster.secret.read": "查看 Kubernetes Secret",
  "cluster.secret.manage": "管理 Kubernetes Secret",
  "cluster.connection.revoke": "断开 Agent 连接",
  "user.read": "查看用户",
  "user.manage": "管理用户",
  "rbac.read": "查看角色与绑定",
  "rbac.manage": "管理角色与绑定",
  "audit.read": "查看审计事件",
};

function permissionLabel(name: string): string {
  return PERMISSION_LABELS[name] ?? name;
}

/*
 * Permissions whose reach is worth naming in the role editor.
 *
 * Not a security control — the Server decides — but a role is written once and
 * lived with for a long time, and these four are the ones whose consequences an
 * operator is least likely to reconstruct from the name alone.
 */
const PERMISSION_WARNINGS: Record<string, string> = {
  "cluster.secret.read": "可读取 Secret 明文取值",
  "cluster.secret.manage": "可修改和删除 Secret",
  "cluster.pod.exec": "可进入容器终端",
  "cluster.pod.terminal_recording.create": "可持久化终端输出，内容可能包含敏感信息",
  "cluster.pod.terminal_recording.read": "可读取同一项目范围内的历史终端输出",
  "cluster.pod.port_forward": "可访问 Pod 内部监听端口",
  "cluster.node.drain": "可排空节点并驱逐 Pod",
  "cluster.namespace.manage": "删除命名空间会连同其中的全部对象一起移除",
  "rbac.manage": "可创建角色并授予自己已持有的权限",
};

/**
 * Permissions that together reach Secret values without `cluster.secret.read`.
 *
 * Creating a workload is enough to mount any Secret in the Namespace, and either
 * of the two Pod permissions then reads it out of the running container. That is
 * Kubernetes' own equivalence — `kubectl` behaves the same way — so it is not
 * something to refuse here. What can be fixed is that the role editor showed no
 * sign of it: withholding `cluster.secret.read` looked like withholding Secret
 * access, and it is not.
 */
const SECRET_REACHING_PERMISSIONS = ["cluster.pod.logs.read", "cluster.pod.exec"];

/**
 * How a permission's scope floor reads on a badge.
 *
 * The floor is a scope name and not a flag because there are two of them:
 * `user.manage` reaches nothing below global, `project.create` nothing below
 * tenant. A role bound to a Project that carries only the latter grants exactly
 * nothing, and the old boolean had no way to say so.
 */
function scopeFloorLabel(minimumScope: string): string {
  return minimumScope === "global" ? "仅全局生效" : "仅全局和租户生效";
}

const SCOPE_BREADTH: Record<string, number> = { global: 2, tenant: 1, project: 0 };

/**
 * Mirrors `rbac.InertAt` in `pkg/server/rbac/service.go`: a binding grants
 * nothing by carrying a permission whose floor is wider than the binding's own
 * scope. An unknown floor — a permission this Console build predates — is
 * treated as reaching everywhere, so the form claims nothing it cannot support.
 */
function scopeIsBelowFloor(scopeType: string, minimumScope: string | undefined): boolean {
  if (minimumScope === undefined) {
    return false;
  }
  return (SCOPE_BREADTH[scopeType] ?? 0) < (SCOPE_BREADTH[minimumScope] ?? 0);
}

const SCOPE_LABELS: Record<string, string> = {
  global: "全局",
  tenant: "租户",
  project: "项目",
};

export function AccessAuditApp(_props: AppComponentProps) {
  const { permissions } = useSessionContext();
  const [section, setSection] = useState("users");

  const nav = NAV.map((item) => ({
    ...item,
    hidden:
      (item.id === "users" && !permissions.can("user.read", GLOBAL)) ||
      (item.id === "roles" && !permissions.can("rbac.read", GLOBAL)) ||
      (item.id === "role-bindings" && !permissions.can("rbac.read", GLOBAL)) ||
      (item.id === "audit" && !permissions.canAnywhere("audit.read")),
  }));

  const firstVisible = nav.find((item) => !item.hidden)?.id;
  const active = nav.find((item) => item.id === section && !item.hidden) ? section : firstVisible;

  if (!firstVisible || !active) {
    return (
      <div className="p-4">
        <Alert tone="warning">
          当前账号没有用户、角色、角色绑定或审计的读取权限。相关入口已隐藏，服务端也会拒绝对应请求。
        </Alert>
      </div>
    );
  }

  return (
    <AppShell nav={nav} activeId={active} onNavigate={setSection}>
      {active === "users" ? <UserSection /> : null}
      {active === "roles" ? <RoleSection /> : null}
      {active === "role-bindings" ? <RoleBindingSection /> : null}
      {active === "audit" ? <AuditSection /> : null}
    </AppShell>
  );
}

function UserSection() {
  const { permissions, session } = useSessionContext();
  const canManage = permissions.can("user.manage", GLOBAL);

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
  const [createOpen, setCreateOpen] = useState(false);
  const [renameTarget, setRenameTarget] = useState<ManagedUser | null>(null);
  const [statusTarget, setStatusTarget] = useState<ManagedUser | null>(null);
  const [unlockTarget, setUnlockTarget] = useState<ManagedUser | null>(null);
  const [resetTarget, setResetTarget] = useState<ManagedUser | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ManagedUser | null>(null);

  const query = useUsers({
    limit: DEFAULT_PAGE_SIZE,
    offset,
    ...(debouncedSearch ? { q: debouncedSearch } : {}),
    ...(status === "all" ? {} : { status: status as UserStatus }),
  });

  const createUser = useCreateUser();
  const updateUser = useUpdateUser();
  const setUserStatus = useSetUserStatus();
  const unlockUser = useUnlockUser();
  const resetPassword = useResetUserPassword();
  const deleteUser = useDeleteUser();

  const currentUserId = session?.user.id;

  // Which of these accounts are global administrators.
  //
  // Marked rather than hidden, and the choice is deliberate. Seven write paths
  // refuse a non-administrator acting on one of these accounts, and each refusal
  // already tells the caller what the account is — the guards are the disclosure,
  // so concealing the list would be a curtain in front of an open door. What was
  // actually missing is that the refusals arrived with no warning: nothing on
  // the row said why this user, and not the one above it, could not be renamed.
  //
  // Needs `rbac.read`, which `user.read` does not imply, so the query is gated
  // and its absence degrades to an unmarked list rather than an error.
  const canReadBindings = permissions.can("rbac.read", GLOBAL);
  const adminBindings = useRoleBindings(
    { limit: DEFAULT_PAGE_SIZE, offset: 0, role: BUILTIN_ADMIN_ROLE, scope_type: "global" },
    canReadBindings,
  );
  const globalAdminIds = useMemo(
    () => new Set((adminBindings.data?.role_bindings ?? []).map((binding) => binding.subject_id)),
    [adminBindings.data],
  );

  // Opening a dialog clears what the previous attempt left behind.
  //
  // A mutation holds its error until it is reset or runs again, and these
  // dialogs are opened by setting a target — nothing about opening one touches
  // the mutation behind it. So a refusal stayed on screen for the rest of the
  // session: reopen the dialog, open a different one, act on a different user,
  // and the same red box was still there describing an operation that was not
  // the one in front of the operator. Only one of them is open at a time, so
  // they are cleared together rather than each open site remembering its own.
  const clearActionErrors = useCallback(() => {
    createUser.reset();
    updateUser.reset();
    setUserStatus.reset();
    unlockUser.reset();
    resetPassword.reset();
    deleteUser.reset();
  }, [createUser, updateUser, setUserStatus, unlockUser, resetPassword, deleteUser]);

  const columns = useMemo<ColumnDef<ManagedUser, unknown>[]>(
    () => [
      {
        header: "用户",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="flex flex-wrap items-center gap-2">
              <span className="text-foreground font-medium">{row.original.display_name}</span>
              {globalAdminIds.has(row.original.id) ? (
                <Badge tone="warning">全局管理员</Badge>
              ) : null}
            </span>
            {/* The id belongs here for the same reason it is on every other row
                in this application: it is what an audit event, a role binding
                and a support request all refer to, and it was the one table that
                did not offer it. On the username's line, so the row stays two
                lines deep. */}
            <span className="flex items-center gap-2">
              <span className="zke-mono text-muted-foreground text-xs">
                {row.original.username}
              </span>
              <IdentifierLabel value={row.original.id} />
            </span>
          </div>
        ),
      },
      {
        header: "状态",
        size: 168,
        /*
         * The lock expiry lives here rather than in a column of its own.
         *
         * `lock_expires_at` is null for every account that is not locked, so as
         * a standalone column it was 130px of em-dash on every healthy row — a
         * column whose normal state is empty is asking to be read as broken.
         * Attached to the status it qualifies, it appears exactly when there is
         * something to say, and the two facts about a locked account — that it
         * is locked, and until when — are read together.
         */
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <StatusBadge kind="user" value={row.original.status} />
            {row.original.lock_expires_at ? (
              <span className="text-subtle-foreground text-xs">
                锁定至 <RelativeTime value={row.original.lock_expires_at} className="inline" />
              </span>
            ) : null}
            {row.original.failed_login_count > 0 ? (
              <span className="text-subtle-foreground text-xs">
                连续失败 {row.original.failed_login_count} 次
              </span>
            ) : null}
          </div>
        ),
      },
      {
        header: "密码更新",
        size: 130,
        cell: ({ row }) => <RelativeTime value={row.original.password_changed_at} />,
      },
      {
        id: "actions",
        header: "",
        size: 56,
        /*
         * One menu, not five buttons.
         *
         * Laid out in the row, these actions took a third of the table's width
         * and repeated on every line — and they flattened the hierarchy that
         * matters most here: "改显示名" and "删除" were the same control at the
         * same weight, differing only in the colour of one of them. Behind a
         * menu the row goes quiet, the destructive item sits below a separator
         * in its own tone, and the column costs 56px instead of 320.
         */
        cell: ({ row }) => {
          const user = row.original;
          if (!canManage) {
            return null;
          }
          const disabling = user.status !== "disabled";
          return (
            <div className="flex justify-end">
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button size="icon-sm" variant="ghost" aria-label={`${user.display_name} 的操作`}>
                    <MoreHorizontal />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-40">
                  <DropdownMenuItem
                    onSelect={() => {
                      clearActionErrors();
                      setRenameTarget(user);
                    }}
                  >
                    改显示名
                  </DropdownMenuItem>
                  {user.status === "locked" ? (
                    <DropdownMenuItem
                      onSelect={() => {
                        clearActionErrors();
                        setUnlockTarget(user);
                      }}
                    >
                      解锁
                    </DropdownMenuItem>
                  ) : null}
                  <DropdownMenuItem
                    onSelect={() => {
                      clearActionErrors();
                      setResetTarget(user);
                    }}
                  >
                    重置密码
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    disabled={user.id === currentUserId && disabling}
                    onSelect={() => {
                      clearActionErrors();
                      setStatusTarget(user);
                    }}
                  >
                    {disabling ? "禁用" : "启用"}
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    variant="danger"
                    onSelect={() => {
                      clearActionErrors();
                      setDeleteTarget(user);
                    }}
                  >
                    删除
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          );
        },
      },
    ],
    [canManage, currentUserId, clearActionErrors, globalAdminIds],
  );

  return (
    <>
      {/* A full-height column, so the table below can take the remaining space
          and scroll inside itself. Left to grow, the whole view scrolls and the
          table's sticky header sticks to nothing. */}
      <div className="flex h-full min-h-0 flex-col">
        <SectionTitle
          title="用户"
          description="本地用户使用 Argon2id 摘要存储；禁用、删除与重置密码都会撤销该用户的全部会话"
          actions={
            canManage ? (
              <Button
                size="sm"
                variant="primary"
                onClick={() => {
                  clearActionErrors();
                  setCreateOpen(true);
                }}
              >
                <UserPlus />
                新建用户
              </Button>
            ) : null
          }
        />

        <DataTable
          columns={columns}
          data={query.data?.users}
          isLoading={query.isLoading}
          isFetching={query.isFetching}
          error={query.error}
          onRetry={() => void query.refetch()}
          rowKey={(row) => row.id}
          emptyTitle="没有匹配的用户"
          toolbar={
            <>
              <Input
                className="max-w-56"
                placeholder="按用户名或显示名搜索"
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
                  <SelectItem value="all">全部状态</SelectItem>
                  <SelectItem value="active">正常</SelectItem>
                  <SelectItem value="locked">已锁定</SelectItem>
                  <SelectItem value="disabled">已禁用</SelectItem>
                </SelectContent>
              </Select>
            </>
          }
          pagination={{ value: query.data?.pagination, onOffsetChange: setOffset }}
        />
      </div>

      <CreateUserDialog
        open={createOpen}
        pending={createUser.isPending}
        onClose={() => setCreateOpen(false)}
        onSubmit={async (input) => {
          await createUser.mutateAsync(input);
          toast.success(`用户 ${input.username} 已创建`);
          setCreateOpen(false);
        }}
      />

      <RenameUserDialog
        user={renameTarget}
        pending={updateUser.isPending}
        onClose={() => setRenameTarget(null)}
        onSubmit={async (displayName) => {
          if (!renameTarget) {
            return;
          }
          await updateUser.mutateAsync({ userId: renameTarget.id, displayName });
          toast.success("显示名已更新");
          setRenameTarget(null);
        }}
      />

      <SensitiveActionDialog
        open={Boolean(statusTarget)}
        onOpenChange={(open) => !open && setStatusTarget(null)}
        title={statusTarget?.status === "disabled" ? "启用用户" : "禁用用户"}
        destructive={statusTarget?.status !== "disabled"}
        scopeLines={[
          {
            label: "用户",
            name: `${statusTarget?.display_name ?? ""}（${statusTarget?.username ?? ""}）`,
            id: statusTarget?.id,
          },
        ]}
        impacts={
          statusTarget?.status === "disabled"
            ? ["用户恢复为正常状态，可重新登录"]
            : ["用户无法登录", "该用户的全部现有会话立即被撤销", "已授予的角色绑定保持不变"]
        }
        confirmationText={statusTarget?.status === "disabled" ? undefined : statusTarget?.username}
        pending={setUserStatus.isPending}
        error={setUserStatus.error}
        onConfirm={async () => {
          if (!statusTarget) {
            return;
          }
          try {
            await setUserStatus.mutateAsync({
              userId: statusTarget.id,
              status: statusTarget.status === "disabled" ? "active" : "disabled",
            });
            toast.success("用户状态已更新");
            setStatusTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />

      <SensitiveActionDialog
        open={Boolean(unlockTarget)}
        onOpenChange={(open) => !open && setUnlockTarget(null)}
        title="解锁用户"
        scopeLines={[
          {
            label: "用户",
            name: `${unlockTarget?.display_name ?? ""}（${unlockTarget?.username ?? ""}）`,
            id: unlockTarget?.id,
          },
        ]}
        impacts={["清除连续失败计数", "账户立即恢复为正常状态"]}
        pending={unlockUser.isPending}
        error={unlockUser.error}
        onConfirm={async () => {
          if (!unlockTarget) {
            return;
          }
          try {
            await unlockUser.mutateAsync({ userId: unlockTarget.id });
            toast.success("用户已解锁");
            setUnlockTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />

      <ResetPasswordDialog
        user={resetTarget}
        pending={resetPassword.isPending}
        error={resetPassword.error}
        onClose={() => setResetTarget(null)}
        onSubmit={async (password) => {
          if (!resetTarget) {
            return;
          }
          await resetPassword.mutateAsync({ userId: resetTarget.id, password });
          toast.success("密码已重置，该用户的全部会话已撤销");
          setResetTarget(null);
        }}
      />

      <SensitiveActionDialog
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title="删除用户"
        destructive
        description="用户记录将被永久删除且无法恢复，历史审计记录仍会保留。"
        scopeLines={[
          {
            label: "用户",
            name: `${deleteTarget?.display_name ?? ""}（${deleteTarget?.username ?? ""}）`,
            id: deleteTarget?.id,
          },
        ]}
        impacts={[
          "用户记录和全部现有会话被永久删除",
          "该用户的全部角色绑定被永久删除",
          "用户名随之释放，可由新用户重新使用",
          "服务端会保留最后一个有效的全局管理员，必要时会拒绝该操作",
        ]}
        confirmationText={deleteTarget?.username}
        confirmLabel="确认删除"
        pending={deleteUser.isPending}
        error={deleteUser.error}
        onConfirm={async () => {
          if (!deleteTarget) {
            return;
          }
          try {
            await deleteUser.mutateAsync({ userId: deleteTarget.id });
            toast.success("用户已删除");
            setDeleteTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />
    </>
  );
}

function CreateUserDialog({
  open,
  pending,
  onClose,
  onSubmit,
}: {
  open: boolean;
  pending: boolean;
  onClose: () => void;
  onSubmit: (input: { username: string; displayName: string; password: string }) => Promise<void>;
}) {
  const [username, setUsername] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");

  const tooShort = password.length > 0 && password.length < 15;

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          setUsername("");
          setDisplayName("");
          setPassword("");
          onClose();
        }
      }}
    >
      <DialogContent aria-describedby={undefined}>
        <DialogHeader>
          <DialogTitle>新建用户</DialogTitle>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label htmlFor="new-username">用户名</Label>
            <Input
              id="new-username"
              autoComplete="off"
              value={username}
              onChange={(event) => setUsername(event.target.value)}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="new-display-name">显示名</Label>
            <Input
              id="new-display-name"
              value={displayName}
              maxLength={253}
              onChange={(event) => setDisplayName(event.target.value)}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="new-user-password">初始密码</Label>
            <Input
              id="new-user-password"
              type="password"
              autoComplete="new-password"
              value={password}
              aria-invalid={tooShort}
              onChange={(event) => setPassword(event.target.value)}
            />
            <FieldHint>至少 15 个字符。请通过安全渠道告知用户，并要求其首次登录后修改。</FieldHint>
          </div>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={pending}>
            取消
          </Button>
          <Button
            variant="primary"
            disabled={pending || !username.trim() || !displayName.trim() || password.length < 15}
            onClick={() => {
              void onSubmit({
                username: username.trim(),
                displayName: displayName.trim(),
                password,
              })
                .then(() => {
                  setUsername("");
                  setDisplayName("");
                  setPassword("");
                })
                .catch((error: unknown) => notifyFailure("创建用户失败", error));
            }}
          >
            {pending ? "创建中…" : "创建"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function RenameUserDialog({
  user,
  pending,
  onClose,
  onSubmit,
}: {
  user: ManagedUser | null;
  pending: boolean;
  onClose: () => void;
  onSubmit: (displayName: string) => Promise<void>;
}) {
  const [value, setValue] = useState("");
  const [initializedFor, setInitializedFor] = useState<string | null>(null);

  if (user && initializedFor !== user.id) {
    setInitializedFor(user.id);
    setValue(user.display_name);
  }
  if (!user && initializedFor !== null) {
    setInitializedFor(null);
  }

  return (
    <Dialog open={Boolean(user)} onOpenChange={(open) => !open && onClose()}>
      <DialogContent aria-describedby={undefined}>
        <DialogHeader>
          <DialogTitle>修改显示名</DialogTitle>
        </DialogHeader>
        <div className="grid gap-1.5">
          <Label htmlFor="user-display-name">显示名</Label>
          <Input
            id="user-display-name"
            value={value}
            maxLength={253}
            onChange={(event) => setValue(event.target.value)}
          />
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={pending}>
            取消
          </Button>
          <Button
            variant="primary"
            disabled={pending || value.trim().length === 0}
            onClick={() => {
              void onSubmit(value.trim()).catch((error: unknown) =>
                notifyFailure("修改失败", error),
              );
            }}
          >
            确认
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ResetPasswordDialog({
  user,
  pending,
  error,
  onClose,
  onSubmit,
}: {
  user: ManagedUser | null;
  pending: boolean;
  error: unknown;
  onClose: () => void;
  onSubmit: (password: string) => Promise<void>;
}) {
  const [password, setPassword] = useState("");

  return (
    <SensitiveActionDialog
      open={Boolean(user)}
      onOpenChange={(open) => {
        if (!open) {
          setPassword("");
          onClose();
        }
      }}
      title="重置用户密码"
      destructive
      scopeLines={[
        {
          label: "用户",
          name: `${user?.display_name ?? ""}（${user?.username ?? ""}）`,
          id: user?.id,
        },
      ]}
      impacts={[
        "该用户的全部现有会话立即被撤销",
        "用户必须使用新密码重新登录",
        "新密码不会在任何列表或日志中再次显示",
      ]}
      confirmationText={user?.username}
      confirmLabel="重置密码"
      pending={pending}
      error={error}
      onConfirm={() => {
        void onSubmit(password)
          .then(() => setPassword(""))
          .catch(() => undefined);
      }}
    >
      <div className="grid gap-1.5">
        <Label htmlFor="reset-password">新密码</Label>
        <Input
          id="reset-password"
          type="password"
          autoComplete="new-password"
          value={password}
          aria-invalid={password.length > 0 && password.length < 15}
          onChange={(event) => setPassword(event.target.value)}
        />
        <FieldHint>至少 15 个字符，请通过安全渠道传递。</FieldHint>
      </div>
    </SensitiveActionDialog>
  );
}

function RoleSection() {
  const { permissions } = useSessionContext();
  const canManage = permissions.can("rbac.manage", GLOBAL);

  const [offset, setOffset] = useState(0);
  const [editorTarget, setEditorTarget] = useState<Role | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<Role | null>(null);

  const query = useRoles({ limit: DEFAULT_PAGE_SIZE, offset });
  const createRole = useCreateRole();
  const updateRole = useUpdateRole();
  const deleteRole = useDeleteRole();

  // See the note in the user section: opening a dialog does not touch the
  // mutation behind it, so the last refusal outlives the attempt that produced
  // it unless it is cleared here.
  const clearActionErrors = useCallback(() => {
    createRole.reset();
    updateRole.reset();
    deleteRole.reset();
  }, [createRole, updateRole, deleteRole]);

  const columns = useMemo<ColumnDef<Role, unknown>[]>(
    () => [
      {
        header: "角色",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="flex items-center gap-2">
              <span className="text-foreground font-medium">{row.original.display_name}</span>
              {row.original.builtin ? <Badge tone="neutral">内置</Badge> : null}
            </span>
            <span className="zke-mono text-muted-foreground text-xs">{row.original.name}</span>
          </div>
        ),
      },
      {
        header: "说明",
        cell: ({ row }) => (
          <span className="text-muted-foreground text-[13px]">
            {row.original.description || "—"}
          </span>
        ),
      },
      {
        header: "权限",
        size: 110,
        cell: ({ row }) => (
          <span className="text-foreground text-[13px]">{row.original.permissions.length} 项</span>
        ),
      },
      {
        header: "绑定",
        size: 90,
        cell: ({ row }) => (
          <span className="text-foreground text-[13px]">{row.original.binding_count}</span>
        ),
      },
      {
        id: "actions",
        header: "",
        size: 130,
        cell: ({ row }) => (
          <div className="flex justify-end gap-1">
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                clearActionErrors();
                setEditorTarget(row.original);
              }}
            >
              {canManage && !row.original.builtin ? "编辑" : "查看"}
            </Button>
            {canManage && !row.original.builtin ? (
              <Button
                size="sm"
                variant="ghost"
                className="text-danger"
                onClick={() => {
                  clearActionErrors();
                  setDeleteTarget(row.original);
                }}
              >
                删除
              </Button>
            ) : null}
          </div>
        ),
      },
    ],
    [canManage, clearActionErrors],
  );

  return (
    <>
      <div className="flex h-full min-h-0 flex-col">
        <SectionTitle
          title="角色"
          description="角色是一组权限的集合。修改角色会立即改变所有已绑定该角色的用户的权限"
          actions={
            canManage ? (
              <Button
                size="sm"
                variant="primary"
                onClick={() => {
                  clearActionErrors();
                  setCreateOpen(true);
                }}
              >
                <ShieldHalf />
                新建角色
              </Button>
            ) : null
          }
        />

        <DataTable
          columns={columns}
          data={query.data?.roles}
          isLoading={query.isLoading}
          isFetching={query.isFetching}
          error={query.error}
          onRetry={() => void query.refetch()}
          rowKey={(row) => row.id}
          emptyTitle="没有角色"
          pagination={{ value: query.data?.pagination, onOffsetChange: setOffset }}
        />
      </div>

      <RoleEditorDialog
        open={createOpen}
        role={null}
        pending={createRole.isPending}
        error={createRole.error}
        onClose={() => setCreateOpen(false)}
        onSubmit={async (input) => {
          await createRole.mutateAsync({
            name: input.name,
            displayName: input.displayName,
            description: input.description,
            permissions: input.permissions,
          });
          toast.success("角色已创建");
          setCreateOpen(false);
        }}
      />

      <RoleEditorDialog
        open={Boolean(editorTarget)}
        role={editorTarget}
        readOnly={!canManage || Boolean(editorTarget?.builtin)}
        pending={updateRole.isPending}
        error={updateRole.error}
        onClose={() => setEditorTarget(null)}
        onSubmit={async (input) => {
          if (!editorTarget) {
            return;
          }
          await updateRole.mutateAsync({
            roleId: editorTarget.id,
            displayName: input.displayName,
            description: input.description,
            permissions: input.permissions,
          });
          toast.success("角色已更新");
          setEditorTarget(null);
        }}
      />

      <SensitiveActionDialog
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title="删除角色"
        destructive
        scopeLines={[
          {
            label: "角色",
            name: `${deleteTarget?.display_name ?? ""}（${deleteTarget?.name ?? ""}）`,
            id: deleteTarget?.id ?? null,
          },
        ]}
        impacts={[
          "角色定义被移除，无法恢复",
          "仍被绑定的角色不能删除，服务端会拒绝并要求先删除相关绑定",
        ]}
        confirmLabel="确认删除"
        pending={deleteRole.isPending}
        error={deleteRole.error}
        onConfirm={async () => {
          if (!deleteTarget) {
            return;
          }
          try {
            await deleteRole.mutateAsync({ roleId: deleteTarget.id });
            toast.success("角色已删除");
            setDeleteTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />
    </>
  );
}

/*
 * One dialog for creating, editing and viewing a role.
 *
 * The three differ in two details — whether the name is editable and whether
 * anything is — and splitting them into separate components meant the permission
 * picker existed twice, which is the part worth getting right once. A builtin
 * role opens here read-only rather than not opening at all: what `admin` grants
 * is exactly the question an operator asks before deciding they need a narrower
 * role.
 */
function RoleEditorDialog({
  open,
  role,
  readOnly = false,
  pending,
  error,
  onClose,
  onSubmit,
}: {
  open: boolean;
  role: Role | null;
  readOnly?: boolean;
  pending: boolean;
  error: unknown;
  onClose: () => void;
  onSubmit: (input: {
    name: string;
    displayName: string;
    description: string;
    permissions: string[];
  }) => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [description, setDescription] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [wasOpen, setWasOpen] = useState(open);
  const [loadedRoleId, setLoadedRoleId] = useState<string | null>(null);

  const permissionsQuery = usePermissions(open);
  const catalog = permissionsQuery.data?.permissions ?? [];

  // Reset during render rather than in an effect, so the previous role's
  // permissions are never visible for a frame after the dialog reopens.
  if (wasOpen !== open) {
    setWasOpen(open);
    if (!open) {
      setLoadedRoleId(null);
    }
  }
  const targetId = open ? (role?.id ?? "new") : null;
  if (targetId !== null && targetId !== loadedRoleId) {
    setLoadedRoleId(targetId);
    setName(role?.name ?? "");
    setDisplayName(role?.display_name ?? "");
    setDescription(role?.description ?? "");
    setSelected(role?.permissions ?? []);
  }

  const editing = Boolean(role);
  const toggle = (permission: string) => {
    setSelected((current) =>
      current.includes(permission)
        ? current.filter((item) => item !== permission)
        : [...current, permission],
    );
  };

  /*
   * A permission the caller does not hold globally is frozen at whatever the
   * role already says about it — checked if the role carries it, unchecked if
   * not — because the Server judges an edit by what it *changes*. Adding one is
   * escalation; removing one is a revocation of authority the caller never had.
   * Leaving it exactly as it is, is neither, so it is the only thing the caller
   * may do with it.
   *
   * This also removes what used to be the only saveable edit of such a role. The
   * checkbox was disabled when unchecked but live when checked, so the sole way
   * to get a role past the old whole-set check was to strip every permission the
   * editor happened not to hold — the interface offered the destructive edit and
   * refused every other one.
   */
  const frozen = useMemo(() => new Set(role?.permissions ?? []), [role]);
  const movable = (permission: PermissionDescriptor) => !readOnly && permission.held;
  const valid =
    displayName.trim() !== "" &&
    selected.length > 0 &&
    (editing || /^[a-z0-9][a-z0-9-]{0,62}$/.test(name));
  const frozenCount = catalog.filter(
    (permission) => !permission.held && frozen.has(permission.name),
  ).length;

  // Whether this role reaches Secret values without asking for them. Shown, not
  // refused: the combination is legitimate and common — it is what running a
  // workload and debugging it looks like — and the point is that the person
  // writing the role finds out here rather than assuming the opposite.
  const secretReachingSelected = SECRET_REACHING_PERMISSIONS.filter((permission) =>
    selected.includes(permission),
  );
  const reachesSecretsIndirectly =
    selected.includes("cluster.resource.create") &&
    secretReachingSelected.length > 0 &&
    !selected.includes("cluster.secret.read");

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      {/*
       * The permission catalog is long enough to outgrow any viewport, so the
       * dialog is a column that scrolls in one place: the header names what is
       * open and the footer keeps 关闭 reachable, both without scrolling. The
       * list itself is deliberately not a second scroll region — nesting one
       * inside a scrolling dialog is how a wheel over the list stops moving
       * the page it is sitting on.
       */}
      <DialogContent
        aria-describedby={undefined}
        className="flex max-w-2xl flex-col overflow-hidden"
      >
        <DialogHeader className="shrink-0">
          <DialogTitle>{readOnly ? "角色详情" : editing ? "编辑角色" : "新建角色"}</DialogTitle>
        </DialogHeader>
        <div className="grid min-h-0 flex-1 auto-rows-min gap-3 overflow-y-auto">
          {readOnly && role?.builtin ? (
            <Alert tone="info">内置角色由 Server 定义，不可修改或删除。</Alert>
          ) : null}

          {/*
           * items-start, because only one of the two columns carries a hint:
           * a stretched cell hands its spare height to the gaps between label
           * and input, and the two inputs stop lining up.
           */}
          <div className="grid grid-cols-2 items-start gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="role-name">标识</Label>
              <Input
                id="role-name"
                value={name}
                disabled={editing || readOnly}
                placeholder="release-engineer"
                onChange={(event) => setName(event.target.value)}
              />
              <FieldHint>
                {editing
                  ? "标识创建后不可修改：绑定与审计记录都以它指代该角色。"
                  : "小写字母、数字和连字符。创建后不可修改。"}
              </FieldHint>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="role-display-name">名称</Label>
              <Input
                id="role-display-name"
                value={displayName}
                disabled={readOnly}
                placeholder="发布工程师"
                onChange={(event) => setDisplayName(event.target.value)}
              />
            </div>
          </div>

          <div className="grid gap-1.5">
            <Label htmlFor="role-description">说明</Label>
            <Input
              id="role-description"
              value={description}
              disabled={readOnly}
              placeholder="这个角色是给谁用的"
              onChange={(event) => setDescription(event.target.value)}
            />
          </div>

          <div className="grid gap-1.5">
            <Label>权限（已选 {selected.length} 项）</Label>
            {permissionsQuery.isLoading ? (
              <span className="text-muted-foreground text-[13px]">正在加载权限字典…</span>
            ) : (
              <div className="border-border rounded-control border">
                {catalog.map((permission) => (
                  <PermissionRow
                    key={permission.name}
                    permission={permission}
                    checked={selected.includes(permission.name)}
                    disabled={!movable(permission)}
                    onToggle={() => toggle(permission.name)}
                  />
                ))}
              </div>
            )}
            <FieldHint>
              只能改动自己在全局已持有的权限。
              {frozenCount > 0
                ? `该角色另有 ${frozenCount} 项权限当前账号未持有，它们保持原样，既不能移除也不能新增。`
                : null}
            </FieldHint>
          </div>

          {reachesSecretsIndirectly ? (
            <Alert tone="info">
              该角色未包含「查看 Kubernetes Secret」，但仍可间接读到 Secret 取值：持有「创建
              Kubernetes 资源」即可创建挂载任意 Secret 的工作负载，再用「
              {secretReachingSelected.map((permission) => permissionLabel(permission)).join("」「")}
              」把内容取出来。这是 Kubernetes 自身的权限等价关系，`kubectl` 同样如此。要真正隔离
              Secret，需要一并收紧工作负载创建权限。
            </Alert>
          ) : null}
        </div>

        {/*
         * Everything about whether the save works sits here, outside the scroll
         * region and directly above the button that produced it.
         *
         * Both used to be the last things inside the scrolling column, below a
         * permission list nearly thirty rows tall, while the button is in the
         * fixed footer. Clicking 保存 and being refused therefore rendered the
         * reason somewhere the operator was not looking and had no reason to
         * scroll to, and the refusal read as nothing happening at all. A message
         * that answers a click has to be reachable from where the click was.
         *
         * The note about Secret reach stays inside the list above: it describes
         * the selection rather than the outcome of pressing a button, and lifting
         * every alert out here would spend the dialog's fixed height on things
         * nobody is waiting for.
         */}
        {/*
         * The Server's own message is shown rather than a fixed one: the
         * refusals here are specific and actionable — which permission
         * exceeded the caller's ceiling, that the role is builtin, that it is
         * still bound — and replacing them with "保存失败" would throw away
         * the only part worth reading.
         */}
        {error ? (
          <Alert tone="danger" className="shrink-0">
            {errorMessage(error)}
          </Alert>
        ) : null}
        <DialogFooter className="shrink-0">
          <Button variant="secondary" onClick={onClose}>
            {readOnly ? "关闭" : "取消"}
          </Button>
          {readOnly ? null : (
            <Button
              variant="primary"
              disabled={!valid || pending}
              onClick={() => {
                void onSubmit({
                  name: name.trim(),
                  displayName: displayName.trim(),
                  description: description.trim(),
                  permissions: selected,
                });
              }}
            >
              {pending ? "保存中…" : "保存"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function PermissionRow({
  permission,
  checked,
  disabled,
  onToggle,
}: {
  permission: PermissionDescriptor;
  checked: boolean;
  disabled: boolean;
  onToggle: () => void;
}) {
  const warning = PERMISSION_WARNINGS[permission.name];
  return (
    <label
      className={cn(
        "border-border flex cursor-pointer items-start gap-3 border-b px-3 py-2 last:border-b-0",
        disabled && "cursor-not-allowed opacity-60",
      )}
    >
      <input
        type="checkbox"
        className="mt-1"
        checked={checked}
        disabled={disabled}
        onChange={onToggle}
      />
      <span className="flex min-w-0 flex-col gap-0.5">
        <span className="flex flex-wrap items-center gap-2">
          <span className="text-foreground text-[13px]">{permissionLabel(permission.name)}</span>
          {warning ? <Badge tone="warning">{warning}</Badge> : null}
          {/*
           * Shown on the permission rather than only on the binding form: the
           * role is written here, and this is the last point at which "this
           * role is for one tenant" and "this permission only works globally"
           * are both in front of the same person.
           */}
          {permission.minimum_scope !== "project" ? (
            <Badge tone="neutral">{scopeFloorLabel(permission.minimum_scope)}</Badge>
          ) : null}
          {!permission.held ? <Badge tone="neutral">当前账号未持有</Badge> : null}
        </span>
        <span className="zke-mono text-muted-foreground text-xs">{permission.name}</span>
      </span>
    </label>
  );
}

function RoleBindingSection() {
  const { permissions, session } = useSessionContext();
  const canManage = permissions.can("rbac.manage", GLOBAL);
  // Nothing in a binding row says whose access it is, so the one row an
  // operator must not delete looks exactly like the rest. The Server refuses it
  // either way; showing it disabled is what stops the click from being made.
  const selfSubjectId = session?.user.id ?? "";

  const [role, setRole] = useState("all");
  const [scopeType, setScopeType] = useState("all");
  const [offset, setOffset] = useState(0);
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<RoleBinding | null>(null);

  const query = useRoleBindings({
    limit: DEFAULT_PAGE_SIZE,
    offset,
    ...(role === "all" ? {} : { role: role as RoleName }),
    ...(scopeType === "all" ? {} : { scope_type: scopeType as ScopeType }),
  });
  // The filter offers whatever roles exist, not a list held here: a role an
  // operator created must be filterable, and one they deleted must stop being
  // offered. The page limit is generous because roles are few and the picker
  // has no paging of its own.
  const roles = useRoles({ limit: 100 });
  const roleOptions = useMemo(() => roles.data?.roles ?? [], [roles.data]);
  const createRoleBinding = useCreateRoleBinding();
  const deleteRoleBinding = useDeleteRoleBinding();

  // See the note in the user section.
  const clearActionErrors = useCallback(() => {
    createRoleBinding.reset();
    deleteRoleBinding.reset();
  }, [createRoleBinding, deleteRoleBinding]);

  const deleteSubject = deleteTarget?.subject;

  const columns = useMemo<ColumnDef<RoleBinding, unknown>[]>(
    () => [
      {
        header: "用户",
        /*
         * `subject` is resolved by the Server, in the same query that reads the
         * binding. Doing it here instead meant paging the user list into a map
         * and giving up past the page limit — a join the database does once,
         * reimplemented in the client and still incomplete.
         *
         * It is omitted only when the subject row is gone, and the identifier is
         * always present, so an orphaned binding stays visible and removable.
         */
        cell: ({ row }) => {
          const subject = row.original.subject;
          // The same two-line shape the users table uses, so the same person
          // looks the same in both places.
          return (
            <div className="flex flex-col gap-0.5">
              {subject ? (
                <span className="text-foreground font-medium">{subject.display_name}</span>
              ) : null}
              <span className="flex items-center gap-2">
                {subject ? (
                  <span className="zke-mono text-muted-foreground text-xs">{subject.username}</span>
                ) : null}
                <IdentifierLabel value={row.original.subject_id} />
              </span>
            </div>
          );
        },
      },
      {
        header: "角色",
        size: 140,
        // Labelled from the roles list where it resolves, and by the stored name
        // where it does not. A binding can outlive nothing here — the foreign key
        // sees to that — but the list is paged, so a role beyond the first page
        // still has to render as something an operator recognises.
        cell: ({ row }) => {
          const definition = roleOptions.find((item) => item.name === row.original.role);
          return (
            <Badge tone={row.original.role === "admin" ? "primary" : "neutral"}>
              {definition?.display_name ?? roleLabel(row.original.role)}
            </Badge>
          );
        },
      },
      {
        header: "作用域",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground text-[13px]">
              {SCOPE_LABELS[row.original.scope_type]}
            </span>
            {row.original.tenant_id ? <IdentifierLabel value={row.original.tenant_id} /> : null}
            {row.original.project_id ? <IdentifierLabel value={row.original.project_id} /> : null}
          </div>
        ),
      },
      {
        header: "创建时间",
        size: 130,
        cell: ({ row }) => <RelativeTime value={row.original.created_at} />,
      },
      {
        id: "actions",
        header: "",
        size: 90,
        cell: ({ row }) => {
          if (!canManage) {
            return null;
          }
          const isSelf = row.original.subject_id === selfSubjectId;
          return (
            <div className="flex justify-end">
              <Button
                size="sm"
                variant="ghost"
                className="text-danger"
                disabled={isSelf}
                title={isSelf ? "不能删除授予自己的权限绑定" : undefined}
                onClick={() => {
                  clearActionErrors();
                  setDeleteTarget(row.original);
                }}
              >
                删除
              </Button>
            </div>
          );
        },
      },
    ],
    [canManage, roleOptions, selfSubjectId, clearActionErrors],
  );

  return (
    <>
      <div className="flex h-full min-h-0 flex-col">
        <SectionTitle
          title="权限绑定"
          description="角色绑定决定用户在 Global、租户或项目作用域内的权限"
          actions={
            canManage ? (
              <Button
                size="sm"
                variant="primary"
                onClick={() => {
                  clearActionErrors();
                  setCreateOpen(true);
                }}
              >
                <KeyRound />
                新建绑定
              </Button>
            ) : null
          }
        />

        <DataTable
          columns={columns}
          data={query.data?.role_bindings}
          isLoading={query.isLoading}
          isFetching={query.isFetching}
          error={query.error}
          onRetry={() => void query.refetch()}
          rowKey={(row) => row.id}
          emptyTitle="没有匹配的角色绑定"
          toolbar={
            <>
              <Select
                value={role}
                onValueChange={(value) => {
                  setRole(value);
                  setOffset(0);
                }}
              >
                <SelectTrigger className="w-32">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">全部角色</SelectItem>
                  {roleOptions.map((item) => (
                    <SelectItem key={item.id} value={item.name}>
                      {item.display_name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Select
                value={scopeType}
                onValueChange={(value) => {
                  setScopeType(value);
                  setOffset(0);
                }}
              >
                <SelectTrigger className="w-36">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">全部作用域</SelectItem>
                  <SelectItem value="global">全局</SelectItem>
                  <SelectItem value="tenant">租户</SelectItem>
                  <SelectItem value="project">项目</SelectItem>
                </SelectContent>
              </Select>
            </>
          }
          pagination={{ value: query.data?.pagination, onOffsetChange: setOffset }}
        />
      </div>

      <CreateRoleBindingDialog
        open={createOpen}
        pending={createRoleBinding.isPending}
        error={createRoleBinding.error}
        onClose={() => setCreateOpen(false)}
        onSubmit={async (input) => {
          await createRoleBinding.mutateAsync(input);
          toast.success("角色绑定已创建");
          setCreateOpen(false);
        }}
      />

      <SensitiveActionDialog
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title="删除角色绑定"
        destructive
        scopeLines={[
          {
            label: "用户",
            // Named where the name is known, and always accompanied by the id:
            // this is the surface where an operator checks they are revoking
            // the right person's access.
            name: deleteSubject
              ? `${deleteSubject.display_name}（${deleteSubject.username}）`
              : (deleteTarget?.subject_id ?? ""),
            id: deleteSubject ? deleteTarget?.subject_id : null,
          },
          {
            label: "角色",
            name: `${roleLabel(deleteTarget?.role ?? "")} · ${SCOPE_LABELS[deleteTarget?.scope_type ?? "global"]}`,
            id: deleteTarget?.project_id ?? deleteTarget?.tenant_id ?? null,
          },
        ]}
        impacts={[
          "该用户立即失去此作用域内的权限",
          "服务端会保留最后一个有效的全局管理员，必要时会拒绝该操作",
        ]}
        confirmLabel="确认删除"
        pending={deleteRoleBinding.isPending}
        error={deleteRoleBinding.error}
        onConfirm={async () => {
          if (!deleteTarget) {
            return;
          }
          try {
            await deleteRoleBinding.mutateAsync({ roleBindingId: deleteTarget.id });
            toast.success("角色绑定已删除");
            setDeleteTarget(null);
          } catch {
            // Error is rendered inside the dialog.
          }
        }}
      />
    </>
  );
}

function CreateRoleBindingDialog({
  open,
  pending,
  error,
  onClose,
  onSubmit,
}: {
  open: boolean;
  pending: boolean;
  error: unknown;
  onClose: () => void;
  onSubmit: (input: {
    subjectId: string;
    role: RoleName;
    scopeType: ScopeType;
    tenantId?: string;
    projectId?: string;
  }) => Promise<void>;
}) {
  const [subject, setSubject] = useState<PickedRecord | null>(null);
  const [role, setRole] = useState<RoleName>("viewer");
  const [scopeType, setScopeType] = useState<ScopeType>("project");
  const [tenant, setTenant] = useState<PickedRecord | null>(null);
  const [project, setProject] = useState<PickedRecord | null>(null);
  const [userSearch, setUserSearch] = useState("");
  const debouncedUserSearch = useDebouncedValue(userSearch);
  const [wasOpen, setWasOpen] = useState(open);

  // Reset during render rather than in an effect, so a previous selection can
  // never be visible for a frame the next time the dialog opens.
  if (wasOpen !== open) {
    setWasOpen(open);
    if (!open) {
      setSubject(null);
      setRole("viewer");
      setScopeType("project");
      setTenant(null);
      setProject(null);
      setUserSearch("");
    }
  }

  const needsTenant = scopeType === "tenant" || scopeType === "project";
  const needsProject = scopeType === "project";

  const users = useUsers(
    {
      limit: 50,
      status: "active",
      ...(debouncedUserSearch.trim() ? { q: debouncedUserSearch.trim() } : {}),
    },
    open,
  );
  const tenants = useTenants({ limit: 100, status: "active" }, open && needsTenant);
  const projects = useProjects(open && needsProject ? (tenant?.id ?? null) : null, {
    limit: 100,
    status: "active",
  });

  const roles = useRoles({ limit: 100 }, open);
  const permissionsQuery = usePermissions(open);
  const held = useMemo(
    () =>
      new Set(
        (permissionsQuery.data?.permissions ?? [])
          .filter((permission) => permission.held)
          .map((permission) => permission.name),
      ),
    [permissionsQuery.data],
  );
  const allRoles = useMemo(() => roles.data?.roles ?? [], [roles.data]);
  const bindableRoles = useMemo(
    () => allRoles.filter((item) => item.permissions.every((permission) => held.has(permission))),
    [allRoles, held],
  );
  const hiddenRoleCount = allRoles.length - bindableRoles.length;

  // The default has to be a role that is actually on offer. `viewer` usually is,
  // but a caller whose own permissions do not cover it would otherwise open the
  // dialog on a selection they cannot submit.
  const selectableRole = bindableRoles.some((item) => item.name === role)
    ? role
    : (bindableRoles[0]?.name ?? "");
  if (selectableRole !== role) {
    setRole(selectableRole);
  }

  const valid =
    Boolean(subject) && role !== "" && (!needsTenant || tenant) && (!needsProject || project);

  // Which of the selected role's permissions this scope will not exercise.
  //
  // The Server refuses only a binding that reaches nothing at all, because a
  // partly-reachable one is a real grant — `admin` on a Tenant is most of
  // `admin`. That leaves the partial case to be shown rather than refused, and
  // this is the moment to show it: the scope and the role are both on screen,
  // and afterwards the binding is just a row that looks like every other.
  // Keyed by permission rather than collected into one "global only" set: the
  // answer depends on the scope this binding is being created at. A Project
  // binding reaches neither the global permissions nor `project.create`, a
  // Tenant binding reaches the latter, and a Global one reaches everything.
  const scopeFloors = useMemo(
    () =>
      new Map(
        (permissionsQuery.data?.permissions ?? []).map((permission) => [
          permission.name,
          permission.minimum_scope,
        ]),
      ),
    [permissionsQuery.data],
  );
  const inertPermissions =
    allRoles
      .find((item) => item.name === role)
      ?.permissions.filter((permission) =>
        scopeIsBelowFloor(scopeType, scopeFloors.get(permission)),
      ) ?? [];

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent aria-describedby={undefined}>
        <DialogHeader>
          <DialogTitle>新建角色绑定</DialogTitle>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label>用户</Label>
            <RecordPicker
              placeholder="选择用户"
              selected={subject}
              options={(users.data?.users ?? []).map((item) => ({
                id: item.id,
                label: item.display_name,
                hint: item.username,
              }))}
              query={users}
              search={userSearch}
              onSearchChange={setUserSearch}
              searchPlaceholder="按用户名或显示名搜索"
              emptyLabel="没有匹配的用户"
              onSelect={setSubject}
            />
          </div>

          {/* Same reason as the role editor: only the role column has a hint. */}
          <div className="grid grid-cols-2 items-start gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="binding-role">角色</Label>
              <Select value={role} onValueChange={(value) => setRole(value as RoleName)}>
                <SelectTrigger id="binding-role">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {bindableRoles.map((item) => (
                    <SelectItem key={item.id} value={item.name}>
                      {item.display_name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {/*
               * Only roles the caller could actually grant are offered. Binding
               * carries the same ceiling as authoring — otherwise `admin`, which
               * already holds everything, would be the way around it — so a role
               * beyond the caller's own permissions is a save the Server refuses.
               */}
              {hiddenRoleCount > 0 ? (
                <FieldHint>
                  另有 {hiddenRoleCount} 个角色包含当前账号未持有的权限，无法授予。
                </FieldHint>
              ) : null}
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="binding-scope">作用域</Label>
              <Select value={scopeType} onValueChange={(value) => setScopeType(value as ScopeType)}>
                <SelectTrigger id="binding-scope">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="global">全局</SelectItem>
                  <SelectItem value="tenant">租户</SelectItem>
                  <SelectItem value="project">项目</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          {needsTenant ? (
            <div className="grid gap-1.5">
              <Label>租户</Label>
              <RecordPicker
                placeholder="选择租户"
                selected={tenant}
                options={(tenants.data?.tenants ?? []).map((item) => ({
                  id: item.id,
                  label: item.name,
                }))}
                query={tenants}
                emptyLabel="没有可见的租户"
                onSelect={(next) => {
                  setTenant(next);
                  // The Project list is scoped to the Tenant, so a Project
                  // chosen under the previous one no longer means anything.
                  setProject(null);
                }}
              />
            </div>
          ) : null}

          {needsProject ? (
            <div className="grid gap-1.5">
              <Label>项目</Label>
              <RecordPicker
                placeholder={tenant ? "选择项目" : "请先选择租户"}
                disabled={!tenant}
                selected={project}
                options={(projects.data?.projects ?? []).map((item) => ({
                  id: item.id,
                  label: item.name,
                }))}
                query={projects}
                emptyLabel="该租户下没有可见的项目"
                onSelect={setProject}
              />
            </div>
          ) : null}

          {scopeType === "global" && role === "admin" ? (
            <Alert tone="warning">全局管理员可以管理所有租户、项目、用户与权限，请谨慎授予。</Alert>
          ) : null}

          {inertPermissions.length > 0 ? (
            <Alert tone="info">
              该角色中有 {inertPermissions.length} 项权限在
              {SCOPE_LABELS[scopeType] ?? scopeType}作用域上不生效，本次绑定不会授予它们：
              {inertPermissions.map((permission) => permissionLabel(permission)).join("、")}
              。其余权限正常生效。
            </Alert>
          ) : null}

          {/*
           * The Server's own message, for the same reason the role editor shows
           * it: the refusals here name what is wrong — a role that reaches
           * nothing at this scope, a permission the caller does not hold — and
           * "确认目标与当前账号的权限" answers none of them.
           */}
          {error ? <Alert tone="danger">{errorMessage(error)}</Alert> : null}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={pending}>
            取消
          </Button>
          <Button
            variant="primary"
            disabled={pending || !valid || !subject}
            onClick={() => {
              if (!subject) {
                return;
              }
              void onSubmit({
                subjectId: subject.id,
                role,
                scopeType,
                ...(needsTenant && tenant ? { tenantId: tenant.id } : {}),
                ...(needsProject && project ? { projectId: project.id } : {}),
              }).catch(() => undefined);
            }}
          >
            {pending ? "创建中…" : "创建"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function AuditSection() {
  const [filters, setFilters] = useState<AuditFilters>({});
  const [offset, setOffset] = useState(0);

  const query = useAuditEvents({ limit: DEFAULT_PAGE_SIZE, offset, ...filters });
  const actions = useAuditActions();

  // Any filter change invalidates the current page position, so paging always
  // restarts at the first page of the new result set.
  const updateFilters = (update: (current: AuditFilters) => AuditFilters) => {
    setFilters(update);
    setOffset(0);
  };

  const events: AuditEvent[] = query.data?.audit_events ?? [];

  // Grouped in the order the Server returned them, which is the order it
  // declares. Actions arrive already sorted by group, so first appearance is
  // enough to establish both the group order and its membership.
  const groupedActions = useMemo(() => {
    const groups: { group: string; actions: { name: string; group: string }[] }[] = [];
    for (const action of actions.data?.audit_actions ?? []) {
      const existing = groups.find((item) => item.group === action.group);
      if (existing) {
        existing.actions.push(action);
        continue;
      }
      groups.push({ group: action.group, actions: [action] });
    }
    return groups;
  }, [actions.data]);

  const columns = useMemo<ColumnDef<AuditEvent, unknown>[]>(
    () => [
      {
        header: "时间",
        size: 170,
        cell: ({ row }) => <AbsoluteTime value={row.original.created_at} />,
      },
      {
        header: "发起者",
        size: 180,
        /*
         * Two lines, like every other stacked cell in this application: who it
         * was on the first, the identifier on the second.
         *
         * Stacked three deep in a 120px column, the badge, the name and the id
         * each took a line of their own and pushed the row to twice the height
         * of the 操作 column beside it — a column of loose ends rather than one
         * subject. The badge qualifies the name, so it sits with it.
         */
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="flex min-w-0 items-center gap-1.5">
              <StatusBadge kind="actor" value={row.original.actor_type} />
              {/* The name recorded when the event was written. The id may point
                  at a user who has since been deleted, which is exactly when
                  the name is the only thing left that reads. */}
              {row.original.actor_user_name ? (
                <span className="text-foreground truncate text-[13px]">
                  {row.original.actor_user_name}
                </span>
              ) : null}
            </span>
            {row.original.actor_user_id ? (
              <IdentifierLabel value={row.original.actor_user_id} />
            ) : null}
          </div>
        ),
      },
      {
        header: "操作",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="zke-mono text-foreground text-[13px]">{row.original.action}</span>
            <span className="text-subtle-foreground text-xs">
              {row.original.target_type}
              {row.original.target_name
                ? ` · ${row.original.target_name}`
                : row.original.target_id
                  ? ` · ${row.original.target_id.slice(0, 8)}…`
                  : ""}
            </span>
          </div>
        ),
      },
      {
        header: "作用域",
        cell: ({ row }) => (
          <div className="text-muted-foreground flex flex-col gap-0.5 text-xs">
            <span>{row.original.scope_type}</span>
            {/* Innermost recorded name first: it survives the subject being
                deleted, which the id below it does not. */}
            {(row.original.cluster_name ??
            row.original.project_name ??
            row.original.tenant_name) ? (
              <span className="text-foreground">
                {row.original.cluster_name ?? row.original.project_name ?? row.original.tenant_name}
              </span>
            ) : null}
            {row.original.cluster_id ? (
              <IdentifierLabel value={row.original.cluster_id} />
            ) : row.original.project_id ? (
              <IdentifierLabel value={row.original.project_id} />
            ) : row.original.tenant_id ? (
              <IdentifierLabel value={row.original.tenant_id} />
            ) : null}
          </div>
        ),
      },
      {
        header: "结果",
        size: 90,
        cell: ({ row }) => <StatusBadge kind="auditResult" value={row.original.result} />,
      },
      {
        header: "请求 ID",
        size: 130,
        cell: ({ row }) => <IdentifierLabel value={row.original.request_id} />,
      },
    ],
    [],
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <SectionTitle
        title="审计事件"
        description="仅返回当前账号 audit.read 权限可见范围内的事件；按时间倒序分页"
      />

      <DataTable
        columns={columns}
        data={events}
        isLoading={query.isLoading}
        isFetching={query.isFetching}
        error={query.error}
        onRetry={() => void query.refetch()}
        rowKey={(row) => row.id}
        emptyTitle="没有匹配的审计事件"
        emptyDescription="调整筛选条件，或确认当前账号的 audit.read 可见范围。"
        pagination={{ value: query.data?.pagination, onOffsetChange: setOffset }}
        // In the table's own toolbar, as the other two sections do it. Loose
        // above the table these controls read as page furniture rather than as
        // the thing narrowing the rows underneath them.
        toolbar={
          <>
            <Select
              value={filters.actor_type ?? "all"}
              onValueChange={(value) =>
                updateFilters((current) => ({
                  ...current,
                  actor_type: value === "all" ? undefined : (value as AuditFilters["actor_type"]),
                }))
              }
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="发起者" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部发起者</SelectItem>
                <SelectItem value="user">用户</SelectItem>
                <SelectItem value="agent">Agent</SelectItem>
                <SelectItem value="system">系统</SelectItem>
              </SelectContent>
            </Select>

            <Select
              value={filters.result ?? "all"}
              onValueChange={(value) =>
                updateFilters((current) => ({
                  ...current,
                  result: value === "all" ? undefined : (value as AuditFilters["result"]),
                }))
              }
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="结果" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部结果</SelectItem>
                <SelectItem value="succeeded">成功</SelectItem>
                <SelectItem value="failed">失败</SelectItem>
                <SelectItem value="denied">拒绝</SelectItem>
              </SelectContent>
            </Select>

            {/*
             * A choice, not a free-text box. The Server matches `action`
             * exactly, so typing was only ever usable by someone who already
             * knew the spelling — and the vocabulary is closed and owned by the
             * Server, which is why it is fetched rather than hardcoded here.
             *
             * Grouped by the family the Server declares, not by splitting the
             * name on dots: `cluster.delete` and `cluster.enrollment.create`
             * are the same family at different depths.
             */}
            <Select
              value={filters.action ?? "all"}
              onValueChange={(value) =>
                updateFilters((current) => ({
                  ...current,
                  action: value === "all" ? undefined : value,
                }))
              }
            >
              {/* The longest name in the vocabulary is 34 characters, which no
                  toolbar control should be sized for; a chosen one that does
                  not fit is cut with an ellipsis, and the title carries it in
                  full. */}
              <SelectTrigger className="w-52" title={filters.action}>
                <SelectValue placeholder="操作" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部操作</SelectItem>
                {groupedActions.map(({ group, actions: inGroup }) => (
                  <SelectGroup key={group}>
                    <SelectLabel>{ACTION_GROUP_LABELS[group] ?? group}</SelectLabel>
                    {inGroup.map((action) => (
                      <SelectItem key={action.name} value={action.name} className="zke-mono">
                        {action.name}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                ))}
              </SelectContent>
            </Select>
            <Input
              className="max-w-52"
              placeholder="按请求 ID 追溯"
              value={filters.request_id ?? ""}
              onChange={(event) =>
                updateFilters((current) => ({
                  ...current,
                  request_id: event.target.value || undefined,
                }))
              }
            />
            {/* Only when something is set: a permanently live "clear" is a
                control that spends most of its life doing nothing. */}
            {Object.values(filters).some(Boolean) ? (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  setFilters({});
                  setOffset(0);
                }}
              >
                清除筛选
              </Button>
            ) : null}
          </>
        }
      />
    </div>
  );
}

export type PickedRecord = { id: string; label: string; hint?: string };

/**
 * Picks a record by name instead of by identifier.
 *
 * Creating a role binding used to mean typing three UUIDs — the subject, the
 * Tenant and the Project — which the operator had to go and find in three other
 * views first. An identifier is how the system refers to a record; it is not how
 * a person does, and asking for one turns an internal detail into the cost of
 * the task. The ids are still exactly what gets submitted, they are just no
 * longer what has to be produced.
 *
 * Options come from the scoped list APIs, so an operator only ever sees records
 * their bindings already allow; picking one can never widen what they may bind.
 */
function RecordPicker({
  placeholder,
  searchPlaceholder,
  selected,
  options,
  query,
  search,
  onSearchChange,
  emptyLabel,
  disabled = false,
  onSelect,
}: {
  placeholder: string;
  searchPlaceholder?: string;
  selected: PickedRecord | null;
  options: PickedRecord[];
  query: { isLoading: boolean; error: unknown; refetch: () => unknown };
  /** Provided only for lists long enough to need narrowing. */
  search?: string;
  onSearchChange?: (value: string) => void;
  emptyLabel: string;
  disabled?: boolean;
  onSelect: (record: PickedRecord) => void;
}) {
  const [open, setOpen] = useState(false);

  return (
    <Popover open={open} onOpenChange={disabled ? undefined : setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          disabled={disabled}
          className={cn(
            "zke-focus border-border bg-surface rounded-control shadow-e1 flex h-9 w-full items-center gap-2 border px-2.5 text-left text-sm transition-colors",
            "hover:border-border-strong disabled:cursor-not-allowed disabled:opacity-60",
          )}
        >
          {selected ? (
            <span className="flex min-w-0 items-baseline gap-2">
              <span className="text-foreground truncate">{selected.label}</span>
              {selected.hint ? (
                <span className="zke-mono text-subtle-foreground shrink-0 text-xs">
                  {selected.hint}
                </span>
              ) : null}
            </span>
          ) : (
            <span className="text-subtle-foreground truncate">{placeholder}</span>
          )}
          <ChevronDown className="text-subtle-foreground ml-auto size-3.5 shrink-0" aria-hidden />
        </button>
      </PopoverTrigger>

      <PopoverContent align="start" className="w-(--radix-popover-trigger-width) p-0">
        {onSearchChange ? (
          <div className="p-2">
            <div className="relative">
              <Search
                className="text-subtle-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2"
                aria-hidden
              />
              <Input
                value={search ?? ""}
                onChange={(event) => onSearchChange(event.target.value)}
                placeholder={searchPlaceholder}
                aria-label={searchPlaceholder}
                className="bg-surface-muted h-8 pl-8 text-[13px]"
              />
            </div>
          </div>
        ) : null}

        <ul className="max-h-56 overflow-y-auto p-1.5" role="listbox">
          {query.isLoading ? (
            <li className="text-subtle-foreground px-2 py-4 text-center text-[13px]">加载中…</li>
          ) : query.error ? (
            <li className="px-2 py-4 text-center">
              <p className="text-danger text-xs">加载失败</p>
              <Button
                size="sm"
                variant="ghost"
                className="mt-1"
                onClick={() => void query.refetch()}
              >
                重试
              </Button>
            </li>
          ) : options.length === 0 ? (
            <li className="text-subtle-foreground px-2 py-4 text-center text-[13px]">
              {emptyLabel}
            </li>
          ) : (
            options.map((option) => (
              <li key={option.id}>
                <button
                  type="button"
                  role="option"
                  aria-selected={option.id === selected?.id}
                  onClick={() => {
                    onSelect(option);
                    setOpen(false);
                  }}
                  className="zke-focus hover:bg-surface-muted flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left transition-colors"
                >
                  <span className="text-foreground min-w-0 flex-1 truncate text-[13px]">
                    {option.label}
                  </span>
                  {option.hint ? (
                    <span className="zke-mono text-subtle-foreground shrink-0 text-xs">
                      {option.hint}
                    </span>
                  ) : null}
                  {option.id === selected?.id ? (
                    <Check className="text-primary size-3.5 shrink-0" aria-hidden />
                  ) : null}
                </button>
              </li>
            ))
          )}
        </ul>
      </PopoverContent>
    </Popover>
  );
}
