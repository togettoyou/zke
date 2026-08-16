import { useState } from "react";
import { Activity, Container, Network, Plus, SquareTerminal } from "lucide-react";
import { toast } from "sonner";

import {
  useCreateAgentEndpointProfile,
  useDeleteAgentEndpointProfile,
  usePlatformSettings,
  useUpdateAgentEndpointProfile,
  useUpdatePlatformSettings,
  type EndpointProfileInput,
} from "@/api/queries/platform-settings";
import type { AgentEndpointProfile, PlatformSettings } from "@/api/types";
import { errorMessage } from "@/api/errors";
import { AppShell, type AppNavItem } from "@/apps/AppShell";
import type { AppComponentProps } from "@/apps/types";
import { useSessionContext } from "@/auth/session-context";
import { notifyFailure } from "@/components/common/notify";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { Badge } from "@/components/ui/badge";
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

const NAV: AppNavItem[] = [
  { id: "endpoints", label: "端点", icon: Network },
  { id: "images", label: "镜像", icon: Container },
  { id: "cluster-terminal", label: "集群终端", icon: SquareTerminal },
  // Its own section rather than two more fields under 镜像: the collector is a
  // workload ZKE installs into somebody else's Cluster, so what it may take
  // from that Cluster is the decision being made here, and an image field is
  // only one part of it.
  { id: "metrics-collection", label: "指标采集", icon: Activity },
];

const PULL_POLICIES = ["Always", "IfNotPresent", "Never"] as const;

const EMPTY_PROFILE: EndpointProfileInput = {
  name: "",
  registration_url: "",
  quic_address: "",
  registration_ca_certificate_pem: "",
  enabled: true,
};

/**
 * Platform configuration: what every Cluster in this deployment inherits.
 *
 * Its own application rather than a section of 系统设置, because the two answer
 * different questions. 系统设置 is about the person at the keyboard — who they
 * are, their password, their desktop. This is about the deployment, and only a
 * global administrator can see or change any of it.
 *
 * All three sections read one settings row and one endpoint list from a single
 * query. The endpoint list is its own resource with its own revisions; images
 * and the Cluster Terminal lifetime are columns of the same row, so they share
 * one draft and one optimistic-concurrency save.
 */
export function PlatformApp(_props: AppComponentProps) {
  const [section, setSection] = useState("endpoints");
  const { permissions } = useSessionContext();
  const query = usePlatformSettings(permissions.isGlobalAdmin);
  const updateSettings = useUpdatePlatformSettings();
  const [draft, setDraft] = useState<PlatformSettings | null>(null);

  // A window restored from a previous session can outlive the role that opened
  // it. The Server refuses every route behind this application anyway; saying
  // so is better than rendering an empty shell whose requests all fail.
  if (!permissions.isGlobalAdmin) {
    return (
      <div className="p-4">
        <Alert tone="warning">平台配置仅对全局管理员开放。请联系全局管理员。</Alert>
      </div>
    );
  }

  const settings = draft ?? query.data?.settings ?? null;

  async function save(next: PlatformSettings): Promise<void> {
    try {
      setDraft(await updateSettings.mutateAsync(next));
      toast.success("平台配置已保存");
    } catch (error) {
      notifyFailure("保存平台配置失败", error);
    }
  }

  return (
    <AppShell nav={NAV} activeId={section} onNavigate={setSection}>
      <div className="mx-auto grid max-w-2xl gap-7">
        {query.isLoading ? (
          <p className="text-muted-foreground text-sm">正在加载平台配置…</p>
        ) : null}
        {query.error ? <Alert tone="danger">{errorMessage(query.error)}</Alert> : null}
        {settings ? (
          <>
            {section === "endpoints" ? (
              <EndpointsSection
                profiles={query.data?.agent_endpoint_profiles ?? []}
                defaultProfileID={settings.default_endpoint_profile_id}
              />
            ) : null}
            {section === "images" ? (
              <ImagesSection settings={settings} onChange={setDraft} />
            ) : null}
            {section === "cluster-terminal" ? (
              <ClusterTerminalSection settings={settings} onChange={setDraft} />
            ) : null}
            {section === "metrics-collection" ? (
              <MetricsCollectionSection settings={settings} onChange={setDraft} />
            ) : null}
            {section === "endpoints" ? null : (
              <SaveRow pending={updateSettings.isPending} onSave={() => save(settings)} />
            )}
          </>
        ) : null}
      </div>
    </AppShell>
  );
}

/**
 * One save button, shown on every settings section.
 *
 * Images, the Cluster Terminal lifetime and the collector's budget are columns
 * of a single row guarded by one revision, so a save from any section writes
 * all of them. Saying so under the button keeps that from being a surprise: an
 * operator who edited an image and then switched sections has not left the edit
 * behind.
 */
