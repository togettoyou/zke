import { useState } from "react";

import { SegmentedTabs, ViewPanels } from "./MetricsViews";
import { COMPUTE_DIMENSIONS } from "./metrics-catalog";

/**
 * CPU and memory, from the Cluster down to the Pod.
 *
 * Two rows of choices rather than one long list of charts: a dimension, then an
 * angle on it. Each panel is a request a single-instance Server runs against
 * storage every Cluster shares, and the catalogue is large enough that drawing
 * all of it at once would make an open window a standing load on the whole
 * deployment. It is also how the question gets asked — an operator looks at
 * Namespace requests, or at Node saturation, not at thirty charts while
 * comparing.
 */
export function ComputeSection({ initialQuery }: { initialQuery?: string }) {
  const initial = COMPUTE_DIMENSIONS.flatMap((dimension) =>
    dimension.views.map((view) => ({ dimension, view })),
  ).find(({ view }) =>
    view.panels.some((panel) => panel.queries.some((query) => query.name === initialQuery)),
  );
  const [dimensionId, setDimensionId] = useState(initial?.dimension.id ?? COMPUTE_DIMENSIONS[0].id);
  const [viewId, setViewId] = useState(initial?.view.id ?? COMPUTE_DIMENSIONS[0].views[0].id);

  const dimension =
    COMPUTE_DIMENSIONS.find((item) => item.id === dimensionId) ?? COMPUTE_DIMENSIONS[0];
  // Falling back to the dimension's first view rather than clearing the
  // selection: 用量 and 利用率 exist almost everywhere, so moving between
  // dimensions keeps showing the same angle wherever it makes sense.
  const view = dimension.views.find((item) => item.id === viewId) ?? dimension.views[0];

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <SegmentedTabs
          items={COMPUTE_DIMENSIONS}
          activeId={dimension.id}
          onSelect={setDimensionId}
          label="资源维度"
        />
        {dimension.views.length > 1 ? (
          <SegmentedTabs
            items={dimension.views}
            activeId={view.id}
            onSelect={setViewId}
            label="视角"
          />
        ) : null}
      </div>
      <ViewPanels key={`${dimension.id}:${view.id}`} view={view} />
    </div>
  );
}
