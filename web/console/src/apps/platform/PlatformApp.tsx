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
import type {
  AgentEndpointProfile,
  PlatformSettings,
  PlatformSettingsUpdate,
  WorkloadSettings,
} from "@/api/types";
import { errorMessage } from "@/api/errors";
import { AppShell, type AppNavItem } from "@/apps/AppShell";
import type { AppComponentProps } from "@/apps/types";
import { useSessionContext } from "@/auth/session-context";
import { ErrorAlert } from "@/components/common/error-alert";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Textarea } from "@/components/ui/input";
import { FieldError, FieldHint, Label } from "@/components/ui/label";
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
import { parseQuantity } from "@/lib/quantity";

// One section per workload ZKE installs, rather than one section of images and
// another of budgets. What image runs and what it may take from the Cluster are
// one decision about one thing, and splitting them meant an operator sizing the
// Cluster Terminal read its image on a different page.
const NAV: AppNavItem[] = [
  { id: "endpoints", label: "端点", icon: Network },
  { id: "agent", label: "Agent", icon: Container },
  { id: "cluster-terminal", label: "集群终端", icon: SquareTerminal },
  { id: "metrics-collection", label: "指标采集", icon: Activity },
];

const PULL_POLICIES = ["Always", "IfNotPresent", "Never"] as const;

/**
 * The workload names the Server declares. They are the keys of
 * `settings.workloads`, and the metrics three are the same names the Cluster
 * reports its installed components under.
 */
const WORKLOAD_AGENT = "agent";
const WORKLOAD_CLUSTER_TERMINAL = "cluster-terminal";
const WORKLOAD_COLLECTOR = "collector";
const WORKLOAD_KUBE_STATE = "kube-state-metrics";
const WORKLOAD_NODE_EXPORTER = "node-exporter";

/**
 * Which workloads each section owns, and whether it owns the session lifetime.
 *
 * A save sends exactly this and nothing else. Sections used to share one write
 * of the whole settings object, which made an edit left behind in one of them
 * go to the Server when another was saved, under a button that only named one
 * of the two. The Server takes partial updates now, so a section can only ever
 * write what it shows.
 */
const SECTIONS = {
  agent: { workloads: [WORKLOAD_AGENT], sessionTTL: false },
  "cluster-terminal": { workloads: [WORKLOAD_CLUSTER_TERMINAL], sessionTTL: true },
  "metrics-collection": {
    workloads: [WORKLOAD_COLLECTOR, WORKLOAD_KUBE_STATE, WORKLOAD_NODE_EXPORTER],
    sessionTTL: false,
  },
} as const satisfies Record<string, { workloads: readonly string[]; sessionTTL: boolean }>;

/**
 * How each workload is presented. Titles and descriptions live here rather than
 * with the values: the Server stores what an operator set, and this is the only
 * side that has to explain it.
 *
 * Every one of them takes the same six fields, because every one of them is a
 * container ZKE puts into somebody else's Cluster. The placeholders are what
 * the platform ships with, and they differ on purpose — the node exporter runs
 * on every Node, so its cost is multiplied by the size of the Cluster and its
 * defaults are correspondingly small; the Agent ships with none at all, because
 * what it costs depends on how much of the Cluster is being watched.
 */
