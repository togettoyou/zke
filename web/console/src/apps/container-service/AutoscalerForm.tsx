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
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
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

/**
 * Creates or replaces one HorizontalPodAutoscaler.
 *
 * Editing loads the object first: the update replaces the whole spec and carries
 * the UID and resourceVersion it was read at, so there is nothing safe to submit
 * until the current spec has arrived.
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

  if (existingName && existing.isLoading) {
    return (
      <Dialog open onOpenChange={(open) => !open && onClose()}>
        <DialogContent aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>编辑 HPA · {existingName}</DialogTitle>
          </DialogHeader>
          <LoadingState />
        </DialogContent>
      </Dialog>
    );
  }
  if (existingName && (existing.error || !existing.data)) {
    return (
      <Dialog open onOpenChange={(open) => !open && onClose()}>
        <DialogContent aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>编辑 HPA · {existingName}</DialogTitle>
          </DialogHeader>
          <Alert tone="danger">{errorMessage(existing.error)}</Alert>
          <DialogFooter>
            <Button variant="ghost" onClick={onClose}>
              关闭
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    );
  }

  const unsupportedMetrics = existing.data?.metrics.filter(
    (metric) => metric.type !== "Resource" && metric.type !== "ContainerResource",
  );
  if (existingName && unsupportedMetrics && unsupportedMetrics.length > 0) {
    return (
      <Dialog open onOpenChange={(open) => !open && onClose()}>
        <DialogContent aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>无法使用类型化表单编辑 · {existingName}</DialogTitle>
            <DialogDescription>
              该 HPA 包含 {unsupportedMetrics.map((metric) => metric.type).join("、")} 高级指标。
            </DialogDescription>
          </DialogHeader>
          <Alert tone="warning">
            类型化表单仅支持 Resource 和 ContainerResource。为避免丢失现有指标，请关闭此窗口并使用
            YAML 编辑。
          </Alert>
          <DialogFooter>
            <Button variant="primary" onClick={onClose}>
              关闭
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
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

  const nameValid = existing !== null || (DNS_SUBDOMAIN.test(name.trim()) && name.length <= 253);
  const boundsValid =
    /^\d+$/.test(minReplicas.trim()) &&
    /^\d+$/.test(maxReplicas.trim()) &&
    Number(minReplicas.trim()) >= 1 &&
    Number(maxReplicas.trim()) >= Number(minReplicas.trim()) &&
    Number(maxReplicas.trim()) <= 1_000_000;
  const metricsValid =
    metrics.length > 0 &&
    metrics.every(
      (metric) =>
        metric.name.trim() !== "" &&
        metric.value.trim() !== "" &&
        (metric.type !== "ContainerResource" || metric.container.trim() !== "") &&
        (metric.targetType !== "Utilization" ||
          (/^\d+$/.test(metric.value.trim()) && Number(metric.value.trim()) >= 1)),
    );
  const rulesValid = [scaleUp, scaleDown].every(
    (rules) =>
      !rules.enabled ||
      (rules.policies.length > 0 &&
        rules.policies.every(
          (policy) =>
            /^\d+$/.test(policy.value.trim()) &&
            Number(policy.value.trim()) >= 1 &&
            /^\d+$/.test(policy.periodSeconds.trim()) &&
            Number(policy.periodSeconds.trim()) >= 1 &&
            Number(policy.periodSeconds.trim()) <= 1_800,
        ) &&
        (rules.stabilizationWindow.trim() === "" ||
          (/^\d+$/.test(rules.stabilizationWindow.trim()) &&
            Number(rules.stabilizationWindow.trim()) <= 3_600))),
  );
  const valid = nameValid && targetName.trim() !== "" && boundsValid && metricsValid && rulesValid;

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
      <Dialog open={previewed === null} onOpenChange={(open) => !open && onClose()}>
        <DialogContent aria-describedby={undefined} className="w-[min(760px,calc(100vw-2rem))]">
          <DialogHeader>
            <DialogTitle>{existing ? `编辑 HPA · ${existing.name}` : "创建 HPA"}</DialogTitle>
            <DialogDescription>
              第一步只执行服务端 DryRun，不会在集群中写入任何变更。
            </DialogDescription>
          </DialogHeader>

          <div className="grid gap-4">
            <FormSection title="基本信息">
              <div className="grid gap-3 sm:grid-cols-2">
                {existing ? null : (
                  <Field label="名称" htmlFor="hpa-name">
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
                <Field label="目标名称" htmlFor="hpa-target-name">
                  <Input
                    id="hpa-target-name"
                    value={targetName}
                    autoComplete="off"
                    spellCheck={false}
                    onChange={(event) => setTargetName(event.target.value)}
                  />
                </Field>
                <Field label="最小副本数" htmlFor="hpa-min">
                  <Input
                    id="hpa-min"
                    value={minReplicas}
                    inputMode="numeric"
                    onChange={(event) => setMinReplicas(event.target.value)}
                  />
                </Field>
                <Field label="最大副本数" htmlFor="hpa-max">
                  <Input
                    id="hpa-max"
                    value={maxReplicas}
                    inputMode="numeric"
                    onChange={(event) => setMaxReplicas(event.target.value)}
                  />
                </Field>
              </div>
            </FormSection>

            <FormSection
              title="指标"
              hint="至少一个；Utilization 是相对 requests 的百分比，需要目标容器声明 requests"
            >
              <MetricRows rows={metrics} onChange={setMetrics} />
            </FormSection>

            <FormSection title="伸缩行为" hint="可选；不启用时使用 Kubernetes 默认策略">
              <RulesEditor label="扩容" rules={scaleUp} onChange={setScaleUp} idPrefix="up" />
              <div className="mt-3">
                <RulesEditor
                  label="缩容"
                  rules={scaleDown}
                  onChange={setScaleDown}
                  idPrefix="down"
                />
              </div>
            </FormSection>
          </div>

          <Alert tone="info" className="mt-4">
            目标：{clusterName} / {namespace}
          </Alert>
          {mutation.error ? (
            <Alert tone="danger" className="mt-3">
              {errorMessage(mutation.error)}
            </Alert>
          ) : null}

          <DialogFooter>
            <Button variant="ghost" onClick={onClose} disabled={mutation.isPending}>
              取消
            </Button>
            <Button
              variant="primary"
              disabled={!valid || mutation.isPending}
              onClick={() => submit(true, buildSpec())}
            >
              {mutation.isPending ? "预检中…" : "执行 DryRun 预检"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <SensitiveActionDialog
        open={previewed !== null}
        onOpenChange={(open) => !open && setPreviewed(null)}
        title={existing ? "确认更新 HPA" : "确认创建 HPA"}
        description="DryRun 已通过。确认后将向同一集群提交实际变更。"
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
          <div className="grid grid-cols-[9rem_1fr_1fr_9rem_6rem] gap-2">
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
    <div className="border-border/60 grid gap-2 rounded-md border p-2">
      <label className="flex items-center gap-2 text-[13px]">
        <Checkbox
          checked={rules.enabled}
          onCheckedChange={(checked) => onChange({ ...rules, enabled: checked === true })}
        />
        自定义{label}策略
      </label>
      {rules.enabled ? (
        <>
          <div className="grid grid-cols-2 gap-2">
            <Field label="稳定窗口（秒）" htmlFor={`hpa-${idPrefix}-window`}>
              <Input
                id={`hpa-${idPrefix}-window`}
                value={rules.stabilizationWindow}
                inputMode="numeric"
                placeholder="留空使用默认"
                onChange={(event) =>
                  onChange({ ...rules, stabilizationWindow: event.target.value })
                }
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
              <Input
                value={policy.value}
                aria-label={`${label}策略 ${index + 1} 数值`}
                placeholder="数值"
                inputMode="numeric"
                onChange={(event) =>
                  onChange({
                    ...rules,
                    policies: rules.policies.map((entry, position) =>
                      position === index ? { ...entry, value: event.target.value } : entry,
                    ),
                  })
                }
              />
              <Input
                value={policy.periodSeconds}
                aria-label={`${label}策略 ${index + 1} 周期秒数`}
                placeholder="周期（秒）"
                inputMode="numeric"
                onChange={(event) =>
                  onChange({
                    ...rules,
                    policies: rules.policies.map((entry, position) =>
                      position === index ? { ...entry, periodSeconds: event.target.value } : entry,
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
  children,
}: {
  title: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <section>
      <div className="mb-2 flex items-baseline gap-2">
        <h4 className="text-foreground text-[13px] font-medium">{title}</h4>
        {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
      </div>
      {children}
    </section>
  );
}

function Field({
  label,
  htmlFor,
  children,
}: {
  label: string;
  htmlFor: string;
  children: ReactNode;
}) {
  return (
    <div className="grid content-start gap-1.5">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
    </div>
  );
}
