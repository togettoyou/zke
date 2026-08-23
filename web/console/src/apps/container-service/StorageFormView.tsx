import { useState, type ReactNode } from "react";
import { Plus, X } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import {
  useCreateStorageResource,
  useStorageResource,
  useUpdateStorageResource,
  type StorageCreateSpec,
  type StorageUpdateSpec,
} from "@/api/queries/storage";
import type {
  KubernetesStorageResource,
  KubernetesStorageResourceDetail,
  KubernetesStorageResourceSummary,
} from "@/api/types";
import { PageHeader } from "@/apps/AppShell";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
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

import { ACCESS_MODES, storageKindLabel, type AccessMode } from "./storage-catalog";

const DNS_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;
/** A Kubernetes quantity: a number with an optional binary or decimal suffix. */
const QUANTITY = /^\d+(\.\d+)?(Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E|m)?$/;
const DEFAULT_OPTION = "__default__";

type PairDraft = { key: string; value: string };

/**
 * Creates or edits one PersistentVolume, PersistentVolumeClaim or StorageClass.
 *
 * A page rather than a dialog, and one form for both jobs, as the workload and
 * networking sections do it: what an operator needs to see while changing a
 * volume is what the volume currently is, and a box laid over the list showed
 * one field with no surroundings.
 *
 * Editing shows every field and lets almost none of them be changed — that is
 * Kubernetes, not a limit of this form. A bound PV's source, a PVC's access
 * modes and StorageClass, a StorageClass's provisioner and parameters are all
 * refused after creation, so they are rendered from the live object and
 * disabled. What stays open is exactly what the typed API accepts: a PV's
 * reclaim policy, a PVC's requested size (upwards only) and a StorageClass's
 * expansion switch. Showing the rest read-only is the point — an edit form that
 * hid them would leave the operator guessing what they are about to keep.
 */
export function StorageFormView({
  clusterId,
  clusterName,
  namespace,
  resource,
  existing,
  onClose,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  resource: KubernetesStorageResource;
  /** Set when editing; absent when creating. */
  existing: KubernetesStorageResourceSummary | null;
  onClose: () => void;
}) {
  const kind = storageKindLabel(resource);
  // The immutable fields are only in the detail — a PV's source, a
  // StorageClass's parameters — so an edit waits for it rather than opening a
  // form full of blanks that would read as "these are now empty".
  const detail = useStorageResource(
    clusterId,
    existing ? namespace : null,
    resource,
    existing?.name ?? null,
  );

  if (existing && (detail.error || detail.isLoading || !detail.data)) {
    return (
      <div className="grid gap-3">
        <PageHeader title={`编辑 ${kind} · ${existing.name}`} onBack={onClose} />
        {detail.error ? (
          <ErrorState error={detail.error} onRetry={() => void detail.refetch()} />
        ) : (
          <LoadingState />
        )}
      </div>
    );
  }

  return (
    <StorageFormBody
      // Remounted when the object changes: the drafts below take their initial
      // values once, and a form already open must not be rewritten underneath
      // the operator by a refetch.
      key={`${resource}/${existing?.uid ?? "new"}`}
      clusterId={clusterId}
      clusterName={clusterName}
      namespace={namespace}
      resource={resource}
      existing={existing}
      detail={existing ? (detail.data as KubernetesStorageResourceDetail) : null}
      onClose={onClose}
    />
  );
}

