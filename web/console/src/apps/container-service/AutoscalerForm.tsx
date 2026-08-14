import { useState, type ReactNode } from "react";
import { Plus, X } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import {
  useAutoscaler,
  useCreateAutoscaler,
  useUpdateAutoscaler,
  type AutoscalerDetail,
} from "@/api/queries/autoscaling";
import type { KubernetesHPABehavior, KubernetesHPASpecInput } from "@/api/types";
import { PageHeader } from "@/apps/AppShell";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import { Input, NumericInput } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, Checkbox } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useSubmissionKey } from "@/lib/use-submission-key";

const DNS_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;
const DEFAULT_OPTION = "__default__";
const MAX_REPLICAS = 1_000_000;
const MAX_PERIOD_SECONDS = 1_800;
const MAX_STABILIZATION_SECONDS = 3_600;

type MetricDraft = {
  type: "Resource" | "ContainerResource";
  name: string;
  container: string;
  targetType: "Utilization" | "AverageValue";
  value: string;
};

type PolicyDraft = { type: "Pods" | "Percent"; value: string; periodSeconds: string };

type RulesDraft = {
  enabled: boolean;
  stabilizationWindow: string;
  selectPolicy: string;
  policies: PolicyDraft[];
};

const EMPTY_RULES: RulesDraft = {
  enabled: false,
  stabilizationWindow: "",
  selectPolicy: DEFAULT_OPTION,
  policies: [],
};

type SectionKey = "basic" | "metrics" | "behavior";

/** The titles the sections are rendered with, to point at one from elsewhere. */
const SECTION_LABELS: Record<SectionKey, string> = {
  basic: "基本信息",
  metrics: "指标",
  behavior: "伸缩行为",
};

/** The one thing currently blocking submission, and where it can be fixed. */
type FormProblem = { section: SectionKey; message: string };

type AutoscalerDraft = {
  name: string;
  editing: boolean;
  targetName: string;
  minReplicas: string;
  maxReplicas: string;
  metrics: MetricDraft[];
  scaleUp: RulesDraft;
  scaleDown: RulesDraft;
};

/*
 * The first problem in the form, read top to bottom.
 *
 * One at a time, and named where it can be fixed: a list of every fault at the
 * bottom of the page is a list an operator has to map back onto fields, and the
 * page is longer than the screen. Reported in the order the sections appear, so
 * fixing what is reported moves down the form rather than around it.
 */
function autoscalerProblem(draft: AutoscalerDraft): FormProblem | null {
  return basicProblem(draft) ?? metricsProblem(draft.metrics) ?? behaviorProblem(draft);
}

function basicProblem(draft: AutoscalerDraft): FormProblem | null {
  const at = (message: string): FormProblem => ({ section: "basic", message });
  if (!draft.editing) {
    const name = draft.name.trim();
    if (name === "") {
      return at("请填写名称。");
    }
    if (name.length > 253) {
      return at("名称最长 253 个字符。");
    }
    if (!DNS_SUBDOMAIN.test(name)) {
      return at(
        "名称必须是合法的 DNS 子域名：只能包含小写字母、数字、连字符和点，并以字母或数字开头和结尾。",
      );
    }
  }
  if (draft.targetName.trim() === "") {
    return at("请填写目标工作负载的名称。");
  }
  const minimum = draft.minReplicas.trim();
  const maximum = draft.maxReplicas.trim();
  if (!/^\d+$/.test(minimum) || Number(minimum) < 1) {
    return at("最小副本数必须是不小于 1 的整数。");
  }
  if (!/^\d+$/.test(maximum)) {
    return at("最大副本数必须是整数。");
  }
  if (Number(maximum) < Number(minimum)) {
    return at("最大副本数不能小于最小副本数。");
  }
  if (Number(maximum) > MAX_REPLICAS) {
    return at(`最大副本数不能超过 ${MAX_REPLICAS}。`);
  }
  return null;
}

function metricsProblem(metrics: MetricDraft[]): FormProblem | null {
  const at = (message: string): FormProblem => ({ section: "metrics", message });
  if (metrics.length === 0) {
    return at("请至少添加一个指标：没有指标的 HPA 不会伸缩。");
  }
  for (const [index, metric] of metrics.entries()) {
    const where = `第 ${index + 1} 个指标`;
    if (metric.name.trim() === "") {
      return at(`${where}缺少资源名称，例如 cpu 或 memory。`);
    }
    if (metric.type === "ContainerResource" && metric.container.trim() === "") {
      return at(`${where}是 ContainerResource，需要指定容器名。`);
    }
    const value = metric.value.trim();
    if (value === "") {
      return at(`${where}缺少目标值。`);
    }
    if (metric.targetType === "Utilization" && (!/^\d+$/.test(value) || Number(value) < 1)) {
      return at(`${where}的 Utilization 目标是百分比，必须是不小于 1 的整数。`);
    }
  }
  return null;
}

