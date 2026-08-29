import { useMemo } from "react";

import { useThemeStore, type Theme } from "@/theme/theme-store";

/**
 * The colours a chart draws with, read from the document rather than written
 * here.
 *
 * Canvas cannot resolve `var(--token)`, so every value has to be a literal by
 * the time it reaches uPlot. Reading them from the computed style keeps the
 * single source in `theme.css`: a palette copied into TypeScript would go stale
 * the first time the theme changed, and it would do so silently, in one theme
 * only.
 */
export type ChartPalette = {
  axis: string;
  grid: string;
  /** Never empty, so a slot index always resolves to a colour. */
  series: readonly [string, ...string[]];
};

const SERIES_TOKENS = [
  "--chart-1",
  "--chart-2",
  "--chart-3",
  "--chart-4",
  "--chart-5",
  "--chart-6",
  "--chart-7",
  "--chart-8",
] as const;

/** Only reached before the stylesheet has applied; the light theme's slot 1. */
const FALLBACK_SERIES = "#2a78d6";

/**
 * Stroke patterns for the curves past the eighth.
 *
 * The palette's eight steps are validated as a set and no ninth colour can be
 * added to it without breaking the separation the set was chosen for. So the
 * ninth curve reuses the first colour and changes its stroke instead: two
 * curves may share a hue, but never a hue and a pattern. Top N caps the answer
 * long before the third tier is reached; it exists so the wrap is defined
 * rather than surprising.
 */
const DASH_TIERS: (number[] | undefined)[] = [undefined, [7, 4], [2, 3], [10, 3, 2, 3]];

export function seriesColor(palette: ChartPalette, index: number): string {
  return palette.series[index % palette.series.length] ?? palette.series[0];
}

function readSeries(read: (token: string, fallback: string) => string): [string, ...string[]] {
  const [first, ...rest] = SERIES_TOKENS;
  return [read(first, FALLBACK_SERIES), ...rest.map((token) => read(token, FALLBACK_SERIES))];
}

export function seriesDash(index: number): number[] | undefined {
  const tier = Math.floor(index / SERIES_TOKENS.length);
  return DASH_TIERS[Math.min(tier, DASH_TIERS.length - 1)];
}

/**
 * `theme` is not read: the values come from the document, which the theme store
 * has already restyled by the time this runs. It is the parameter that says so
 * — passing it makes the dependency real rather than a lint exception.
 */
function readChartPalette(_theme: Theme): ChartPalette {
  const styles = getComputedStyle(document.documentElement);
  const read = (token: string, fallback: string) =>
    styles.getPropertyValue(token).trim() || fallback;
  return {
    axis: read("--subtle-foreground", "#78828f"),
    grid: read("--border", "#dfe4ec"),
    series: readSeries(read),
  };
}

/** Re-read on every theme change, which is the only time these values move. */
export function useChartPalette(): ChartPalette {
  const theme = useThemeStore((state) => state.theme);
  return useMemo(() => readChartPalette(theme), [theme]);
}