const WORKLOAD_FORMS = {
  [WORKLOAD_AGENT]: {
    label: "Agent",
    title: "Agent",
    description:
      "接入该集群的常驻组件，所有查询与操作都由它执行。镜像与预算在签发接入凭证时复制为不可变快照，后续修改只影响之后签发的凭证，不影响已接入的集群。默认不设 CPU 限制：被限流的 Agent 不会报错，只会让该集群的每一次查询、每一次终端按键都变慢。",
    placeholders: {
      cpuRequest: "50m",
      cpuLimit: "",
      memoryRequest: "128Mi",
      memoryLimit: "512Mi",
    },
  },
  [WORKLOAD_CLUSTER_TERMINAL]: {
    label: "Cluster Terminal",
    title: "Cluster Terminal",
    description:
      "每个集群终端会话在目标集群里运行的临时 Pod，会话结束即回收。预算按会话计算，因此并发会话越多、开销越大。内存上限需要留出余量——终端里的 kubectl 对着大集群取全量 JSON 是唯一真正占内存的操作。修改后立即对下一个会话生效。",
    placeholders: {
      cpuRequest: "25m",
      cpuLimit: "500m",
      memoryRequest: "64Mi",
      memoryLimit: "256Mi",
    },
  },
  [WORKLOAD_COLLECTOR]: {
    label: "指标采集组件",
    title: "采集组件（vmagent）",
    description:
      "抓取 kubelet 与下面两个导出器，经该集群 Agent 已有的连接把样本回传给 Server。每个集群一个。",
    placeholders: {
      cpuRequest: "50m",
      cpuLimit: "500m",
      memoryRequest: "128Mi",
      memoryLimit: "512Mi",
    },
  },
  [WORKLOAD_KUBE_STATE]: {
    label: "kube-state-metrics",
    title: "对象指标导出器（kube-state-metrics）",
    description:
      "节点可分配量与 Pod 申请/限制的唯一来源，也是把 Pod 归到工作负载的依据。没有它，用量曲线没有分母，利用率、申请量与工作负载视图都是空的。每个集群一个。",
    placeholders: {
      cpuRequest: "20m",
      cpuLimit: "500m",
      memoryRequest: "128Mi",
      memoryLimit: "512Mi",
    },
  },
  [WORKLOAD_NODE_EXPORTER]: {
    label: "node-exporter",
    title: "节点指标导出器（node-exporter）",
    description:
      "磁盘、文件系统与网络的唯一来源。以 DaemonSet 运行在每个节点上，因此预算取值应当保持很小——它的开销会乘以集群规模。它需要 host 网络与 hostPath，运行在 baseline 或 restricted Pod Security 级别的 Namespace 会拒绝它。",
    placeholders: {
      cpuRequest: "10m",
      cpuLimit: "200m",
      memoryRequest: "32Mi",
      memoryLimit: "128Mi",
    },
  },
} as const;

const EMPTY_WORKLOAD: WorkloadSettings = {
  image: "",
  image_pull_policy: "IfNotPresent",
  cpu_request: "",
  memory_request: "",
  cpu_limit: "",
  memory_limit: "",
};

/**
 * One workload's values.
 *
 * The Server refuses to serve settings missing a workload it declares, so a
 * blank here means this Console knows a name the Server does not — a version
 * skew, which shows as empty fields rather than a crash.
 */
function workloadOf(settings: PlatformSettings, component: string): WorkloadSettings {
  return settings.workloads[component] ?? EMPTY_WORKLOAD;
}

function workloadLabel(component: string): string {
  return WORKLOAD_FORMS[component as keyof typeof WORKLOAD_FORMS]?.label ?? component;
}

type SettingsSection = keyof typeof SECTIONS;

