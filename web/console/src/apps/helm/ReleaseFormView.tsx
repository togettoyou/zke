import { useMemo, useState } from "react";
import { Eye, ShipWheel } from "lucide-react";

import {
  useHelmChart,
  useHelmChartVersions,
  useHelmRepositories,
  useInstallHelmRelease,
  useUpgradeHelmRelease,
  type HelmReleaseWriteInput,
} from "@/api/queries/helm";
import type { HelmReleaseReport } from "@/api/types";
import { PageHeader } from "@/apps/AppShell";
import { ErrorAlert } from "@/components/common/error-alert";
import { notifyFailure } from "@/components/common/notify";
import { LoadingState } from "@/components/common/state";
import { YamlDiff } from "@/components/common/yaml-diff";
import { YamlEditor } from "@/components/common/yaml-editor";
import { Button } from "@/components/ui/button";
import { Input, NumericInput } from "@/components/ui/input";
import { Alert, Card, CardTitle } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { ChartIcon, Field, FieldGrid, FormSection, SwitchField } from "./form";

/**
 * Installing and upgrading a release.
 *
 * It is a page rather than a dialog because of what it contains: a values
 * document that is edited and scrolled, a rendered manifest that is compared
 * against what is running, and a set of switches whose consequences an operator
 * has to think about. None of that fits a box over a list.
 *
 * The order on the page is the order of the decision. What to install, then with
 * what values, then how to apply it, then — and only then — what it would
 * actually do. The preview is not optional decoration: `dry_run` sends the exact
 * body the apply will send and reports the manifest the Cluster would receive,
 * so approving it is approving the request itself.
 */

export type ReleaseFormMode = "install" | "upgrade";

export type ReleaseFormTarget = {
  clusterId: string;
  clusterName: string;
  namespace: string;
  /**
   * Empty when upgrading: Helm records which chart a release came from but not
   * which repository, so the operator picks it here rather than the Console
   * guessing and silently upgrading from somewhere else.
   */
  repositoryId: string;
  chart: string;
  /** Empty for an install of the newest published version. */
  version: string;
  /** Set when upgrading: the release being replaced. */
  releaseName?: string;
  /** The values the running revision was installed with, as YAML. */
  currentValues?: string;
  /** The manifest the running revision rendered, for the diff. */
  currentManifest?: string;
};

