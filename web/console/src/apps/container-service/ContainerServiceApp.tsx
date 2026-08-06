import { useMemo, useState } from "react";
import {
  Bell,
  Box,
  Database,
  Gauge,
  KeyRound,
  FileCog,
  FolderTree,
  LayoutDashboard,
  Layers,
  Network,
  Search,
  Server,
  ShieldCheck,
} from "lucide-react";

import { useClusters } from "@/api/queries/clusters";
import { useNamespaces } from "@/api/queries/namespaces";
import { AppShell, ScopeRequired, type AppNavItem } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { ErrorState, LoadingState } from "@/components/common/state";
import { StatusBadge } from "@/components/common/status";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useScopeStore } from "@/scope/scope-store";

import { AuthorizationSection } from "./AuthorizationSection";
import { AutoscalerSection } from "./AutoscalerSection";
import { ConfigurationSection } from "./ConfigurationSection";
import { StorageSection } from "./StorageSection";
import { EventSection } from "./EventSection";
import { NamespaceSection } from "./NamespaceSection";
import { NetworkingSection } from "./NetworkingSection";
import { NodeSection } from "./NodeSection";
import { OverviewSection } from "./OverviewSection";
import { PodSection } from "./PodSection";
import { PolicySection } from "./PolicySection";
import { ResourceBrowserSection } from "./ResourceBrowserSection";
import { useTargetClusterStore, useTargetNamespaceStore } from "./selection-store";
import { WorkloadSection } from "./WorkloadSection";

/**
 * Resource categories, the way every established Kubernetes console is
 * organised.
 */
const NAV: AppNavItem[] = [
  { id: "overview", label: "概览", icon: LayoutDashboard },
  { id: "nodes", label: "节点", icon: Server },
  { id: "namespaces", label: "命名空间", icon: FolderTree },
  { id: "workloads", label: "工作负载", icon: Layers },
  { id: "pods", label: "Pod", icon: Box },
  { id: "networking", label: "服务与路由", icon: Network },
  { id: "configmaps", label: "配置管理", icon: FileCog },
  { id: "storage", label: "存储", icon: Database },
  { id: "autoscaling", label: "自动伸缩", icon: Gauge },
  { id: "policies", label: "策略管理", icon: ShieldCheck },
  { id: "authorization", label: "授权管理", icon: KeyRound },
  // After the typed categories and before 事件: it is the escape hatch for the
  // types the categories above do not model, so it reads as the end of the
  // resource list rather than an item inside it. 事件 stays last — it is not a
  // resource category at all but a stream about the ones above.
  { id: "browser", label: "资源对象浏览器", icon: Search },
  { id: "events", label: "事件", icon: Bell },
];

/** Sections whose queries are scoped by a Namespace as well as by a Cluster. */
const NAMESPACED_SECTIONS = new Set([
  "workloads",
  "pods",
  "networking",
  "configmaps",
  "autoscaling",
  "events",
]);

/**
 * The Namespace picker reads one page of Namespaces at the endpoint's maximum.
 * It is a selector, not a list view — the paged list lives in its own section —
 * so a Cluster with more Namespaces than this shows a truncation notice rather
 * than growing paging controls into the toolbar.
 */
const NAMESPACE_PICKER_LIMIT = 500;

/**
 * Container service is a single-cluster application: the operator picks one
 * target Cluster and every page and operation in the window is scoped to it.
 * Only online Clusters can be the target, because each request is executed by
 * that Cluster's Agent.
 */
