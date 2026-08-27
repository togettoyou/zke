import { useCallback, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { ColumnDef } from "@tanstack/react-table";
import { History, PackageSearch, Trash2, Undo2, Upload } from "lucide-react";

import {
  fetchHelmRelease,
  useHelmRelease,
  useHelmReleaseRevisions,
  useHelmReleases,
} from "@/api/queries/helm-releases";
import { useRollbackHelmRelease, useUninstallHelmRelease } from "@/api/queries/helm";
import type { KubernetesHelmRelease, KubernetesHelmReleaseDetail } from "@/api/types";
import { PageHeader, SectionToolbarActions } from "@/apps/AppShell";
import { DataTable } from "@/components/common/data-table";
import { DetailCard, DetailRow } from "@/components/common/detail";
import { notifyFailure } from "@/components/common/notify";
import { RefreshAction } from "@/components/common/refresh-action";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { ErrorState, LoadingState } from "@/components/common/state";
import { RelativeTime, StatusBadge } from "@/components/common/status";
import { YamlEditor } from "@/components/common/yaml-editor";
import { Button } from "@/components/ui/button";
import { Alert } from "@/components/ui/misc";
import { formatAbsolute } from "@/lib/time";
import { useSubmissionKey } from "@/lib/use-submission-key";
import { stringify as stringifyYaml } from "yaml";

import { SwitchField } from "./form";
import type { HelmAccess } from "./permissions";

/**
 * What is installed in one Namespace, and the three operations that change it
 * without choosing a new chart.
 *
 * Installing and upgrading open the form view instead, because both are a
 * decision about content: a values document to edit and a rendered manifest to
 * approve. Rolling back and uninstalling are decisions about a target — which
 * revision, and whether to keep the history — so they are confirmations rather
 * than pages.
 */
export function ReleaseSection({
  clusterId,
  clusterName,
  namespace,
  access,
  onBrowseCharts,
  onUpgrade,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  access: HelmAccess;
  onBrowseCharts: () => void;
  onUpgrade: (release: KubernetesHelmReleaseDetail) => void;
}) {
  const list = useHelmReleases(access.canRead ? clusterId : null, namespace);
  const [openRelease, setOpenRelease] = useState<string | null>(null);
  // The three operations that change a release, reachable from the row itself.
  //
  // A list of installed applications is where an operator already knows which
  // one they mean; making them open a detail page first in order to find the
  // button adds a step to every change and answers nothing they had not already
  // decided. The confirmations are unchanged — the dialogs below are the same
  // ones the detail view opens, so the safeguards do not depend on the route
  // taken to them.
  const [rollbackTarget, setRollbackTarget] = useState<KubernetesHelmRelease | null>(null);
  const [uninstallTarget, setUninstallTarget] = useState<string | null>(null);
  // Upgrading needs the release's values, which a listing does not carry: it
  // reads labels only, because decompressing every release to draw a table
  // would be a page of Secrets read for four columns. So the row asks for the
  // one release it is about, at the moment it is clicked, and opens the form
  // when it arrives.
  const queryClient = useQueryClient();
  const [upgradeTarget, setUpgradeTarget] = useState<string | null>(null);
  const openUpgrade = useCallback(
    async (name: string) => {
      setUpgradeTarget(name);
      try {
        onUpgrade(await fetchHelmRelease(queryClient, clusterId, namespace, name));
      } catch (error) {
        notifyFailure("读取 Helm 应用", error);
      } finally {
        setUpgradeTarget(null);
      }
    },
    [queryClient, clusterId, namespace, onUpgrade],
  );

  const columns = useMemo<ColumnDef<KubernetesHelmRelease, unknown>[]>(
    () => [
      {
        header: "名称",
        cell: ({ row }) => (
          <span className="text-foreground font-medium break-all">{row.original.name}</span>
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
      // Omitted entirely for a reader with none of the three permissions: an
      // always-present empty column reserves width for buttons that will never
      // appear, and pushes the columns that do say something.
      ...(access.canInstall || access.canUninstall
        ? [
            {
              id: "actions",
              header: "",
              size: 210,
              cell: ({ row }) => (
                /* The row itself opens the release, so every button here has to
                   stop the click from reaching it — otherwise confirming a
                   dialog would leave the detail page open behind it. */
                <div
                  className="flex justify-end gap-1"
                  onClick={(event) => event.stopPropagation()}
                  role="presentation"
                >
                  {access.canInstall && access.canBrowseCharts ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => void openUpgrade(row.original.name)}
                      disabled={upgradeTarget !== null}
                      aria-label={`升级 ${row.original.name}`}
                    >
                      <Upload />
                      升级
                    </Button>
                  ) : null}
                  {/* A first revision has nothing behind it to go back to, and
                      Helm would refuse; the button says so by not being there. */}
                  {access.canInstall && row.original.revision > 1 ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => setRollbackTarget(row.original)}
                      aria-label={`回滚 ${row.original.name}`}
                    >
                      <Undo2 />
                      回滚
                    </Button>
                  ) : null}
                  {access.canUninstall ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => setUninstallTarget(row.original.name)}
                      aria-label={`卸载 ${row.original.name}`}
                    >
                      <Trash2 />
                      卸载
                    </Button>
                  ) : null}
                </div>
              ),
            } satisfies ColumnDef<KubernetesHelmRelease, unknown>,
          ]
        : []),
    ],
    [access.canBrowseCharts, access.canInstall, access.canUninstall, openUpgrade, upgradeTarget],
  );

  if (!access.canRead) {
    return (
      <Alert tone="warning">
        Helm 的 Release 存放在 `helm.sh/release.v1` 类型的 Secret 中，读取它需要
        cluster.secret.read；kube-* 与 Agent 所在的命名空间还需要对应的独立命名空间权限。
      </Alert>
    );
  }

  if (openRelease) {
    return (
      <ReleaseDetailView
        clusterId={clusterId}
        clusterName={clusterName}
        namespace={namespace}
        name={openRelease}
        access={access}
        onBack={() => setOpenRelease(null)}
        onUpgrade={onUpgrade}
        onUninstalled={() => setOpenRelease(null)}
      />
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <SectionToolbarActions>
        <RefreshAction isFetching={list.isFetching} onRefresh={() => void list.refetch()} />
        {access.canInstall && access.canBrowseCharts ? (
          <Button size="sm" onClick={onBrowseCharts}>
            <PackageSearch />
            安装应用
          </Button>
        ) : null}
      </SectionToolbarActions>
      <DataTable
        columns={columns}
        data={list.data?.releases}
        isLoading={list.isLoading}
        isFetching={list.isFetching}
        error={list.error}
        onRetry={() => void list.refetch()}
        onRowClick={(release) => setOpenRelease(release.name)}
        rowKey={(release) => release.name}
        emptyTitle="该命名空间没有 Helm 应用"
        emptyDescription={`${namespace} 中没有以 Secret 存储的 Release。使用其他存储驱动（ConfigMap、SQL）的 Release 不会出现在这里。`}
      />
      <RollbackDialog
        open={rollbackTarget !== null}
        onOpenChange={(open) => setRollbackTarget(open ? rollbackTarget : null)}
        clusterId={clusterId}
        clusterName={clusterName}
        namespace={namespace}
        name={rollbackTarget?.name ?? ""}
        /* Zero is Helm's own "the revision before this one", which is what a
           quick rollback from a list means. Choosing a specific revision is a
           decision made against the history, and that lives in the detail. */
        revision={0}
        onDone={() => {
          setRollbackTarget(null);
          void list.refetch();
        }}
      />
      <UninstallDialog
        open={uninstallTarget !== null}
        onOpenChange={(open) => setUninstallTarget(open ? uninstallTarget : null)}
        clusterId={clusterId}
        clusterName={clusterName}
        namespace={namespace}
        name={uninstallTarget ?? ""}
        onDone={() => {
          setUninstallTarget(null);
          void list.refetch();
        }}
      />
    </div>
  );
}

