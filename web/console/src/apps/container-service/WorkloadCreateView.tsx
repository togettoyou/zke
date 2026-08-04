import { useId, useState, type ReactNode } from "react";
import { AlertTriangle, ArrowLeft, Plus, X } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import { useCreateWorkload, type WorkloadCreateSpec } from "@/api/queries/workloads";
import type { KubernetesWorkloadResource } from "@/api/types";
import { SectionTitle } from "@/apps/AppShell";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { Button } from "@/components/ui/button";
import { Input, NumericInput, Textarea } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, Checkbox, Switch } from "@/components/ui/misc";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/cn";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { kindLabel } from "./workload-catalog";
import {
  buildWorkloadSpec,
  containerProblems,
  DEFAULT_OPTION,
  draftProblem,
  emptyContainer,
  emptyDraft,
  emptyHook,
  emptyProbe,
  emptyToleration,
  emptyVolume,
  MAX_DESCRIPTION_LENGTH,
  nameLimit,
  RESERVED_ANNOTATION_KEY,
  RESERVED_LABEL_KEY,
  type ContainerDraft,
  type EnvDraft,
  type FormSectionKey,
  type HookDraft,
  type KeyValueDraft,
  type ProbeDraft,
  type TolerationDraft,
  type VolumeDraft,
  type VolumeMountDraft,
  type VolumeKind,
  type WorkloadFormDraft,
} from "./workload-form-model";

const VOLUME_KIND_LABELS: Record<VolumeKind, string> = {
  empty_dir: "临时路径（emptyDir）",
  host_path: "主机路径（hostPath）",
  config_map: "配置文件（ConfigMap）",
  secret: "密钥（Secret）",
  persistent_volume_claim: "PVC",
  nfs: "文件存储（NFS）",
};

/** The titles the sections are rendered with, to point at one from elsewhere. */
const SECTION_LABELS: Record<FormSectionKey, string> = {
  basic: "基本信息",
  labels: "标签",
  annotations: "注解",
  volumes: "数据卷",
  containers: "实例内容器",
  job: "执行参数",
  schedule: "调度",
  imagePullSecrets: "镜像访问凭证",
  nodeSelector: "节点调度策略",
  tolerations: "容忍调度",
};

const HOST_PATH_TYPES = [
  "DirectoryOrCreate",
  "Directory",
  "FileOrCreate",
  "File",
  "Socket",
  "CharDevice",
  "BlockDevice",
];

/**
 * Creates one workload of the type the operator is currently looking at.
 *
 * A page rather than a dialog: the Pod template is most of a Kubernetes
 * workload, and reading forty fields through a box laid over the list is worse
 * than leaving the list. The form only shows and only sends the fields the type
 * accepts — the Server rejects a `replicas` on a DaemonSet rather than ignoring
 * it, so a field that does not apply must be absent from the request, not
 * present and empty.
 */