function SaveRow({ pending, onSave }: { pending: boolean; onSave: () => void }) {
  return (
    <div className="grid gap-1.5">
      <Button variant="primary" className="justify-self-start" disabled={pending} onClick={onSave}>
        {pending ? "保存中…" : "保存平台配置"}
      </Button>
      <FieldHint>镜像、集群终端与指标采集属于同一份平台配置，保存会一并写入各处的改动。</FieldHint>
    </div>
  );
}

function ImagesSection({
  settings,
  onChange,
}: {
  settings: PlatformSettings;
  onChange: (next: PlatformSettings) => void;
}) {
  return (
    <section>
      <h3 className="text-foreground mb-1 text-[13px] font-semibold">镜像与拉取策略</h3>
      <p className="text-muted-foreground mb-3 text-xs leading-relaxed">
        Agent 镜像在创建接入凭证时复制为不可变快照，后续修改不影响已签发的凭证。Cluster Terminal
        镜像在创建新会话时读取，修改后立即对下一个会话生效。指标采集组件的镜像在「指标采集」中配置。
      </p>
      <div className="grid gap-3">
        <div className="grid gap-1.5">
          <Label>Agent 镜像</Label>
          <Input
            value={settings.agent_image}
            onChange={(event) => onChange({ ...settings, agent_image: event.target.value })}
          />
        </div>
        <PullPolicySelect
          label="Agent 拉取策略"
          value={settings.agent_image_pull_policy}
          onChange={(value) => onChange({ ...settings, agent_image_pull_policy: value })}
        />
        <div className="grid gap-1.5">
          <Label>Cluster Terminal 镜像</Label>
          <Input
            value={settings.cluster_terminal_image}
            onChange={(event) =>
              onChange({ ...settings, cluster_terminal_image: event.target.value })
            }
          />
        </div>
        <PullPolicySelect
          label="Cluster Terminal 拉取策略"
          value={settings.cluster_terminal_image_pull_policy}
          onChange={(value) => onChange({ ...settings, cluster_terminal_image_pull_policy: value })}
        />
      </div>
    </section>
  );
}

/**
 * What the metrics collector is, and how much of a Cluster it may take.
 *
 * The budget is here rather than in the Server's configuration file for the
 * same reason the image is: collection is enabled per Cluster long after the
 * Server started, and the Clusters are not all the same size.
 *
 * An empty field is a real answer — Kubernetes has no spelling for "no limit"
 * other than leaving the entry off the container — so nothing here fills a
 * blank back in on the operator's behalf.
 */
function MetricsCollectionSection({
  settings,
  onChange,
}: {
  settings: PlatformSettings;
  onChange: (next: PlatformSettings) => void;
}) {
  return (
    <section>
      <h3 className="text-foreground mb-1 text-[13px] font-semibold">指标采集组件</h3>
      <p className="text-muted-foreground mb-3 text-xs leading-relaxed">
        采集组件由目标集群的 Agent 装进它自己的 Agent Namespace。这里的取值在安装时读取，
        修改后对下一次安装生效；已安装的集群需要在「可观测性 → 采集接入」中重新安装才会更换。
      </p>
      <div className="grid gap-3">
        <div className="grid gap-1.5">
          <Label htmlFor="metrics-collector-image">镜像</Label>
          <Input
            id="metrics-collector-image"
            value={settings.metrics_collector_image}
            onChange={(event) =>
              onChange({ ...settings, metrics_collector_image: event.target.value })
            }
          />
        </div>
        <PullPolicySelect
          label="拉取策略"
          value={settings.metrics_collector_image_pull_policy}
          onChange={(value) =>
            onChange({ ...settings, metrics_collector_image_pull_policy: value })
          }
        />
        <div className="grid gap-1.5">
          <h4 className="text-foreground mt-2 text-[13px] font-semibold">资源请求与限制</h4>
          <p className="text-muted-foreground text-xs leading-relaxed">
            Kubernetes 数量，例如 <code className="zke-mono">500m</code>、
            <code className="zke-mono">512Mi</code>。留空表示不在容器上设置该项：限制留空意味着
            采集组件只受目标 Namespace 自身的 LimitRange 约束。
          </p>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <QuantityField
            id="metrics-collector-cpu-request"
            label="CPU 请求"
            placeholder="50m"
            value={settings.metrics_collector_cpu_request}
            onChange={(value) => onChange({ ...settings, metrics_collector_cpu_request: value })}
          />
          <QuantityField
            id="metrics-collector-cpu-limit"
            label="CPU 限制"
            placeholder="500m"
            value={settings.metrics_collector_cpu_limit}
            onChange={(value) => onChange({ ...settings, metrics_collector_cpu_limit: value })}
          />
          <QuantityField
            id="metrics-collector-memory-request"
            label="内存请求"
            placeholder="128Mi"
            value={settings.metrics_collector_memory_request}
            onChange={(value) => onChange({ ...settings, metrics_collector_memory_request: value })}
          />
          <QuantityField
            id="metrics-collector-memory-limit"
            label="内存限制"
            placeholder="512Mi"
            value={settings.metrics_collector_memory_limit}
            onChange={(value) => onChange({ ...settings, metrics_collector_memory_limit: value })}
          />
        </div>
        <FieldHint>
          限制不能低于对应的请求；采集组件的磁盘缓冲区大小仍由 Server 配置文件中的
          <code className="zke-mono"> observability.metrics.collector_buffer_size </code>
          决定。
        </FieldHint>
      </div>
    </section>
  );
}

