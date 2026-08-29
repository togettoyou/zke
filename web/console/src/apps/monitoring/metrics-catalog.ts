/**
 * What the monitoring application draws, and how each number reads.
 *
 * The Server owns the queries; this owns the arrangement. Keeping the two
 * apart is what lets a panel put usage, requests and allocatable capacity on
 * one pair of axes without the Server having to know that three of its named
 * queries answer one question.
 *
 * Panels are grouped into views and views into dimensions on purpose. Every
 * panel is a request a single-instance Server runs against storage that every
 * Cluster shares, so a window left open is a standing load on the whole
 * deployment — the arrangement is what bounds how much of the catalogue is in
 * flight at once. It is also how the question gets asked: an operator looks at
 * Node saturation, or at Namespace requests, not at thirty charts at a time.
 */

export type MetricUnit =
  "millicores" | "bytes" | "bytes_per_second" | "ops_per_second" | "seconds" | "ratio" | "count";

export type CollectorComponent = "kube-state-metrics" | "node-exporter";

export type PanelQuery = {
  /** A name from the Server's query catalogue. */
  name: string;
  /** Distinguishes this query's curves when a panel draws more than one. */
  label?: string;
  /** The scrape target this query reads, when it is not the kubelet. */
  requires?: CollectorComponent;
};

export type Panel = {
  id: string;
  title: string;
  description?: string;
  unit: MetricUnit;
  /** Series labels besides the Cluster, in display order. */
  labels: readonly string[];
  queries: readonly PanelQuery[];
  /**
   * Keeps the axis at full scale. Set for a ratio that means something against
   * 1 — utilisation, density, commitment — and left off for one that is
   * expected to sit near zero, where the shape is the whole point.
   */
  fullScale?: boolean;
  /**
   * Draws the series stacked. Only for a panel whose series are parts of one
   * total — Pod phases, Node readiness — where the height of the stack is an
   * answer of its own. Never for independent measurements: two Nodes' CPU
   * usage piled on top of each other is a number nothing has.
   */
  stack?: boolean;
  /** The line the panel's curves are read against. */
  reference?: { value: number; label: string };
  /**
   * Added to the panel's empty state. For a panel whose data depends on
   * something the operator can act on but the Server cannot see — a scrape
   * target that older collector installs did not configure, a Kubernetes object
   * that may simply not exist in this Cluster. Without it the panel reads as
   * "no data" and the reader has no way to tell an idle Cluster from one that
   * has to be reconfigured.
   */
  emptyNote?: string;
};

export type MetricsView = {
  id: string;
  label: string;
  description?: string;
  /** Whether the answer is bounded by a Top N the operator chooses. */
  top: boolean;
  /** Whether a Namespace filter applies. */
  namespace: boolean;
  panels: readonly Panel[];
};

/**
 * Never empty, so falling back to the first view is a fact rather than a
 * possibility every caller has to re-check.
 */
export type MetricsViews = readonly [MetricsView, ...MetricsView[]];

export type MetricsDimension = {
  id: string;
  label: string;
  views: MetricsViews;
};

const KUBE_STATE = "kube-state-metrics" as const;
const NODE_EXPORTER = "node-exporter" as const;

/**
 * For a panel whose metric was added to the scrape configuration after the
 * first collectors shipped.
 *
 * The Server never rewrites a collector already running inside somebody's
 * Cluster, so an install made before the family was collected keeps scraping
 * exactly what it was told to. That reads as an empty chart, and the reader has
 * no way to tell it from a Cluster where nothing is wrong.
 */
const RESCRAPE_NOTE =
  "该指标在更新后的抓取配置中才会被采集。若集群的采集组件安装于此之前，请在「采集接入」中重新安装采集，以更新抓取配置。";

/** Which install a missing component belongs to, in the operator's words. */
export const COMPONENT_LABELS: Record<CollectorComponent, string> = {
  "kube-state-metrics": "对象指标导出器（kube-state-metrics）",
  "node-exporter": "节点指标导出器（node-exporter）",
};

/* ── 计算资源 ─────────────────────────────────────────────────────────── */

const CLUSTER_DIMENSION: MetricsDimension = {
  id: "cluster",
  label: "集群",
  views: [
    {
      id: "capacity",
      label: "用量与容量",
      description:
        "用量是正在使用的，申请量是调度器已经预留、其他工作负载再也拿不到的，可分配量是全部节点加起来的上限。三条线画在一起，才看得出一个集群是真的满了，还是只是被申请占满了。",
      top: false,
      namespace: false,
      panels: [
        {
          id: "cluster-cpu-capacity",
          title: "CPU",
          unit: "millicores",
          labels: [],
          queries: [
            { name: "cluster_cpu_usage", label: "用量" },
            { name: "cluster_cpu_requests", label: "申请量", requires: KUBE_STATE },
            { name: "cluster_cpu_allocatable", label: "可分配量", requires: KUBE_STATE },
          ],
        },
        {
          id: "cluster-memory-capacity",
          title: "内存",
          unit: "bytes",
          labels: [],
          queries: [
            { name: "cluster_memory_usage", label: "用量" },
            { name: "cluster_memory_requests", label: "申请量", requires: KUBE_STATE },
            { name: "cluster_memory_allocatable", label: "可分配量", requires: KUBE_STATE },
          ],
        },
      ],
    },
    {
      id: "utilization",
      label: "利用率",
      description:
        "利用率是用量占可分配量的比例；申请占比是申请量占可分配量的比例。申请占比接近 1 而利用率很低，说明集群被预留占满而不是被用满。",
      top: false,
      namespace: false,
      panels: [
        {
          id: "cluster-cpu-utilization",
          title: "CPU",
          unit: "ratio",
          labels: [],
          fullScale: true,
          // 申请占比 is the one ratio here that legitimately goes above 1, and
          // that is the moment the panel exists to show: past this line the
          // Cluster has promised more than it has, and a Node failure has
          // nowhere to reschedule to.
          reference: { value: 1, label: "可分配量" },
          queries: [
            { name: "cluster_cpu_utilization", label: "利用率", requires: KUBE_STATE },
            { name: "cluster_cpu_commitment", label: "申请占比", requires: KUBE_STATE },
          ],
        },
        {
          id: "cluster-memory-utilization",
          title: "内存",
          unit: "ratio",
          labels: [],
          fullScale: true,
          reference: { value: 1, label: "可分配量" },
          queries: [
            { name: "cluster_memory_utilization", label: "利用率", requires: KUBE_STATE },
            { name: "cluster_memory_commitment", label: "申请占比", requires: KUBE_STATE },
          ],
        },
      ],
    },
    {
      id: "limits",
      label: "限制量",
      top: false,
      namespace: false,
      panels: [
        {
          id: "cluster-cpu-limits",
          title: "CPU 限制量",
          description:
            "容器被允许使用的上限之和，与可分配量画在一起。限制量高于可分配量说明集群超卖，节点在压力下会开始限流；具体哪些容器被限流见「容器 · 限流」。",
          unit: "millicores",
          labels: [],
          queries: [
            { name: "cluster_cpu_limits", label: "限制量", requires: KUBE_STATE },
            { name: "cluster_cpu_allocatable", label: "可分配量", requires: KUBE_STATE },
          ],
        },
        {
          id: "cluster-memory-limits",
          title: "内存限制量",
          description: "内存没有限流：超过限制的容器被直接终止，退出原因是 OOMKilled。",
          unit: "bytes",
          labels: [],
          queries: [
            { name: "cluster_memory_limits", label: "限制量", requires: KUBE_STATE },
            { name: "cluster_memory_allocatable", label: "可分配量", requires: KUBE_STATE },
          ],
        },
      ],
    },
    {
      id: "modes",
      label: "CPU 模式",
      description:
        "CPU 时间花在了哪里。同样是 80% 利用率，花在用户态上和花在内核态、I/O 等待、被抢占上是三种完全不同的机器，其中只有一种在做被购买的那份工作。堆叠的高度就是集群的 CPU 利用率。",
      top: false,
      namespace: false,
      panels: [
        {
          id: "cluster-cpu-modes",
          title: "CPU 模式分布",
          unit: "ratio",
          labels: ["mode"],
          stack: true,
          fullScale: true,
          queries: [{ name: "cluster_cpu_mode", requires: NODE_EXPORTER }],
        },
      ],
    },
  ],
};

