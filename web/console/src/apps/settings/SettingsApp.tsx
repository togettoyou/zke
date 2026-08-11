import { useState, type FormEvent, type ReactNode } from "react";
import { Activity, KeyRound, LayoutGrid, UserRound } from "lucide-react";
import { toast } from "sonner";

import { useChangePassword, useHealth } from "@/api/queries/auth";
import { errorMessage, errorRequestId } from "@/api/errors";
import { AppShell, type AppNavItem } from "@/apps/AppShell";
import type { AppComponentProps } from "@/apps/types";
import { describeCapability, GLOBAL_SCOPE } from "@/auth/capabilities";
import { useSessionContext } from "@/auth/session-context";
import { AbsoluteTime, IdentifierLabel } from "@/components/common/status";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { FieldHint, Label } from "@/components/ui/label";
import { Alert, Switch } from "@/components/ui/misc";
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
  const { permissions } = useSessionContext();
  const canChangePassword = permissions.can("user.password.change", GLOBAL_SCOPE);
  const nav = canChangePassword ? NAV : NAV.filter((item) => item.id !== "password");
  const activeSection = section === "password" && !canChangePassword ? "identity" : section;

  return (
    <AppShell nav={nav} activeId={activeSection} onNavigate={setSection}>
      <div className="mx-auto grid max-w-2xl gap-7">
        {activeSection === "identity" ? <IdentitySection /> : null}
        {activeSection === "password" ? <PasswordSection /> : null}
        {activeSection === "desktop" ? <DesktopSection /> : null}
        {activeSection === "system" ? <SystemSection /> : null}
      </div>
    </AppShell>
  );
}

/* -------------------------------------------------------------------------- */
/*  Grouped rows                                                              */
/* -------------------------------------------------------------------------- */

/**
 * Settings are grouped rows, not a stack of cards.
 *
 * A card is a white panel with a border and a shadow — inside a window that is
 * already a white panel with a border and a shadow, it contributes an outline
 * and nothing else, and four of them in a column read as packaging rather than
 * as content. One bordered group with hairline-separated rows is what every
 * native settings surface does, and it is quieter and easier to scan: the rows
 * share one left edge, so the labels form a column the eye can run down.
 */
function Group({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section>
      <h3 className="text-foreground mb-1 text-[13px] font-semibold">{title}</h3>
      {hint ? <p className="text-muted-foreground mb-2.5 text-xs leading-relaxed">{hint}</p> : null}
      {/* No `overflow-hidden`: the rows carry no fill of their own, so nothing
          needs clipping to the rounded corners — and clipping would cut the
          copy affordance that hangs in an identifier's right margin. */}
      <div className="border-border divide-border/70 rounded-panel divide-y border">{children}</div>
    </section>
  );
}

/** Label on the left, value or control on the right, on one baseline. */
function Row({
  label,
  hint,
  children,
}: {
  label: ReactNode;
  hint?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex items-center justify-between gap-4 px-3.5 py-2.5">
      <div className="min-w-0">
        <div className="text-muted-foreground text-[13px]">{label}</div>
        {hint ? (
          <p className="text-subtle-foreground mt-0.5 text-xs leading-relaxed">{hint}</p>
        ) : null}
      </div>
      <div className="flex min-w-0 shrink-0 items-center justify-end">{children}</div>
    </div>
  );
}

/* -------------------------------------------------------------------------- */
/*  Sections                                                                  */
/* -------------------------------------------------------------------------- */