export function ReleaseFormView({
  mode,
  target,
  canInstallClusterScoped,
  onBack,
  onDone,
}: {
  mode: ReleaseFormMode;
  target: ReleaseFormTarget;
  canInstallClusterScoped: boolean;
  onBack: () => void;
  onDone: (report: HelmReleaseReport) => void;
}) {
  const [repositoryId, setRepositoryId] = useState(target.repositoryId);
  const [version, setVersion] = useState(target.version);
  const [name, setName] = useState(target.releaseName ?? "");
  /*
   * `null` means the operator has not edited the values yet, and the chart's own
   * defaults stand in. It is a separate state from the text itself so that
   * switching chart version before the first edit follows the new version's
   * defaults, while switching after one keeps what was typed — replacing
   * somebody's work with a chart's defaults because a dropdown moved is the
   * failure this exists to avoid.
   */
  const [valuesDraft, setValuesDraft] = useState<string | null>(target.currentValues ?? null);
  const [wait, setWait] = useState(true);
  const [atomic, setAtomic] = useState(mode === "upgrade");
  const [createNamespace, setCreateNamespace] = useState(false);
  const [disableHooks, setDisableHooks] = useState(false);
  const [reuseValues, setReuseValues] = useState(false);
  const [timeoutSeconds, setTimeoutSeconds] = useState("300");
  const [description, setDescription] = useState("");
  /*
   * A preview describes exactly one request body. It is kept together with a
   * signature of that body and shown only while the signature still matches, so
   * editing anything afterwards retires the preview during the same render
   * rather than leaving a picture of a request nobody is going to send next to
   * an Apply button that would send a different one.
   */
  const [preview, setPreview] = useState<{ signature: string; report: HelmReleaseReport } | null>(
    null,
  );

  const repositories = useHelmRepositories();
  const enabledRepositories = useMemo(
    () => (repositories.data?.repositories ?? []).filter((item) => item.enabled),
    [repositories.data],
  );
  const versions = useHelmChartVersions(repositoryId || null, target.chart);
  const chart = useHelmChart(repositoryId || null, target.chart, version);
  const install = useInstallHelmRelease();
  const upgrade = useUpgradeHelmRelease();
  const mutation = mode === "install" ? install : upgrade;
  // One key for the whole form, so a retry after a lost response is a retry and
  // not a second install.
  const idempotencyKey = useSubmissionKey(true);

  const defaults = chart.data?.values ?? "";
  const values = valuesDraft ?? defaults;

  const seconds = Number(timeoutSeconds || "0");
  const invalidTimeout = timeoutSeconds !== "" && (seconds < 1 || seconds > 3600);
  const trimmedName = name.trim();
  // Helm's own rule, checked here so a bad name is a field error rather than a
  // round trip that comes back rejected.
  const invalidName =
    mode === "install" &&
    trimmedName !== "" &&
    !/^[a-z0-9]([a-z0-9.-]{0,51}[a-z0-9])?$/.test(trimmedName);
  const ready =
    Boolean(repositoryId && target.chart) &&
    (mode === "upgrade" || (trimmedName !== "" && !invalidName)) &&
    !invalidTimeout;

  const body = useMemo<Omit<HelmReleaseWriteInput, "dryRun">>(
    () => ({
      clusterId: target.clusterId,
      namespace: target.namespace,
      name: mode === "upgrade" ? (target.releaseName as string) : trimmedName,
      repositoryId,
      chart: target.chart,
      version,
      values,
      createNamespace: mode === "install" ? createNamespace : false,
      // Sent as it is shown. `atomic` cannot roll back without waiting, so the
      // switch above is forced on and disabled while it is set; sending the raw
      // state instead would make the request disagree with the form.
      wait: wait || atomic,
      atomic,
      disableHooks,
      reuseValues: mode === "upgrade" ? reuseValues : false,
      timeoutSeconds: timeoutSeconds ? seconds : undefined,
      description,
      idempotencyKey,
    }),
    [
      atomic,
      createNamespace,
      description,
      disableHooks,
      idempotencyKey,
      mode,
      repositoryId,
      reuseValues,
      seconds,
      target,
      timeoutSeconds,
      trimmedName,
      values,
      version,
      wait,
    ],
  );

  const signature = JSON.stringify(body);
  const activePreview = preview?.signature === signature ? preview.report : null;

  const submit = (dryRun: boolean) => {
    mutation.mutate(
      { ...body, dryRun },
      {
        onSuccess: (result) => {
          if (dryRun) {
            setPreview({ signature, report: result.release });
            return;
          }
          onDone(result.release);
        },
        onError: (error) => {
          notifyFailure(
            dryRun ? "预览 Helm 变更" : mode === "install" ? "安装 Helm 应用" : "升级 Helm 应用",
            error,
          );
        },
      },
    );
  };

  const title =
    mode === "install" ? `安装 ${target.chart}` : `升级 ${target.releaseName ?? target.chart}`;

  return (
    <div className="grid gap-3">
      <PageHeader
        title={title}
        onBack={onBack}
        backDisabled={mutation.isPending}
        actions={
          <>
            <Button
              size="sm"
              variant="secondary"
              disabled={!ready || mutation.isPending}
              onClick={() => submit(true)}
            >
              <Eye />
              预览
            </Button>
            <Button
              size="sm"
              disabled={!ready || mutation.isPending || !activePreview}
              onClick={() => submit(false)}
            >
              <ShipWheel />
              {mode === "install" ? "安装" : "升级"}
            </Button>
          </>
        }
      />

      <div className="grid max-w-4xl gap-3">
        {/* Preview before apply is enforced rather than suggested: a chart is a
            program, and what it renders is not derivable from the values it was
            given. */}
        {activePreview ? null : (
          <Alert tone="info">
            先预览再提交。预览会用完全相同的请求在目标集群渲染一次
            Chart（不写入任何对象），并给出将要应用的清单；只有看过它之后才能提交。
          </Alert>
        )}

        <FormSection title="目标" hint={`${target.clusterName} · ${target.namespace}`}>
          <div className="flex items-start gap-3">
            <ChartIcon url={chart.data?.icon_url} size="lg" />
            <div className="grid min-w-0 flex-1 gap-3">
              <div>
                <div className="text-foreground text-[13px] font-medium break-all">
                  {target.chart}
                </div>
                <p className="text-muted-foreground mt-0.5 text-xs leading-relaxed">
                  {chart.data?.description || "读取 Chart 信息…"}
                </p>
              </div>
              <FieldGrid>
                <Field
                  label="仓库"
                  htmlFor="helm-form-repository"
                  hint={
                    mode === "upgrade"
                      ? "Helm 只记录 Release 用的是哪个 Chart，不记录它来自哪个仓库。"
                      : undefined
                  }
                >
                  <Select value={repositoryId} onValueChange={setRepositoryId}>
                    <SelectTrigger id="helm-form-repository" className="w-full">
                      <SelectValue placeholder="选择仓库" />
                    </SelectTrigger>
                    <SelectContent>
                      {enabledRepositories.map((item) => (
                        <SelectItem key={item.id} value={item.id}>
                          {item.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field
                  label="Chart 版本"
                  htmlFor="helm-chart-version"
                  hint={
                    chart.data?.app_version
                      ? `应用版本 ${chart.data.app_version}`
                      : "仓库索引中保留的版本"
                  }
                >
                  <Select
                    value={version || "__latest__"}
                    onValueChange={(next) => setVersion(next === "__latest__" ? "" : next)}
                  >
                    <SelectTrigger id="helm-chart-version" className="w-full">
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
                </Field>
                <Field
                  label="Release 名"
                  htmlFor="helm-release-name"
                  hint="小写字母、数字、`-` 与 `.`，不超过 53 个字符。"
                  error={invalidName ? "不符合 Helm 的 Release 名规则。" : undefined}
                >
                  {mode === "install" ? (
                    <Input
                      id="helm-release-name"
                      value={name}
                      onChange={(event) => setName(event.target.value)}
                      placeholder="checkout"
                      autoComplete="off"
                      spellCheck={false}
                      maxLength={53}
                      aria-invalid={invalidName || undefined}
                    />
                  ) : (
                    <Input id="helm-release-name" value={target.releaseName ?? ""} readOnly />
                  )}
                </Field>
                <Field
                  label="本次变更说明"
                  htmlFor="helm-description"
                  hint="会连同操作者一起记在该次修订上，`helm history` 也能读到。"
                >
                  <Input
                    id="helm-description"
                    value={description}
                    onChange={(event) => setDescription(event.target.value)}
                    placeholder="例如：季度发布"
                    autoComplete="off"
                    maxLength={200}
                  />
                </Field>
              </FieldGrid>
              {chart.data?.deprecated ? (
                <Alert tone="warning">该 Chart 已被仓库标记为弃用，作者可能不再维护它。</Alert>
              ) : null}
              {/* Under a signing policy an archive that does not verify never
                  reaches this form — the catalogue refuses to serve it. So this
                  is not a claim to weigh, it is the outcome of a check that has
                  already happened, and it belongs beside the button that will
                  send the chart to a Cluster. */}
              {chart.data?.signature?.verified ? (
                <Alert tone="success">
                  该 Chart 的来源证明已通过校验，签名者
                  {chart.data.signature.signed_by?.join("、") || "未署名"}。
                </Alert>
              ) : null}
              {chart.data?.signature?.unsigned ? (
                <Alert tone="warning">
                  该仓库配置为「有签名则校验」，但这个版本没有发布来源证明，
                  因此无法确认它由谁生产。
                </Alert>
              ) : null}
              {(chart.data?.dependencies?.length ?? 0) > 0 ? (
                <Alert tone="info">
                  该 Chart 依赖 {chart.data?.dependencies?.length} 个子
                  Chart，安装它同时会安装它们：
                  {(chart.data?.dependencies ?? []).map((item) => item.name).join("、")}。
                </Alert>
              ) : null}
            </div>
          </div>
        </FormSection>

        <FormSection
          title="Values"
          hint={
            mode === "install"
              ? "初始内容是 Chart 自带的 values.yaml 原文"
              : reuseValues
                ? "已选择沿用上一次的 values，这里提交的内容会与它们合并"
                : "初始内容是当前修订使用的 values，提交后将完整替换它们"
          }
        >
          {/* A chart that packages values.schema.json has said what a valid
              configuration is, and the Server checks against it before the
              request reaches a Cluster. Saying so here is the difference
              between a rejection that looks like a bug and one that was
              announced. */}
          {chart.data?.values_schema ? (
            <Alert tone="info" className="mb-2">
              该 Chart 自带 values.schema.json，提交前会按它校验；不符合的字段会在这里被指名拒绝，
              集群不会收到请求。
            </Alert>
          ) : null}
          {chart.isLoading ? (
            <LoadingState label="读取 Chart 默认值…" />
          ) : (
            <YamlEditor
              value={values}
              onChange={setValuesDraft}
              className="min-h-72"
              label="Helm values"
            />
          )}
          {chart.error ? <ErrorAlert error={chart.error} className="mt-2" /> : null}
        </FormSection>

        <FormSection
          title="执行方式"
          warning={
            canInstallClusterScoped
              ? undefined
              : "你没有该集群的 cluster.manage 权限。如果这个 Chart 会创建 CRD、ClusterRole 这类不属于任何命名空间的对象，Agent 会指名拒绝——命名空间级的授权说明不了集群级的对象。"
          }
        >
          <div className="grid gap-3">
            <SwitchField
              id="helm-wait"
              checked={wait || atomic}
              disabled={atomic}
              onChange={setWait}
              label="等待对象就绪"
              hint="等到 Chart 创建的对象进入就绪状态再返回，最长等待时间见下方。"
            />
            <SwitchField
              id="helm-atomic"
              checked={atomic}
              onChange={setAtomic}
              label="失败时回滚"
              hint="失败时回到操作前的状态。它隐含等待，因此会连带开启上一项。"
            />
            {mode === "install" ? (
              <SwitchField
                id="helm-create-namespace"
                checked={createNamespace}
                onChange={setCreateNamespace}
                label="命名空间不存在时创建"
                hint="仅安装可用；升级时命名空间消失是要报告的故障，不是要修复的问题。"
              />
            ) : (
              <SwitchField
                id="helm-reuse-values"
                checked={reuseValues}
                onChange={setReuseValues}
                label="沿用上一次的 values"
                hint="关闭时，这里提交的 values 完整替换上一次的。"
              />
            )}
            <SwitchField
              id="helm-disable-hooks"
              checked={disableHooks}
              onChange={setDisableHooks}
              label="跳过 Chart 的 hook"
              hint="跳过 hook 装出来的 Release 已经不是 Chart 描述的那一个，这个事实会记在修订上。"
              tone="warning"
            />
            <Field
              label="最长等待（秒）"
              htmlFor="helm-timeout"
              className="max-w-48"
              hint="1 到 3600 秒。"
              error={invalidTimeout ? "取值范围是 1 到 3600 秒。" : undefined}
            >
              <NumericInput
                id="helm-timeout"
                value={timeoutSeconds}
                onValueChange={setTimeoutSeconds}
                placeholder="300"
                aria-invalid={invalidTimeout || undefined}
              />
            </Field>
          </div>
        </FormSection>

        {mutation.error ? <ErrorAlert error={mutation.error} /> : null}

        {activePreview ? (
          <PreviewCard
            report={activePreview}
            before={mode === "upgrade" ? (target.currentManifest ?? "") : ""}
            mode={mode}
          />
        ) : null}
      </div>
    </div>
  );
}

/**
 * What the Cluster said the change would be.
 *
 * An install has nothing to compare against, so it shows the manifest itself.
 * An upgrade shows the difference from what is running, which is the question
 * actually being asked: not "what does this chart render" but "what changes".
 */
function PreviewCard({
  report,
  before,
  mode,
}: {
  report: HelmReleaseReport;
  before: string;
  mode: ReleaseFormMode;
}) {
  return (
    // `min-w-0` for the same reason the README card carries it: the diff and the
    // manifest scroll inside their own boxes only if this one may shrink below
    // the widest line in them.
    <Card className="grid min-w-0 gap-2 p-4">
      <CardTitle>{mode === "install" ? "将要创建的对象" : "与当前修订的差异"}</CardTitle>
      <p className="text-subtle-foreground text-xs">
        由目标集群渲染，未写入任何对象。Chart {report.chart_name} {report.chart_version}
        {report.app_version ? ` · app ${report.app_version}` : ""}。
      </p>
      {report.manifest_truncated ? (
        <Alert tone="info">
          清单超过服务端上限，只展示前一段；提交本身不受影响，完整对象可在容器服务的「资源对象浏览器」中查看。
        </Alert>
      ) : null}
      {mode === "upgrade" && before ? (
        <YamlDiff before={before} after={report.manifest} />
      ) : (
        <YamlEditor
          value={report.manifest || "# 该 Chart 没有渲染出任何对象"}
          onChange={() => {}}
          readOnly
          label="将要应用的清单"
          className="h-[32rem]"
        />
      )}
      {report.notes ? (
        <>
          <CardTitle className="mt-1">NOTES</CardTitle>
          <pre className="zke-mono text-muted-foreground border-border bg-surface-muted/60 rounded-control border p-2.5 text-xs leading-relaxed whitespace-pre-wrap">
            {report.notes}
          </pre>
        </>
      ) : null}
    </Card>
  );
}