const NODE_DIMENSION: MetricsDimension = {
  id: "node",
  label: "节点",
  views: [
    {
      id: "utilization",
      label: "利用率",
      description: "用量占该节点可分配量的比例。",
      top: true,
      namespace: false,
      panels: [
        {
          id: "node-cpu-utilization",
          title: "CPU 利用率",
          unit: "ratio",
          labels: ["node"],
          fullScale: true,
          queries: [{ name: "node_cpu_utilization", requires: KUBE_STATE }],
        },
        {
          id: "node-memory-utilization",
          title: "内存利用率",
          unit: "ratio",
          labels: ["node"],
          fullScale: true,
          queries: [{ name: "node_memory_utilization", requires: KUBE_STATE }],
        },
      ],
    },
    {
      id: "usage",
      label: "用量",
      top: true,
      namespace: false,
      panels: [
        {
          id: "node-cpu-usage",
          title: "CPU 用量",
          unit: "millicores",
          labels: ["node"],
          queries: [{ name: "node_cpu_usage" }],
        },
        {
          id: "node-memory-usage",
          title: "内存用量",
          unit: "bytes",
          labels: ["node"],
          queries: [{ name: "node_memory_usage" }],
        },
      ],
    },
    {
      id: "saturation",
      label: "饱和度",
      description:
        "利用率说的是用掉了多少，饱和度说的是还跟不跟得上。负载高于核数、I/O 等待抬头、可用内存下探，都是节点上的工作负载已经在排队等待。",
      top: true,
      namespace: false,
      panels: [
        {
          id: "node-load1",
          title: "负载",
          description:
            "可运行与不可中断的进程数，与该节点的核数画在一起：负载 8 在 64 核节点上是空闲，在 2 核节点上是排队。1 分钟高、15 分钟低是一次尖峰，反过来则是这个节点已经落后了一刻钟。",
          unit: "count",
          labels: ["node"],
          queries: [
            { name: "node_load1", label: "1 分钟", requires: NODE_EXPORTER },
            { name: "node_load5", label: "5 分钟", requires: NODE_EXPORTER },
            { name: "node_load15", label: "15 分钟", requires: NODE_EXPORTER },
            { name: "node_cpu_cores", label: "核数", requires: KUBE_STATE },
          ],
        },
        {
          id: "node-cpu-iowait",
          title: "CPU I/O 等待",
          description: "CPU 花在等待磁盘上的比例，已按该节点的核数归一。",
          unit: "ratio",
          labels: ["node"],
          queries: [{ name: "node_cpu_iowait", requires: NODE_EXPORTER }],
        },
        {
          id: "node-cpu-steal",
          title: "CPU 被抢占",
          description:
            "虚拟机等待宿主机让出 CPU 的时间占比。抢占来自节点之外，节点内部的利用率、负载与压力都解释不了它，而上面的工作负载确实变慢了。",
          unit: "ratio",
          labels: ["node"],
          queries: [{ name: "node_cpu_steal", requires: NODE_EXPORTER }],
        },
        {
          id: "node-memory-available",
          title: "可用内存",
          description: "内核估算的可分配内存，包含可回收的页缓存，因此不等于空闲内存。",
          unit: "bytes",
          labels: ["node"],
          queries: [{ name: "node_memory_available", requires: NODE_EXPORTER }],
        },
        {
          id: "node-procs",
          title: "进程队列",
          description:
            "可运行的进程在等 CPU，阻塞的进程在等内核——实际上是在等磁盘。负载把两者加在一起，这张图把它们分开：换更快的 CPU 只对前一条线有用。",
          unit: "count",
          labels: ["node"],
          emptyNote: RESCRAPE_NOTE,
          queries: [
            { name: "node_procs_running", label: "可运行", requires: NODE_EXPORTER },
            { name: "node_procs_blocked", label: "阻塞", requires: NODE_EXPORTER },
          ],
        },
      ],
    },
    {
      id: "memory-detail",
      label: "内存明细",
      description:
        "可用内存说的是还剩多少，这里说的是其余部分变成了什么，以及哪一部分工作负载再也拿不回来。",
      top: true,
      namespace: false,
      panels: [
        {
          id: "node-memory-kernel",
          title: "内核内存",
          description:
            "slab 缓存、页表与内核栈。它不属于任何容器，因此不出现在任何 Pod 的工作集里——这里涨上去，是所有工作负载一起变少。",
          unit: "bytes",
          labels: ["node"],
          queries: [{ name: "node_memory_kernel", requires: NODE_EXPORTER }],
        },
        {
          id: "node-memory-commitment",
          title: "内存承诺占比",
          description:
            "内核已经答应出去的内存占它愿意答应的上限。越过这条线之后，新的分配直接失败，而不是从别处回收。",
          unit: "ratio",
          labels: ["node"],
          fullScale: true,
          reference: { value: 1, label: "承诺上限" },
          queries: [{ name: "node_memory_commitment", requires: NODE_EXPORTER }],
        },
        {
          id: "node-memory-swap",
          title: "Swap 使用率",
          description:
            "kubelet 默认拒绝在启用 swap 的节点上启动，因此这张图通常是空的。出现在这里的节点，是在拿磁盘当内存跑工作负载，而它的内存曲线一切正常。",
          unit: "ratio",
          labels: ["node"],
          fullScale: true,
          emptyNote: "未启用 swap 的节点没有数据，这也是 Kubernetes 节点的常态。",
          queries: [{ name: "node_memory_swap_utilization", requires: NODE_EXPORTER }],
        },
        {
          id: "node-swap-io",
          title: "Swap 换入换出",
          description:
            "真正在内存与磁盘之间搬运的页。上面的使用率说 swap 被占用了，而占用可以是很久以前换出去、之后一直没动的页；这张图说的是节点此刻还在搬，那是上面一切都慢下来的那种状态。",
          unit: "ops_per_second",
          labels: ["node"],
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "node_swap_io", requires: NODE_EXPORTER }],
        },
        {
          id: "node-major-page-faults",
          title: "主缺页",
          description:
            "必须回磁盘取页的缺页。次缺页是常态，不在这里；主缺页意味着节点正在把刚换出去的页读回来，表现为延迟而不是 OOM。",
          unit: "ops_per_second",
          labels: ["node"],
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "node_major_page_faults", requires: NODE_EXPORTER }],
        },
        {
          id: "node-oom-kills",
          title: "节点 OOM 次数",
          description:
            "内核在节点层面杀掉的进程数。与容器的 OOMKilled 不是同一件事：节点自己耗尽内存时杀掉的进程，不会进入任何容器状态原因。",
          unit: "count",
          labels: ["node"],
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "node_oom_kills", requires: NODE_EXPORTER }],
        },
      ],
    },
    {
      id: "system",
      label: "系统",
      description:
        "内核在工作负载之间做的事，以及节点自己的状态。没有一项会出现在用量曲线里，每一项都会改变同样规格的节点实际能做完多少事。",
      top: true,
      namespace: false,
      panels: [
        {
          id: "node-context-switches",
          title: "上下文切换",
          unit: "ops_per_second",
          labels: ["node"],
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "node_context_switches", requires: NODE_EXPORTER }],
        },
        {
          id: "node-interrupts",
          title: "中断",
          unit: "ops_per_second",
          labels: ["node"],
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "node_interrupts", requires: NODE_EXPORTER }],
        },
        {
          id: "node-file-descriptors",
          title: "文件描述符使用率",
          description:
            "整台机器共用的描述符表。用尽之后，节点上所有的 accept 与 open 一起失败，而应用报出来的错误不会提到任何资源。",
          unit: "ratio",
          labels: ["node"],
          fullScale: true,
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "node_file_descriptor_utilization", requires: NODE_EXPORTER }],
        },
        {
          id: "node-uptime",
          title: "运行时长",
          description:
            "节点重启在别处不留痕迹：节点重新 Ready，Pod 被重新调度，所有曲线接着画下去。这是唯一一条把重启画成事件的线——它归零的那一刻。",
          unit: "seconds",
          labels: ["node"],
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "node_uptime", requires: NODE_EXPORTER }],
        },
        {
          id: "node-clock-offset",
          title: "时钟偏移",
          description:
            "取绝对值：快 5 秒和慢 5 秒是同一个问题。时钟漂移在别处从来不以自己的名义出现，它表现为证书未生效、日志乱序、样本因为超出摄取窗口被拒绝。",
          unit: "seconds",
          labels: ["node"],
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "node_clock_offset", requires: NODE_EXPORTER }],
        },
        {
          id: "node-clock-sync",
          title: "时钟同步状态",
          description: "1 表示时钟正在被校准；0 表示上面那条偏移没有人在纠正。",
          unit: "count",
          labels: ["node"],
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "node_clock_synchronized", requires: NODE_EXPORTER }],
        },
      ],
    },
    {
      id: "kubelet",
      label: "Kubelet",
      description:
        "节点上其余所有数字都是经由 kubelet 测出来的，因此 kubelet 本身出问题时，节点看起来反而很平静——曲线不是抬升，而是不再变化。",
      top: true,
      namespace: false,
      panels: [
        {
          id: "node-kubelet-workload",
          title: "运行中的 Pod 与容器",
          description:
            "容器数不是 Pod 数：一个 Pod 是若干个容器。容器数在动而 Pod 数不动的节点，是有容器在反复重启，而 Pod 始终停留在 Running。",
          unit: "count",
          labels: ["node"],
          emptyNote: RESCRAPE_NOTE,
          queries: [
            { name: "node_kubelet_pods", label: "Pod" },
            { name: "node_kubelet_containers", label: "容器" },
          ],
        },
        {
          id: "node-kubelet-runtime-errors",
          title: "容器运行时错误",
          description:
            "拉镜像、建沙箱、杀容器——这些调用失败的节点，上面的 Pod 是卡住而不是崩溃，症状在 Pod 层面看不出原因。",
          unit: "ops_per_second",
          labels: ["node"],
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "node_kubelet_runtime_errors" }],
        },
        {
          id: "node-kubelet-pleg",
          title: "PLEG 时延",
          description:
            "kubelet 遍历本节点容器状态一轮的平均耗时，是它自己的心跳。这个值变大之后，就绪、重启与用量全部迟到，Pod 会因为不属于它们的原因被判定为不健康。",
          unit: "seconds",
          labels: ["node"],
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "node_kubelet_pleg_latency" }],
        },
      ],
    },
    {
      id: "density",
      label: "Pod 密度",
      description:
        "节点会用完的第三种容量。CPU 与内存都还有余量的节点，装满 Pod 之后同样调度不进去。",
      top: true,
      namespace: false,
      panels: [
        {
          id: "node-pod-utilization",
          title: "Pod 密度",
          description: "已调度 Pod 数占该节点 Pod 容量的比例。",
          unit: "ratio",
          labels: ["node"],
          fullScale: true,
          queries: [{ name: "node_pod_utilization", requires: KUBE_STATE }],
        },
        {
          id: "node-pod-count",
          title: "Pod 数量",
          unit: "count",
          labels: ["node"],
          queries: [{ name: "node_pod_count", requires: KUBE_STATE }],
        },
      ],
    },
    {
      id: "pressure",
      label: "压力停顿",
      description:
        "内核统计的等待时间：任务因为拿不到 CPU、内存或磁盘而停下来的时间占比。它衡量的是延迟本身，而不是资源用到了多少——内存回收忙碌的节点，可用内存看上去可能完全正常。需要节点内核提供 /proc/pressure（Linux 4.20 及以上），否则这里没有数据。",
      top: true,
      namespace: false,
      panels: [
        {
          id: "node-pressure-cpu",
          title: "CPU 压力",
          unit: "ratio",
          labels: ["node"],
          emptyNote: "需要节点内核提供 /proc/pressure（Linux 4.20 及以上），较旧的内核不会上报。",
          queries: [{ name: "node_pressure_cpu", requires: NODE_EXPORTER }],
        },
        {
          id: "node-pressure-memory",
          title: "内存压力",
          unit: "ratio",
          labels: ["node"],
          emptyNote: "需要节点内核提供 /proc/pressure（Linux 4.20 及以上），较旧的内核不会上报。",
          queries: [{ name: "node_pressure_memory", requires: NODE_EXPORTER }],
        },
        {
          id: "node-pressure-io",
          title: "I/O 压力",
          unit: "ratio",
          labels: ["node"],
          emptyNote: "需要节点内核提供 /proc/pressure（Linux 4.20 及以上），较旧的内核不会上报。",
          queries: [{ name: "node_pressure_io", requires: NODE_EXPORTER }],
        },
      ],
    },
  ],
};

