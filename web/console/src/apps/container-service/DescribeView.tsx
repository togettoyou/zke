import { useMemo } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import type {
  KubernetesDescribe,
  KubernetesDescribeEvent,
  KubernetesDescribeFinding,
  KubernetesDescribeFindingCode,
  KubernetesDescribeRelated,
  KubernetesDescribeRelatedObject,
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
  PVCPending: {
    title: "存储声明等待绑定",
    hint: "工作负载引用的 PVC 尚未绑定。核对 StorageClass、动态供应器、容量与访问模式；WaitForFirstConsumer 表示需要先完成 Pod 调度。",
  },
  WorkloadProgressStalled: {
    title: "发布进度停滞",
    hint: "控制器已超过进度期限并停止等待。原因通常在下方未就绪的 Pod 上。",
  },
  ReplicaCreateRejected: {
    title: "Pod 创建被拒绝",
    hint: "Pod 在被创建出来之前就被拒绝，因此没有 Pod 可查。常见于 ResourceQuota 超限、Pod Security 准入策略或 ServiceAccount 缺失。",
  },
  WorkloadFailed: {
    title: "任务失败",
    hint: "Job 已达到重试上限或运行超期。具体失败原因在它的 Pod 与容器退出码上。",
  },
  NodeNotReady: {
    title: "节点未就绪",
    hint: "kubelet 未持续报告 Ready。核对节点网络、kubelet 状态、运行时与系统资源。",
  },
  NodeMemoryPressure: {
    title: "节点内存压力",
    hint: "kubelet 已报告内存压力。检查节点内存用量、Pod requests/limits 与驱逐记录。",
  },
  NodeDiskPressure: {
    title: "节点磁盘压力",
    hint: "kubelet 已报告磁盘压力。检查镜像文件系统、容器日志和临时存储空间。",
  },
  NodePIDPressure: {
    title: "节点 PID 压力",
    hint: "节点可用进程 ID 接近耗尽。检查异常进程数量以及 Pod 的进程使用。",
  },
  NodeNetworkUnavailable: {
    title: "节点网络不可用",
    hint: "节点网络尚未正确配置。检查 CNI 组件、路由与节点网络状态。",
  },
  NodeSchedulingDisabled: {
    title: "节点已停止调度",
    hint: "该节点被标记为不可调度。若维护已经结束，可在确认节点健康后恢复调度。",
  },
  NodeCPURequestsHigh: {
    title: "CPU 请求接近可分配上限",
    hint: "非终止 Pod 的 CPU requests 已达到节点可分配量的 90%。新 Pod 可能因 CPU 不足无法调度。",
  },
  NodeMemoryRequestsHigh: {
    title: "内存请求接近可分配上限",
    hint: "非终止 Pod 的内存 requests 已达到节点可分配量的 90%。新 Pod 可能因内存不足无法调度。",
  },
  NodePodCapacityHigh: {
    title: "Pod 数接近可分配上限",
    hint: "节点上的非终止 Pod 数已达到可分配 Pod 数的 90%。新 Pod 可能因 Pod 容量不足无法调度。",
  },
  ServiceNoEndpoints: {
    title: "没有后端端点",
    hint: "该 Service 的 EndpointSlice 中没有端点。核对 selector 是否能匹配目标 Pod，或 selectorless Service 是否已创建对应 EndpointSlice。",
  },
  ServiceNoReadyEndpoints: {
    title: "没有就绪后端",
    hint: "EndpointSlice 已包含后端，但当前没有可接收流量的就绪端点。检查下方后端 Pod 的状态、readiness 探针和终止状态。",
  },
  ServiceLoadBalancerPending: {
    title: "外部地址等待分配",
    hint: "LoadBalancer Service 尚未获得外部 IP 或主机名。检查集群的负载均衡控制器、云配额与相关事件。",
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
  // A workload's timeline carries the Events of several objects, so each line
  // has to say which one it is about. A single object's does not: every line
  // would repeat the name in the page header.
  const aggregated = data?.family === "workload";
  const columns = useMemo<ColumnDef<KubernetesDescribeEvent, unknown>[]>(
    () => [
      {
        header: "类型",
        size: 90,
        cell: ({ row }) => <StatusBadge kind="eventType" value={row.original.type} />,
      },
      ...(aggregated
        ? [
            {
              header: "对象",
              size: 190,
              cell: ({ row }: { row: { original: KubernetesDescribeEvent } }) => (
                <div className="flex flex-col gap-0.5">
                  <span className="text-foreground break-all">
                    {row.original.regarding.name || "—"}
                  </span>
                  <span className="text-subtle-foreground text-xs">
                    {row.original.regarding.kind || "未知类型"}
                  </span>
                </div>
              ),
            } satisfies ColumnDef<KubernetesDescribeEvent, unknown>,
          ]
        : []),
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
    [aggregated],
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
        <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto pb-1">
          {data.findings.length === 0 && !relatedHasFindings(data) ? (
            <Alert tone="success">
              未发现已知问题。诊断规则只覆盖已建模的类型与已知失败模式，没有结论不等于对象一定健康，
              仍可结合下方事件与对象详情判断。
            </Alert>
          ) : null}
          {data.findings.length > 0 ? (
            <div className="grid gap-2">
              {data.findings.map((finding, index) => (
                <FindingCard
                  key={`${finding.code}-${finding.scope ?? ""}-${index}`}
                  finding={finding}
                />
              ))}
            </div>
          ) : null}

          {data.node ? <NodeDiagnosticSummary data={data} /> : null}
          {data.storage?.persistent_volume_claim ? (
            <PersistentVolumeClaimDiagnosticSummary data={data} />
          ) : null}
          {data.networking?.service && data.service_endpoints ? (
            <ServiceDiagnosticSummary data={data} />
          ) : null}

          {data.related ? <RelatedSection related={data.related} family={data.family} /> : null}

          {data.degraded_sections.includes("related") ? (
            <Alert tone="warning">
              {data.family === "node"
                ? "本次未能读取分配到该节点的 Pod，关联对象与资源请求汇总不可用。"
                : data.family === "networking"
                  ? "本次未能读取该 Service selector 匹配的后端 Pod，端点统计与 Service 自身事件仍然有效。"
                  : "本次未能读取该工作负载拥有的对象，因此下方只有它自身的状态与事件。"}
            </Alert>
          ) : null}
          {data.degraded_sections.includes("node.resources") &&
          !data.degraded_sections.includes("related") ? (
            <Alert tone="warning">本次无法计算节点资源请求汇总，节点条件与事件仍然有效。</Alert>
          ) : null}
          {data.degraded_sections.includes("related.persistent_volume_claims") ? (
            <Alert tone="warning">
              部分工作负载引用的 PVC 未能读取，关联存储与诊断结论可能不完整。
            </Alert>
          ) : null}
          {data.degraded_sections.includes("service.endpoints") ? (
            <Alert tone="warning">
              本次未能读取该 Service 的 EndpointSlice，端点统计与相关诊断结论不可用。
            </Alert>
          ) : null}
          {data.events.omitted ? (
            <Alert tone={data.events.omitted === "unavailable" ? "warning" : "info"}>
              {OMITTED_EVENTS[data.events.omitted] ?? `事件未返回：${data.events.omitted}`}
            </Alert>
          ) : null}
          {data.degraded_sections.includes("events.related") ? (
            <Alert tone="warning">部分关联对象的事件未能读取，时间线并不完整。</Alert>
          ) : null}
          {data.events.truncated ? (
            <Alert tone="info">
              事件多于本次窗口，只展示最近的一部分；更早的事件可在事件页按对象查看。
            </Alert>
          ) : null}

          {/* Keep enough viewport for several rows. On a short window the whole
              diagnosis scrolls, instead of shrinking this table to a header and
              a sliver of one row. */}
          <Card className="flex min-h-72 flex-1 flex-col">
            <CardTitle>{aggregated ? "事件时间线" : "该对象的事件"}</CardTitle>
            <div className="mt-2 flex min-h-0 flex-1 flex-col">
              <DataTable
                columns={columns}
                data={data.events.items}
                rowKey={(event) => event.uid}
                emptyTitle="没有相关事件"
                emptyDescription="集群中没有属于这些对象的 Kubernetes Event，或事件已按保留期被回收。"
              />
            </div>
          </Card>
        </div>
      )}
    </div>
  );
}

