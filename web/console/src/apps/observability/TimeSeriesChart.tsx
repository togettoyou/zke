import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import "./uplot-theme.css";

import { seriesColor, seriesDash, type ChartPalette } from "./chart-palette";

export type ChartSeries = {
  id: string;
  label: string;
  /** Same length as `timestamps`; `null` is a gap, never a zero. */
  values: (number | null)[];
};

export type TimeSeriesChartProps = {
  /** Unix seconds, ascending, shared by every series. */
  timestamps: number[];
  series: ChartSeries[];
  palette: ChartPalette;
  formatValue: (value: number) => string;
  /**
   * Formats the whole set of axis ticks at once, so they can share one unit.
   * Defaults to formatting each tick on its own.
   */
  formatAxis?: (splits: number[]) => string[];
  ariaLabel: string;
  height?: number;
  /** Series the legend has switched off. */
  hidden?: ReadonlySet<string>;
  /** Called with the window a drag across the plot selected. */
  onSelectRange?: (startMs: number, endMs: number) => void;
  /**
   * Keeps the axis at full scale for a value that means something against 1.
   * A utilisation of 12% drawn against its own maximum looks identical to one
   * of 98%.
   */
  fullScale?: boolean;
  /**
   * Draws the series as a stack, each one resting on the sum of the ones below
   * it. Only for a panel whose series add up to something — a phase breakdown,
   * a set of Namespaces inside one Cluster — where the total is as much of the
   * answer as any single curve. Anything else is stacked nonsense: two
   * utilisations summed to 140% describe nothing.
   */
  stacked?: boolean;
  /**
   * A horizontal line the curves are read against — the capacity, the quota,
   * the limit. Drawn because the comparison is the point of the panel and the
   * reader would otherwise have to hold the number in their head.
   */
  reference?: { value: number; label: string };
  /** Charts sharing a key share a crosshair. */
  syncKey?: string;
};

/* Enough rows to cover a default Top N, and few enough that the readout still
   fits beside the chart it belongs to rather than hanging out of the panel. */
const TOOLTIP_ROWS = 8;

const TIME_OF_DAY = new Intl.DateTimeFormat("zh-CN", {
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});
const DAY_AND_TIME = new Intl.DateTimeFormat("zh-CN", {
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});
const FULL_TIME = new Intl.DateTimeFormat("zh-CN", {
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});

type PlotOrder = {
  /** Series index drawn at each plot position, in painting order. */
  order: number[];
  /** What uPlot draws, aligned with `order`. */
  columns: (number | null)[][];
  /** What the reader is told, indexed by series rather than by position. */
  values: (number | null)[][];
};

/**
 * Turns the series into what the canvas needs.
 *
 * Unstacked this is the identity. Stacked, each column becomes the running
 * total up to and including its own series, and the order is reversed: uPlot
 * paints in index order and every band is filled to the axis, so the total has
 * to go down first and the bottom band last for each one to end up showing its
 * own share.
 *
 * A hidden series contributes nothing to the totals rather than leaving a step
 * in them — switching a curve off in the legend has to remove it from the sum,
 * or the stack keeps describing a series that is no longer on the chart.
 *
 * A gap stays a gap. A null is not carried into the running total as a zero,
 * because a Cluster that stopped reporting did not report zero.
 */
function plotOrder(
  series: ChartSeries[],
  hidden: ReadonlySet<string> | undefined,
  stacked: boolean | undefined,
): PlotOrder {
  const values = series.map((item) => item.values);
  if (!stacked) {
    return { order: series.map((_, index) => index), columns: values, values };
  }
  const length = series[0]?.values.length ?? 0;
  const running = new Array<number>(length).fill(0);
  const cumulative = series.map((item) => {
    const off = hidden?.has(item.id) ?? false;
    return item.values.map((value, index) => {
      if (value === null) {
        return null;
      }
      if (!off) {
        running[index] = (running[index] ?? 0) + value;
      }
      return running[index] ?? 0;
    });
  });
  const order = series.map((_, index) => series.length - 1 - index);
  return { order, columns: order.map((source) => cumulative[source] ?? []), values };
}