function ReleaseDetailView({
  clusterId,
  clusterName,
  namespace,
  name,
  access,
  onBack,
  onUpgrade,
  onUninstalled,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  name: string;
  access: HelmAccess;
  onBack: () => void;
  onUpgrade: (release: KubernetesHelmReleaseDetail) => void;
  onUninstalled: () => void;
}) {
  // `undefined` reads whichever revision storage holds as newest; picking one
  // from the history pins it.
  const [revision, setRevision] = useState<number | undefined>(undefined);
  const detail = useHelmRelease(clusterId, namespace, name, revision);
  const history = useHelmReleaseRevisions(clusterId, namespace, name);
  const [rollbackTo, setRollbackTo] = useState<number | null>(null);
  const [uninstalling, setUninstalling] = useState(false);

  return (
    <div className="grid gap-3">
      <PageHeader
        title={name}
        onBack={onBack}
        actions={
          <>
            {access.canInstall && access.canBrowseCharts && detail.data ? (
              <Button
                size="sm"
                variant="secondary"
                onClick={() => onUpgrade(detail.data)}
                disabled={revision !== undefined}
              >
                <Upload />
                升级
              </Button>
            ) : null}
            {access.canUninstall ? (
              <Button size="sm" variant="danger" onClick={() => setUninstalling(true)}>
                <Trash2 />
                卸载
              </Button>
            ) : null}
          </>
        }
      />
      {detail.error ? (
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      ) : detail.isLoading || !detail.data ? (
        <LoadingState />
      ) : (
        <>
          {revision === undefined ? null : (
            <Alert tone="info">
              正在查看第 {revision} 次修订而不是当前修订。升级基于当前修订进行，
              需要先回到当前修订。
            </Alert>
          )}
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

          <DetailCard title="Values">
            <Alert tone="warning" className="mb-2">
              这些是安装或升级该 Release 时传入的 values，可能包含密码等凭证。 查看它需要 Secret
              读取权限，本次查看已写入审计。
            </Alert>
            {/* YAML rather than JSON: this is the same document the operator
                edits when installing and upgrading, and reading it back in a
                second notation makes comparing the two a translation exercise. */}
            <YamlEditor
              value={
                Object.keys(detail.data.values ?? {}).length === 0
                  ? "# 安装时没有覆盖任何默认值"
                  : stringifyYaml(detail.data.values)
              }
              onChange={() => {}}
              readOnly
              label={`${detail.data.name} 的 values`}
              className="h-72"
            />
          </DetailCard>

          {detail.data.notes ? (
            <DetailCard title="NOTES">
              {/* Plain text, not YAML and not Markdown: NOTES.txt is whatever
                  the chart's template rendered, and formatting it as either
                  would be a claim about it that the chart never made. It flows
                  with the page rather than scrolling inside a box — the page
                  already scrolls, and a second scrollbar inside the first is a
                  choice nobody wants to make while reading. */}
              <pre className="zke-mono text-muted-foreground border-border bg-surface-muted/60 rounded-control border p-2.5 text-xs leading-relaxed whitespace-pre-wrap">
                {detail.data.notes}
              </pre>
            </DetailCard>
          ) : null}

          <DetailCard title="渲染清单">
            {detail.data.manifest_truncated ? (
              <Alert tone="info" className="mb-2">
                清单超过服务端上限，只展示前一段；完整对象可在容器服务的「资源对象浏览器」中逐个查看。
              </Alert>
            ) : null}
            <YamlEditor
              value={detail.data.manifest || "# 该修订没有渲染出任何对象"}
              onChange={() => {}}
              readOnly
              label={`${detail.data.name} 的渲染清单`}
              className="h-96"
            />
          </DetailCard>

          <DetailCard title="修订历史">
            {history.error ? (
              <ErrorState error={history.error} onRetry={() => void history.refetch()} />
            ) : history.isLoading ? (
              <LoadingState />
            ) : (
              <div className="grid gap-1">
                {(history.data?.releases ?? []).map((item) => {
                  const current = item.revision === (history.data?.releases[0]?.revision ?? 0);
                  return (
                    <div
                      key={item.revision}
                      className="border-border flex flex-wrap items-center gap-2 border-b py-1.5 last:border-b-0"
                    >
                      <span className="zke-tnum text-foreground w-12 text-xs">
                        #{item.revision}
                      </span>
                      <StatusBadge kind="helmRelease" value={item.status || "unknown"} />
                      <RelativeTime
                        value={item.updated}
                        className="text-muted-foreground text-xs"
                      />
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
                      {access.canInstall && !current ? (
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => setRollbackTo(item.revision)}
                          aria-label={`回滚到第 ${item.revision} 次修订`}
                        >
                          <Undo2 />
                          回滚到此
                        </Button>
                      ) : null}
                    </div>
                  );
                })}
                <p className="text-subtle-foreground mt-1 text-xs">
                  这里是存储实际保留的修订：被 `--history-max` 清理掉的修订不再存在，
                  也无法回滚到它们。
                </p>
              </div>
            )}
          </DetailCard>
        </>
      )}

      <RollbackDialog
        open={rollbackTo !== null}
        onOpenChange={(open) => setRollbackTo(open ? rollbackTo : null)}
        clusterId={clusterId}
        clusterName={clusterName}
        namespace={namespace}
        name={name}
        revision={rollbackTo ?? 0}
        onDone={() => {
          setRollbackTo(null);
          setRevision(undefined);
        }}
      />
      <UninstallDialog
        open={uninstalling}
        onOpenChange={setUninstalling}
        clusterId={clusterId}
        clusterName={clusterName}
        namespace={namespace}
        name={name}
        onDone={() => {
          setUninstalling(false);
          onUninstalled();
        }}
      />
    </div>
  );
}

