/** The three numbers a legend row reports, or null where nothing was sampled. */
export type SeriesStats = { last: number | null; mean: number | null; max: number | null };

/** The legend's three numbers, ignoring the gaps a chart must not bridge. */
export function summarise(values: (number | null)[]): SeriesStats {
  let last: number | null = null;
  let max: number | null = null;
  let total = 0;
  let counted = 0;
  for (const value of values) {
    if (value === null || !Number.isFinite(value)) {
      continue;
    }
    last = value;
    max = max === null ? value : Math.max(max, value);
    total += value;
    counted += 1;
  }
  return { last, mean: counted === 0 ? null : total / counted, max };
}