function QuantityField({
  id,
  label,
  placeholder,
  value,
  onChange,
}: {
  id: string;
  label: string;
  placeholder: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <div className="grid content-start gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        className="zke-mono"
        placeholder={placeholder}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      />
    </div>
  );
}

function PullPolicySelect({
  label,
  value,
  onChange,
}: {
  label: string;
  value: PlatformSettings["agent_image_pull_policy"];
  onChange: (value: PlatformSettings["agent_image_pull_policy"]) => void;
}) {
  return (
    <div className="grid content-start gap-1.5">
      <Label>{label}</Label>
      <Select
        value={value}
        onValueChange={(next) => onChange(next as PlatformSettings["agent_image_pull_policy"])}
      >
        <SelectTrigger>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {PULL_POLICIES.map((policy) => (
            <SelectItem key={policy} value={policy}>
              {policy}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

/**
 * The lifetime the Agent gives a Cluster Terminal Pod.
 *
 * Entered in minutes because that is the unit the decision is made in, and
 * stored in seconds because that is what the Agent's request carries. The
 * Server accepts 60 to 3600 seconds; a value set outside this Console that is
 * not a whole minute is shown rounded and would be rewritten by a save here.
 */
function ClusterTerminalSection({
  settings,
  onChange,
}: {
  settings: PlatformSettings;
  onChange: (next: PlatformSettings) => void;
}) {
  const minutes = Math.round(settings.cluster_terminal_session_ttl_seconds / 60);

  return (
    <section>
      <h3 className="text-foreground mb-1 text-[13px] font-semibold">集群终端</h3>
      <p className="text-muted-foreground mb-3 text-xs leading-relaxed">
        每个 Cluster Terminal 会话在目标集群中运行一个临时 Pod。存续时长到期后 Agent 回收该
        Pod，修改后立即对下一个会话生效，无需重启 Server。
      </p>
      <div className="grid max-w-xs gap-1.5">
        <Label htmlFor="cluster-terminal-ttl">会话存续时长（分钟）</Label>
        <Input
          id="cluster-terminal-ttl"
          type="number"
          min={1}
          max={60}
          step={1}
          value={minutes}
          onChange={(event) => {
            const next = Number(event.target.value);
            if (!Number.isFinite(next)) {
              return;
            }
            onChange({
              ...settings,
              cluster_terminal_session_ttl_seconds:
                Math.min(60, Math.max(1, Math.round(next))) * 60,
            });
          }}
        />
        <FieldHint>1 至 60 分钟。已经建立的会话不受本次修改影响。</FieldHint>
      </div>
    </section>
  );
}

function EndpointsSection({
  profiles,
  defaultProfileID,
}: {
  profiles: AgentEndpointProfile[];
  defaultProfileID: string;
}) {
  const createProfile = useCreateAgentEndpointProfile();
  const updateProfile = useUpdateAgentEndpointProfile();
  const deleteProfile = useDeleteAgentEndpointProfile();
  const [profileTarget, setProfileTarget] = useState<AgentEndpointProfile | null | undefined>();
  const [deleteTarget, setDeleteTarget] = useState<AgentEndpointProfile | null>(null);
  const [profileDraft, setProfileDraft] = useState<EndpointProfileInput>(EMPTY_PROFILE);

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
                  {profile.id === defaultProfileID ? <Badge tone="info">平台默认</Badge> : null}
                </div>
                <p className="text-subtle-foreground zke-mono mt-1 truncate text-xs">
                  {profile.registration_url} · {profile.quic_address}
                </p>
              </div>
              <div className="flex shrink-0 gap-1">
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={profile.id === defaultProfileID}
                  onClick={() => openProfile(profile)}
                >
                  编辑
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  className="text-danger"
                  disabled={profile.id === defaultProfileID || deleteProfile.isPending}
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
                  notifyFailure("保存 Agent 接入端点失败", error);
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