function RollbackDialog({
  open,
  onOpenChange,
  clusterId,
  clusterName,
  namespace,
  name,
  revision,
  onDone,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  clusterId: string;
  clusterName: string;
  namespace: string;
  name: string;
  revision: number;
  onDone: () => void;
}) {
  const rollback = useRollbackHelmRelease();
  const idempotencyKey = useSubmissionKey(open);
  const [wait, setWait] = useState(true);
  // Zero is Helm's own "the revision before the current one". It is a real
  // target, but it has no number to show, so the dialog names it rather than
  // printing "第 0 次修订" — which is a revision that does not exist.
  const target = revision > 0 ? `第 ${revision} 次修订` : "上一个修订";

  return (
    <SensitiveActionDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`回滚 ${name} 到${target}`}
      description="回滚会重放该修订记录下来的对象：缺失的重新创建，改变的替换回去。它本身也产生一个新的修订，而不是抹掉中间发生过的事。"
      scopeLines={[
        { label: "集群", name: clusterName, id: clusterId },
        { label: "命名空间", name: namespace },
        { label: "Release", name },
      ]}
      impacts={[
        `该 Release 拥有的对象会被改回${target}的状态`,
        "回滚会写入一个新的修订，修订号继续递增",
        "被回滚覆盖的配置不会自动保留，需要时请先记录当前 values",
      ]}
      confirmationText={name}
      confirmLabel="回滚"
      pending={rollback.isPending}
      error={rollback.error}
      onConfirm={() =>
        rollback.mutate(
          {
            clusterId,
            namespace,
            name,
            revision,
            wait,
            dryRun: false,
            idempotencyKey,
          },
          {
            onSuccess: onDone,
            onError: (error) => notifyFailure("回滚 Helm 应用", error),
          },
        )
      }
    >
      <SwitchField
        id="helm-rollback-wait"
        checked={wait}
        onChange={setWait}
        label="等待对象就绪"
        hint="等到回滚后的对象进入就绪状态再返回，失败会如实报告。"
      />
    </SensitiveActionDialog>
  );
}