const NAMESPACE_DIMENSION: MetricsDimension = {
  id: "namespace",
  label: "Namespace",
  views: [
    {
      id: "usage",
      label: "用量",
      top: true,
      namespace: true,
      panels: [
        {
          id: "namespace-cpu-usage",
          title: "CPU 用量",
          unit: "millicores",
          labels: ["namespace"],
          queries: [{ name: "namespace_cpu_usage" }],
        },
        {
          id: "namespace-memory-usage",
          title: "内存用量",
          unit: "bytes",
          labels: ["namespace"],
          queries: [{ name: "namespace_memory_usage" }],
        },
      ],
    },
    {
      id: "requests",
      label: "申请量",
      description: "调度器为该 Namespace 预留、其他工作负载无法使用的量。",
      top: true,
      namespace: true,
      panels: [
        {
          id: "namespace-cpu-requests",
          title: "CPU 申请量",
          unit: "millicores",
          labels: ["namespace"],
          queries: [{ name: "namespace_cpu_requests", requires: KUBE_STATE }],
        },
        {
          id: "namespace-memory-requests",
          title: "内存申请量",
          unit: "bytes",
          labels: ["namespace"],
          queries: [{ name: "namespace_memory_requests", requires: KUBE_STATE }],
        },
      ],
    },
    {
      id: "limits",
      label: "限制量",
      top: true,
      namespace: true,
      panels: [
        {
          id: "namespace-cpu-limits",
          title: "CPU 限制量",
          unit: "millicores",
          labels: ["namespace"],
          queries: [{ name: "namespace_cpu_limits", requires: KUBE_STATE }],
        },
        {
          id: "namespace-memory-limits",
          title: "内存限制量",
          unit: "bytes",
          labels: ["namespace"],
          queries: [{ name: "namespace_memory_limits", requires: KUBE_STATE }],
        },
      ],
    },
    {
      id: "scale",
      label: "规模",
      top: true,
      namespace: true,
      panels: [
        {
          id: "namespace-pod-count",
          title: "运行中 Pod 数量",
          unit: "count",
          labels: ["namespace"],
          queries: [{ name: "namespace_pod_count", requires: KUBE_STATE }],
        },
      ],
    },
    {
      id: "quota",
      label: "配额",
      description:
        "ResourceQuota 的已用量占上限的比例，按资源分别统计。配额用满的 Namespace 会拒绝新建 Pod，而集群本身可能还很空闲——这种情况在上面任何一张用量曲线里都看不见，因为那些工作负载根本没有被创建出来。没有配置 ResourceQuota 的 Namespace 在这里没有数据。",
      top: true,
      namespace: true,
      panels: [
        {
          id: "namespace-quota-utilization",
          title: "配额使用率",
          unit: "ratio",
          labels: ["namespace", "resource"],
          fullScale: true,
          reference: { value: 1, label: "配额上限" },
          emptyNote: "没有配置 ResourceQuota 的 Namespace 不会有数据。",
          queries: [{ name: "namespace_quota_utilization", requires: KUBE_STATE }],
        },
      ],
    },
  ],
};