function relatedHasFindings(data: KubernetesDescribe): boolean {
  const related = data.related;
  if (!related) {
    return false;
  }
  return [...related.controllers, ...related.persistent_volume_claims, ...related.pods].some(
    (object) => object.findings.length > 0,
  );
}

/**
 * What the workload owns.
 *
 * A workload that will not come up is almost always a statement about these
 * objects rather than about itself, so they are given the same weight as the
 * workload's own findings: unhealthy first, each carrying the conclusion drawn
 * for it. The healthy ones stay as one line — they are context, and a page that
 * spent ten rows on the replicas that are fine would bury the one that is not.
 */
function RelatedSection({
  related,
  family,
}: {
  related: KubernetesDescribeRelated;
  family: KubernetesDescribe["family"];
}) {
  const unhealthy = related.pods.filter((pod) => !pod.ready || pod.findings.length > 0);
  const healthy = related.pods.filter((pod) => pod.ready && pod.findings.length === 0);
  if (
    related.controllers.length === 0 &&
    related.persistent_volume_claims.length === 0 &&
    related.pods.length === 0
  ) {
    return null;
  }
  return (
    <Card>
      <CardTitle>
        {family === "node" ? "已分配 Pod" : family === "networking" ? "后端 Pod" : "关联对象"}
      </CardTitle>
      <div className="mt-2 grid gap-2">
        {related.controllers.map((controller) => (
          <RelatedRow key={controller.uid} object={controller} showNamespace={family === "node"} />
        ))}
        {related.persistent_volume_claims.map((claim) => (
          <RelatedRow key={claim.uid} object={claim} showNamespace={family === "node"} />
        ))}
        {unhealthy.map((pod) => (
          <RelatedRow key={pod.uid} object={pod} showNamespace={family === "node"} />
        ))}
        {healthy.length > 0 ? (
          <div className="text-subtle-foreground flex flex-wrap items-center gap-1.5 text-xs">
            <span>就绪 {healthy.length} 个：</span>
            {healthy.map((pod) => (
              <span key={pod.uid} className="zke-mono break-all">
                {family === "node" && pod.namespace ? `${pod.namespace}/${pod.name}` : pod.name}
              </span>
            ))}
          </div>
        ) : null}
        {related.truncated ? (
          <p className="text-subtle-foreground text-xs">
            {family === "node"
              ? "该节点上的非终止 Pod 多于此处展示的；这里只展示有界窗口，其余对象已省略。"
              : family === "networking"
                ? "该 Service selector 匹配的 Pod 多于此处展示的；这里只展示有界窗口，其余对象已省略。"
                : "该工作负载拥有或引用的对象多于此处展示的；这里只展示有界窗口，其余对象已省略。"}
          </p>
        ) : null}
      </div>
    </Card>
  );
}

