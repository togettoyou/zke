import { useState, type ReactNode } from "react";
import { Plus, X } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import { useCreateWorkload, type WorkloadCreateSpec } from "@/api/queries/workloads";
import type { KubernetesWorkloadResource } from "@/api/types";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
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

import { kindLabel } from "./workload-catalog";

/*
 * Client-side shapes, mirroring what the Server validates. They exist so the
 * form can say what is wrong before a round trip, not to replace the check —
 * every one of these is enforced again by the Server, which stays the authority.
 */
const DNS_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
/** A Service name is an RFC 1035 label: it has to start with a letter. */
const DNS_1035_LABEL = /^[a-z]([-a-z0-9]*[a-z0-9])?$/;
const LABEL_NAME = /^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/;
const LABEL_VALUE = /^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$/;
/** The Server owns this key and refuses a request that carries it. */
const RESERVED_LABEL_KEY = "zke.io/workload-id";
const INT32_MAX = 2_147_483_647;
const MAX_CONTAINERS = 100;
const MAX_IMAGE_BYTES = 2048;
const MAX_CRON_SCHEDULE_BYTES = 512;
const MAX_CRON_TIME_ZONE_BYTES = 128;
/** Radix Select cannot hold an empty value, so "unset" needs a name of its own. */
const DEFAULT_OPTION = "__default__";

type ContainerDraft = { name: string; image: string; policy: string };
type LabelDraft = { key: string; value: string };

const EMPTY_CONTAINER: ContainerDraft = { name: "", image: "", policy: DEFAULT_OPTION };

/**
 * Creates one workload of the type the operator is currently looking at.
 *
 * The form only shows and only sends the fields that type accepts. The Server
 * rejects a `replicas` on a DaemonSet rather than ignoring it, so a field that
 * does not apply must be absent from the request, not present and empty.
 */