const WORKLOAD_DIMENSION: MetricsDimension = {
  id: "workload",
  label: "工作负载",
  views: [
    {
      id: "usage",
      label: "用量",
      description:
        "Pod 用量按拥有它的控制器汇总；Deployment 的 Pod 会归到 Deployment 而不是它当前的 ReplicaSet。",
      top: true,
      namespace: true,
      panels: [
        {
          id: "workload-cpu-usage",
          title: "CPU 用量",
          unit: "millicores",
          labels: ["namespace", "workload_kind", "workload"],
          queries: [{ name: "workload_cpu_usage", requires: KUBE_STATE }],
        },
        {
          id: "workload-memory-usage",
          title: "内存用量",
          unit: "bytes",
          labels: ["namespace", "workload_kind", "workload"],
          queries: [{ name: "workload_memory_usage", requires: KUBE_STATE }],
        },
      ],
    },
  ],
};

const POD_DIMENSION: MetricsDimension = {
  id: "pod",
  label: "Pod",
  views: [
    {
      id: "usage",
      label: "用量",
      top: true,
      namespace: true,
      panels: [
        {
          id: "pod-cpu-usage",
          title: "CPU 用量",
          unit: "millicores",
          labels: ["namespace", "pod"],
          queries: [{ name: "pod_cpu_usage" }],
        },
        {
          id: "pod-memory-usage",
          title: "内存用量",
          unit: "bytes",
          labels: ["namespace", "pod"],
          queries: [{ name: "pod_memory_usage" }],
        },
      ],
    },
    {
      id: "restarts",
      label: "重启",
      top: true,
      namespace: true,
      panels: [
        {
          id: "pod-restarts",
          title: "重启次数",
          description: "所选时间范围内新增的容器重启次数，不是计数器的累计值。",
          unit: "count",
          labels: ["namespace", "pod"],
          queries: [{ name: "pod_restarts", requires: KUBE_STATE }],
        },
        {
          id: "pod-oom-kills",
          title: "OOM 次数",
          description:
            "容器内被内核杀掉的次数，按发生时间计。Kubernetes 侧的 OOMKilled 只保留最后一次退出原因，Pod 被控制器换掉之后就随之消失，这条计数器是留下来的那份。",
          unit: "count",
          labels: ["namespace", "pod"],
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "pod_oom_kills" }],
        },
      ],
    },
    {
      id: "disk",
      label: "磁盘",
      description:
        "节点层面能看出某块盘被打满，看不出是谁打满的。这两张图回答后一个问题，数据与限流、Pod 网络来自 kubelet 的同一个端点。",
      top: true,
      namespace: true,
      panels: [
        {
          id: "pod-disk-read",
          title: "磁盘读取",
          unit: "bytes_per_second",
          labels: ["namespace", "pod"],
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "pod_disk_read" }],
        },
        {
          id: "pod-disk-write",
          title: "磁盘写入",
          unit: "bytes_per_second",
          labels: ["namespace", "pod"],
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "pod_disk_write" }],
        },
      ],
    },
    {
      id: "network",
      label: "网络",
      description:
        "按 Pod 统计的收发速率。节点网卡打满是「存储与网络」回答的问题，是谁打满的在这里回答。",
      top: true,
      namespace: true,
      panels: [
        {
          id: "pod-network-receive",
          title: "网络接收",
          unit: "bytes_per_second",
          labels: ["namespace", "pod"],
          emptyNote:
            "这些指标来自 kubelet 的 cAdvisor 端点。若集群的采集组件安装于该端点被纳入抓取之前，请在「采集接入」中重新安装采集，以更新抓取配置。",
          queries: [{ name: "pod_network_receive" }],
        },
        {
          id: "pod-network-transmit",
          title: "网络发送",
          unit: "bytes_per_second",
          labels: ["namespace", "pod"],
          emptyNote:
            "这些指标来自 kubelet 的 cAdvisor 端点。若集群的采集组件安装于该端点被纳入抓取之前，请在「采集接入」中重新安装采集，以更新抓取配置。",
          queries: [{ name: "pod_network_transmit" }],
        },
        {
          id: "pod-network-drops",
          title: "网络丢包",
          description:
            "Pod 网卡上没有送出去的包。丢包在收发字节曲线上完全看不出来——失败的那部分流量根本没有被计入。",
          unit: "ops_per_second",
          labels: ["namespace", "pod"],
          emptyNote: RESCRAPE_NOTE,
          queries: [{ name: "pod_network_drops" }],
        },
      ],
    },
  ],
};

const CONTAINER_DIMENSION: MetricsDimension = {
  id: "container",
  label: "容器",
  views: [
    {
      id: "usage",
      label: "用量",
      description:
        "Pod 是一组进程而不是一个。上一层的曲线说明这个 Pod 在消耗，这里说明是其中哪个容器。",
      top: true,
      namespace: true,
      panels: [
        {
          id: "container-cpu-usage",
          title: "CPU 用量",
          unit: "millicores",
          labels: ["namespace", "pod", "container"],
          queries: [{ name: "container_cpu_usage" }],
        },
        {
          id: "container-memory-usage",
          title: "内存用量",
          unit: "bytes",
          labels: ["namespace", "pod", "container"],
          queries: [{ name: "container_memory_usage" }],
        },
      ],
    },
    {
      id: "throttling",
      label: "限流",
      description:
        "容器因为达到 CPU 限制而被暂停的周期占比。被限流的容器用量恰好等于它被允许的量，因此在任何用量曲线上都看不出异常——变慢的原因只在这条曲线里。没有设置 CPU 限制的容器在这里没有数据。",
      top: true,
      namespace: true,
      panels: [
        {
          id: "container-cpu-throttling",
          title: "CPU 限流比例",
          unit: "ratio",
          labels: ["namespace", "pod", "container"],
          fullScale: true,
          emptyNote:
            "这些指标来自 kubelet 的 cAdvisor 端点。若集群的采集组件安装于该端点被纳入抓取之前，请在「采集接入」中重新安装采集，以更新抓取配置。",
          queries: [{ name: "container_cpu_throttling" }],
        },
      ],
    },
  ],
};

