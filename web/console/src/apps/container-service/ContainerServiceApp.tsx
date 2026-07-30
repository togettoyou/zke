import { useState } from "react";
import { FolderTree, Server } from "lucide-react";

import { useClusters } from "@/api/queries/clusters";
import { AppShell, ScopeRequired, type AppNavItem } from "@/apps/AppShell";
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

import { useTargetClusterStore } from "./cluster-store";
import { NamespaceSection } from "./NamespaceSection";
import { NodeSection } from "./NodeSection";

/**
 * Resource categories, the way every established Kubernetes console is
 * organised. Both entries here are cluster-scoped, which is why the toolbar
 * carries no Namespace selector yet: a control that scopes nothing would be
 * a control that does nothing. It arrives with the first namespaced list.
 */
const NAV: AppNavItem[] = [
  { id: "nodes", label: "节点", icon: Server },
  { id: "namespaces", label: "命名空间", icon: FolderTree },
];

/**
 * Container service is a single-cluster application: the operator picks one
 * target Cluster and every page and operation in the window is scoped to it.
 * Only online Clusters can be the target, because each request is executed by
 * that Cluster's Agent.
 */
export function ContainerServiceApp() {
  const scope = useScopeStore((state) => state.scope);
  const [section, setSection] = useState("nodes");
  const clusters = useClusters(scope.projectId, {
    limit: 100,
    offset: 0,
    status: "active",
  });
  const storedClusters = useTargetClusterStore((state) => state.byProject);
  const selectCluster = useTargetClusterStore((state) => state.select);

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

  if (!scope.projectId) {
    return <ScopeRequired />;
  }

  return (
    <AppShell
      nav={NAV}
      activeId={section}
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
          {clusterId ? (
            <span className="zke-mono text-subtle-foreground text-xs">{clusterId}</span>
          ) : null}
        </>
      }
      statusBar={
        clusterId ? "所有查询与变更均由所选集群的在线 Agent 定域执行" : "没有可执行请求的在线集群"
      }
    >
      {clusters.error ? (
        <ErrorState error={clusters.error} onRetry={() => void clusters.refetch()} />
      ) : clusters.isLoading ? (
        <LoadingState />
      ) : projectClusters.length === 0 ? (
        <NoTargetCluster
          title="该项目没有已接入的集群"
          description="请先在「集群接入管理」中接入并启用集群。"
        />
      ) : clusterId === "" ? (
        <NoTargetCluster
          title="该项目没有在线集群"
          description="容器服务的每个查询和变更都由目标集群的 Agent 定域执行，需要至少一个 Agent 处于在线状态。"
        />
      ) : section === "nodes" ? (
        <NodeSection
          clusterId={clusterId}
          clusterName={clusterName}
          tenantId={scope.tenantId}
          projectId={scope.projectId}
        />
      ) : (
        <NamespaceSection
          clusterId={clusterId}
          clusterName={clusterName}
          tenantId={scope.tenantId}
          projectId={scope.projectId}
        />
      )}
    </AppShell>
  );
}

function NoTargetCluster({ title, description }: { title: string; description: string }) {
  return (
    <div className="flex h-full items-center justify-center text-center">
      <div className="max-w-sm">
        <p className="text-foreground text-sm font-medium">{title}</p>
        <p className="text-muted-foreground mt-1 text-[13px]">{description}</p>
      </div>
    </div>
  );
}
