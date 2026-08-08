import { useMemo } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import type {
  KubernetesDescribe,
  KubernetesDescribeEvent,
  KubernetesDescribeFinding,
  KubernetesDescribeFindingCode,
} from "@/api/types";
import { PageHeader, SectionToolbarActions } from "@/apps/AppShell";
import { DataTable } from "@/components/common/data-table";
import { RefreshAction } from "@/components/common/refresh-action";
import { ErrorState, LoadingState } from "@/components/common/state";
import { CopyButton, RelativeTime, StatusBadge } from "@/components/common/status";
import { Badge } from "@/components/ui/badge";
import { Card, CardTitle, Alert } from "@/components/ui/misc";
import { formatAbsolute } from "@/lib/time";

/**
 * What each finding means, and what to do about it.
 *
 * The Server sends a stable code, the Kubernetes reason and the upstream message
 * verbatim, and no prose: an explanation written next to the rule that produced
 * it would be the Server saying things the Cluster never said, and it could not
 * be translated. So the wording lives here, keyed by code, and the message
 * underneath it is always Kubernetes' own.
 */
const FINDING_LABELS: Record<KubernetesDescribeFindingCode, { title: string; hint: string }> = {
  PodUnschedulable: {
    title: "无法调度",
    hint: "调度器找不到满足要求的节点。核对资源 requests、nodeSelector/亲和性、污点与容忍，以及节点当前余量。",
  },
  ImagePullFailure: {
    title: "镜像拉取失败",
    hint: "核对镜像地址与标签是否存在、imagePullSecrets 是否正确，以及节点能否访问镜像仓库。",
  },
  ContainerConfigError: {
    title: "容器配置错误",
    hint: "容器引用的 ConfigMap、Secret 或其中的键不存在。按消息中的名称核对引用对象是否在同一命名空间。",
  },
  CrashLoopBackOff: {
    title: "反复重启",
    hint: "容器启动后退出，Kubernetes 已进入退避等待。查看上一次容器日志与退出码定位应用自身的失败。",
  },
  ContainerTerminated: {
    title: "容器异常退出",
    hint: "容器以非正常状态结束。结合退出码与容器日志判断是应用错误还是启动命令有误。",
  },
  OOMKilled: {
    title: "内存超限被终止",
    hint: "容器达到内存上限后被 kubelet 终止。核对 limits.memory 与应用实际用量。",
  },
  VolumeMountFailure: {
    title: "存储挂载失败",
    hint: "核对 PVC 是否已绑定、StorageClass 是否可用，以及节点上的挂载与附着是否成功。",
  },
  ProbeFailure: {
    title: "探针未通过",
    hint: "readiness 或 liveness 探针持续失败，Pod 因此不进入服务。核对探针路径、端口与初始延迟。",
  },
};

const OMITTED_EVENTS: Record<string, string> = {
  unsupported_scope:
    "该对象是集群级资源，Event 所属命名空间属于约定而非规则，因此不展示可能属于其他对象的事件。",
  unavailable:
    "本次未能读取该对象的 Kubernetes Event，下面的对象状态与结论仍然有效，但缺少事件佐证。",
};

export type DescribeViewProps = {
  /** The object's name, shown as the view's title. */
  name: string;
  /** What kind of object this is, e.g. `Pod`。 */
  kindLabel: string;
  data: KubernetesDescribe | undefined;
  isLoading: boolean;
  isFetching: boolean;
  error: unknown;
  onRetry: () => void;
  onBack: () => void;
};

/**
 * The answer to "why is this not running", in one place.
 *
 * It exists because the two halves of that answer used to live a page apart:
 * the object's detail said a container was waiting, and finding out why meant
 * leaving for the Namespace's Event list and reading through everything that
 * happened to every other object in it. This view is the join — the findings
 * first, the Events that back them underneath — and each finding names the
 * condition, container state or Event it was read from, so a reader can check
 * it rather than take it.
 */