function isSettingsSection(section: string): section is SettingsSection {
  return section in SECTIONS;
}

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
 * All four sections read one settings object and one endpoint list from a
 * single query. The endpoint list is its own resource with its own revisions;
 * the rest is one revision covering the installed workloads and the session
 * lifetime, edited and saved one section at a time — see {@link SECTIONS}.
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

  const stored = query.data?.settings ?? null;
  const settings = draft ?? stored;

  /*
   * Leaving a section discards what was typed in it.
   *
   * The draft used to survive the move, which made two things true at once that
   * cannot both be: the section looked unchanged when it was returned to — it
   * still showed the edit — while nothing in the rest of the application knew
   * the edit existed. Dropping it means coming back to a section always shows
   * what the deployment is actually configured with.
   */
  function navigate(next: string) {
    if (next !== section) {
      setDraft(null);
      updateSettings.reset();
    }
    setSection(next);
  }

  async function save(active: SettingsSection): Promise<void> {
    if (!stored || !settings) {
      return;
    }
    // The revision comes from what the Server last returned rather than from
    // the draft: it is the Server's answer to "is this still the version you
    // read", and a draft can only ever hold a staler one.
    const update: PlatformSettingsUpdate = { expected_revision: stored.revision };
    const workloads: Record<string, WorkloadSettings> = {};
    for (const component of SECTIONS[active].workloads) {
      workloads[component] = workloadOf(settings, component);
    }
    if (Object.keys(workloads).length > 0) {
      update.workloads = workloads;
    }
    if (SECTIONS[active].sessionTTL) {
      update.cluster_terminal_session_ttl_seconds = settings.cluster_terminal_session_ttl_seconds;
    }
    try {
      await updateSettings.mutateAsync(update);
      // The mutation invalidates the query, so the saved values come back from
      // the Server rather than being held here as a second copy.
      setDraft(null);
      toast.success("平台配置已保存");
    } catch {
      // Reported next to the button by SaveRow.
    }
  }

  const activeSettingsSection = isSettingsSection(section) ? section : null;
  const problems =
    settings && activeSettingsSection ? sectionProblems(activeSettingsSection, settings) : [];

  return (
    <AppShell nav={NAV} activeId={section} onNavigate={navigate}>
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
            {section === "agent" ? <AgentSection settings={settings} onChange={setDraft} /> : null}
            {section === "cluster-terminal" ? (
              <ClusterTerminalSection settings={settings} onChange={setDraft} />
            ) : null}
            {section === "metrics-collection" ? (
              <MetricsCollectionSection settings={settings} onChange={setDraft} />
            ) : null}
            {activeSettingsSection ? (
              <SaveRow
                pending={updateSettings.isPending}
                problems={problems}
                error={updateSettings.error}
                onSave={() => void save(activeSettingsSection)}
              />
            ) : null}
          </>
        ) : null}
      </div>
    </AppShell>
  );
}

/**
 * The save button of one settings section, with whatever is stopping it.
 *
 * Two kinds of refusal end up here and they are kept apart: `problems` are what
 * this form can decide on its own, listed before anything is sent, and `error`
 * is what the Server refused. The Server checks all of it again — it owns these
 * values, and the browser is not a boundary — but a form that submits a limit
 * below its own request only to be told so a round trip later has made the
 * operator wait to learn something it already knew.
 */
function SaveRow({
  pending,
  problems,
  error,
  onSave,
}: {
  pending: boolean;
  problems: string[];
  error: unknown;
  onSave: () => void;
}) {
  return (
    <div className="grid gap-2">
      {problems.length > 0 ? (
        <Alert tone="danger">
          <ul className="list-disc space-y-0.5 pl-4">
            {problems.map((problem) => (
              <li key={problem}>{problem}</li>
            ))}
          </ul>
        </Alert>
      ) : null}
      <ErrorAlert error={error} />
      <Button
        variant="primary"
        className="justify-self-start"
        disabled={pending || problems.length > 0}
        onClick={onSave}
      >
        {pending ? "保存中…" : "保存本页配置"}
      </Button>
      <FieldHint>
        只保存当前页面的改动，其他页面的配置保持不变；切换到其他页面会丢弃本页未保存的修改。
      </FieldHint>
    </div>
  );
}

/**
 * What this section would be refused for, in the Server's own terms.
 *
 * Deliberately the same rules as `platformsettings.validateSettingsInput`, in
 * the same order: this is a second statement of one boundary, so where the two
 * disagree the Server wins and the operator sees its message instead. Anything
 * the browser cannot decide — whether an image reference resolves, say — is not
 * checked here at all rather than guessed at.
 */
