import { useState, type FormEvent, type ReactNode } from "react";
import { Activity, KeyRound, LayoutGrid, Network, Plus, UserRound } from "lucide-react";
import { toast } from "sonner";

import { useChangePassword, useHealth } from "@/api/queries/auth";
import {
  useCreateAgentEndpointProfile,
  useDeleteAgentEndpointProfile,
  usePlatformSettings,
  useUpdateAgentEndpointProfile,
  useUpdatePlatformSettings,
  type EndpointProfileInput,
} from "@/api/queries/platform-settings";
import type { AgentEndpointProfile, PlatformSettings } from "@/api/types";
import { errorMessage, errorRequestId } from "@/api/errors";
import { AppShell, type AppNavItem } from "@/apps/AppShell";
import type { AppComponentProps } from "@/apps/types";
import { describeCapability, GLOBAL_SCOPE } from "@/auth/capabilities";
import { useSessionContext } from "@/auth/session-context";
import { AbsoluteTime, IdentifierLabel } from "@/components/common/status";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Textarea } from "@/components/ui/input";
import { FieldHint, Label } from "@/components/ui/label";
import { Alert, Switch } from "@/components/ui/misc";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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

const PLATFORM_NAV: AppNavItem = { id: "platform", label: "平台配置", icon: Network };

