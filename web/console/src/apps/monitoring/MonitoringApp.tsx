import { useEffect, useMemo, useState } from "react";
import {
  Activity,
  Boxes,
  Cpu,
  HardDrive,
  LayoutDashboard,
  PlugZap,
  SearchCode,
} from "lucide-react";

import { useMetricsQueryCatalog } from "@/api/queries/observability";
import { AppShell, ScopeRequired, type AppNavItem } from "@/apps/AppShell";
import { METRICS_EVIDENCE_QUERY_KEY } from "@/apps/evidence-link";
import type { AppComponentProps } from "@/apps/types";
import { isApiError } from "@/api/errors";
import { useSessionContext } from "@/auth/session-context";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { Alert } from "@/components/ui/misc";
import { useScopeStore } from "@/scope/scope-store";

import { CollectionQualitySection } from "./CollectionQualitySection";
import { CollectionSection } from "./CollectionSection";
import { ComputeSection } from "./ComputeSection";
import { ExploreSection } from "./ExploreSection";
import { KubernetesSection } from "./KubernetesSection";
import { MetricsToolbar } from "./MetricsToolbar";
import { OverviewSection } from "./OverviewSection";
import { StorageNetworkSection } from "./StorageNetworkSection";
import { MetricsGate, MetricsScopeProvider } from "./MetricsScopeProvider";
import { ExploreProvider } from "./explore/explore-state";
import {
  COLLECTION_QUALITY_VIEWS,
  COMPUTE_DIMENSIONS,
  KUBERNETES_VIEWS,
  OVERVIEW_PANELS,
  STORAGE_VIEWS,
  type MetricsView,
} from "./metrics-catalog";

/**
 * Collection comes first because it comes first: a Cluster reports nothing
 * until its collector is installed, so an operator opening this application on
 * a fresh deployment lands on the only screen that has anything for them to do.
 *
 * It is also the one section that changes what runs inside somebody's Cluster,
 * so it is hidden from an operator who only holds the read permission — and
 * then this is not where they land. DEFAULT_CHART_SECTION is.
 */
const COLLECTION_SECTION = "collection";

/**
 * Ad-hoc queries. Named so the shell can tell the Explore state provider
 * whether the screen it holds the expressions for is the one on screen: it
 * stays mounted across a section change on purpose, and something has to stop
 * it re-running those expressions on every tick of a clock nobody is watching.
 */
const EXPLORE_SECTION = "explore";

/**
 * Where an operator lands when the section they asked for is not open to them.
 *
 * Not simply the first entry of the rail any more. 数据探索 sits second, and it
 * opens on an empty expression box — a worse first screen than the dashboard
 * for somebody who holds only the read permission and therefore never sees
 * 采集接入 above it. The rail is ordered for reaching a tool; this is ordered
 * for arriving somewhere.
 */
const DEFAULT_CHART_SECTION = "overview";

/**
 * The chart sections, split by the question rather than by the metric.
 *
 * One 指标总览 holding everything was the wrong shape twice over: it could not
 * fit the catalogue, and the catalogue does not answer one question. Capacity,
 * device saturation and object state are asked at different times by different
 * people, and each of them wants its own set of panels open — not a select at
 * the top of a single screen.
 */
const NAV: AppNavItem[] = [
  { id: COLLECTION_SECTION, label: "采集接入", icon: PlugZap },
  // Second, directly under 采集接入 and above the dashboards. The four sections
  // below answer the questions somebody wrote a panel for; this one answers the
  // rest, which during an incident is most of them. Putting it after the
  // dashboards would file the general tool behind the specific ones.
  { id: EXPLORE_SECTION, label: "数据探索", icon: SearchCode },
  { id: "overview", label: "集群总览", icon: LayoutDashboard },
  { id: "compute", label: "计算资源", icon: Cpu },
  { id: "storage", label: "存储与网络", icon: HardDrive },
  { id: "kubernetes", label: "Kubernetes 资源", icon: Boxes },
  // Last, and a chart section rather than part of 采集接入 above: it answers
  // why the other four are empty, and it has to be reachable by an operator who
  // holds only the read permission.
  { id: "collection-quality", label: "采集质量", icon: Activity },
];

function viewsContainQuery(views: readonly MetricsView[], query: string): boolean {
  return views.some((view) =>
    view.panels.some((panel) => panel.queries.some((item) => item.name === query)),
  );
}

function evidenceSection(query: string): string {
  if (OVERVIEW_PANELS.some((panel) => panel.queries.some((item) => item.name === query)))
    return "overview";
  if (COMPUTE_DIMENSIONS.some((dimension) => viewsContainQuery(dimension.views, query)))
    return "compute";
  if (viewsContainQuery(STORAGE_VIEWS, query)) return "storage";
  if (viewsContainQuery(KUBERNETES_VIEWS, query)) return "kubernetes";
  if (viewsContainQuery(COLLECTION_QUALITY_VIEWS, query)) return "collection-quality";
  return COLLECTION_SECTION;
}