function UninstallDialog({
  open,
  onOpenChange,
  clusterId,
  clusterName,
  namespace,
  name,
  onDone,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  clusterId: string;
  clusterName: string;
  namespace: string;
  name: string;
  onDone: () => void;
}) {
  const uninstall = useUninstallHelmRelease();
  const idempotencyKey = useSubmissionKey(open);
  // Keeping the history is the default because it is the reversible choice: it
  // costs one Secret per retained revision and is the only thing that makes a
  // later rollback possible.
  const [keepHistory, setKeepHistory] = useState(true);

  return (
    <SensitiveActionDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`卸载 ${name}`}
      description="卸载会删除该 Release 拥有的全部对象。它不会删除 Chart 没有创建的东西，也不会删除已经被别人接管的对象。"
      scopeLines={[
        { label: "集群", name: clusterName, id: clusterId },
        { label: "命名空间", name: namespace },
        { label: "Release", name },
      ]}
      impacts={[
        "该 Release 创建的工作负载、服务、配置与存储声明会被删除",
        keepHistory
          ? "修订历史会保留，之后仍可回滚"
          : "修订历史一并删除，之后无法回滚到任何一次修订",
        "由 Chart 创建的 PersistentVolumeClaim 一旦删除，其中的数据由存储类的回收策略决定",
      ]}
      confirmationText={name}
      confirmLabel="卸载"
      destructive
      pending={uninstall.isPending}
      error={uninstall.error}
      onConfirm={() =>
        uninstall.mutate(
          {
            clusterId,
            namespace,
            name,
            keepHistory,
            dryRun: false,
            idempotencyKey,
          },
          {
            onSuccess: onDone,
            onError: (error) => notifyFailure("卸载 Helm 应用", error),
          },
        )
      }
    >
      <SwitchField
        id="helm-uninstall-keep-history"
        checked={keepHistory}
        onChange={setKeepHistory}
        label="保留修订历史"
        hint="保留下来的历史正是之后回滚所需要的；取消勾选会连同历史一起删除。"
      />
    </SensitiveActionDialog>
  );
}
