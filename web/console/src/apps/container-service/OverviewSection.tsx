import type { ReactNode } from "react";
import { ArrowUpRight, ChevronRight } from "lucide-react";

import { useClusterOverview } from "@/api/queries/cluster-overview";
import type {
  KubernetesClusterOverview,
  KubernetesClusterOverviewIssue,
  KubernetesOverviewStatusCounts,
} from "@/api/types";
import { SectionToolbarActions } from "@/apps/AppShell";
import { DetailCard } from "@/components/common/detail";
import { RefreshAction } from "@/components/common/refresh-action";
import { ErrorState, LoadingState } from "@/components/common/state";
import { StatusBadge } from "@/components/common/status";
import { Badge } from "@/components/ui/badge";
import { Alert, Card } from "@/components/ui/misc";
import { cn } from "@/lib/cn";
import { formatAbsolute } from "@/lib/time";

import { kindLabel, WORKLOAD_TYPES } from "./workload-catalog";

/** Why a section of the aggregate is incomplete, in the operator's terms. */
const ISSUE_CODES: Record<string, string> = {
  item_limit_reached: "对象数量超过服务端单部分上限，计数被截断",
  agent_not_connected: "集群 Agent 未连接",
  agent_capability_unavailable: "集群 Agent 不支持该查询",
  resource_capacity_exhausted: "服务端并发容量已满",
  response_budget_exhausted: "响应预算已耗尽",
  cluster_api_unauthenticated: "Agent 的 Kubernetes 凭证被拒绝",
  cluster_api_forbidden: "Agent 无权读取该资源",
  cluster_api_unavailable: "Kubernetes API 不可用",
  cluster_api_timeout: "读取 Kubernetes 超时",
  agent_response_too_large: "Agent 响应过大",
  invalid_agent_response: "Agent 返回了无法解析的响应",
  canceled: "查询已取消",
  cluster_api_error: "读取 Kubernetes 失败",
};

const SECTION_LABELS: Record<string, string> = {
  nodes: "节点",
  namespaces: "命名空间",
  pods: "Pod",
  "workloads.deployments": "Deployment",
  "workloads.statefulsets": "StatefulSet",
  "workloads.daemonsets": "DaemonSet",
  "workloads.jobs": "Job",
  "workloads.cronjobs": "CronJob",
  "storage.persistentvolumes": "PersistentVolume",
  "storage.persistentvolumeclaims": "PersistentVolumeClaim",
};

/**
 * Where a number on this page goes when it is clicked.
 *
 * The overview is the landing page because an operator arriving at a Cluster
 * does not yet know which category to open — which is only true if the page can
 * take them there. Every count here is a count of objects that have a list of
 * their own, so the count is the way in.
 */
export type OverviewNavigate = (section: string, tab?: string) => void;

/**
 * One Cluster at a glance.
 *
 * Everything here is a count or a capacity total, which is the case where a
 * number reads better than a chart: there is no trend and no comparison between
 * series, only "how many" and "how much of what is available". The three
 * capacity rows get a meter because they do have a maximum to be read against.
 */
export function OverviewSection({
  clusterId,
  onNavigate,
}: {
  clusterId: string;
  onNavigate?: OverviewNavigate;
}) {
  const overview = useClusterOverview(clusterId);

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* `generated_at` travels with the refresh control because together they
          are what says this page is a snapshot rather than a live reading. The
          Server holds a completed snapshot briefly, so a refresh inside that
          window returns the same timestamp — which is why the timestamp is
          shown rather than implied. */}
      <SectionToolbarActions>
        {overview.data ? (
          <span className="text-subtle-foreground text-xs">
            生成于 {formatAbsolute(overview.data.generated_at)}
          </span>
        ) : null}
        <RefreshAction isFetching={overview.isFetching} onRefresh={() => void overview.refetch()} />
      </SectionToolbarActions>
      {overview.error ? (
        <ErrorState error={overview.error} onRetry={() => void overview.refetch()} />
      ) : overview.isLoading || !overview.data ? (
        <LoadingState />
      ) : (
        <div className="min-h-0 flex-1 overflow-auto pb-1">
          <OverviewContent overview={overview.data} onNavigate={onNavigate} />
        </div>
      )}
    </div>
  );
}

