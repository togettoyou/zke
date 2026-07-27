import { useState, type FormEvent } from "react";
import { Activity, KeyRound, LayoutGrid, ShieldCheck, UserRound } from "lucide-react";
import { toast } from "sonner";

import { useChangePassword, useHealth } from "@/api/queries/auth";
import { errorMessage, errorRequestId } from "@/api/errors";
import { AppShell, SectionTitle, type AppNavItem } from "@/apps/AppShell";
import type { AppComponentProps } from "@/apps/types";
import { describeCapability } from "@/auth/capabilities";
import { useSessionContext } from "@/auth/session-context";
import { AbsoluteTime, IdentifierLabel } from "@/components/common/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { FieldHint, Label } from "@/components/ui/label";
import { Alert, Card, CardTitle, Separator, Switch } from "@/components/ui/misc";
import { clearDesktopState } from "@/desktop/persistence";
import { useWindowStore } from "@/desktop/window-store";
import { useScopeStore } from "@/scope/scope-store";
import { useThemeStore } from "@/theme/theme-store";

const NAV: AppNavItem[] = [
  { id: "identity", label: "身份与权限", icon: UserRound },
  { id: "password", label: "修改密码", icon: KeyRound },
  { id: "desktop", label: "桌面偏好", icon: LayoutGrid },
  { id: "system", label: "系统状态", icon: Activity },
];

export function SettingsApp(_props: AppComponentProps) {
  const [section, setSection] = useState("identity");

  return (
    <AppShell nav={NAV} activeId={section} onNavigate={setSection}>
      {section === "identity" ? <IdentitySection /> : null}
      {section === "password" ? <PasswordSection /> : null}
      {section === "desktop" ? <DesktopSection /> : null}
      {section === "system" ? <SystemSection /> : null}
    </AppShell>
  );
}

