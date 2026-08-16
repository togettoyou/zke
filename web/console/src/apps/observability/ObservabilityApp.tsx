import { useMemo, useState } from "react";
import { Activity, PlugZap } from "lucide-react";

import { useMetricsQueryCatalog } from "@/api/queries/observability";
import { AppShell, ScopeRequired, type AppNavItem } from "@/apps/AppShell";
import type { AppComponentProps } from "@/apps/types";
import { isApiError } from "@/api/errors";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { useScopeStore } from "@/scope/scope-store";

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
 * Observability is a multi-cluster application: within the selected Project it
 * opens on every Cluster the operator may read rather than asking which one
 * first. Narrowing to a single Cluster is a filter inside the view, not a
 * precondition for entering it.
 *
 * A Project is still a precondition, and it is checked here rather than in the
 * section that needs it: 采集接入 lists the Clusters of one Project, so without a
 * scope there is nothing for the navigation rail to navigate between, and a rail
 * standing beside 请先选择项目 offers a choice that leads nowhere. Every other
 * application in the Console answers this the same way — the shell does not
 * open until there is a scope for it to work in.
 */
export function ObservabilityApp(_props: AppComponentProps) {
  const [activeId, setActiveId] = useState(DEFAULT_SECTION);
  const projectId = useScopeStore((state) => state.scope.projectId);
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

  if (!projectId) {
    return <ScopeRequired />;
  }

  return (
    <AppShell nav={NAV} activeId={activeId} onNavigate={setActiveId}>
      {content}
    </AppShell>
  );
}
