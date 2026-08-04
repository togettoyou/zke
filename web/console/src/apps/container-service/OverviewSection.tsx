import type { ReactNode } from "react";

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
};

/**
 * One Cluster at a glance.
 *
 * Everything here is a count or a capacity total, which is the case where a
 * number reads better than a chart: there is no trend and no comparison between
 * series, only "how many" and "how much of what is available". The two capacity
 * rows get a meter because they do have a maximum to be read against.
 */
export function OverviewSection({ clusterId }: { clusterId: string }) {
  const overview = useClusterOverview(clusterId);

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* `generated_at` travels with the refresh control because together they
          are what says this page is a snapshot rather than a live reading. */}
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
          <OverviewContent overview={overview.data} />
        </div>
      )}
    </div>
  );
}

function OverviewContent({ overview }: { overview: KubernetesClusterOverview }) {
  const { nodes, namespaces, pods, workloads, resources } = overview;
  const issues = new Map(overview.issues.map((issue) => [issue.section, issue]));
  const nodeIssue = issues.get("nodes");
  const namespaceIssue = issues.get("namespaces");
  const podIssue = issues.get("pods");
  const workloadIssues = overview.issues.filter((issue) => issue.section.startsWith("workloads."));

  return (
    <div className="grid gap-3">
      {overview.partial ? <PartialNotice issues={overview.issues} /> : null}

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile
          label="节点"
          value={sectionValue(nodes.total, nodeIssue)}
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
        <StatTile label="命名空间" value={sectionValue(namespaces.total, namespaceIssue)} />
        <StatTile
          label="Pod"
          value={sectionValue(pods.total, podIssue)}
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
              <div
                key={type.resource}
                className="border-border/50 grid grid-cols-[8rem_4rem_1fr] items-baseline gap-3 border-b py-1.5 text-[13px] last:border-b-0 last:pb-0"
              >
                <span className="text-muted-foreground">{kindLabel(type.resource)}</span>
                <span className="zke-tnum text-foreground font-medium">
                  {sectionValue(entry?.total ?? 0, issue)}
                </span>
                <StatusCounts counts={entry?.status_counts} unavailable={Boolean(issue)} />
              </div>
            );
          })}
        </div>
      </DetailCard>

      <div className="grid gap-3 md:grid-cols-2">
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
 * A count, and what it is made of.
 *
 * The number is the headline and wears text ink; the badges beside it carry the
 * breakdown, each with its own label so nothing is encoded by colour alone.
 */
function StatTile({
  label,
  value,
  detail,
}: {
  label: string;
  value: ReactNode;
  detail?: ReactNode;
}) {
  return (
    <Card>
      <p className="text-muted-foreground text-xs">{label}</p>
      <p className="zke-tnum text-foreground mt-1 text-2xl font-semibold tracking-tight">{value}</p>
      {detail ? (
        <div className="text-muted-foreground mt-2 flex flex-wrap items-center gap-1.5 text-xs">
          {detail}
        </div>
      ) : null}
    </Card>
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
  kind?: "node" | "pod" | "workload";
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
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs">
      {entries.map(([status, value]) => (
        <span key={status} className="flex items-center gap-1.5">
          <StatusBadge kind={kind ?? "workload"} value={statusBadgeValue(kind, status)} />
          <span className="zke-tnum text-muted-foreground">{value}</span>
        </span>
      ))}
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

function statusBadgeValue(kind: "node" | "pod" | "workload" | undefined, status: string) {
  if (kind !== "pod" || status.length === 0) {
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