function IdentitySection() {
  const { session, permissions } = useSessionContext();

  return (
    <div className="grid gap-4">
      <SectionTitle title="当前身份" description="来自 /api/v1/auth/me 的会话信息" />
      <Card className="grid gap-2">
        <div className="flex items-center justify-between gap-3">
          <span className="text-muted-foreground text-[13px]">显示名</span>
          <span className="text-foreground text-[13px] font-medium">
            {session?.user.display_name}
          </span>
        </div>
        <div className="flex items-center justify-between gap-3">
          <span className="text-muted-foreground text-[13px]">用户名</span>
          <span className="zke-mono text-foreground text-[13px]">{session?.user.username}</span>
        </div>
        <div className="flex items-center justify-between gap-3">
          <span className="text-muted-foreground text-[13px]">用户 ID</span>
          <IdentifierLabel value={session?.user.id ?? "—"} />
        </div>
        <div className="flex items-center justify-between gap-3">
          <span className="text-muted-foreground text-[13px]">会话绝对到期</span>
          <AbsoluteTime value={session?.expires_at} />
        </div>
      </Card>

      <SectionTitle
        title="权限能力"
        description="按 RoleBinding 作用域展开；界面据此控制入口，服务端仍会对每次请求重新授权"
      />
      {permissions.capabilities.length === 0 ? (
        <Alert tone="warning">
          当前账号没有任何 RoleBinding，只能访问系统设置。请联系全局管理员授予权限。
        </Alert>
      ) : (
        <div className="grid gap-2">
          {permissions.capabilities.map((capability, index) => (
            <Card key={index} className="grid gap-2">
              <div className="flex flex-wrap items-center gap-2">
                <CardTitle>{describeCapability(capability)}</CardTitle>
                {capability.tenant_id ? (
                  <span className="text-muted-foreground text-xs">
                    Tenant <IdentifierLabel value={capability.tenant_id} />
                  </span>
                ) : null}
                {capability.project_id ? (
                  <span className="text-muted-foreground text-xs">
                    Project <IdentifierLabel value={capability.project_id} />
                  </span>
                ) : null}
              </div>
              <div className="flex flex-wrap gap-1">
                {capability.permissions.map((permission) => (
                  <Badge key={permission} tone="neutral" className="zke-mono">
                    {permission}
                  </Badge>
                ))}
              </div>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}

function PasswordSection() {
  const changePassword = useChangePassword();
  const { refresh } = useSessionContext();
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [repeated, setRepeated] = useState("");
  const [done, setDone] = useState(false);

  const mismatch = repeated.length > 0 && repeated !== newPassword;
  const tooShort = newPassword.length > 0 && newPassword.length < 15;

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    if (mismatch || tooShort || !currentPassword || !newPassword) {
      return;
    }
    try {
      await changePassword.mutateAsync({ currentPassword, newPassword });
      setCurrentPassword("");
      setNewPassword("");
      setRepeated("");
      setDone(true);
      toast.success("密码已修改，请使用新密码重新登录");
      // The Server revoked every session for this user, including this one.
      refresh();
    } catch {
      setCurrentPassword("");
    }
  }

  return (
    <div className="max-w-md">
      <SectionTitle
        title="修改密码"
        description="修改成功后，当前用户的全部会话将被撤销，需要使用新密码重新登录"
      />
      {done ? <Alert tone="success">密码已修改，请重新登录。</Alert> : null}
      <form onSubmit={handleSubmit} className="mt-3 grid gap-3">
        <div className="grid gap-1.5">
          <Label htmlFor="current-password">当前密码</Label>
          <Input
            id="current-password"
            type="password"
            autoComplete="current-password"
            required
            value={currentPassword}
            onChange={(event) => setCurrentPassword(event.target.value)}
          />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="new-password">新密码</Label>
          <Input
            id="new-password"
            type="password"
            autoComplete="new-password"
            required
            value={newPassword}
            aria-invalid={tooShort}
            onChange={(event) => setNewPassword(event.target.value)}
          />
          <FieldHint>至少 15 个字符，且不能与当前密码相同。</FieldHint>
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="repeat-password">确认新密码</Label>
          <Input
            id="repeat-password"
            type="password"
            autoComplete="new-password"
            required
            value={repeated}
            aria-invalid={mismatch}
            onChange={(event) => setRepeated(event.target.value)}
          />
          {mismatch ? (
            <FieldHint className="text-danger">两次输入的新密码不一致。</FieldHint>
          ) : null}
        </div>

        {changePassword.error ? (
          <Alert tone="danger">
            {errorMessage(changePassword.error)}
            {errorRequestId(changePassword.error) ? (
              <span className="zke-mono mt-1 block text-xs opacity-80">
                请求 ID：{errorRequestId(changePassword.error)}
              </span>
            ) : null}
          </Alert>
        ) : null}

        <Button
          type="submit"
          variant="primary"
          disabled={changePassword.isPending || mismatch || tooShort}
        >
          {changePassword.isPending ? "提交中…" : "修改密码"}
        </Button>
      </form>
    </div>
  );
}

function DesktopSection() {
  const theme = useThemeStore((state) => state.theme);
  const setTheme = useThemeStore((state) => state.setTheme);
  const closeAll = useWindowStore((state) => state.closeAll);
  const resetScope = useScopeStore((state) => state.reset);
  const { session } = useSessionContext();

  return (
    <div className="grid max-w-lg gap-4">
      <SectionTitle title="桌面偏好" description="仅保存在本浏览器，不上传服务端" />

      <Card className="flex items-center justify-between gap-4">
        <div>
          <CardTitle>深色主题</CardTitle>
          <p className="text-muted-foreground mt-0.5 text-xs">
            默认跟随系统；此处的选择会记录在本地。
          </p>
        </div>
        <Switch
          checked={theme === "dark"}
          onCheckedChange={(checked) => setTheme(checked ? "dark" : "light")}
          aria-label="深色主题"
        />
      </Card>

      <Card className="grid gap-3">
        <div>
          <CardTitle>重置桌面布局</CardTitle>
          <p className="text-muted-foreground mt-0.5 text-xs">
            关闭全部窗口、清除本地保存的窗口位置与作用域选择。不会影响服务端任何数据。
          </p>
        </div>
        <Separator />
        <Button
          variant="secondary"
          className="justify-self-start"
          onClick={() => {
            closeAll();
            resetScope();
            if (session) {
              clearDesktopState(session.user.id);
            }
            toast.success("桌面布局已重置");
          }}
        >
          <LayoutGrid />
          重置桌面
        </Button>
      </Card>
    </div>
  );
}

function SystemSection() {
  const health = useHealth();

  return (
    <div className="grid max-w-lg gap-3">
      <SectionTitle title="系统状态" description="来自 ZKE Server 的就绪检查" />
      <Card className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <ShieldCheck className="text-muted-foreground size-4" aria-hidden />
          <span className="text-foreground text-[13px]">Server 就绪状态</span>
        </div>
        {health.isLoading ? (
          <Badge tone="neutral">检查中</Badge>
        ) : health.error ? (
          <Badge tone="danger">不可用</Badge>
        ) : (
          <Badge tone="success">就绪</Badge>
        )}
      </Card>
      {health.error ? <Alert tone="danger">{errorMessage(health.error)}</Alert> : null}
      <p className="text-subtle-foreground text-xs">
        Console 与 API 采用同源部署；生产环境由同一 Origin 提供静态资源与 /api 入口。
      </p>
    </div>
  );
}