export const COMPUTE_DIMENSIONS: readonly [MetricsDimension, ...MetricsDimension[]] = [
  CLUSTER_DIMENSION,
  NODE_DIMENSION,
  NAMESPACE_DIMENSION,
  WORKLOAD_DIMENSION,
  POD_DIMENSION,
  CONTAINER_DIMENSION,
];

/* ── 存储与网络 ───────────────────────────────────────────────────────── */

export const STORAGE_VIEWS: MetricsViews = [
  {
    id: "pvc",
    label: "持久卷",
    description:
      "PersistentVolumeClaim 的用量，由挂载它的 kubelet 上报，因此不需要节点指标导出器。下面的节点文件系统描述的是磁盘，这里描述的是声明——一个写满的 PVC 所在的磁盘可能还很空。",
    top: true,
    namespace: true,
    panels: [
      {
        id: "pvc-utilization",
        title: "PVC 使用率",
        unit: "ratio",
        labels: ["namespace", "persistentvolumeclaim"],
        fullScale: true,
        emptyNote:
          "该视图来自 kubelet 的卷统计端点，只统计已被 Pod 挂载的 PVC。若集群的采集组件安装于该端点被纳入抓取之前，请在「采集接入」中重新安装采集，以更新抓取配置。",
        queries: [{ name: "pvc_utilization" }],
      },
      {
        id: "pvc-used",
        title: "PVC 已用空间",
        unit: "bytes",
        labels: ["namespace", "persistentvolumeclaim"],
        emptyNote:
          "该视图来自 kubelet 的卷统计端点，只统计已被 Pod 挂载的 PVC。若集群的采集组件安装于该端点被纳入抓取之前，请在「采集接入」中重新安装采集，以更新抓取配置。",
        queries: [{ name: "pvc_used_bytes" }],
      },
      {
        id: "pvc-inodes",
        title: "PVC inode 使用率",
        description: "inode 用完的卷剩余空间看起来完全正常，写入却全部失败。",
        unit: "ratio",
        labels: ["namespace", "persistentvolumeclaim"],
        fullScale: true,
        emptyNote:
          "该视图来自 kubelet 的卷统计端点，只统计已被 Pod 挂载的 PVC。若集群的采集组件安装于该端点被纳入抓取之前，请在「采集接入」中重新安装采集，以更新抓取配置。",
        queries: [{ name: "pvc_inode_utilization" }],
      },
    ],
  },
  {
    id: "filesystem",
    label: "文件系统",
    description:
      "空间与 inode 是两种各自会先耗尽的资源：inode 用完的文件系统，剩余空间看起来完全正常，写入却全部失败。",
    top: true,
    namespace: false,
    panels: [
      {
        id: "filesystem-utilization",
        title: "文件系统使用率",
        unit: "ratio",
        labels: ["node", "mountpoint"],
        fullScale: true,
        queries: [{ name: "node_filesystem_utilization", requires: NODE_EXPORTER }],
      },
      {
        id: "filesystem-inodes",
        title: "inode 使用率",
        unit: "ratio",
        labels: ["node", "mountpoint"],
        fullScale: true,
        queries: [{ name: "node_filesystem_inode_utilization", requires: NODE_EXPORTER }],
      },
      {
        id: "filesystem-faults",
        title: "只读挂载与设备错误",
        description:
          "写满是上面两张图的事，这一张是空间还富余时就已经失败的那些：I/O 错误之后被内核改成只读的文件系统，写入全部失败而已用空间纹丝不动；统计不到的挂载点，则是设备正在消失。",
        unit: "count",
        labels: ["node"],
        queries: [
          { name: "node_filesystem_readonly", label: "只读挂载点", requires: NODE_EXPORTER },
          { name: "node_filesystem_device_errors", label: "设备错误", requires: NODE_EXPORTER },
        ],
      },
    ],
  },
  {
    id: "disk",
    label: "磁盘吞吐",
    top: true,
    namespace: false,
    panels: [
      {
        id: "disk-read",
        title: "磁盘读取",
        unit: "bytes_per_second",
        labels: ["node", "device"],
        queries: [{ name: "node_disk_read", requires: NODE_EXPORTER }],
      },
      {
        id: "disk-write",
        title: "磁盘写入",
        unit: "bytes_per_second",
        labels: ["node", "device"],
        queries: [{ name: "node_disk_write", requires: NODE_EXPORTER }],
      },
      {
        id: "disk-busy",
        title: "磁盘繁忙度",
        description:
          "设备上有请求在处理的时间占比。接近 1 的设备就是这个节点上一切变慢的原因，与它的吞吐量高低无关。",
        unit: "ratio",
        labels: ["node", "device"],
        fullScale: true,
        queries: [{ name: "node_disk_io_utilization", requires: NODE_EXPORTER }],
      },
    ],
  },
  {
    id: "iops",
    label: "磁盘 IOPS",
    description: "每秒完成的读写次数。小文件负载会在吞吐量还很低时先打满 IOPS。",
    top: true,
    namespace: false,
    panels: [
      {
        id: "disk-read-ops",
        title: "读 IOPS",
        unit: "ops_per_second",
        labels: ["node", "device"],
        queries: [{ name: "node_disk_read_ops", requires: NODE_EXPORTER }],
      },
      {
        id: "disk-write-ops",
        title: "写 IOPS",
        unit: "ops_per_second",
        labels: ["node", "device"],
        queries: [{ name: "node_disk_write_ops", requires: NODE_EXPORTER }],
      },
    ],
  },
  {
    id: "disk-latency",
    label: "磁盘延迟",
    description:
      "吞吐量与 IOPS 说的是设备做了多少，延迟说的是每一次操作花了多久——后者才是上面的工作负载真正感受到的东西。繁忙度接近 1 而队列很短的设备是一直在做事，队列涨起来的设备才是排上队了。",
    top: true,
    namespace: false,
    panels: [
      {
        id: "disk-read-latency",
        title: "读延迟",
        unit: "seconds",
        labels: ["node", "device"],
        queries: [{ name: "node_disk_read_latency", requires: NODE_EXPORTER }],
      },
      {
        id: "disk-write-latency",
        title: "写延迟",
        unit: "seconds",
        labels: ["node", "device"],
        queries: [{ name: "node_disk_write_latency", requires: NODE_EXPORTER }],
      },
      {
        id: "disk-queue",
        title: "队列长度",
        description: "设备上平均有多少个请求在飞。这是「一直在忙」与「已经排队」之间的那条界线。",
        unit: "count",
        labels: ["node", "device"],
        queries: [{ name: "node_disk_queue", requires: NODE_EXPORTER }],
      },
    ],
  },
  {
    id: "network",
    label: "网络",
    top: true,
    namespace: false,
    panels: [
      {
        id: "network-receive",
        title: "网络接收",
        unit: "bytes_per_second",
        labels: ["node", "device"],
        queries: [{ name: "node_network_receive", requires: NODE_EXPORTER }],
      },
      {
        id: "network-transmit",
        title: "网络发送",
        unit: "bytes_per_second",
        labels: ["node", "device"],
        queries: [{ name: "node_network_transmit", requires: NODE_EXPORTER }],
      },
      {
        id: "network-packets",
        title: "网络包速率",
        description:
          "云上的网卡同时按带宽和包数计费与限速，而由小请求组成的流量先撞到的是包数上限——那一刻字节曲线看起来还只用了一半。",
        unit: "ops_per_second",
        labels: ["node", "device"],
        queries: [{ name: "node_network_packets", requires: NODE_EXPORTER }],
      },
      {
        id: "network-errors",
        title: "错误与丢包",
        description: "收发两个方向的错误与丢包合计。持续高于零的接口就是需要处理的那一个。",
        unit: "ops_per_second",
        labels: ["node", "device"],
        queries: [{ name: "node_network_errors", requires: NODE_EXPORTER }],
      },
    ],
  },
  {
    id: "network-saturation",
    label: "网络饱和",
    description:
      "吞吐量说的是通过了多少，这里说的是还通不通得过。连接跟踪表写满、握手完成却没有进程接收、重传升高，都会让请求超时，而节点的字节计数在这三种情况下都完全正常。",
    top: true,
    namespace: false,
    panels: [
      {
        id: "node-conntrack",
        title: "连接跟踪表使用率",
        description: "表写满的节点会丢弃新建连接，已建立的连接不受影响——现象是新请求超时。",
        unit: "ratio",
        labels: ["node"],
        fullScale: true,
        queries: [{ name: "node_conntrack_utilization", requires: NODE_EXPORTER }],
      },
      {
        id: "node-tcp-retransmission",
        title: "TCP 重传比例",
        description: "重传段数占发出段数的比例。按比例而不是按条数，繁忙节点才不会天然显得更糟。",
        unit: "ratio",
        labels: ["node"],
        queries: [{ name: "node_tcp_retransmission", requires: NODE_EXPORTER }],
      },
      {
        id: "node-listen-drops",
        title: "连接队列丢弃",
        description: "握手已经完成、监听队列却满了而被丢掉的连接。客户端看到的是连接超时。",
        unit: "ops_per_second",
        labels: ["node"],
        queries: [{ name: "node_tcp_listen_drops", requires: NODE_EXPORTER }],
      },
      {
        id: "node-tcp-connections",
        title: "TCP 连接数",
        description:
          "已建立的连接，与正在等待 TIME_WAIT 结束的连接。后者每一条都占着一个本地端口，短连接密集的节点会先用完端口，而不是用完这一屏上的任何别的东西。",
        unit: "count",
        labels: ["node"],
        emptyNote: RESCRAPE_NOTE,
        queries: [
          { name: "node_tcp_connections", label: "已建立", requires: NODE_EXPORTER },
          { name: "node_tcp_timewait", label: "TIME_WAIT", requires: NODE_EXPORTER },
        ],
      },
      {
        id: "node-udp-errors",
        title: "UDP 错误",
        description:
          "集群 DNS 走 UDP：节点上的接收缓冲区溢出，在每一个 Pod 里都表现为解析超时，而它不出现在任何 TCP 计数器和任何吞吐曲线上。",
        unit: "ops_per_second",
        labels: ["node"],
        emptyNote: RESCRAPE_NOTE,
        queries: [{ name: "node_udp_errors", requires: NODE_EXPORTER }],
      },
      {
        id: "node-socket-memory",
        title: "套接字内存",
        description:
          "内核为套接字缓冲区占用的内存。越过内核自己的上限之后，它会开始裁剪连接，应用看到的是没有人发出过的 reset。",
        unit: "bytes",
        labels: ["node"],
        emptyNote: RESCRAPE_NOTE,
        queries: [{ name: "node_socket_memory", requires: NODE_EXPORTER }],
      },
    ],
  },
];

