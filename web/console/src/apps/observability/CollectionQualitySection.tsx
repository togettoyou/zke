import { ViewedPanels } from "./MetricsViews";
import { COLLECTION_QUALITY_VIEWS } from "./metrics-catalog";

/**
 * Whether the numbers on every other screen are arriving at all.
 *
 * The charts elsewhere describe the Cluster; these describe the pipeline that
 * measures it, and they are read at the moment the two are indistinguishable —
 * a screen full of flat lines is either a quiet Cluster or a target that
 * stopped answering, and no chart of the Cluster itself can say which.
 */
export function CollectionQualitySection({ initialQuery }: { initialQuery?: string }) {
  return (
    <ViewedPanels
      views={COLLECTION_QUALITY_VIEWS}
      label="采集质量视角"
      initialQuery={initialQuery}
    />
  );
}