export function WorkloadCreateView({
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
  const [draft, setDraft] = useState<WorkloadFormDraft>(emptyDraft);
  const [activeContainer, setActiveContainer] = useState(0);

  const patch = (changes: Partial<WorkloadFormDraft>) =>
    setDraft((current) => ({ ...current, ...changes }));
  const patchContainer = (index: number, changes: Partial<ContainerDraft>) =>
    setDraft((current) => ({
      ...current,
      containers: current.containers.map((container, position) =>
        position === index ? { ...container, ...changes } : container,
      ),
    }));

  const replicated = resource === "deployments" || resource === "statefulsets";
  const statefulSet = resource === "statefulsets";
  const jobLike = resource === "jobs" || resource === "cronjobs";
  const cronJob = resource === "cronjobs";

  const problem = draftProblem(draft, resource);
  // The problem is shown by the section that can fix it, and the sections are
  // checked in the order they are read, so it is always the topmost one.
  const problemIn = (section: FormSectionKey) =>
    problem?.section === section ? problem.message : undefined;
  // One container is open at a time, so the tab strip has to carry the ones that
  // are not: a problem the operator cannot see is a disabled button with no cause.
  const problems = containerProblems(draft.containers, draft.volumes);
  const container = draft.containers[activeContainer] ?? draft.containers[0];
  const containerIndex = Math.min(activeContainer, draft.containers.length - 1);

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
      <div className="grid gap-3">
        <SectionTitle
          title={`创建 ${kind} · ${namespace}`}
          actions={
            <Button size="sm" variant="secondary" onClick={onClose}>
              <ArrowLeft />
              返回列表
            </Button>
          }
        />

        <FormSection title="基本信息" problem={problemIn("basic")}>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field
              label="名称"
              hint={`最长 ${nameLimit(resource)} 个字符，小写字母、数字、- 与 .，以字母或数字开头`}
            >
              {(id) => (
                <Input
                  id={id}
                  value={draft.name}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="例如 model-gateway"
                  onChange={(event) => patch({ name: event.target.value })}
                />
              )}
            </Field>
            {replicated ? (
              <Field label="实例数量" hint="留空使用 Kubernetes 默认值">
                {(id) => (
                  <NumericInput
                    id={id}
                    value={draft.replicas}
                    onValueChange={(replicas) => patch({ replicas })}
                  />
                )}
              </Field>
            ) : null}
            {statefulSet ? (
              <Field label="Service 名称" hint="必须是同一命名空间中已存在的 Service">
                {(id) => (
                  <Input
                    id={id}
                    value={draft.serviceName}
                    autoComplete="off"
                    spellCheck={false}
                    onChange={(event) => patch({ serviceName: event.target.value })}
                  />
                )}
              </Field>
            ) : null}
          </div>
          <div className="mt-3">
            <Field
              label="描述"
              hint={`写入 ${RESERVED_ANNOTATION_KEY} 注解，最长 ${MAX_DESCRIPTION_LENGTH} 个字符`}
            >
              {(id) => (
                <Textarea
                  id={id}
                  value={draft.description}
                  spellCheck={false}
                  className="min-h-16"
                  onChange={(event) => patch({ description: event.target.value })}
                />
              )}
            </Field>
          </div>
        </FormSection>

        <FormSection
          title="标签"
          hint={`${RESERVED_LABEL_KEY} 由 Server 写入，不能在此设置`}
          problem={problemIn("labels")}
        >
          <KeyValueRows
            rows={draft.labels}
            onChange={(labels) => patch({ labels })}
            addLabel="新增标签"
          />
        </FormSection>

        <FormSection
          title="注解"
          hint={`${RESERVED_ANNOTATION_KEY} 由上面的描述字段写入`}
          problem={problemIn("annotations")}
        >
          <KeyValueRows
            rows={draft.annotations}
            onChange={(annotations) => patch({ annotations })}
            addLabel="新增注解"
          />
        </FormSection>

        <FormSection
          title="数据卷"
          hint="为容器提供存储；声明后还需要在容器的「数据卷挂载」中挂载到具体路径"
          problem={problemIn("volumes")}
        >
          <VolumeRows rows={draft.volumes} onChange={(volumes) => patch({ volumes })} />
        </FormSection>

        <FormSection
          title="实例内容器"
          hint="至少一个非初始化容器；容器名在所有容器之间不能重复"
          problem={problemIn("containers")}
        >
          <div className="mb-3 flex flex-wrap items-center gap-2">
            {draft.containers.map((item, index) => (
              <div
                key={item.id}
                className={cn(
                  "rounded-control flex items-center gap-1 border px-2 py-1 text-[13px]",
                  problems.has(item.id)
                    ? "border-danger text-danger bg-surface"
                    : index === containerIndex
                      ? "border-primary bg-surface text-foreground"
                      : "border-border text-muted-foreground",
                )}
              >
                <button
                  type="button"
                  className="zke-focus rounded-sm"
                  title={problems.get(item.id)}
                  onClick={() => setActiveContainer(index)}
                >
                  {item.name.trim() === "" ? `container-${index + 1}` : item.name}
                  {item.init ? "（init）" : ""}
                </button>
                {problems.has(item.id) ? (
                  <AlertTriangle className="text-danger size-3 shrink-0" aria-hidden />
                ) : null}
                {draft.containers.length > 1 ? (
                  <button
                    type="button"
                    aria-label={`移除容器 ${item.name}`}
                    className="zke-focus text-subtle-foreground hover:text-danger rounded-sm"
                    onClick={() => {
                      patch({
                        containers: draft.containers.filter((_, position) => position !== index),
                      });
                      setActiveContainer(0);
                    }}
                  >
                    <X className="size-3" />
                  </button>
                ) : null}
              </div>
            ))}
            <Button
              size="sm"
              variant="secondary"
              onClick={() => {
                patch({
                  containers: [...draft.containers, emptyContainer(draft.containers.length + 1)],
                });
                setActiveContainer(draft.containers.length);
              }}
            >
              <Plus />
              添加容器
            </Button>
          </div>

          {container ? (
            <ContainerPanel
              container={container}
              volumes={draft.volumes}
              onChange={(changes) => patchContainer(containerIndex, changes)}
            />
          ) : null}
        </FormSection>

        {jobLike ? (
          <FormSection
            title="执行参数"
            hint="留空的项交由 Kubernetes 默认处理"
            problem={problemIn("job")}
          >
            <div className="grid gap-3 sm:grid-cols-2">
              <CountField
                label="并行度"
                value={draft.parallelism}
                onChange={(parallelism) => patch({ parallelism })}
              />
              <CountField
                label="完成数"
                value={draft.completions}
                onChange={(completions) => patch({ completions })}
              />
              <CountField
                label="失败重试上限"
                value={draft.backoffLimit}
                onChange={(backoffLimit) => patch({ backoffLimit })}
              />
              <CountField
                label="完成后保留秒数"
                value={draft.ttlSeconds}
                onChange={(ttlSeconds) => patch({ ttlSeconds })}
              />
            </div>
          </FormSection>
        ) : null}

        {cronJob ? (
          <FormSection title="调度" problem={problemIn("schedule")}>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Cron 表达式">
                {(id) => (
                  <Input
                    id={id}
                    value={draft.schedule}
                    autoComplete="off"
                    spellCheck={false}
                    placeholder="例如 */5 * * * *"
                    onChange={(event) => patch({ schedule: event.target.value })}
                  />
                )}
              </Field>
              <Field label="时区" hint="留空使用控制器本地时区">
                {(id) => (
                  <Input
                    id={id}
                    value={draft.timeZone}
                    autoComplete="off"
                    spellCheck={false}
                    placeholder="例如 Asia/Shanghai"
                    onChange={(event) => patch({ timeZone: event.target.value })}
                  />
                )}
              </Field>
              <Field label="并发策略">
                {(id) => (
                  <Select
                    value={draft.concurrencyPolicy}
                    onValueChange={(concurrencyPolicy) => patch({ concurrencyPolicy })}
                  >
                    <SelectTrigger id={id}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={DEFAULT_OPTION}>默认（Allow）</SelectItem>
                      <SelectItem value="Allow">Allow</SelectItem>
                      <SelectItem value="Forbid">Forbid</SelectItem>
                      <SelectItem value="Replace">Replace</SelectItem>
                    </SelectContent>
                  </Select>
                )}
              </Field>
              <CountField
                label="启动截止秒数"
                value={draft.startingDeadline}
                onChange={(startingDeadline) => patch({ startingDeadline })}
              />
              <CountField
                label="成功历史保留"
                value={draft.successfulHistory}
                onChange={(successfulHistory) => patch({ successfulHistory })}
              />
              <CountField
                label="失败历史保留"
                value={draft.failedHistory}
                onChange={(failedHistory) => patch({ failedHistory })}
              />
            </div>
            <label className="mt-3 flex items-center gap-2 text-[13px]">
              <Checkbox
                checked={draft.suspend}
                onCheckedChange={(checked) => patch({ suspend: checked === true })}
              />
              创建后立即暂停调度
            </label>
          </FormSection>
        ) : null}

        <FormSection
          title="镜像访问凭证"
          hint="只传递 Secret 名称引用，不读取 Secret 正文"
          problem={problemIn("imagePullSecrets")}
        >
          <StringRows
            rows={draft.imagePullSecrets}
            onChange={(imagePullSecrets) => patch({ imagePullSecrets })}
            addLabel="添加镜像访问凭证"
            placeholder="例如 registry-key"
          />
        </FormSection>

        <FormSection
          title="节点调度策略"
          hint="按节点标签精确匹配；亲和性等更复杂的规则请在创建后通过 YAML 配置"
          problem={problemIn("nodeSelector")}
        >
          <KeyValueRows
            rows={draft.nodeSelector}
            onChange={(nodeSelector) => patch({ nodeSelector })}
            addLabel="新增节点标签"
            keyPlaceholder="例如 kubernetes.io/os"
            valuePlaceholder="例如 linux"
          />
        </FormSection>

        <FormSection
          title="容忍调度"
          hint="容忍节点污点，使 Pod 可以调度到被污染的节点"
          problem={problemIn("tolerations")}
        >
          <TolerationRows
            rows={draft.tolerations}
            onChange={(tolerations) => patch({ tolerations })}
          />
        </FormSection>

        {/*
         * The message itself is up in the section that can fix it; down here,
         * next to the button it disables, what is missing is where to look.
         */}
        {problem ? (
          <Alert tone="warning">「{SECTION_LABELS[problem.section]}」中还有需要修正的项。</Alert>
        ) : null}
        {create.error ? <Alert tone="danger">{errorMessage(create.error)}</Alert> : null}

        <div className="flex items-center justify-end gap-3 pb-2">
          <span className="text-subtle-foreground text-xs">
            目标：{clusterName} / {namespace}
          </span>
          <Button
            variant="primary"
            size="sm"
            disabled={problem !== null || create.isPending}
            onClick={() => submit(true, buildWorkloadSpec(draft, resource))}
          >
            {create.isPending ? "预检中…" : "执行 DryRun 预检"}
          </Button>
        </div>
      </div>

      <SensitiveActionDialog
        open={previewed !== null}
        onOpenChange={(open) => !open && setPreviewed(null)}
        title={`确认创建 ${kind}`}
        description="DryRun 已通过。确认后将向同一集群发送实际创建请求。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          { label: "命名空间", name: namespace },
          { label: kind, name: previewed?.name ?? draft.name.trim() },
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

function ContainerPanel({
  container,
  volumes,
  onChange,
}: {
  container: ContainerDraft;
  volumes: VolumeDraft[];
  onChange: (changes: Partial<ContainerDraft>) => void;
}) {
  const [advanced, setAdvanced] = useState(false);

  return (
    <div className="border-border bg-surface-muted/40 rounded-panel grid gap-3 border p-3">
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="名称">
          {(id) => (
            <Input
              id={id}
              value={container.name}
              autoComplete="off"
              spellCheck={false}
              onChange={(event) => onChange({ name: event.target.value })}
            />
          )}
        </Field>
        <Field label="镜像拉取策略" hint="留空时由 Kubernetes 按镜像版本决定">
          {(id) => (
            <Select value={container.policy} onValueChange={(policy) => onChange({ policy })}>
              <SelectTrigger id={id}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={DEFAULT_OPTION}>默认</SelectItem>
                <SelectItem value="Always">Always</SelectItem>
                <SelectItem value="IfNotPresent">IfNotPresent</SelectItem>
                <SelectItem value="Never">Never</SelectItem>
              </SelectContent>
            </Select>
          )}
        </Field>
        <Field label="镜像" hint="可直接粘贴完整镜像地址">
          {(id) => (
            <Input
              id={id}
              value={container.image}
              autoComplete="off"
              spellCheck={false}
              placeholder="例如 nginx 或 registry.example.com/team/app"
              onChange={(event) => onChange({ image: event.target.value })}
            />
          )}
        </Field>
        <Field label="镜像版本（Tag）" hint="不填由镜像地址自身决定，通常为 latest">
          {(id) => (
            <Input
              id={id}
              value={container.tag}
              autoComplete="off"
              spellCheck={false}
              placeholder="例如 1.25"
              onChange={(event) => onChange({ tag: event.target.value })}
            />
          )}
        </Field>
      </div>

      <Field label="环境变量" hint="值可以是字面量，也可以引用 ConfigMap 或 Secret 中的某个键">
        {() => <EnvRows rows={container.env} onChange={(env) => onChange({ env })} />}
      </Field>

      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="CPU 限制" hint="单位为核，request 用于预分配，limit 为使用上限">
          {() => (
            <div className="flex items-center gap-2">
              <RangeInput
                prefix="request"
                value={container.cpuRequest}
                onChange={(cpuRequest) => onChange({ cpuRequest })}
              />
              <span className="text-subtle-foreground text-xs">-</span>
              <RangeInput
                prefix="limit"
                value={container.cpuLimit}
                onChange={(cpuLimit) => onChange({ cpuLimit })}
              />
              <span className="text-muted-foreground text-xs">核</span>
            </div>
          )}
        </Field>
        <Field label="内存限制" hint="单位为 MiB">
          {() => (
            <div className="flex items-center gap-2">
              <RangeInput
                prefix="request"
                value={container.memoryRequest}
                onChange={(memoryRequest) => onChange({ memoryRequest })}
              />
              <span className="text-subtle-foreground text-xs">-</span>
              <RangeInput
                prefix="limit"
                value={container.memoryLimit}
                onChange={(memoryLimit) => onChange({ memoryLimit })}
              />
              <span className="text-muted-foreground text-xs">MiB</span>
            </div>
          )}
        </Field>
        <Field label="GPU 资源" hint="写入 nvidia.com/gpu 限额；需要集群已安装对应的设备插件">
          {(id) => (
            <div className="flex items-center gap-2">
              <NumericInput
                id={id}
                value={container.gpuLimit}
                className="w-28"
                placeholder="0"
                onValueChange={(gpuLimit) => onChange({ gpuLimit })}
              />
              <span className="text-muted-foreground text-xs">卡</span>
            </div>
          )}
        </Field>
      </div>

      <div>
        <Button size="sm" variant="ghost" onClick={() => setAdvanced(!advanced)}>
          {advanced ? "隐藏高级设置" : "显示高级设置"}
        </Button>
      </div>

      {advanced ? (
        <div className="grid gap-3">
          <Field label="工作目录" hint="指定容器运行后的工作目录">
            {(id) => (
              <Input
                id={id}
                value={container.workingDir}
                autoComplete="off"
                spellCheck={false}
                placeholder="例如 /srv"
                onChange={(event) => onChange({ workingDir: event.target.value })}
              />
            )}
          </Field>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="运行命令" hint="覆盖镜像 ENTRYPOINT，每行一条">
              {(id) => (
                <Textarea
                  id={id}
                  value={container.command.join("\n")}
                  spellCheck={false}
                  className="zke-mono min-h-20 text-xs"
                  onChange={(event) => onChange({ command: event.target.value.split("\n") })}
                />
              )}
            </Field>
            <Field label="运行参数" hint="覆盖镜像 CMD，每行一条">
              {(id) => (
                <Textarea
                  id={id}
                  value={container.args.join("\n")}
                  spellCheck={false}
                  className="zke-mono min-h-20 text-xs"
                  onChange={(event) => onChange({ args: event.target.value.split("\n") })}
                />
              )}
            </Field>
          </div>

          <Field label="数据卷挂载" hint="只能挂载上方「数据卷」中已声明的卷">
            {() => (
              <MountRows
                rows={container.mounts}
                volumes={volumes}
                onChange={(mounts) => onChange({ mounts })}
              />
            )}
          </Field>

          {/*
           * An init container runs to completion before the Pod starts: there is
           * nothing to probe and no lifecycle to hook into, and Kubernetes
           * rejects both rather than ignoring them.
           */}
          {container.init ? (
            <p className="text-subtle-foreground text-xs">
              初始化容器在主容器之前运行完毕，因此没有健康检查和生命周期钩子。
            </p>
          ) : (
            <>
              <Field label="存活检查" hint="检查容器是否正常，不正常则重启实例">
                {() => (
                  <ProbeEditor
                    probe={container.liveness}
                    onChange={(liveness) => onChange({ liveness })}
                  />
                )}
              </Field>
              <Field label="就绪检查" hint="检查容器是否就绪，不就绪则停止转发流量到当前实例">
                {() => (
                  <ProbeEditor
                    probe={container.readiness}
                    onChange={(readiness) => onChange({ readiness })}
                  />
                )}
              </Field>

              <Field label="启动后执行" hint="容器启动后立即执行，与容器进程并行">
                {() => (
                  <HookEditor
                    hook={container.postStart}
                    onChange={(postStart) => onChange({ postStart })}
                  />
                )}
              </Field>
              <Field label="结束前执行" hint="容器被终止前执行，执行完成后才发送终止信号">
                {() => (
                  <HookEditor
                    hook={container.preStop}
                    onChange={(preStop) => onChange({ preStop })}
                  />
                )}
              </Field>
            </>
          )}

          <div className="grid gap-3 sm:grid-cols-2">
            <ToggleField
              label="初始化容器"
              hint="标识为 init container，在主容器之前按顺序运行"
              checked={container.init}
              onChange={(init) =>
                onChange(
                  // Turning it on drops what an init container cannot carry,
                  // rather than leaving it set in a section that just vanished.
                  init
                    ? {
                        init,
                        liveness: emptyProbe(),
                        readiness: emptyProbe(),
                        postStart: emptyHook(),
                        preStop: emptyHook(),
                      }
                    : { init },
                )
              }
            />
            <ToggleField
              label="特权级容器"
              hint="容器将拥有宿主机的 root 权限"
              checked={container.privileged}
              onChange={(privileged) => onChange({ privileged })}
            />
          </div>
        </div>
      ) : null}
    </div>
  );
}

function ProbeEditor({
  probe,
  onChange,
}: {
  probe: ProbeDraft;
  onChange: (probe: ProbeDraft) => void;
}) {
  return (
    <div className="grid gap-2">
      <label className="flex items-center gap-2 text-[13px]">
        <Checkbox
          checked={probe.enabled}
          onCheckedChange={(checked) => onChange({ ...probe, enabled: checked === true })}
        />
        启用
      </label>
      {probe.enabled ? (
        <div className="grid gap-2 sm:grid-cols-3">
          <Select
            value={probe.kind}
            onValueChange={(kind) => onChange({ ...probe, kind: kind as ProbeDraft["kind"] })}
          >
            <SelectTrigger aria-label="检查方式">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="http_get">HTTP 请求</SelectItem>
              <SelectItem value="tcp_socket">TCP 端口</SelectItem>
              <SelectItem value="exec">执行命令</SelectItem>
            </SelectContent>
          </Select>
          {probe.kind === "exec" ? (
            <Textarea
              value={probe.command}
              aria-label="检查命令"
              spellCheck={false}
              placeholder="每行一条，例如 cat /tmp/healthy"
              className="zke-mono min-h-9 text-xs sm:col-span-2"
              onChange={(event) => onChange({ ...probe, command: event.target.value })}
            />
          ) : (
            <>
              <Input
                value={probe.port}
                aria-label="检查端口"
                autoComplete="off"
                placeholder="端口号或端口名"
                onChange={(event) => onChange({ ...probe, port: event.target.value })}
              />
              {probe.kind === "http_get" ? (
                <>
                  <Input
                    value={probe.path}
                    aria-label="检查路径"
                    autoComplete="off"
                    spellCheck={false}
                    placeholder="/healthz"
                    onChange={(event) => onChange({ ...probe, path: event.target.value })}
                  />
                  <SchemeSelect
                    label="检查协议"
                    value={probe.scheme}
                    onChange={(scheme) => onChange({ ...probe, scheme })}
                  />
                </>
              ) : null}
            </>
          )}
          <NumberInput
            label="初始延迟（秒）"
            value={probe.initialDelaySeconds}
            onChange={(initialDelaySeconds) => onChange({ ...probe, initialDelaySeconds })}
          />
          <NumberInput
            label="检查间隔（秒）"
            value={probe.periodSeconds}
            onChange={(periodSeconds) => onChange({ ...probe, periodSeconds })}
          />
          <NumberInput
            label="超时（秒）"
            value={probe.timeoutSeconds}
            onChange={(timeoutSeconds) => onChange({ ...probe, timeoutSeconds })}
          />
          <NumberInput
            label="健康阈值"
            value={probe.successThreshold}
            onChange={(successThreshold) => onChange({ ...probe, successThreshold })}
          />
          <NumberInput
            label="不健康阈值"
            value={probe.failureThreshold}
            onChange={(failureThreshold) => onChange({ ...probe, failureThreshold })}
          />
        </div>
      ) : null}
    </div>
  );
}

function HookEditor({ hook, onChange }: { hook: HookDraft; onChange: (hook: HookDraft) => void }) {
  return (
    <div className="grid gap-2">
      <label className="flex items-center gap-2 text-[13px]">
        <Checkbox
          checked={hook.enabled}
          onCheckedChange={(checked) => onChange({ ...hook, enabled: checked === true })}
        />
        启用
      </label>
      {hook.enabled ? (
        <div className="grid gap-2 sm:grid-cols-3">
          <Select
            value={hook.kind}
            onValueChange={(kind) => onChange({ ...hook, kind: kind as HookDraft["kind"] })}
          >
            <SelectTrigger aria-label="执行方式">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="exec">执行命令</SelectItem>
              <SelectItem value="http_get">HTTP 请求</SelectItem>
            </SelectContent>
          </Select>
          {hook.kind === "exec" ? (
            <Textarea
              value={hook.command}
              aria-label="执行命令"
              spellCheck={false}
              placeholder="每行一条"
              className="zke-mono min-h-9 text-xs sm:col-span-2"
              onChange={(event) => onChange({ ...hook, command: event.target.value })}
            />
          ) : (
            <>
              <Input
                value={hook.port}
                aria-label="端口"
                autoComplete="off"
                placeholder="端口号或端口名"
                onChange={(event) => onChange({ ...hook, port: event.target.value })}
              />
              <Input
                value={hook.path}
                aria-label="路径"
                autoComplete="off"
                spellCheck={false}
                placeholder="/"
                onChange={(event) => onChange({ ...hook, path: event.target.value })}
              />
              <SchemeSelect
                label="协议"
                value={hook.scheme}
                onChange={(scheme) => onChange({ ...hook, scheme })}
              />
            </>
          )}
        </div>
      ) : null}
    </div>
  );
}

/** The scheme an HTTP probe or hook connects with; unset means Kubernetes' own. */
function SchemeSelect({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger aria-label={label}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={DEFAULT_OPTION}>默认（HTTP）</SelectItem>
        <SelectItem value="HTTP">HTTP</SelectItem>
        <SelectItem value="HTTPS">HTTPS</SelectItem>
      </SelectContent>
    </Select>
  );
}

function EnvRows({ rows, onChange }: { rows: EnvDraft[]; onChange: (rows: EnvDraft[]) => void }) {
  const update = (index: number, changes: Partial<(typeof rows)[number]>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...changes } : row)));

  return (
    <div className="grid gap-2">
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[1fr_auto] items-start gap-2">
          <div className="grid gap-2 sm:grid-cols-4">
            <Input
              value={row.name}
              aria-label={`第 ${index + 1} 个变量名`}
              autoComplete="off"
              spellCheck={false}
              placeholder="变量名"
              onChange={(event) => update(index, { name: event.target.value })}
            />
            <Select
              value={row.source}
              onValueChange={(source) =>
                update(index, { source: source as (typeof rows)[number]["source"] })
              }
            >
              <SelectTrigger aria-label={`第 ${index + 1} 个变量来源`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="value">字面值</SelectItem>
                <SelectItem value="config_map">ConfigMap</SelectItem>
                <SelectItem value="secret">Secret</SelectItem>
              </SelectContent>
            </Select>
            {row.source === "value" ? (
              <Input
                value={row.value}
                aria-label={`第 ${index + 1} 个变量值`}
                autoComplete="off"
                spellCheck={false}
                placeholder="值"
                className="sm:col-span-2"
                onChange={(event) => update(index, { value: event.target.value })}
              />
            ) : (
              <>
                <Input
                  value={row.refName}
                  aria-label={`第 ${index + 1} 个引用对象`}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="对象名称"
                  onChange={(event) => update(index, { refName: event.target.value })}
                />
                <Input
                  value={row.refKey}
                  aria-label={`第 ${index + 1} 个引用键`}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="键"
                  onChange={(event) => update(index, { refKey: event.target.value })}
                />
              </>
            )}
          </div>
          <RemoveButton
            label={`移除第 ${index + 1} 个变量`}
            onClick={() => onChange(rows.filter((_, position) => position !== index))}
          />
        </div>
      ))}
      <AddButton
        label="新增变量"
        onClick={() =>
          onChange([...rows, { name: "", source: "value", value: "", refName: "", refKey: "" }])
        }
      />
    </div>
  );
}