/* ── Kubernetes 资源 ──────────────────────────────────────────────────── */

export const KUBERNETES_VIEWS: MetricsViews = [
  {
    id: "pods",
    label: "Pod 状态",
    description:
      "资源视图能回答此刻有多少 Pod 不正常，只有曲线能回答它是什么时候开始的，以及是不是每天同一时间都这样。",
    top: false,
    namespace: true,
    panels: [
      {
        id: "pod-phase",
        title: "Pod 状态分布",
        // Stacked because the phases are parts of one number: the height of the
        // stack is the Pod count, and a Failed band appearing without the total
        // moving is a different event from one that grew it.
        unit: "count",
        labels: ["phase"],
        stack: true,
        queries: [{ name: "cluster_pod_phase", requires: KUBE_STATE }],
      },
      {
        id: "container-restarts",
        title: "容器重启次数",
        description: "所选时间范围内集群新增的容器重启次数。",
        unit: "count",
        labels: [],
        queries: [{ name: "cluster_container_restarts", requires: KUBE_STATE }],
      },
      {
        id: "pod-scheduling",
        title: "就绪与调度失败",
        description:
          "Running 不等于在服务：就绪探针失败的 Pod 仍然是 Running，却已经被从每一个 Service 后面摘掉，而状态分布把这件事画成一片健康。无法调度的 Pod 同样是 Pending，与还在拉镜像的 Pod 长得一模一样——一个自己会好，另一个在等永远可能不来的容量、容忍或卷。",
        unit: "count",
        labels: [],
        emptyNote: RESCRAPE_NOTE,
        queries: [
          { name: "cluster_pod_ready", label: "就绪", requires: KUBE_STATE },
          { name: "cluster_pod_unschedulable", label: "无法调度", requires: KUBE_STATE },
        ],
      },
    ],
  },
  {
    id: "nodes",
    label: "节点状态",
    top: false,
    namespace: false,
    panels: [
      {
        id: "node-readiness",
        title: "节点就绪状态",
        description: "按 Ready 条件统计的节点数，三种状态相加等于集群的节点总数。",
        unit: "count",
        labels: ["status"],
        stack: true,
        queries: [{ name: "cluster_node_readiness", requires: KUBE_STATE }],
      },
      {
        id: "node-pressure",
        title: "节点压力",
        description:
          "当前处于内存、磁盘或 PID 压力下的节点数。持平为零正是要看的答案，因此这条线始终画出来。",
        unit: "count",
        labels: ["condition"],
        queries: [{ name: "cluster_node_pressure", requires: KUBE_STATE }],
      },
      {
        id: "node-unschedulable",
        title: "已封锁节点",
        description:
          "被 cordon 的节点不是故障节点：它不带任何条件，用量一切正常，只是安静地不再接收 Pod。一次没做完的维护让集群少掉三分之一可调度容量，别处不会说。",
        unit: "count",
        labels: [],
        emptyNote: RESCRAPE_NOTE,
        queries: [{ name: "cluster_node_unschedulable", requires: KUBE_STATE }],
      },
    ],
  },
  {
    id: "containers",
    label: "容器状态",
    description:
      "重启曲线说明有容器在反复出问题，这两张图说明是什么问题。原因保留 Kubernetes 的英文原值，与 kubectl describe 里看到的一致。只统计操作者需要处理的原因。",
    top: false,
    namespace: true,
    panels: [
      {
        id: "container-waiting",
        title: "等待中的容器",
        description:
          "拉不到镜像、配置解析不了、进程反复退出——三种故障对应三种处理方式，重启次数分不出来。",
        unit: "count",
        labels: ["reason"],
        stack: true,
        queries: [{ name: "pod_container_waiting", requires: KUBE_STATE }],
      },
      {
        id: "container-terminated",
        title: "容器退出原因",
        description:
          "最近一次退出的原因。OOMKilled 是这张图存在的理由：内存超限的容器被直接杀掉，它在任何用量曲线上都只是突然消失。",
        unit: "count",
        labels: ["reason"],
        stack: true,
        queries: [{ name: "pod_container_terminated", requires: KUBE_STATE }],
      },
    ],
  },
  {
    id: "replicas",
    label: "工作负载副本",
    description:
      "3/3 就绪与 1/3 就绪在列表里是同一个绿色对勾，这里是把两者分开的那个数字，缺得最多的排在最前。",
    top: true,
    namespace: true,
    panels: [
      {
        id: "workload-replicas-unavailable",
        title: "未就绪副本",
        unit: "count",
        labels: ["namespace", "workload_kind", "workload"],
        queries: [{ name: "workload_replicas_unavailable", requires: KUBE_STATE }],
      },
      {
        id: "workload-replicas",
        title: "期望与就绪副本",
        description:
          "两条线画在一起才看得出发生了什么：一起抬升是扩容，就绪掉下去而期望不动是副本掉了，两条一起波动是滚动更新。",
        unit: "count",
        labels: ["namespace", "workload_kind", "workload"],
        queries: [
          { name: "workload_replicas_desired", label: "期望", requires: KUBE_STATE },
          { name: "workload_replicas_ready", label: "就绪", requires: KUBE_STATE },
        ],
      },
    ],
  },
  {
    id: "jobs",
    label: "批处理",
    description:
      "副本视角故意不包含 Job：跑完的 Job 不是缺副本的工作负载。而一个连续失败了一周的定时任务，在别的任何视角里都看不见——等有人去看的时候，它的 Pod 早就不在了。",
    top: false,
    namespace: true,
    panels: [
      {
        id: "job-status",
        title: "Job 状态",
        unit: "count",
        labels: [],
        emptyNote: RESCRAPE_NOTE,
        queries: [
          { name: "cluster_job_active", label: "运行中", requires: KUBE_STATE },
          { name: "cluster_job_failed", label: "失败", requires: KUBE_STATE },
        ],
      },
      {
        id: "job-failed-namespace",
        title: "各 Namespace 失败任务",
        unit: "count",
        labels: ["namespace"],
        emptyNote: RESCRAPE_NOTE,
        queries: [{ name: "namespace_job_failed", requires: KUBE_STATE }],
      },
    ],
  },
  {
    id: "storage-objects",
    label: "存储对象",
    description:
      "持久卷视角量的是已经被挂载的 PVC，而故障恰好落在这个集合之外：卡在 Pending 的声明没有 Pod 起得来，因此没有任何使用率可量。持久卷同理，Released 与 Failed 占着真实的存储，声明已经没了，卷还在。",
    top: false,
    namespace: false,
    panels: [
      {
        id: "pvc-phase",
        title: "PVC 状态分布",
        unit: "count",
        labels: ["phase"],
        stack: true,
        emptyNote: RESCRAPE_NOTE,
        queries: [{ name: "cluster_pvc_phase", requires: KUBE_STATE }],
      },
      {
        id: "pv-phase",
        title: "持久卷状态分布",
        unit: "count",
        labels: ["phase"],
        stack: true,
        emptyNote: RESCRAPE_NOTE,
        queries: [{ name: "cluster_pv_phase", requires: KUBE_STATE }],
      },
    ],
  },
];

