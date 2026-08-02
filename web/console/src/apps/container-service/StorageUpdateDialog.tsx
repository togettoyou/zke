import { useState } from "react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import { useUpdateStorageResource, type StorageUpdateSpec } from "@/api/queries/storage";
import type { KubernetesStorageResource, KubernetesStorageResourceSummary } from "@/api/types";
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

import { storageKindLabel } from "./storage-catalog";

const QUANTITY = /^\d+(\.\d+)?(Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E|m)?$/;

/**
 * Changes the one field each storage type allows changing.
 *
 * There is no general edit form here because there is no general edit:
 * This dialog deliberately exposes exactly the three focused mutations the
 * typed Server API accepts. Advanced fields remain available through YAML.
 */
export function StorageUpdateDialog({
  clusterId,
  clusterName,
  namespace,
  resource,
  target,
  onClose,
}: {
  clusterId: string;
  clusterName: string;
  namespace: string;
  resource: KubernetesStorageResource;
  target: KubernetesStorageResourceSummary;
  onClose: () => void;
}) {
  const update = useUpdateStorageResource();
  const kind = storageKindLabel(resource);
  const [previewed, setPreviewed] = useState<StorageUpdateSpec | null>(null);
  const previewKey = useSubmissionKey(previewed === null);
  const applyKey = useSubmissionKey(previewed !== null);

  const currentCapacity = target.persistent_volume_claim?.requested_capacity ?? "";
  const [reclaim, setReclaim] = useState(
    target.persistent_volume?.reclaim_policy === "Delete" ? "Delete" : "Retain",
  );
  const [capacity, setCapacity] = useState(currentCapacity);
  const [allowExpansion, setAllowExpansion] = useState(
    target.storage_class?.allow_volume_expansion ?? false,
  );

  const build = (): StorageUpdateSpec => {
    if (resource === "persistentvolumes") {
      return { persistent_volume: { reclaim_policy: reclaim as "Retain" | "Delete" } };
    }
    if (resource === "persistentvolumeclaims") {
      return { persistent_volume_claim: { requested_capacity: capacity.trim() } };
    }
    return { storage_class: { allow_volume_expansion: allowExpansion } };
  };

  const valid =
    resource === "persistentvolumeclaims"
      ? QUANTITY.test(capacity.trim()) && capacity.trim() !== currentCapacity
      : resource === "persistentvolumes"
        ? reclaim !== (target.persistent_volume?.reclaim_policy ?? "")
        : allowExpansion !== (target.storage_class?.allow_volume_expansion ?? false);

  const submit = (dryRun: boolean, spec: StorageUpdateSpec) =>
    void update
      .mutateAsync({
        clusterId,
        namespace,
        resource,
        name: target.name,
        uid: target.uid,
        resourceVersion: target.resource_version,
        spec,
        dryRun,
        idempotencyKey: dryRun ? previewKey : applyKey,
      })
      .then(() => {
        if (dryRun) {
          setPreviewed(spec);
          return;
        }
        toast.success(`${kind} ${target.name} 已更新`);
        onClose();
      })
      .catch(() => undefined);

  return (
    <>
      <Dialog open={previewed === null} onOpenChange={(open) => !open && onClose()}>
        <DialogContent aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>
              编辑 {kind} · {target.name}
            </DialogTitle>
            <DialogDescription>第一步只执行服务端 DryRun，不会在集群中写入变更。</DialogDescription>
          </DialogHeader>

          {resource === "persistentvolumes" ? (
            <div className="grid gap-1.5">
              <Label htmlFor="storage-reclaim">回收策略</Label>
              <Select value={reclaim} onValueChange={setReclaim}>
                <SelectTrigger id="storage-reclaim">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="Retain">Retain（删除 PV 时保留数据）</SelectItem>
                  <SelectItem value="Delete">Delete（删除 PV 时销毁数据）</SelectItem>
                </SelectContent>
              </Select>
              <span className="text-subtle-foreground text-xs">
                当前：{target.persistent_volume?.reclaim_policy || "—"}。这是 PV
                类型化接口只开放这一字段。
              </span>
            </div>
          ) : null}

          {resource === "persistentvolumeclaims" ? (
            <div className="grid gap-1.5">
              <Label htmlFor="storage-capacity">申请容量</Label>
              <Input
                id="storage-capacity"
                value={capacity}
                autoComplete="off"
                placeholder="例如 20Gi"
                onChange={(event) => setCapacity(event.target.value)}
              />
              <span className="text-subtle-foreground text-xs">
                当前申请 {currentCapacity || "—"}
                {target.persistent_volume_claim?.capacity
                  ? `，已分配 ${target.persistent_volume_claim.capacity}`
                  : ""}
                。只能增大：Kubernetes 不支持缩容，且需要 StorageClass 允许扩容。
              </span>
            </div>
          ) : null}

          {resource === "storageclasses" ? (
            <div className="grid gap-1.5">
              <label className="flex items-center gap-2 text-[13px]">
                <Checkbox
                  checked={allowExpansion}
                  onCheckedChange={(checked) => setAllowExpansion(checked === true)}
                />
                允许卷扩容
              </label>
              <span className="text-subtle-foreground text-xs">
                类型化接口只开放这一字段；其他高级变更请使用 YAML，并接受 Kubernetes 服务端校验。
              </span>
            </div>
          ) : null}

          {update.error ? (
            <Alert tone="danger" className="mt-3">
              {errorMessage(update.error)}
            </Alert>
          ) : null}

          <DialogFooter>
            <Button variant="ghost" onClick={onClose} disabled={update.isPending}>
              取消
            </Button>
            <Button
              variant="primary"
              disabled={!valid || update.isPending}
              onClick={() => submit(true, build())}
            >
              {update.isPending ? "预检中…" : "执行 DryRun 预检"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <SensitiveActionDialog
        open={previewed !== null}
        onOpenChange={(open) => !open && setPreviewed(null)}
        title={`确认更新 ${kind}`}
        description="DryRun 已通过。确认后将向同一集群提交实际变更。"
        scopeLines={[
          { label: "集群", name: clusterName, id: clusterId },
          ...(resource === "persistentvolumeclaims"
            ? [{ label: "命名空间", name: namespace }]
            : []),
          { label: kind, name: target.name, id: target.uid },
        ]}
        impacts={updateImpacts(resource, {
          reclaim,
          capacity: capacity.trim(),
          allowExpansion,
        })}
        confirmLabel="确认更新"
        destructive={resource === "persistentvolumes" && reclaim === "Delete"}
        pending={update.isPending}
        error={update.error}
        onConfirm={() => previewed && submit(false, previewed)}
      />
    </>
  );
}

function updateImpacts(
  resource: KubernetesStorageResource,
  values: { reclaim: string; capacity: string; allowExpansion: boolean },
): string[] {
  const precondition =
    "请求携带该对象当前的 UID 与 resourceVersion，期间对象若已变化，更新会被拒绝而不是覆盖。";
  if (resource === "persistentvolumes") {
    return [
      values.reclaim === "Delete"
        ? "改为 Delete 后，删除该 PV 或其绑定的 PVC 时，底层存储和其中的数据会被一并销毁。"
        : "改为 Retain 后，PV 被释放时数据会保留，但需要管理员手动清理或重新绑定。",
      precondition,
    ];
  }
  if (resource === "persistentvolumeclaims") {
    return [
      `申请容量将调整为 ${values.capacity}。扩容由 StorageClass 和 CSI 驱动执行，可能需要一段时间才反映到已分配容量。`,
      "部分驱动要求 Pod 重启后文件系统才会扩展到新的容量。",
      precondition,
    ];
  }
  return [
    values.allowExpansion
      ? "之后由该 StorageClass 制备的 PVC 将允许扩容。"
      : "关闭后，引用该 StorageClass 的 PVC 将不能再扩容。",
    "已有的 PV 和 PVC 不受影响。",
    precondition,
  ];
}
