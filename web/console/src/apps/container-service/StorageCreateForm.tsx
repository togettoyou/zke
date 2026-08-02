import { useState, type ReactNode } from "react";
import { Plus, X } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import { useCreateStorageResource, type StorageCreateSpec } from "@/api/queries/storage";
import type { KubernetesStorageResource } from "@/api/types";
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

import { ACCESS_MODES, storageKindLabel, type AccessMode } from "./storage-catalog";

const DNS_SUBDOMAIN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$/;
/** A Kubernetes quantity: a number with an optional binary or decimal suffix. */
const QUANTITY = /^\d+(\.\d+)?(Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E|m)?$/;
const DEFAULT_OPTION = "__default__";

type PairDraft = { key: string; value: string };

/**
 * Creates one PersistentVolume, PersistentVolumeClaim or StorageClass.
 *
 * The typed API intentionally keeps later mutations narrow, so creation makes
 * the durable choices explicit and leaves advanced changes to YAML.
 */
export function StorageCreateForm({
  clusterId,
  clusterName,
  namespace,
  resource,
  onClose,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  resource: KubernetesStorageResource;
  onClose: () => void;
}) {
  const create = useCreateStorageResource();
  const kind = storageKindLabel(resource);
  const [previewed, setPreviewed] = useState<StorageCreateSpec | null>(null);
  const previewKey = useSubmissionKey(previewed === null);
  const applyKey = useSubmissionKey(previewed !== null);
  const [name, setName] = useState("");

  const volume = usePersistentVolumeDraft();
  const claim = useClaimDraft();
  const storageClass = useStorageClassDraft();
  const editor =
    resource === "persistentvolumes"
      ? volume
      : resource === "persistentvolumeclaims"
        ? claim
        : storageClass;

  const nameValid = DNS_SUBDOMAIN.test(name.trim()) && name.length <= 253;
  const valid = nameValid && editor.valid;

  const submit = (dryRun: boolean, spec: StorageCreateSpec) =>
    void create
      .mutateAsync({
        clusterId,
        namespace,
        resource,
        name: name.trim(),
        spec,
        dryRun,
        idempotencyKey: dryRun ? previewKey : applyKey,
      })
      .then(() => {
        if (dryRun) {
          setPreviewed(spec);
          return;
        }
        toast.success(`${kind} ${name.trim()} 已创建`);
        onClose();
      })
      .catch(() => undefined);

  return (
    <>
      <Dialog open={previewed === null} onOpenChange={(open) => !open && onClose()}>
        <DialogContent aria-describedby={undefined} className="w-[min(760px,calc(100vw-2rem))]">
          <DialogHeader>
            <DialogTitle>创建 {kind}</DialogTitle>
            <DialogDescription>
              第一步只执行服务端 DryRun，不会在集群中持久化对象。
            </DialogDescription>
          </DialogHeader>

          <div className="grid gap-4">
            <FormSection title="基本信息">
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
              </div>
            </FormSection>
            {editor.fields}
          </div>

          <Alert tone="info" className="mt-4">
            目标：{clusterName}
            {resource === "persistentvolumeclaims" ? ` / ${namespace}` : "（集群级对象）"}
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
              onClick={() => submit(true, editor.build())}
            >
              {create.isPending ? "预检中…" : "执行 DryRun 预检"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <SensitiveActionDialog
        open={previewed !== null}
        onOpenChange={(open) => !open && setPreviewed(null)}
        title={`确认创建 ${kind}`}
        description="DryRun 已通过。确认后将向同一集群发送实际创建请求。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          ...(resource === "persistentvolumeclaims"
            ? [{ label: "命名空间", name: namespace }]
            : []),
          { label: kind, name: name.trim() },
        ]}
        impacts={createImpacts(resource)}
        confirmLabel="确认创建"
        pending={create.isPending}
        error={create.error}
        onConfirm={() => previewed && submit(false, previewed)}
      />
    </>
  );
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

type SpecEditor = { fields: ReactNode; valid: boolean; build: () => StorageCreateSpec };

