import { useEffect, useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";

import { useNodeMetrics, usePodMetrics } from "@/api/queries/metrics";
import { SectionToolbarActions } from "@/apps/AppShell";
import { DataTable } from "@/components/common/data-table";
import { RefreshAction } from "@/components/common/refresh-action";
import { Alert } from "@/components/ui/misc";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatAbsolute } from "@/lib/time";

type MetricsTab = "nodes" | "pods";

type UsageMetric = {
  name: string;
  timestamp: string;
  window_seconds: number;
  cpu_usage_millis: number;
  memory_usage_bytes: number;
  container_count?: number;
};

type ResourceUsageSectionProps = {
  clusterId: string;
  namespace: string;
  onNamespaceScopeChange: (namespaced: boolean) => void;
};

/** A typed, scoped view of the same Metrics API used by `kubectl top`. */
export function ResourceUsageSection({
  clusterId,
  namespace,
  onNamespaceScopeChange,
}: ResourceUsageSectionProps) {
  const [tab, setTab] = useState<MetricsTab>("nodes");
  const podTab = tab === "pods";
  const nodes = useNodeMetrics(podTab ? null : clusterId);
  const pods = usePodMetrics(podTab ? clusterId : null, podTab ? namespace : null);
  const query = podTab ? pods : nodes;
  const snapshot = query.data;

  useEffect(() => onNamespaceScopeChange(podTab), [onNamespaceScopeChange, podTab]);

  const columns = useMemo<ColumnDef<UsageMetric, unknown>[]>(
    () => [
      {
        header: podTab ? "Pod" : "节点",
        cell: ({ row }) => <span className="text-foreground font-medium">{row.original.name}</span>,
      },
      {
        header: "CPU",
        size: 150,
        cell: ({ row }) => (
          <span className="zke-tnum text-muted-foreground">
            {formatCPU(row.original.cpu_usage_millis)}
          </span>
        ),
      },
      {
        header: "内存",
        size: 150,
        cell: ({ row }) => (
          <span className="zke-tnum text-muted-foreground">
            {formatBytes(row.original.memory_usage_bytes)}
          </span>
        ),
      },
      ...(podTab
        ? [
            {
              header: "容器",
              size: 100,
              cell: ({ row }: { row: { original: UsageMetric } }) => (
                <span className="zke-tnum text-muted-foreground">
                  {row.original.container_count}
                </span>
              ),
            },
          ]
        : []),
      {
        header: "采样时间",
        size: 190,
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-muted-foreground text-xs">
              {formatAbsolute(row.original.timestamp)}
            </span>
            <span className="text-subtle-foreground text-xs">
              {row.original.window_seconds} 秒窗口
            </span>
          </div>
        ),
      },
    ],
    [podTab],
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <SectionToolbarActions>
        {snapshot ? (
          <span className="text-subtle-foreground text-xs">
            获取时间 {formatAbsolute(snapshot.generated_at)}
          </span>
        ) : null}
        {podTab && !namespace ? null : (
          <RefreshAction isFetching={query.isFetching} onRefresh={() => void query.refetch()} />
        )}
      </SectionToolbarActions>
      <Tabs
        value={tab}
        onValueChange={(value) => setTab(value as MetricsTab)}
        className="flex min-h-0 flex-1 flex-col"
      >
        <TabsList className="w-fit">
          <TabsTrigger value="nodes">节点</TabsTrigger>
          <TabsTrigger value="pods">Pod</TabsTrigger>
        </TabsList>
        <TabsContent value={tab} className="flex min-h-0 flex-1 flex-col">
          {podTab && !namespace ? (
            <Alert tone="info" className="mt-1">
              Pod 资源用量按命名空间定域，正在等待工具栏解析出可用的命名空间。
            </Alert>
          ) : snapshot && !snapshot.available ? (
            <Alert tone="warning" className="mt-1">
              <span className="font-medium">实时资源用量不可用。</span>
              <span className="ml-1">{snapshot.message}</span>
            </Alert>
          ) : (
            <DataTable
              columns={columns}
              data={snapshot?.items}
              isLoading={query.isLoading}
              isFetching={query.isFetching}
              error={query.error}
              onRetry={() => void query.refetch()}
              rowKey={(item) => item.name}
              emptyTitle={podTab ? "该命名空间暂无 Pod 指标" : "该集群暂无节点指标"}
              emptyDescription={
                podTab
                  ? "Metrics Server 尚未采集到当前命名空间中 Pod 的资源用量。"
                  : "Metrics Server 尚未采集到节点资源用量。"
              }
            />
          )}
        </TabsContent>
      </Tabs>
    </div>
  );
}

function formatCPU(millis: number): string {
  if (millis < 1_000) {
    return `${millis}m`;
  }
  const cores = millis / 1_000;
  return `${cores.toFixed(millis % 1_000 === 0 ? 0 : 2)} core`;
}

function formatBytes(bytes: number): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}
