import { Fragment, useState } from "react";
import { Library, PackageSearch, ShipWheel } from "lucide-react";
import { toast } from "sonner";

import { useClusters } from "@/api/queries/clusters";
import { useNamespaces } from "@/api/queries/namespaces";
import type { KubernetesHelmReleaseDetail } from "@/api/types";
import { AppShell, ScopeRequired, type AppNavItem } from "@/apps/AppShell";
import {
  useTargetClusterStore,
  useTargetNamespaceStore,
} from "@/apps/container-service/selection-store";
import type { AppComponentProps } from "@/apps/types";
import { useSessionContext } from "@/auth/session-context";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { StatusBadge } from "@/components/common/status";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useScopeStore } from "@/scope/scope-store";
import { stringify as stringifyYaml } from "yaml";

import { ChartCatalogSection } from "./ChartCatalogSection";
import { helmAccess } from "./permissions";
import { ReleaseFormView, type ReleaseFormMode, type ReleaseFormTarget } from "./ReleaseFormView";
import { ReleaseSection } from "./ReleaseSection";
import { RepositorySection } from "./RepositorySection";

/**
 * Helm applications.
 *
 * A single-cluster application, like the container service: the operator picks
 * one target Cluster and one Namespace, and every release operation in the
 * window happens there. The chart catalogue is the exception and is deliberately
 * not scoped to either — it is one platform-wide list of what may be installed
 * anywhere, and duplicating it per Cluster would mean deciding separately in
 * every Cluster whether a public repository is trusted.
 *
 * The read-only release view in 容器服务 answers "what is installed next to this
 * workload". This application is where releases are changed, because changing
 * one needs a workspace the resource lists there do not have: a chart to choose,
 * a values document to edit, and a rendered manifest to approve before anything
 * is written.
 */
const NAV: AppNavItem[] = [
  { id: "releases", label: "已安装应用", icon: ShipWheel },
  { id: "catalog", label: "Chart 目录", icon: PackageSearch },
  { id: "repositories", label: "Chart 仓库", icon: Library },
];

const NAMESPACE_PICKER_LIMIT = 500;

/** Sections whose target is one Namespace of one Cluster. */
const SCOPED_SECTIONS = new Set(["releases", "catalog"]);