function sectionProblems(section: SettingsSection, settings: PlatformSettings): string[] {
  const problems: string[] = [];
  if (SECTIONS[section].sessionTTL) {
    // Whole seconds between 60 and 3600, which is what the Server accepts — not
    // whole minutes. A deployment configured elsewhere may hold 90 seconds, and
    // a form that called that invalid would refuse to save a page the operator
    // never edited.
    const seconds = settings.cluster_terminal_session_ttl_seconds;
    if (!Number.isInteger(seconds) || seconds < 60 || seconds > 3600) {
      problems.push("集群终端会话存续时长必须在 1 至 60 分钟之间。");
    }
  }
  // A save writes every workload of the section, so every one of them is
  // checked. Naming the workload in each message matters: several identical
  // "内存限制不能低于内存请求" would send the operator back to guess which one
  // the Server meant.
  for (const component of SECTIONS[section].workloads) {
    const label = workloadLabel(component);
    const workload = workloadOf(settings, component);
    if (!validImage(workload.image)) {
      problems.push(`${label}的镜像不能为空，且不能包含空白字符。`);
    }
    for (const quantity of [
      { label: `${label}的 CPU 请求`, value: workload.cpu_request },
      { label: `${label}的 CPU 限制`, value: workload.cpu_limit },
      { label: `${label}的内存请求`, value: workload.memory_request },
      { label: `${label}的内存限制`, value: workload.memory_limit },
    ]) {
      const problem = quantityProblem(quantity.label, quantity.value);
      if (problem) {
        problems.push(problem);
      }
    }
    if (exceedsLimit(workload.cpu_request, workload.cpu_limit)) {
      problems.push(`${label}的 CPU 限制不能低于 CPU 请求。`);
    }
    if (exceedsLimit(workload.memory_request, workload.memory_limit)) {
      problems.push(`${label}的内存限制不能低于内存请求。`);
    }
  }
  return problems;
}

/**
 * The Server trims an image reference before checking it, so surrounding
 * whitespace is not what makes one invalid — whitespace inside it is.
 */
function validImage(value: string): boolean {
  const trimmed = value.trim();
  return trimmed !== "" && new TextEncoder().encode(trimmed).length <= 512 && !/\s/.test(trimmed);
}

/**
 * An empty quantity is a real answer — it means the entry is left off the
 * container — so only a value that is present and unusable is a problem.
 *
 * The label arrives already naming its workload. This used to prepend one of
 * its own, which read as "采集组件的 node-exporter 的内存限制" once there was
 * more than one budgeted workload to be wrong about.
 */
function quantityProblem(label: string, value: string): string | null {
  const trimmed = value.trim();
  if (trimmed === "") {
    return null;
  }
  if (trimmed.length > 32) {
    return `${label}过长。`;
  }
  const parsed = parseQuantity(trimmed);
  if (parsed === null) {
    return `${label}不是合法的 Kubernetes 数量，例如 500m 或 512Mi。`;
  }
  if (parsed <= 0) {
    return `${label}必须大于 0。`;
  }
  return null;
}

/** True only when both values are readable and the limit is the smaller one. */
function exceedsLimit(request: string, limit: string): boolean {
  const parsedRequest = parseQuantity(request.trim());
  const parsedLimit = parseQuantity(limit.trim());
  if (parsedRequest === null || parsedLimit === null) {
    return false;
  }
  return parsedLimit < parsedRequest;
}

function AgentSection({
  settings,
  onChange,
}: {
  settings: PlatformSettings;
  onChange: (next: PlatformSettings) => void;
}) {
  return (
    <div className="grid gap-6">
      {SECTIONS.agent.workloads.map((component) => (
        <WorkloadFields
          key={component}
          component={component}
          settings={settings}
          onChange={onChange}
        />
      ))}
      <BudgetHint />
    </div>
  );
}