function OverviewContent({
  overview,
  onNavigate,
}: {
  overview: KubernetesClusterOverview;
  onNavigate?: OverviewNavigate;
}) {
  const { nodes, namespaces, pods, workloads, storage, resources } = overview;
  const issues = new Map(overview.issues.map((issue) => [issue.section, issue]));
  const nodeIssue = issues.get("nodes");
  const namespaceIssue = issues.get("namespaces");
  const podIssue = issues.get("pods");
  const volumeIssue = issues.get("storage.persistentvolumes");
  const claimIssue = issues.get("storage.persistentvolumeclaims");
  const workloadIssues = overview.issues.filter((issue) => issue.section.startsWith("workloads."));

  return (
    <div className="grid gap-3">
      {overview.partial ? <PartialNotice issues={overview.issues} /> : null}

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile
          label="节点"
          value={sectionValue(nodes.total, nodeIssue)}
          open={onNavigate ? { label: "节点", run: () => onNavigate("nodes") } : undefined}
          detail={
            sectionHasData(nodeIssue) ? (
              <>
                <StatusBadge kind="node" value="ready" />
                <span className="zke-tnum">{nodes.status_counts.ready ?? 0}</span>
                {nodes.unschedulable > 0 ? (
                  <>
                    <StatusBadge kind="scheduling" value="unschedulable" />
                    <span className="zke-tnum">{nodes.unschedulable}</span>
                  </>
                ) : null}
              </>
            ) : (
              "数据不可用"
            )
          }
        />
        <StatTile
          label="命名空间"
          value={sectionValue(namespaces.total, namespaceIssue)}
          open={onNavigate ? { label: "命名空间", run: () => onNavigate("namespaces") } : undefined}
          /* Active is the whole story until it is not: a Namespace that has been
             terminating for a while is stuck on a finalizer, and it is the one
             thing this count can say that the total cannot. */
          detail={
            sectionHasData(namespaceIssue) ? (
              <>
                <Badge tone="success">活跃</Badge>
                <span className="zke-tnum">{namespaces.status_counts.active ?? 0}</span>
                {(namespaces.status_counts.terminating ?? 0) > 0 ? (
                  <>
                    <Badge tone="warning">终止中</Badge>
                    <span className="zke-tnum">{namespaces.status_counts.terminating}</span>
                  </>
                ) : null}
              </>
            ) : (
              "数据不可用"
            )
          }
        />
        <StatTile
          label="Pod"
          value={sectionValue(pods.total, podIssue)}
          open={
            onNavigate
              ? { label: "Pod", run: () => onNavigate("pods"), namespaced: true }
              : undefined
          }
          detail={
            sectionHasData(podIssue) ? (
              <>
                <Badge tone="success">就绪</Badge>
                <span className="zke-tnum">{pods.ready}</span>
                {pods.not_ready > 0 ? (
                  <>
                    <Badge tone="warning">未就绪</Badge>
                    <span className="zke-tnum">{pods.not_ready}</span>
                  </>
                ) : null}
                {pods.terminating > 0 ? (
                  <>
                    <Badge tone="neutral">终止中</Badge>
                    <span className="zke-tnum">{pods.terminating}</span>
                  </>
                ) : null}
              </>
            ) : (
              "数据不可用"
            )
          }
        />
        <StatTile
          label="工作负载"
          value={workloadIssues.length > 0 ? `≥${workloads.total}` : workloads.total}
          open={
            onNavigate
              ? { label: "工作负载", run: () => onNavigate("workloads"), namespaced: true }
              : undefined
          }
          /* The five types are broken out below; what the tile adds is the one
             reading that spans them — how many of everything is healthy. */
          detail={
            <StatusCounts
              counts={workloads.status_counts}
              kind="workload"
              unavailable={workloadIssues.length > 0}
            />
          }
        />
      </div>

      <DetailCard title="资源">
        <div className="grid gap-4 py-1">
          <Meter
            label="CPU 请求"
            used={resources.cpu_requested_millis}
            total={resources.cpu_allocatable_millis}
            format={formatCores}
            usedIssue={podIssue}
            totalIssue={nodeIssue}
            footnote={resourceCapacity(
              "容量",
              resources.cpu_capacity_millis,
              formatCores,
              nodeIssue,
            )}
          />
          <Meter
            label="内存请求"
            used={resources.memory_requested_bytes}
            total={resources.memory_allocatable_bytes}
            format={formatBytes}
            usedIssue={podIssue}
            totalIssue={nodeIssue}
            footnote={resourceCapacity(
              "容量",
              resources.memory_capacity_bytes,
              formatBytes,
              nodeIssue,
            )}
          />
          <Meter
            label="Pod 数量"
            used={resources.non_terminal_pods}
            total={resources.pod_allocatable}
            format={(value) => String(value)}
            usedIssue={podIssue}
            totalIssue={nodeIssue}
            footnote={resourceCapacity(
              "容量",
              resources.pod_capacity,
              (value) => String(value),
              nodeIssue,
            )}
          />
        </div>
      </DetailCard>

      <DetailCard title="工作负载分布">
        <div className="grid gap-2 py-1">
          {WORKLOAD_TYPES.map((type) => {
            const entry = workloads.by_resource.find((item) => item.resource === type.resource);
            const issue = issues.get(`workloads.${type.resource}`);
            return (
              <CountRow
                key={type.resource}
                label={kindLabel(type.resource)}
                value={sectionValue(entry?.total ?? 0, issue)}
                open={
                  onNavigate
                    ? {
                        label: kindLabel(type.resource),
                        run: () => onNavigate("workloads", type.resource),
                        namespaced: true,
                      }
                    : undefined
                }
              >
                <StatusCounts counts={entry?.status_counts} unavailable={Boolean(issue)} />
              </CountRow>
            );
          })}
        </div>
      </DetailCard>

      {/*
       * Storage gets counts and totals, not a meter.
       *
       * CPU and memory are read against what the Cluster can allocate, and
       * Kubernetes reports that number. Volume capacity has no such maximum:
       * under a dynamic provisioner the supply is whatever the backing system
       * will still hand out, and nothing in the API says how much that is. A bar
       * drawn here would need a denominator that does not exist, so what is
       * shown is how much has been provisioned, how much has been asked for, and
       * which phases the objects are sitting in — a Pending claim is the thing
       * worth seeing, and it is a count.
       */}
      <DetailCard title="存储">
        <div className="grid gap-2 py-1">
          <CountRow
            label="PersistentVolume"
            value={sectionValue(storage.persistent_volumes.total, volumeIssue)}
            note={resourceCapacity(
              "总容量",
              storage.persistent_volumes.capacity_bytes,
              formatBytes,
              volumeIssue,
            )}
            open={
              onNavigate
                ? {
                    label: "PersistentVolume",
                    run: () => onNavigate("storage", "persistentvolumes"),
                  }
                : undefined
            }
          >
            <StatusCounts
              counts={storage.persistent_volumes.status_counts}
              kind="volume"
              unavailable={Boolean(volumeIssue)}
            />
          </CountRow>
          <CountRow
            label="PersistentVolumeClaim"
            value={sectionValue(storage.persistent_volume_claims.total, claimIssue)}
            note={resourceCapacity(
              "申请容量",
              storage.persistent_volume_claims.requested_bytes,
              formatBytes,
              claimIssue,
            )}
            open={
              onNavigate
                ? {
                    label: "PersistentVolumeClaim",
                    run: () => onNavigate("storage", "persistentvolumeclaims"),
                    namespaced: true,
                  }
                : undefined
            }
          >
            <StatusCounts
              counts={storage.persistent_volume_claims.status_counts}
              kind="volume"
              unavailable={Boolean(claimIssue)}
            />
          </CountRow>
        </div>
      </DetailCard>

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        <DetailCard title="节点状态">
          <div className="py-1">
            <StatusCounts
              counts={nodes.status_counts}
              kind="node"
              unavailable={Boolean(nodeIssue)}
            />
          </div>
        </DetailCard>
        <DetailCard title="Pod 状态">
          <div className="py-1">
            <StatusCounts counts={pods.status_counts} kind="pod" unavailable={Boolean(podIssue)} />
          </div>
        </DetailCard>
        <DetailCard title="Kubernetes 版本">
          <VersionCounts versions={nodes.kubernetes_versions} unavailable={Boolean(nodeIssue)} />
        </DetailCard>
      </div>
    </div>
  );
}

