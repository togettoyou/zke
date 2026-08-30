import { useState } from "react";

import {
  useCreateMetricsSavedQuery,
  useUpdateMetricsSavedQuery,
} from "@/api/queries/observability";
import type { MetricsSavedQuery, MetricsSavedQueryVisibility } from "@/api/types";
import { ErrorAlert } from "@/components/common/error-alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input, Textarea } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

import { CLUSTER_SCOPE_LABEL } from "./metricsql";

export type SaveQueryDraft = {
  /** Present when an existing entry is being edited rather than a new one saved. */
  existing?: MetricsSavedQuery;
  expression: string;
};

/**
 * Naming an expression so it can be found again, and deciding who else sees it.
 *
 * A dialog rather than a page: three fields, no list, nothing to scroll, and
 * the operator is in the middle of writing a query they want to come back to.
 *
 * Sharing is a separate permission, so the choice is offered but disabled
 * without it, with the reason on the option itself. Hiding it would leave an
 * operator wondering why their colleague has a shared list and they do not.
 */
export function SaveQueryDialog({
  projectId,
  draft,
  canShare,
  onClose,
}: {
  projectId: string;
  draft: SaveQueryDraft | null;
  canShare: boolean;
  onClose: () => void;
}) {
  const create = useCreateMetricsSavedQuery(projectId);
  const update = useUpdateMetricsSavedQuery(projectId);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [visibility, setVisibility] = useState<MetricsSavedQueryVisibility>("private");

  const editing = draft?.existing;
  // Seeded from the draft rather than keeping whatever the previous open left
  // behind: the dialog is reused for "save this" and "edit that", and a name
  // carried between the two would be saved onto the wrong entry.
  //
  // Adjusted during render rather than from an effect. Each open passes a new
  // draft object, so this runs exactly once per open, and the first paint
  // already shows the right values — an effect would show the previous entry's
  // name for a frame.
  const [seeded, setSeeded] = useState<SaveQueryDraft | null>(null);
  if (draft && draft !== seeded) {
    setSeeded(draft);
    setName(draft.existing?.name ?? "");
    setDescription(draft.existing?.description ?? "");
    setVisibility(draft.existing?.visibility ?? "private");
  }

  const pending = create.isPending || update.isPending;
  const error = create.error ?? update.error;
  const expression = draft?.expression ?? "";
  const submittable = name.trim() !== "" && expression.trim() !== "" && !pending;

  const submit = () => {
    if (!submittable) {
      return;
    }
    const body = {
      name: name.trim(),
      description: description.trim(),
      expression: expression.trim(),
      visibility,
    };
    const done = { onSuccess: () => onClose() };
    if (editing) {
      update.mutate({ id: editing.id, body }, done);
      return;
    }
    create.mutate(body, done);
  };

  return (
    <Dialog open={draft !== null} onOpenChange={(open) => (open ? undefined : onClose())}>
      <DialogContent className="w-[min(560px,calc(100vw-2rem))]">
        <DialogHeader>
          <DialogTitle>{editing ? "编辑保存的查询" : "保存查询"}</DialogTitle>
          <DialogDescription>
            只保存表达式本身。运行时读哪个集群，由当时选定的目标集群决定，Server
            会把它写进表达式的每一个选择器 ——所以共享一条表达式不会共享任何集群的数据。
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="saved-query-name">名称</Label>
            <Input
              id="saved-query-name"
              value={name}
              maxLength={60}
              autoFocus
              placeholder="例如：节点内存工作集"
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="saved-query-description">说明（可选）</Label>
            <Input
              id="saved-query-description"
              value={description}
              maxLength={160}
              placeholder="这条表达式回答的问题"
              onChange={(event) => setDescription(event.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="saved-query-visibility">可见范围</Label>
            <Select
              value={visibility}
              onValueChange={(value) => setVisibility(value as MetricsSavedQueryVisibility)}
            >
              <SelectTrigger id="saved-query-visibility" className="text-[13px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="private">仅自己可见</SelectItem>
                <SelectItem value="project" disabled={!canShare}>
                  项目内共享
                  {canShare ? "" : "（需要采集组件管理权限）"}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="saved-query-expression">表达式</Label>
            <Textarea
              id="saved-query-expression"
              value={expression}
              readOnly
              rows={3}
              className="zke-mono min-h-0 text-xs"
            />
            <p className="text-subtle-foreground text-[11px]">
              表达式按书写内容保存。其中的 <code className="zke-mono">{CLUSTER_SCOPE_LABEL}</code>{" "}
              条件会在每次执行时被替换为当前目标集群。
            </p>
          </div>
          {error ? <ErrorAlert error={error} /> : null}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose} disabled={pending}>
              取消
            </Button>
            <Button type="submit" disabled={!submittable}>
              {pending ? "保存中…" : "保存"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
