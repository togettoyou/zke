import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Bell, ExternalLink, ScrollText } from "lucide-react";

import type {
  KubernetesDescribe,
  KubernetesDescribeEvent,
  KubernetesDescribeFinding,
  KubernetesDescribeFindingCode,
  KubernetesDescribeRelated,
  KubernetesDescribeRelatedObject,
} from "@/api/types";
import { PageHeader, SectionToolbarActions } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
import { DetailConditions } from "@/components/common/detail";
import { RefreshAction } from "@/components/common/refresh-action";
import { ErrorState, LoadingState } from "@/components/common/state";
import { CopyButton, RelativeTime, StatusBadge } from "@/components/common/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardTitle, Alert } from "@/components/ui/misc";
import { cn } from "@/lib/cn";
import { formatAbsolute } from "@/lib/time";
import { useScopeStore } from "@/scope/scope-store";

import { useDiagnosticNavigation } from "./diagnostic-navigation-context";

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
  IngressAddressPending: {
    title: "Ingress 地址等待发布",
    hint: "Ingress Controller 尚未在状态中发布入口地址。检查 IngressClass 对应的 Controller 是否运行，以及它是否接受了该对象。",
  },
  IngressControllerRejected: {
    title: "Ingress Controller 拒绝配置",
    hint: "Controller 已通过事件报告同步或配置失败。按上方 Kubernetes 原始消息核对 IngressClass、注解和路由规则。",
  },
  IngressBackendServiceNotFound: {
    title: "后端 Service 不存在",
    hint: "Ingress 引用的 Service 未出现在同一命名空间的完整清单中。核对后端名称与部署顺序。",
  },
  IngressBackendPortNotFound: {
    title: "后端端口不存在",
    hint: "Service 存在，但没有 Ingress 引用的端口名称或端口号。核对 Ingress backend 与 Service ports。",
  },
  IngressBackendNoEndpoints: {
    title: "后端没有端点",
    hint: "Service 端口存在，但相关 EndpointSlice 中没有端点。核对 Service selector 与后端 Pod。",
  },
  IngressBackendNoReadyEndpoints: {
    title: "后端没有就绪端点",
    hint: "相关 EndpointSlice 已包含端点，但没有 Ready 后端可接收流量。检查 Pod 状态与 readiness 探针。",
  },
  GatewayAddressPending: {
    title: "Gateway 地址等待分配",
    hint: "Gateway Controller 尚未在状态中发布地址。核对 GatewayClass Controller、地址池和对象 Condition。",
  },
  GatewayNotAccepted: {
    title: "Gateway 未被接受",
    hint: "GatewayClass 对应的 Controller 尚未接受该 Gateway。按 Kubernetes 原始 reason 与 message 核对类和配置。",
  },
  GatewayNotProgrammed: {
    title: "Gateway 尚未编程",
    hint: "Controller 尚未把期望配置下发到数据面。检查 Controller 状态与 Gateway Condition。",
  },
  GatewayNotReady: {
    title: "Gateway 未就绪",
    hint: "Gateway Controller 明确报告 Ready 不为 True。结合 Condition 原始消息定位数据面或基础设施问题。",
  },
  GatewayListenerNotAccepted: {
    title: "监听器未被接受",
    hint: "Controller 未接受该 Listener。检查协议、端口、主机名以及 GatewayClass 支持范围。",
  },
  GatewayListenerNotProgrammed: {
    title: "监听器尚未编程",
    hint: "Listener 配置尚未下发到数据面。结合 Programmed Condition 的原始消息检查 Controller。",
  },
  GatewayListenerConflicted: {
    title: "监听器配置冲突",
    hint: "Listener 与同一 Gateway 上的其他监听器发生冲突。检查端口、协议和主机名组合。",
  },
  GatewayListenerReferencesInvalid: {
    title: "监听器引用无效",
    hint: "Listener 的证书或其他对象引用没有解析成功。检查对象名称、命名空间、类型和引用授权。",
  },
  GatewayRouteUnattached: {
    title: "Route 尚未绑定",
    hint: "Controller 尚未为任何 ParentRef 写入状态。核对 Gateway 名称、命名空间、Listener 的 allowedRoutes，以及 Controller 是否支持该 Route 类型。",
  },
  GatewayRouteNotAccepted: {
    title: "Route 未被父级接受",
    hint: "按父级 Accepted Condition 核对 Listener 协议、hostname、sectionName、allowedRoutes 与 Controller 支持范围。",
  },
  GatewayRouteReferencesInvalid: {
    title: "Route 引用未解析",
    hint: "核对 BackendRef 名称、类型、端口和命名空间；跨命名空间 BackendRef 必须由目标命名空间的 ReferenceGrant 明确授权。",
  },
  GatewayRoutePartiallyInvalid: {
    title: "Route 部分规则无效",
    hint: "Controller 只接受了部分规则或回退到上一份有效配置。按原始消息检查具体 match、filter 与 backendRef。",
  },
  HPAStatusStale: {
    title: "HPA 状态尚未追上配置",
    hint: "控制器观察到的 Generation 落后于当前配置。等待下一次同步；若持续不变，检查 HPA Controller 状态。",
  },
  HPAUnableToScale: {
    title: "无法读取或更新伸缩目标",
    hint: "HPA Controller 无法读取目标的 scale 子资源或更新副本数。按 Kubernetes 原始消息核对目标名称、类型和权限。",
  },
  HPAMetricsUnavailable: {
    title: "伸缩指标不可用",
    hint: "HPA 当前无法计算期望副本数。检查 Metrics API、指标适配器、指标名称和目标工作负载的资源 requests。",
  },
  HPAScalingLimited: {
    title: "伸缩受到上下限约束",
    hint: "计算出的期望副本数超出 minReplicas 或 maxReplicas。核对副本上下限是否符合当前负载需求。",
  },
  VPAStatusStale: {
    title: "VPA 状态尚未追上配置",
    hint: "控制器观察到的 Generation 落后于当前配置。等待下一次同步；若持续不变，检查 VPA Controller 状态与事件。",
  },
  VPARecommendationUnavailable: {
    title: "尚无资源建议",
    hint: "VPA Controller 尚未生成容器资源建议。检查目标是否有可匹配的 Pod、历史指标是否充足，以及 Metrics API 是否可用。",
  },
  VPAConfigurationUnsupported: {
    title: "VPA 配置不受支持",
    hint: "VPA Controller 明确拒绝了当前策略。按 Kubernetes 原始消息核对更新模式、受控资源和容器策略。",
  },
  VPANoPodsMatched: {
    title: "VPA 未匹配到 Pod",
    hint: "伸缩目标当前没有可供 VPA 采样的 Pod。核对目标名称、工作负载副本和 Pod selector。",
  },
  VPALowConfidence: {
    title: "VPA 建议置信度较低",
    hint: "可用历史样本不足，当前建议可能不稳定。继续观察指标采集，并谨慎使用会自动驱逐或重建 Pod 的更新模式。",
  },
  KEDANotReady: {
    title: "KEDA ScaledObject 未就绪",
    hint: "KEDA Controller 尚未接受或完成当前伸缩配置。按原始 reason 和 message 检查触发器、认证引用与指标端点。",
  },
  KEDAFallbackActive: {
    title: "KEDA 已进入回退状态",
    hint: "触发器连续读取失败，KEDA 正在使用回退副本策略。检查外部指标源、网络连通性与 TriggerAuthentication。",
  },
  ResourceQuotaExhausted: {
    title: "命名空间额度已耗尽",
    hint: "一项或多项 used 已达到 hard，新对象可能因此被准入拒绝。核对下方具体额度；需要时释放资源或调整 ResourceQuota。",
  },
  PDBNoDisruptionsAllowed: {
    title: "当前不允许自愿中断",
    hint: "PodDisruptionBudget 当前不会批准 eviction。它可能是在按预期保护副本；结合健康/期望数与 Kubernetes 原始消息判断是等待副本恢复还是需要调整预算。",
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
  const navigation = useDiagnosticNavigation();
  const { permissions } = useSessionContext();
  const scope = useScopeStore((state) => state.scope);
  const projectScope = {
    type: "project" as const,
    tenantId: scope.tenantId,
    projectId: scope.projectId,
  };
  const canReadEvents = permissions.can("cluster.event.read", projectScope);
  const canReadLogs = permissions.can("cluster.pod.logs.read", projectScope);
  const [highlightedEvidence, setHighlightedEvidence] = useState<string | null>(null);

  const focusEvidence = (kind: string, evidenceName: string) => {
    const anchor = evidenceAnchor(kind, evidenceName);
    const element = document.getElementById(anchor);
    if (!element) {
      return;
    }
    setHighlightedEvidence(anchor);
    element.scrollIntoView({ behavior: "smooth", block: "center" });
    window.setTimeout(
      () => setHighlightedEvidence((current) => (current === anchor ? null : current)),
      2_000,
    );
  };

  const openPodLogs = (
    pod: { name: string; uid: string; namespace: string },
    finding: KubernetesDescribeFinding,
  ) => {
    if (!navigation || !canReadLogs || !finding.scope || !pod.uid) {
      return;
    }
    navigation.open({
      view: "pod-logs",
      namespace: pod.namespace,
      pod: { name: pod.name, uid: pod.uid },
      container: finding.scope,
      previous: previousLogsFinding(finding.code),
    });
  };

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
          <div
            id={evidenceAnchor("Event", row.original.uid)}
            className={cn(
              "-m-1 flex flex-col gap-0.5 rounded px-1 py-1 transition-colors",
              highlightedEvidence === evidenceAnchor("Event", row.original.uid) &&
                "bg-warning/15 ring-warning/40 ring-1",
            )}
          >
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
    [aggregated, highlightedEvidence],
  );

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      <PageHeader
        title={`${name} · 诊断`}
        onBack={onBack}
        actions={
          data ? (
            <div className="flex items-center gap-1.5">
              {navigation && canReadEvents && data.target.namespace && data.target.uid ? (
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() =>
                    navigation.open({
                      view: "events",
                      namespace: data.target.namespace,
                      object: {
                        kind: data.target.kind,
                        name: data.target.name,
                        uid: data.target.uid,
                      },
                    })
                  }
                >
                  <Bell />
                  精确事件
                </Button>
              ) : null}
              <CopyButton value={() => describeText(data, kindLabel)} label="复制为文本" />
            </div>
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
                  canOpenLogs={Boolean(
                    canReadLogs &&
                    data.pod?.uid &&
                    data.target.namespace &&
                    finding.scope &&
                    logFinding(finding.code),
                  )}
                  onOpenLogs={() =>
                    openPodLogs(
                      {
                        name: data.target.name,
                        uid: data.pod?.uid ?? "",
                        namespace: data.target.namespace,
                      },
                      finding,
                    )
                  }
                  onFocusEvidence={focusEvidence}
                  canFocusEvidence={(kind, evidenceName) =>
                    kind === "Event"
                      ? data.events.items.some((event) => event.uid === evidenceName)
                      : kind === "Condition" &&
                        describeConditions(data).some(
                          (condition) => condition.type === evidenceName,
                        )
                  }
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
          {data.networking?.ingress && data.ingress_backends ? (
            <IngressDiagnosticSummary data={data} />
          ) : null}
          {data.networking?.gateway && data.gateway_status ? (
            <GatewayDiagnosticSummary data={data} />
          ) : null}
          {data.networking?.gateway_route ? <GatewayRouteDiagnosticSummary data={data} /> : null}
          {data.autoscaler || data.vertical_pod_autoscaler || data.keda_scaled_object ? (
            <AutoscalerDiagnosticSummary data={data} />
          ) : null}
          {data.policy && data.policy_status ? <PolicyDiagnosticSummary data={data} /> : null}

          {data.related ? (
            <RelatedSection
              related={data.related}
              family={data.family}
              defaultNamespace={data.target.namespace}
              canReadLogs={canReadLogs}
              onOpenPodLogs={openPodLogs}
            />
          ) : null}

          <ConditionEvidenceSection data={data} highlightedEvidence={highlightedEvidence} />

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
          {data.degraded_sections.includes("ingress.backends") ? (
            <Alert tone="warning">
              本次未能读取该 Ingress 引用的 Service，后端存在性、端口与端点诊断不可用。
            </Alert>
          ) : null}
          {data.degraded_sections.includes("ingress.endpoints") ? (
            <Alert tone="warning">
              本次未能读取后端 Service 的 EndpointSlice，Service
              与端口状态仍然有效，但端点诊断不可用。
            </Alert>
          ) : null}
          {data.degraded_sections.includes("autoscaler.target") ? (
            <Alert tone="warning">
              本次未能读取自动伸缩对象的类型化目标，其自身 Condition 与事件仍然有效。
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
  if (data.ingress_backends?.items.some((backend) => backend.findings.length > 0)) {
    return true;
  }
  if (data.gateway_status?.listeners.some((listener) => listener.findings.length > 0)) {
    return true;
  }
  if (data.autoscaler_target?.findings.length) {
    return true;
  }
  const related = data.related;
  if (!related) {
    return false;
  }
  return [...related.controllers, ...related.persistent_volume_claims, ...related.pods].some(
    (object) => object.findings.length > 0,
  );
}

type EvidenceCondition = {
  type: string;
  status: string;
  reason?: string;
  message?: string;
};

/** Conditions named by a finding, rendered as inspectable evidence anchors. */
function ConditionEvidenceSection({
  data,
  highlightedEvidence,
}: {
  data: KubernetesDescribe;
  highlightedEvidence: string | null;
}) {
  const referenced = new Set(
    allDescribeFindings(data).flatMap((finding) =>
      finding.evidence
        .filter((evidence) => evidence.kind === "Condition")
        .map((evidence) => evidence.name),
    ),
  );
  const seen = new Set<string>();
  const conditions = describeConditions(data).filter((condition) => {
    if (!referenced.has(condition.type) || seen.has(condition.type)) {
      return false;
    }
    seen.add(condition.type);
    return true;
  });
  if (conditions.length === 0) {
    return null;
  }
  return (
    <Card>
      <CardTitle>诊断依据</CardTitle>
      <div className="mt-2 grid gap-2">
        {conditions.map((condition) => {
          const anchor = evidenceAnchor("Condition", condition.type);
          return (
            <div
              key={condition.type}
              id={anchor}
              className={cn(
                "border-border/60 rounded-control border p-2.5 transition-colors",
                highlightedEvidence === anchor && "bg-warning/10 ring-warning/40 ring-1",
              )}
            >
              <div className="flex flex-wrap items-center gap-2 text-xs">
                <span className="text-foreground font-medium">{condition.type}</span>
                <span className="zke-mono text-subtle-foreground">{condition.status}</span>
                {condition.reason ? (
                  <span className="zke-mono text-subtle-foreground">{condition.reason}</span>
                ) : null}
              </div>
              {condition.message ? (
                <p className="text-muted-foreground mt-1 text-xs break-words">
                  {condition.message}
                </p>
              ) : null}
            </div>
          );
        })}
      </div>
    </Card>
  );
}

function allDescribeFindings(data: KubernetesDescribe): KubernetesDescribeFinding[] {
  const related = data.related
    ? [
        ...data.related.controllers,
        ...data.related.persistent_volume_claims,
        ...data.related.pods,
      ].flatMap((object) => object.findings)
    : [];
  return [
    ...data.findings,
    ...related,
    ...(data.ingress_backends?.items.flatMap((backend) => backend.findings) ?? []),
    ...(data.gateway_status?.listeners.flatMap((listener) => listener.findings) ?? []),
    ...(data.autoscaler_target?.findings ?? []),
  ];
}

function describeConditions(data: KubernetesDescribe): EvidenceCondition[] {
  return [
    ...(data.pod?.conditions ?? []),
    ...(data.workload?.conditions ?? []),
    ...(data.node?.conditions ?? []),
    ...(data.storage?.persistent_volume_claim_detail?.conditions ?? []),
    ...(data.networking?.gateway?.conditions ?? []),
    ...(data.networking?.gateway?.listeners.flatMap((listener) =>
      listener.conditions.map((condition) => ({
        ...condition,
        type: `listener/${listener.name}/${condition.type}`,
      })),
    ) ?? []),
    ...(data.networking?.gateway_route?.parents.flatMap((parent) =>
      parent.conditions.map((condition) => ({
        ...condition,
        type: `parent/${parent.parent.name || "unknown"}/${condition.type}`,
      })),
    ) ?? []),
    ...(data.autoscaler?.conditions ?? []),
    ...(data.vertical_pod_autoscaler?.conditions ?? []),
    ...(data.keda_scaled_object?.conditions ?? []),
    ...(data.policy?.disruption_budget_detail?.conditions ?? []),
  ];
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
  defaultNamespace,
  canReadLogs,
  onOpenPodLogs,
}: {
  related: KubernetesDescribeRelated;
  family: KubernetesDescribe["family"];
  defaultNamespace: string;
  canReadLogs: boolean;
  onOpenPodLogs: (
    pod: { name: string; uid: string; namespace: string },
    finding: KubernetesDescribeFinding,
  ) => void;
}) {
  const navigation = useDiagnosticNavigation();
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
          <RelatedRow
            key={claim.uid}
            object={claim}
            showNamespace={family === "node"}
            onOpenDescribe={
              navigation
                ? () =>
                    navigation.open({
                      view: "describe",
                      type: "persistent-volume-claim",
                      namespace: claim.namespace || defaultNamespace,
                      name: claim.name,
                    })
                : undefined
            }
          />
        ))}
        {unhealthy.map((pod) => (
          <RelatedRow
            key={pod.uid}
            object={pod}
            showNamespace={family === "node"}
            onOpenDescribe={
              navigation
                ? () =>
                    navigation.open({
                      view: "describe",
                      type: "pod",
                      namespace: pod.namespace || defaultNamespace,
                      name: pod.name,
                    })
                : undefined
            }
            onOpenFindingLogs={
              canReadLogs && pod.uid
                ? (finding) =>
                    onOpenPodLogs(
                      {
                        name: pod.name,
                        uid: pod.uid,
                        namespace: pod.namespace || defaultNamespace,
                      },
                      finding,
                    )
                : undefined
            }
          />
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
      <div className="mt-2 grid gap-2 @md:grid-cols-2 @3xl:grid-cols-4">
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
      <div className="mt-2 grid gap-2 @md:grid-cols-2 @3xl:grid-cols-4">
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
        <div className="mt-2 grid gap-2 @md:grid-cols-2">
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
      <div className="mt-2 grid gap-2 @md:grid-cols-2 @3xl:grid-cols-4">
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

function IngressDiagnosticSummary({ data }: { data: KubernetesDescribe }) {
  const backends = data.ingress_backends;
  const navigation = useDiagnosticNavigation();
  if (!backends) {
    return null;
  }
  return (
    <Card>
      <CardTitle>Ingress 后端</CardTitle>
      {backends.items.length === 0 ? (
        <p className="text-subtle-foreground mt-2 text-xs">
          没有可诊断的 Service 后端；自定义 Resource backend 不会被解释为 Service。
        </p>
      ) : (
        <div className="mt-2 grid gap-2">
          {backends.items.map((backend) => {
            const port = backend.port_name || String(backend.port_number || "—");
            const inventoryUnknown = backend.service_found === undefined;
            return (
              <div
                key={`${backend.service_name}/${backend.port_name}/${backend.port_number}`}
                className="border-border/60 rounded-control grid gap-1.5 border p-2.5"
              >
                <div className="flex flex-wrap items-center gap-2 text-[13px]">
                  <span className="text-foreground font-medium break-all">
                    {backend.service_name}:{port}
                  </span>
                  {inventoryUnknown ? (
                    <Badge tone="neutral">状态未知</Badge>
                  ) : backend.service_found === false ? (
                    <Badge tone="warning">Service 不存在</Badge>
                  ) : backend.port_found === false ? (
                    <Badge tone="warning">端口不存在</Badge>
                  ) : backend.endpoint_state_available ? (
                    <Badge tone={backend.ready_endpoints > 0 ? "success" : "warning"}>
                      Ready {backend.ready_endpoints}/{backend.endpoints}
                    </Badge>
                  ) : (
                    <Badge tone="neutral">端点不可用</Badge>
                  )}
                  <span className="text-subtle-foreground text-xs break-all">
                    {backend.references.join(" · ")}
                  </span>
                  {navigation && backend.service_found ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() =>
                        navigation.open({
                          view: "describe",
                          type: "service",
                          namespace: data.target.namespace,
                          name: backend.service_name,
                        })
                      }
                    >
                      <ExternalLink />
                      诊断 Service
                    </Button>
                  ) : null}
                </div>
                {backend.findings.map((finding, index) => (
                  <div
                    key={`${finding.code}-${index}`}
                    className="flex flex-wrap items-center gap-1.5"
                  >
                    <Badge tone="warning">
                      {FINDING_LABELS[finding.code]?.title ?? finding.code}
                    </Badge>
                    <span className="text-muted-foreground text-xs">
                      {FINDING_LABELS[finding.code]?.hint}
                    </span>
                  </div>
                ))}
              </div>
            );
          })}
        </div>
      )}
      {backends.truncated ? (
        <p className="text-subtle-foreground mt-2 text-xs">后端引用超过 20 个，只诊断前 20 个。</p>
      ) : null}
      {backends.services_truncated ? (
        <p className="text-subtle-foreground mt-2 text-xs">
          Service 清单还有下一页，未出现的后端状态保持未知。
        </p>
      ) : null}
      {backends.endpoint_slices_truncated ? (
        <p className="text-subtle-foreground mt-2 text-xs">
          EndpointSlice 清单还有下一页，端点计数为下限且不生成缺失结论。
        </p>
      ) : null}
    </Card>
  );
}

function GatewayDiagnosticSummary({ data }: { data: KubernetesDescribe }) {
  const gateway = data.networking?.gateway;
  const status = data.gateway_status;
  if (!gateway || !status) {
    return null;
  }
  return (
    <Card>
      <CardTitle>Gateway 状态</CardTitle>
      <div className="mt-2 grid gap-2 @md:grid-cols-2">
        <DiagnosticValue
          label="地址"
          value={gateway.addresses.map((item) => item.value).join(", ") || "尚未分配"}
        />
        <DiagnosticValue label="监听器状态" value={`${status.listeners.length} 个`} />
      </div>
      {status.listeners.length === 0 ? (
        <p className="text-subtle-foreground mt-2 text-xs">
          Controller 尚未报告 Listener 状态，仍可结合上方 Gateway Condition 与事件判断。
        </p>
      ) : (
        <div className="mt-2 grid gap-2">
          {status.listeners.map((listener) => (
            <div
              key={listener.name}
              className="border-border/60 rounded-control grid gap-1.5 border p-2.5"
            >
              <div className="flex flex-wrap items-center gap-2 text-[13px]">
                <span className="text-foreground font-medium break-all">{listener.name}</span>
                <Badge tone={listener.findings.length === 0 ? "success" : "warning"}>
                  {listener.findings.length === 0
                    ? "状态正常"
                    : `${listener.findings.length} 个问题`}
                </Badge>
                <span className="text-subtle-foreground text-xs">
                  已附加 Route {listener.attached_routes} 个
                </span>
              </div>
              {listener.findings.map((finding, index) => (
                <div key={`${finding.code}-${index}`} className="grid gap-0.5">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <Badge tone="warning">
                      {FINDING_LABELS[finding.code]?.title ?? finding.code}
                    </Badge>
                    {finding.reason ? (
                      <span className="zke-mono text-subtle-foreground text-xs">
                        {finding.reason}
                      </span>
                    ) : null}
                  </div>
                  {finding.message ? (
                    <span className="text-muted-foreground text-xs break-words">
                      {finding.message}
                    </span>
                  ) : null}
                </div>
              ))}
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}

function GatewayRouteDiagnosticSummary({ data }: { data: KubernetesDescribe }) {
  const route = data.networking?.gateway_route;
  if (!route) return null;
  return (
    <Card>
      <CardTitle>Route 状态</CardTitle>
      <div className="mt-2 grid gap-2 @2xl:grid-cols-3">
        <DiagnosticValue label="ParentRef" value={`${route.parent_refs.length} 个`} />
        <DiagnosticValue label="BackendRef" value={`${route.backend_refs.length} 个`} />
        <DiagnosticValue label="Controller 状态" value={`${route.parents.length} 个`} />
      </div>
      <div className="mt-2 grid gap-2">
        {route.parents.map((parent, index) => (
          <div
            key={`${parent.controller_name}/${parent.parent.name}/${index}`}
            className="border-border/60 rounded-control grid gap-1.5 border p-2.5"
          >
            <div className="flex flex-wrap items-center gap-2 text-[13px]">
              <span className="text-foreground font-medium">
                {parent.parent.namespace ? `${parent.parent.namespace}/` : ""}
                {parent.parent.name || "未知父级"}
              </span>
              <span className="zke-mono text-subtle-foreground text-xs break-all">
                {parent.controller_name || "未知 Controller"}
              </span>
            </div>
            <DetailConditions conditions={parent.conditions} />
          </div>
        ))}
      </div>
    </Card>
  );
}

function AutoscalerDiagnosticSummary({ data }: { data: KubernetesDescribe }) {
  const hpa = data.autoscaler;
  const vpa = data.vertical_pod_autoscaler;
  const keda = data.keda_scaled_object;
  const navigation = useDiagnosticNavigation();
  if (!hpa && !vpa && !keda) {
    return null;
  }
  const autoscalingTarget = hpa?.target ?? vpa?.target ?? keda?.target;
  const target = data.autoscaler_target;
  return (
    <Card>
      <CardTitle>自动伸缩状态</CardTitle>
      <div className="mt-2 grid gap-2 @md:grid-cols-2">
        <DiagnosticValue
          label="伸缩目标"
          value={
            autoscalingTarget ? `${autoscalingTarget.kind}/${autoscalingTarget.name}` : "状态不可用"
          }
        />
        {hpa ? (
          <>
            <DiagnosticValue
              label="当前 / 期望副本"
              value={`${hpa.current_replicas} → ${hpa.desired_replicas}`}
            />
            <DiagnosticValue label="副本区间" value={`${hpa.min_replicas} – ${hpa.max_replicas}`} />
            <DiagnosticValue
              label="Generation"
              value={`${hpa.generation}（已观察 ${hpa.observed_generation ?? "—"}）`}
            />
          </>
        ) : null}
        {vpa ? (
          <>
            <DiagnosticValue label="更新模式" value={vpa.update_mode || "Off"} />
            <DiagnosticValue label="容器建议" value={`${(vpa.recommendations ?? []).length} 个`} />
            <DiagnosticValue
              label="Generation"
              value={`${vpa.generation}（已观察 ${vpa.observed_generation || "—"}）`}
            />
          </>
        ) : null}
        {keda ? (
          <>
            <DiagnosticValue
              label="副本区间"
              value={`${keda.min_replicas} – ${keda.max_replicas}`}
            />
            <DiagnosticValue
              label="控制器状态"
              value={
                keda.paused
                  ? "已暂停"
                  : keda.fallback
                    ? "回退中"
                    : keda.ready
                      ? keda.active
                        ? "活跃"
                        : "等待触发"
                      : "未就绪"
              }
            />
            <DiagnosticValue label="生成的 HPA" value={keda.hpa_name || "尚未生成"} />
          </>
        ) : null}
      </div>
      {target ? (
        <div className="border-border/60 rounded-control mt-2 grid gap-1.5 border p-2.5">
          <div className="flex flex-wrap items-center gap-2 text-[13px]">
            <span className="text-foreground font-medium break-all">
              {target.kind}/{target.name}
            </span>
            <Badge tone={target.ready && target.findings.length === 0 ? "success" : "warning"}>
              {target.status || "状态未知"}
            </Badge>
            {navigation && workloadResourceForKind(target.kind) ? (
              <Button
                size="sm"
                variant="ghost"
                onClick={() =>
                  navigation.open({
                    view: "describe",
                    type: "workload",
                    namespace: target.namespace || data.target.namespace,
                    resource: workloadResourceForKind(target.kind)!,
                    name: target.name,
                  })
                }
              >
                <ExternalLink />
                诊断工作负载
              </Button>
            ) : null}
          </div>
          {target.findings.map((finding, index) => (
            <div key={`${finding.code}-${index}`} className="grid gap-0.5">
              <div className="flex flex-wrap items-center gap-1.5">
                <Badge tone="warning">{FINDING_LABELS[finding.code]?.title ?? finding.code}</Badge>
                {finding.reason ? (
                  <span className="zke-mono text-subtle-foreground text-xs">{finding.reason}</span>
                ) : null}
              </div>
              {finding.message ? (
                <span className="text-muted-foreground text-xs break-words">{finding.message}</span>
              ) : null}
            </div>
          ))}
        </div>
      ) : null}
    </Card>
  );
}

function PolicyDiagnosticSummary({ data }: { data: KubernetesDescribe }) {
  const policy = data.policy;
  const status = data.policy_status;
  if (!policy || !status) {
    return null;
  }
  if (policy.resource_quota) {
    return (
      <Card>
        <CardTitle>配额用量</CardTitle>
        {status.quota_usage.length === 0 ? (
          <p className="text-subtle-foreground mt-2 text-xs">该 ResourceQuota 没有 hard 额度项。</p>
        ) : (
          <div className="mt-2 grid gap-2">
            {status.quota_usage.map((usage) => (
              <div
                key={usage.resource}
                className="border-border/60 rounded-control flex flex-wrap items-center justify-between gap-2 border p-2.5"
              >
                <span className="zke-mono text-foreground text-[13px] break-all">
                  {usage.resource}
                </span>
                <div className="flex items-center gap-2">
                  <span className="zke-mono text-muted-foreground text-xs">
                    {usage.used} / {usage.hard}
                  </span>
                  <Badge tone={usage.exhausted ? "warning" : "success"}>
                    {usage.exhausted ? "已耗尽" : "有余量"}
                  </Badge>
                </div>
              </div>
            ))}
          </div>
        )}
      </Card>
    );
  }
  const budget = policy.disruption_budget;
  if (!budget) {
    return null;
  }
  return (
    <Card>
      <CardTitle>中断预算状态</CardTitle>
      <div className="mt-2 grid gap-2 @md:grid-cols-2">
        <DiagnosticValue
          label="当前健康 / 期望健康"
          value={`${budget.current_healthy} / ${budget.desired_healthy}`}
        />
        <DiagnosticValue label="当前可中断" value={String(budget.disruptions_allowed)} />
        <DiagnosticValue label="预期 Pod" value={String(budget.expected_pods)} />
        <DiagnosticValue
          label="预算"
          value={
            budget.min_available
              ? `minAvailable ${budget.min_available}`
              : budget.max_unavailable
                ? `maxUnavailable ${budget.max_unavailable}`
                : "—"
          }
        />
      </div>
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
  onOpenDescribe,
  onOpenFindingLogs,
}: {
  object: KubernetesDescribeRelatedObject;
  showNamespace: boolean;
  onOpenDescribe?: () => void;
  onOpenFindingLogs?: (finding: KubernetesDescribeFinding) => void;
}) {
  return (
    <div className="border-border/60 rounded-control grid gap-1 border p-2.5">
      <div className="flex flex-wrap items-center gap-2 text-[13px]">
        <span className="text-foreground font-medium break-all">
          {showNamespace && object.namespace ? `${object.namespace}/${object.name}` : object.name}
        </span>
        <span className="text-subtle-foreground text-xs">{object.kind}</span>
        <Badge tone={object.ready ? "success" : "warning"}>{object.status || "—"}</Badge>
        {onOpenDescribe ? (
          <Button size="sm" variant="ghost" onClick={onOpenDescribe}>
            <ExternalLink />
            诊断
          </Button>
        ) : null}
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
            {onOpenFindingLogs && finding.scope && logFinding(finding.code) ? (
              <Button size="sm" variant="ghost" onClick={() => onOpenFindingLogs(finding)}>
                <ScrollText />
                {previousLogsFinding(finding.code) ? "上一次日志" : "当前日志"}
              </Button>
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

function FindingCard({
  finding,
  canOpenLogs,
  onOpenLogs,
  onFocusEvidence,
  canFocusEvidence,
}: {
  finding: KubernetesDescribeFinding;
  canOpenLogs: boolean;
  onOpenLogs: () => void;
  onFocusEvidence: (kind: string, name: string) => void;
  canFocusEvidence: (kind: string, name: string) => boolean;
}) {
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
        {canOpenLogs ? (
          <Button size="sm" variant="ghost" onClick={onOpenLogs}>
            <ScrollText />
            {previousLogsFinding(finding.code) ? "查看上一次日志" : "查看当前日志"}
          </Button>
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
          {finding.evidence.map((item) =>
            canFocusEvidence(item.kind, item.name) ? (
              <button
                key={`${item.kind}-${item.name}`}
                type="button"
                // `zke-focus`, like every other control: this used to draw its
                // own ring off `ring-accent`, which is not a token this theme
                // defines — so the rule generated nothing and the only thing
                // left was `outline-none`, i.e. no keyboard focus at all.
                className="zke-focus border-border/60 hover:bg-surface-muted rounded-full border px-2 py-0.5 transition-colors"
                onClick={() => onFocusEvidence(item.kind, item.name)}
              >
                {evidenceLabel(item.kind)} · {item.name}
              </button>
            ) : (
              <span
                key={`${item.kind}-${item.name}`}
                className="border-border/60 rounded-full border px-2 py-0.5"
              >
                {evidenceLabel(item.kind)} · {item.name}
              </span>
            ),
          )}
        </div>
      ) : null}
    </Card>
  );
}

function logFinding(code: KubernetesDescribeFindingCode): boolean {
  return ["CrashLoopBackOff", "ContainerTerminated", "OOMKilled", "ProbeFailure"].includes(code);
}

function previousLogsFinding(code: KubernetesDescribeFindingCode): boolean {
  return ["CrashLoopBackOff", "ContainerTerminated", "OOMKilled"].includes(code);
}

function workloadResourceForKind(kind: string) {
  switch (kind) {
    case "Deployment":
      return "deployments" as const;
    case "StatefulSet":
      return "statefulsets" as const;
    case "DaemonSet":
      return "daemonsets" as const;
    case "Job":
      return "jobs" as const;
    case "CronJob":
      return "cronjobs" as const;
    default:
      return undefined;
  }
}

function evidenceAnchor(kind: string, name: string): string {
  return `describe-evidence-${kind}-${encodeURIComponent(name)}`;
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
  if (data.ingress_backends) {
    lines.push(
      "",
      `Ingress 后端（${data.ingress_backends.items.length}${data.ingress_backends.truncated ? "，已截断" : ""}）`,
    );
    for (const backend of data.ingress_backends.items) {
      const port = backend.port_name || String(backend.port_number || "—");
      const state =
        backend.service_found === undefined
          ? "状态未知"
          : backend.service_found === false
            ? "Service 不存在"
            : backend.port_found === false
              ? "端口不存在"
              : backend.endpoint_state_available
                ? `Ready ${backend.ready_endpoints}/${backend.endpoints}`
                : "端点不可用";
      lines.push(
        `  - Service/${backend.service_name}:${port} ${state} (${backend.references.join(", ")})`,
      );
      for (const finding of backend.findings) {
        lines.push(
          `      ${FINDING_LABELS[finding.code]?.title ?? finding.code} [${finding.code}]`,
        );
      }
    }
  }
  if (data.gateway_status) {
    lines.push("", `Gateway 监听器（${data.gateway_status.listeners.length}）`);
    for (const listener of data.gateway_status.listeners) {
      lines.push(`  - ${listener.name} attachedRoutes=${listener.attached_routes}`);
      for (const finding of listener.findings) {
        lines.push(
          `      ${FINDING_LABELS[finding.code]?.title ?? finding.code} [${finding.code}]` +
            `${finding.reason ? ` reason=${finding.reason}` : ""}` +
            `${finding.message ? ` ${finding.message}` : ""}`,
        );
      }
    }
  }
  if (data.autoscaler) {
    const autoscaler = data.autoscaler;
    lines.push(
      "",
      "自动伸缩概况",
      `  Target: ${autoscaler.target.api_version} ${autoscaler.target.kind}/${autoscaler.target.name}`,
      `  Replicas: ${autoscaler.current_replicas} -> ${autoscaler.desired_replicas} (${autoscaler.min_replicas}-${autoscaler.max_replicas})`,
      `  Generation: ${autoscaler.generation} / observed ${autoscaler.observed_generation ?? "—"}`,
    );
    if (data.autoscaler_target) {
      const target = data.autoscaler_target;
      lines.push(`  Target status: ${target.kind}/${target.name} ${target.status || "—"}`);
      for (const finding of target.findings) {
        lines.push(
          `      ${FINDING_LABELS[finding.code]?.title ?? finding.code} [${finding.code}]` +
            `${finding.message ? ` ${finding.message}` : ""}`,
        );
      }
    }
  }
  if (data.vertical_pod_autoscaler) {
    const autoscaler = data.vertical_pod_autoscaler;
    lines.push(
      "",
      "VPA 概况",
      `  Target: ${autoscaler.target.api_version} ${autoscaler.target.kind}/${autoscaler.target.name}`,
      `  Update mode: ${autoscaler.update_mode || "Off"}`,
      `  Recommendations: ${(autoscaler.recommendations ?? []).length}`,
      `  Generation: ${autoscaler.generation} / observed ${autoscaler.observed_generation || "—"}`,
    );
  }
  if (data.keda_scaled_object) {
    const autoscaler = data.keda_scaled_object;
    lines.push(
      "",
      "KEDA ScaledObject 概况",
      `  Target: ${autoscaler.target.api_version} ${autoscaler.target.kind}/${autoscaler.target.name}`,
      `  Replicas: ${autoscaler.min_replicas}-${autoscaler.max_replicas}`,
      `  Ready / active / fallback / paused: ${autoscaler.ready} / ${autoscaler.active} / ${autoscaler.fallback} / ${autoscaler.paused}`,
      `  Generated HPA: ${autoscaler.hpa_name || "—"}`,
    );
  }
  if (!data.autoscaler && data.autoscaler_target) {
    const target = data.autoscaler_target;
    lines.push(`  Target status: ${target.kind}/${target.name} ${target.status || "—"}`);
    for (const finding of target.findings) {
      lines.push(
        `      ${FINDING_LABELS[finding.code]?.title ?? finding.code} [${finding.code}]` +
          `${finding.message ? ` ${finding.message}` : ""}`,
      );
    }
  }
  if (data.policy?.resource_quota && data.policy_status) {
    lines.push("", "配额用量");
    for (const usage of data.policy_status.quota_usage) {
      lines.push(
        `  - ${usage.resource}: ${usage.used} / ${usage.hard}${usage.exhausted ? "（已耗尽）" : ""}`,
      );
    }
  }
  if (data.policy?.disruption_budget) {
    const budget = data.policy.disruption_budget;
    lines.push(
      "",
      "中断预算概况",
      `  Healthy: ${budget.current_healthy} / desired ${budget.desired_healthy}`,
      `  Disruptions allowed: ${budget.disruptions_allowed}`,
      `  Expected Pods: ${budget.expected_pods}`,
    );
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
