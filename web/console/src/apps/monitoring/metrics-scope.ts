import { createContext, useContext } from "react";

import type { useMetricsQuery } from "@/api/queries/observability";

import type { TimeRange, TimeWindow } from "./time-range";

/**
 * The Namespace filter's "no filter" choice.
 *
 * A sentinel rather than the empty string because a Radix select item cannot
 * carry an empty value — it reserves that for "nothing is selected", which is a
 * different state from "every Namespace".
 */
export const ALL_NAMESPACES = "__all__";

export const DEFAULT_RANGE: TimeRange = { kind: "relative", seconds: 60 * 60 };

export const REFRESH_CHOICES: readonly { value: number | null; label: string }[] = [
  { value: null, label: "不自动刷新" },
  { value: 30, label: "每 30 秒" },
  { value: 60, label: "每 1 分钟" },
  { value: 300, label: "每 5 分钟" },
];

export type MetricsCluster = { id: string; name: string };

export type MetricsScopeValue = {
  /** The Clusters of the currently selected Project, in listing order. */
  clusters: MetricsCluster[];
  /**
   * The Cluster every chart in the application describes. Empty only before the
   * Cluster list has loaded, or when the Project has none.
   *
   * One Cluster rather than a set, for the same reason the container service is
   * operated one Cluster at a time: adding two Clusters' curves together
   * produces a number that exists nowhere, and drawing them side by side on
   * shared axes puts two questions in one picture. Comparing Clusters is a
   * different feature from watching one.
   */
  clusterId: string;
  setClusterId: (value: string) => void;
  namespace: string;
  setNamespace: (value: string) => void;
  top: number;
  setTop: (value: number) => void;
  range: TimeRange;
  setRange: (range: TimeRange) => void;
  /** Turns a drag across a chart into the window every chart then draws. */
  selectRange: (startMs: number, endMs: number) => void;
  zoomOutRange: () => void;
  /**
   * Identifies the window without its position, so a chart keeps one cache
   * entry while the clock moves under it.
   *
   * The resolved window itself is deliberately not on this value. Nothing reads
   * it, and putting an object that is rebuilt on every tick of the clock into
   * the context would re-render every consumer once a minute to hand them
   * numbers they never look at.
   */
  windowKey: string;
  /**
   * Reads the window a request should ask for, at the moment it asks. Shared by
   * every chart, so panels that issue their own requests still describe the
   * same window.
   */
  readWindow: () => TimeWindow;
  refreshSeconds: number | null;
  setRefreshSeconds: (value: number | null) => void;
  refresh: () => void;
  /**
   * Increments every time the window moves — a new range, the refresh button,
   * a tick of the auto-refresh interval.
   *
   * The chart panels do not need it: they are cached queries, and moving the
   * window invalidates them. Explore is not a cached query — it is a mutation
   * the operator triggers — so without a signal it would go on showing the
   * answer to a window that is no longer on screen while every panel beside it
   * had moved on.
   */
  refreshToken: number;
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
 * Whether the selected Cluster has reported anything at all.
 *
 * `collection_health` reads the collector's own scrape results, so an empty
 * answer means this Cluster has sent nothing — which is a different screen from
 * a Cluster that is reporting but idle, and the reason the gate exists.
 */
export type MetricsHealthQuery = ReturnType<typeof useMetricsQuery>;

export const MetricsHealthContext = createContext<MetricsHealthQuery | null>(null);

export function useMetricsHealth(): MetricsHealthQuery | null {
  return useContext(MetricsHealthContext);
}
