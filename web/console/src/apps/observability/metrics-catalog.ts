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
          description:
            "可运行与不可中断的进程数，与该节点的核数画在一起：负载 8 在 64 核节点上是空闲，在 2 核节点上是排队。",
          unit: "count",
          labels: ["node"],
          queries: [
            { name: "node_load1", label: "负载", requires: NODE_EXPORTER },
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
