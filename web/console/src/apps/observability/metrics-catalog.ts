/**
 * What the observability application draws, and how each number reads.
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
  "millicores" | "bytes" | "bytes_per_second" | "ops_per_second" | "ratio" | "count";

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
          description: "容器被允许使用的上限之和。超过可分配量意味着节点在压力下会开始限流。",
          unit: "millicores",
          labels: [],
          queries: [{ name: "cluster_cpu_limits", requires: KUBE_STATE }],
        },
        {
          id: "cluster-memory-limits",
          title: "内存限制量",
          unit: "bytes",
          labels: [],
          queries: [{ name: "cluster_memory_limits", requires: KUBE_STATE }],
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
          title: "1 分钟负载",
          description: "可运行与不可中断的进程数。与该节点的核数比较才有意义。",
          unit: "count",
          labels: ["node"],
          queries: [{ name: "node_load1", requires: NODE_EXPORTER }],
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
          id: "node-memory-available",
          title: "可用内存",
          description: "内核估算的可分配内存，包含可回收的页缓存，因此不等于空闲内存。",
          unit: "bytes",
          labels: ["node"],
          queries: [{ name: "node_memory_available", requires: NODE_EXPORTER }],
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
];

/* ── 存储与网络 ───────────────────────────────────────────────────────── */

export const STORAGE_VIEWS: MetricsViews = [
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
        id: "network-errors",
        title: "错误与丢包",
        description: "收发两个方向的错误与丢包合计。持续高于零的接口就是需要处理的那一个。",
        unit: "ops_per_second",
        labels: ["node", "device"],
        queries: [{ name: "node_network_errors", requires: NODE_EXPORTER }],
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
        unit: "count",
        labels: ["phase"],
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
    ],
  },
];

/* ── 总览 ─────────────────────────────────────────────────────────────── */

export const OVERVIEW_PANELS: readonly Panel[] = [
  {
    id: "overview-cpu",
    title: "CPU 利用率与申请占比",
    unit: "ratio",
    labels: [],
    fullScale: true,
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
const TRANSLATED_LABELS = new Set(["phase", "status", "condition"]);

const LABEL_VALUES: Record<string, string> = {
  Running: "运行中",
  Pending: "等待中",
  Succeeded: "已完成",
  Failed: "失败",
  Unknown: "未知",
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