/* ── 采集质量 ─────────────────────────────────────────────────────────── */

/**
 * The collection pipeline observing itself.
 *
 * Every series here has been in storage since the first install — the collector
 * writes a handful of them for each target it scrapes — and until now only the
 * overall `up` average was ever read. They answer the one question the rest of
 * this application cannot answer about itself: when a screen is empty, whether
 * the Cluster is idle, a target is down, or one exporter's collector cannot run
 * on these Nodes.
 *
 * It is a chart section rather than a part of 采集接入 on purpose. Installing
 * and removing collectors needs `cluster.metrics.manage`; reading charts needs
 * only `cluster.metrics.read` — and an operator who can only read is exactly
 * the one left with a screen of empty charts and no way to find out why.
 */
export const COLLECTION_QUALITY_VIEWS: MetricsViews = [
  {
    id: "scrape",
    label: "抓取",
    description:
      "采集器为它抓的每一个目标写下这几条序列。它们与集群的摄取预算是同一件事：样本数是每次抓取要付的，新增序列是在整个保留期里要付的。",
    top: false,
    namespace: false,
    panels: [
      {
        id: "collection-target-health",
        title: "目标健康度",
        description:
          "哪一个目标在失败，而不是有几个在失败。集群级的平均值只说明出了问题，这张图说明该去修 kubelet、对象导出器还是节点导出器——三者是三种不同的处置。",
        unit: "ratio",
        labels: ["job"],
        fullScale: true,
        queries: [{ name: "collection_target_health" }],
      },
      {
        id: "collection-scrape-duration",
        title: "抓取耗时",
        description:
          "抓取耗时接近抓取间隔的目标会开始被截断，而随之而来的数据缺失读起来像集群安静了下来，不像某个目标变慢了。",
        unit: "seconds",
        labels: ["job"],
        queries: [{ name: "collection_scrape_duration" }],
      },
      {
        id: "collection-samples",
        title: "采集样本数",
        description:
          "每次抓取在过滤之后真正写进存储的样本数，也就是这个集群的摄取预算花在哪里。开始被限流的集群，是这条线先动的。",
        unit: "count",
        labels: ["job"],
        queries: [{ name: "collection_samples" }],
      },
      {
        id: "collection-series-added",
        title: "新增序列",
        description:
          "这次抓取带来了上次没有的多少条序列。样本数是每次抓取要付的钱，这条是整个保留期要付的——样本数平稳而它持续不为零的目标，标签里带着每次重启都会变的东西。",
        unit: "count",
        labels: ["job"],
        queries: [{ name: "collection_series_added" }],
      },
      {
        id: "collection-node-collectors",
        title: "节点采集器失败数",
        description:
          "节点导出器对自己每个 collector 的报告，按失败的节点数统计。它是唯一能把「这个集群没什么可报」和「这个 collector 在这里跑不起来」分开的序列——缺少 /proc/pressure 的内核、没有加载模块的 conntrack，在别处都只是一张空图。",
        unit: "count",
        labels: ["collector"],
        queries: [{ name: "collection_node_collectors", requires: NODE_EXPORTER }],
      },
    ],
  },
];

/* ── 总览 ─────────────────────────────────────────────────────────────── */

