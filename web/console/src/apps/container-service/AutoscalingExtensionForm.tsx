import { useMemo, useState, type ReactNode } from "react";
import { Plus, X } from "lucide-react";
import { toast } from "sonner";

import { errorMessage, errorRequestId } from "@/api/errors";
import {
  useCreateKEDAScaledObject,
  useCreateVerticalPodAutoscaler,
  useUpdateKEDAScaledObject,
  useUpdateVerticalPodAutoscaler,
} from "@/api/queries/autoscaling";
import type {
  KubernetesKEDADetail,
  KubernetesKEDASpecInput,
  KubernetesKEDATrigger,
  KubernetesVPADetail,
  KubernetesVPASpecInput,
  KubernetesVPAContainerPolicy,
} from "@/api/types";
import { PageHeader } from "@/apps/AppShell";
import { notifyFailure } from "@/components/common/notify";
import { Button } from "@/components/ui/button";
import { Input, NumericInput } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, Card, Checkbox, Switch } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { parseQuantity } from "@/lib/quantity";
import { useSubmissionKey } from "@/lib/use-submission-key";

type Kind = "vpa" | "keda";
type Detail = KubernetesVPADetail | KubernetesKEDADetail;

type VPAUpdateMode = KubernetesVPASpecInput["update_mode"];
type VPAPolicyMode = KubernetesVPAContainerPolicy["mode"];
type VPAControlledValues = KubernetesVPAContainerPolicy["controlled_values"];

type VPAPolicyDraft = {
  containerName: string;
  mode: VPAPolicyMode;
  minCPU: string;
  maxCPU: string;
  minMemory: string;
  maxMemory: string;
  resourceSelection: "default" | "custom";
  controlCPU: boolean;
  controlMemory: boolean;
  controlledValues: VPAControlledValues;
};

type MetadataDraft = { key: string; value: string };
type KEDAMetricType = NonNullable<KubernetesKEDATrigger["metric_type"]>;

type KEDATriggerDraft = {
  type: string;
  name: string;
  metricType: KEDAMetricType;
  useCachedMetrics: boolean;
  authenticationRefName: string;
  metadata: MetadataDraft[];
  redactedMetadataKeys: string[];
};

const DEFAULT_OPTION = "__default__";
const DNS_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;
const DNS_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const MAX_REPLICAS = 1_000_000;
const MAX_VPA_POLICIES = 64;
const MAX_KEDA_TRIGGERS = 16;
const MAX_METADATA_ENTRIES = 64;

function emptyVPAPolicy(): VPAPolicyDraft {
  return {
    containerName: "*",
    mode: "",
    minCPU: "",
    maxCPU: "",
    minMemory: "",
    maxMemory: "",
    resourceSelection: "default",
    controlCPU: true,
    controlMemory: true,
    controlledValues: "",
  };
}

function vpaPolicyDraft(policy: KubernetesVPAContainerPolicy): VPAPolicyDraft {
  const controlledResources = policy.controlled_resources ?? [];
  return {
    containerName: policy.container_name,
    mode: policy.mode,
    minCPU: policy.min_allowed?.cpu ?? "",
    maxCPU: policy.max_allowed?.cpu ?? "",
    minMemory: policy.min_allowed?.memory ?? "",
    maxMemory: policy.max_allowed?.memory ?? "",
    resourceSelection: controlledResources.length === 0 ? "default" : "custom",
    controlCPU: controlledResources.includes("cpu"),
    controlMemory: controlledResources.includes("memory"),
    controlledValues: policy.controlled_values,
  };
}

function emptyKEDATrigger(): KEDATriggerDraft {
  return {
    type: "prometheus",
    name: "",
    metricType: "",
    useCachedMetrics: false,
    authenticationRefName: "",
    metadata: [
      { key: "serverAddress", value: "http://prometheus:9090" },
      { key: "query", value: "sum(queue_depth)" },
      { key: "threshold", value: "10" },
    ],
    redactedMetadataKeys: [],
  };
}