function NodeDiagnosticSummary({ data }: { data: KubernetesDescribe }) {
  if (!data.node) {
    return null;
  }
  const resources = data.node_resources;
  return (
    <Card>
      <CardTitle>节点概况</CardTitle>
      <div className="mt-2 grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        <DiagnosticValue
          label="CPU requests / 可分配"
          value={
            resources
              ? `${formatMilliCPU(resources.cpu_requested_millis)} / ${formatMilliCPU(resources.cpu_allocatable_millis)}`
              : "—"
          }
        />
        <DiagnosticValue
          label="内存 requests / 可分配"
          value={
            resources
              ? `${formatBytes(resources.memory_requested_bytes)} / ${formatBytes(resources.memory_allocatable_bytes)}`
              : "—"
          }
        />
        <DiagnosticValue
          label="非终止 Pod / 可分配"
          value={resources ? `${resources.non_terminal_pods} / ${resources.pod_allocatable}` : "—"}
        />
        <DiagnosticValue
          label="污点"
          value={
            data.node.taints.length === 0
              ? "无"
              : data.node.taints
                  .map(
                    (taint) =>
                      `${taint.key}${taint.value ? `=${taint.value}` : ""}:${taint.effect}`,
                  )
                  .join(", ")
          }
        />
      </div>
      {resources?.truncated ? (
        <p className="text-subtle-foreground mt-2 text-xs">
          Pod 列表超过单次读取上限，requests 仅为已读取部分的下限，因此不生成资源占比结论。
        </p>
      ) : null}
    </Card>
  );
}