function PartialNotice({ issues }: { issues: KubernetesClusterOverviewIssue[] }) {
  return (
    <Alert tone="warning">
      <p className="font-medium">这份概览不完整，下列部分的计数偏低或缺失：</p>
      <ul className="mt-1.5 list-disc space-y-0.5 pl-5">
        {issues.map((issue) => (
          <li key={`${issue.section}/${issue.code}`}>
            {SECTION_LABELS[issue.section] ?? issue.section}：
            {ISSUE_CODES[issue.code] ?? issue.code}
          </li>
        ))}
      </ul>
    </Alert>
  );
}

/**
 * The list a count opens, and what the operator will find there.
 *
 * `namespaced` is not decoration. This page counts a whole Cluster, while the
 * Pod, workload and claim lists are scoped by one Namespace, so arriving at one
 * of them shows a smaller number than the tile that led there. Saying so before
 * the click is cheaper than explaining the difference after it.
 */
type OverviewLink = { label: string; run: () => void; namespaced?: boolean };

function openTitle(link: OverviewLink): string {
  return link.namespaced
    ? `打开「${link.label}」列表；该列表按命名空间定域，显示的是当前选中的命名空间`
    : `打开「${link.label}」列表`;
}

/** Hover, focus and cursor treatment shared by everything on this page that opens a list. */
const OPENS_LIST =
  "zke-focus hover:border-primary/40 hover:bg-surface-muted/50 cursor-pointer text-left transition-colors";

