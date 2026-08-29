import { useMemo, useState } from "react";
import { ArrowLeft, ShipWheel } from "lucide-react";

import {
  useHelmChart,
  useHelmChartVersions,
  useHelmOperation,
  useHelmRepositories,
  useInstallHelmRelease,
  useUpgradeHelmRelease,
  type HelmReleaseWriteInput,
} from "@/api/queries/helm";
import type { HelmReleaseOperation } from "@/api/types";
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
import { OperationProgress } from "./operation";

/**
 * Installing and upgrading a release.
 *
 * It is a page rather than a dialog because of what it contains: a values
 * document that is edited and scrolled, a rendered manifest that is compared
 * against what is running, and a set of switches whose consequences an operator
 * has to think about. None of that fits a box over a list.
 *
 * The page has three steps and shows one at a time, which is the whole of its
 * design. It used to have two buttons — 预览 and 安装 — and rendered the preview
 * below a form long enough that the operator had to know to scroll for it; the
 * common outcome was pressing 预览, seeing nothing change, and pressing it
 * again. So there is one button, it moves the page forward, and each step
 * replaces the one before it:
 *
 *   配置 → 确认 → 执行
 *
 * 确认 is not decoration. `dry_run` sends the exact body the apply will send and
 * reports the manifest the Cluster would receive, so approving what is on that
 * step is approving the request itself. And because both steps are operations
 * rather than held-open requests, both of them say what they are doing while
 * they do it — see OperationProgress.
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
  onDone: (operation: HelmReleaseOperation) => void;
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
   * The two operations this page can have started. The step it is showing is
   * derived from them rather than stored: a page with an apply running is on
   * the 执行 step by definition, and a second source of truth for that is a
   * second thing that can be wrong.
   */
  const [previewId, setPreviewId] = useState<string | null>(null);
  const [applyId, setApplyId] = useState<string | null>(null);

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

  /*
   * One key per attempt, and a preview and its apply are two requests under two
   * keys.
   *
   * The Server claims a key for the operation it starts, so presenting the same
   * one again is what makes a retried submission a retry. It also refuses a key
   * presented for a *different* request — which is what the preview and the
   * apply are, differing by `dry_run` — so they cannot share one. Going back to
   * the form retires the attempt: whatever comes next is a new request, and
   * answering it with the previous attempt's account would describe a
   * configuration nobody is submitting any more.
   */
  const submissionKey = useSubmissionKey(true);
  const [attempt, setAttempt] = useState(0);
  const previewKey = `${submissionKey}:d${attempt}`;
  const applyKey = `${submissionKey}:a${attempt}`;

  const preview = useHelmOperation(target.clusterId, target.namespace, previewId);
  const apply = useHelmOperation(target.clusterId, target.namespace, applyId);

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

  const body = useMemo<Omit<HelmReleaseWriteInput, "dryRun" | "idempotencyKey">>(
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
    }),
    [
      atomic,
      createNamespace,
      description,
      disableHooks,
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

  const submit = (dryRun: boolean) => {
    mutation.mutate(
      { ...body, dryRun, idempotencyKey: dryRun ? previewKey : applyKey },
      {
        onSuccess: (result) => {
          if (dryRun) {
            setPreviewId(result.operation.id);
            return;
          }
          setApplyId(result.operation.id);
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

  /** Back to the form, retiring everything the previous attempt produced. */
  const restart = () => {
    setPreviewId(null);
    setApplyId(null);
    setAttempt((value) => value + 1);
    mutation.reset();
  };

  const verb = mode === "install" ? "安装" : "升级";
  const subject = mode === "install" ? target.chart : (target.releaseName ?? target.chart);

  if (applyId) {
    return (
      <ApplyStep
        verb={verb}
        subject={subject}
        operation={apply.data?.operation}
        error={apply.error}
        onRestart={restart}
        onDone={onDone}
      />
    );
  }

  if (previewId) {
    return (
      <ConfirmStep
        verb={verb}
        subject={subject}
        mode={mode}
        before={mode === "upgrade" ? (target.currentManifest ?? "") : ""}
        operation={preview.data?.operation}
        error={preview.error}
        submitting={mutation.isPending}
        submitError={mutation.error}
        onRestart={restart}
        onConfirm={() => submit(false)}
      />
    );
  }

  return (
    <div className="grid gap-3">
      <PageHeader
        title={`${verb} ${target.chart}`}
        onBack={onBack}
        backDisabled={mutation.isPending}
        actions={
          /*
           * One button, and it says what pressing it does. There used to be two
           * — 预览 then 安装 — and the difference between them was a thing the
           * operator had to hold in their head while the preview appeared below
           * a form too long to see the bottom of.
           */
          <Button size="sm" disabled={!ready || mutation.isPending} onClick={() => submit(true)}>
            <ShipWheel />
            {mutation.isPending ? "正在提交…" : `预览并${verb}`}
          </Button>
        }
      />

      <div className="grid gap-3">
        <Alert tone="info">
          下一步会用完全相同的请求在目标集群渲染一次 Chart（不写入任何对象），
          并把将要应用的清单摆出来；确认之后才会真正{verb}。
        </Alert>

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
              hint="等到 Chart 创建的对象进入就绪状态再返回，最长等待时间见下方。等待期间集群会持续报告它在等什么。"
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
      </div>
    </div>
  );
}

/**
 * 确认: what the Cluster said the change would be.
 *
 * Same shape as 执行 below it: the account of what is happening is at the top,
 * and what it produced follows. A preview is a real request to a real Cluster
 * and can take as long as fetching a chart takes, so the operator watches it
 * there — and when it finishes, the progress does not move out from under them
 * to the bottom of a page the manifest just made long.
 */
function ConfirmStep({
  verb,
  subject,
  mode,
  before,
  operation,
  error,
  submitting,
  submitError,
  onRestart,
  onConfirm,
}: {
  verb: string;
  subject: string;
  mode: ReleaseFormMode;
  before: string;
  operation?: HelmReleaseOperation;
  error: unknown;
  submitting: boolean;
  submitError: unknown;
  onRestart: () => void;
  onConfirm: () => void;
}) {
  const report = operation?.status === "succeeded" ? operation.report : undefined;

  return (
    <div className="grid gap-3">
      <PageHeader
        title={`确认${verb} ${subject}`}
        onBack={onRestart}
        backDisabled={submitting}
        actions={
          <>
            <Button size="sm" variant="secondary" disabled={submitting} onClick={onRestart}>
              <ArrowLeft />
              返回修改
            </Button>
            <Button size="sm" disabled={!report || submitting} onClick={onConfirm}>
              <ShipWheel />
              {submitting ? "正在提交…" : `确认${verb}`}
            </Button>
          </>
        }
      />
      <div className="grid gap-3">
        {error ? <ErrorAlert error={error} /> : null}
        {submitError ? <ErrorAlert error={submitError} /> : null}
        <Card className="grid min-w-0 gap-2 p-4">
          <CardTitle>{report ? "预览过程" : "正在预览"}</CardTitle>
          {operation ? (
            <OperationProgress operation={operation} />
          ) : (
            <LoadingState label="正在提交预览…" />
          )}
        </Card>
        {report ? (
          <Card className="grid min-w-0 gap-2 p-4">
            <CardTitle>
              {mode === "install" || !before ? "将要创建的对象" : "与当前修订的差异"}
            </CardTitle>
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
                <ReleaseNotes notes={report.notes} />
              </>
            ) : null}
          </Card>
        ) : null}
      </div>
    </div>
  );
}

/**
 * 执行: the change itself.
 *
 * Nothing can be edited from here and the way back is deliberately not 返回 —
 * an operation is running in a Cluster and leaving the page does not stop it.
 * When it ends, the same account stays on screen: the log of a deployment that
 * went wrong is worth more than the log of one that went right, and this is the
 * only place it exists.
 */
function ApplyStep({
  verb,
  subject,
  operation,
  error,
  onRestart,
  onDone,
}: {
  verb: string;
  subject: string;
  operation?: HelmReleaseOperation;
  error: unknown;
  onRestart: () => void;
  onDone: (operation: HelmReleaseOperation) => void;
}) {
  const finished = operation && operation.status !== "running";

  return (
    <div className="grid gap-3">
      <PageHeader
        title={`${verb} ${subject}`}
        actions={
          finished ? (
            operation.status === "succeeded" ? (
              <Button size="sm" onClick={() => onDone(operation)}>
                完成
              </Button>
            ) : (
              <Button size="sm" variant="secondary" onClick={onRestart}>
                <ArrowLeft />
                返回修改
              </Button>
            )
          ) : undefined
        }
      />
      <div className="grid gap-3">
        {error ? <ErrorAlert error={error} /> : null}
        {/* The operation is running in a Cluster and does not depend on this
            page being open. Saying so is the difference between waiting here
            because it is necessary and waiting here because nobody said it was
            not. */}
        {operation && operation.status === "running" ? (
          <Alert tone="info">
            可以离开这个页面，操作会在集群里继续；「已安装应用」里能回到它的进展。
          </Alert>
        ) : null}
        {operation?.status === "succeeded" && operation.report ? (
          <Alert tone="success">
            {operation.report.name} 现在是第 {operation.report.revision} 次修订
            {operation.report.chart_version
              ? `，Chart ${operation.report.chart_name} ${operation.report.chart_version}`
              : ""}
            。
          </Alert>
        ) : null}
        <Card className="grid min-w-0 gap-2 p-4">
          <CardTitle>执行过程</CardTitle>
          {operation ? (
            <OperationProgress operation={operation} />
          ) : (
            <LoadingState label="正在提交…" />
          )}
        </Card>
        {operation?.status === "succeeded" && operation.report?.notes ? (
          <Card className="grid min-w-0 gap-2 p-4">
            <CardTitle>NOTES</CardTitle>
            <ReleaseNotes notes={operation.report.notes} />
          </Card>
        ) : null}
      </div>
    </div>
  );
}

/**
 * NOTES.txt as the chart rendered it.
 *
 * Plain text, not YAML and not Markdown: it is whatever the chart's template
 * produced, and formatting it as either would be a claim about it the chart
 * never made.
 */
function ReleaseNotes({ notes }: { notes: string }) {
  return (
    <pre className="zke-mono text-muted-foreground border-border bg-surface-muted/60 rounded-control border p-2.5 text-xs leading-relaxed whitespace-pre-wrap">
      {notes}
    </pre>
  );
}