/** Said once per section, because it is the same rule for every workload. */
function BudgetHint() {
  return (
    <FieldHint>
      限制不能低于对应的请求；留空表示不在容器上设置该项，把预算交给目标 Namespace 的 LimitRange。
    </FieldHint>
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
    <div className="grid gap-6">
      <section>
        <h3 className="text-foreground mb-1 text-[13px] font-semibold">指标采集组件</h3>
        <p className="text-muted-foreground text-xs leading-relaxed">
          三个组件由目标集群的 Agent 一并装进它自己的 Agent Namespace，也一并卸载：没人抓取的
          导出器是浪费，而抓取一个从未安装的目标只会产生持续失败的 job。这里的取值在安装时读取，
          修改后对下一次安装生效；已安装的集群需要在「可观测性 → 采集接入」中重新安装才会更换。
        </p>
      </section>
      {SECTIONS["metrics-collection"].workloads.map((component) => (
        <WorkloadFields
          key={component}
          component={component}
          settings={settings}
          onChange={onChange}
        />
      ))}
      <BudgetHint />
      <FieldHint>
        采集组件的磁盘缓冲区大小仍由 Server 配置文件中的
        <code className="zke-mono"> observability.metrics.collector_buffer_size </code>
        决定。
      </FieldHint>
    </div>
  );
}

/**
 * One workload: what runs, and how much of the Cluster it may take.
 *
 * The same six fields for all of them, because it is the same decision every
 * time — the Agent, the Cluster Terminal Pod and the three metrics workloads
 * are all containers ZKE puts into an operator's Cluster. An empty quantity is
 * a real answer, so nothing here fills a blank back in on their behalf.
 */
function WorkloadFields({
  component,
  settings,
  onChange,
}: {
  component: string;
  settings: PlatformSettings;
  onChange: (next: PlatformSettings) => void;
}) {
  const form = WORKLOAD_FORMS[component as keyof typeof WORKLOAD_FORMS];
  const workload = workloadOf(settings, component);
  // A workload the Server declares and this Console does not know how to
  // describe is a version skew. Rendering nothing is better than a section with
  // no title over inputs whose meaning is unexplained.
  if (!form) {
    return null;
  }
  const { placeholders } = form;
  return (
    <section>
      <h4 className="text-foreground mb-1 text-[13px] font-semibold">{form.title}</h4>
      <p className="text-muted-foreground mb-3 text-xs leading-relaxed">{form.description}</p>
      <div className="grid gap-3">
        <ImageField
          id={`${component}-image`}
          label="镜像"
          value={workload.image}
          onChange={(value) => onChange(withWorkload(settings, component, { image: value }))}
        />
        <PullPolicySelect
          label="拉取策略"
          value={workload.image_pull_policy}
          onChange={(value) =>
            onChange(withWorkload(settings, component, { image_pull_policy: value }))
          }
        />
        <div className="grid grid-cols-2 gap-3">
          <QuantityField
            id={`${component}-cpu-request`}
            label="CPU 请求"
            placeholder={placeholders.cpuRequest}
            value={workload.cpu_request}
            onChange={(value) =>
              onChange(withWorkload(settings, component, { cpu_request: value }))
            }
          />
          <QuantityField
            id={`${component}-cpu-limit`}
            label="CPU 限制"
            placeholder={placeholders.cpuLimit}
            value={workload.cpu_limit}
            onChange={(value) => onChange(withWorkload(settings, component, { cpu_limit: value }))}
          />
          <QuantityField
            id={`${component}-memory-request`}
            label="内存请求"
            placeholder={placeholders.memoryRequest}
            value={workload.memory_request}
            onChange={(value) =>
              onChange(withWorkload(settings, component, { memory_request: value }))
            }
          />
          <QuantityField
            id={`${component}-memory-limit`}
            label="内存限制"
            placeholder={placeholders.memoryLimit}
            value={workload.memory_limit}
            onChange={(value) =>
              onChange(withWorkload(settings, component, { memory_limit: value }))
            }
          />
        </div>
      </div>
    </section>
  );
}

/**
 * One workload's values, with some of them replaced.
 *
 * Every workload is edited through the same six fields, so the setter takes the
 * workload name and a patch rather than the form knowing which column it writes
 * — that mapping is what a new workload used to have to be added to.
 */
