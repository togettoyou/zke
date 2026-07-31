import { useState } from "react";
import { MoreHorizontal, Pause, Play, RotateCw, Scaling, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { errorMessage } from "@/api/errors";
import {
  useDeleteWorkload,
  useRestartWorkload,
  useScaleWorkload,
  useSetCronJobSuspension,
} from "@/api/queries/workloads";
import type { KubernetesWorkloadSummary } from "@/api/types";
import { SensitiveActionDialog, type ScopeLine } from "@/components/common/sensitive-action-dialog";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert } from "@/components/ui/misc";
import { useSubmissionKey } from "@/lib/use-submission-key";

import { kindLabel, supportsRestart, supportsScale, supportsSuspension } from "./workload-catalog";

/** The exact object an action will act on, and where it lives. */
export type WorkloadTarget = {
  clusterId: string;
  clusterName: string;
  namespace: string;
  workload: KubernetesWorkloadSummary;
};

type WorkloadAction = "scale" | "restart" | "suspension" | "delete";
type ActiveWorkloadAction = {
  type: WorkloadAction;
  // Freeze the object the operator chose. A background refetch must not replace
  // its UID or CronJob state between DryRun and the confirmed write.
  target: WorkloadTarget;
};

/**
 * The mutations a workload offers, and the dialogs behind them.
 *
 * Every action is a two-step: the first submission is a Kubernetes server-side
 * DryRun, and only after it passes can the operator confirm the real write. The
 * dialogs mount when an action is chosen and unmount when it closes, so the
 * mutation state and the idempotency keys belong to one attempt and cannot leak
 * into the next one.
 *
 * A table row collapses them into an overflow menu — fifty rows of buttons is a
 * wall — while a detail view spells them out. There is one control on a detail
 * page, and putting the actions it exists for behind an unlabelled affordance
 * buys nothing back.
 */
export function WorkloadActions({
  target,
  canUpdate,
  canDelete,
  variant = "menu",
  onDeleted,
}: {
  target: WorkloadTarget;
  canUpdate: boolean;
  canDelete: boolean;
  variant?: "menu" | "buttons";
  /** Called after a real deletion, so a detail view can leave the object it was showing. */
  onDeleted?: () => void;
}) {
  const [action, setAction] = useState<ActiveWorkloadAction | null>(null);
  const { resource, name, uid } = target.workload;
  const openAction = (type: WorkloadAction) => setAction({ type, target });

  const scalable = canUpdate && supportsScale(resource);
  const restartable = canUpdate && supportsRestart(resource);
  const suspendable = canUpdate && supportsSuspension(resource);
  // The endpoint requires a UID precondition, so an object that somehow arrived
  // without one has no deletion this Console can safely submit.
  const removable = canDelete && Boolean(uid);

  if (!scalable && !restartable && !suspendable && !removable) {
    return null;
  }

  const suspended = target.workload.cron_job?.suspend ?? false;

  return (
    <>
      {variant === "menu" ? (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button size="icon-sm" variant="ghost" aria-label={`${name} 的操作`}>
              <MoreHorizontal />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-40">
            {scalable ? (
              <DropdownMenuItem onSelect={() => openAction("scale")}>伸缩</DropdownMenuItem>
            ) : null}
            {restartable ? (
              <DropdownMenuItem onSelect={() => openAction("restart")}>滚动重启</DropdownMenuItem>
            ) : null}
            {suspendable ? (
              <DropdownMenuItem onSelect={() => openAction("suspension")}>
                {suspended ? "恢复" : "暂停"}
              </DropdownMenuItem>
            ) : null}
            {removable ? (
              <>
                {scalable || restartable || suspendable ? <DropdownMenuSeparator /> : null}
                <DropdownMenuItem variant="danger" onSelect={() => openAction("delete")}>
                  删除
                </DropdownMenuItem>
              </>
            ) : null}
          </DropdownMenuContent>
        </DropdownMenu>
      ) : (
        <>
          {scalable ? (
            <Button size="sm" variant="secondary" onClick={() => openAction("scale")}>
              <Scaling />
              伸缩
            </Button>
          ) : null}
          {restartable ? (
            <Button size="sm" variant="secondary" onClick={() => openAction("restart")}>
              <RotateCw />
              滚动重启
            </Button>
          ) : null}
          {suspendable ? (
            <Button size="sm" variant="secondary" onClick={() => openAction("suspension")}>
              {suspended ? <Play /> : <Pause />}
              {suspended ? "恢复" : "暂停"}
            </Button>
          ) : null}
          {removable ? (
            // Tinted rather than solid: a filled danger button in a page header
            // is louder than the header, and the confirmation dialog is where
            // this operation is actually guarded.
            <Button
              size="sm"
              variant="secondary"
              className="text-danger"
              onClick={() => openAction("delete")}
            >
              <Trash2 />
              删除
            </Button>
          ) : null}
        </>
      )}

      {action?.type === "scale" ? (
        <ScaleDialog target={action.target} onClose={() => setAction(null)} />
      ) : null}
      {action?.type === "restart" ? (
        <RestartDialog target={action.target} onClose={() => setAction(null)} />
      ) : null}
      {action?.type === "suspension" ? (
        <SuspensionDialog target={action.target} onClose={() => setAction(null)} />
      ) : null}
      {action?.type === "delete" ? (
        <DeleteDialog
          target={action.target}
          onClose={() => setAction(null)}
          onDeleted={onDeleted}
        />
      ) : null}
    </>
  );
}

