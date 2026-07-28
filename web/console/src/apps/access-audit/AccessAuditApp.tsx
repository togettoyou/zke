import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import {
  Check,
  ChevronDown,
  ClipboardList,
  KeyRound,
  MoreHorizontal,
  Search,
  ShieldCheck,
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
  DEFAULT_PAGE_SIZE,
  type AuditEvent,
  type ManagedUser,
  type Role,
  type RoleBinding,
  type ScopeType,
  type UserStatus,
} from "@/api/types";
import { AppShell, SectionTitle, type AppNavItem } from "@/apps/AppShell";
import type { AppComponentProps } from "@/apps/types";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
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
 */
const ACTION_GROUP_ORDER = ["auth", "user", "role_binding", "tenant", "project", "cluster"];

const ACTION_GROUP_LABELS: Record<string, string> = {
  auth: "认证",
  user: "用户",
  role_binding: "权限绑定",
  tenant: "租户",
  project: "项目",
  cluster: "集群",
};

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
      (item.id === "role-bindings" && !permissions.can("rbac.read", GLOBAL)) ||
      (item.id === "audit" && !permissions.canAnywhere("audit.read")),
  }));

  const firstVisible = nav.find((item) => !item.hidden)?.id;
  const active = nav.find((item) => item.id === section && !item.hidden) ? section : firstVisible;

  if (!firstVisible || !active) {
    return (
      <div className="p-4">
        <Alert tone="warning">
          当前账号没有用户、角色绑定或审计的读取权限。相关入口已隐藏，服务端也会拒绝对应请求。
        </Alert>
      </div>
    );
  }

  return (
    <AppShell nav={nav} activeId={active} onNavigate={setSection}>
      {active === "users" ? <UserSection /> : null}
      {active === "role-bindings" ? <RoleBindingSection /> : null}
      {active === "audit" ? <AuditSection /> : null}
    </AppShell>
  );
}