export function DescribeView({
  name,
  kindLabel,
  data,
  isLoading,
  isFetching,
  error,
  onRetry,
  onBack,
}: DescribeViewProps) {
  const columns = useMemo<ColumnDef<KubernetesDescribeEvent, unknown>[]>(
    () => [
      {
        header: "类型",
        size: 90,
        cell: ({ row }) => <StatusBadge kind="eventType" value={row.original.type} />,
      },
      {
        header: "原因",
        size: 170,
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground font-medium break-words">{row.original.reason}</span>
            {row.original.container ? (
              <span className="text-subtle-foreground text-xs break-all">
                容器 {row.original.container}
              </span>
            ) : null}
          </div>
        ),
      },
      {
        header: "消息",
        cell: ({ row }) => (
          <span className="text-muted-foreground text-xs break-words">{row.original.message}</span>
        ),
      },
      {
        header: "来源",
        size: 130,
        cell: ({ row }) => (
          <span className="text-subtle-foreground text-xs break-all">
            {row.original.source || "—"}
          </span>
        ),
      },
      {
        header: "次数",
        size: 70,
        cell: ({ row }) => <span className="zke-tnum">{row.original.count || 1}</span>,
      },
      {
        header: "最近发生",
        size: 130,
        cell: ({ row }) => (
          <RelativeTime
            value={row.original.last_seen ?? row.original.first_seen}
            className="text-muted-foreground"
          />
        ),
      },
    ],
    [],
  );

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      <PageHeader
        title={`${name} · 诊断`}
        onBack={onBack}
        actions={
          data ? (
            <CopyButton value={() => describeText(data, kindLabel)} label="复制为文本" />
          ) : null
        }
      />
      <SectionToolbarActions>
        <RefreshAction isFetching={isFetching} onRefresh={onRetry} />
      </SectionToolbarActions>

      {error ? (
        <ErrorState error={error} onRetry={onRetry} />
      ) : isLoading || !data ? (
        <LoadingState />
      ) : (
        <div className="flex min-h-0 flex-col gap-3">
          {data.findings.length === 0 ? (
            <Alert tone="success">
              未发现已知问题。诊断规则只覆盖已建模的类型与已知失败模式，没有结论不等于对象一定健康，
              仍可结合下方事件与对象详情判断。
            </Alert>
          ) : (
            <div className="grid gap-2">
              {data.findings.map((finding, index) => (
                <FindingCard
                  key={`${finding.code}-${finding.scope ?? ""}-${index}`}
                  finding={finding}
                />
              ))}
            </div>
          )}

          {data.events.omitted ? (
            <Alert tone={data.events.omitted === "unavailable" ? "warning" : "info"}>
              {OMITTED_EVENTS[data.events.omitted] ?? `事件未返回：${data.events.omitted}`}
            </Alert>
          ) : null}
          {data.events.truncated ? (
            <Alert tone="info">
              该对象的事件多于本次窗口，只展示最近的一部分；更早的事件可在事件页按对象查看。
            </Alert>
          ) : null}

          <Card className="flex min-h-0 flex-1 flex-col">
            <CardTitle>该对象的事件</CardTitle>
            <div className="mt-2 flex min-h-0 flex-1 flex-col">
              <DataTable
                columns={columns}
                data={data.events.items}
                rowKey={(event) => event.uid}
                emptyTitle="没有该对象的事件"
                emptyDescription="集群中没有属于该对象的 Kubernetes Event，或事件已按保留期被回收。"
              />
            </div>
          </Card>
        </div>
      )}
    </div>
  );
}

function FindingCard({ finding }: { finding: KubernetesDescribeFinding }) {
  const label = FINDING_LABELS[finding.code];
  return (
    <Card className="border-warning/35">
      <div className="flex flex-wrap items-center gap-2">
        <Badge tone="warning">{label?.title ?? finding.code}</Badge>
        {finding.scope ? (
          <span className="text-muted-foreground text-xs break-all">容器 {finding.scope}</span>
        ) : null}
        {finding.reason ? (
          <span className="zke-mono text-subtle-foreground text-xs break-all">
            {finding.reason}
          </span>
        ) : null}
        {finding.exit_code !== undefined ? (
          <span className="zke-tnum text-subtle-foreground text-xs">
            退出码 {finding.exit_code}
          </span>
        ) : null}
      </div>
      {finding.message ? (
        // Kubernetes' own words, kept as they arrived: this is the line an
        // operator pastes into a ticket, and paraphrasing it loses the detail
        // that makes it searchable.
        <p className="text-foreground mt-2 text-[13px] leading-relaxed break-words">
          {finding.message}
        </p>
      ) : null}
      {label ? (
        <p className="text-muted-foreground mt-1.5 text-xs leading-relaxed">{label.hint}</p>
      ) : null}
      {finding.evidence.length > 0 ? (
        <div className="text-subtle-foreground mt-2 flex flex-wrap items-center gap-1.5 text-xs">
          <span>依据</span>
          {finding.evidence.map((item) => (
            <span
              key={`${item.kind}-${item.name}`}
              className="border-border/60 rounded-full border px-2 py-0.5"
            >
              {evidenceLabel(item.kind)} · {item.name}
            </span>
          ))}
        </div>
      ) : null}
    </Card>
  );
}

function evidenceLabel(kind: string): string {
  switch (kind) {
    case "Condition":
      return "条件";
    case "ContainerState":
      return "容器状态";
    case "Event":
      return "事件";
    default:
      return kind;
  }
}

/**
 * The same view as plain text, for a ticket or a chat message.
 *
 * Built here rather than served by the Server: it is a rendering of what is
 * already on screen, and a second wording of the same facts maintained on the
 * other side of the API would drift from this one.
 */
function describeText(data: KubernetesDescribe, kindLabel: string): string {
  const lines: string[] = [
    `${kindLabel}: ${data.target.namespace ? `${data.target.namespace}/` : ""}${data.target.name}`,
    `UID: ${data.target.uid || "—"}`,
    "",
    `诊断（${data.findings.length}）`,
  ];
  if (data.findings.length === 0) {
    lines.push("  未发现已知问题");
  }
  for (const finding of data.findings) {
    const label = FINDING_LABELS[finding.code]?.title ?? finding.code;
    const head = [
      `  - ${label} [${finding.code}]`,
      finding.scope ? `容器=${finding.scope}` : "",
      finding.reason ? `reason=${finding.reason}` : "",
      finding.exit_code === undefined ? "" : `exitCode=${finding.exit_code}`,
    ].filter(Boolean);
    lines.push(head.join(" "));
    if (finding.message) {
      lines.push(`      ${finding.message}`);
    }
  }
  lines.push("", `事件（${data.events.items.length}${data.events.truncated ? "，已截断" : ""}）`);
  if (data.events.omitted) {
    lines.push(`  未读取：${data.events.omitted}`);
  }
  for (const event of data.events.items) {
    const seen = event.last_seen ?? event.first_seen;
    lines.push(
      `  ${seen ? formatAbsolute(seen) : "—"} ${event.type} ${event.reason}` +
        `${event.container ? ` (${event.container})` : ""} x${event.count || 1} ${event.message}`,
    );
  }
  return lines.join("\n");
}