function PersistentVolumeClaimDiagnosticSummary({ data }: { data: KubernetesDescribe }) {
  const claim = data.storage?.persistent_volume_claim;
  if (!claim) {
    return null;
  }
  const conditions = data.storage?.persistent_volume_claim_detail?.conditions ?? [];
  return (
    <Card>
      <CardTitle>存储声明概况</CardTitle>
      <div className="mt-2 grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        <DiagnosticValue label="状态" value={claim.phase || "—"} />
        <DiagnosticValue
          label="容量 / 申请"
          value={`${claim.capacity || "尚未分配"} / ${claim.requested_capacity || "—"}`}
        />
        <DiagnosticValue label="StorageClass" value={claim.storage_class_name ?? "默认"} />
        <DiagnosticValue label="绑定卷" value={claim.volume_name || "未绑定"} />
      </div>
      {conditions.length > 0 ? (
        <div className="mt-2 grid gap-1.5">
          {conditions.map((condition) => (
            <div
              key={condition.type}
              className="border-border/60 rounded-control border px-2.5 py-2 text-xs"
            >
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-foreground font-medium">{condition.type}</span>
                <span className="zke-mono text-subtle-foreground">{condition.status}</span>
                {condition.reason ? (
                  <span className="zke-mono text-subtle-foreground">{condition.reason}</span>
                ) : null}
              </div>
              {condition.message ? (
                <p className="text-muted-foreground mt-1 break-words">{condition.message}</p>
              ) : null}
            </div>
          ))}
        </div>
      ) : null}
    </Card>
  );
}

function ServiceDiagnosticSummary({ data }: { data: KubernetesDescribe }) {
  const service = data.networking?.service;
  const endpoints = data.service_endpoints;
  if (!service || !endpoints) {
    return null;
  }
  if (service.spec.type === "ExternalName") {
    return (
      <Card>
        <CardTitle>Service 端点概况</CardTitle>
        <div className="mt-2 grid gap-2 sm:grid-cols-2">
          <DiagnosticValue label="类型" value="ExternalName" />
          <DiagnosticValue label="外部名称" value={service.spec.external_name || "—"} />
        </div>
        <p className="text-subtle-foreground mt-2 text-xs">
          ExternalName 通过 DNS 别名转发，不要求 EndpointSlice 或集群内后端 Pod。
        </p>
      </Card>
    );
  }
  return (
    <Card>
      <CardTitle>Service 端点概况</CardTitle>
      <div className="mt-2 grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        <DiagnosticValue label="类型" value={service.spec.type || "ClusterIP"} />
        <DiagnosticValue
          label="就绪 / 全部端点"
          value={`${endpoints.ready_endpoints} / ${endpoints.endpoints}`}
        />
        <DiagnosticValue label="EndpointSlice" value={String(endpoints.endpoint_slices)} />
        <DiagnosticValue
          label="Serving / 终止中"
          value={`${endpoints.serving_endpoints} / ${endpoints.terminating_endpoints}`}
        />
      </div>
      {endpoints.truncated ? (
        <p className="text-subtle-foreground mt-2 text-xs">
          EndpointSlice 超过单次读取上限，端点计数仅为已读取部分的下限，因此不生成缺失端点结论。
        </p>
      ) : null}
    </Card>
  );
}

function DiagnosticValue({ label, value }: { label: string; value: string }) {
  return (
    <div className="border-border/60 rounded-control min-w-0 border p-2.5">
      <p className="text-subtle-foreground text-xs">{label}</p>
      <p className="zke-mono text-foreground mt-1 text-[13px] break-all">{value}</p>
    </div>
  );
}

function formatMilliCPU(value: number): string {
  return value % 1000 === 0 ? `${value / 1000}` : `${value}m`;
}

function formatBytes(value: number): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let amount = value;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  return `${amount >= 10 || unit === 0 ? amount.toFixed(0) : amount.toFixed(1)} ${units[unit]}`;
}

