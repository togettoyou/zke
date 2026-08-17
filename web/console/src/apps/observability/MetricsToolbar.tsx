import { RotateCw, ZoomOut } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

import { TimeRangePicker } from "./TimeRangePicker";
import { ALL_CLUSTERS, REFRESH_CHOICES, useMetricsScope } from "./metrics-scope";
import { MAX_RANGE_SECONDS, rangeSeconds } from "./time-range";

/**
 * One row of filters above every chart in the application.
 *
 * Scope first, then the window, then how often it moves — the order they are
 * reached for. They live in the shell's toolbar rather than inside a section so
 * that moving between 计算资源 and 存储与网络 keeps the question the operator
 * was asking: same Clusters, same hour.
 */
export function MetricsToolbar() {
  const {
    clusters,
    clusterId,
    setClusterId,
    range,
    setRange,
    zoomOutRange,
    refreshSeconds,
    setRefreshSeconds,
    refresh,
  } = useMetricsScope();

  return (
    <>
      <Select value={clusterId} onValueChange={setClusterId}>
        <SelectTrigger className="h-8 w-[13rem] text-[13px]" aria-label="集群范围">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL_CLUSTERS}>全部集群（{clusters.length}）</SelectItem>
          {clusters.map((cluster) => (
            <SelectItem key={cluster.id} value={cluster.id}>
              {cluster.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      <TimeRangePicker range={range} onChange={setRange} />

      <Button
        size="icon-sm"
        variant="ghost"
        aria-label="扩大时间范围"
        title="扩大时间范围"
        disabled={rangeSeconds(range) >= MAX_RANGE_SECONDS}
        onClick={zoomOutRange}
      >
        <ZoomOut />
      </Button>

      <Select
        value={refreshSeconds === null ? "off" : String(refreshSeconds)}
        onValueChange={(value) => setRefreshSeconds(value === "off" ? null : Number(value))}
      >
        <SelectTrigger className="h-8 w-[9.5rem] text-[13px]" aria-label="自动刷新间隔">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {REFRESH_CHOICES.map((choice) => (
            <SelectItem
              key={choice.label}
              value={choice.value === null ? "off" : String(choice.value)}
            >
              {choice.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      <Button
        size="icon-sm"
        variant="ghost"
        aria-label="立即刷新"
        title="立即刷新"
        onClick={refresh}
      >
        <RotateCw />
      </Button>
    </>
  );
}
