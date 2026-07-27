import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { ClipboardList, KeyRound, ShieldCheck, UserPlus, Users } from "lucide-react";
import { toast } from "sonner";

import { useAuditEvents, type AuditFilters } from "@/api/queries/audit";
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
import { ErrorState } from "@/components/common/state";
import { Badge } from "@/components/ui/badge";
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
import { Alert } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

const NAV: AppNavItem[] = [
  { id: "users", label: "用户", icon: Users },
  { id: "role-bindings", label: "权限绑定", icon: ShieldCheck },
  { id: "audit", label: "审计事件", icon: ClipboardList },
];

const GLOBAL = { type: "global" } as const;

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
          <div className="flex flex-col">
            <span className="text-foreground font-medium">{row.original.display_name}</span>
            <span className="zke-mono text-muted-foreground text-xs">{row.original.username}</span>
          </div>
        ),
      },
      {
        header: "状态",
        size: 130,
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <StatusBadge kind="user" value={row.original.status} />
            {row.original.failed_login_count > 0 ? (
              <span className="text-subtle-foreground text-xs">
                连续失败 {row.original.failed_login_count} 次
              </span>
            ) : null}
          </div>
        ),
      },
      {
        header: "锁定到期",
        size: 130,
        cell: ({ row }) => <RelativeTime value={row.original.lock_expires_at} />,
      },
      {
        header: "密码更新",
        size: 130,
        cell: ({ row }) => <RelativeTime value={row.original.password_changed_at} />,
      },
      {
        id: "actions",
        header: "",
        size: 320,
        cell: ({ row }) => {
          const user = row.original;
          if (!canManage) {
            return null;
          }
          return (
            <div className="flex flex-wrap justify-end gap-1">
              <Button size="sm" variant="ghost" onClick={() => setRenameTarget(user)}>
                改显示名
              </Button>
              {user.status === "locked" ? (
                <Button size="sm" variant="ghost" onClick={() => setUnlockTarget(user)}>
                  解锁
                </Button>
              ) : null}
              <Button size="sm" variant="ghost" onClick={() => setResetTarget(user)}>
                重置密码
              </Button>
              <Button
                size="sm"
                variant="ghost"
                disabled={user.id === currentUserId && user.status === "active"}
                onClick={() => setStatusTarget(user)}
              >
                {user.status === "disabled" ? "启用" : "禁用"}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                className="text-danger"
                onClick={() => setDeleteTarget(user)}
              >
                删除
              </Button>
            </div>
          );
        },
      },
    ],
    [canManage, currentUserId],
  );

  return (
    <>
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

  const columns = useMemo<ColumnDef<RoleBinding, unknown>[]>(
    () => [
      {
        header: "Subject",
        cell: ({ row }) => <IdentifierLabel value={row.original.subject_id} />,
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
              {row.original.scope_type === "global"
                ? "全局"
                : row.original.scope_type === "tenant"
                  ? "租户"
                  : "项目"}
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
                <SelectItem value="tenant">Tenant</SelectItem>
                <SelectItem value="project">Project</SelectItem>
              </SelectContent>
            </Select>
          </>
        }
        pagination={{ value: query.data?.pagination, onOffsetChange: setOffset }}
      />

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
          { label: "Subject", name: deleteTarget?.subject_id ?? "" },
          {
            label: "角色",
            name: `${deleteTarget?.role === "admin" ? "管理员" : "只读"} · ${deleteTarget?.scope_type ?? ""}`,
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
  const [subjectId, setSubjectId] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  const [scopeType, setScopeType] = useState<ScopeType>("project");
  const [tenantId, setTenantId] = useState("");
  const [projectId, setProjectId] = useState("");

  const needsTenant = scopeType === "tenant" || scopeType === "project";
  const needsProject = scopeType === "project";
  const valid =
    subjectId.trim().length > 0 &&
    (!needsTenant || tenantId.trim().length > 0) &&
    (!needsProject || projectId.trim().length > 0);

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent aria-describedby={undefined}>
        <DialogHeader>
          <DialogTitle>新建角色绑定</DialogTitle>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label htmlFor="binding-subject">用户 ID</Label>
            <Input
              id="binding-subject"
              className="zke-mono"
              placeholder="UUID"
              value={subjectId}
              onChange={(event) => setSubjectId(event.target.value)}
            />
            <FieldHint>可在「用户」列表中复制目标用户的 ID。</FieldHint>
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
                  <SelectItem value="tenant">Tenant</SelectItem>
                  <SelectItem value="project">Project</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          {needsTenant ? (
            <div className="grid gap-1.5">
              <Label htmlFor="binding-tenant">Tenant ID</Label>
              <Input
                id="binding-tenant"
                className="zke-mono"
                value={tenantId}
                onChange={(event) => setTenantId(event.target.value)}
              />
            </div>
          ) : null}

          {needsProject ? (
            <div className="grid gap-1.5">
              <Label htmlFor="binding-project">Project ID</Label>
              <Input
                id="binding-project"
                className="zke-mono"
                value={projectId}
                onChange={(event) => setProjectId(event.target.value)}
              />
            </div>
          ) : null}

          {scopeType === "global" && role === "admin" ? (
            <Alert tone="warning">全局管理员可以管理所有租户、项目、用户与权限，请谨慎授予。</Alert>
          ) : null}

          {error ? <Alert tone="danger">创建失败，请检查输入的 ID 与权限。</Alert> : null}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose} disabled={pending}>
            取消
          </Button>
          <Button
            variant="primary"
            disabled={pending || !valid}
            onClick={() => {
              void onSubmit({
                subjectId: subjectId.trim(),
                role,
                scopeType,
                ...(needsTenant ? { tenantId: tenantId.trim() } : {}),
                ...(needsProject ? { projectId: projectId.trim() } : {}),
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
  const query = useAuditEvents(filters);

  const events: AuditEvent[] = query.data?.pages.flatMap((page) => page.audit_events) ?? [];

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
    <>
      <SectionTitle
        title="审计事件"
        description="仅返回当前账号 audit.read 权限可见范围内的事件；游标分页，按时间倒序"
      />

      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Select
          value={filters.actor_type ?? "all"}
          onValueChange={(value) =>
            setFilters((current) => ({
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
            setFilters((current) => ({
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

        <Input
          className="max-w-44"
          placeholder="按 action 精确筛选"
          value={filters.action ?? ""}
          onChange={(event) =>
            setFilters((current) => ({ ...current, action: event.target.value || undefined }))
          }
        />
        <Input
          className="max-w-52"
          placeholder="按请求 ID 追溯"
          value={filters.request_id ?? ""}
          onChange={(event) =>
            setFilters((current) => ({ ...current, request_id: event.target.value || undefined }))
          }
        />
        <Button size="sm" variant="ghost" onClick={() => setFilters({})}>
          清除筛选
        </Button>
      </div>

      {query.error ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      ) : (
        <>
          <DataTable
            columns={columns}
            data={events}
            isLoading={query.isLoading}
            isFetching={query.isFetching}
            rowKey={(row) => row.id}
            emptyTitle="没有匹配的审计事件"
            emptyDescription="调整筛选条件，或确认当前账号的 audit.read 可见范围。"
          />
          <div className="mt-3 flex justify-center">
            <Button
              size="sm"
              variant="secondary"
              disabled={!query.hasNextPage || query.isFetchingNextPage}
              onClick={() => void query.fetchNextPage()}
            >
              {query.isFetchingNextPage
                ? "加载中…"
                : query.hasNextPage
                  ? "加载更多"
                  : "没有更多事件"}
            </Button>
          </div>
        </>
      )}
    </>
  );
}