/**
 * A count, and what it is made of.
 *
 * The number is the headline and wears text ink; the badges beside it carry the
 * breakdown, each with its own label so nothing is encoded by colour alone.
 */
function StatTile({
  label,
  value,
  detail,
  open,
}: {
  label: string;
  value: ReactNode;
  detail?: ReactNode;
  open?: OverviewLink;
}) {
  /*
   * Spans rather than paragraphs and divs: a clickable tile is a `button`, and
   * a button may only contain phrasing content. The display classes make them
   * lay out exactly as the block elements did.
   */
  const body = (
    <>
      <span className="text-muted-foreground flex items-center gap-1 text-xs">
        {label}
        {open ? <ArrowUpRight className="size-3 shrink-0" aria-hidden /> : null}
      </span>
      <span className="zke-tnum text-foreground mt-1 block text-[22px] font-semibold tracking-tight">
        {value}
      </span>
      {detail ? (
        <span className="text-muted-foreground mt-2 flex flex-wrap items-center gap-1.5 text-xs">
          {detail}
        </span>
      ) : null}
    </>
  );
  if (!open) {
    return <Card>{body}</Card>;
  }
  return (
    <Card asChild>
      <button type="button" onClick={open.run} title={openTitle(open)} className={OPENS_LIST}>
        {body}
      </button>
    </Card>
  );
}

/** One named count inside a card, with its status histogram beside it. */
function CountRow({
  label,
  value,
  note,
  open,
  children,
}: {
  label: string;
  value: ReactNode;
  /** A second number about the same objects, such as their total capacity. */
  note?: string;
  open?: OverviewLink;
  children: ReactNode;
}) {
  const row = (
    <>
      <span className="text-muted-foreground min-w-0 break-words">{label}</span>
      <span className="zke-tnum text-foreground font-medium">{value}</span>
      <span className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1.5">
        {children}
        {note ? <span className="text-subtle-foreground text-xs">{note}</span> : null}
      </span>
      {open ? (
        <ChevronRight
          className="text-subtle-foreground size-3.5 shrink-0 self-center"
          aria-hidden
        />
      ) : null}
    </>
  );
  const layout = cn(
    "border-border/50 grid items-baseline gap-3 border-b py-1.5 text-[13px] last:border-b-0 last:pb-0",
    open ? "grid-cols-[10rem_4rem_1fr_auto]" : "grid-cols-[10rem_4rem_1fr]",
  );
  if (!open) {
    return <div className={layout}>{row}</div>;
  }
  return (
    <button
      type="button"
      onClick={open.run}
      title={openTitle(open)}
      className={cn(layout, OPENS_LIST, "rounded-control -mx-1 px-1")}
    >
      {row}
    </button>
  );
}

/**
 * One magnitude read against the maximum it is allowed to reach.
 *
 * The fill is a single hue because there is one measure, not a set of series.
 * It turns to the warning and danger steps only where that says something true:
 * a request total at or past what the Cluster can allocate is a condition, not a
 * shade — and the numbers above it say so in text either way.
 */
function Meter({
  label,
  used,
  total,
  format,
  footnote,
  usedIssue,
  totalIssue,
}: {
  label: string;
  used: number;
  total: number;
  format: (value: number) => string;
  footnote?: string;
  usedIssue?: KubernetesClusterOverviewIssue;
  totalIssue?: KubernetesClusterOverviewIssue;
}) {
  const ratioKnown = !usedIssue && !totalIssue && total > 0;
  const ratio = ratioKnown ? used / total : 0;
  const percent = Math.min(100, Math.round(ratio * 100));
  const tone = ratio >= 1 ? "bg-danger" : ratio >= 0.9 ? "bg-warning" : "bg-primary";

  return (
    <div className="grid gap-1.5">
      <div className="flex flex-wrap items-baseline justify-between gap-2 text-[13px]">
        <span className="text-muted-foreground">{label}</span>
        <span className="zke-tnum text-foreground">
          {sectionValue(used, usedIssue, format)} / {sectionValue(total, totalIssue, format)}
          <span className="text-subtle-foreground ml-2">
            {ratioKnown ? `${percent}%` : "比例不可用"}
          </span>
        </span>
      </div>
      {/* A recessive track with a rounded fill: the bar is the mark, the border
          radius is what keeps a near-empty meter from reading as a hairline. */}
      <div className="bg-border/60 h-1.5 w-full overflow-hidden rounded-full">
        <div
          className={cn("h-full rounded-full transition-[width]", tone)}
          style={{ width: `${ratioKnown ? percent : 0}%` }}
        />
      </div>
      {footnote ? <span className="text-subtle-foreground text-xs">{footnote}</span> : null}
    </div>
  );
}