/**
 * Monitoring is a single-Cluster application: it opens inside the selected
 * Project and every chart under it describes the one Cluster picked in the
 * toolbar.
 *
 * A Project is a precondition, and it is checked here rather than in the
 * section that needs it: every section works within one Project, so without a
 * scope there is nothing for the navigation rail to navigate between, and a rail
 * standing beside 请先选择项目 offers a choice that leads nowhere. Every other
 * application in the Console answers this the same way — the shell does not
 * open until there is a scope for it to work in.
 *
 * The two permissions split the rail. Reading charts is `cluster.metrics.read`;
 * installing and removing collectors is `cluster.metrics.manage`, and it is the
 * only thing here that changes what runs inside somebody's Cluster. Hiding the
 * entry an operator cannot use is presentation, not enforcement — the Server
 * refuses the request either way — but an entry that only ever answers 403 is
 * worse than no entry.
 */
export function MonitoringApp(_props: AppComponentProps) {
  const [initialQuery] = useState(() => sessionStorage.getItem(METRICS_EVIDENCE_QUERY_KEY) ?? "");
  useEffect(() => {
    sessionStorage.removeItem(METRICS_EVIDENCE_QUERY_KEY);
  }, []);
  const [section, setSection] = useState(() => evidenceSection(initialQuery));
  const scope = useScopeStore((state) => state.scope);
  const projectId = scope.projectId;
  const { permissions } = useSessionContext();
  const catalog = useMetricsQueryCatalog();

  const projectScope = {
    type: "project" as const,
    tenantId: scope.tenantId,
    projectId,
  };
  const canManageCollection = permissions.can("cluster.metrics.manage", projectScope);
  const canReadMetrics = permissions.can("cluster.metrics.read", projectScope);

  const nav = NAV.map((item) => ({
    ...item,
    hidden: item.id === COLLECTION_SECTION ? !canManageCollection : !canReadMetrics,
  }));
  // A section that has just been hidden — the scope changed under a window that
  // was already open — falls back to the landing section rather than rendering
  // behind a rail that no longer offers it. Undefined only when the rail is
  // empty, which is the state the warning below covers.
  const landing =
    nav.find((item) => item.id === DEFAULT_CHART_SECTION && !item.hidden)?.id ??
    nav.find((item) => !item.hidden)?.id;
  const activeId = nav.find((item) => item.id === section && !item.hidden) ? section : landing;
  const charts = Boolean(activeId) && activeId !== COLLECTION_SECTION;

  const content = useMemo(() => {
    if (catalog.isPending) {
      return <LoadingState />;
    }
    // A Server without metrics storage answers every metrics route with
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
    switch (activeId) {
      case "overview":
        return (
          <MetricsGate>
            <OverviewSection />
          </MetricsGate>
        );
      case "compute":
        return (
          <MetricsGate>
            <ComputeSection initialQuery={initialQuery} />
          </MetricsGate>
        );
      case "storage":
        return (
          <MetricsGate>
            <StorageNetworkSection initialQuery={initialQuery} />
          </MetricsGate>
        );
      case "kubernetes":
        return (
          <MetricsGate>
            <KubernetesSection initialQuery={initialQuery} />
          </MetricsGate>
        );
      case EXPLORE_SECTION:
        return (
          <MetricsGate>
            <ExploreSection />
          </MetricsGate>
        );
      case "collection-quality":
        return (
          <MetricsGate>
            <CollectionQualitySection initialQuery={initialQuery} />
          </MetricsGate>
        );
      default:
        return <CollectionSection />;
    }
  }, [activeId, catalog, initialQuery]);

  if (!projectId) {
    return <ScopeRequired />;
  }

  if (!landing || !activeId) {
    return (
      <div className="p-4">
        <Alert tone="warning">
          当前账号在该项目下既没有指标读取权限，也没有采集组件管理权限。相关入口已隐藏，服务端也会拒绝
          对应请求。
        </Alert>
      </div>
    );
  }

  return (
    // The scope provider wraps the shell rather than the sections: the toolbar
    // is the thing that sets it, and it is rendered by the shell.
    <MetricsScopeProvider enabled={charts}>
      {/* Outside the shell rather than inside the section: the rail unmounts
          whichever section is not open, and the expressions an operator is in
          the middle of writing have to survive a look at another one. */}
      <ExploreProvider enabled={activeId === EXPLORE_SECTION}>
        <AppShell
          nav={nav}
          activeId={activeId}
          onNavigate={setSection}
          toolbar={charts ? <MetricsToolbar /> : undefined}
        >
          {content}
        </AppShell>
      </ExploreProvider>
    </MetricsScopeProvider>
  );
}
