import type { MetricsExploreOutcome, MetricsQuerySeries } from "@/api/types";

import type { LegendSeries } from "../ChartLegend";
import { summarise } from "../series-stats";

/**
 * A row of the table view: one series with its labels and its summary numbers.
 *
 * The labels stay a map rather than a rendered string because the table turns
 * them into columns, and which columns exist depends on what the operator
 * grouped by.
 */
export type ExploreSeriesRow = {
  id: string;
  reference: string;
  labels: Record<string, string>;
  values: (number | null)[];
  stats: ReturnType<typeof summarise>;
  /**
   * The two the shared legend summary does not carry. A table is read across a
   * row rather than across time, so the spread matters there in a way it does
   * not in a legend.
   */
  minimum: number | null;
  median: number | null;
};

/**
 * The timestamps every curve is drawn against.
 *
 * The Server answers every expression in one request on one grid, so any
 * non-empty series has the full column. The longest is taken rather than the
 * first because an expression can legitimately return nothing, and an empty
 * first row would otherwise collapse the axis for the ones below it.
 */
export function chartTimestamps(outcomes: MetricsExploreOutcome[]): number[] {
  let timestamps: number[] = [];
  for (const outcome of outcomes) {
    for (const series of outcome.series) {
      if (series.points.length > timestamps.length) {
        timestamps = series.points.map((point) => Number(point[0]));
      }
    }
  }
  return timestamps;
}

/**
 * What a curve is called.
 *
 * The reference comes first because it is what ties the curve to the expression
 * that produced it, and with several expressions on shared axes that is the
 * first thing a reader needs. `__name__` is promoted out of the brace list for
 * the same reason a metric name is written before its matchers.
 */
export function seriesLabel(reference: string, labels: Record<string, string>): string {
  const { __name__: name, ...rest } = labels;
  const pairs = Object.keys(rest)
    .sort()
    .map((key) => `${key}="${rest[key] ?? ""}"`);
  const body = pairs.length > 0 ? `{${pairs.join(", ")}}` : "";
  if (!name && !body) {
    return reference;
  }
  return `${reference} · ${name ?? ""}${body}`;
}

/** A stable identity for a series, so hiding one survives a re-run. */
function seriesId(reference: string, labels: Record<string, string>): string {
  return `${reference}:${Object.keys(labels)
    .sort()
    .map((key) => `${key}=${labels[key] ?? ""}`)
    .join(",")}`;
}

function alignValues(series: MetricsQuerySeries, timestamps: number[]): (number | null)[] {
  const values = series.points.map((point) => (point[1] === null ? null : Number(point[1])));
  if (values.length === timestamps.length) {
    return values;
  }
  // Padded rather than trusted: uPlot reads the columns positionally, and a
  // short one would silently shift a curve in time.
  const byTimestamp = new Map<number, number | null>();
  series.points.forEach((point, index) => {
    byTimestamp.set(Number(point[0]), values[index] ?? null);
  });
  return timestamps.map((at) => byTimestamp.get(at) ?? null);
}

function minimumOf(values: (number | null)[]): number | null {
  let minimum: number | null = null;
  for (const value of values) {
    if (value === null || !Number.isFinite(value)) {
      continue;
    }
    minimum = minimum === null ? value : Math.min(minimum, value);
  }
  return minimum;
}

function medianOf(values: (number | null)[]): number | null {
  const present = values.filter(
    (value): value is number => value !== null && Number.isFinite(value),
  );
  if (present.length === 0) {
    return null;
  }
  present.sort((first, second) => first - second);
  const middle = Math.floor(present.length / 2);
  if (present.length % 2 === 1) {
    return present[middle] ?? null;
  }
  const low = present[middle - 1];
  const high = present[middle];
  return low === undefined || high === undefined ? null : (low + high) / 2;
}

/** Every successful expression's series, flattened onto one time grid. */
export function exploreSeriesRows(
  outcomes: MetricsExploreOutcome[],
  timestamps: number[],
): ExploreSeriesRow[] {
  const rows: ExploreSeriesRow[] = [];
  for (const outcome of outcomes) {
    for (const series of outcome.series) {
      const values = alignValues(series, timestamps);
      rows.push({
        id: seriesId(outcome.ref_id, series.labels),
        reference: outcome.ref_id,
        labels: series.labels,
        values,
        stats: summarise(values),
        minimum: minimumOf(values),
        median: medianOf(values),
      });
    }
  }
  return rows;
}