/**
 * A window longer than a day needs the date on the axis; a shorter one does
 * not, and repeating it on every tick spends the width that keeps the labels
 * from colliding.
 */
function axisFormatter(spanSeconds: number): (value: number) => string {
  return spanSeconds > 24 * 60 * 60
    ? (value) => DAY_AND_TIME.format(value * 1000)
    : (value) => TIME_OF_DAY.format(value * 1000);
}

/**
 * The readout under the cursor.
 *
 * Written straight to the DOM rather than through React: it moves with every
 * pointer event, and a state update per frame would re-render the panel — and
 * the chart inside it — at pointer speed. Rows are pooled for the same reason.
 *
 * Labels arrive from Cluster, Node and Pod names, so they are placed with
 * `textContent`. There is no path here where markup from a Cluster could be
 * parsed.
 */
function tooltipPlugin(
  formatValue: (value: number) => string,
  /**
   * The value to show for a drawn series, which is not always the one on the
   * canvas: a stacked chart draws running totals, and a readout of those would
   * report every series as larger than it is.
   */
  valueAt: (seriesIndex: number, dataIndex: number) => number | null,
): uPlot.Plugin {
  let root: HTMLDivElement | null = null;
  let head: HTMLDivElement | null = null;
  let body: HTMLDivElement | null = null;
  const rows: {
    row: HTMLDivElement;
    key: HTMLSpanElement;
    label: HTMLSpanElement;
    value: HTMLSpanElement;
  }[] = [];
  let overflow: HTMLDivElement | null = null;

  const ensureRow = (index: number) => {
    let entry = rows[index];
    if (entry) {
      return entry;
    }
    const row = document.createElement("div");
    row.className = "flex items-center gap-2";
    const key = document.createElement("span");
    // A short stroke rather than a filled block: at this density a block is
    // data-weight ink doing a label's job.
    key.className = "h-0.5 w-3 shrink-0 rounded-full";
    const label = document.createElement("span");
    label.className = "text-muted-foreground min-w-0 flex-1 truncate";
    const value = document.createElement("span");
    value.className = "text-foreground zke-tnum shrink-0 font-medium";
    row.append(key, label, value);
    body?.append(row);
    entry = { row, key, label, value };
    rows[index] = entry;
    return entry;
  };

  return {
    hooks: {
      init: (plot) => {
        root = document.createElement("div");
        root.className =
          "border-border bg-surface shadow-e3 rounded-panel pointer-events-none absolute top-0 left-0 z-10 hidden min-w-44 max-w-72 border px-2.5 py-2 text-xs";
        head = document.createElement("div");
        head.className = "text-subtle-foreground mb-1.5 text-[11px]";
        body = document.createElement("div");
        body.className = "flex flex-col gap-1";
        overflow = document.createElement("div");
        overflow.className = "text-subtle-foreground mt-1 text-[11px]";
        root.append(head, body, overflow);
        plot.over.append(root);
      },
      setCursor: (plot) => {
        if (!root || !head || !body || !overflow) {
          return;
        }
        const index = plot.cursor.idx;
        const left = plot.cursor.left ?? -10;
        if (index == null || left < 0) {
          root.classList.add("hidden");
          return;
        }
        const at = plot.data[0]?.[index];
        if (at == null) {
          root.classList.add("hidden");
          return;
        }
        const entries: { label: string; value: number; color: string }[] = [];
        for (let seriesIndex = 1; seriesIndex < plot.series.length; seriesIndex += 1) {
          const series = plot.series[seriesIndex];
          const value = series?.show ? valueAt(seriesIndex, index) : null;
          if (!series || value == null) {
            continue;
          }
          entries.push({
            label: typeof series.label === "string" ? series.label : "",
            value,
            color: typeof series.stroke === "string" ? series.stroke : "",
          });
        }
        if (entries.length === 0) {
          root.classList.add("hidden");
          return;
        }
        // Largest first: the reader is looking for whichever curve is on top.
        entries.sort((first, second) => second.value - first.value);
        head.textContent = FULL_TIME.format((at as number) * 1000);
        const shown = Math.min(entries.length, TOOLTIP_ROWS);
        for (let position = 0; position < shown; position += 1) {
          const entry = entries[position];
          const row = ensureRow(position);
          if (!entry) {
            continue;
          }
          row.key.style.backgroundColor = entry.color;
          row.label.textContent = entry.label;
          row.value.textContent = formatValue(entry.value);
          row.row.classList.remove("hidden");
        }
        for (let position = shown; position < rows.length; position += 1) {
          rows[position]?.row.classList.add("hidden");
        }
        overflow.textContent =
          entries.length > shown ? `其余 ${entries.length - shown} 条已省略` : "";
        overflow.classList.toggle("hidden", entries.length <= shown);

        root.classList.remove("hidden");
        // Flipped to the other side of the cursor near the right edge, so the
        // readout never leaves the plot it belongs to.
        const width = root.offsetWidth;
        const height = root.offsetHeight;
        const flip = left + width + 16 > plot.over.clientWidth;
        const x = flip ? left - width - 12 : left + 12;
        const y = Math.min(
          Math.max((plot.cursor.top ?? 0) - height / 2, 0),
          Math.max(plot.over.clientHeight - height, 0),
        );
        root.style.transform = `translate(${Math.max(x, 0)}px, ${y}px)`;
      },
      destroy: () => {
        root?.remove();
        root = null;
        head = null;
        body = null;
        overflow = null;
        rows.length = 0;
      },
    },
  };
}

