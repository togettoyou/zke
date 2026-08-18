/**
 * The window every chart in the observability application draws.
 *
 * One model for both ways of asking: a relative range that follows the clock,
 * and an absolute one that does not. Dragging across a chart produces the
 * second kind, which is why it has to exist at all — a selection that kept
 * following the clock would slide out from under the thing the operator just
 * pointed at.
 */
export type TimeRange =
  { kind: "relative"; seconds: number } | { kind: "absolute"; startMs: number; endMs: number };

/** A resolved window, in the shape a request needs. */
export type TimeWindow = {
  startMs: number;
  endMs: number;
  stepSeconds: number;
};

/**
 * The Server's own limits, restated here so the picker can refuse a range
 * before it becomes a 400 the operator has to read as an error banner. Changing
 * either of them without changing `metricsquery.Config` moves the failure back
 * into the response.
 */
export const MAX_RANGE_SECONDS = 7 * 24 * 60 * 60;
/** Narrower than this is a mis-drag, not a range anybody meant to select. */
export const MIN_RANGE_SECONDS = 60;

export const RELATIVE_PRESETS: readonly { seconds: number; label: string }[] = [
  { seconds: 15 * 60, label: "最近 15 分钟" },
  { seconds: 60 * 60, label: "最近 1 小时" },
  { seconds: 3 * 60 * 60, label: "最近 3 小时" },
  { seconds: 6 * 60 * 60, label: "最近 6 小时" },
  { seconds: 12 * 60 * 60, label: "最近 12 小时" },
  { seconds: 24 * 60 * 60, label: "最近 24 小时" },
  { seconds: 2 * 24 * 60 * 60, label: "最近 2 天" },
  { seconds: 7 * 24 * 60 * 60, label: "最近 7 天" },
];

/**
 * The steps a request may use.
 *
 * A ladder rather than `span / points`: the step decides the query key, and an
 * arbitrary second count would produce a different key — and therefore a
 * different cache entry and a different request — for every pixel of window
 * width. The rungs are also the durations a reader recognises on an axis. The
 * first one is the Server's minimum step; anything below it is refused.
 */
const STEP_LADDER = [
  15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 14400, 21600, 43200, 86400,
] as const;

/**
 * How many points a chart aims for.
 *
 * Well under the Server's ceiling of 1500, and under the pixel width of any
 * panel the Console draws: more points than pixels is a query the whole
 * deployment pays for and nobody can see.
 */
const TARGET_POINTS = 360;

const LONGEST_STEP = STEP_LADDER[STEP_LADDER.length - 1] ?? 86400;

export function stepFor(spanSeconds: number): number {
  for (const step of STEP_LADDER) {
    if (spanSeconds / step <= TARGET_POINTS) {
      return step;
    }
  }
  return LONGEST_STEP;
}

/**
 * Places a range on the step grid.
 *
 * The end is floored to the step so a relative window only moves when a new
 * point could exist. Everything downstream is keyed on this window, so an
 * unaligned clock would rewrite every query key every second and turn the cache
 * into a miss.
 */
export function resolveWindow(range: TimeRange, nowMs: number): TimeWindow {
  if (range.kind === "absolute") {
    const spanSeconds = Math.max(
      MIN_RANGE_SECONDS,
      Math.round((range.endMs - range.startMs) / 1000),
    );
    const stepSeconds = stepFor(spanSeconds);
    const stepMs = stepSeconds * 1000;
    const endMs = Math.floor(range.endMs / stepMs) * stepMs;
    return { startMs: endMs - spanSeconds * 1000, endMs, stepSeconds };
  }
  const stepSeconds = stepFor(range.seconds);
  const stepMs = stepSeconds * 1000;
  const endMs = Math.floor(nowMs / stepMs) * stepMs;
  return { startMs: endMs - range.seconds * 1000, endMs, stepSeconds };
}

/**
 * Names a window without pinning it in time.
 *
 * "The last hour at a one minute step" is one question whose answer moves;
 * putting the moving endpoints in a cache key made every tick of the clock a
 * different question, which is how a refresh became a reload. Two windows share
 * a key exactly when they are the same question.
 */
export function windowKeyFor(range: TimeRange, window: TimeWindow): string {
  return range.kind === "relative"
    ? `relative:${range.seconds}:${window.stepSeconds}`
    : `absolute:${range.startMs}:${range.endMs}:${window.stepSeconds}`;
}

export function rangeSeconds(range: TimeRange): number {
  return range.kind === "relative"
    ? range.seconds
    : Math.round((range.endMs - range.startMs) / 1000);
}

/**
 * Clamps a selection to something the Server will answer.
 *
 * Returns null for a drag too narrow to be a range, so a stray click on a chart
 * does not throw the whole view onto a one-second window.
 */
export function selectionToRange(startMs: number, endMs: number): TimeRange | null {
  const [low, high] = startMs <= endMs ? [startMs, endMs] : [endMs, startMs];
  const spanSeconds = Math.round((high - low) / 1000);
  if (spanSeconds < MIN_RANGE_SECONDS) {
    return null;
  }
  return {
    kind: "absolute",
    startMs: low,
    endMs: Math.min(high, low + MAX_RANGE_SECONDS * 1000),
  };
}

/** Widens a range around its own centre, for the way back out of a zoom. */
export function zoomOut(range: TimeRange, nowMs: number): TimeRange {
  const span = rangeSeconds(range);
  const widened = Math.min(span * 4, MAX_RANGE_SECONDS);
  if (range.kind === "relative") {
    return { kind: "relative", seconds: widened };
  }
  const centre = (range.startMs + range.endMs) / 2;
  const half = (widened * 1000) / 2;
  const endMs = Math.min(centre + half, nowMs);
  return { kind: "absolute", startMs: endMs - widened * 1000, endMs };
}

export function formatDuration(seconds: number): string {
  if (seconds % (24 * 60 * 60) === 0) {
    return `${seconds / (24 * 60 * 60)} 天`;
  }
  if (seconds % (60 * 60) === 0) {
    return `${seconds / (60 * 60)} 小时`;
  }
  if (seconds % 60 === 0) {
    return `${seconds / 60} 分钟`;
  }
  return `${seconds} 秒`;
}

const ABSOLUTE_FORMAT = new Intl.DateTimeFormat("zh-CN", {
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});

export function formatRange(range: TimeRange): string {
  if (range.kind === "relative") {
    const preset = RELATIVE_PRESETS.find((item) => item.seconds === range.seconds);
    return preset ? preset.label : `最近 ${formatDuration(range.seconds)}`;
  }
  return `${ABSOLUTE_FORMAT.format(range.startMs)} — ${ABSOLUTE_FORMAT.format(range.endMs)}`;
}

/**
 * `datetime-local` reads and writes wall-clock text with no zone, so the value
 * has to be built from local parts rather than from `toISOString`, which is
 * UTC and would shift the field by the operator's offset.
 */
export function toLocalInput(ms: number): string {
  const at = new Date(ms);
  const pad = (value: number) => String(value).padStart(2, "0");
  return (
    `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}` +
    `T${pad(at.getHours())}:${pad(at.getMinutes())}`
  );
}

export function fromLocalInput(value: string): number | null {
  const parsed = new Date(value);
  const ms = parsed.getTime();
  return Number.isFinite(ms) ? ms : null;
}