function MountRows({
  rows,
  volumes,
  onChange,
}: {
  rows: VolumeMountDraft[];
  volumes: VolumeDraft[];
  onChange: (rows: VolumeMountDraft[]) => void;
}) {
  const update = (index: number, changes: Partial<(typeof rows)[number]>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...changes } : row)));

  if (volumes.length === 0) {
    return <p className="text-subtle-foreground text-xs">请先在上方「数据卷」中声明数据卷。</p>;
  }

  return (
    <div className="grid gap-2">
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[1fr_auto] items-start gap-2">
          <div className="grid gap-2 sm:grid-cols-4">
            <Select value={row.name} onValueChange={(name) => update(index, { name })}>
              <SelectTrigger aria-label={`第 ${index + 1} 个挂载的数据卷`}>
                <SelectValue placeholder="选择数据卷" />
              </SelectTrigger>
              <SelectContent>
                {volumes
                  .filter((volume) => volume.name.trim() !== "")
                  .map((volume) => (
                    <SelectItem key={volume.id} value={volume.name.trim()}>
                      {volume.name}
                    </SelectItem>
                  ))}
              </SelectContent>
            </Select>
            <Input
              value={row.mountPath}
              aria-label={`第 ${index + 1} 个挂载路径`}
              autoComplete="off"
              spellCheck={false}
              placeholder="挂载路径，例如 /etc/app"
              onChange={(event) => update(index, { mountPath: event.target.value })}
            />
            <Input
              value={row.subPath}
              aria-label={`第 ${index + 1} 个子路径`}
              autoComplete="off"
              spellCheck={false}
              placeholder="子路径（可选）"
              onChange={(event) => update(index, { subPath: event.target.value })}
            />
            <label className="flex items-center gap-2 text-[13px]">
              <Checkbox
                checked={row.readOnly}
                onCheckedChange={(checked) => update(index, { readOnly: checked === true })}
              />
              只读
            </label>
          </div>
          <RemoveButton
            label={`移除第 ${index + 1} 个挂载`}
            onClick={() => onChange(rows.filter((_, position) => position !== index))}
          />
        </div>
      ))}
      <AddButton
        label="添加挂载"
        onClick={() =>
          onChange([...rows, { name: "", mountPath: "", subPath: "", readOnly: false }])
        }
      />
    </div>
  );
}