function StorageFormBody({
  clusterId,
  clusterName,
  namespace,
  resource,
  existing,
  detail,
  onClose,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  resource: KubernetesStorageResource;
  existing: KubernetesStorageResourceSummary | null;
  detail: KubernetesStorageResourceDetail | null;
  onClose: () => void;
}) {
  const create = useCreateStorageResource();
  const update = useUpdateStorageResource();
  const mutation = existing ? update : create;
  const kind = storageKindLabel(resource);
  const [previewed, setPreviewed] = useState<StorageCreateSpec | StorageUpdateSpec | null>(null);
  const previewKey = useSubmissionKey(previewed === null);
  const applyKey = useSubmissionKey(previewed !== null);
  const [name, setName] = useState(existing?.name ?? "");

  const volume = usePersistentVolumeDraft(detail);
  const claim = useClaimDraft(detail);
  const storageClass = useStorageClassDraft(detail);
  const editor =
    resource === "persistentvolumes"
      ? volume
      : resource === "persistentvolumeclaims"
        ? claim
        : storageClass;

  /*
   * The one thing blocking submission, named where it can be fixed.
   *
   * One at a time, in the order the sections appear: a disabled button with no
   * reason next to it is a form an operator has to re-read field by field, and
   * the reason is rarely the field they are looking at.
   */
  const problem = existing ? editor.updateProblem : (nameProblem(name) ?? editor.problem);
  const valid = problem === null;

  const submit = (dryRun: boolean, spec: StorageCreateSpec | StorageUpdateSpec) => {
    const shared = {
      clusterId,
      namespace,
      resource,
      dryRun,
      idempotencyKey: dryRun ? previewKey : applyKey,
    };
    const request = existing
      ? update.mutateAsync({
          ...shared,
          name: existing.name,
          uid: existing.uid,
          resourceVersion: existing.resource_version,
          spec: spec as StorageUpdateSpec,
        })
      : create.mutateAsync({ ...shared, name: name.trim(), spec: spec as StorageCreateSpec });
    void request
      .then(() => {
        if (dryRun) {
          setPreviewed(spec);
          return;
        }
        toast.success(`${kind} ${existing?.name ?? name.trim()} 已${existing ? "更新" : "创建"}`);
        onClose();
      })
      .catch(() => undefined);
  };

  return (
    <>
      <div className="grid gap-3">
        <PageHeader
          title={
            existing
              ? `编辑 ${kind} · ${existing.name}`
              : `创建 ${kind}${resource === "persistentvolumeclaims" ? ` · ${namespace}` : ""}`
          }
          onBack={onClose}
        />

        {existing ? (
          <Alert tone="info">{lockNotice(resource)}</Alert>
        ) : (
          <FormSection
            title="基本信息"
            problem={problem?.section === "基本信息" ? problem.message : undefined}
          >
            <div className="grid gap-1.5">
              <Label htmlFor="storage-name">名称</Label>
              <Input
                id="storage-name"
                value={name}
                autoComplete="off"
                spellCheck={false}
                placeholder="例如 model-cache"
                onChange={(event) => setName(event.target.value)}
              />
              <span className="text-subtle-foreground text-xs">
                合法的 DNS 子域名，最长 253 个字符；创建后不可修改
              </span>
            </div>
          </FormSection>
        )}

        {editor.fields}

        {/*
         * The message itself is up in the section that can fix it; down here,
         * next to the button it disables, what is missing is where to look.
         * Only while creating: an edit has one editable field in one section
         * already sitting above this button, so pointing at it would repeat what
         * is on screen.
         */}
        {problem && !existing ? (
          <Alert tone="warning">「{problem.section}」中还有需要修正的项。</Alert>
        ) : null}
        {mutation.error ? <Alert tone="danger">{errorMessage(mutation.error)}</Alert> : null}

        <div className="flex items-center justify-end gap-3 pb-2">
          <span className="text-subtle-foreground text-xs">
            目标：{clusterName}
            {resource === "persistentvolumeclaims" ? ` / ${namespace}` : "（集群级对象）"}
          </span>
          <Button
            variant="primary"
            size="sm"
            disabled={!valid || mutation.isPending}
            onClick={() => submit(true, existing ? editor.buildUpdate() : editor.build())}
          >
            {mutation.isPending ? "DryRun 预检中…" : "执行 DryRun 预检"}
          </Button>
        </div>
      </div>

      <SensitiveActionDialog
        open={previewed !== null}
        onOpenChange={(open) => !open && setPreviewed(null)}
        title={existing ? `确认更新 ${kind}` : `确认创建 ${kind}`}
        description="DryRun 预检已通过。确认后将向同一集群提交实际变更。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          ...(resource === "persistentvolumeclaims"
            ? [{ label: "命名空间", name: namespace }]
            : []),
          { label: kind, name: existing?.name ?? name.trim(), id: existing?.uid },
        ]}
        impacts={existing ? editor.updateImpacts : createImpacts(resource)}
        confirmLabel={existing ? "确认更新" : "确认创建"}
        destructive={existing !== null && editor.destructiveUpdate}
        pending={mutation.isPending}
        error={mutation.error}
        onConfirm={() => previewed && submit(false, previewed)}
      />
    </>
  );
}

/** Said once at the top of an edit rather than on each disabled field. */
function lockNotice(resource: KubernetesStorageResource): string {
  if (resource === "persistentvolumes") {
    return "Kubernetes 只允许修改已登记 PV 的回收策略。容量、访问模式、卷模式和后端来源在创建后不可变，下面按对象当前状态显示并已锁定。";
  }
  if (resource === "persistentvolumeclaims") {
    return "Kubernetes 只允许扩大 PVC 的申请容量，且需要其 StorageClass 允许扩容。访问模式、StorageClass、卷模式和绑定的卷在创建后不可变，下面按对象当前状态显示并已锁定。";
  }
  return "Kubernetes 只允许修改 StorageClass 的「允许卷扩容」。Provisioner、回收策略、绑定模式和参数在创建后不可变，下面按对象当前状态显示并已锁定。";
}