function UserSection() {
  const { permissions, session } = useSessionContext();
  const canManage = permissions.can("user.manage", GLOBAL);

  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("all");
  const [offset, setOffset] = useState(0);
  const [createOpen, setCreateOpen] = useState(false);
  const [renameTarget, setRenameTarget] = useState<ManagedUser | null>(null);
  const [statusTarget, setStatusTarget] = useState<ManagedUser | null>(null);
  const [unlockTarget, setUnlockTarget] = useState<ManagedUser | null>(null);
  const [resetTarget, setResetTarget] = useState<ManagedUser | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ManagedUser | null>(null);

  const query = useUsers({
    limit: DEFAULT_PAGE_SIZE,
    offset,
    ...(search ? { q: search } : {}),
    ...(status === "all" ? {} : { status: status as UserStatus }),
  });

  const createUser = useCreateUser();
  const updateUser = useUpdateUser();
  const setUserStatus = useSetUserStatus();
  const unlockUser = useUnlockUser();
  const resetPassword = useResetUserPassword();
  const deleteUser = useDeleteUser();

  const currentUserId = session?.user.id;

  const columns = useMemo<ColumnDef<ManagedUser, unknown>[]>(
    () => [
      {
        header: "用户",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground font-medium">{row.original.display_name}</span>
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
                  <DropdownMenuItem onSelect={() => setRenameTarget(user)}>
                    改显示名
                  </DropdownMenuItem>
                  {user.status === "locked" ? (
                    <DropdownMenuItem onSelect={() => setUnlockTarget(user)}>解锁</DropdownMenuItem>
                  ) : null}
                  <DropdownMenuItem onSelect={() => setResetTarget(user)}>
                    重置密码
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    disabled={user.id === currentUserId && disabling}
                    onSelect={() => setStatusTarget(user)}
                  >
                    {disabling ? "禁用" : "启用"}
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem variant="danger" onSelect={() => setDeleteTarget(user)}>
                    删除
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          );
        },
      },
    ],
    [canManage, currentUserId],
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
              <Button size="sm" variant="primary" onClick={() => setCreateOpen(true)}>
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
        description="删除为逻辑删除：账号不可再登录，历史审计记录保留。"
        scopeLines={[
          {
            label: "用户",
            name: `${deleteTarget?.display_name ?? ""}（${deleteTarget?.username ?? ""}）`,
            id: deleteTarget?.id,
          },
        ]}
        impacts={[
          "该用户无法再登录",
          "全部现有会话立即被撤销",
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

function RoleBindingSection() {
  const { permissions } = useSessionContext();
  const canManage = permissions.can("rbac.manage", GLOBAL);

  const [role, setRole] = useState("all");
  const [scopeType, setScopeType] = useState("all");
  const [offset, setOffset] = useState(0);
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<RoleBinding | null>(null);

  const query = useRoleBindings({
    limit: DEFAULT_PAGE_SIZE,
    offset,
    ...(role === "all" ? {} : { role: role as Role }),
    ...(scopeType === "all" ? {} : { scope_type: scopeType as ScopeType }),
  });
  const createRoleBinding = useCreateRoleBinding();
  const deleteRoleBinding = useDeleteRoleBinding();

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
        size: 100,
        cell: ({ row }) => (
          <Badge tone={row.original.role === "admin" ? "primary" : "neutral"}>
            {row.original.role === "admin" ? "管理员" : "只读"}
          </Badge>
        ),
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
        cell: ({ row }) =>
          canManage ? (
            <div className="flex justify-end">
              <Button
                size="sm"
                variant="ghost"
                className="text-danger"
                onClick={() => setDeleteTarget(row.original)}
              >
                删除
              </Button>
            </div>
          ) : null,
      },
    ],
    [canManage],
  );

  return (
    <>
      <div className="flex h-full min-h-0 flex-col">
        <SectionTitle
          title="权限绑定"
          description="角色绑定决定用户在 Global、租户或项目作用域内的权限"
          actions={
            canManage ? (
              <Button size="sm" variant="primary" onClick={() => setCreateOpen(true)}>
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
                  <SelectItem value="admin">管理员</SelectItem>
                  <SelectItem value="viewer">只读</SelectItem>
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
            name: `${deleteTarget?.role === "admin" ? "管理员" : "只读"} · ${SCOPE_LABELS[deleteTarget?.scope_type ?? "global"]}`,
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
    role: Role;
    scopeType: ScopeType;
    tenantId?: string;
    projectId?: string;
  }) => Promise<void>;
}) {
  const [subject, setSubject] = useState<PickedRecord | null>(null);
  const [role, setRole] = useState<Role>("viewer");
  const [scopeType, setScopeType] = useState<ScopeType>("project");
  const [tenant, setTenant] = useState<PickedRecord | null>(null);
  const [project, setProject] = useState<PickedRecord | null>(null);
  const [userSearch, setUserSearch] = useState("");
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
    { limit: 50, status: "active", ...(userSearch.trim() ? { q: userSearch.trim() } : {}) },
    open,
  );
  const tenants = useTenants({ limit: 100, status: "active" }, open && needsTenant);
  const projects = useProjects(open && needsProject ? (tenant?.id ?? null) : null, {
    limit: 100,
    status: "active",
  });

  const valid = Boolean(subject) && (!needsTenant || tenant) && (!needsProject || project);

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

          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="binding-role">角色</Label>
              <Select value={role} onValueChange={(value) => setRole(value as Role)}>
                <SelectTrigger id="binding-role">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="admin">管理员</SelectItem>
                  <SelectItem value="viewer">只读</SelectItem>
                </SelectContent>
              </Select>
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

          {error ? <Alert tone="danger">创建失败，请确认目标与当前账号的权限。</Alert> : null}
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

  const columns = useMemo<ColumnDef<AuditEvent, unknown>[]>(
    () => [
      {
        header: "时间",
        size: 170,
        cell: ({ row }) => <AbsoluteTime value={row.original.created_at} />,
      },
      {
        header: "发起者",
        size: 120,
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <StatusBadge kind="actor" value={row.original.actor_type} />
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
              {row.original.target_id ? ` · ${row.original.target_id.slice(0, 8)}…` : ""}
            </span>
          </div>
        ),
      },
      {
        header: "作用域",
        cell: ({ row }) => (
          <div className="text-muted-foreground flex flex-col gap-0.5 text-xs">
            <span>{row.original.scope_type}</span>
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
              <SelectTrigger className="w-52">
                <SelectValue placeholder="操作" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部操作</SelectItem>
                {ACTION_GROUP_ORDER.map((group) => {
                  const inGroup = (actions.data?.audit_actions ?? []).filter(
                    (action) => action.group === group,
                  );
                  if (inGroup.length === 0) {
                    return null;
                  }
                  return (
                    <SelectGroup key={group}>
                      <SelectLabel>{ACTION_GROUP_LABELS[group]}</SelectLabel>
                      {inGroup.map((action) => (
                        <SelectItem key={action.name} value={action.name} className="zke-mono">
                          {action.name}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  );
                })}
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