export function ContainerServiceApp() {
  const scope = useScopeStore((state) => state.scope);
  const { permissions } = useSessionContext();
  // The overview is where an operator lands: it is the one view that answers
  // "what is in this cluster" before they know which category to open.
  const [section, setSection] = useState("overview");
  const clusters = useClusters(scope.projectId, {
    limit: 100,
    offset: 0,
    status: "active",
  });
  const storedClusters = useTargetClusterStore((state) => state.selections);
  const selectCluster = useTargetClusterStore((state) => state.select);
  const storedNamespaces = useTargetNamespaceStore((state) => state.selections);
  const selectNamespace = useTargetNamespaceStore((state) => state.select);

  const projectClusters = clusters.data?.clusters ?? [];
  // Offline Clusters stay in the picker — disabled — because an operator looking
  // for one needs to see that it is known and offline rather than find it
  // silently missing. The stored choice is re-resolved against the online set on
  // every render, so a Cluster that went offline selects nothing.
  const onlineClusters = projectClusters.filter(
    (cluster) => cluster.connection.status === "online",
  );
  const stored = scope.projectId ? storedClusters[scope.projectId] : undefined;
  const clusterId = onlineClusters.some((cluster) => cluster.id === stored)
    ? (stored as string)
    : (onlineClusters[0]?.id ?? "");
  const clusterName =
    projectClusters.find((cluster) => cluster.id === clusterId)?.name ?? clusterId;

  // Reading Events is its own permission rather than part of reading the
  // Cluster, so the rail hides the category a caller cannot open at all. The
  // Server enforces it regardless.
  const canReadEvents = permissions.can("cluster.event.read", {
    type: "project",
    tenantId: scope.tenantId,
    projectId: scope.projectId,
  });
  // Reading RBAC is its own permission too, and for the same reason: a grant
  // decides what every other permission is worth.
  const canReadRbac = permissions.can("cluster.rbac.read", {
    type: "project",
    tenantId: scope.tenantId,
    projectId: scope.projectId,
  });
  const nav = useMemo(
    () =>
      NAV.map((item) => {
        if (item.id === "events") {
          return { ...item, hidden: !canReadEvents };
        }
        if (item.id === "authorization") {
          return { ...item, hidden: !canReadRbac };
        }
        return item;
      }),
    [canReadEvents, canReadRbac],
  );
  const hiddenSection =
    (section === "events" && !canReadEvents) || (section === "authorization" && !canReadRbac);
  const activeSection = hiddenSection ? "overview" : section;

  // Only the namespaced sections need the picker, and only they pay for the
  // query behind it. A control that scopes nothing would be a control that does
  // nothing, so the cluster-scoped sections do not show one.
  // Storage spans both scoping models: PersistentVolume and StorageClass are
  // cluster objects, PersistentVolumeClaim is namespaced. The section reports
  // which one its current tab is showing, so the picker appears exactly while it
  // scopes something.
  const [storageNamespaced, setStorageNamespaced] = useState(false);
  // Authorization spans both scoping models the same way storage does:
  // ClusterRole and ClusterRoleBinding are cluster objects, the other three are
  // namespaced.
  const [authorizationNamespaced, setAuthorizationNamespaced] = useState(true);
  // Policies too: PriorityClass ranks Pods across the Cluster, the other four
  // constrain one Namespace.
  const [policiesNamespaced, setPoliciesNamespaced] = useState(true);
  const namespaced =
    NAMESPACED_SECTIONS.has(activeSection) ||
    (activeSection === "storage" && storageNamespaced) ||
    (activeSection === "authorization" && authorizationNamespaced) ||
    (activeSection === "policies" && policiesNamespaced);
  /*
   * Storage waits for its own Namespace rather than being held behind the shared
   * gate below. Its tabs change scope, so unmounting the section to wait would
   * also discard which tab is open — clicking the PersistentVolumeClaim tab
   * would bounce straight back to the cluster-scoped one it was just left on.
   */
  const awaitsNamespace =
    namespaced &&
    activeSection !== "storage" &&
    activeSection !== "authorization" &&
    activeSection !== "policies";
  const namespaces = useNamespaces(namespaced && clusterId ? clusterId : null, {
    limit: NAMESPACE_PICKER_LIMIT,
  });
  const namespaceNames = (namespaces.data?.namespaces ?? []).map((namespace) => namespace.name);
  // Same resolution rule as the Cluster above: the stored choice only counts
  // while it still exists in the Cluster, and `default` is the conventional
  // landing place when it does not.
  const storedNamespace = clusterId ? storedNamespaces[clusterId] : undefined;
  const namespace = namespaceNames.includes(storedNamespace ?? "")
    ? (storedNamespace as string)
    : namespaceNames.includes("default")
      ? "default"
      : (namespaceNames[0] ?? "");
  // Only while the picker is on screen: the query is disabled elsewhere, but its
  // last result stays cached, and a note about a control nobody can see is noise.
  const namespacesTruncated = namespaced && Boolean(namespaces.data?.continue_token);

  if (!scope.projectId) {
    return <ScopeRequired />;
  }

  return (
    <AppShell
      nav={nav}
      activeId={activeSection}
      onNavigate={setSection}
      toolbar={
        <>
          <span className="text-muted-foreground text-xs">目标集群</span>
          <Select
            value={clusterId}
            onValueChange={(value) => selectCluster(scope.projectId as string, value)}
            disabled={clusters.isLoading}
          >
            <SelectTrigger className="w-64" aria-label="目标集群">
              <SelectValue placeholder={clusters.isLoading ? "加载集群…" : "选择集群"} />
            </SelectTrigger>
            <SelectContent>
              {projectClusters.map((cluster) => {
                const online = cluster.connection.status === "online";
                return (
                  <SelectItem key={cluster.id} value={cluster.id} disabled={!online}>
                    <span className="flex items-center gap-2">
                      {cluster.name}
                      {online ? null : (
                        <StatusBadge kind="connection" value={cluster.connection.status} />
                      )}
                    </span>
                  </SelectItem>
                );
              })}
            </SelectContent>
          </Select>
          {/*
           * No Cluster identifier in the toolbar. The picker above already names
           * the target, and a UUID sitting next to it permanently is chrome an
           * operator never reads — it belongs where identity has to be verified,
           * which is the confirmation dialog of each sensitive operation, and
           * where Clusters are administered, which is 集群接入管理.
           */}
          {namespaced && clusterId ? (
            <>
              {/* Named as the navigation rail names it. One resource with two
                  names in one window is one name too many. */}
              <span className="text-muted-foreground ml-2 text-xs">命名空间</span>
              <Select
                value={namespace}
                onValueChange={(value) => selectNamespace(clusterId, value)}
                disabled={namespaces.isLoading || namespaceNames.length === 0}
              >
                <SelectTrigger className="w-56" aria-label="命名空间">
                  <SelectValue
                    placeholder={namespaces.isLoading ? "加载命名空间…" : "选择命名空间"}
                  />
                </SelectTrigger>
                <SelectContent>
                  {namespaceNames.map((name) => (
                    <SelectItem key={name} value={name}>
                      {name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </>
          ) : null}
        </>
      }
      /*
       * What the pickers above say, for the views that replace them. An object
       * of the same name exists in every Cluster, so a page showing one without
       * naming its Cluster is a page that cannot be read.
       */
      scope={
        clusterId ? `${clusterName}${namespaced && namespace ? ` · ${namespace}` : ""}` : undefined
      }
      /*
       * The status bar carries exceptions only. A line restating that every
       * request runs on the selected Cluster's Agent was true on every screen of
       * this window, which is exactly why nobody read it — and it cost a row of
       * height on all of them. What remains is what an operator cannot infer
       * from the toolbar: a truncated Namespace picker, or no target at all.
       */
      statusBar={
        clusterId
          ? namespacesTruncated
            ? `命名空间选择器只列出前 ${NAMESPACE_PICKER_LIMIT} 个`
            : undefined
          : "没有可执行请求的在线集群"
      }
    >
      {clusters.error ? (
        <ErrorState error={clusters.error} onRetry={() => void clusters.refetch()} />
      ) : clusters.isLoading ? (
        <LoadingState />
      ) : projectClusters.length === 0 ? (
        <EmptyNotice
          title="该项目没有已接入的集群"
          description="请先在「集群接入管理」中接入并启用集群。"
        />
      ) : clusterId === "" ? (
        <EmptyNotice
          title="该项目没有在线集群"
          description="容器服务的每个查询和变更都由目标集群的 Agent 定域执行，需要至少一个 Agent 处于在线状态。"
        />
      ) : /*
       * Every section is keyed by the target Cluster, and only the Cluster.
       *
       * The key is a reset: changing it throws the section's state away. The
       * Cluster belongs there — nothing an operator was looking at survives
       * pointing the window at different infrastructure. The Namespace does not.
       * A section's state is which tab is open and which page of the list is
       * showing, and the second one resets itself: a continuation token is only
       * meaningful to the list that issued it, so every list here already keys its
       * pager on the Namespace. Keying the section on it as well only discarded
       * the tab, which is how selecting a Namespace while reading DaemonSets
       * landed back on Deployments.
       *
       * Nothing namespaced is left stale by that. The views that hold one object —
       * a detail page, a form, the YAML editor — put up a page header, and the
       * shell hides the toolbar while one is up, so the Namespace picker is not on
       * screen to be changed; a confirmation dialog is modal, for the same effect.
       *
       * 事件 is the exception and stays keyed on the Namespace: its state is an
       * accumulated stream of that Namespace's events, so a remount is the correct
       * way to drop them, and it has no tab to lose.
       */
      activeSection === "overview" ? (
        <OverviewSection key={clusterId} clusterId={clusterId} />
      ) : activeSection === "nodes" ? (
        <NodeSection
          key={clusterId}
          clusterId={clusterId}
          clusterName={clusterName}
          tenantId={scope.tenantId}
          projectId={scope.projectId}
        />
      ) : activeSection === "namespaces" ? (
        <NamespaceSection
          key={clusterId}
          clusterId={clusterId}
          clusterName={clusterName}
          tenantId={scope.tenantId}
          projectId={scope.projectId}
        />
      ) : awaitsNamespace && namespaces.error ? (
        <ErrorState error={namespaces.error} onRetry={() => void namespaces.refetch()} />
      ) : awaitsNamespace && namespaces.isLoading ? (
        <LoadingState />
      ) : awaitsNamespace && namespace === "" ? (
        <EmptyNotice
          title="该集群没有可见的命名空间"
          description="工作负载、Pod、服务与路由、配置管理、自动伸缩、事件和 PersistentVolumeClaim 按命名空间定域查询，需要目标集群中至少存在一个当前身份可见的命名空间。"
        />
      ) : activeSection === "events" ? (
        <EventSection
          key={`${clusterId}/${namespace}`}
          clusterId={clusterId}
          namespace={namespace}
        />
      ) : activeSection === "authorization" ? (
        <AuthorizationSection
          key={clusterId}
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          tenantId={scope.tenantId}
          projectId={scope.projectId}
          onNamespaceScopeChange={setAuthorizationNamespaced}
        />
      ) : activeSection === "browser" ? (
        <ResourceBrowserSection
          key={clusterId}
          clusterId={clusterId}
          clusterName={clusterName}
          tenantId={scope.tenantId}
          projectId={scope.projectId}
        />
      ) : activeSection === "policies" ? (
        <PolicySection
          key={clusterId}
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          tenantId={scope.tenantId}
          projectId={scope.projectId}
          onNamespaceScopeChange={setPoliciesNamespaced}
        />
      ) : activeSection === "autoscaling" ? (
        <AutoscalerSection
          key={clusterId}
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          tenantId={scope.tenantId}
          projectId={scope.projectId}
        />
      ) : activeSection === "storage" ? (
        <StorageSection
          key={clusterId}
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          tenantId={scope.tenantId}
          projectId={scope.projectId}
          onNamespaceScopeChange={setStorageNamespaced}
        />
      ) : activeSection === "configmaps" ? (
        <ConfigurationSection
          key={clusterId}
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          tenantId={scope.tenantId}
          projectId={scope.projectId}
        />
      ) : activeSection === "networking" ? (
        <NetworkingSection
          key={clusterId}
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          tenantId={scope.tenantId}
          projectId={scope.projectId}
        />
      ) : activeSection === "pods" ? (
        <PodSection
          key={clusterId}
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          tenantId={scope.tenantId}
          projectId={scope.projectId}
        />
      ) : (
        <WorkloadSection
          key={clusterId}
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          tenantId={scope.tenantId}
          projectId={scope.projectId}
        />
      )}
    </AppShell>
  );
}

function EmptyNotice({ title, description }: { title: string; description: string }) {
  return (
    <div className="flex h-full items-center justify-center text-center">
      <div className="max-w-sm">
        <p className="text-foreground text-sm font-medium">{title}</p>
        <p className="text-muted-foreground mt-1 text-[13px]">{description}</p>
      </div>
    </div>
  );
}
