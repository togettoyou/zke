import { useMemo, useState } from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { History, ShipWheel } from "lucide-react";

import {
  useHelmRelease,
  useHelmReleaseRevisions,
  useHelmReleases,
} from "@/api/queries/helm-releases";
import type { KubernetesHelmRelease, KubernetesHelmReleaseDetail } from "@/api/types";
import { PageHeader, SectionToolbarActions } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { DataTable } from "@/components/common/data-table";
import { DetailCard, DetailRow } from "@/components/common/detail";
import { RefreshAction } from "@/components/common/refresh-action";
import { ErrorState, LoadingState } from "@/components/common/state";
import { RelativeTime, StatusBadge } from "@/components/common/status";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/misc";
import { formatAbsolute } from "@/lib/time";

import { canUseProtectedNamespace } from "./namespace-permissions";
import type { ClusterSectionProps } from "./types";

type HelmReleaseSectionProps = ClusterSectionProps & {
  /** The Namespace every query in this section is scoped to. */
  namespace: string;
  /** Opens the standalone Helm application, where the writes live. */
  onOpenHelmApp: () => void;
};

/**
 * The Helm releases installed in one Namespace.
 *
 * A release is not a Kubernetes kind, and nothing else in this application can
 * answer for it: the resource browser shows the Deployment a chart produced, and
 * the Deployment carries no reference back to the release that produced it. This
 * section reads Helm's own storage — one Secret per revision — and reports what
 * is in it.
 *
 * It is the reading half on purpose, not a half-finished version of the other
 * one. Installing, upgrading, rolling back and uninstalling need Helm's own
 * rendering engine and a workspace of their own — a chart repository, a values
 * editor, a rendered diff — none of which belongs inside a Namespace-scoped
 * resource list. Those live in the standalone Helm application, which the header
 * links to; here an operator answers "what is installed, at what version, with
 * what values" without leaving the workload they were looking at.
 */