function behaviorProblem(draft: AutoscalerDraft): FormProblem | null {
  return rulesProblem("扩容", draft.scaleUp) ?? rulesProblem("缩容", draft.scaleDown);
}

function rulesProblem(label: string, rules: RulesDraft): FormProblem | null {
  const at = (message: string): FormProblem => ({ section: "behavior", message });
  if (!rules.enabled) {
    return null;
  }
  const window = rules.stabilizationWindow.trim();
  if (window !== "" && (!/^\d+$/.test(window) || Number(window) > MAX_STABILIZATION_SECONDS)) {
    return at(`${label}的稳定窗口必须是 0–${MAX_STABILIZATION_SECONDS} 秒的整数，留空使用默认值。`);
  }
  if (rules.policies.length === 0) {
    return at(`自定义${label}策略后至少需要一条策略，否则该方向没有可执行的规则。`);
  }
  for (const [index, policy] of rules.policies.entries()) {
    const where = `${label}策略 ${index + 1}`;
    const value = policy.value.trim();
    if (!/^\d+$/.test(value) || Number(value) < 1) {
      return at(`${where}的数值必须是不小于 1 的整数。`);
    }
    const period = policy.periodSeconds.trim();
    if (!/^\d+$/.test(period) || Number(period) < 1 || Number(period) > MAX_PERIOD_SECONDS) {
      return at(`${where}的周期必须是 1–${MAX_PERIOD_SECONDS} 秒的整数。`);
    }
  }
  return null;
}

/**
 * Creates or replaces one HorizontalPodAutoscaler.
 *
 * Editing loads the object first: the update replaces the whole spec and carries
 * the UID and resourceVersion it was read at, so there is nothing safe to submit
 * until the current spec has arrived.
 *
 * A page rather than a dialog, like every other typed form here: an HPA with a
 * few metrics and both scaling directions customised is taller than a box laid
 * over the list can show, and the list is of no use while it is being filled in.
 * Entered from the detail page, the detail stays open underneath, so leaving the
 * form returns to the object that was being read rather than to the list.
 */
export function AutoscalerForm({
  clusterId,
  clusterName,
  namespace,
  existingName,
  onClose,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  /** Set when editing; null when creating. */
  existingName: string | null;
  onClose: () => void;
}) {
  const existing = useAutoscaler(clusterId, namespace, existingName);
  const title = `编辑 HPA · ${existingName}`;

  if (existingName && existing.isLoading) {
    return (
      <>
        <PageHeader title={title} onBack={onClose} />
        <LoadingState />
      </>
    );
  }
  if (existingName && (existing.error || !existing.data)) {
    return (
      <>
        <PageHeader title={title} onBack={onClose} />
        <ErrorState error={existing.error} onRetry={() => void existing.refetch()} />
      </>
    );
  }

  const unsupportedMetrics = existing.data?.metrics.filter(
    (metric) => metric.type !== "Resource" && metric.type !== "ContainerResource",
  );
  if (existingName && unsupportedMetrics && unsupportedMetrics.length > 0) {
    return (
      <>
        <PageHeader title={title} onBack={onClose} />
        <Alert tone="warning">
          该 HPA 使用了 {unsupportedMetrics.map((metric) => metric.type).join("、")}{" "}
          指标，类型化表单只建模 Resource 与 ContainerResource。这里的更新会替换整份 spec，
          用它保存会丢掉这些指标，因此本表单不打开——请改用详情页的 YAML 入口编辑。
        </Alert>
      </>
    );
  }

  return (
    <AutoscalerEditor
      clusterId={clusterId}
      clusterName={clusterName}
      namespace={namespace}
      existing={existingName ? (existing.data as AutoscalerDetail) : null}
      onClose={onClose}
    />
  );
}