/** The target lines every one of these dialogs opens with. */
function scopeLines(target: WorkloadTarget): ScopeLine[] {
  return [
    { label: "集群", name: target.clusterName, id: target.clusterId },
    { label: "命名空间", name: target.namespace },
    {
      label: kindLabel(target.workload.resource),
      name: target.workload.name,
      id: target.workload.uid,
    },
  ];
}

/**
 * Scaling asks for the replica count first, then previews that exact number.
 *
 * The confirmed request replays the number the DryRun validated rather than
 * whatever the field holds at confirmation time — otherwise the preview and the
 * write could disagree about what was checked.
 */
function ScaleDialog({ target, onClose }: { target: WorkloadTarget; onClose: () => void }) {
  const scale = useScaleWorkload();
  const current = target.workload.replicas?.desired ?? 0;
  const [value, setValue] = useState(String(current));
  const [previewed, setPreviewed] = useState<number | null>(null);
  const previewKey = useSubmissionKey(true);
  const applyKey = useSubmissionKey(true);

  // `spec.replicas` is an int32, so a value the field cannot hold is rejected
  // here rather than sent to be refused as a malformed patch.
  const valid = /^\d+$/.test(value.trim()) && Number(value.trim()) <= 2_147_483_647;
  const requested = valid ? Number(value.trim()) : current;

  const submit = (dryRun: boolean, replicas: number) =>
    void scale
      .mutateAsync({
        clusterId: target.clusterId,
        namespace: target.namespace,
        resource: target.workload.resource,
        name: target.workload.name,
        replicas,
        dryRun,
        idempotencyKey: dryRun ? previewKey : applyKey,
      })
      .then(() => {
        if (dryRun) {
          setPreviewed(replicas);
          return;
        }
        toast.success(`${target.workload.name} 副本数已调整为 ${replicas}`);
        onClose();
      })
      .catch(() => undefined);

  return (
    <>
      <Dialog open={previewed === null} onOpenChange={(open) => !open && onClose()}>
        <DialogContent aria-describedby={undefined}>
          <DialogHeader>
            <DialogTitle>伸缩 {kindLabel(target.workload.resource)}</DialogTitle>
            <DialogDescription>第一步只执行服务端 DryRun，不会真正改变副本数。</DialogDescription>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="workload-replicas">目标副本数</Label>
            <Input
              id="workload-replicas"
              value={value}
              inputMode="numeric"
              autoComplete="off"
              spellCheck={false}
              onChange={(event) => setValue(event.target.value)}
            />
            <span className="text-subtle-foreground text-xs">当前期望副本数：{current}</span>
          </div>
          <Alert tone="info" className="mt-3">
            目标：{target.clusterName} / {target.namespace} / {target.workload.name}
          </Alert>
          {scale.error ? (
            <Alert tone="danger" className="mt-3">
              {errorMessage(scale.error)}
            </Alert>
          ) : null}
          <DialogFooter>
            <Button variant="ghost" onClick={onClose} disabled={scale.isPending}>
              取消
            </Button>
            <Button
              variant="primary"
              disabled={!valid || scale.isPending}
              onClick={() => submit(true, requested)}
            >
              {scale.isPending ? "预检中…" : "执行 DryRun 预检"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <SensitiveActionDialog
        open={previewed !== null}
        onOpenChange={(open) => !open && onClose()}
        title="确认伸缩"
        description="DryRun 已通过。确认后将向同一集群提交实际变更。"
        scopeLines={scopeLines(target)}
        impacts={[
          `期望副本数将从 ${current} 调整为 ${previewed ?? current}。`,
          (previewed ?? current) < current
            ? "控制器会终止多出的 Pod，它们正在处理的请求可能被中断。"
            : "控制器会按更新策略创建新的 Pod，调度取决于集群当前可用资源。",
        ]}
        confirmLabel="确认伸缩"
        destructive={(previewed ?? current) < current}
        pending={scale.isPending}
        error={scale.error}
        onConfirm={() => submit(false, previewed ?? current)}
      />
    </>
  );
}

function RestartDialog({ target, onClose }: { target: WorkloadTarget; onClose: () => void }) {
  const restart = useRestartWorkload();
  const [previewed, setPreviewed] = useState(false);
  const previewKey = useSubmissionKey(true);
  const applyKey = useSubmissionKey(true);

  return (
    <SensitiveActionDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={`滚动重启 ${kindLabel(target.workload.resource)}`}
      description={
        previewed
          ? "DryRun 已通过。再次确认将提交实际重启。"
          : "首次点击只执行服务端 DryRun；预检通过后才会真正触发重启。"
      }
      scopeLines={scopeLines(target)}
      impacts={[
        "控制器将按该工作负载的更新策略滚动替换它的全部 Pod。",
        "重启标记由本次提交的幂等键派生，因此重试同一次提交不会触发第二轮滚动。",
      ]}
      confirmLabel={previewed ? "确认重启" : "执行 DryRun 预检"}
      destructive
      pending={restart.isPending}
      error={restart.error}
      onConfirm={() => {
        const dryRun = !previewed;
        void restart
          .mutateAsync({
            clusterId: target.clusterId,
            namespace: target.namespace,
            resource: target.workload.resource,
            name: target.workload.name,
            dryRun,
            idempotencyKey: dryRun ? previewKey : applyKey,
          })
          .then(() => {
            if (dryRun) {
              setPreviewed(true);
              toast.success("滚动重启 DryRun 已通过");
              return;
            }
            toast.success(`${target.workload.name} 已提交滚动重启`);
            onClose();
          })
          .catch(() => undefined);
      }}
    />
  );
}

function SuspensionDialog({ target, onClose }: { target: WorkloadTarget; onClose: () => void }) {
  const setSuspension = useSetCronJobSuspension();
  const [previewed, setPreviewed] = useState(false);
  const previewKey = useSubmissionKey(true);
  const applyKey = useSubmissionKey(true);
  // The action is the opposite of the state the CronJob is in.
  const suspending = !(target.workload.cron_job?.suspend ?? false);

  return (
    <SensitiveActionDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={suspending ? "暂停 CronJob 调度" : "恢复 CronJob 调度"}
      description={
        previewed
          ? "DryRun 已通过。再次确认将提交实际变更。"
          : "首次点击只执行服务端 DryRun；预检通过后才会真正修改 CronJob。"
      }
      scopeLines={scopeLines(target)}
      impacts={
        suspending
          ? [
              "调度器将停止按 schedule 表达式创建新的 Job。",
              "已经在运行的 Job 不受影响，也不会被取消。",
            ]
          : [
              "调度器将按 schedule 表达式重新创建 Job。",
              "暂停期间错过的调度可能在恢复后立即补跑，具体取决于 startingDeadlineSeconds 和 missed schedule 限制。",
            ]
      }
      confirmLabel={previewed ? "确认执行" : "执行 DryRun 预检"}
      destructive={suspending}
      pending={setSuspension.isPending}
      error={setSuspension.error}
      onConfirm={() => {
        const dryRun = !previewed;
        void setSuspension
          .mutateAsync({
            clusterId: target.clusterId,
            namespace: target.namespace,
            resource: target.workload.resource,
            name: target.workload.name,
            suspended: suspending,
            dryRun,
            idempotencyKey: dryRun ? previewKey : applyKey,
          })
          .then(() => {
            if (dryRun) {
              setPreviewed(true);
              toast.success("CronJob 变更 DryRun 已通过");
              return;
            }
            toast.success(
              suspending
                ? `${target.workload.name} 已暂停调度`
                : `${target.workload.name} 已恢复调度`,
            );
            onClose();
          })
          .catch(() => undefined);
      }}
    />
  );
}

function DeleteDialog({
  target,
  onClose,
  onDeleted,
}: {
  target: WorkloadTarget;
  onClose: () => void;
  onDeleted?: () => void;
}) {
  const remove = useDeleteWorkload();
  const [previewed, setPreviewed] = useState(false);
  const previewKey = useSubmissionKey(true);
  const applyKey = useSubmissionKey(true);
  const kind = kindLabel(target.workload.resource);

  return (
    <SensitiveActionDialog
      open
      onOpenChange={(open) => !open && onClose()}
      title={`删除 ${kind}`}
      description={
        previewed
          ? "DryRun 已通过。再次确认将提交实际删除。"
          : "首次点击只执行服务端 DryRun；预检通过后才能实际删除。"
      }
      scopeLines={scopeLines(target)}
      impacts={[
        `将删除该 ${kind}，它管理的 Pod 会被一并清理。`,
        "请求携带该对象当前的 UID 前置条件，避免误删同名重建的对象。",
      ]}
      confirmationText={previewed ? target.workload.name : undefined}
      confirmLabel={previewed ? "确认删除" : "执行 DryRun 预检"}
      destructive
      pending={remove.isPending}
      error={remove.error}
      onConfirm={() => {
        const dryRun = !previewed;
        void remove
          .mutateAsync({
            clusterId: target.clusterId,
            namespace: target.namespace,
            resource: target.workload.resource,
            name: target.workload.name,
            uid: target.workload.uid,
            dryRun,
            idempotencyKey: dryRun ? previewKey : applyKey,
          })
          .then(() => {
            if (dryRun) {
              setPreviewed(true);
              toast.success(`${kind} 删除 DryRun 已通过`);
              return;
            }
            toast.success(`${kind} ${target.workload.name} 已提交删除`);
            onDeleted?.();
            onClose();
          })
          .catch(() => undefined);
      }}
    />
  );
}