export function HelmReleaseSection({
  clusterId,
  agentNamespace,
  namespace,
  tenantId,
  projectId,
  onOpenHelmApp,
}: HelmReleaseSectionProps) {
  const { permissions } = useSessionContext();
  const projectScope = { type: "project" as const, tenantId, projectId };
  const protectedAccess = canUseProtectedNamespace(
    permissions,
    { namespace, agentNamespace },
    projectScope,
  );
  // A release holds the values its chart was installed with, which routinely
  // include a credential. That makes reading one a Secret read, and the Server
  // requires the Secret permission accordingly — so an operator without it is
  // shown why rather than an empty list.
  const canRead = protectedAccess && permissions.can("cluster.secret.read", projectScope);
  const list = useHelmReleases(canRead ? clusterId : null, namespace);
  const [detailName, setDetailName] = useState<string | null>(null);

  const columns = useMemo<ColumnDef<KubernetesHelmRelease, unknown>[]>(
    () => [
      {
        header: "名称",
        cell: ({ row }) => (
          <div className="flex flex-col gap-0.5">
            <span className="text-foreground font-medium break-all">{row.original.name}</span>
            <span className="zke-mono text-subtle-foreground text-xs break-all">
              {row.original.secret_name}
            </span>
          </div>
        ),
      },
      {
        header: "状态",
        size: 120,
        cell: ({ row }) => (
          <StatusBadge kind="helmRelease" value={row.original.status || "unknown"} />
        ),
      },
      {
        header: "修订",
        size: 90,
        cell: ({ row }) => <span className="zke-tnum">{row.original.revision}</span>,
      },
      {
        header: "更新时间",
        size: 150,
        cell: ({ row }) => (
          <RelativeTime value={row.original.updated} className="text-muted-foreground" />
        ),
      },
    ],
    [],
  );

  if (!canRead) {
    return (
      <div className="flex h-full min-h-0 flex-col gap-3">
        <Alert tone="warning">
          Helm Release 存放在 `helm.sh/release.v1` 类型的 Secret 中，查看它需要 Secret
          读取权限及对应的独立命名空间权限。
        </Alert>
      </div>
    );
  }

  if (detailName) {
    return (
      <HelmReleaseDetailView
        clusterId={clusterId}
        namespace={namespace}
        name={detailName}
        onBack={() => setDetailName(null)}
      />
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <SectionToolbarActions>
        <RefreshAction isFetching={list.isFetching} onRefresh={() => void list.refetch()} />
        {/* The way out to the application that manages releases, rather than a
            notice explaining what this page does not do. */}
        <Button size="sm" variant="secondary" onClick={onOpenHelmApp}>
          <ShipWheel />
          Helm 应用
        </Button>
      </SectionToolbarActions>
      <DataTable
        columns={columns}
        data={list.data?.releases}
        isLoading={list.isLoading}
        isFetching={list.isFetching}
        error={list.error}
        onRetry={() => void list.refetch()}
        onRowClick={(release) => setDetailName(release.name)}
        rowKey={(release) => release.name}
        emptyTitle="该命名空间没有 Helm Release"
        emptyDescription={`${namespace} 中没有以 Secret 存储的 Helm Release。使用其他存储驱动（ConfigMap、SQL）的 Release 不会出现在这里。`}
      />
    </div>
  );
}

function HelmReleaseDetailView({
  clusterId,
  namespace,
  name,
  onBack,
}: {
  clusterId: string;
  namespace: string;
  name: string;
  onBack: () => void;
}) {
  // `undefined` reads whichever revision storage holds as newest; picking one
  // from the history pins it.
  const [revision, setRevision] = useState<number | undefined>(undefined);
  const detail = useHelmRelease(clusterId, namespace, name, revision);
  const history = useHelmReleaseRevisions(clusterId, namespace, name);

  return (
    <div className="grid gap-3">
      <PageHeader title={name} onBack={onBack} />
      {detail.error ? (
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      ) : detail.isLoading || !detail.data ? (
        <LoadingState />
      ) : (
        <>
          <div className="grid gap-3 @2xl:grid-cols-2">
            <DetailCard title="概览">
              <DetailRow label="名称" value={detail.data.name} />
              <DetailRow label="命名空间" value={detail.data.namespace} />
              <DetailRow
                label="状态"
                value={<StatusBadge kind="helmRelease" value={detail.data.status || "unknown"} />}
              />
              <DetailRow
                label="修订"
                value={<span className="zke-tnum">{detail.data.revision}</span>}
              />
              <DetailRow label="说明" value={detail.data.description || "—"} />
              <DetailRow
                label="首次部署"
                value={
                  detail.data.first_deployed ? formatAbsolute(detail.data.first_deployed) : "—"
                }
              />
              <DetailRow
                label="最近部署"
                value={detail.data.last_deployed ? formatAbsolute(detail.data.last_deployed) : "—"}
              />
              <DetailRow
                label="存储对象"
                value={
                  <span className="zke-mono text-xs break-all">{detail.data.secret_name}</span>
                }
              />
            </DetailCard>
            <DetailCard title="Chart">
              <DetailRow label="名称" value={detail.data.chart_name || "—"} />
              <DetailRow
                label="版本"
                value={<span className="zke-mono text-xs">{detail.data.chart_version || "—"}</span>}
              />
              <DetailRow
                label="应用版本"
                value={<span className="zke-mono text-xs">{detail.data.app_version || "—"}</span>}
              />
              <DetailRow label="描述" value={detail.data.chart_description || "—"} />
            </DetailCard>
          </div>

          <HelmReleaseValues release={detail.data} />

          {detail.data.notes ? (
            <DetailCard title="NOTES">
              <pre className="zke-mono text-muted-foreground max-h-72 overflow-auto text-xs whitespace-pre-wrap">
                {detail.data.notes}
              </pre>
            </DetailCard>
          ) : null}

          <DetailCard title="渲染清单">
            {detail.data.manifest_truncated ? (
              <Alert tone="info" className="mb-2">
                清单超过服务端上限，只展示前一段；完整对象可在「资源对象浏览器」中逐个查看。
              </Alert>
            ) : null}
            <pre className="zke-mono text-muted-foreground max-h-96 overflow-auto text-xs whitespace-pre">
              {detail.data.manifest || "（该修订没有渲染出任何对象）"}
            </pre>
          </DetailCard>

          <DetailCard title="修订历史">
            {history.error ? (
              <ErrorState error={history.error} onRetry={() => void history.refetch()} />
            ) : history.isLoading ? (
              <LoadingState />
            ) : (
              <div className="grid gap-1">
                {(history.data?.releases ?? []).map((item) => (
                  <div
                    key={item.revision}
                    className="border-border flex flex-wrap items-center gap-2 border-b py-1.5 last:border-b-0"
                  >
                    <span className="zke-tnum text-foreground w-12 text-xs">#{item.revision}</span>
                    <StatusBadge kind="helmRelease" value={item.status || "unknown"} />
                    <RelativeTime value={item.updated} className="text-muted-foreground text-xs" />
                    <span className="grow" />
                    {item.revision === detail.data.revision ? (
                      <span className="text-subtle-foreground text-xs">当前查看</span>
                    ) : (
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => setRevision(item.revision)}
                        aria-label={`查看第 ${item.revision} 次修订`}
                      >
                        <History />
                        查看
                      </Button>
                    )}
                  </div>
                ))}
                <p className="text-subtle-foreground mt-1 text-xs">
                  这里是存储实际保留的修订：被 `--history-max` 清理掉的修订不再存在。
                </p>
              </div>
            )}
          </DetailCard>
        </>
      )}
    </div>
  );
}

/**
 * The values the release was installed with.
 *
 * Shown as JSON rather than as rows: a chart's values are an arbitrarily nested
 * document, and flattening it into key-value pairs would lose the shape an
 * operator needs to compare against their own values file.
 */
function HelmReleaseValues({ release }: { release: KubernetesHelmReleaseDetail }) {
  const empty = Object.keys(release.values ?? {}).length === 0;
  return (
    <DetailCard title="Values">
      <Alert tone="warning" className="mb-2">
        这些是安装或升级该 Release 时传入的 values，可能包含密码等凭证。查看它需要 Secret
        读取权限，本次查看已写入审计。
      </Alert>
      <pre className="zke-mono text-muted-foreground max-h-72 overflow-auto text-xs whitespace-pre">
        {empty ? "（安装时没有覆盖任何默认值）" : JSON.stringify(release.values, null, 2)}
      </pre>
    </DetailCard>
  );
}