function kedaTriggerDraft(trigger: KubernetesKEDATrigger): KEDATriggerDraft {
  const redactedMetadataKeys = trigger.redacted_metadata_keys ?? [];
  const redacted = new Set(redactedMetadataKeys);
  const legacyMetricType =
    isResourceMetricTrigger(trigger.type) &&
    (trigger.metadata?.type === "Utilization" || trigger.metadata?.type === "AverageValue")
      ? trigger.metadata.type
      : "";
  const metadata = Object.entries(trigger.metadata ?? {})
    .filter(
      ([key, value]) =>
        !redacted.has(key) && value !== "[redacted]" && !(key === "type" && legacyMetricType),
    )
    .map(([key, value]) => ({ key, value }));
  return {
    type: trigger.type,
    name: trigger.name,
    metricType:
      trigger.metric_type ||
      legacyMetricType ||
      (isResourceMetricTrigger(trigger.type) ? "Utilization" : ""),
    useCachedMetrics: trigger.use_cached_metrics,
    authenticationRefName: trigger.authentication_ref_name,
    metadata: metadata.length > 0 ? metadata : [{ key: "", value: "" }],
    redactedMetadataKeys,
  };
}

export function AutoscalingExtensionForm({
  kind,
  clusterId,
  clusterName,
  namespace,
  existing,
  onClose,
}: {
  kind: Kind;
  clusterId: string;
  clusterName: string;
  namespace: string;
  existing: Detail | null;
  onClose: () => void;
}) {
  const vpa = kind === "vpa" ? (existing as KubernetesVPADetail | null) : null;
  const keda = kind === "keda" ? (existing as KubernetesKEDADetail | null) : null;
  const [name, setName] = useState(existing?.name ?? "");
  const [targetKind, setTargetKind] = useState<string>(existing?.target.kind ?? "Deployment");
  const [targetName, setTargetName] = useState(existing?.target.name ?? "");
  const [updateMode, setUpdateMode] = useState<VPAUpdateMode>(
    (vpa?.update_mode as VPAUpdateMode) || "Off",
  );
  const [vpaPolicies, setVPAPolicies] = useState<VPAPolicyDraft[]>(
    (vpa?.container_policies ?? []).map(vpaPolicyDraft),
  );
  const [minimum, setMinimum] = useState(String(keda?.min_replicas ?? 0));
  const [maximum, setMaximum] = useState(String(keda?.max_replicas ?? 10));
  const [polling, setPolling] = useState(String(keda?.polling_interval ?? 30));
  const [cooldown, setCooldown] = useState(String(keda?.cooldown_period ?? 300));
  const [kedaTriggers, setKEDATriggers] = useState<KEDATriggerDraft[]>(
    keda ? (keda.triggers ?? []).map(kedaTriggerDraft) : [emptyKEDATrigger()],
  );
  const [previewed, setPreviewed] = useState(false);
  const createVPA = useCreateVerticalPodAutoscaler();
  const updateVPA = useUpdateVerticalPodAutoscaler();
  const createKEDA = useCreateKEDAScaledObject();
  const updateKEDA = useUpdateKEDAScaledObject();
  const pending =
    createVPA.isPending || updateVPA.isPending || createKEDA.isPending || updateKEDA.isPending;
  const mutationError =
    kind === "vpa"
      ? vpa
        ? updateVPA.error
        : createVPA.error
      : keda
        ? updateKEDA.error
        : createKEDA.error;
  const previewKey = useSubmissionKey(true);
  const applyKey = useSubmissionKey(true);
  const label = kind === "vpa" ? "VPA" : "KEDA ScaledObject";
  const problem = useMemo(
    () =>
      commonProblem(name, targetName, Boolean(existing)) ??
      (kind === "vpa"
        ? vpaProblem(vpaPolicies)
        : kedaProblem(minimum, maximum, polling, cooldown, kedaTriggers)),
    [
      cooldown,
      existing,
      kedaTriggers,
      kind,
      maximum,
      minimum,
      name,
      polling,
      targetName,
      vpaPolicies,
    ],
  );

  const changed = <T,>(setter: (value: T) => void, value: T) => {
    setter(value);
    setPreviewed(false);
  };

  const submit = (dryRun: boolean) => {
    if (problem) {
      toast.error(problem);
      return;
    }
    const common = {
      clusterId,
      namespace,
      name: name.trim(),
      dryRun,
      idempotencyKey: dryRun ? previewKey : applyKey,
    };
    let promise: Promise<unknown>;
    if (kind === "vpa") {
      const spec: KubernetesVPASpecInput = {
        target: {
          api_version: "apps/v1",
          kind: targetKind as "Deployment" | "StatefulSet" | "DaemonSet",
          name: targetName.trim(),
        },
        update_mode: updateMode,
        container_policies: vpaPolicies.map(buildVPAPolicy),
      };
      promise = vpa
        ? updateVPA.mutateAsync({
            ...common,
            uid: vpa.uid,
            resourceVersion: vpa.resource_version,
            spec,
          })
        : createVPA.mutateAsync({ ...common, spec });
    } else {
      const spec: KubernetesKEDASpecInput = {
        target: {
          api_version: "apps/v1",
          kind: targetKind as "Deployment" | "StatefulSet",
          name: targetName.trim(),
        },
        min_replicas: Number(minimum),
        max_replicas: Number(maximum),
        polling_interval: Number(polling),
        cooldown_period: Number(cooldown),
        triggers: kedaTriggers.map(buildKEDATrigger),
      };
      promise = keda
        ? updateKEDA.mutateAsync({
            ...common,
            uid: keda.uid,
            resourceVersion: keda.resource_version,
            spec,
          })
        : createKEDA.mutateAsync({ ...common, spec });
    }
    void promise
      .then(() => {
        if (dryRun) {
          setPreviewed(true);
          toast.success(`${label} DryRun 预检已通过`);
        } else {
          toast.success(`${label} 已保存`);
          onClose();
        }
      })
      .catch((error: unknown) => {
        notifyFailure(`${label}${dryRun ? " DryRun 预检" : " 保存"}失败`, error);
      });
  };

  return (
    <div className="grid gap-3">
      <PageHeader
        title={`${existing ? "编辑" : "创建"} ${label} · ${clusterName} · ${namespace}`}
        onBack={onClose}
      />
      <div className="grid gap-4">
        <FormSection title="基本信息">
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
            <Field label="名称" htmlFor="extension-name">
              <Input
                id="extension-name"
                value={name}
                disabled={Boolean(existing) || pending}
                onChange={(event) => changed(setName, event.target.value)}
              />
            </Field>
            <Field label="目标类型" htmlFor="extension-target-kind">
              <Select value={targetKind} onValueChange={(value) => changed(setTargetKind, value)}>
                <SelectTrigger id="extension-target-kind" disabled={pending}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="Deployment">Deployment</SelectItem>
                  <SelectItem value="StatefulSet">StatefulSet</SelectItem>
                  {kind === "vpa" ? <SelectItem value="DaemonSet">DaemonSet</SelectItem> : null}
                </SelectContent>
              </Select>
            </Field>
            <Field label="目标名称" htmlFor="extension-target-name">
              <Input
                id="extension-target-name"
                value={targetName}
                disabled={pending}
                onChange={(event) => changed(setTargetName, event.target.value)}
              />
            </Field>
            {kind === "vpa" ? (
              <Field label="更新模式" htmlFor="vpa-update-mode">
                <Select
                  value={updateMode}
                  onValueChange={(value) => changed(setUpdateMode, value as VPAUpdateMode)}
                >
                  <SelectTrigger id="vpa-update-mode" disabled={pending}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(["Off", "Initial", "Recreate", "InPlaceOrRecreate", "InPlace"] as const).map(
                      (mode) => (
                        <SelectItem key={mode} value={mode}>
                          {mode}
                        </SelectItem>
                      ),
                    )}
                  </SelectContent>
                </Select>
              </Field>
            ) : (
              <KEDABounds
                minimum={minimum}
                maximum={maximum}
                polling={polling}
                cooldown={cooldown}
                disabled={pending}
                onMinimum={(value) => changed(setMinimum, value)}
                onMaximum={(value) => changed(setMaximum, value)}
                onPolling={(value) => changed(setPolling, value)}
                onCooldown={(value) => changed(setCooldown, value)}
              />
            )}
          </div>
        </FormSection>

        {kind === "vpa" ? (
          <VPAPolicyEditor
            policies={vpaPolicies}
            disabled={pending}
            onChange={(value) => changed(setVPAPolicies, value)}
          />
        ) : (
          <KEDATriggerEditor
            triggers={kedaTriggers}
            disabled={pending}
            onChange={(value) => changed(setKEDATriggers, value)}
          />
        )}

        {problem ? <Alert tone="danger">{problem}</Alert> : null}
        {mutationError ? (
          <Alert tone="danger" role="alert" aria-live="assertive">
            {errorMessage(mutationError)}
            {errorRequestId(mutationError) ? (
              <span className="zke-mono mt-1 block text-xs opacity-80">
                请求 ID：{errorRequestId(mutationError)}
              </span>
            ) : null}
          </Alert>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button variant="secondary" disabled={pending} onClick={onClose}>
            取消
          </Button>
          <Button
            variant={previewed ? "danger" : "primary"}
            disabled={pending || Boolean(problem)}
            onClick={() => submit(!previewed)}
          >
            {previewed ? "确认应用" : "执行 DryRun 预检"}
          </Button>
        </div>
      </div>
    </div>
  );
}

function KEDABounds({
  minimum,
  maximum,
  polling,
  cooldown,
  disabled,
  onMinimum,
  onMaximum,
  onPolling,
  onCooldown,
}: {
  minimum: string;
  maximum: string;
  polling: string;
  cooldown: string;
  disabled: boolean;
  onMinimum: (value: string) => void;
  onMaximum: (value: string) => void;
  onPolling: (value: string) => void;
  onCooldown: (value: string) => void;
}) {
  return (
    <>
      <NumberField label="最小副本" value={minimum} disabled={disabled} setValue={onMinimum} />
      <NumberField label="最大副本" value={maximum} disabled={disabled} setValue={onMaximum} />
      <NumberField
        label="轮询间隔（秒）"
        value={polling}
        disabled={disabled}
        setValue={onPolling}
      />
      <NumberField
        label="冷却时间（秒）"
        value={cooldown}
        disabled={disabled}
        setValue={onCooldown}
      />
    </>
  );
}

function VPAPolicyEditor({
  policies,
  disabled,
  onChange,
}: {
  policies: VPAPolicyDraft[];
  disabled: boolean;
  onChange: (policies: VPAPolicyDraft[]) => void;
}) {
  const update = (index: number, patch: Partial<VPAPolicyDraft>) =>
    onChange(
      policies.map((policy, position) => (position === index ? { ...policy, ...patch } : policy)),
    );
  return (
    <FormSection
      title="容器策略"
      hint="留空时使用 VPA 默认策略；可按容器名或 * 设置 CPU、内存边界。"
      actions={
        <Button
          size="sm"
          variant="secondary"
          disabled={disabled || policies.length >= MAX_VPA_POLICIES}
          onClick={() => onChange([...policies, emptyVPAPolicy()])}
        >
          <Plus />
          添加容器策略
        </Button>
      }
    >
      {policies.length === 0 ? (
        <Alert tone="info">当前没有容器策略，VPA 将使用控制器默认值。</Alert>
      ) : null}
      {policies.map((policy, index) => (
        <Card key={index} className="grid gap-4">
          <div className="flex items-center justify-between gap-3">
            <span className="text-foreground text-sm font-medium">策略 {index + 1}</span>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={`移除容器策略 ${index + 1}`}
              disabled={disabled}
              onClick={() => onChange(policies.filter((_, position) => position !== index))}
            >
              <X />
            </Button>
          </div>
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            <Field
              label="容器名称"
              htmlFor={`vpa-policy-${index}-container`}
              hint="* 表示其他全部容器"
            >
              <Input
                id={`vpa-policy-${index}-container`}
                value={policy.containerName}
                disabled={disabled}
                onChange={(event) => update(index, { containerName: event.target.value })}
              />
            </Field>
            <Field label="容器模式" htmlFor={`vpa-policy-${index}-mode`}>
              <Select
                value={policy.mode || DEFAULT_OPTION}
                onValueChange={(value) =>
                  update(index, { mode: value === DEFAULT_OPTION ? "" : (value as VPAPolicyMode) })
                }
              >
                <SelectTrigger id={`vpa-policy-${index}-mode`} disabled={disabled}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={DEFAULT_OPTION}>默认</SelectItem>
                  <SelectItem value="Auto">Auto</SelectItem>
                  <SelectItem value="Off">Off</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field label="调整范围" htmlFor={`vpa-policy-${index}-values`}>
              <Select
                value={policy.controlledValues || DEFAULT_OPTION}
                onValueChange={(value) =>
                  update(index, {
                    controlledValues:
                      value === DEFAULT_OPTION ? "" : (value as VPAControlledValues),
                  })
                }
              >
                <SelectTrigger id={`vpa-policy-${index}-values`} disabled={disabled}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={DEFAULT_OPTION}>默认</SelectItem>
                  <SelectItem value="RequestsOnly">只调整 Requests</SelectItem>
                  <SelectItem value="RequestsAndLimits">调整 Requests 和 Limits</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>
          <div className="grid gap-3 lg:grid-cols-2">
            <QuantityBounds
              label="CPU"
              minimum={policy.minCPU}
              maximum={policy.maxCPU}
              disabled={disabled}
              onMinimum={(value) => update(index, { minCPU: value })}
              onMaximum={(value) => update(index, { maxCPU: value })}
              examples="例如 100m、2"
            />
            <QuantityBounds
              label="内存"
              minimum={policy.minMemory}
              maximum={policy.maxMemory}
              disabled={disabled}
              onMinimum={(value) => update(index, { minMemory: value })}
              onMaximum={(value) => update(index, { maxMemory: value })}
              examples="例如 128Mi、2Gi"
            />
          </div>
          <div className="grid items-end gap-3 md:grid-cols-2">
            <Field label="受控资源">
              <Select
                value={policy.resourceSelection}
                onValueChange={(value) =>
                  update(index, { resourceSelection: value as "default" | "custom" })
                }
              >
                <SelectTrigger disabled={disabled}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="default">控制器默认（CPU 和内存）</SelectItem>
                  <SelectItem value="custom">自定义</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            {policy.resourceSelection === "custom" ? (
              <div className="flex min-h-9 flex-wrap items-center gap-4">
                <CheckField
                  label="CPU"
                  checked={policy.controlCPU}
                  disabled={disabled}
                  onChange={(checked) => update(index, { controlCPU: checked })}
                />
                <CheckField
                  label="内存"
                  checked={policy.controlMemory}
                  disabled={disabled}
                  onChange={(checked) => update(index, { controlMemory: checked })}
                />
              </div>
            ) : (
              <p className="text-subtle-foreground flex min-h-9 items-center text-xs">
                默认同时控制 CPU 与内存 Requests。
              </p>
            )}
          </div>
        </Card>
      ))}
    </FormSection>
  );
}

function QuantityBounds({
  label,
  minimum,
  maximum,
  disabled,
  onMinimum,
  onMaximum,
  examples,
}: {
  label: string;
  minimum: string;
  maximum: string;
  disabled: boolean;
  onMinimum: (value: string) => void;
  onMaximum: (value: string) => void;
  examples: string;
}) {
  return (
    <div className="bg-surface-muted/45 grid gap-2 rounded-md p-3">
      <span className="text-foreground text-xs font-medium">{label} 边界</span>
      <div className="grid gap-2 sm:grid-cols-2">
        <Field label="最小值" hint={examples}>
          <Input value={minimum} disabled={disabled} onChange={(e) => onMinimum(e.target.value)} />
        </Field>
        <Field label="最大值" hint={examples}>
          <Input value={maximum} disabled={disabled} onChange={(e) => onMaximum(e.target.value)} />
        </Field>
      </div>
    </div>
  );
}

function KEDATriggerEditor({
  triggers,
  disabled,
  onChange,
}: {
  triggers: KEDATriggerDraft[];
  disabled: boolean;
  onChange: (triggers: KEDATriggerDraft[]) => void;
}) {
  const update = (index: number, patch: Partial<KEDATriggerDraft>) =>
    onChange(
      triggers.map((trigger, position) =>
        position === index ? { ...trigger, ...patch } : trigger,
      ),
    );
  return (
    <FormSection
      title="触发器"
      hint="每个触发器单独填写类型、认证引用和 metadata，不需要编写 JSON。"
      actions={
        <Button
          size="sm"
          variant="secondary"
          disabled={disabled || triggers.length >= MAX_KEDA_TRIGGERS}
          onClick={() => onChange([...triggers, emptyKEDATrigger()])}
        >
          <Plus />
          添加触发器
        </Button>
      }
    >
      <Alert tone="warning">
        metadata 不接受密码、Token、Secret、API Key 或连接串；认证信息必须通过同命名空间的
        TriggerAuthentication 引用。
      </Alert>
      {triggers.map((trigger, index) => (
        <Card key={index} className="grid gap-4">
          <div className="flex items-center justify-between gap-3">
            <span className="text-foreground text-sm font-medium">触发器 {index + 1}</span>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={`移除触发器 ${index + 1}`}
              disabled={disabled || triggers.length <= 1}
              onClick={() => onChange(triggers.filter((_, position) => position !== index))}
            >
              <X />
            </Button>
          </div>
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            <Field
              label="类型"
              htmlFor={`keda-trigger-${index}-type`}
              hint="例如 prometheus、cron、rabbitmq、kafka、cpu 或 memory"
            >
              <Input
                id={`keda-trigger-${index}-type`}
                value={trigger.type}
                disabled={disabled}
                onChange={(event) => {
                  const type = event.target.value;
                  update(index, {
                    type,
                    metricType:
                      isResourceMetricTrigger(type) &&
                      trigger.metricType !== "Utilization" &&
                      trigger.metricType !== "AverageValue"
                        ? "Utilization"
                        : trigger.metricType,
                  });
                }}
              />
            </Field>
            <Field label="名称（可选）" htmlFor={`keda-trigger-${index}-name`}>
              <Input
                id={`keda-trigger-${index}-name`}
                value={trigger.name}
                disabled={disabled}
                onChange={(event) => update(index, { name: event.target.value })}
              />
            </Field>
            <Field
              label="指标目标类型"
              htmlFor={`keda-trigger-${index}-metric-type`}
              hint={
                isResourceMetricTrigger(trigger.type)
                  ? "CPU/Memory 必须选择 Utilization 或 AverageValue"
                  : "可选；对应 KEDA trigger.metricType"
              }
            >
              <Select
                value={trigger.metricType || DEFAULT_OPTION}
                onValueChange={(value) =>
                  update(index, {
                    metricType: value === DEFAULT_OPTION ? "" : (value as KEDAMetricType),
                  })
                }
              >
                <SelectTrigger id={`keda-trigger-${index}-metric-type`} disabled={disabled}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {!isResourceMetricTrigger(trigger.type) ? (
                    <SelectItem value={DEFAULT_OPTION}>控制器默认</SelectItem>
                  ) : null}
                  <SelectItem value="Utilization">Utilization</SelectItem>
                  {!isResourceMetricTrigger(trigger.type) ? (
                    <SelectItem value="Value">Value</SelectItem>
                  ) : null}
                  <SelectItem value="AverageValue">AverageValue</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field
              label="TriggerAuthentication 名称（可选）"
              htmlFor={`keda-trigger-${index}-auth`}
              className="md:col-span-2 xl:col-span-3"
            >
              <Input
                id={`keda-trigger-${index}-auth`}
                value={trigger.authenticationRefName}
                disabled={disabled}
                onChange={(event) => update(index, { authenticationRefName: event.target.value })}
              />
            </Field>
          </div>
          <label className="flex items-center gap-2 text-[13px]">
            <Switch
              checked={trigger.useCachedMetrics}
              disabled={disabled}
              onCheckedChange={(checked) => update(index, { useCachedMetrics: checked })}
            />
            使用缓存指标
          </label>
          {trigger.redactedMetadataKeys.length > 0 ? (
            <Alert tone="warning">
              已有对象包含已脱敏 metadata：{trigger.redactedMetadataKeys.join("、")}
              。这些值不会回填；保存时将移除， 请先把认证信息迁移到上面的 TriggerAuthentication。
            </Alert>
          ) : null}
          <MetadataEditor
            triggerIndex={index}
            rows={trigger.metadata}
            disabled={disabled}
            onChange={(metadata) => update(index, { metadata })}
          />
        </Card>
      ))}
    </FormSection>
  );
}

function MetadataEditor({
  triggerIndex,
  rows,
  disabled,
  onChange,
}: {
  triggerIndex: number;
  rows: MetadataDraft[];
  disabled: boolean;
  onChange: (rows: MetadataDraft[]) => void;
}) {
  const update = (index: number, patch: Partial<MetadataDraft>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...patch } : row)));
  return (
    <div className="grid gap-2">
      <Label>Metadata</Label>
      {rows.map((row, index) => (
        <div
          key={index}
          className="grid gap-2 sm:grid-cols-[minmax(8rem,0.8fr)_minmax(10rem,1.2fr)_auto]"
        >
          <Input
            value={row.key}
            aria-label={`触发器 ${triggerIndex + 1} metadata ${index + 1} 键`}
            placeholder="键，例如 threshold"
            disabled={disabled}
            onChange={(event) => update(index, { key: event.target.value })}
          />
          <Input
            value={row.value}
            aria-label={`触发器 ${triggerIndex + 1} metadata ${index + 1} 值`}
            placeholder="值"
            disabled={disabled}
            onChange={(event) => update(index, { value: event.target.value })}
          />
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label={`移除触发器 ${triggerIndex + 1} metadata ${index + 1}`}
            disabled={disabled || rows.length <= 1}
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
          disabled={disabled || rows.length >= MAX_METADATA_ENTRIES}
          onClick={() => onChange([...rows, { key: "", value: "" }])}
        >
          <Plus />
          添加 Metadata
        </Button>
      </div>
    </div>
  );
}

function buildVPAPolicy(policy: VPAPolicyDraft): KubernetesVPAContainerPolicy {
  const minAllowed: Record<string, string> = {};
  const maxAllowed: Record<string, string> = {};
  if (policy.minCPU.trim()) minAllowed.cpu = policy.minCPU.trim();
  if (policy.minMemory.trim()) minAllowed.memory = policy.minMemory.trim();
  if (policy.maxCPU.trim()) maxAllowed.cpu = policy.maxCPU.trim();
  if (policy.maxMemory.trim()) maxAllowed.memory = policy.maxMemory.trim();
  const controlledResources: ("cpu" | "memory")[] = [];
  if (policy.resourceSelection === "custom") {
    if (policy.controlCPU) controlledResources.push("cpu");
    if (policy.controlMemory) controlledResources.push("memory");
  }
  return {
    container_name: policy.containerName.trim(),
    mode: policy.mode,
    min_allowed: minAllowed,
    max_allowed: maxAllowed,
    controlled_resources: controlledResources,
    controlled_values: policy.controlledValues,
  };
}

function buildKEDATrigger(trigger: KEDATriggerDraft): KubernetesKEDATrigger {
  return {
    type: trigger.type.trim(),
    name: trigger.name.trim(),
    metric_type: trigger.metricType,
    use_cached_metrics: trigger.useCachedMetrics,
    authentication_ref_name: trigger.authenticationRefName.trim(),
    metadata: Object.fromEntries(trigger.metadata.map((row) => [row.key, row.value])),
    redacted_metadata_keys: [],
  };
}

function commonProblem(name: string, targetName: string, editing: boolean): string | null {
  const resourceName = name.trim();
  if (!editing) {
    if (!resourceName) return "请填写名称。";
    if (resourceName.length > 253 || !DNS_SUBDOMAIN.test(resourceName)) {
      return "名称必须是最长 253 个字符的合法 DNS 子域名。";
    }
  }
  const target = targetName.trim();
  if (!target) return "请填写目标工作负载名称。";
  if (target.length > 253 || !DNS_SUBDOMAIN.test(target)) {
    return "目标工作负载名称必须是合法的 DNS 子域名。";
  }
  return null;
}

function vpaProblem(policies: VPAPolicyDraft[]): string | null {
  const seen = new Set<string>();
  for (const [index, policy] of policies.entries()) {
    const where = `容器策略 ${index + 1}`;
    const container = policy.containerName.trim();
    if (
      !container ||
      (container !== "*" && (container.length > 63 || !DNS_LABEL.test(container)))
    ) {
      return `${where}的容器名称必须是 * 或合法的 DNS 标签。`;
    }
    if (seen.has(container)) return `${where}与另一条策略使用了相同容器名称。`;
    seen.add(container);
    if (policy.resourceSelection === "custom" && !policy.controlCPU && !policy.controlMemory) {
      return `${where}选择了自定义受控资源，请至少选择 CPU 或内存。`;
    }
    const quantityProblem =
      quantityBoundsProblem(where, "CPU", policy.minCPU, policy.maxCPU) ??
      quantityBoundsProblem(where, "内存", policy.minMemory, policy.maxMemory);
    if (quantityProblem) return quantityProblem;
  }
  return null;
}

function quantityBoundsProblem(
  where: string,
  resourceName: string,
  minimum: string,
  maximum: string,
): string | null {
  const min = minimum.trim() ? parseQuantity(minimum) : null;
  const max = maximum.trim() ? parseQuantity(maximum) : null;
  if (minimum.trim() && (min === null || min <= 0)) {
    return `${where}的${resourceName}最小值必须是正数 Kubernetes quantity。`;
  }
  if (maximum.trim() && (max === null || max <= 0)) {
    return `${where}的${resourceName}最大值必须是正数 Kubernetes quantity。`;
  }
  if (min !== null && max !== null && min > max) {
    return `${where}的${resourceName}最小值不能超过最大值。`;
  }
  return null;
}

function kedaProblem(
  minimum: string,
  maximum: string,
  polling: string,
  cooldown: string,
  triggers: KEDATriggerDraft[],
): string | null {
  const numbers = [minimum, maximum, polling, cooldown];
  if (numbers.some((value) => !/^\d+$/.test(value.trim()))) {
    return "副本数、轮询间隔和冷却时间必须是整数。";
  }
  const [min, max, poll, cool] = numbers.map(Number);
  if (min! < 0 || max! < 1 || max! > MAX_REPLICAS || min! > max!) {
    return `副本范围无效：最小副本不得小于 0，最大副本为 1–${MAX_REPLICAS}，且最大值不能小于最小值。`;
  }
  if (poll! < 1 || poll! > 3_600) return "轮询间隔必须是 1–3600 秒。";
  if (cool! < 0 || cool! > 86_400) return "冷却时间必须是 0–86400 秒。";
  if (triggers.length === 0) return "请至少添加一个触发器。";
  for (const [index, trigger] of triggers.entries()) {
    const where = `触发器 ${index + 1}`;
    const type = trigger.type.trim();
    if (!type || type.length > 63 || !DNS_LABEL.test(type)) {
      return `${where}的类型必须是最长 63 个字符的合法 DNS 标签。`;
    }
    const triggerName = trigger.name.trim();
    if (triggerName && (triggerName.length > 63 || !DNS_LABEL.test(triggerName))) {
      return `${where}的名称必须是合法的 DNS 标签。`;
    }
    if (
      isResourceMetricTrigger(type) &&
      trigger.metricType !== "Utilization" &&
      trigger.metricType !== "AverageValue"
    ) {
      return `${where}的 CPU/Memory 指标目标类型必须是 Utilization 或 AverageValue。`;
    }
    const auth = trigger.authenticationRefName.trim();
    if (auth && (auth.length > 253 || !DNS_SUBDOMAIN.test(auth))) {
      return `${where}的 TriggerAuthentication 名称必须是合法的 DNS 子域名。`;
    }
    if (trigger.redactedMetadataKeys.length > 0 && !auth) {
      return `${where}包含已脱敏的认证 metadata；请设置 TriggerAuthentication 后再保存。`;
    }
    if (trigger.metadata.length === 0) return `${where}至少需要一项 metadata。`;
    const keys = new Set<string>();
    for (const [metadataIndex, row] of trigger.metadata.entries()) {
      const position = `${where}的第 ${metadataIndex + 1} 项 metadata`;
      if (!row.key || row.key.trim() !== row.key) return `${position}缺少合法键名。`;
      if (row.value.trim() !== row.value) return `${position}的值不能带首尾空白。`;
      if (keys.has(row.key)) return `${where}包含重复的 metadata 键 ${row.key}。`;
      keys.add(row.key);
      if (isResourceMetricTrigger(type) && row.key === "type") {
        return `${where}不能再使用 metadata.type；KEDA 2.18 起请改用上方的指标目标类型字段。`;
      }
      if (sensitiveKEDAMetadataKey(row.key)) {
        return `${position}可能包含敏感认证信息，请改用 TriggerAuthentication。`;
      }
    }
  }
  return null;
}

function isResourceMetricTrigger(type: string): boolean {
  return type.trim() === "cpu" || type.trim() === "memory";
}

function sensitiveKEDAMetadataKey(key: string): boolean {
  const lower = key.toLowerCase();
  const fragments = [
    "password",
    "passwd",
    "token",
    "secret",
    "apikey",
    "api_key",
    "accountkey",
    "account_key",
    "connectionstring",
    "connection_string",
  ];
  return (
    fragments.some((fragment) => lower.includes(fragment)) ||
    lower === "sas" ||
    lower.endsWith("sastoken") ||
    lower.endsWith("sas_token")
  );
}

function Field({
  label,
  htmlFor,
  hint,
  className,
  children,
}: {
  label: string;
  htmlFor?: string;
  hint?: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={`grid content-start gap-1.5 ${className ?? ""}`}>
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint ? <p className="text-muted-foreground text-xs">{hint}</p> : null}
    </div>
  );
}

function FormSection({
  title,
  hint,
  actions,
  children,
}: {
  title: string;
  hint?: string;
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="grid gap-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h4 className="text-foreground text-[13px] font-medium">{title}</h4>
          {hint ? (
            <p className="text-subtle-foreground mt-1 text-xs leading-relaxed">{hint}</p>
          ) : null}
        </div>
        {actions ? <div className="shrink-0">{actions}</div> : null}
      </div>
      {children}
    </section>
  );
}

function NumberField({
  label,
  value,
  disabled,
  setValue,
}: {
  label: string;
  value: string;
  disabled: boolean;
  setValue: (value: string) => void;
}) {
  return (
    <Field label={label}>
      <NumericInput value={value} disabled={disabled} onValueChange={setValue} />
    </Field>
  );
}

function CheckField({
  label,
  checked,
  disabled,
  onChange,
}: {
  label: string;
  checked: boolean;
  disabled: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <label className="flex items-center gap-2 text-[13px]">
      <Checkbox
        checked={checked}
        disabled={disabled}
        onCheckedChange={(value) => onChange(value === true)}
      />
      {label}
    </label>
  );
}