function RelatedRow({
  object,
  showNamespace,
}: {
  object: KubernetesDescribeRelatedObject;
  showNamespace: boolean;
}) {
  return (
    <div className="border-border/60 rounded-control grid gap-1 border p-2.5">
      <div className="flex flex-wrap items-center gap-2 text-[13px]">
        <span className="text-foreground font-medium break-all">
          {showNamespace && object.namespace ? `${object.namespace}/${object.name}` : object.name}
        </span>
        <span className="text-subtle-foreground text-xs">{object.kind}</span>
        <Badge tone={object.ready ? "success" : "warning"}>{object.status || "—"}</Badge>
      </div>
      {object.findings.map((finding, index) => (
        <div key={`${finding.code}-${finding.scope ?? ""}-${index}`} className="grid gap-0.5">
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge tone="warning">{FINDING_LABELS[finding.code]?.title ?? finding.code}</Badge>
            {finding.scope ? (
              <span className="text-muted-foreground text-xs break-all">容器 {finding.scope}</span>
            ) : null}
            {finding.exit_code !== undefined ? (
              <span className="zke-tnum text-subtle-foreground text-xs">
                退出码 {finding.exit_code}
              </span>
            ) : null}
          </div>
          {finding.message ? (
            <span className="text-muted-foreground text-xs break-words">{finding.message}</span>
          ) : null}
        </div>
      ))}
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
    case "ObjectStatus":
      return "对象状态";
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
  if (data.node) {
    lines.push("", "节点概况");
    if (data.node_resources) {
      const resources = data.node_resources;
      lines.push(
        `  CPU requests: ${formatMilliCPU(resources.cpu_requested_millis)} / ${formatMilliCPU(resources.cpu_allocatable_millis)}`,
        `  Memory requests: ${formatBytes(resources.memory_requested_bytes)} / ${formatBytes(resources.memory_allocatable_bytes)}`,
        `  Non-terminal Pods: ${resources.non_terminal_pods} / ${resources.pod_allocatable}${resources.truncated ? "（下限，列表已截断）" : ""}`,
      );
    }
    lines.push(
      `  Taints: ${
        data.node.taints.length === 0
          ? "无"
          : data.node.taints
              .map((taint) => `${taint.key}${taint.value ? `=${taint.value}` : ""}:${taint.effect}`)
              .join(", ")
      }`,
    );
  }
  if (data.storage?.persistent_volume_claim) {
    const claim = data.storage.persistent_volume_claim;
    lines.push(
      "",
      "存储声明概况",
      `  Phase: ${claim.phase || "—"}`,
      `  Capacity: ${claim.capacity || "尚未分配"} / requested ${claim.requested_capacity || "—"}`,
      `  StorageClass: ${claim.storage_class_name ?? "默认"}`,
      `  Volume: ${claim.volume_name || "未绑定"}`,
    );
  }
  if (data.networking?.service && data.service_endpoints) {
    const service = data.networking.service;
    const endpoints = data.service_endpoints;
    if (service.spec.type === "ExternalName") {
      lines.push(
        "",
        "Service 端点概况",
        "  Type: ExternalName",
        `  ExternalName: ${service.spec.external_name || "—"}`,
      );
    } else {
      lines.push(
        "",
        "Service 端点概况",
        `  Type: ${service.spec.type || "ClusterIP"}`,
        `  Ready endpoints: ${endpoints.ready_endpoints} / ${endpoints.endpoints}${endpoints.truncated ? "（下限，列表已截断）" : ""}`,
        `  EndpointSlices: ${endpoints.endpoint_slices}`,
        `  Serving / terminating: ${endpoints.serving_endpoints} / ${endpoints.terminating_endpoints}`,
      );
    }
  }
  if (data.related) {
    const objects = [
      ...data.related.controllers,
      ...data.related.persistent_volume_claims,
      ...data.related.pods,
    ];
    lines.push(
      "",
      `${data.family === "networking" ? "后端 Pod" : "关联对象"}（${objects.length}${data.related.truncated ? "，已截断" : ""}）`,
    );
    for (const object of objects) {
      const objectName =
        data.family === "node" && object.namespace
          ? `${object.namespace}/${object.name}`
          : object.name;
      lines.push(`  - ${object.kind}/${objectName} ${object.status}`);
      for (const finding of object.findings) {
        const label = FINDING_LABELS[finding.code]?.title ?? finding.code;
        lines.push(
          `      ${label} [${finding.code}]${finding.scope ? ` 容器=${finding.scope}` : ""}` +
            `${finding.message ? ` ${finding.message}` : ""}`,
        );
      }
    }
  }
  lines.push("", `事件（${data.events.items.length}${data.events.truncated ? "，已截断" : ""}）`);
  if (data.events.omitted) {
    lines.push(`  未读取：${data.events.omitted}`);
  }
  for (const event of data.events.items) {
    const seen = event.last_seen ?? event.first_seen;
    // The subject is written out only for the aggregated timelines, where the
    // lines are about several objects and a line without it says nothing.
    const subject =
      data.family === "workload" ? ` ${event.regarding.kind}/${event.regarding.name}` : "";
    lines.push(
      `  ${seen ? formatAbsolute(seen) : "—"} ${event.type} ${event.reason}` +
        `${subject}${event.container ? ` (${event.container})` : ""}` +
        ` x${event.count || 1} ${event.message}`,
    );
  }
  return lines.join("\n");
}