export function toLegendSeries(rows: ExploreSeriesRow[]): LegendSeries[] {
  return rows.map((row) => ({
    id: row.id,
    label: seriesLabel(row.reference, row.labels),
    values: row.values,
    stats: row.stats,
  }));
}

/**
 * Numbers, for an expression whose unit nobody knows.
 *
 * The catalogue knows what each of its queries counts in and formats
 * accordingly — bytes as MiB, ratios as percentages, seconds as durations.
 * Here the Server cannot know: `node_memory_working_set_bytes` and
 * `kube_pod_container_status_restarts_total` arrive through the same route, and
 * guessing a unit from a metric name would be wrong exactly where it mattered.
 *
 * So the value is shown as itself. `2,061,271,040` rather than `20.61亿`: the
 * grouped digits are the number the expression returned, they can be compared
 * with what the same expression prints anywhere else, and they do not silently
 * impose a scale — a locale-specific 亿 is not a unit the metric has, and it
 * reads as one.
 */
const EXACT = new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 6 });

export function formatExploreValue(value: number): string {
  if (Number.isNaN(value)) {
    return "NaN";
  }
  if (!Number.isFinite(value)) {
    return value > 0 ? "+Inf" : "-Inf";
  }
  // Below what six decimals can show, exponent notation says "very small"
  // where the grouped form would say "0".
  if (value !== 0 && Math.abs(value) < 1e-6) {
    return value.toExponential(2);
  }
  return EXACT.format(value);
}

/**
 * The SI prefixes an axis tick may carry.
 *
 * Unit-neutral on purpose. An axis has room for four or five characters, and
 * `2G` next to a legend reading `2,061,271,040` is a scale the reader already
 * knows how to undo — where `20亿` is a different number system standing in for
 * one the metric never used.
 *
 * Only upwards. `0.5` is a perfectly ordinary value for a ratio, and writing it
 * `500m` would be correct SI and completely unhelpful: below one, decimals say
 * the same thing without asking anybody to undo a prefix.
 */
const SI_PREFIXES = [
  { factor: 1e12, symbol: "T" },
  { factor: 1e9, symbol: "G" },
  { factor: 1e6, symbol: "M" },
  { factor: 1e3, symbol: "k" },
] as const;

/**
 * Formats a whole set of axis ticks against one shared scale.
 *
 * Per-tick scaling would put `900M` directly below `1G`, which is the same
 * number twice in two units. The scale comes from the largest tick, so every
 * label on the axis is read the same way.
 */
export function formatExploreAxis(splits: number[]): string[] {
  const finite = splits.filter((value) => Number.isFinite(value)).map(Math.abs);
  const peak = finite.length > 0 ? Math.max(...finite) : 0;
  const scale = SI_PREFIXES.find((prefix) => peak >= prefix.factor);
  const factor = scale?.factor ?? 1;
  const symbol = scale?.symbol ?? "";
  const scaled = splits.map((value) => value / factor);
  // Decimals from the axis as a whole rather than from each tick, so the ticks
  // line up on their decimal point instead of stepping in and out.
  const largest = peak / factor;
  const decimals = decimalsFor(largest);
  return scaled.map((value) => {
    if (!Number.isFinite(value)) {
      return "";
    }
    if (value === 0) {
      return "0";
    }
    // Past what the chosen precision can show, the tick would read `0` beside
    // another `0` a pixel away.
    if (Math.abs(value) < 10 ** -decimals) {
      return value.toExponential(1);
    }
    return trimZeros(value.toFixed(decimals)) + symbol;
  });
}

function decimalsFor(largest: number): number {
  if (largest >= 100) return 0;
  if (largest >= 10) return 1;
  if (largest >= 1) return 2;
  if (largest >= 0.01) return 4;
  return 6;
}

function trimZeros(text: string): string {
  return text.includes(".") ? text.replace(/\.?0+$/, "") : text;
}