function createImpacts(resource: KubernetesStorageResource): string[] {
  if (resource === "persistentvolumes") {
    return [
      "将在集群中登记一个 PersistentVolume，指向你填写的后端存储。",
      "ZKE 不会创建底层存储：填写的 CSI 卷、NFS 导出或本地路径必须已经存在。",
      "类型化接口创建后只开放回收策略更新；其他高级字段需要通过 YAML 管理。",
    ];
  }
  if (resource === "persistentvolumeclaims") {
    return [
      "将在该命名空间创建一个 PVC；若指定了支持动态制备的 StorageClass，存储会被自动创建。",
      "创建后只能扩容，不能缩容，也不能修改访问模式或 StorageClass。",
    ];
  }
  return [
    "将新增一个 StorageClass，之后的 PVC 可以引用它动态制备卷。",
    "类型化接口创建后只开放「允许扩容」开关；其他高级字段需要通过 YAML 管理。",
  ];
}

const UPDATE_PRECONDITION =
  "请求携带该对象当前的 UID 与 resourceVersion，期间对象若已变化，更新会被拒绝而不是覆盖。";

/*
 * The sections these forms render, named by their own titles.
 *
 * A literal union rather than free text: the section that carries a problem is
 * pointed at from the footer, and a typo there would silently stop the message
 * from appearing anywhere.
 */
type StorageSection = "基本信息" | "卷" | "来源" | "申领" | "StorageClass" | "参数";

/** The one thing currently blocking submission, and where it can be fixed. */
type StorageProblem = { section: StorageSection; message: string };

function at(section: StorageSection, message: string): StorageProblem {
  return { section, message };
}

type SpecEditor = {
  fields: ReactNode;
  /**
   * What stops this draft from being created, or null when nothing does. The
   * editors own their sections, so each one reports which of its own sections
   * carries the message and renders it there.
   */
  problem: StorageProblem | null;
  /**
   * What stops an update from being submitted. Not the same question: an update
   * carries one field, and it is refused both when that field is unusable and
   * when it matches what is already on screen — a confirmation dialog for a
   * write that changes nothing costs an audit record and nothing else.
   */
  updateProblem: StorageProblem | null;
  build: () => StorageCreateSpec;
  buildUpdate: () => StorageUpdateSpec;
  updateImpacts: string[];
  destructiveUpdate: boolean;
};

function nameProblem(name: string): StorageProblem | null {
  const trimmed = name.trim();
  if (trimmed === "") {
    return at("基本信息", "请填写名称。");
  }
  if (trimmed.length > 253) {
    return at("基本信息", "名称最长 253 个字符。");
  }
  if (!DNS_SUBDOMAIN.test(trimmed)) {
    return at(
      "基本信息",
      "名称必须是合法的 DNS 子域名：只能包含小写字母、数字、连字符和点，并以字母或数字开头和结尾。",
    );
  }
  return null;
}

/** A Kubernetes quantity field, which every capacity input here is. */
function quantityProblem(
  section: StorageSection,
  value: string,
  label: string,
): StorageProblem | null {
  const trimmed = value.trim();
  if (trimmed === "") {
    return at(section, `请填写${label}。`);
  }
  if (!QUANTITY.test(trimmed)) {
    return at(section, `${label}必须是 Kubernetes quantity，例如 10Gi 或 500Mi。`);
  }
  return null;
}

/*
 * What the chosen volume source still needs.
 *
 * Two of these are not missing fields but combinations Kubernetes refuses, and
 * they are reported the same way: the operator has to change something in this
 * section either way, and which of the two kinds it is does not change that.
 */
function persistentVolumeSourceProblem(draft: {
  sourceType: "csi" | "nfs" | "local";
  blockVolume: boolean;
  driver: string;
  volumeHandle: string;
  fsType: string;
  server: string;
  path: string;
  localNode: string;
}): StorageProblem | null {
  const { sourceType, blockVolume, driver, volumeHandle, fsType, server, path, localNode } = draft;
  if (sourceType === "csi") {
    if (driver.trim() === "") {
      return at("来源", "请填写 CSI 驱动名称。");
    }
    if (volumeHandle.trim() === "") {
      return at("来源", "请填写卷标识（volumeHandle）。");
    }
  }
  if (sourceType === "nfs") {
    if (blockVolume) {
      return at("来源", "NFS 只支持 Filesystem 卷模式，不能作为 Block 卷。");
    }
    if (server.trim() === "") {
      return at("来源", "请填写 NFS 服务器地址。");
    }
    if (path.trim() === "") {
      return at("来源", "请填写 NFS 导出路径。");
    }
  }
  if (sourceType === "local") {
    if (path.trim() === "") {
      return at("来源", "请填写节点上的路径。");
    }
    if (localNode.trim() === "") {
      return at("来源", "请填写节点名称：Local PV 必须绑定到具体节点。");
    }
  }
  if (blockVolume && sourceType !== "nfs" && fsType.trim() !== "") {
    return at("来源", "Block 卷不能设置文件系统类型，请清空该字段。");
  }
  return null;
}

