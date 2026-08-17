import { createContext, useContext } from "react";

import type { useMetricsQuery } from "@/api/queries/observability";

import type { TimeRange, TimeWindow } from "./time-range";

export const ALL_CLUSTERS = "__all__";

/**
 * What the Server accepts as a Namespace, checked here so a typo does not
 * become a 400 the operator has to read as an error banner.
 */
export const NAMESPACE_PATTERN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

export const DEFAULT_RANGE: TimeRange = { kind: "relative", seconds: 60 * 60 };

export const REFRESH_CHOICES: readonly { value: number | null; label: string }[] = [
  { value: null, label: "不自动刷新" },
  { value: 30, label: "每 30 秒" },
  { value: 60, label: "每 1 分钟" },
  { value: 300, label: "每 5 分钟" },
];

export type MetricsCluster = { id: string; name: string };

export type MetricsScopeValue = {
  clusters: MetricsCluster[];
  clusterId: string;
  setClusterId: (value: string) => void;
  /** undefined means every Cluster the caller may read. */
  clusterIds: string[] | undefined;
  namespace: string;
  setNamespace: (value: string) => void;
  top: number;
  setTop: (value: number) => void;
  range: TimeRange;
  setRange: (range: TimeRange) => void;
  /** Turns a drag across a chart into the window every chart then draws. */
  selectRange: (startMs: number, endMs: number) => void;
  zoomOutRange: () => void;
  window: TimeWindow;
  refreshSeconds: number | null;
  setRefreshSeconds: (value: number | null) => void;
  refresh: () => void;
  live: boolean;
};

export const MetricsScopeContext = createContext<MetricsScopeValue | null>(null);

export function useMetricsScope(): MetricsScopeValue {
  const value = useContext(MetricsScopeContext);
  if (!value) {
    throw new Error("useMetricsScope must be used inside MetricsScopeProvider");
  }
  return value;
}

/**
 * The Cluster inventory, shared so the gate and the toolbar read one answer.
 * It is `collection_health`: an instant query over every visible Cluster, so it
 * names exactly the Clusters that have sent anything.
 */
export type MetricsHealthQuery = ReturnType<typeof useMetricsQuery>;

export const MetricsHealthContext = createContext<MetricsHealthQuery | null>(null);

export function useMetricsHealth(): MetricsHealthQuery | null {
  return useContext(MetricsHealthContext);
}