function AutoscalerEditor({
  clusterId,
  clusterName,
  namespace,
  existing,
  onClose,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  existing: AutoscalerDetail | null;
  onClose: () => void;
}) {
  const create = useCreateAutoscaler();
  const update = useUpdateAutoscaler();
  const mutation = existing ? update : create;
  const [previewed, setPreviewed] = useState<KubernetesHPASpecInput | null>(null);
  const previewKey = useSubmissionKey(previewed === null);
  const applyKey = useSubmissionKey(previewed !== null);
  // Pinned at mount: a background refetch must not re-arm the precondition with
  // a version the form's contents were never based on.
  const [pinned] = useState(() =>
    existing ? { uid: existing.uid, resourceVersion: existing.resource_version } : null,
  );

  const [name, setName] = useState(existing?.name ?? "");
  const [targetKind, setTargetKind] = useState(
    existing?.target.kind === "StatefulSet" ? "StatefulSet" : "Deployment",
  );
  const [targetName, setTargetName] = useState(existing?.target.name ?? "");
  const [minReplicas, setMinReplicas] = useState(existing ? String(existing.min_replicas) : "1");
  const [maxReplicas, setMaxReplicas] = useState(existing ? String(existing.max_replicas) : "10");
  const [metrics, setMetrics] = useState<MetricDraft[]>(
    existing && existing.metrics.length > 0
      ? existing.metrics.map(toMetricDraft)
      : [
          {
            type: "Resource",
            name: "cpu",
            container: "",
            targetType: "Utilization",
            value: "80",
          },
        ],
  );
  const [scaleUp, setScaleUp] = useState<RulesDraft>(toRulesDraft(existing?.behavior?.scale_up));
  const [scaleDown, setScaleDown] = useState<RulesDraft>(
    toRulesDraft(existing?.behavior?.scale_down),
  );

  const problem = autoscalerProblem({
    name,
    editing: existing !== null,
    targetName,
    minReplicas,
    maxReplicas,
    metrics,
    scaleUp,
    scaleDown,
  });
  const problemIn = (section: SectionKey) =>
    problem?.section === section ? problem.message : undefined;

  const buildSpec = (): KubernetesHPASpecInput => {
    const behavior = {
      ...(scaleUp.enabled ? { scale_up: buildRules(scaleUp) } : {}),
      ...(scaleDown.enabled ? { scale_down: buildRules(scaleDown) } : {}),
    };
    return {
      target: {
        api_version: "apps/v1",
        kind: targetKind as "Deployment" | "StatefulSet",
        name: targetName.trim(),
      },
      min_replicas: Number(minReplicas.trim()),
      max_replicas: Number(maxReplicas.trim()),
      metrics: metrics.map((metric) =>
        metric.type === "Resource"
          ? {
              type: "Resource" as const,
              resource: { name: metric.name.trim(), target: buildTarget(metric) },
            }
          : {
              type: "ContainerResource" as const,
              container_resource: {
                name: metric.name.trim(),
                container: metric.container.trim(),
                target: buildTarget(metric),
              },
            },
      ),
      ...(Object.keys(behavior).length > 0 ? { behavior } : {}),
    };
  };

  const submit = (dryRun: boolean, spec: KubernetesHPASpecInput) => {
    const shared = {
      clusterId,
      namespace,
      spec,
      dryRun,
      idempotencyKey: dryRun ? previewKey : applyKey,
    };
    const request = existing
      ? update.mutateAsync({
          ...shared,
          name: existing.name,
          uid: pinned?.uid ?? existing.uid,
          resourceVersion: pinned?.resourceVersion ?? existing.resource_version,
        })
      : create.mutateAsync({ ...shared, name: name.trim() });
    void request
      .then(() => {
        if (dryRun) {
          setPreviewed(spec);
          return;
        }
        toast.success(`HPA ${existing?.name ?? name.trim()} 已${existing ? "更新" : "创建"}`);
        onClose();
      })
      .catch(() => undefined);
  };

  return (
    <>
      <div className="grid gap-3">
        <PageHeader
          title={existing ? `编辑 HPA · ${existing.name}` : `创建 HPA · ${namespace}`}
          onBack={onClose}
          backDisabled={mutation.isPending}
        />

        <FormSection title={SECTION_LABELS.basic} problem={problemIn("basic")}>
          <div className="grid gap-3 sm:grid-cols-2">
            {existing ? null : (
              <Field
                label="名称"
                htmlFor="hpa-name"
                hint="合法的 DNS 子域名，最长 253 个字符；创建后不可修改"
              >
                <Input
                  id="hpa-name"
                  value={name}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="例如 api-autoscaler"
                  onChange={(event) => setName(event.target.value)}
                />
              </Field>
            )}
            <Field label="目标类型" htmlFor="hpa-target-kind">
              <Select value={targetKind} onValueChange={setTargetKind}>
                <SelectTrigger id="hpa-target-kind">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="Deployment">Deployment</SelectItem>
                  <SelectItem value="StatefulSet">StatefulSet</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field
              label="目标名称"
              htmlFor="hpa-target-name"
              hint="必须是同一命名空间中已存在的工作负载"
            >
              <Input
                id="hpa-target-name"
                value={targetName}
                autoComplete="off"
                spellCheck={false}
                onChange={(event) => setTargetName(event.target.value)}
              />
            </Field>
            <Field label="最小副本数" htmlFor="hpa-min" hint="至少 1">
              <NumericInput id="hpa-min" value={minReplicas} onValueChange={setMinReplicas} />
            </Field>
            <Field label="最大副本数" htmlFor="hpa-max" hint="不小于最小副本数">
              <NumericInput id="hpa-max" value={maxReplicas} onValueChange={setMaxReplicas} />
            </Field>
          </div>
        </FormSection>

        <FormSection
          title={SECTION_LABELS.metrics}
          hint="至少一个；Utilization 按 requests 的百分比计算，目标容器需声明 requests"
          problem={problemIn("metrics")}
        >
          <MetricRows rows={metrics} onChange={setMetrics} />
        </FormSection>

        <FormSection
          title={SECTION_LABELS.behavior}
          hint="可选；不启用时使用 Kubernetes 默认策略"
          problem={problemIn("behavior")}
        >
          <RulesEditor label="扩容" rules={scaleUp} onChange={setScaleUp} idPrefix="up" />
          <div className="mt-3">
            <RulesEditor label="缩容" rules={scaleDown} onChange={setScaleDown} idPrefix="down" />
          </div>
        </FormSection>

        {/*
         * The message itself is up in the section that can fix it; down here,
         * next to the button it disables, what is missing is where to look.
         */}
        {problem ? (
          <Alert tone="warning">「{SECTION_LABELS[problem.section]}」中还有需要修正的项。</Alert>
        ) : null}
        {mutation.error ? <Alert tone="danger">{errorMessage(mutation.error)}</Alert> : null}

        <div className="flex flex-wrap items-center justify-end gap-3 pb-2">
          <span className="text-subtle-foreground text-xs">
            目标：{clusterName} / {namespace}
          </span>
          {existing ? (
            <span className="text-subtle-foreground text-xs">
              更新会替换整份 spec：本次未提交的指标或行为策略将从对象中移除。
            </span>
          ) : null}
          <Button
            variant="primary"
            size="sm"
            disabled={problem !== null || mutation.isPending}
            onClick={() => submit(true, buildSpec())}
          >
            {mutation.isPending ? "DryRun 预检中…" : "执行 DryRun 预检"}
          </Button>
        </div>
      </div>

      <SensitiveActionDialog
        open={previewed !== null}
        onOpenChange={(open) => !open && setPreviewed(null)}
        title={existing ? "确认更新 HPA" : "确认创建 HPA"}
        description="DryRun 预检已通过。确认后将向同一集群提交实际变更。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          { label: "HPA", name: existing?.name ?? name.trim(), id: existing?.uid },
          { label: "目标", name: `${targetKind}/${targetName.trim()}` },
        ]}
        impacts={
          existing
            ? [
                "整份 spec 会被替换：本次未提交的指标或行为策略将从对象中移除。",
                `控制器会按新的区间 ${minReplicas}–${maxReplicas} 调整目标副本数，可能立即触发一次伸缩。`,
                "请求携带该对象当前的 UID 与 resourceVersion，期间对象若已变化，更新会被拒绝而不是覆盖。",
              ]
            : [
                `创建后，${targetKind}/${targetName.trim()} 的副本数将由控制器接管，手动伸缩会在下一个周期被覆盖。`,
                `副本数会被约束在 ${minReplicas}–${maxReplicas} 之间，创建后可能立即触发一次伸缩。`,
                "指标不可用时（例如集群未安装 Metrics Server）HPA 不会伸缩，并在状态中报告原因。",
              ]
        }
        confirmLabel={existing ? "确认更新" : "确认创建"}
        destructive={existing !== null}
        pending={mutation.isPending}
        error={mutation.error}
        onConfirm={() => previewed && submit(false, previewed)}
      />
    </>
  );
}

