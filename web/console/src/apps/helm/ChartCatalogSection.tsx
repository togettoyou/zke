import { useMemo, useState } from "react";
import { ExternalLink, RefreshCw, ShipWheel } from "lucide-react";

import {
  useHelmChart,
  useHelmChartVersions,
  useHelmCharts,
  useHelmRepositories,
  useRefreshHelmCharts,
} from "@/api/queries/helm";
import type { HelmChartSummary } from "@/api/types";
import { PageHeader, SectionToolbarActions } from "@/apps/AppShell";
import { DetailCard, DetailRow } from "@/components/common/detail";
import { Markdown } from "@/components/common/markdown";
import { notifyFailure } from "@/components/common/notify";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { RelativeTime } from "@/components/common/status";
import { YamlEditor } from "@/components/common/yaml-editor";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert, Card, CardTitle } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { HintTooltip } from "@/components/ui/tooltip";

import { ChartIcon, Field } from "./form";

/**
 * Choosing what to install.
 *
 * The catalogue is a list of repositories an administrator curated and the
 * charts they publish. There is no field for a chart URL anywhere in this
 * application, and that is the point: "install this chart" must not be a way to
 * make the Server fetch from an address the caller chose.
 *
 * Searching happens on the Server. A public index runs to thousands of charts,
 * and downloading all of them into the browser to filter locally is the download
 * the Server's cache exists to avoid — which is also why the listing says how
 * old that cache is and offers to read the index again.
 */
