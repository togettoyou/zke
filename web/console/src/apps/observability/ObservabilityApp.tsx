import { useMemo, useState } from "react";
import { Activity, PlugZap } from "lucide-react";

import { useMetricsQueryCatalog } from "@/api/queries/observability";
import { AppShell, type AppNavItem } from "@/apps/AppShell";
import type { AppComponentProps } from "@/apps/types";
import { isApiError } from "@/api/errors";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";

import { CollectionSection } from "./CollectionSection";
import { MetricsOverviewSection } from "./MetricsOverviewSection";

/**
 * Collection comes first because it comes first: a Cluster reports nothing
 * until its collector is installed, so an operator opening this application on
 * a fresh deployment lands on the only screen that has anything for them to do.
 */
const DEFAULT_SECTION = "collection";

const NAV: AppNavItem[] = [
  { id: DEFAULT_SECTION, label: "采集接入", icon: PlugZap },
  { id: "metrics", label: "指标总览", icon: Activity },
];

/**
 * Observability is a multi-cluster application: it opens on everything the
 * operator may read rather than asking which Cluster first. Narrowing to one
 * Cluster is a filter inside the view, not a precondition for entering it.
 */
export function ObservabilityApp(_props: AppComponentProps) {
  const [activeId, setActiveId] = useState(DEFAULT_SECTION);
  const catalog = useMetricsQueryCatalog();

  const content = useMemo(() => {
    if (catalog.isPending) {
      return <LoadingState />;
    }
    // A Server without metrics storage answers every observability route with
    // the same explicit state. Saying so once, here, is clearer than each
    // panel failing on its own.
    if (catalog.error) {
      if (isApiError(catalog.error) && catalog.error.code === "metrics_disabled") {
        return (
          <EmptyState
            title="本部署未启用指标存储"
            description="ZKE Server 需要配置指标存储后端后才能采集与查询指标。启用前，集群侧不会部署任何采集组件。"
          />
        );
      }
      return <ErrorState error={catalog.error} onRetry={() => void catalog.refetch()} />;
    }
    if (activeId === "metrics") {
      return <MetricsOverviewSection />;
    }
    return <CollectionSection />;
  }, [activeId, catalog]);

  return (
    <AppShell nav={NAV} activeId={activeId} onNavigate={setActiveId}>
      {content}
    </AppShell>
  );
}