export function HelmApp({ windowId }: Pick<AppComponentProps, "windowId">) {
  void windowId;
  const scope = useScopeStore((state) => state.scope);
  const { permissions } = useSessionContext();
  const [section, setSection] = useState("releases");
  const [form, setForm] = useState<{ mode: ReleaseFormMode; target: ReleaseFormTarget } | null>(
    null,
  );

  const clusters = useClusters(scope.projectId, { limit: 100, offset: 0, status: "active" });
  const storedClusters = useTargetClusterStore((state) => state.selections);
  const selectCluster = useTargetClusterStore((state) => state.select);
  const storedNamespaces = useTargetNamespaceStore((state) => state.selections);
  const selectNamespace = useTargetNamespaceStore((state) => state.select);

  const projectClusters = clusters.data?.clusters ?? [];
  // Offline Clusters stay in the picker — disabled — so an operator looking for
  // one sees that it is known and offline rather than silently missing. Every
  // request here is executed by that Cluster's Agent, so an offline Cluster
  // cannot be the target.
  const onlineClusters = projectClusters.filter(
    (cluster) => cluster.connection.status === "online",
  );
  const storedCluster = scope.projectId ? storedClusters[scope.projectId] : undefined;
  const clusterId = onlineClusters.some((cluster) => cluster.id === storedCluster)
    ? (storedCluster as string)
    : (onlineClusters[0]?.id ?? "");
  const selectedCluster = projectClusters.find((cluster) => cluster.id === clusterId);
  const clusterName = selectedCluster?.name ?? clusterId;
  const agentNamespace = selectedCluster?.agent_namespace ?? "";

  const scoped = SCOPED_SECTIONS.has(section);
  const namespaces = useNamespaces(scoped && clusterId ? clusterId : null, {
    limit: NAMESPACE_PICKER_LIMIT,
  });
  const namespaceNames = (namespaces.data?.namespaces ?? []).map((item) => item.name);
  const storedNamespace = clusterId ? storedNamespaces[clusterId] : undefined;
  const namespace = namespaceNames.includes(storedNamespace ?? "")
    ? (storedNamespace as string)
    : namespaceNames.includes("default")
      ? "default"
      : (namespaceNames[0] ?? "");

  // Recomputed on every render rather than memoised: it is a handful of set
  // lookups over the session's capabilities, and the answer changes with the
  // Namespace picker — a protected Namespace needs a grant an ordinary one does
  // not, so a stale answer here would offer a button the Server refuses.
  const access = helmAccess(
    permissions,
    { namespace, agentNamespace },
    { type: "project", tenantId: scope.tenantId, projectId: scope.projectId },
  );

  /**
   * Upgrading starts from the running revision rather than from a blank form.
   *
   * Its current values seed the editor, so an upgrade that only changes the
   * chart version does not silently drop everything that was configured, and
   * its rendered manifest becomes the "before" side of the preview diff. Which
   * repository the chart came from is not recorded by Helm, so the form asks.
   */
  const openUpgrade = (release: KubernetesHelmReleaseDetail) => {
    setForm({
      mode: "upgrade",
      target: {
        clusterId,
        clusterName,
        namespace,
        repositoryId: "",
        chart: release.chart_name,
        version: "",
        releaseName: release.name,
        currentValues:
          Object.keys(release.values ?? {}).length === 0 ? "" : stringifyYaml(release.values),
        currentManifest: release.manifest,
      },
    });
  };

  // Both catalogue sections read the same platform-wide list, so both are
  // hidden without the permission that reads it — an operator who may look at
  // releases but not at the catalogue would otherwise find two pages whose
  // every request comes back refused. The Server enforces it regardless.
  const nav = NAV.map((item) =>
    item.id === "catalog" || item.id === "repositories"
      ? { ...item, hidden: !access.canBrowseCharts }
      : item,
  );
  const activeSection =
    (section === "catalog" || section === "repositories") && !access.canBrowseCharts
      ? "releases"
      : section;

  if (!scope.projectId) {
    return <ScopeRequired />;
  }

  const body = () => {
    if (clusters.error) {
      return <ErrorState error={clusters.error} onRetry={() => void clusters.refetch()} />;
    }
    if (clusters.isLoading) {
      return <LoadingState />;
    }
    if (activeSection === "repositories") {
      return <RepositorySection canManage={access.canManageRepositories} />;
    }
    if (!clusterId) {
      return (
        <EmptyState
          title="没有可执行请求的在线集群"
          description="Helm 的渲染与写入由目标集群的 Agent 执行，因此需要先有一个在线集群。"
        />
      );
    }
    if (namespaces.isLoading) {
      return <LoadingState label="加载命名空间…" />;
    }
    if (!namespace) {
      return (
        <EmptyState
          title="该集群没有可用的命名空间"
          description="Release 安装在具体的命名空间中，请先在容器服务里创建一个。"
        />
      );
    }
    if (form) {
      return (
        <ReleaseFormView
          mode={form.mode}
          target={form.target}
          canInstallClusterScoped={access.canInstallClusterScoped}
          onBack={() => setForm(null)}
          onDone={(operation) => {
            setForm(null);
            setSection("releases");
            const revision = operation.report?.revision ?? 0;
            toast.success(
              form.mode === "install"
                ? `已安装 ${operation.release_name}，当前修订 ${revision}`
                : `已升级 ${operation.release_name} 到修订 ${revision}`,
            );
          }}
        />
      );
    }
    if (activeSection === "catalog") {
      return (
        <ChartCatalogSection
          namespace={namespace}
          canInstall={access.canInstall}
          onInstall={(choice) =>
            setForm({
              mode: "install",
              target: {
                clusterId,
                clusterName,
                namespace,
                repositoryId: choice.repositoryId,
                chart: choice.chart,
                version: choice.version,
              },
            })
          }
        />
      );
    }
    return (
      <ReleaseSection
        clusterId={clusterId}
        clusterName={clusterName}
        namespace={namespace}
        access={access}
        onBrowseCharts={() => setSection("catalog")}
        onUpgrade={openUpgrade}
      />
    );
  };

  return (
    <AppShell
      nav={nav}
      activeId={activeSection}
      onNavigate={(id) => {
        setSection(id);
        setForm(null);
      }}
      /*
       * The repositories section has no scope pickers, but it must not pass an
       * empty toolbar: the shell renders the toolbar row only when there is
       * something in it, and that row is what carries the slot a section
       * portals its own actions into. A null toolbar therefore takes 添加仓库
       * and 刷新 with it. What stands in its place says the one thing the
       * missing pickers would otherwise have to be read for — that this list is
       * not scoped to the Cluster the other two sections are looking at.
       */
      toolbar={
        activeSection === "repositories" ? (
          <span className="text-muted-foreground text-xs">平台级 Chart 目录，对所有集群生效</span>
        ) : (
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
              <>
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
        )
      }
      scope={
        activeSection === "repositories"
          ? "平台级 Chart 目录"
          : clusterId
            ? `${clusterName}${namespace ? ` · ${namespace}` : ""}`
            : undefined
      }
      statusBar={
        activeSection === "repositories"
          ? undefined
          : clusterId
            ? namespaces.data?.continue_token
              ? `命名空间选择器只列出前 ${NAMESPACE_PICKER_LIMIT} 个`
              : undefined
            : "没有可执行请求的在线集群"
      }
    >
      {/*
       * A Fragment rather than a wrapping box. The Cluster is a hard identity
       * boundary — a release named `checkout` exists in many Clusters and is a
       * different application in each — so the key stays, but a `h-full` box
       * around it costs the page its bottom padding: content taller than a
       * full-height child overflows that child, and a scroll container adds its
       * padding-bottom to the scrollable area only for content that grew the
       * child rather than escaped it. The sections that want to fill the work
       * area ask for `h-full` themselves.
       */}
      <Fragment key={`${clusterId}:${namespace}`}>{body()}</Fragment>
    </AppShell>
  );
}