export function ChartCatalogSection({
  namespace,
  canInstall,
  onInstall,
}: {
  /** The Namespace an install would go into, shown so the target is never implicit. */
  namespace: string;
  canInstall: boolean;
  onInstall: (choice: { repositoryId: string; chart: string; version: string }) => void;
}) {
  const repositories = useHelmRepositories();
  const enabled = useMemo(
    () => (repositories.data?.repositories ?? []).filter((item) => item.enabled),
    [repositories.data],
  );
  const [repositoryId, setRepositoryId] = useState("");
  const [search, setSearch] = useState("");
  const [openChart, setOpenChart] = useState<string | null>(null);

  // Resolved on every render rather than written back into state: the stored
  // choice only counts while that repository still exists and is still switched
  // on, and a repository that was disabled while the page was open must fall
  // back on the spot rather than one render later.
  const activeRepository = enabled.some((item) => item.id === repositoryId)
    ? repositoryId
    : (enabled[0]?.id ?? "");

  const term = search.trim();
  const charts = useHelmCharts(activeRepository || null, term);
  const refresh = useRefreshHelmCharts();

  if (repositories.error) {
    return <ErrorState error={repositories.error} onRetry={() => void repositories.refetch()} />;
  }
  if (repositories.isLoading) {
    return <LoadingState />;
  }
  if (enabled.length === 0) {
    return (
      <EmptyState
        title="没有可用的 Chart 仓库"
        description="平台上还没有启用的 Chart 仓库。持有 helm.repository.manage 的管理员可以在「Chart 仓库」中添加一个。"
      />
    );
  }

  if (openChart) {
    return (
      <ChartDetailView
        repositoryId={activeRepository}
        chart={openChart}
        namespace={namespace}
        canInstall={canInstall}
        onBack={() => setOpenChart(null)}
        onInstall={(version) =>
          onInstall({ repositoryId: activeRepository, chart: openChart, version })
        }
      />
    );
  }

  const reread = () =>
    refresh.mutate(
      { repositoryId: activeRepository, search: term },
      { onError: (error) => notifyFailure("重新拉取 Chart 索引", error) },
    );

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      <SectionToolbarActions>
        {/* Not a plain refetch: that would answer from the Server's cached index
            and report the same catalogue back. This discards the cache and reads
            the repository again, which is what a refresh has to mean when the
            thing on screen is a cached copy of somebody else's list. */}
        <HintTooltip label="丢弃服务端缓存并重新拉取该仓库的 index.yaml">
          <Button
            size="sm"
            variant="secondary"
            onClick={reread}
            disabled={!activeRepository || refresh.isPending}
          >
            <RefreshCw className={refresh.isPending ? "animate-spin" : undefined} />
            重新拉取索引
          </Button>
        </HintTooltip>
      </SectionToolbarActions>

      <div className="grid items-start gap-x-4 gap-y-3 @md:grid-cols-[16rem_1fr]">
        <Field label="仓库" htmlFor="helm-repository">
          <Select value={activeRepository} onValueChange={setRepositoryId}>
            <SelectTrigger id="helm-repository" className="w-full">
              <SelectValue placeholder="选择仓库" />
            </SelectTrigger>
            <SelectContent>
              {enabled.map((item) => (
                <SelectItem key={item.id} value={item.id}>
                  {item.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label="搜索" htmlFor="helm-chart-search">
          <Input
            id="helm-chart-search"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder="按名称、描述或关键字过滤"
            autoComplete="off"
            spellCheck={false}
          />
        </Field>
      </div>

      {charts.error ? (
        <ErrorState error={charts.error} onRetry={() => void charts.refetch()} />
      ) : charts.isLoading ? (
        <LoadingState label="读取仓库索引…" />
      ) : (charts.data?.charts.length ?? 0) === 0 ? (
        <EmptyState
          title="没有匹配的 Chart"
          description={
            term ? `该仓库中没有匹配「${term}」的 Chart。` : "该仓库的索引中没有任何 Chart。"
          }
        />
      ) : (
        <div className="flex min-h-0 flex-1 flex-col gap-2">
          {/*
           * The cache is stated rather than left to be discovered. A catalogue
           * read minutes ago is normal and fine; not knowing that it was is what
           * turns "my chart is missing" into a bug report.
           */}
          <p className="text-subtle-foreground flex flex-wrap items-center gap-1 text-xs">
            共 {charts.data?.total} 个 Chart，已展示 {charts.data?.charts.length} 个 · 索引读取于
            {charts.data ? <RelativeTime value={charts.data.fetched_at} /> : "—"}
          </p>
          <div className="min-h-0 flex-1 overflow-auto">
            <div className="grid gap-2 pb-1 @2xl:grid-cols-2 @3xl:grid-cols-3">
              {(charts.data?.charts ?? []).map((chart) => (
                <ChartCard key={chart.name} chart={chart} onOpen={() => setOpenChart(chart.name)} />
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function ChartCard({ chart, onOpen }: { chart: HelmChartSummary; onOpen: () => void }) {
  return (
    <Card
      className="hover:border-border-strong zke-focus flex cursor-pointer items-start gap-3 p-3 text-left transition-[border-color] duration-150"
      asChild
    >
      <button type="button" onClick={onOpen} aria-label={`查看 ${chart.name}`}>
        <ChartIcon url={chart.icon_url} />
        <span className="grid min-w-0 gap-1">
          <span className="flex flex-wrap items-center gap-1.5">
            <span className="text-foreground text-[13px] font-medium break-all">{chart.name}</span>
            {chart.deprecated ? <Badge tone="warning">已弃用</Badge> : null}
          </span>
          <span className="zke-mono text-subtle-foreground text-xs">
            {chart.version}
            {chart.app_version ? ` · app ${chart.app_version}` : ""}
          </span>
          <span className="text-muted-foreground line-clamp-2 text-xs leading-relaxed">
            {chart.description || "（该 Chart 没有描述）"}
          </span>
        </span>
      </button>
    </Card>
  );
}

/**
 * One chart, read before it is installed.
 *
 * The README and the chart's own values.yaml are here rather than in the install
 * form: they are what an operator reads to decide, and the form is where they
 * act on the decision.
 */
function ChartDetailView({
  repositoryId,
  chart,
  namespace,
  canInstall,
  onBack,
  onInstall,
}: {
  repositoryId: string;
  chart: string;
  namespace: string;
  canInstall: boolean;
  onBack: () => void;
  onInstall: (version: string) => void;
}) {
  const [version, setVersion] = useState("");
  const detail = useHelmChart(repositoryId, chart, version);
  const versions = useHelmChartVersions(repositoryId, chart);

  return (
    <div className="grid gap-3">
      <PageHeader
        title={chart}
        onBack={onBack}
        actions={
          canInstall ? (
            <Button size="sm" onClick={() => onInstall(version)} disabled={!detail.data}>
              <ShipWheel />
              安装到 {namespace}
            </Button>
          ) : undefined
        }
      />
      {detail.error ? (
        <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
      ) : detail.isLoading || !detail.data ? (
        <LoadingState label="下载 Chart…" />
      ) : (
        <div className="grid max-w-4xl gap-3">
          {canInstall ? null : (
            <Alert tone="warning">
              你可以浏览这个 Chart，但没有在该命名空间安装它的权限。安装同时需要
              cluster.helm.manage、资源创建与更新权限，以及 Secret 管理权限——Helm 的 Release
              存储本身就是一个 Secret。
            </Alert>
          )}
          {detail.data.deprecated ? (
            <Alert tone="warning">该 Chart 已被仓库标记为弃用，作者可能不再维护它。</Alert>
          ) : null}

          <Card className="flex items-start gap-3 p-4">
            <ChartIcon url={detail.data.icon_url} size="lg" />
            <div className="grid min-w-0 gap-1">
              <div className="flex flex-wrap items-baseline gap-2">
                <span className="text-foreground text-[15px] font-semibold break-all">
                  {detail.data.name}
                </span>
                <span className="zke-mono text-subtle-foreground text-xs">
                  {detail.data.version}
                  {detail.data.app_version ? ` · app ${detail.data.app_version}` : ""}
                </span>
              </div>
              <p className="text-muted-foreground text-xs leading-relaxed">
                {detail.data.description || "（该 Chart 没有描述）"}
              </p>
              {(detail.data.keywords?.length ?? 0) > 0 ? (
                <div className="mt-0.5 flex flex-wrap gap-1">
                  {(detail.data.keywords ?? []).slice(0, 8).map((keyword) => (
                    <Badge key={keyword} tone="neutral">
                      {keyword}
                    </Badge>
                  ))}
                </div>
              ) : null}
            </div>
          </Card>

          <div className="grid gap-3 @2xl:grid-cols-2">
            <DetailCard title="来源">
              <DetailRow label="类型" value={detail.data.type || "application"} />
              <DetailRow label="主页" value={<ExternalURL url={detail.data.home} />} />
              <DetailRow
                label="源码"
                value={
                  (detail.data.sources?.length ?? 0) === 0 ? (
                    "—"
                  ) : (
                    <span className="grid gap-0.5">
                      {(detail.data.sources ?? []).map((source) => (
                        <ExternalURL key={source} url={source} />
                      ))}
                    </span>
                  )
                }
              />
              <DetailRow
                label="版本"
                value={
                  <Select
                    value={version || "__latest__"}
                    onValueChange={(next) => setVersion(next === "__latest__" ? "" : next)}
                  >
                    <SelectTrigger aria-label="Chart 版本" className="w-56">
                      <SelectValue placeholder="最新版本" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="__latest__">最新版本</SelectItem>
                      {(versions.data?.versions ?? []).map((item) => (
                        <SelectItem key={item.version} value={item.version}>
                          {item.version}
                          {item.app_version ? ` · app ${item.app_version}` : ""}
                          {item.deprecated ? " · 已弃用" : ""}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                }
              />
            </DetailCard>
            <DetailCard title="依赖">
              {(detail.data.dependencies?.length ?? 0) === 0 ? (
                <p className="text-subtle-foreground text-xs">该 Chart 不依赖其他 Chart。</p>
              ) : (
                (detail.data.dependencies ?? []).map((dependency) => (
                  <DetailRow
                    key={`${dependency.name}@${dependency.version}`}
                    label={dependency.name}
                    value={
                      <span className="zke-mono text-xs break-all">
                        {dependency.version}
                        {dependency.condition ? ` · 条件 ${dependency.condition}` : ""}
                      </span>
                    }
                  />
                ))
              )}
            </DetailCard>
          </div>

          <Card className="grid gap-2 p-4">
            <CardTitle>默认 values</CardTitle>
            <p className="text-subtle-foreground text-xs">
              Chart 自带的 values.yaml 原文，注释即文档。安装时以它为起点编辑。
            </p>
            {/* The same read-only editor the container service uses for YAML:
                line numbers and highlighting, because this is the document an
                operator is about to copy from and edit. */}
            <YamlEditor
              value={detail.data.values || "# 该 Chart 没有 values.yaml"}
              onChange={() => {}}
              readOnly
              label={`${detail.data.name} 默认 values`}
              // A height, not a cap: the editor paints into absolutely positioned
              // layers, so a `max-h` alone leaves it with nothing to be capped
              // against and it collapses to a line. It scrolls inside this.
              className="h-96"
            />
          </Card>

          {detail.data.readme ? (
            /* `min-w-0` so the README's widest code block scrolls inside the
               card instead of widening it — a grid item will not shrink below
               its content unless it is told it may. */
            <Card className="grid min-w-0 gap-2 p-4">
              <CardTitle>README</CardTitle>
              {/* Rendered rather than dumped: a chart README is documentation,
                  and the renderer builds React nodes instead of injecting HTML,
                  so nothing the chart author wrote becomes markup here.
                  
                  No height cap and no scrollbar of its own: the page already
                  scrolls, and a document boxed inside a scrolling page gives the
                  reader two scrollbars to choose between and a card whose bottom
                  edge is never where the text ends. */}
              <Markdown text={detail.data.readme} />
            </Card>
          ) : null}
        </div>
      )}
    </div>
  );
}

/**
 * A link a chart supplied.
 *
 * Only http and https open. Anything else is shown as the text it is — a chart
 * author is not given a way to put a scheme of their choosing behind a control
 * in this Console.
 */
function ExternalURL({ url }: { url?: string }) {
  if (!url) {
    return <>—</>;
  }
  const lower = url.trim().toLowerCase();
  if (!lower.startsWith("https://") && !lower.startsWith("http://")) {
    return <span className="break-all">{url}</span>;
  }
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer nofollow"
      className="zke-focus text-primary rounded-inline inline-flex items-center gap-1 break-all hover:underline"
    >
      {url}
      <ExternalLink className="size-3 shrink-0" aria-hidden="true" />
    </a>
  );
}