/** The status histogram of one section, sorted by count. */
function StatusCounts({
  counts,
  kind,
  unavailable = false,
}: {
  counts: KubernetesOverviewStatusCounts | undefined;
  kind?: "node" | "pod" | "workload" | "volume";
  unavailable?: boolean;
}) {
  const entries = Object.entries(counts ?? {})
    .filter(([, value]) => value > 0)
    .sort((left, right) => right[1] - left[1]);
  if (entries.length === 0) {
    return (
      <span className="text-subtle-foreground text-xs">{unavailable ? "数据不完整" : "—"}</span>
    );
  }
  return (
    // A span, because these histograms also appear inside the rows that open a
    // list, and those rows are buttons.
    <span className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs">
      {entries.map(([status, value]) => (
        <span key={status} className="flex items-center gap-1.5">
          <StatusBadge kind={kind ?? "workload"} value={statusBadgeValue(kind, status)} />
          <span className="zke-tnum text-muted-foreground">{value}</span>
        </span>
      ))}
    </span>
  );
}

/**
 * How many Nodes run each kubelet version.
 *
 * A Cluster mid-upgrade reports more than one, and the skew between them decides
 * which APIs are safe to use — so the second version appearing here is itself the
 * message. It is stated rather than coloured: running two versions is what an
 * upgrade looks like from the inside, not a fault.
 */
function VersionCounts({
  versions,
  unavailable,
}: {
  versions: Record<string, number>;
  unavailable: boolean;
}) {
  const entries = Object.entries(versions)
    .filter(([, value]) => value > 0)
    .sort((left, right) => right[1] - left[1] || left[0].localeCompare(right[0]));
  if (entries.length === 0) {
    return (
      <p className="text-subtle-foreground py-1 text-xs">{unavailable ? "数据不完整" : "—"}</p>
    );
  }
  return (
    <div className="py-1">
      <div className="grid gap-1.5">
        {entries.map(([version, count]) => (
          <div key={version} className="flex items-baseline justify-between gap-3 text-[13px]">
            <span className="zke-mono text-foreground min-w-0 break-all">{version}</span>
            <span className="zke-tnum text-muted-foreground shrink-0">{count} 个节点</span>
          </div>
        ))}
      </div>
      {entries.length > 1 ? (
        <p className="text-subtle-foreground mt-2 text-xs">
          集群中的节点运行着不同版本，通常出现在升级过程中。
        </p>
      ) : null}
    </div>
  );
}

function sectionValue(
  value: number,
  issue: KubernetesClusterOverviewIssue | undefined,
  format: (value: number) => string = String,
): string {
  if (!issue) {
    return format(value);
  }
  return issue.code === "item_limit_reached" ? `≥${format(value)}` : "—";
}

function sectionHasData(issue: KubernetesClusterOverviewIssue | undefined): boolean {
  return !issue || issue.code === "item_limit_reached";
}

function resourceCapacity(
  label: string,
  value: number,
  format: (value: number) => string,
  issue: KubernetesClusterOverviewIssue | undefined,
): string {
  const formatted = sectionValue(value, issue, format);
  return formatted === "—" ? `${label}未知` : `${label} ${formatted}`;
}

/**
 * The overview lowercases every status it counts; two of the label tables are
 * keyed by the Kubernetes phase as Kubernetes writes it.
 */
function statusBadgeValue(
  kind: "node" | "pod" | "workload" | "volume" | undefined,
  status: string,
) {
  if ((kind !== "pod" && kind !== "volume") || status.length === 0) {
    return status;
  }
  return status.charAt(0).toUpperCase() + status.slice(1);
}

/** Millicores as the operator thinks of them: cores once there is more than one. */
function formatCores(millis: number): string {
  if (millis === 0) {
    return "0";
  }
  if (millis < 1_000) {
    return `${millis}m`;
  }
  return `${(millis / 1_000).toFixed(millis % 1_000 === 0 ? 0 : 2)} core`;
}

function formatBytes(bytes: number): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}