export function WorkloadCreateDialog({
  clusterId,
  clusterName,
  namespace,
  resource,
  onClose,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  resource: KubernetesWorkloadResource;
  onClose: () => void;
}) {
  const create = useCreateWorkload();
  const kind = kindLabel(resource);
  const previewKey = useSubmissionKey(true);
  const applyKey = useSubmissionKey(true);
  const [previewed, setPreviewed] = useState<WorkloadCreateSpec | null>(null);

  const [name, setName] = useState("");
  const [containers, setContainers] = useState<ContainerDraft[]>([{ ...EMPTY_CONTAINER }]);
  const [initContainers, setInitContainers] = useState<ContainerDraft[]>([]);
  const [labels, setLabels] = useState<LabelDraft[]>([]);
  const [replicas, setReplicas] = useState("");
  const [serviceName, setServiceName] = useState("");
  const [parallelism, setParallelism] = useState("");
  const [completions, setCompletions] = useState("");
  const [backoffLimit, setBackoffLimit] = useState("");
  const [ttlSeconds, setTtlSeconds] = useState("");
  const [schedule, setSchedule] = useState("");
  const [timeZone, setTimeZone] = useState("");
  const [concurrencyPolicy, setConcurrencyPolicy] = useState(DEFAULT_OPTION);
  const [startingDeadline, setStartingDeadline] = useState("");
  const [successfulHistory, setSuccessfulHistory] = useState("");
  const [failedHistory, setFailedHistory] = useState("");
  const [suspend, setSuspend] = useState(false);

  const replicated = resource === "deployments" || resource === "statefulsets";
  const statefulSet = resource === "statefulsets";
  // A CronJob carries the Job template fields as well as its own schedule.
  const jobLike = resource === "jobs" || resource === "cronjobs";
  const cronJob = resource === "cronjobs";

  const numbers = [
    ...(replicated ? [replicas] : []),
    ...(jobLike ? [parallelism, completions, backoffLimit, ttlSeconds] : []),
    ...(cronJob ? [startingDeadline, successfulHistory, failedHistory] : []),
  ];

  const valid =
    validDNS1123Subdomain(name.trim()) &&
    validContainers(containers, initContainers) &&
    validLabels(labels) &&
    numbers.every(validOptionalCount) &&
    (!statefulSet ||
      (DNS_1035_LABEL.test(serviceName.trim()) && serviceName.trim().length <= 63)) &&
    (!cronJob ||
      (schedule.trim() !== "" &&
        utf8Length(schedule.trim()) <= MAX_CRON_SCHEDULE_BYTES &&
        utf8Length(timeZone.trim()) <= MAX_CRON_TIME_ZONE_BYTES));

  const buildSpec = (): WorkloadCreateSpec => {
    const spec: WorkloadCreateSpec = {
      name: name.trim(),
      containers: containers.map(toContainerTemplate),
    };
    if (initContainers.length > 0) {
      spec.init_containers = initContainers.map(toContainerTemplate);
    }
    const labelEntries = labels.filter((label) => label.key.trim() !== "");
    if (labelEntries.length > 0) {
      spec.labels = Object.fromEntries(
        labelEntries.map((label) => [label.key.trim(), label.value.trim()]),
      );
    }
    if (replicated) {
      assignCount(spec, "replicas", replicas);
    }
    if (statefulSet) {
      spec.service_name = serviceName.trim();
    }
    if (jobLike) {
      assignCount(spec, "parallelism", parallelism);
      assignCount(spec, "completions", completions);
      assignCount(spec, "backoff_limit", backoffLimit);
      assignCount(spec, "ttl_seconds_after_finished", ttlSeconds);
    }
    if (cronJob) {
      spec.schedule = schedule.trim();
      if (timeZone.trim() !== "") {
        spec.time_zone = timeZone.trim();
      }
      if (concurrencyPolicy !== DEFAULT_OPTION) {
        spec.concurrency_policy = concurrencyPolicy as "Allow" | "Forbid" | "Replace";
      }
      if (suspend) {
        spec.suspend = true;
      }
      assignCount(spec, "starting_deadline_seconds", startingDeadline);
      assignCount(spec, "successful_jobs_history_limit", successfulHistory);
      assignCount(spec, "failed_jobs_history_limit", failedHistory);
    }
    return spec;
  };

  const submit = (dryRun: boolean, spec: WorkloadCreateSpec) =>
    void create
      .mutateAsync({
        clusterId,
        namespace,
        resource,
        spec,
        dryRun,
        idempotencyKey: dryRun ? previewKey : applyKey,
      })
      .then(() => {
        if (dryRun) {
          setPreviewed(spec);
          return;
        }
        toast.success(`${kind} ${spec.name} 已创建`);
        onClose();
      })
      .catch(() => undefined);

  return (
    <>
      <Dialog open={previewed === null} onOpenChange={(open) => !open && onClose()}>
        <DialogContent aria-describedby={undefined} className="w-[min(720px,calc(100vw-2rem))]">
          <DialogHeader>
            <DialogTitle>创建 {kind}</DialogTitle>
            <DialogDescription>
              第一步只执行服务端 DryRun，不会在集群中持久化对象。
            </DialogDescription>
          </DialogHeader>

          <div className="grid gap-4">
            <FormSection title="基本信息">
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="名称" htmlFor="workload-name">
                  <Input
                    id="workload-name"
                    value={name}
                    autoComplete="off"
                    spellCheck={false}
                    placeholder="例如 model-gateway"
                    onChange={(event) => setName(event.target.value)}
                  />
                </Field>
                {replicated ? (
                  <Field
                    label="副本数"
                    htmlFor="workload-replica-count"
                    hint="留空使用 Kubernetes 默认值"
                  >
                    <Input
                      id="workload-replica-count"
                      value={replicas}
                      inputMode="numeric"
                      autoComplete="off"
                      onChange={(event) => setReplicas(event.target.value)}
                    />
                  </Field>
                ) : null}
                {statefulSet ? (
                  <Field
                    label="Service 名称"
                    htmlFor="workload-service"
                    hint="必须是同一命名空间中已存在的 Service"
                  >
                    <Input
                      id="workload-service"
                      value={serviceName}
                      autoComplete="off"
                      spellCheck={false}
                      onChange={(event) => setServiceName(event.target.value)}
                    />
                  </Field>
                ) : null}
              </div>
            </FormSection>

            <ContainerRows
              title="容器"
              hint="至少一个；容器名在主容器与初始化容器之间也不能重复"
              rows={containers}
              onChange={setContainers}
              minimum={1}
            />

            <ContainerRows
              title="初始化容器"
              hint="可选，按顺序在主容器之前运行"
              rows={initContainers}
              onChange={setInitContainers}
              minimum={0}
            />

            {jobLike ? (
              <FormSection title="执行参数" hint="留空的项交由 Kubernetes 默认处理">
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="并行度" htmlFor="workload-parallelism">
                    <Input
                      id="workload-parallelism"
                      value={parallelism}
                      inputMode="numeric"
                      onChange={(event) => setParallelism(event.target.value)}
                    />
                  </Field>
                  <Field label="完成数" htmlFor="workload-completions">
                    <Input
                      id="workload-completions"
                      value={completions}
                      inputMode="numeric"
                      onChange={(event) => setCompletions(event.target.value)}
                    />
                  </Field>
                  <Field label="失败重试上限" htmlFor="workload-backoff">
                    <Input
                      id="workload-backoff"
                      value={backoffLimit}
                      inputMode="numeric"
                      onChange={(event) => setBackoffLimit(event.target.value)}
                    />
                  </Field>
                  <Field label="完成后保留秒数" htmlFor="workload-ttl">
                    <Input
                      id="workload-ttl"
                      value={ttlSeconds}
                      inputMode="numeric"
                      onChange={(event) => setTtlSeconds(event.target.value)}
                    />
                  </Field>
                </div>
              </FormSection>
            ) : null}

            {cronJob ? (
              <FormSection title="调度">
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="Cron 表达式" htmlFor="workload-schedule">
                    <Input
                      id="workload-schedule"
                      value={schedule}
                      autoComplete="off"
                      spellCheck={false}
                      placeholder="例如 */5 * * * *"
                      onChange={(event) => setSchedule(event.target.value)}
                    />
                  </Field>
                  <Field label="时区" htmlFor="workload-timezone" hint="留空使用控制器本地时区">
                    <Input
                      id="workload-timezone"
                      value={timeZone}
                      autoComplete="off"
                      spellCheck={false}
                      placeholder="例如 Asia/Shanghai"
                      onChange={(event) => setTimeZone(event.target.value)}
                    />
                  </Field>
                  <Field label="并发策略" htmlFor="workload-concurrency">
                    <Select value={concurrencyPolicy} onValueChange={setConcurrencyPolicy}>
                      <SelectTrigger id="workload-concurrency">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value={DEFAULT_OPTION}>默认（Allow）</SelectItem>
                        <SelectItem value="Allow">Allow</SelectItem>
                        <SelectItem value="Forbid">Forbid</SelectItem>
                        <SelectItem value="Replace">Replace</SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>
                  <Field label="启动截止秒数" htmlFor="workload-deadline">
                    <Input
                      id="workload-deadline"
                      value={startingDeadline}
                      inputMode="numeric"
                      onChange={(event) => setStartingDeadline(event.target.value)}
                    />
                  </Field>
                  <Field label="成功历史保留" htmlFor="workload-successful-history">
                    <Input
                      id="workload-successful-history"
                      value={successfulHistory}
                      inputMode="numeric"
                      onChange={(event) => setSuccessfulHistory(event.target.value)}
                    />
                  </Field>
                  <Field label="失败历史保留" htmlFor="workload-failed-history">
                    <Input
                      id="workload-failed-history"
                      value={failedHistory}
                      inputMode="numeric"
                      onChange={(event) => setFailedHistory(event.target.value)}
                    />
                  </Field>
                </div>
                <label className="mt-3 flex items-center gap-2 text-[13px]">
                  <Checkbox
                    checked={suspend}
                    onCheckedChange={(checked) => setSuspend(checked === true)}
                  />
                  创建后立即暂停调度
                </label>
              </FormSection>
            ) : null}

            <LabelRows rows={labels} onChange={setLabels} />
          </div>

          <Alert tone="info" className="mt-4">
            目标：{clusterName} / {namespace}
          </Alert>
          {create.error ? (
            <Alert tone="danger" className="mt-3">
              {errorMessage(create.error)}
            </Alert>
          ) : null}

          <DialogFooter>
            <Button variant="ghost" onClick={onClose} disabled={create.isPending}>
              取消
            </Button>
            <Button
              variant="primary"
              disabled={!valid || create.isPending}
              onClick={() => submit(true, buildSpec())}
            >
              {create.isPending ? "预检中…" : "执行 DryRun 预检"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <SensitiveActionDialog
        open={previewed !== null}
        onOpenChange={(open) => !open && onClose()}
        title={`确认创建 ${kind}`}
        description="DryRun 已通过。确认后将向同一集群发送实际创建请求。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          { label: kind, name: previewed?.name ?? name.trim() },
        ]}
        impacts={createImpacts(resource, previewed)}
        confirmLabel="确认创建"
        pending={create.isPending}
        error={create.error}
        onConfirm={() => previewed && submit(false, previewed)}
      />
    </>
  );
}

