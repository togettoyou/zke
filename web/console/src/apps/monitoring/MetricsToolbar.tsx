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
import { REFRESH_CHOICES, useMetricsScope } from "./metrics-scope";
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
      {/* The target Cluster, and the only scope in this application. Charts
          are read one Cluster at a time, the way the container service is
          operated one Cluster at a time: two Clusters summed into one curve is
          a number that exists nowhere, and two drawn on shared axes are two
          questions in one picture.

          Labelled the way the container service labels it. A bare select in a
          toolbar leaves the reader to infer what it selects, and the two
          applications selecting the same thing should say so with the same
          words. */}
      <span className="text-muted-foreground text-xs">目标集群</span>
      <Select value={clusterId} onValueChange={setClusterId} disabled={clusters.length === 0}>
        <SelectTrigger className="h-8 w-[13rem] text-[13px]" aria-label="目标集群">
          <SelectValue placeholder="选择集群" />
        </SelectTrigger>
        <SelectContent>
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
