import { useMemo, useState } from "react";
import { Bell, Box, FolderTree, LayoutDashboard, Layers, Server } from "lucide-react";

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

import { EventSection } from "./EventSection";
import { NamespaceSection } from "./NamespaceSection";
import { NodeSection } from "./NodeSection";
import { OverviewSection } from "./OverviewSection";
import { PodSection } from "./PodSection";
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
  { id: "events", label: "事件", icon: Bell },
];

/** Sections whose queries are scoped by a Namespace as well as by a Cluster. */
const NAMESPACED_SECTIONS = new Set(["workloads", "pods", "events"]);

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
  const nav = useMemo(
    () => NAV.map((item) => (item.id === "events" ? { ...item, hidden: !canReadEvents } : item)),
    [canReadEvents],
  );
  const activeSection = section === "events" && !canReadEvents ? "overview" : section;

  // Only the namespaced sections need the picker, and only they pay for the
  // query behind it. A control that scopes nothing would be a control that does
  // nothing, so the cluster-scoped sections do not show one.
  const namespaced = NAMESPACED_SECTIONS.has(activeSection);
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
      statusBar={
        clusterId
          ? namespacesTruncated
            ? `所有查询与变更均由所选集群的在线 Agent 定域执行 · 命名空间选择器只列出前 ${NAMESPACE_PICKER_LIMIT} 个`
            : "所有查询与变更均由所选集群的在线 Agent 定域执行"
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
      ) : activeSection === "overview" ? (
        <OverviewSection key={clusterId} clusterId={clusterId} clusterName={clusterName} />
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
      ) : namespaces.error ? (
        <ErrorState error={namespaces.error} onRetry={() => void namespaces.refetch()} />
      ) : namespaces.isLoading ? (
        <LoadingState />
      ) : namespace === "" ? (
        <EmptyNotice
          title="该集群没有可见的命名空间"
          description="工作负载、Pod 和事件按命名空间定域查询，需要目标集群中至少存在一个当前身份可见的命名空间。"
        />
      ) : activeSection === "events" ? (
        <EventSection
          key={`${clusterId}/${namespace}`}
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
        />
      ) : activeSection === "pods" ? (
        <PodSection
          key={`${clusterId}/${namespace}`}
          clusterId={clusterId}
          clusterName={clusterName}
          namespace={namespace}
          tenantId={scope.tenantId}
          projectId={scope.projectId}
        />
      ) : (
        <WorkloadSection
          key={`${clusterId}/${namespace}`}
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