function VolumeRows({
  rows,
  onChange,
}: {
  rows: VolumeDraft[];
  onChange: (rows: VolumeDraft[]) => void;
}) {
  const update = (index: number, changes: Partial<VolumeDraft>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...changes } : row)));

  return (
    <div className="grid gap-2">
      {rows.map((row, index) => (
        <div key={row.id} className="grid grid-cols-[1fr_auto] items-start gap-2">
          <div className="grid gap-2 sm:grid-cols-4">
            <Input
              value={row.name}
              aria-label={`第 ${index + 1} 个数据卷名称`}
              autoComplete="off"
              spellCheck={false}
              placeholder="数据卷名称"
              onChange={(event) => update(index, { name: event.target.value })}
            />
            <Select
              value={row.kind}
              onValueChange={(kind) => update(index, { kind: kind as VolumeKind })}
            >
              <SelectTrigger aria-label={`第 ${index + 1} 个数据卷类型`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(Object.keys(VOLUME_KIND_LABELS) as VolumeKind[]).map((kind) => (
                  <SelectItem key={kind} value={kind}>
                    {VOLUME_KIND_LABELS[kind]}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <VolumeSourceFields row={row} index={index} update={update} />
          </div>
          <RemoveButton
            label={`移除第 ${index + 1} 个数据卷`}
            onClick={() => onChange(rows.filter((_, position) => position !== index))}
          />
        </div>
      ))}
      <AddButton label="添加数据卷" onClick={() => onChange([...rows, emptyVolume()])} />
    </div>
  );
}

function VolumeSourceFields({
  row,
  index,
  update,
}: {
  row: VolumeDraft;
  index: number;
  update: (index: number, changes: Partial<VolumeDraft>) => void;
}) {
  switch (row.kind) {
    case "empty_dir":
      return (
        <>
          <Select value={row.medium} onValueChange={(medium) => update(index, { medium })}>
            <SelectTrigger aria-label={`第 ${index + 1} 个数据卷介质`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={DEFAULT_OPTION}>节点磁盘</SelectItem>
              <SelectItem value="Memory">内存（tmpfs）</SelectItem>
            </SelectContent>
          </Select>
          <NumericInput
            value={row.sizeLimit}
            decimal
            aria-label={`第 ${index + 1} 个数据卷容量上限`}
            placeholder="容量上限 MiB（可选）"
            onValueChange={(sizeLimit) => update(index, { sizeLimit })}
          />
        </>
      );
    case "host_path":
      return (
        <>
          <Input
            value={row.hostPath}
            aria-label={`第 ${index + 1} 个主机路径`}
            autoComplete="off"
            spellCheck={false}
            placeholder="/var/log"
            onChange={(event) => update(index, { hostPath: event.target.value })}
          />
          <Select
            value={row.hostPathType}
            onValueChange={(hostPathType) => update(index, { hostPathType })}
          >
            <SelectTrigger aria-label={`第 ${index + 1} 个主机路径类型`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={DEFAULT_OPTION}>不校验</SelectItem>
              {HOST_PATH_TYPES.map((type) => (
                <SelectItem key={type} value={type}>
                  {type}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </>
      );
    case "nfs":
      return (
        <>
          <Input
            value={row.nfsServer}
            aria-label={`第 ${index + 1} 个 NFS 服务器`}
            autoComplete="off"
            spellCheck={false}
            placeholder="服务器地址"
            onChange={(event) => update(index, { nfsServer: event.target.value })}
          />
          <Input
            value={row.nfsPath}
            aria-label={`第 ${index + 1} 个 NFS 路径`}
            autoComplete="off"
            spellCheck={false}
            placeholder="/exports/data"
            onChange={(event) => update(index, { nfsPath: event.target.value })}
          />
        </>
      );
    default:
      return (
        <>
          <Input
            value={row.refName}
            aria-label={`第 ${index + 1} 个数据卷引用的对象`}
            autoComplete="off"
            spellCheck={false}
            placeholder={
              row.kind === "persistent_volume_claim"
                ? "PVC 名称"
                : row.kind === "secret"
                  ? "Secret 名称"
                  : "ConfigMap 名称"
            }
            onChange={(event) => update(index, { refName: event.target.value })}
          />
          <label className="flex items-center gap-2 text-[13px]">
            {row.kind === "persistent_volume_claim" ? (
              <>
                <Checkbox
                  checked={row.readOnly}
                  onCheckedChange={(checked) => update(index, { readOnly: checked === true })}
                />
                只读
              </>
            ) : (
              <>
                <Checkbox
                  checked={row.optional}
                  onCheckedChange={(checked) => update(index, { optional: checked === true })}
                />
                对象不存在时仍启动
              </>
            )}
          </label>
        </>
      );
  }
}

function TolerationRows({
  rows,
  onChange,
}: {
  rows: TolerationDraft[];
  onChange: (rows: TolerationDraft[]) => void;
}) {
  const update = (index: number, changes: Partial<TolerationDraft>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...changes } : row)));

  return (
    <div className="grid gap-2">
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[1fr_auto] items-start gap-2">
          <div className="grid gap-2 sm:grid-cols-5">
            <Input
              value={row.key}
              aria-label={`第 ${index + 1} 个容忍键`}
              autoComplete="off"
              spellCheck={false}
              placeholder="污点键"
              onChange={(event) => update(index, { key: event.target.value })}
            />
            <Select value={row.operator} onValueChange={(operator) => update(index, { operator })}>
              <SelectTrigger aria-label={`第 ${index + 1} 个容忍运算符`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="Equal">Equal</SelectItem>
                <SelectItem value="Exists">Exists</SelectItem>
              </SelectContent>
            </Select>
            <Input
              value={row.value}
              aria-label={`第 ${index + 1} 个容忍值`}
              autoComplete="off"
              spellCheck={false}
              disabled={row.operator === "Exists"}
              placeholder={row.operator === "Exists" ? "Exists 匹配任意值" : "污点值"}
              onChange={(event) => update(index, { value: event.target.value })}
            />
            <Select value={row.effect} onValueChange={(effect) => update(index, { effect })}>
              <SelectTrigger aria-label={`第 ${index + 1} 个容忍效果`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={DEFAULT_OPTION}>全部效果</SelectItem>
                <SelectItem value="NoSchedule">NoSchedule</SelectItem>
                <SelectItem value="PreferNoSchedule">PreferNoSchedule</SelectItem>
                <SelectItem value="NoExecute">NoExecute</SelectItem>
              </SelectContent>
            </Select>
            <NumericInput
              value={row.tolerationSeconds}
              aria-label={`第 ${index + 1} 个容忍时长`}
              disabled={row.effect !== "NoExecute"}
              placeholder={row.effect === "NoExecute" ? "容忍秒数" : "仅 NoExecute"}
              onValueChange={(tolerationSeconds) => update(index, { tolerationSeconds })}
            />
          </div>
          <RemoveButton
            label={`移除第 ${index + 1} 条容忍`}
            onClick={() => onChange(rows.filter((_, position) => position !== index))}
          />
        </div>
      ))}
      <AddButton label="添加容忍" onClick={() => onChange([...rows, emptyToleration()])} />
    </div>
  );
}

function KeyValueRows({
  rows,
  onChange,
  addLabel,
  keyPlaceholder = "键",
  valuePlaceholder = "值",
}: {
  rows: KeyValueDraft[];
  onChange: (rows: KeyValueDraft[]) => void;
  addLabel: string;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
}) {
  const update = (index: number, changes: Partial<KeyValueDraft>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...changes } : row)));

  return (
    <div className="grid gap-2">
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[1fr_auto] items-start gap-2">
          <div className="grid gap-2 sm:grid-cols-2">
            <Input
              value={row.key}
              aria-label={`第 ${index + 1} 个键`}
              autoComplete="off"
              spellCheck={false}
              placeholder={keyPlaceholder}
              onChange={(event) => update(index, { key: event.target.value })}
            />
            <Input
              value={row.value}
              aria-label={`第 ${index + 1} 个值`}
              autoComplete="off"
              spellCheck={false}
              placeholder={valuePlaceholder}
              onChange={(event) => update(index, { value: event.target.value })}
            />
          </div>
          <RemoveButton
            label={`移除第 ${index + 1} 项`}
            onClick={() => onChange(rows.filter((_, position) => position !== index))}
          />
        </div>
      ))}
      <AddButton label={addLabel} onClick={() => onChange([...rows, { key: "", value: "" }])} />
    </div>
  );
}