export const OVERVIEW_PANELS: readonly Panel[] = [
  // Usage first, and from the kubelet alone. Every other panel on this screen
  // reads kube-state-metrics, so a Cluster whose object exporter is down or was
  // never installed would land on a completely blank overview while the usage
  // data behind it is arriving normally.
  {
    id: "overview-cpu-usage",
    title: "CPU 用量",
    unit: "millicores",
    labels: [],
    queries: [{ name: "cluster_cpu_usage" }],
  },
  {
    id: "overview-memory-usage",
    title: "内存用量",
    unit: "bytes",
    labels: [],
    queries: [{ name: "cluster_memory_usage" }],
  },
  {
    id: "overview-cpu",
    title: "CPU 利用率与申请占比",
    unit: "ratio",
    labels: [],
    fullScale: true,
    reference: { value: 1, label: "可分配量" },
    queries: [
      { name: "cluster_cpu_utilization", label: "利用率", requires: KUBE_STATE },
      { name: "cluster_cpu_commitment", label: "申请占比", requires: KUBE_STATE },
    ],
  },
  {
    id: "overview-memory",
    title: "内存利用率与申请占比",
    unit: "ratio",
    labels: [],
    fullScale: true,
    reference: { value: 1, label: "可分配量" },
    queries: [
      { name: "cluster_memory_utilization", label: "利用率", requires: KUBE_STATE },
      { name: "cluster_memory_commitment", label: "申请占比", requires: KUBE_STATE },
    ],
  },
  {
    id: "overview-phase",
    title: "Pod 状态分布",
    unit: "count",
    labels: ["phase"],
    stack: true,
    queries: [{ name: "cluster_pod_phase", requires: KUBE_STATE }],
  },
  {
    id: "overview-restarts",
    title: "容器重启次数",
    unit: "count",
    labels: [],
    queries: [{ name: "cluster_container_restarts", requires: KUBE_STATE }],
  },
];

/* ── 数值呈现 ─────────────────────────────────────────────────────────── */

const COUNT_FORMAT = new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 });

function formatMillicores(value: number): string {
  if (Math.abs(value) >= 1000) {
    return `${(value / 1000).toFixed(2)} 核`;
  }
  return `${Math.round(value)} m`;
}

export function formatBytes(value: number): string {
  let scaled = value;
  let unit = 0;
  while (Math.abs(scaled) >= 1024 && unit < BYTE_UNITS.length - 1) {
    scaled /= 1024;
    unit += 1;
  }
  return `${scaled.toFixed(unit === 0 ? 0 : 1)} ${BYTE_UNITS[unit]}`;
}

const BYTE_UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"] as const;

/**
 * The scale one duration is read in.
 *
 * A disk answers in microseconds and a Node's uptime in weeks, and both arrive
 * from the Server in seconds. Choosing the scale from the value is what keeps
 * `0.000012 s` and `1209600 s` from both being written out in full.
 */
function secondScaleFor(value: number): { factor: number; suffix: string } {
  const magnitude = Math.abs(value);
  if (magnitude >= 86_400) return { factor: 86_400, suffix: " 天" };
  if (magnitude >= 3_600) return { factor: 3_600, suffix: " 小时" };
  if (magnitude >= 60) return { factor: 60, suffix: " 分钟" };
  if (magnitude >= 1) return { factor: 1, suffix: " s" };
  if (magnitude >= 1e-3) return { factor: 1e-3, suffix: " ms" };
  return { factor: 1e-6, suffix: " µs" };
}

export function formatSeconds(value: number): string {
  // Zero has no scale of its own, and picking one from the magnitude would
  // write it as `0.00 µs` — a precision the number does not have.
  if (value === 0) return "0 s";
  const { factor, suffix } = secondScaleFor(value);
  const scaled = value / factor;
  return `${scaled.toFixed(Math.abs(scaled) < 10 ? 2 : 1)}${suffix}`;
}

/**
 * Axis ticks, all in one unit.
 *
 * A tick formatter that scales each label on its own produces `500 m` above
 * `1.00 核` on the same axis, and `0 B` under `5.6 GiB` — technically correct
 * and unreadable as a scale. The unit is chosen once from the largest tick and
 * every label is written in it.
 */
export function axisFormatterFor(unit: MetricUnit): (splits: number[]) => string[] {
  if (unit === "millicores") {
    return (splits) => {
      const peak = Math.max(...splits.map(Math.abs), 0);
      if (peak < 1000) {
        return splits.map((value) => `${Math.round(value)} m`);
      }
      const decimals = peak < 10_000 ? 1 : 0;
      return splits.map((value) => `${(value / 1000).toFixed(decimals)} 核`);
    };
  }
  if (unit === "bytes" || unit === "bytes_per_second") {
    const suffix = unit === "bytes" ? "" : "/s";
    return (splits) => {
      const peak = Math.max(...splits.map(Math.abs), 0);
      let scale = 0;
      while (peak / 1024 ** (scale + 1) >= 1 && scale < BYTE_UNITS.length - 1) {
        scale += 1;
      }
      const divisor = 1024 ** scale;
      // Decimals from the largest tick rather than from each one, so the
      // column reads as one scale: `0 GiB` beside `1.9 GiB` looks like two.
      const decimals = scale === 0 ? 0 : peak / divisor < 10 ? 1 : 0;
      return splits.map(
        (value) => `${(value / divisor).toFixed(decimals)} ${BYTE_UNITS[scale]}${suffix}`,
      );
    };
  }
  if (unit === "seconds") {
    return (splits) => {
      const peak = Math.max(...splits.map(Math.abs), 0);
      const { factor, suffix } = secondScaleFor(peak);
      const decimals = peak / factor < 10 ? 2 : 1;
      return splits.map((value) => `${(value / factor).toFixed(decimals)}${suffix}`);
    };
  }
  const format = formatterFor(unit);
  return (splits) => splits.map(format);
}

export function formatterFor(unit: MetricUnit): (value: number) => string {
  switch (unit) {
    case "bytes":
      return formatBytes;
    case "bytes_per_second":
      return (value) => `${formatBytes(value)}/s`;
    case "ops_per_second":
      return (value) => `${COUNT_FORMAT.format(value)} /s`;
    case "seconds":
      return formatSeconds;
    case "ratio":
      return (value) => `${(value * 100).toFixed(1)}%`;
    case "count":
      return (value) => COUNT_FORMAT.format(value);
    default:
      return formatMillicores;
  }
}

/**
 * The Server's label values, in the operator's words.
 *
 * Only for the labels whose values are a fixed vocabulary: a phase, a node
 * condition and a readiness status arrive as English enum values that a chart
 * legend would otherwise show untranslated beside Chinese titles. Applied per
 * label rather than to every value, because Namespaces, Pods and Nodes are
 * named by whoever created them — one called `Unknown` must keep its name.
 */
const TRANSLATED_LABELS = new Set(["phase", "status", "condition", "mode"]);

const LABEL_VALUES: Record<string, string> = {
  Running: "运行中",
  Pending: "等待中",
  Succeeded: "已完成",
  Failed: "失败",
  Unknown: "未知",
  Bound: "已绑定",
  Available: "可用",
  Released: "已释放",
  Lost: "已丢失",
  user: "用户态",
  system: "内核态",
  nice: "低优先级",
  iowait: "I/O 等待",
  steal: "被抢占",
  irq: "硬中断",
  softirq: "软中断",
  MemoryPressure: "内存压力",
  DiskPressure: "磁盘压力",
  PIDPressure: "PID 压力",
  true: "就绪",
  false: "未就绪",
  unknown: "状态未知",
};

export function displayLabelValue(label: string, value: string): string {
  return TRANSLATED_LABELS.has(label) ? (LABEL_VALUES[value] ?? value) : value;
}