function createImpacts(
  resource: KubernetesWorkloadResource,
  spec: WorkloadCreateSpec | null,
): string[] {
  if (!spec) {
    return [];
  }
  const impacts = [
    `将在目标集群持久化一个 ${kindLabel(resource)}，Pod 模板包含 ${spec.containers.length} 个主容器` +
      `${spec.init_containers?.length ? `和 ${spec.init_containers.length} 个初始化容器` : ""}。`,
  ];
  impacts.push(
    resource === "deployments" || resource === "statefulsets" || resource === "daemonsets"
      ? "Server 会写入不可覆盖的 zke.io/workload-id 选择器标签，避免控制器选中其他工作负载的 Pod。"
      : "Server 会写入不可覆盖的 zke.io/workload-id 标识标签；Job 的 Pod 选择器仍由 Kubernetes 按控制器 UID 生成。",
  );
  if (resource === "cronjobs") {
    impacts.push(
      spec.suspend
        ? `将按 ${spec.schedule} 注册调度，但创建后处于暂停状态，不会立即产生 Job。`
        : `调度器将按 ${spec.schedule} 表达式创建 Job。`,
    );
    return impacts;
  }
  if (resource === "jobs") {
    impacts.push("Job 创建后立即开始执行，Pod 会消耗目标命名空间的资源配额。");
    return impacts;
  }
  if (resource === "daemonsets") {
    impacts.push("控制器会在每个可调度节点上各创建一个 Pod。");
    return impacts;
  }
  if (resource === "statefulsets") {
    impacts.push(
      `StatefulSet 将引用同一命名空间中已有的 Service ${spec.service_name}；本次操作不会创建 Service。`,
    );
  }
  impacts.push(
    spec.replicas === undefined || spec.replicas === null
      ? "控制器将按 Kubernetes 默认副本数创建 Pod。"
      : `控制器将创建 ${spec.replicas} 个副本。`,
  );
  return impacts;
}