function usePersistentVolumeDraft(existing: KubernetesStorageResourceDetail | null): SpecEditor {
  const view = existing?.persistent_volume;
  const source = existing?.persistent_volume_detail?.source;
  const locked = existing !== null;

  const [capacity, setCapacity] = useState(view?.capacity ?? "");
  const [modes, setModes] = useState<AccessMode[]>(
    (view?.access_modes as AccessMode[] | undefined) ?? ["ReadWriteOnce"],
  );
  const [reclaim, setReclaim] = useState(
    view ? (view.reclaim_policy === "Delete" ? "Delete" : "Retain") : DEFAULT_OPTION,
  );
  const [storageClassName, setStorageClassName] = useState(view?.storage_class_name ?? "");
  const [volumeMode, setVolumeMode] = useState(view?.volume_mode || DEFAULT_OPTION);
  const [sourceType, setSourceType] = useState<"csi" | "nfs" | "local">(
    source?.type === "nfs" ? "nfs" : source?.type === "local" ? "local" : "csi",
  );
  const [driver, setDriver] = useState(source?.csi?.driver ?? "");
  const [volumeHandle, setVolumeHandle] = useState(source?.csi?.volume_handle ?? "");
  const [fsType, setFsType] = useState(source?.csi?.fs_type ?? source?.local?.fs_type ?? "");
  const [readOnly, setReadOnly] = useState(
    source?.csi?.read_only ?? source?.nfs?.read_only ?? false,
  );
  const [server, setServer] = useState(source?.nfs?.server ?? "");
  const [path, setPath] = useState(source?.nfs?.path ?? source?.local?.path ?? "");
  const [localNode, setLocalNode] = useState(
    existing?.persistent_volume_detail?.node_affinity?.terms?.[0]?.match_expressions?.find(
      (expression) => expression.key === "kubernetes.io/hostname",
    )?.values?.[0] ?? "",
  );

  const blockVolume = volumeMode === "Block";

  const problem =
    quantityProblem("卷", capacity, "容量") ??
    (modes.length === 0 ? at("卷", "请至少选择一种访问模式。") : null) ??
    persistentVolumeSourceProblem({
      sourceType,
      blockVolume,
      driver,
      volumeHandle,
      fsType,
      server,
      path,
      localNode,
    });
  // The reclaim policy is the only field an update carries. "默认" is not one of
  // its values — it means the field is absent, which an update cannot say.
  const updateProblem =
    reclaim !== "Retain" && reclaim !== "Delete"
      ? at("卷", "请选择回收策略。")
      : reclaim === (view?.reclaim_policy ?? "")
        ? at("卷", "回收策略与当前一致，没有需要提交的改动。")
        : null;
  // Every field but one is read-only while editing, so the message that belongs
  // on screen is the one about the write actually being attempted.
  const shown = locked ? updateProblem : problem;

  const build = (): StorageCreateSpec => ({
    persistent_volume: {
      capacity: capacity.trim(),
      access_modes: modes,
      ...(reclaim === DEFAULT_OPTION ? {} : { reclaim_policy: reclaim as "Retain" | "Delete" }),
      ...(storageClassName.trim() ? { storage_class_name: storageClassName.trim() } : {}),
      ...(volumeMode === DEFAULT_OPTION
        ? {}
        : { volume_mode: volumeMode as "Filesystem" | "Block" }),
      source: {
        type: sourceType,
        ...(sourceType === "csi"
          ? {
              csi: {
                driver: driver.trim(),
                volume_handle: volumeHandle.trim(),
                read_only: readOnly,
                ...(fsType.trim() ? { fs_type: fsType.trim() } : {}),
              },
            }
          : {}),
        ...(sourceType === "nfs"
          ? { nfs: { server: server.trim(), path: path.trim(), read_only: readOnly } }
          : {}),
        ...(sourceType === "local"
          ? { local: { path: path.trim(), ...(fsType.trim() ? { fs_type: fsType.trim() } : {}) } }
          : {}),
      },
      ...(sourceType === "local"
        ? {
            node_affinity: {
              terms: [
                {
                  match_expressions: [
                    {
                      key: "kubernetes.io/hostname",
                      operator: "In" as const,
                      values: [localNode.trim()],
                    },
                  ],
                },
              ],
            },
          }
        : {}),
    },
  });

  const fields = (
    <>
      <FormSection title="卷" problem={shown?.section === "卷" ? shown.message : undefined}>
        <div className="grid gap-3 @md:grid-cols-2">
          <Field label="容量" htmlFor="pv-capacity" hint="Kubernetes quantity，例如 10Gi">
            <Input
              id="pv-capacity"
              value={capacity}
              autoComplete="off"
              placeholder="10Gi"
              disabled={locked}
              onChange={(event) => setCapacity(event.target.value)}
            />
          </Field>
          <Field label="StorageClass" htmlFor="pv-class" hint="留空表示不属于任何 StorageClass">
            <Input
              id="pv-class"
              value={storageClassName}
              autoComplete="off"
              spellCheck={false}
              disabled={locked}
              onChange={(event) => setStorageClassName(event.target.value)}
            />
          </Field>
          <Field
            label="回收策略"
            htmlFor="pv-reclaim"
            hint={
              locked ? `当前 ${view?.reclaim_policy || "—"}，这是本页唯一可改的字段` : undefined
            }
          >
            <Select value={reclaim} onValueChange={setReclaim}>
              <SelectTrigger id="pv-reclaim">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {locked ? null : <SelectItem value={DEFAULT_OPTION}>默认</SelectItem>}
                <SelectItem value="Retain">Retain（保留数据）</SelectItem>
                <SelectItem value="Delete">Delete（销毁数据）</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field label="卷模式" htmlFor="pv-volume-mode">
            <Select value={volumeMode} onValueChange={setVolumeMode} disabled={locked}>
              <SelectTrigger id="pv-volume-mode">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={DEFAULT_OPTION}>默认（Filesystem）</SelectItem>
                <SelectItem value="Filesystem">Filesystem</SelectItem>
                <SelectItem value="Block">Block</SelectItem>
              </SelectContent>
            </Select>
          </Field>
        </div>
        <AccessModePicker modes={modes} disabled={locked} onChange={setModes} />
      </FormSection>

      <FormSection
        title="来源"
        hint="ZKE 不会创建底层存储，它必须已经存在"
        problem={shown?.section === "来源" ? shown.message : undefined}
      >
        <div className="grid gap-3">
          <Field label="类型" htmlFor="pv-source-type">
            <Select
              value={sourceType}
              disabled={locked}
              onValueChange={(value) => setSourceType(value as "csi" | "nfs" | "local")}
            >
              <SelectTrigger id="pv-source-type" className="w-48">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="csi">CSI</SelectItem>
                <SelectItem value="nfs">NFS</SelectItem>
                <SelectItem value="local">Local</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          {sourceType === "csi" ? (
            <div className="grid gap-3 @md:grid-cols-2">
              <Field label="驱动" htmlFor="pv-driver">
                <Input
                  id="pv-driver"
                  value={driver}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="例如 ebs.csi.aws.com"
                  disabled={locked}
                  onChange={(event) => setDriver(event.target.value)}
                />
              </Field>
              <Field label="卷句柄" htmlFor="pv-handle">
                <Input
                  id="pv-handle"
                  value={volumeHandle}
                  autoComplete="off"
                  spellCheck={false}
                  disabled={locked}
                  onChange={(event) => setVolumeHandle(event.target.value)}
                />
              </Field>
              <Field label="文件系统" htmlFor="pv-fs">
                <Input
                  id="pv-fs"
                  value={fsType}
                  autoComplete="off"
                  placeholder="例如 ext4"
                  disabled={locked}
                  onChange={(event) => setFsType(event.target.value)}
                />
              </Field>
            </div>
          ) : null}
          {sourceType === "nfs" ? (
            <div className="grid gap-3 @md:grid-cols-2">
              <Field label="服务器" htmlFor="pv-server">
                <Input
                  id="pv-server"
                  value={server}
                  autoComplete="off"
                  spellCheck={false}
                  disabled={locked}
                  onChange={(event) => setServer(event.target.value)}
                />
              </Field>
              <Field label="导出路径" htmlFor="pv-path">
                <Input
                  id="pv-path"
                  value={path}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="/exports/data"
                  disabled={locked}
                  onChange={(event) => setPath(event.target.value)}
                />
              </Field>
            </div>
          ) : null}
          {sourceType === "local" ? (
            <div className="grid gap-3 @md:grid-cols-2">
              <Field label="节点路径" htmlFor="pv-local-path">
                <Input
                  id="pv-local-path"
                  value={path}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="/mnt/disks/ssd1"
                  disabled={locked}
                  onChange={(event) => setPath(event.target.value)}
                />
              </Field>
              <Field label="文件系统" htmlFor="pv-local-fs">
                <Input
                  id="pv-local-fs"
                  value={fsType}
                  autoComplete="off"
                  disabled={locked}
                  onChange={(event) => setFsType(event.target.value)}
                />
              </Field>
              <Field
                label="节点名称"
                htmlFor="pv-local-node"
                hint="将生成 kubernetes.io/hostname Node Affinity"
              >
                <Input
                  id="pv-local-node"
                  value={localNode}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="例如 worker-01"
                  disabled={locked}
                  onChange={(event) => setLocalNode(event.target.value)}
                />
              </Field>
            </div>
          ) : null}
          {sourceType === "local" ? null : (
            <label className="flex items-center gap-2 text-[13px]">
              <Checkbox
                checked={readOnly}
                disabled={locked}
                onCheckedChange={(checked) => setReadOnly(checked === true)}
              />
              只读挂载
            </label>
          )}
        </div>
      </FormSection>
    </>
  );

  return {
    fields,
    problem,
    updateProblem,
    build,
    buildUpdate: () => ({
      persistent_volume: { reclaim_policy: reclaim as "Retain" | "Delete" },
    }),
    updateImpacts: [
      reclaim === "Delete"
        ? "改为 Delete 后，删除该 PV 或其绑定的 PVC 时，底层存储和其中的数据会被一并销毁。"
        : "改为 Retain 后，PV 被释放时数据会保留，但需要管理员手动清理或重新绑定。",
      UPDATE_PRECONDITION,
    ],
    destructiveUpdate: reclaim === "Delete",
  };
}