function usePersistentVolumeDraft(): SpecEditor {
  const [capacity, setCapacity] = useState("");
  const [modes, setModes] = useState<AccessMode[]>(["ReadWriteOnce"]);
  const [reclaim, setReclaim] = useState(DEFAULT_OPTION);
  const [storageClassName, setStorageClassName] = useState("");
  const [volumeMode, setVolumeMode] = useState(DEFAULT_OPTION);
  const [sourceType, setSourceType] = useState<"csi" | "nfs" | "local">("csi");
  const [driver, setDriver] = useState("");
  const [volumeHandle, setVolumeHandle] = useState("");
  const [fsType, setFsType] = useState("");
  const [readOnly, setReadOnly] = useState(false);
  const [server, setServer] = useState("");
  const [path, setPath] = useState("");
  const [localNode, setLocalNode] = useState("");

  const blockVolume = volumeMode === "Block";

  const sourceValid =
    sourceType === "csi"
      ? driver.trim() !== "" && volumeHandle.trim() !== "" && (!blockVolume || fsType.trim() === "")
      : sourceType === "nfs"
        ? !blockVolume && server.trim() !== "" && path.trim() !== ""
        : path.trim() !== "" && localNode.trim() !== "" && (!blockVolume || fsType.trim() === "");
  const valid = QUANTITY.test(capacity.trim()) && modes.length > 0 && sourceValid;

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
      <FormSection title="卷">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="容量" htmlFor="pv-capacity" hint="Kubernetes quantity，例如 10Gi">
            <Input
              id="pv-capacity"
              value={capacity}
              autoComplete="off"
              placeholder="10Gi"
              onChange={(event) => setCapacity(event.target.value)}
            />
          </Field>
          <Field label="StorageClass" htmlFor="pv-class" hint="留空表示不属于任何 StorageClass">
            <Input
              id="pv-class"
              value={storageClassName}
              autoComplete="off"
              spellCheck={false}
              onChange={(event) => setStorageClassName(event.target.value)}
            />
          </Field>
          <Field label="回收策略" htmlFor="pv-reclaim">
            <Select value={reclaim} onValueChange={setReclaim}>
              <SelectTrigger id="pv-reclaim">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={DEFAULT_OPTION}>默认</SelectItem>
                <SelectItem value="Retain">Retain（保留数据）</SelectItem>
                <SelectItem value="Delete">Delete（销毁数据）</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field label="卷模式" htmlFor="pv-volume-mode">
            <Select value={volumeMode} onValueChange={setVolumeMode}>
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
        <AccessModePicker modes={modes} onChange={setModes} />
      </FormSection>

      <FormSection title="来源" hint="ZKE 不会创建底层存储，它必须已经存在">
        <div className="grid gap-3">
          <Field label="类型" htmlFor="pv-source-type">
            <Select
              value={sourceType}
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
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="驱动" htmlFor="pv-driver">
                <Input
                  id="pv-driver"
                  value={driver}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="例如 ebs.csi.aws.com"
                  onChange={(event) => setDriver(event.target.value)}
                />
              </Field>
              <Field label="卷句柄" htmlFor="pv-handle">
                <Input
                  id="pv-handle"
                  value={volumeHandle}
                  autoComplete="off"
                  spellCheck={false}
                  onChange={(event) => setVolumeHandle(event.target.value)}
                />
              </Field>
              <Field label="文件系统" htmlFor="pv-fs">
                <Input
                  id="pv-fs"
                  value={fsType}
                  autoComplete="off"
                  placeholder="例如 ext4"
                  onChange={(event) => setFsType(event.target.value)}
                />
              </Field>
            </div>
          ) : null}
          {sourceType === "nfs" ? (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="服务器" htmlFor="pv-server">
                <Input
                  id="pv-server"
                  value={server}
                  autoComplete="off"
                  spellCheck={false}
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
                  onChange={(event) => setPath(event.target.value)}
                />
              </Field>
            </div>
          ) : null}
          {sourceType === "local" ? (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="节点路径" htmlFor="pv-local-path">
                <Input
                  id="pv-local-path"
                  value={path}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="/mnt/disks/ssd1"
                  onChange={(event) => setPath(event.target.value)}
                />
              </Field>
              <Field label="文件系统" htmlFor="pv-local-fs">
                <Input
                  id="pv-local-fs"
                  value={fsType}
                  autoComplete="off"
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
                  onChange={(event) => setLocalNode(event.target.value)}
                />
              </Field>
            </div>
          ) : null}
          {sourceType === "local" ? null : (
            <label className="flex items-center gap-2 text-[13px]">
              <Checkbox
                checked={readOnly}
                onCheckedChange={(checked) => setReadOnly(checked === true)}
              />
              只读挂载
            </label>
          )}
          {blockVolume && sourceType === "nfs" ? (
            <Alert tone="warning">NFS 只支持 Filesystem 卷模式，不能作为 Block 卷。</Alert>
          ) : null}
          {blockVolume && sourceType !== "nfs" && fsType.trim() ? (
            <Alert tone="warning">Block 卷不能设置文件系统类型，请清空该字段。</Alert>
          ) : null}
        </div>
      </FormSection>
    </>
  );

  return { fields, valid, build };
}