function toContainerTemplate(container: ContainerDraft) {
  return {
    name: container.name.trim(),
    image: container.image.trim(),
    ...(container.policy === DEFAULT_OPTION
      ? {}
      : { image_pull_policy: container.policy as "Always" | "IfNotPresent" | "Never" }),
  };
}

/** Writes an optional count onto the request only when the field was filled in. */
function assignCount<K extends keyof WorkloadCreateSpec>(
  spec: WorkloadCreateSpec,
  key: K,
  value: string,
): void {
  if (value.trim() === "") {
    return;
  }
  spec[key] = Number(value.trim()) as WorkloadCreateSpec[K];
}

/** Empty means "leave it to Kubernetes"; anything else must be a non-negative int32. */
function validOptionalCount(value: string): boolean {
  const trimmed = value.trim();
  return trimmed === "" || (/^\d+$/.test(trimmed) && Number(trimmed) <= INT32_MAX);
}

function validContainers(containers: ContainerDraft[], initContainers: ContainerDraft[]): boolean {
  if (
    containers.length === 0 ||
    containers.length > MAX_CONTAINERS ||
    initContainers.length > MAX_CONTAINERS
  ) {
    return false;
  }
  const names = new Set<string>();
  for (const container of [...containers, ...initContainers]) {
    const containerName = container.name.trim();
    const image = container.image.trim();
    if (
      !DNS_LABEL.test(containerName) ||
      containerName.length > 63 ||
      image === "" ||
      utf8Length(image) > MAX_IMAGE_BYTES ||
      /\s/.test(image) ||
      names.has(containerName)
    ) {
      return false;
    }
    names.add(containerName);
  }
  return true;
}

function validLabels(labels: LabelDraft[]): boolean {
  const keys = new Set<string>();
  for (const label of labels) {
    const key = label.key.trim();
    if (key === "") {
      // A completely untouched row is dropped. A value without a key is not:
      // silently dropping it would make the request differ from the form.
      if (label.value.trim() === "") {
        continue;
      }
      return false;
    }
    if (
      key === RESERVED_LABEL_KEY ||
      !validLabelKey(key) ||
      label.value.trim().length > 63 ||
      !LABEL_VALUE.test(label.value.trim()) ||
      keys.has(key)
    ) {
      return false;
    }
    keys.add(key);
  }
  return true;
}