function useClaimDraft(existing: KubernetesStorageResourceDetail | null): SpecEditor {
  const view = existing?.persistent_volume_claim;
  const locked = existing !== null;
  const currentCapacity = view?.requested_capacity ?? "";

  const [capacity, setCapacity] = useState(currentCapacity);
  const [modes, setModes] = useState<AccessMode[]>(
    (view?.access_modes as AccessMode[] | undefined) ?? ["ReadWriteOnce"],
  );
  const [storageClassName, setStorageClassName] = useState(view?.storage_class_name ?? "");
  const [useDefaultClass, setUseDefaultClass] = useState(
    view ? view.storage_class_name === null : true,
  );
  const [volumeName, setVolumeName] = useState(view?.volume_name ?? "");
  const [volumeMode, setVolumeMode] = useState(view?.volume_mode || DEFAULT_OPTION);

  const problem =
    quantityProblem("申领", capacity, "申请容量") ??
    (modes.length === 0 ? at("申领", "请至少选择一种访问模式。") : null);
  /*
   * The requested capacity is the only field an update carries.
   *
   * Whether it is actually an increase is left to the Server, which refuses a
   * shrink before it reaches the Agent: comparing two Kubernetes quantities
   * needs a unit parser, and one written here could disagree with the one that
   * decides.
   */
  const updateProblem =
    quantityProblem("申领", capacity, "申请容量") ??
    (capacity.trim() === currentCapacity
      ? at("申领", "申请容量与当前一致，没有需要提交的改动。")
      : null);
  const shown = locked ? updateProblem : problem;

  const build = (): StorageCreateSpec => ({
    persistent_volume_claim: {
      requested_capacity: capacity.trim(),
      access_modes: modes,
      // An empty string and an absent field mean different things here: `""`
      // asks Kubernetes for no StorageClass at all, which is how a PVC binds a
      // manually created PV. "Use the default" is the field being absent.
      ...(useDefaultClass ? {} : { storage_class_name: storageClassName.trim() }),
      ...(volumeName.trim() ? { volume_name: volumeName.trim() } : {}),
      ...(volumeMode === DEFAULT_OPTION
        ? {}
        : { volume_mode: volumeMode as "Filesystem" | "Block" }),
    },
  });

  const fields = (
    <FormSection title="申领" problem={shown?.section === "申领" ? shown.message : undefined}>
      <div className="grid gap-3 @md:grid-cols-2">
        <Field
          label="申请容量"
          htmlFor="pvc-capacity"
          hint={
            locked
              ? `当前申请 ${currentCapacity || "—"}${
                  view?.capacity ? `，已分配 ${view.capacity}` : ""
                }。只能增大：Kubernetes 不支持缩容，扩容还需要 StorageClass 允许。`
              : "Kubernetes quantity，例如 10Gi"
          }
        >
          <Input
            id="pvc-capacity"
            value={capacity}
            autoComplete="off"
            placeholder="10Gi"
            onChange={(event) => setCapacity(event.target.value)}
          />
        </Field>
        <Field label="卷模式" htmlFor="pvc-volume-mode">
          <Select value={volumeMode} onValueChange={setVolumeMode} disabled={locked}>
            <SelectTrigger id="pvc-volume-mode">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={DEFAULT_OPTION}>默认（Filesystem）</SelectItem>
              <SelectItem value="Filesystem">Filesystem</SelectItem>
              <SelectItem value="Block">Block</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        <Field
          label="绑定到指定卷"
          htmlFor="pvc-volume-name"
          hint={locked ? undefined : "留空表示由 Kubernetes 选择"}
        >
          <Input
            id="pvc-volume-name"
            value={volumeName}
            autoComplete="off"
            spellCheck={false}
            disabled={locked}
            onChange={(event) => setVolumeName(event.target.value)}
          />
        </Field>
      </div>
      <label className="mt-3 flex items-center gap-2 text-[13px]">
        <Checkbox
          checked={useDefaultClass}
          disabled={locked}
          onCheckedChange={(checked) => setUseDefaultClass(checked === true)}
        />
        使用集群默认 StorageClass
      </label>
      {useDefaultClass ? null : (
        <div className="mt-2 grid gap-1.5">
          <Label htmlFor="pvc-class">StorageClass</Label>
          <Input
            id="pvc-class"
            value={storageClassName}
            autoComplete="off"
            spellCheck={false}
            placeholder="留空表示不使用任何 StorageClass（绑定手工创建的 PV）"
            disabled={locked}
            onChange={(event) => setStorageClassName(event.target.value)}
          />
        </div>
      )}
      <AccessModePicker modes={modes} disabled={locked} onChange={setModes} />
    </FormSection>
  );

  return {
    fields,
    problem,
    updateProblem,
    build,
    buildUpdate: () => ({
      persistent_volume_claim: { requested_capacity: capacity.trim() },
    }),
    updateImpacts: [
      `申请容量将调整为 ${capacity.trim()}。扩容由 StorageClass 和 CSI 驱动执行，可能需要一段时间才反映到已分配容量。`,
      "部分驱动要求 Pod 重启后文件系统才会扩展到新的容量。",
      UPDATE_PRECONDITION,
    ],
    destructiveUpdate: false,
  };
}