function StringRows({
  rows,
  onChange,
  addLabel,
  placeholder,
}: {
  rows: string[];
  onChange: (rows: string[]) => void;
  addLabel: string;
  placeholder: string;
}) {
  return (
    <div className="grid gap-2">
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[1fr_auto] items-start gap-2">
          <Input
            value={row}
            aria-label={`第 ${index + 1} 项`}
            autoComplete="off"
            spellCheck={false}
            placeholder={placeholder}
            onChange={(event) =>
              onChange(
                rows.map((item, position) => (position === index ? event.target.value : item)),
              )
            }
          />
          <RemoveButton
            label={`移除第 ${index + 1} 项`}
            onClick={() => onChange(rows.filter((_, position) => position !== index))}
          />
        </div>
      ))}
      <AddButton label={addLabel} onClick={() => onChange([...rows, ""])} />
    </div>
  );
}

function RangeInput({
  prefix,
  value,
  onChange,
}: {
  prefix: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <div className="border-border bg-surface rounded-control flex items-center border">
      <span className="text-subtle-foreground border-border border-r px-2 text-xs">{prefix}</span>
      <NumericInput
        value={value}
        decimal
        aria-label={prefix}
        className="h-8 w-16 border-0 bg-transparent px-2 text-[13px] shadow-none"
        onValueChange={onChange}
      />
    </div>
  );
}

