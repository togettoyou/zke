import { ViewedPanels } from "./MetricsViews";
import type { MetricsViews } from "./metrics-catalog";

/** One monitoring catalogue branch, loading only the selected view's panels. */
export function CatalogueSection({
  views,
  label,
  initialQuery,
}: {
  views: MetricsViews;
  label: string;
  initialQuery?: string;
}) {
  return <ViewedPanels views={views} label={label} initialQuery={initialQuery} />;
}