function useStorageClassDraft(existing: KubernetesStorageResourceDetail | null): SpecEditor {
  const view = existing?.storage_class;
  const locked = existing !== null;
  const currentExpansion = view?.allow_volume_expansion ?? false;

  const [provisioner, setProvisioner] = useState(view?.provisioner ?? "");
  const [reclaim, setReclaim] = useState(view?.reclaim_policy || DEFAULT_OPTION);
  const [bindingMode, setBindingMode] = useState(view?.volume_binding_mode || DEFAULT_OPTION);
  const [allowExpansion, setAllowExpansion] = useState(currentExpansion);
  const [parameters, setParameters] = useState<PairDraft[]>(() =>
    Object.entries(existing?.storage_class_detail?.parameters ?? {}).map(([key, value]) => ({
      key,
      value,
    })),
  );
  const parameterKeys = parameters.map((pair) => pair.key.trim()).filter(Boolean);
  const duplicateParameter = new Set(parameterKeys).size !== parameterKeys.length;

  const problem =
    provisioner.trim() === ""
      ? at("StorageClass", "请填写 Provisioner。")
      : duplicateParameter
        ? at("参数", "参数键不能重复；请合并或删除重复项。")
        : null;
  // The expansion switch is the only field an update carries, and a switch that
  // is already in the requested position has nothing to submit.
  const updateProblem =
    allowExpansion === currentExpansion
      ? at("StorageClass", "「允许卷扩容」与当前一致，没有需要提交的改动。")
      : null;
  const shown = locked ? updateProblem : problem;

  const build = (): StorageCreateSpec => ({
    storage_class: {
      provisioner: provisioner.trim(),
      allow_volume_expansion: allowExpansion,
      ...(reclaim === DEFAULT_OPTION ? {} : { reclaim_policy: reclaim as "Retain" | "Delete" }),
      ...(bindingMode === DEFAULT_OPTION
        ? {}
        : { volume_binding_mode: bindingMode as "Immediate" | "WaitForFirstConsumer" }),
      ...(parameters.some((pair) => pair.key.trim() !== "")
        ? {
            parameters: Object.fromEntries(
              parameters
                .filter((pair) => pair.key.trim() !== "")
                .map((pair) => [pair.key.trim(), pair.value]),
            ),
          }
        : {}),
    },
  });

  const fields = (
    <>
      <FormSection
        title="StorageClass"
        problem={shown?.section === "StorageClass" ? shown.message : undefined}
      >
        <div className="grid gap-3 @md:grid-cols-2">
          <Field label="Provisioner" htmlFor="sc-provisioner">
            <Input
              id="sc-provisioner"
              value={provisioner}
              autoComplete="off"
              spellCheck={false}
              placeholder="例如 ebs.csi.aws.com"
              disabled={locked}
              onChange={(event) => setProvisioner(event.target.value)}
            />
          </Field>
          <Field label="回收策略" htmlFor="sc-reclaim">
            <Select value={reclaim} onValueChange={setReclaim} disabled={locked}>
              <SelectTrigger id="sc-reclaim">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={DEFAULT_OPTION}>默认（Delete）</SelectItem>
                <SelectItem value="Retain">Retain</SelectItem>
                <SelectItem value="Delete">Delete</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field label="绑定模式" htmlFor="sc-binding">
            <Select value={bindingMode} onValueChange={setBindingMode} disabled={locked}>
              <SelectTrigger id="sc-binding">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={DEFAULT_OPTION}>默认（Immediate）</SelectItem>
                <SelectItem value="Immediate">Immediate</SelectItem>
                <SelectItem value="WaitForFirstConsumer">WaitForFirstConsumer</SelectItem>
              </SelectContent>
            </Select>
          </Field>
        </div>
        <label className="mt-3 flex items-center gap-2 text-[13px]">
          <Checkbox
            checked={allowExpansion}
            onCheckedChange={(checked) => setAllowExpansion(checked === true)}
          />
          允许卷扩容
        </label>
        {locked ? (
          <span className="text-subtle-foreground mt-1 block text-xs">
            这是本页唯一可改的字段；当前 {currentExpansion ? "允许" : "不允许"}。
          </span>
        ) : null}
      </FormSection>

      <FormSection
        title="参数"
        hint="Provisioner 自定义的键值对"
        problem={shown?.section === "参数" ? shown.message : undefined}
      >
        <PairList rows={parameters} disabled={locked} onChange={setParameters} />
      </FormSection>
    </>
  );

  return {
    fields,
    problem,
    updateProblem,
    build,
    buildUpdate: () => ({ storage_class: { allow_volume_expansion: allowExpansion } }),
    updateImpacts: [
      allowExpansion
        ? "之后由该 StorageClass 制备的 PVC 将允许扩容。"
        : "关闭后，引用该 StorageClass 的 PVC 将不能再扩容。",
      "已有的 PV 和 PVC 不受影响。",
      UPDATE_PRECONDITION,
    ],
    destructiveUpdate: false,
  };
}

