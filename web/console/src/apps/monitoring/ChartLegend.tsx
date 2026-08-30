import { cn } from "@/lib/cn";

import type { ChartSeries } from "./TimeSeriesChart";
import type { SeriesStats } from "./series-stats";

export type LegendSeries = ChartSeries & { stats: SeriesStats };

/**
 * The legend, which is also the values without a pointer.
 *
 * Three summary numbers per curve rather than a colour chip and a name: the
 * question a chart is opened with is usually "which one is the highest" or
 * "which one moved", and both are answered here without hovering anything. It
 * is also what keeps identity off colour alone — several palette steps sit
 * below 3:1 against the light surface, which is only acceptable while every
 * series is named in text beside its value.
 *
 * Shared by the catalogue panels and by Explore. They draw different questions
 * but the same kind of answer, and a second legend would drift from this one in
 * exactly the places that matter: which numbers it reports, and whether a click
 * hides a curve.
 */
export function ChartLegend({
  series,
  hidden,
  onToggle,
  formatValue,
  colorAt,
  dashAt,
  numberColumnWidth = "4.25rem",
}: {
  series: LegendSeries[];
  hidden: ReadonlySet<string>;
  onToggle: (id: string) => void;
  formatValue: (value: number) => string;
  colorAt: (index: number) => string;
  dashAt: (index: number) => number[] | undefined;
  /**
   * How much room each number gets. The default fits a catalogue panel, whose
   * values carry a unit that shortens them — `1.2 GiB`, `78%`. Explore shows
   * raw numbers, which are much longer, and a column sized for the first would
   * clip the second.
   */
  numberColumnWidth?: string;
}) {
  const columns = `minmax(0,1fr) repeat(3, ${numberColumnWidth})`;
  return (
    <div className="mt-3">
      <div
        className="text-subtle-foreground grid gap-x-3 px-1 pb-1 text-[11px]"
        style={{ gridTemplateColumns: columns }}
      >
        <span>序列</span>
        <span className="text-right">最新</span>
        <span className="text-right">平均</span>
        <span className="text-right">最大</span>
      </div>
      <ul className="max-h-36 overflow-y-auto">
        {series.map((item, index) => {
          const off = hidden.has(item.id);
          const dash = dashAt(index);
          return (
            <li key={item.id}>
              <button
                type="button"
                role="switch"
                aria-checked={!off}
                onClick={() => onToggle(item.id)}
                style={{ gridTemplateColumns: columns }}
                className={cn(
                  "zke-focus rounded-control hover:bg-surface-muted grid w-full items-center gap-x-3 px-1 py-0.5 text-left text-xs transition-colors",
                  off && "opacity-45",
                )}
              >
                <span className="flex min-w-0 items-center gap-2">
                  <span
                    aria-hidden
                    className="h-0 w-3.5 shrink-0 border-t-2"
                    style={{
                      borderColor: colorAt(index),
                      borderTopStyle: dash ? "dashed" : "solid",
                    }}
                  />
                  <span className="text-muted-foreground truncate">{item.label}</span>
                </span>
                <span className="zke-tnum text-foreground truncate text-right font-medium">
                  {item.stats.last === null ? "—" : formatValue(item.stats.last)}
                </span>
                <span className="zke-tnum text-muted-foreground truncate text-right">
                  {item.stats.mean === null ? "—" : formatValue(item.stats.mean)}
                </span>
                <span className="zke-tnum text-muted-foreground truncate text-right">
                  {item.stats.max === null ? "—" : formatValue(item.stats.max)}
                </span>
              </button>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