function useClaimDraft(): SpecEditor {
  const [capacity, setCapacity] = useState("");
  const [modes, setModes] = useState<AccessMode[]>(["ReadWriteOnce"]);
  const [storageClassName, setStorageClassName] = useState("");
  const [useDefaultClass, setUseDefaultClass] = useState(true);
  const [volumeName, setVolumeName] = useState("");
  const [volumeMode, setVolumeMode] = useState(DEFAULT_OPTION);

  const valid = QUANTITY.test(capacity.trim()) && modes.length > 0;

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
    <FormSection title="申领">
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="申请容量" htmlFor="pvc-capacity" hint="Kubernetes quantity，例如 10Gi">
          <Input
            id="pvc-capacity"
            value={capacity}
            autoComplete="off"
            placeholder="10Gi"
            onChange={(event) => setCapacity(event.target.value)}
          />
        </Field>
        <Field label="卷模式" htmlFor="pvc-volume-mode">
          <Select value={volumeMode} onValueChange={setVolumeMode}>
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
        <Field label="绑定到指定卷" htmlFor="pvc-volume-name" hint="留空表示由 Kubernetes 选择">
          <Input
            id="pvc-volume-name"
            value={volumeName}
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => setVolumeName(event.target.value)}
          />
        </Field>
      </div>
      <label className="mt-3 flex items-center gap-2 text-[13px]">
        <Checkbox
          checked={useDefaultClass}
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
            onChange={(event) => setStorageClassName(event.target.value)}
          />
        </div>
      )}
      <AccessModePicker modes={modes} onChange={setModes} />
    </FormSection>
  );

  return { fields, valid, build };
}

function useStorageClassDraft(): SpecEditor {
  const [provisioner, setProvisioner] = useState("");
  const [reclaim, setReclaim] = useState(DEFAULT_OPTION);
  const [bindingMode, setBindingMode] = useState(DEFAULT_OPTION);
  const [allowExpansion, setAllowExpansion] = useState(false);
  const [parameters, setParameters] = useState<PairDraft[]>([]);
  const parameterKeys = parameters.map((pair) => pair.key.trim()).filter(Boolean);
  const duplicateParameter = new Set(parameterKeys).size !== parameterKeys.length;

  const valid = provisioner.trim() !== "" && !duplicateParameter;

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
      <FormSection title="StorageClass">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Provisioner" htmlFor="sc-provisioner">
            <Input
              id="sc-provisioner"
              value={provisioner}
              autoComplete="off"
              spellCheck={false}
              placeholder="例如 ebs.csi.aws.com"
              onChange={(event) => setProvisioner(event.target.value)}
            />
          </Field>
          <Field label="回收策略" htmlFor="sc-reclaim">
            <Select value={reclaim} onValueChange={setReclaim}>
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
            <Select value={bindingMode} onValueChange={setBindingMode}>
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
      </FormSection>

      <FormSection title="参数" hint="Provisioner 自定义的键值对">
        <PairList rows={parameters} onChange={setParameters} />
        {duplicateParameter ? (
          <Alert tone="warning" className="mt-2">
            参数键不能重复；请合并或删除重复项。
          </Alert>
        ) : null}
      </FormSection>
    </>
  );

  return { fields, valid, build };
}

/**
 * Access modes are a set, not a choice: a volume may allow several. Checkboxes
 * say that; a dropdown would not.
 */
function AccessModePicker({
  modes,
  onChange,
}: {
  modes: AccessMode[];
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
    <div className="grid content-start gap-1.5">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint ? <span className="text-subtle-foreground text-xs">{hint}</span> : null}
    </div>
  );
}

function PairList({
  rows,
  onChange,
}: {
  rows: PairDraft[];
  onChange: (rows: PairDraft[]) => void;
}) {
  const update = (index: number, patch: Partial<PairDraft>) =>
    onChange(rows.map((row, position) => (position === index ? { ...row, ...patch } : row)));

  return (
    <div className="grid gap-2">
      {rows.map((row, index) => (
        <div key={index} className="grid grid-cols-[1fr_1fr_auto] items-center gap-2">
          <Input
            value={row.key}
            aria-label={`参数 ${index + 1} 键`}
            placeholder="键"
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => update(index, { key: event.target.value })}
          />
          <Input
            value={row.value}
            aria-label={`参数 ${index + 1} 值`}
            placeholder="值"
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => update(index, { value: event.target.value })}
          />
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label={`移除参数 ${index + 1}`}
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
          添加参数
        </Button>
      </div>
    </div>
  );
}