/**
 * Access modes are a set, not a choice: a volume may allow several. Checkboxes
 * say that; a dropdown would not.
 */
function AccessModePicker({
  modes,
  disabled,
  onChange,
}: {
  modes: AccessMode[];
  disabled?: boolean;
  onChange: (modes: AccessMode[]) => void;
}) {
  return (
    <div className="mt-3 grid gap-1.5">
      <span className="text-foreground text-[13px] font-medium">访问模式</span>
      <div className="flex flex-wrap gap-x-4 gap-y-1.5">
        {ACCESS_MODES.map((mode) => (
          <label key={mode} className="flex items-center gap-2 text-[13px]">
            <Checkbox
              checked={modes.includes(mode)}
              disabled={disabled}
              onCheckedChange={(checked) =>
                onChange(
                  checked === true ? [...modes, mode] : modes.filter((entry) => entry !== mode),
                )
              }
            />
            {mode}
          </label>
        ))}
      </div>
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

function PairList({
  rows,
  disabled,
  onChange,
}: {
  rows: PairDraft[];
  disabled?: boolean;
  onChange: (rows: PairDraft[]) => void;
}) {
  const update = (index: number, patch: Partial<PairDraft>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...patch } : row)));

  return (
    <div className="grid gap-2">
      {rows.length === 0 && disabled ? (
        <span className="text-subtle-foreground text-xs">该 StorageClass 没有参数。</span>
      ) : null}
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[1fr_1fr_auto] items-center gap-2">
          <Input
            value={row.key}
            aria-label={`参数 ${index + 1} 键`}
            placeholder="键"
            autoComplete="off"
            spellCheck={false}
            disabled={disabled}
            onChange={(event) => update(index, { key: event.target.value })}
          />
          <Input
            value={row.value}
            aria-label={`参数 ${index + 1} 值`}
            placeholder="值"
            autoComplete="off"
            spellCheck={false}
            disabled={disabled}
            onChange={(event) => update(index, { value: event.target.value })}
          />
          {disabled ? null : (
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={`移除参数 ${index + 1}`}
              onClick={() => onChange(rows.filter((_, position) => position !== index))}
            >
              <X />
            </Button>
          )}
        </div>
      ))}
      {disabled ? null : (
        <div>
          <Button
            size="sm"
            variant="secondary"
            onClick={() => onChange([...rows, { key: "", value: "" }])}
          >
            <Plus />
            添加参数
          </Button>
        </div>
      )}
    </div>
  );
}