/**
 * A uPlot chart with its imperative lifetime contained.
 *
 * uPlot rather than SVG because a metrics view draws many Clusters over many
 * points, and that many DOM nodes slows down the window they live in — dragging
 * and scrolling included, which is the part an operator feels. Everything
 * imperative about it stays here: creation, `setData`, `setSize` and
 * destruction, so no view has to remember to tear an instance down.
 */
export function TimeSeriesChart({
  timestamps,
  series,
  palette,
  formatValue,
  formatAxis,
  ariaLabel,
  height = 200,
  hidden,
  onSelectRange,
  fullScale = false,
  stacked = false,
  reference,
  syncKey,
}: TimeSeriesChartProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const plotRef = useRef<uPlot | null>(null);
  const [width, setWidth] = useState(0);

  // Held in refs so a new callback identity does not rebuild the instance: the
  // panel passes fresh closures on every render, and rebuilding a chart
  // mid-drag would cancel the drag that is about to call it. Written in an
  // effect rather than during render, and declared before the effect that
  // builds the plot so the values are current by the time it reads them.
  const selectRef = useRef(onSelectRange);
  const formatRef = useRef(formatValue);
  const axisRef = useRef(formatAxis);
  useEffect(() => {
    selectRef.current = onSelectRange;
    formatRef.current = formatValue;
    axisRef.current = formatAxis;
  });

  // Observing our own container, not the window: the chart has to follow the
  // panel it sits in, which changes with window resizing, sidebar state and
  // section layout alike. A global resize listener would miss all but the first.
  //
  // Measured in a layout effect and read from the live rect, not from the
  // observer's first entry: a chart that remounts — which happens whenever the
  // window it draws changes — would otherwise wait for the observer's first
  // callback before it could be built, and the panel would blank for as long as
  // that took. Measuring before paint means the instance exists in the same
  // commit the container does.
  useLayoutEffect(() => {
    const container = containerRef.current;
    if (!container) {
      return;
    }
    const measure = () =>
      setWidth(Math.max(0, Math.floor(container.getBoundingClientRect().width)));
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(container);
    return () => observer.disconnect();
  }, []);

  // What is drawn, which is not always what was passed in. A stack is painted
  // from the total downwards — every band is filled to the axis, and the one
  // below has to be painted over the one above it — so the drawing order is the
  // reverse of the series order, and each column carries the running total up
  // to its own series rather than that series' own value.
  const plotted = useMemo(() => plotOrder(series, hidden, stacked), [series, hidden, stacked]);

  const data = useMemo<uPlot.AlignedData>(
    () => [timestamps, ...plotted.columns] as unknown as uPlot.AlignedData,
    [plotted, timestamps],
  );

  // The readout reads the original values through this rather than the canvas,
  // and it must not rebuild the instance when only the numbers change.
  const readoutRef = useRef(plotted);
  useEffect(() => {
    readoutRef.current = plotted;
  });

  // The instance is rebuilt when the series set, the palette or the size
  // changes, and only then. Those change what the chart *is*; new data for the
  // same series is a `setData` below, which is what keeps a poll from flashing
  // the chart.
  const seriesSignature = useMemo(
    () => series.map((item) => `${item.id} ${item.label}`).join(""),
    [series],
  );
  const paletteSignature = palette.series.join(",");
  const first = timestamps[0];
  const final = timestamps[timestamps.length - 1];
  const spanSeconds = first !== undefined && final !== undefined ? final - first : 3600;

  useEffect(() => {
    const container = containerRef.current;
    if (!container || width === 0) {
      return;
    }
    const formatAxisValues = (splits: number[]) =>
      axisRef.current ? axisRef.current(splits) : splits.map((value) => formatRef.current(value));
    const formatTime = axisFormatter(spanSeconds);
    const plot = new uPlot(
      {
        width,
        height,
        padding: [12, 10, 0, 0],
        legend: { show: false },
        cursor: {
          // `setScale: false` because the window is owned by the view, not by
          // this chart: a drag asks for a range, the view refetches at a step
          // that suits it, and every other chart moves with it. Zooming the
          // local scale instead would stretch the points already on screen and
          // leave the panel beside it describing a different window.
          drag: { x: true, y: false, setScale: false, dist: 6 },
          focus: { prox: 24 },
          points: { size: 6, width: 2 },
          ...(syncKey
            ? {
                sync: {
                  key: syncKey,
                  // Only the x position travels. The charts in a view have
                  // different series sets, so a synced series index would
                  // highlight an unrelated curve in the panel beside it.
                  setSeries: false,
                  scales: ["x", null] as [string, null],
                },
              }
            : {}),
        },
        scales: {
          x: { time: true },
          y: {
            range: (_plot, dataMin, dataMax) => {
              if (dataMin == null || dataMax == null) {
                return [0, 1];
              }
              // Anchored at zero: a memory curve that varies by 2% around 4 GiB
              // is a flat line, and drawing it as a mountain range invents a
              // volatility the Cluster does not have.
              const min = dataMin < 0 ? dataMin * 1.05 : 0;
              let max = fullScale ? Math.max(1, dataMax) : dataMax > 0 ? dataMax * 1.08 : 1;
              // A reference line off the top of the axis is a line the reader
              // never sees, which is worse than not drawing one: the panel then
              // says the curves are being compared against something and shows
              // no comparison.
              if (reference) {
                max = Math.max(max, reference.value * 1.05);
              }
              return [min, max];
            },
          },
        },
        axes: [
          {
            stroke: palette.axis,
            grid: { stroke: palette.grid, width: 1, dash: [3, 4] },
            ticks: { stroke: palette.grid, width: 1, size: 4 },
            font: "11px system-ui, sans-serif",
            values: (_plot, splits) => splits.map(formatTime),
          },
          {
            stroke: palette.axis,
            grid: { stroke: palette.grid, width: 1, dash: [3, 4] },
            ticks: { show: false },
            font: "11px system-ui, sans-serif",
            // Wide enough for a formatted byte count, which is the longest
            // label any unit here produces.
            size: 68,
            values: (_plot, splits) => formatAxisValues(splits),
          },
        ],
        series: [
          { label: "时间" },
          ...plotted.order.map((source) => {
            const item = series[source];
            const color = seriesColor(palette, source);
            return {
              label: item?.label ?? "",
              stroke: color,
              width: 1.75,
              dash: stacked ? undefined : seriesDash(source),
              show: !(item && hidden?.has(item.id)),
              // A gap must read as a gap: connecting across it would draw a
              // collection outage as a straight line between the samples on
              // either side.
              spanGaps: false,
              points: { show: false },
              // A stacked band is opaque on purpose. Every band is filled down
              // to the axis and painted over the one above it, so a translucent
              // fill would blend two bands into a third colour that belongs to
              // neither series.
              ...(stacked
                ? { fill: color }
                : // A single curve gets a wash under it — with nothing to
                  // occlude, the fill makes the shape readable at a glance. Two
                  // or more would hide each other.
                  series.length === 1
                  ? {
                      fill: (plot: uPlot) => {
                        const gradient = plot.ctx.createLinearGradient(
                          0,
                          plot.bbox.top,
                          0,
                          plot.bbox.top + plot.bbox.height,
                        );
                        gradient.addColorStop(0, `${color}38`);
                        gradient.addColorStop(1, `${color}00`);
                        return gradient;
                      },
                    }
                  : {}),
            };
          }),
        ],
        hooks: {
          // Drawn after the curves rather than under them: the line is what the
          // curves are judged against, and a series crossing it has to be seen
          // crossing it.
          draw: reference
            ? [
                (plot: uPlot) => {
                  const context = plot.ctx;
                  const y = Math.round(plot.valToPos(reference.value, "y", true)) + 0.5;
                  if (y < plot.bbox.top || y > plot.bbox.top + plot.bbox.height) {
                    return;
                  }
                  context.save();
                  context.strokeStyle = palette.axis;
                  context.lineWidth = 1;
                  context.setLineDash([5, 4]);
                  context.beginPath();
                  context.moveTo(plot.bbox.left, y);
                  context.lineTo(plot.bbox.left + plot.bbox.width, y);
                  context.stroke();
                  context.setLineDash([]);
                  context.fillStyle = palette.axis;
                  context.font = `${11 * devicePixelRatio}px system-ui, sans-serif`;
                  context.textAlign = "right";
                  context.textBaseline = "bottom";
                  context.fillText(
                    reference.label,
                    plot.bbox.left + plot.bbox.width - 4 * devicePixelRatio,
                    y - 2 * devicePixelRatio,
                  );
                  context.restore();
                },
              ]
            : [],
          setSelect: [
            (plot) => {
              if (plot.select.width <= 0) {
                return;
              }
              const startMs = plot.posToVal(plot.select.left, "x") * 1000;
              const endMs = plot.posToVal(plot.select.left + plot.select.width, "x") * 1000;
              // Cleared before the callback: the view is about to answer with a
              // different window, and a highlight left over from the old one
              // would sit on top of it.
              plot.setSelect({ left: 0, top: 0, width: 0, height: 0 }, false);
              selectRef.current?.(startMs, endMs);
            },
          ],
        },
        plugins: [
          tooltipPlugin(
            (value) => formatRef.current(value),
            (seriesIndex, dataIndex) => {
              const current = readoutRef.current;
              const source = current.order[seriesIndex - 1];
              if (source === undefined) {
                return null;
              }
              return current.values[source]?.[dataIndex] ?? null;
            },
          ),
        ],
      },
      data,
      container,
    );
    plotRef.current = plot;
    return () => {
      plot.destroy();
      plotRef.current = null;
    };
    // `data` and `hidden` are intentionally absent: they flow through setData
    // and setSeries below, which is what keeps a poll or a legend click from
    // rebuilding the instance.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    seriesSignature,
    paletteSignature,
    width,
    height,
    fullScale,
    stacked,
    reference?.value,
    reference?.label,
    syncKey,
    spanSeconds,
  ]);

  useEffect(() => {
    plotRef.current?.setData(data);
  }, [data]);

  useEffect(() => {
    const plot = plotRef.current;
    if (!plot) {
      return;
    }
    // Through the drawing order, which is not the series order once the chart
    // is stacked.
    plotted.order.forEach((source, position) => {
      const item = series[source];
      if (!item) {
        return;
      }
      const show = !hidden?.has(item.id);
      const drawn = plot.series[position + 1];
      if (drawn && drawn.show !== show) {
        plot.setSeries(position + 1, { show });
      }
    });
  }, [hidden, series, plotted]);

  useEffect(() => {
    if (width > 0) {
      plotRef.current?.setSize({ width, height });
    }
  }, [width, height]);

  return (
    <div
      ref={containerRef}
      className="w-full"
      style={{ height }}
      role="img"
      aria-label={ariaLabel}
    />
  );
}