function buildTarget(metric: MetricDraft) {
  return metric.targetType === "Utilization"
    ? { type: "Utilization" as const, average_utilization: Number(metric.value.trim()) }
    : { type: "AverageValue" as const, average_value: metric.value.trim() };
}

function buildRules(rules: RulesDraft): ScalingRules {
  return {
    ...(rules.stabilizationWindow.trim()
      ? { stabilization_window_seconds: Number(rules.stabilizationWindow.trim()) }
      : {}),
    ...(rules.selectPolicy === DEFAULT_OPTION
      ? {}
      : { select_policy: rules.selectPolicy as "Max" | "Min" | "Disabled" }),
    policies: rules.policies.map((policy) => ({
      type: policy.type,
      value: Number(policy.value.trim()),
      period_seconds: Number(policy.periodSeconds.trim()),
    })),
  };
}

function toMetricDraft(metric: AutoscalerDetail["metrics"][number]): MetricDraft {
  const utilization = metric.target.average_utilization;
  return {
    type: metric.type === "ContainerResource" ? "ContainerResource" : "Resource",
    name: metric.name,
    container: metric.container,
    targetType: utilization !== undefined ? "Utilization" : "AverageValue",
    value: utilization !== undefined ? String(utilization) : metric.target.average_value,
  };
}