function IdentitySection() {
  const { session, permissions } = useSessionContext();

  return (
    <>
      <Group title="当前身份" hint="来自 /api/v1/auth/me 的会话信息">
        <Row label="显示名">
          <span className="text-foreground text-[13px] font-medium">
            {session?.user.display_name}
          </span>
        </Row>
        <Row label="用户名">
          <span className="zke-mono text-foreground text-[13px]">{session?.user.username}</span>
        </Row>
        <Row label="用户 ID">
          <IdentifierLabel value={session?.user.id ?? "—"} />
        </Row>
        <Row label="会话绝对到期">
          <AbsoluteTime value={session?.expires_at} />
        </Row>
      </Group>

      <section>
        <h3 className="text-foreground mb-1 text-[13px] font-semibold">权限能力</h3>
        <p className="text-muted-foreground mb-2.5 text-xs leading-relaxed">
          按角色绑定作用域展开；界面据此控制入口，服务端仍会对每次请求重新授权。
        </p>

        {permissions.capabilities.length === 0 ? (
          <Alert tone="warning">
            当前账号没有任何角色绑定，只能访问系统设置。请联系全局管理员授予权限。
          </Alert>
        ) : (
          <div className="border-border divide-border/70 rounded-panel divide-y border">
            {permissions.capabilities.map((capability, index) => (
              <div key={index} className="grid gap-2 px-3.5 py-3">
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                  <span className="text-foreground text-[13px] font-medium">
                    {describeCapability(capability)}
                  </span>
                  {capability.tenant_id ? (
                    <span className="text-subtle-foreground flex items-center gap-1 text-xs">
                      Tenant
                      <IdentifierLabel value={capability.tenant_id} />
                    </span>
                  ) : null}
                  {capability.project_id ? (
                    <span className="text-subtle-foreground flex items-center gap-1 text-xs">
                      Project
                      <IdentifierLabel value={capability.project_id} />
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
              </div>
            ))}
          </div>
        )}
      </section>
    </>
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
    <section className="max-w-md">
      <h3 className="text-foreground mb-1 text-[13px] font-semibold">修改密码</h3>
      <p className="text-muted-foreground mb-3 text-xs leading-relaxed">
        修改成功后，当前用户的全部会话将被撤销，需要使用新密码重新登录。
      </p>

      {done ? <Alert tone="success">密码已修改，请重新登录。</Alert> : null}

      <form onSubmit={handleSubmit} className="mt-3 grid gap-3.5">
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
          className="mt-1 justify-self-start px-5"
          disabled={changePassword.isPending || mismatch || tooShort}
        >
          {changePassword.isPending ? "提交中…" : "修改密码"}
        </Button>
      </form>
    </section>
  );
}

function DesktopSection() {
  const theme = useThemeStore((state) => state.theme);
  const setTheme = useThemeStore((state) => state.setTheme);
  const closeAll = useWindowStore((state) => state.closeAll);
  const resetScope = useScopeStore((state) => state.reset);
  const { session } = useSessionContext();

  return (
    <Group title="桌面偏好" hint="仅保存在本浏览器，不上传服务端">
      <Row label="深色主题" hint="默认跟随系统，选择记录在本地">
        <Switch
          checked={theme === "dark"}
          onCheckedChange={(checked) => setTheme(checked ? "dark" : "light")}
          aria-label="深色主题"
        />
      </Row>
      <Row label="重置桌面布局" hint="关闭全部窗口，清除本地保存的窗口位置与作用域选择">
        <Button
          size="sm"
          variant="secondary"
          onClick={() => {
            closeAll();
            resetScope();
            if (session) {
              clearDesktopState(session.user.id);
            }
            toast.success("桌面布局已重置");
          }}
        >
          重置桌面
        </Button>
      </Row>
    </Group>
  );
}

function SystemSection() {
  const health = useHealth();

  const status = health.isLoading
    ? { tone: "neutral" as const, label: "检查中" }
    : health.error
      ? { tone: "danger" as const, label: "不可用" }
      : { tone: "success" as const, label: "就绪" };

  return (
    <>
      <Group title="系统状态" hint="来自 ZKE Server 的就绪检查">
        <Row label="Server 就绪状态">
          {/* A dot and a word rather than a filled pill: this is one reading on
              an otherwise quiet list, and a badge would be the loudest thing on
              the page. The dot is never the only carrier — the word says it. */}
          <span className="text-foreground flex items-center gap-1.5 text-[13px]">
            <StatusDot tone={status.tone} />
            {status.label}
          </span>
        </Row>
      </Group>

      {health.error ? <Alert tone="danger">{errorMessage(health.error)}</Alert> : null}
    </>
  );
}