function validDNS1123Subdomain(value: string): boolean {
  return (
    value.length > 0 &&
    value.length <= 253 &&
    value.split(".").every((part) => part.length <= 63 && DNS_LABEL.test(part))
  );
}

function validLabelKey(value: string): boolean {
  const parts = value.split("/");
  if (parts.length > 2) {
    return false;
  }
  const name = parts.at(-1) ?? "";
  if (name.length === 0 || name.length > 63 || !LABEL_NAME.test(name)) {
    return false;
  }
  return parts.length === 1 || validDNS1123Subdomain(parts[0] ?? "");
}

function utf8Length(value: string): number {
  return new TextEncoder().encode(value).length;
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
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    /*
     * `content-start` is load-bearing. A field is a grid item stretched to the
     * height of the tallest one in its row, and a grid's default `align-content`
     * spreads that extra height across its auto rows — so a field beside one
     * carrying a hint had its own label and input pushed apart by half the
     * hint's height, and no two inputs in a row started at the same place.
     */
    <div className="grid content-start gap-1.5">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
    </div>
  );
}

function ContainerRows({
  title,
  hint,
  rows,
  onChange,
  minimum,
}: {
  title: string;
  hint: string;
  rows: ContainerDraft[];
  onChange: (rows: ContainerDraft[]) => void;
  minimum: number;
}) {
  const update = (index: number, patch: Partial<ContainerDraft>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...patch } : row)));

  return (
    <FormSection title={title} hint={hint}>
      <div className="grid gap-2">
        {rows.map((row, index) => (
          <div key={index} className="grid grid-cols-[1fr_1.5fr_9rem_auto] items-center gap-2">
            <Input
              value={row.name}
              aria-label={`${title} ${index + 1} 名称`}
              autoComplete="off"
              spellCheck={false}
              placeholder="容器名"
              onChange={(event) => update(index, { name: event.target.value })}
            />
            <Input
              value={row.image}
              aria-label={`${title} ${index + 1} 镜像`}
              autoComplete="off"
              spellCheck={false}
              placeholder="镜像，例如 nginx:alpine"
              onChange={(event) => update(index, { image: event.target.value })}
            />
            <Select value={row.policy} onValueChange={(value) => update(index, { policy: value })}>
              <SelectTrigger aria-label={`${title} ${index + 1} 拉取策略`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={DEFAULT_OPTION}>默认策略</SelectItem>
                <SelectItem value="Always">Always</SelectItem>
                <SelectItem value="IfNotPresent">IfNotPresent</SelectItem>
                <SelectItem value="Never">Never</SelectItem>
              </SelectContent>
            </Select>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={`移除${title} ${index + 1}`}
              disabled={rows.length <= minimum}
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
            disabled={rows.length >= MAX_CONTAINERS}
            onClick={() => onChange([...rows, { ...EMPTY_CONTAINER }])}
          >
            <Plus />
            添加{title}
          </Button>
        </div>
      </div>
    </FormSection>
  );
}

function LabelRows({
  rows,
  onChange,
}: {
  rows: LabelDraft[];
  onChange: (rows: LabelDraft[]) => void;
}) {
  const update = (index: number, patch: Partial<LabelDraft>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...patch } : row)));

  return (
    <FormSection title="标签" hint={`可选；${RESERVED_LABEL_KEY} 由 Server 写入，不能自行设置`}>
      <div className="grid gap-2">
        {rows.map((row, index) => (
          <div key={index} className="grid grid-cols-[1fr_1fr_auto] items-center gap-2">
            <Input
              value={row.key}
              aria-label={`标签 ${index + 1} 键`}
              autoComplete="off"
              spellCheck={false}
              placeholder="键"
              onChange={(event) => update(index, { key: event.target.value })}
            />
            <Input
              value={row.value}
              aria-label={`标签 ${index + 1} 值`}
              autoComplete="off"
              spellCheck={false}
              placeholder="值"
              onChange={(event) => update(index, { value: event.target.value })}
            />
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={`移除标签 ${index + 1}`}
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
            onClick={() => onChange([...rows, { key: "", value: "" }])}
          >
            <Plus />
            添加标签
          </Button>
        </div>
      </div>
    </FormSection>
  );
}