export function SettingsApp(_props: AppComponentProps) {
  const [section, setSection] = useState("identity");
  const { permissions } = useSessionContext();
  const canChangePassword = permissions.can("user.password.change", GLOBAL_SCOPE);
  const nav = (canChangePassword ? NAV : NAV.filter((item) => item.id !== "password")).concat(
    permissions.isGlobalAdmin ? [PLATFORM_NAV] : [],
  );
  const activeSection =
    (section === "password" && !canChangePassword) ||
    (section === "platform" && !permissions.isGlobalAdmin)
      ? "identity"
      : section;

  return (
    <AppShell nav={nav} activeId={activeSection} onNavigate={setSection}>
      <div className="mx-auto grid max-w-2xl gap-7">
        {activeSection === "identity" ? <IdentitySection /> : null}
        {activeSection === "password" ? <PasswordSection /> : null}
        {activeSection === "desktop" ? <DesktopSection /> : null}
        {activeSection === "system" ? <SystemSection /> : null}
        {activeSection === "platform" ? <PlatformSection /> : null}
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

const EMPTY_PROFILE: EndpointProfileInput = {
  name: "",
  registration_url: "",
  quic_address: "",
  registration_ca_certificate_pem: "",
  enabled: true,
};

function PlatformSection() {
  const query = usePlatformSettings();
  const updateSettings = useUpdatePlatformSettings();
  const createProfile = useCreateAgentEndpointProfile();
  const updateProfile = useUpdateAgentEndpointProfile();
  const deleteProfile = useDeleteAgentEndpointProfile();
  const [settingsDraft, setSettingsDraft] = useState<PlatformSettings | null>(null);
  const [profileTarget, setProfileTarget] = useState<AgentEndpointProfile | null | undefined>();
  const [deleteTarget, setDeleteTarget] = useState<AgentEndpointProfile | null>(null);
  const [profileDraft, setProfileDraft] = useState<EndpointProfileInput>(EMPTY_PROFILE);

  const settings = settingsDraft ?? query.data?.settings ?? null;
  const profiles = query.data?.agent_endpoint_profiles ?? [];

  function openProfile(profile?: AgentEndpointProfile) {
    setProfileTarget(profile ?? null);
    setProfileDraft(
      profile
        ? {
            name: profile.name,
            registration_url: profile.registration_url,
            quic_address: profile.quic_address,
            registration_ca_certificate_pem: profile.registration_ca_certificate_pem,
            enabled: profile.enabled,
          }
        : EMPTY_PROFILE,
    );
  }

  if (query.isLoading) return <p className="text-muted-foreground text-sm">正在加载平台配置…</p>;
  if (query.error || !settings) return <Alert tone="danger">{errorMessage(query.error)}</Alert>;

  return (
    <>
      <section>
        <div className="mb-2.5 grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-4 gap-y-1">
          <div className="min-w-0">
            <h3 className="text-foreground text-[13px] font-semibold">Agent 接入端点</h3>
            <p className="text-muted-foreground mt-1 max-w-2xl text-xs leading-relaxed">
              端点决定 Agent 注册 URL 与 QUIC 地址。保存时自动更新 Listener 证书，无需重启 Server。
            </p>
          </div>
          <Button size="sm" variant="primary" className="self-start" onClick={() => openProfile()}>
            <Plus />
            新增端点
          </Button>
        </div>
        <div className="border-border divide-border/70 rounded-panel divide-y border">
          {profiles.map((profile) => (
            <div key={profile.id} className="flex items-center justify-between gap-4 px-3.5 py-3">
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-foreground text-[13px] font-medium">{profile.name}</span>
                  <Badge
                    tone={
                      profile.status === "ready"
                        ? "success"
                        : profile.status === "disabled"
                          ? "neutral"
                          : "warning"
                    }
                  >
                    {profile.status === "ready"
                      ? "可用"
                      : profile.status === "disabled"
                        ? "已禁用"
                        : "证书不可用"}
                  </Badge>
                  {profile.id === settings.default_endpoint_profile_id ? (
                    <Badge tone="info">平台默认</Badge>
                  ) : null}
                </div>
                <p className="text-subtle-foreground zke-mono mt-1 truncate text-xs">
                  {profile.registration_url} · {profile.quic_address}
                </p>
              </div>
              <div className="flex shrink-0 gap-1">
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={profile.id === settings.default_endpoint_profile_id}
                  onClick={() => openProfile(profile)}
                >
                  编辑
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  className="text-danger"
                  disabled={
                    profile.id === settings.default_endpoint_profile_id || deleteProfile.isPending
                  }
                  onClick={() => {
                    deleteProfile.reset();
                    setDeleteTarget(profile);
                  }}
                >
                  删除
                </Button>
              </div>
            </div>
          ))}
        </div>
      </section>

      <section>
        <h3 className="text-foreground mb-1 text-[13px] font-semibold">Agent 安装默认值</h3>
        <p className="text-muted-foreground mb-3 text-xs leading-relaxed">
          创建接入凭证时会将这些值复制为不可变快照，后续修改不会影响已经签发的凭证。
        </p>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label>Agent 镜像</Label>
            <Input
              value={settings.agent_image}
              onChange={(event) =>
                setSettingsDraft({ ...settings, agent_image: event.target.value })
              }
            />
          </div>
          <div className="grid content-start gap-1.5">
            <Label>Agent 拉取策略</Label>
            <Select
              value={settings.agent_image_pull_policy}
              onValueChange={(value) =>
                setSettingsDraft({
                  ...settings,
                  agent_image_pull_policy: value as PlatformSettings["agent_image_pull_policy"],
                })
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {["Always", "IfNotPresent", "Never"].map((value) => (
                  <SelectItem key={value} value={value}>
                    {value}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="grid gap-1.5">
            <Label>Cluster Terminal 镜像</Label>
            <Input
              value={settings.cluster_terminal_image}
              onChange={(event) =>
                setSettingsDraft({ ...settings, cluster_terminal_image: event.target.value })
              }
            />
          </div>
          <div className="grid content-start gap-1.5">
            <Label>Cluster Terminal 拉取策略</Label>
            <Select
              value={settings.cluster_terminal_image_pull_policy}
              onValueChange={(value) =>
                setSettingsDraft({
                  ...settings,
                  cluster_terminal_image_pull_policy:
                    value as PlatformSettings["cluster_terminal_image_pull_policy"],
                })
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {["Always", "IfNotPresent", "Never"].map((value) => (
                  <SelectItem key={value} value={value}>
                    {value}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <FieldHint>镜像与拉取策略修改后立即用于新建终端会话。</FieldHint>
          </div>
          <Button
            variant="primary"
            className="justify-self-start"
            disabled={updateSettings.isPending}
            onClick={async () => {
              try {
                const updated = await updateSettings.mutateAsync(settings);
                setSettingsDraft(updated);
                toast.success("平台配置已保存");
              } catch (error) {
                toast.error(errorMessage(error));
              }
            }}
          >
            {updateSettings.isPending ? "保存中…" : "保存默认值"}
          </Button>
        </div>
      </section>

      <Dialog
        open={profileTarget !== undefined}
        onOpenChange={(open) => !open && setProfileTarget(undefined)}
      >
        <DialogContent aria-describedby={undefined} className="w-[min(680px,calc(100vw-2rem))]">
          <DialogHeader>
            <DialogTitle>
              {profileTarget ? "编辑 Agent 接入端点" : "新增 Agent 接入端点"}
            </DialogTitle>
          </DialogHeader>
          <div className="grid gap-3">
            <div className="grid gap-1.5">
              <Label>名称</Label>
              <Input
                value={profileDraft.name}
                onChange={(event) => setProfileDraft({ ...profileDraft, name: event.target.value })}
              />
            </div>
            <div className="grid gap-1.5">
              <Label>注册 URL</Label>
              <Input
                placeholder="https://zke.example.com"
                value={profileDraft.registration_url}
                onChange={(event) => {
                  const registrationURL = event.target.value;
                  setProfileDraft({
                    ...profileDraft,
                    registration_url: registrationURL,
                    registration_ca_certificate_pem: registrationURL
                      .trimStart()
                      .toLowerCase()
                      .startsWith("http://")
                      ? ""
                      : profileDraft.registration_ca_certificate_pem,
                  });
                }}
              />
              <FieldHint>支持 HTTP 或 HTTPS。</FieldHint>
            </div>
            <div className="grid gap-1.5">
              <Label>QUIC 地址</Label>
              <Input
                placeholder="zke.example.com:8443"
                value={profileDraft.quic_address}
                onChange={(event) =>
                  setProfileDraft({ ...profileDraft, quic_address: event.target.value })
                }
              />
            </div>
            {profileDraft.registration_url.trimStart().toLowerCase().startsWith("https://") ? (
              <div className="grid gap-1.5">
                <Label>自定义 HTTPS CA（可选）</Label>
                <Textarea
                  rows={5}
                  placeholder="-----BEGIN CERTIFICATE-----"
                  value={profileDraft.registration_ca_certificate_pem}
                  onChange={(event) =>
                    setProfileDraft({
                      ...profileDraft,
                      registration_ca_certificate_pem: event.target.value,
                    })
                  }
                />
                <FieldHint>公共可信证书无需填写；仅用于自签名证书或私有 CA。</FieldHint>
              </div>
            ) : null}
            <div className="flex items-center justify-between">
              <Label>启用</Label>
              <Switch
                checked={profileDraft.enabled}
                onCheckedChange={(checked) =>
                  setProfileDraft({ ...profileDraft, enabled: checked })
                }
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setProfileTarget(undefined)}>
              取消
            </Button>
            <Button
              variant="primary"
              disabled={createProfile.isPending || updateProfile.isPending}
              onClick={async () => {
                try {
                  if (profileTarget)
                    await updateProfile.mutateAsync({
                      ...profileDraft,
                      id: profileTarget.id,
                      expected_revision: profileTarget.revision,
                    });
                  else await createProfile.mutateAsync(profileDraft);
                  setProfileTarget(undefined);
                  toast.success("端点已保存");
                } catch (error) {
                  toast.error(errorMessage(error));
                }
              }}
            >
              保存
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <SensitiveActionDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => {
          if (!open) {
            setDeleteTarget(null);
            deleteProfile.reset();
          }
        }}
        title="删除 Agent 接入端点"
        description="删除后，该端点不再出现在新凭据的可选列表中。"
        scopeLines={
          deleteTarget
            ? [
                { label: "端点", name: deleteTarget.name, id: deleteTarget.id },
                { label: "注册 URL", name: deleteTarget.registration_url },
                { label: "QUIC 地址", name: deleteTarget.quic_address },
              ]
            : []
        }
        impacts={[
          "已经签发的接入凭据保留其不可变快照，不受本次删除影响。",
          "该端点将无法再用于创建新的接入凭据。",
        ]}
        confirmationText={deleteTarget?.name}
        confirmLabel="删除端点"
        destructive
        pending={deleteProfile.isPending}
        error={deleteProfile.error}
        onConfirm={async () => {
          if (!deleteTarget) return;
          try {
            await deleteProfile.mutateAsync(deleteTarget.id);
            setDeleteTarget(null);
            toast.success("端点已删除");
          } catch {
            // The shared sensitive-action dialog renders the API error with its request ID.
          }
        }}
      />
    </>
  );
}