function NumberInput({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <NumericInput value={value} aria-label={label} placeholder={label} onValueChange={onChange} />
  );
}

function CountField({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <Field label={label}>
      {(id) => <NumericInput id={id} value={value} onValueChange={onChange} />}
    </Field>
  );
}

function ToggleField({
  label,
  hint,
  checked,
  onChange,
}: {
  label: string;
  hint: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <div className="grid gap-1.5">
      <div className="flex items-center gap-2">
        <Switch checked={checked} onCheckedChange={onChange} aria-label={label} />
        <span className="text-foreground text-[13px]">{label}</span>
      </div>
      <span className="text-subtle-foreground text-xs">{hint}</span>
    </div>
  );
}

function RemoveButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <Button size="icon-sm" variant="ghost" aria-label={label} onClick={onClick}>
      <X />
    </Button>
  );
}

function AddButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <div>
      <Button size="sm" variant="secondary" onClick={onClick}>
        <Plus />
        {label}
      </Button>
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
    <section className="border-border bg-surface rounded-panel border p-3">
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
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: (id: string) => ReactNode;
}) {
  const id = useId();
  return (
    <div className="grid content-start gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children(id)}
      {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
    </div>
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
  if (spec.containers.some((container) => container.privileged)) {
    impacts.push("存在特权级容器：它将拥有宿主机的 root 权限。");
  }
  if (spec.volumes?.some((volume) => volume.host_path)) {
    impacts.push("存在主机路径数据卷：容器将直接读写所在节点的文件系统。");
  }
  if (spec.tolerations?.length) {
    impacts.push("已配置容忍调度，Pod 可以被调度到带有对应污点的节点上。");
  }
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