function withWorkload(
  settings: PlatformSettings,
  component: string,
  patch: Partial<WorkloadSettings>,
): PlatformSettings {
  return {
    ...settings,
    workloads: {
      ...settings.workloads,
      [component]: { ...workloadOf(settings, component), ...patch },
    },
  };
}

/** An image reference. Marked invalid only for the two things a browser knows. */
function ImageField({
  id,
  label,
  value,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
}) {
  const invalid = !validImage(value);
  return (
    <div className="grid content-start gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        className="zke-mono"
        autoComplete="off"
        spellCheck={false}
        aria-invalid={invalid || undefined}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      />
    </div>
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
  const problem = quantityProblem(label, value);
  return (
    <div className="grid content-start gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        className="zke-mono"
        placeholder={placeholder}
        autoComplete="off"
        spellCheck={false}
        aria-invalid={problem ? true : undefined}
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
  value: WorkloadSettings["image_pull_policy"];
  onChange: (value: WorkloadSettings["image_pull_policy"]) => void;
}) {
  return (
    <div className="grid content-start gap-1.5">
      <Label>{label}</Label>
      <Select
        value={value}
        onValueChange={(next) => onChange(next as WorkloadSettings["image_pull_policy"])}
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
 * The Pod one Cluster Terminal session runs, and how long it lives.
 *
 * The lifetime is entered in minutes because that is the unit the decision is
 * made in, and stored in seconds because that is what the Agent's request
 * carries. The Server accepts 60 to 3600 seconds; a value set outside this
 * Console that is not a whole minute is shown rounded and would be rewritten by
 * a save here.
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
    <div className="grid gap-6">
      {SECTIONS["cluster-terminal"].workloads.map((component) => (
        <WorkloadFields
          key={component}
          component={component}
          settings={settings}
          onChange={onChange}
        />
      ))}
      <section>
        <h4 className="text-foreground mb-1 text-[13px] font-semibold">会话存续时长</h4>
        <p className="text-muted-foreground mb-3 text-xs leading-relaxed">
          存续时长到期后 Agent 回收该 Pod。修改后立即对下一个会话生效，无需重启 Server。
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
      <BudgetHint />
    </div>
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
  // Field errors appear on the first submit rather than while the first
  // character is being typed: a form that is red before it has been filled in
  // is reporting the operator's progress, not their mistakes.
  const [submitted, setSubmitted] = useState(false);

  const problems = profileProblems(profileDraft);
  const showProblems: ProfileProblems = submitted ? problems : {};
  const pending = createProfile.isPending || updateProfile.isPending;

  function openProfile(profile?: AgentEndpointProfile) {
    createProfile.reset();
    updateProfile.reset();
    setSubmitted(false);
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

  async function submitProfile(): Promise<void> {
    setSubmitted(true);
    if (pending || Object.keys(problems).length > 0) {
      return;
    }
    try {
      if (profileTarget) {
        await updateProfile.mutateAsync({
          ...profileDraft,
          id: profileTarget.id,
          expected_revision: profileTarget.revision,
        });
      } else {
        await createProfile.mutateAsync(profileDraft);
      }
      setProfileTarget(undefined);
      toast.success("端点已保存");
    } catch {
      // Reported inside the dialog, above its footer. A toast would land behind
      // the dialog's own overlay, blurred, under the form that produced it.
    }
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
          {/* A form, so Enter submits — see the note on 组织与资源's name dialog. */}
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void submitProfile();
            }}
          >
            <div className="grid gap-3">
              <div className="grid gap-1.5">
                <Label htmlFor="endpoint-name">名称</Label>
                <Input
                  id="endpoint-name"
                  autoFocus
                  maxLength={128}
                  aria-invalid={showProblems.name ? true : undefined}
                  aria-describedby={showProblems.name ? "endpoint-name-error" : undefined}
                  value={profileDraft.name}
                  onChange={(event) =>
                    setProfileDraft({ ...profileDraft, name: event.target.value })
                  }
                />
                {showProblems.name ? (
                  <FieldError id="endpoint-name-error">{showProblems.name}</FieldError>
                ) : null}
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="endpoint-registration-url">注册 URL</Label>
                <Input
                  id="endpoint-registration-url"
                  className="zke-mono"
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="https://zke.example.com"
                  aria-invalid={showProblems.registration_url ? true : undefined}
                  aria-describedby={
                    showProblems.registration_url ? "endpoint-registration-url-error" : undefined
                  }
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
                <FieldHint>支持 HTTP 或 HTTPS，只填协议和主机，不带路径或查询参数。</FieldHint>
                {showProblems.registration_url ? (
                  <FieldError id="endpoint-registration-url-error">
                    {showProblems.registration_url}
                  </FieldError>
                ) : null}
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="endpoint-quic-address">QUIC 地址</Label>
                <Input
                  id="endpoint-quic-address"
                  className="zke-mono"
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="zke.example.com:8443"
                  aria-invalid={showProblems.quic_address ? true : undefined}
                  aria-describedby={
                    showProblems.quic_address ? "endpoint-quic-address-error" : undefined
                  }
                  value={profileDraft.quic_address}
                  onChange={(event) =>
                    setProfileDraft({ ...profileDraft, quic_address: event.target.value })
                  }
                />
                <FieldHint>host:port 格式，端口介于 1 和 65535 之间。</FieldHint>
                {showProblems.quic_address ? (
                  <FieldError id="endpoint-quic-address-error">
                    {showProblems.quic_address}
                  </FieldError>
                ) : null}
              </div>
              {profileDraft.registration_url.trimStart().toLowerCase().startsWith("https://") ? (
                <div className="grid gap-1.5">
                  <Label htmlFor="endpoint-ca">自定义 HTTPS CA（可选）</Label>
                  <Textarea
                    id="endpoint-ca"
                    rows={5}
                    className="zke-mono"
                    spellCheck={false}
                    placeholder="-----BEGIN CERTIFICATE-----"
                    aria-invalid={showProblems.registration_ca_certificate_pem ? true : undefined}
                    aria-describedby={
                      showProblems.registration_ca_certificate_pem ? "endpoint-ca-error" : undefined
                    }
                    value={profileDraft.registration_ca_certificate_pem}
                    onChange={(event) =>
                      setProfileDraft({
                        ...profileDraft,
                        registration_ca_certificate_pem: event.target.value,
                      })
                    }
                  />
                  <FieldHint>公共可信证书无需填写；仅用于自签名证书或私有 CA。</FieldHint>
                  {showProblems.registration_ca_certificate_pem ? (
                    <FieldError id="endpoint-ca-error">
                      {showProblems.registration_ca_certificate_pem}
                    </FieldError>
                  ) : null}
                </div>
              ) : null}
              <div className="flex items-center justify-between">
                <Label htmlFor="endpoint-enabled">启用</Label>
                <Switch
                  id="endpoint-enabled"
                  checked={profileDraft.enabled}
                  onCheckedChange={(checked) =>
                    setProfileDraft({ ...profileDraft, enabled: checked })
                  }
                />
              </div>
            </div>
            <ErrorAlert
              error={profileTarget ? updateProfile.error : createProfile.error}
              className="mt-3"
            />
            <DialogFooter>
              <Button
                type="button"
                variant="ghost"
                disabled={pending}
                onClick={() => setProfileTarget(undefined)}
              >
                取消
              </Button>
              <Button type="submit" variant="primary" disabled={pending}>
                {pending ? "保存中…" : "保存"}
              </Button>
            </DialogFooter>
          </form>
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

/** The name the Server's own deployment configuration reserves for itself. */
const RESERVED_PROFILE_NAME = "部署配置默认端点";

type ProfileProblems = Partial<Record<keyof EndpointProfileInput, string>>;

/**
 * What the Server would refuse this endpoint for, per field.
 *
 * Same rules as `platformsettings.validateProfileInput`, restated because the
 * refusal is worth having before a round trip — and because the Server can only
 * name one problem at a time, while a form with four empty fields has four. The
 * Server still validates all of it; this is an affordance, not the check.
 */
function profileProblems(draft: EndpointProfileInput): ProfileProblems {
  const problems: ProfileProblems = {};

  const name = draft.name.trim();
  if (name === "") {
    problems.name = "名称不能为空。";
  } else if (new TextEncoder().encode(name).length > 128) {
    problems.name = "名称不能超过 128 字节。";
  } else if (name.toLowerCase() === RESERVED_PROFILE_NAME.toLowerCase()) {
    problems.name = `「${RESERVED_PROFILE_NAME}」由 Server 部署配置保留。`;
  }

  const registrationURL = draft.registration_url.trim();
  const parsed = parseRegistrationURL(registrationURL);
  if (registrationURL === "") {
    problems.registration_url = "注册 URL 不能为空。";
  } else if (!parsed) {
    problems.registration_url = "注册 URL 必须是包含主机名的完整 HTTP 或 HTTPS 地址。";
  } else if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    problems.registration_url = "注册 URL 只支持 HTTP 或 HTTPS。";
  } else if (parsed.username !== "" || parsed.password !== "") {
    problems.registration_url = "注册 URL 不能包含用户名或密码。";
  } else if (
    parsed.search !== "" ||
    parsed.hash !== "" ||
    (parsed.pathname !== "" && parsed.pathname !== "/")
  ) {
    problems.registration_url = "注册 URL 不能包含路径、查询参数或片段。";
  }

  const quicProblem = quicAddressProblem(draft.quic_address.trim());
  if (quicProblem) {
    problems.quic_address = quicProblem;
  }

  const certificate = draft.registration_ca_certificate_pem.trim();
  if (certificate !== "") {
    if (parsed?.protocol === "http:") {
      problems.registration_ca_certificate_pem = "HTTP 注册地址不能配置 HTTPS CA。";
    } else if (!looksLikeCertificatePEM(certificate)) {
      problems.registration_ca_certificate_pem =
        "必须是单个 PEM 证书，以 -----BEGIN CERTIFICATE----- 开头。";
    }
  }

  return problems;
}

/**
 * `URL` accepts anything with a scheme, including `mailto:` and a bare
 * `sdf:` — so the host is checked separately rather than trusted from a
 * successful parse. A value with no scheme at all fails to parse, which is the
 * common case here and the one the field's own hint is about.
 */
function parseRegistrationURL(value: string): URL | null {
  try {
    const parsed = new URL(value);
    return parsed.host === "" ? null : parsed;
  } catch {
    return null;
  }
}

function quicAddressProblem(value: string): string | null {
  if (value === "") {
    return "QUIC 地址不能为空。";
  }
  // Rightmost colon, so an IPv6 literal in brackets keeps its own colons.
  const separator = value.lastIndexOf(":");
  const host = value.slice(0, separator);
  const port = value.slice(separator + 1);
  if (separator <= 0 || host.trim() === "" || !/^\d{1,5}$/.test(port)) {
    return "QUIC 地址必须使用 host:port 格式。";
  }
  const portNumber = Number(port);
  if (portNumber < 1 || portNumber > 65535) {
    return "QUIC 端口必须介于 1 和 65535 之间。";
  }
  return null;
}

/**
 * The shape of a PEM certificate, which is as far as a browser can honestly
 * go: whether the block parses as a certificate, and whether that certificate
 * is a CA, is decided by the Server.
 */
function looksLikeCertificatePEM(value: string): boolean {
  return (
    value.startsWith("-----BEGIN CERTIFICATE-----") &&
    value.endsWith("-----END CERTIFICATE-----") &&
    value.indexOf("-----BEGIN CERTIFICATE-----", 1) === -1
  );
}