type ScalingRules = NonNullable<KubernetesHPABehavior["scale_up"]>;

function toRulesDraft(rules: ScalingRules | undefined): RulesDraft {
  if (!rules) {
    return { ...EMPTY_RULES, policies: [] };
  }
  return {
    enabled: true,
    stabilizationWindow:
      rules.stabilization_window_seconds === undefined
        ? ""
        : String(rules.stabilization_window_seconds),
    selectPolicy: rules.select_policy ?? DEFAULT_OPTION,
    policies: rules.policies.map((policy) => ({
      type: policy.type,
      value: String(policy.value),
      periodSeconds: String(policy.period_seconds),
    })),
  };
}

function MetricRows({
  rows,
  onChange,
}: {
  rows: MetricDraft[];
  onChange: (rows: MetricDraft[]) => void;
}) {
  const update = (index: number, patch: Partial<MetricDraft>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...patch } : row)));

  return (
    <div className="grid gap-2">
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[1fr_auto] items-start gap-2">
          <div className="grid gap-2 sm:grid-cols-[9rem_1fr_1fr_9rem_6rem]">
            <Select
              value={row.type}
              onValueChange={(value) =>
                update(index, { type: value as "Resource" | "ContainerResource" })
              }
            >
              <SelectTrigger aria-label={`指标 ${index + 1} 类型`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="Resource">Resource</SelectItem>
                <SelectItem value="ContainerResource">ContainerResource</SelectItem>
              </SelectContent>
            </Select>
            <Input
              value={row.name}
              aria-label={`指标 ${index + 1} 资源`}
              placeholder="cpu 或 memory"
              autoComplete="off"
              onChange={(event) => update(index, { name: event.target.value })}
            />
            <Input
              value={row.container}
              aria-label={`指标 ${index + 1} 容器`}
              placeholder="容器名"
              autoComplete="off"
              disabled={row.type !== "ContainerResource"}
              onChange={(event) => update(index, { container: event.target.value })}
            />
            <Select
              value={row.targetType}
              onValueChange={(value) =>
                update(index, { targetType: value as "Utilization" | "AverageValue" })
              }
            >
              <SelectTrigger aria-label={`指标 ${index + 1} 目标类型`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="Utilization">Utilization</SelectItem>
                <SelectItem value="AverageValue">AverageValue</SelectItem>
              </SelectContent>
            </Select>
            <Input
              value={row.value}
              aria-label={`指标 ${index + 1} 目标值`}
              placeholder={row.targetType === "Utilization" ? "80" : "500m"}
              autoComplete="off"
              onChange={(event) => update(index, { value: event.target.value })}
            />
          </div>
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label={`移除指标 ${index + 1}`}
            disabled={rows.length <= 1}
            onClick={() => onChange(rows.filter((_, position) => position !== index))}
          >
            <X />
          </Button>
        </div>
      ))}
      <div>
        <Button
          size="sm"
          variant="secondary"
          disabled={rows.length >= 16}
          onClick={() =>
            onChange([
              ...rows,
              {
                type: "Resource",
                name: "memory",
                container: "",
                targetType: "Utilization",
                value: "80",
              },
            ])
          }
        >
          <Plus />
          添加指标
        </Button>
      </div>
    </div>
  );
}

function RulesEditor({
  label,
  rules,
  onChange,
  idPrefix,
}: {
  label: string;
  rules: RulesDraft;
  onChange: (rules: RulesDraft) => void;
  idPrefix: string;
}) {
  return (
    <div className="border-border/60 rounded-control grid gap-2 border p-2">
      <label className="flex items-center gap-2 text-[13px]">
        <Checkbox
          checked={rules.enabled}
          onCheckedChange={(checked) => onChange({ ...rules, enabled: checked === true })}
        />
        自定义{label}策略
      </label>
      {rules.enabled ? (
        <>
          <div className="grid gap-2 sm:grid-cols-2">
            <Field label="稳定窗口（秒）" htmlFor={`hpa-${idPrefix}-window`}>
              <NumericInput
                id={`hpa-${idPrefix}-window`}
                value={rules.stabilizationWindow}
                placeholder="留空使用默认"
                onValueChange={(stabilizationWindow) => onChange({ ...rules, stabilizationWindow })}
              />
            </Field>
            <Field label="策略选择" htmlFor={`hpa-${idPrefix}-select`}>
              <Select
                value={rules.selectPolicy}
                onValueChange={(value) => onChange({ ...rules, selectPolicy: value })}
              >
                <SelectTrigger id={`hpa-${idPrefix}-select`}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={DEFAULT_OPTION}>默认</SelectItem>
                  <SelectItem value="Max">Max</SelectItem>
                  <SelectItem value="Min">Min</SelectItem>
                  <SelectItem value="Disabled">Disabled（禁止该方向伸缩）</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>
          {rules.policies.map((policy, index) => (
            <div key={index} className="grid grid-cols-[8rem_1fr_1fr_auto] items-center gap-2">
              <Select
                value={policy.type}
                onValueChange={(value) =>
                  onChange({
                    ...rules,
                    policies: rules.policies.map((entry, position) =>
                      position === index ? { ...entry, type: value as "Pods" | "Percent" } : entry,
                    ),
                  })
                }
              >
                <SelectTrigger aria-label={`${label}策略 ${index + 1} 类型`}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="Pods">Pods</SelectItem>
                  <SelectItem value="Percent">Percent</SelectItem>
                </SelectContent>
              </Select>
              <NumericInput
                value={policy.value}
                aria-label={`${label}策略 ${index + 1} 数值`}
                placeholder="数值"
                onValueChange={(value) =>
                  onChange({
                    ...rules,
                    policies: rules.policies.map((entry, position) =>
                      position === index ? { ...entry, value } : entry,
                    ),
                  })
                }
              />
              <NumericInput
                value={policy.periodSeconds}
                aria-label={`${label}策略 ${index + 1} 周期秒数`}
                placeholder="周期（秒）"
                onValueChange={(periodSeconds) =>
                  onChange({
                    ...rules,
                    policies: rules.policies.map((entry, position) =>
                      position === index ? { ...entry, periodSeconds } : entry,
                    ),
                  })
                }
              />
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={`移除${label}策略 ${index + 1}`}
                onClick={() =>
                  onChange({
                    ...rules,
                    policies: rules.policies.filter((_, position) => position !== index),
                  })
                }
              >
                <X />
              </Button>
            </div>
          ))}
          <div>
            <Button
              size="sm"
              variant="secondary"
              disabled={rules.policies.length >= 16}
              onClick={() =>
                onChange({
                  ...rules,
                  policies: [
                    ...rules.policies,
                    { type: "Percent", value: "100", periodSeconds: "60" },
                  ],
                })
              }
            >
              <Plus />
              添加{label}策略
            </Button>
          </div>
        </>
      ) : null}
    </div>
  );
}

function FormSection({
  title,
  hint,
  problem,
  children,
}: {
  title: string;
  hint?: string;
  /** The current blocking problem, when it is this section that carries it. */
  problem?: string;
  children: ReactNode;
}) {
  return (
    <section>
      <div className="mb-2 flex flex-wrap items-baseline gap-2">
        <h4 className="text-foreground text-[13px] font-medium">{title}</h4>
        {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
      </div>
      {problem ? (
        <Alert tone="warning" className="mb-2">
          {problem}
        </Alert>
      ) : null}
      {children}
    </section>
  );
}

function Field({
  label,
  htmlFor,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <div className="grid content-start gap-1.5">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
    </div>
  );
}
