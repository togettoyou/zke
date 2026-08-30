import { useMemo, useState } from "react";
import { BookMarked, Pencil, Trash2, Users } from "lucide-react";

import { useDeleteMetricsSavedQuery, useMetricsSavedQueries } from "@/api/queries/observability";
import type { MetricsSavedQuery } from "@/api/types";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { HintTooltip } from "@/components/ui/tooltip";

import type { SaveQueryDraft } from "./SaveQueryDialog";

/**
 * Beyond this the list stops being a picker and starts being a search problem,
 * so the filter appears. Below it the field would be one more control between
 * the operator and eight rows they can already see.
 */
const FILTER_THRESHOLD = 6;

/**
 * The Project's saved expressions, as somewhere to pick one from.
 *
 * A popover rather than a section of its own: choosing a saved query is
 * something done while writing one, and sending the operator to another screen
 * would lose whatever is in the editor behind it. Managing an entry happens
 * from the same list, because the moment somebody notices a name is wrong is
 * the moment they are reading it.
 */
export function SavedQueryLibrary({
  projectId,
  currentExpression,
  onInsert,
  onEdit,
}: {
  projectId: string;
  /** What "save the current expression" would save, or empty when there is none. */
  currentExpression: string;
  onInsert: (expression: string) => void;
  onEdit: (draft: SaveQueryDraft) => void;
}) {
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  const [pendingDelete, setPendingDelete] = useState<MetricsSavedQuery | null>(null);
  // Loaded with the section rather than on first open: the count on the trigger
  // has to be true before anybody clicks it, and one cached request per Project
  // is cheaper than a popover that opens onto a spinner every time.
  const saved = useMetricsSavedQueries(projectId || null);
  const remove = useDeleteMetricsSavedQuery(projectId);

  // Memoised because it is a dependency below and `?? []` would otherwise be a
  // new array on every render, re-filtering the whole list each time.
  const entries = useMemo(() => saved.data?.queries ?? [], [saved.data]);
  const shown = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    if (!needle) {
      return entries;
    }
    return entries.filter(
      (entry) =>
        entry.name.toLowerCase().includes(needle) ||
        entry.description.toLowerCase().includes(needle) ||
        entry.expression.toLowerCase().includes(needle),
    );
  }, [entries, filter]);

  return (
    <>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button variant="secondary" size="sm" className="gap-1.5">
            <BookMarked />
            保存的查询
            {entries.length > 0 ? (
              <span className="text-subtle-foreground zke-tnum">{entries.length}</span>
            ) : null}
          </Button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-[min(30rem,calc(100vw-2rem))] p-0">
          <div className="border-border flex items-center justify-between gap-2 border-b px-3 py-2">
            <p className="text-foreground text-[13px] font-medium">保存的查询</p>
            <Button
              size="sm"
              variant="ghost"
              disabled={currentExpression.trim() === ""}
              onClick={() => {
                setOpen(false);
                onEdit({ expression: currentExpression });
              }}
            >
              保存当前表达式
            </Button>
          </div>
          {entries.length >= FILTER_THRESHOLD ? (
            <div className="border-border border-b px-3 py-2">
              <Input
                value={filter}
                placeholder="按名称、说明或表达式筛选"
                aria-label="筛选保存的查询"
                className="h-8 text-[13px]"
                onChange={(event) => setFilter(event.target.value)}
              />
            </div>
          ) : null}
          <div className="max-h-[22rem] overflow-y-auto">
            {saved.isPending ? <LoadingState /> : null}
            {saved.error ? (
              <ErrorState error={saved.error} onRetry={() => void saved.refetch()} />
            ) : null}
            {!saved.isPending && !saved.error && shown.length === 0 ? (
              <EmptyState
                title={entries.length === 0 ? "还没有保存的查询" : "没有匹配的查询"}
                description={
                  entries.length === 0
                    ? "写好一条表达式后，用行末的书签按钮把它存下来，下次直接从这里选。"
                    : undefined
                }
              />
            ) : null}
            <ul>
              {shown.map((entry) => (
                <li key={entry.id} className="border-border border-b last:border-b-0">
                  <div className="hover:bg-surface-muted flex items-start gap-2 px-2 py-2 transition-colors">
                    <button
                      type="button"
                      className="zke-focus rounded-control min-w-0 flex-1 px-1 py-0.5 text-left"
                      onClick={() => {
                        onInsert(entry.expression);
                        setOpen(false);
                      }}
                    >
                      <span className="flex flex-wrap items-center gap-1.5">
                        <span className="text-foreground truncate text-[13px] font-medium">
                          {entry.name}
                        </span>
                        {entry.visibility === "project" ? (
                          <Badge tone="info" className="gap-1">
                            <Users className="size-3" aria-hidden />
                            项目共享
                          </Badge>
                        ) : null}
                      </span>
                      {entry.description ? (
                        <span className="text-muted-foreground mt-0.5 block truncate text-xs">
                          {entry.description}
                        </span>
                      ) : null}
                      <span className="zke-mono text-subtle-foreground mt-1 block truncate text-[11px]">
                        {entry.expression}
                      </span>
                      {entry.visibility === "project" ? (
                        <span className="text-subtle-foreground mt-0.5 block text-[11px]">
                          由 {entry.owner_display_name || "已删除的用户"} 共享
                        </span>
                      ) : null}
                    </button>
                    {/* Always visible rather than revealed on hover: on a
                        touch device there is no hover, and an entry that can
                        only be renamed with a mouse cannot be renamed. */}
                    <div className="flex shrink-0 items-center gap-0.5">
                      <HintTooltip label={entry.editable ? "编辑" : "没有权限修改这条共享查询"}>
                        <span>
                          <Button
                            size="icon-sm"
                            variant="ghost"
                            aria-label={`编辑 ${entry.name}`}
                            disabled={!entry.editable}
                            onClick={() => {
                              setOpen(false);
                              onEdit({ existing: entry, expression: entry.expression });
                            }}
                          >
                            <Pencil />
                          </Button>
                        </span>
                      </HintTooltip>
                      <HintTooltip label={entry.editable ? "删除" : "没有权限删除这条共享查询"}>
                        <span>
                          <Button
                            size="icon-sm"
                            variant="ghost"
                            className="text-danger hover:text-danger"
                            aria-label={`删除 ${entry.name}`}
                            disabled={!entry.editable}
                            onClick={() => {
                              setOpen(false);
                              setPendingDelete(entry);
                            }}
                          >
                            <Trash2 />
                          </Button>
                        </span>
                      </HintTooltip>
                    </div>
                  </div>
                </li>
              ))}
            </ul>
          </div>
          {saved.data ? (
            <p className="text-subtle-foreground border-border border-t px-3 py-1.5 text-[11px]">
              该项目最多保存 {saved.data.limit} 条
            </p>
          ) : null}
        </PopoverContent>
      </Popover>

      <SensitiveActionDialog
        open={pendingDelete !== null}
        onOpenChange={(next) => (next ? undefined : setPendingDelete(null))}
        title="删除保存的查询"
        description="表达式本身会被删除，已经画出来的图表不受影响。"
        destructive
        scopeLines={[
          { label: "名称", name: pendingDelete?.name ?? "" },
          {
            label: "可见范围",
            name: pendingDelete?.visibility === "project" ? "项目内共享" : "仅自己可见",
          },
        ]}
        impacts={
          pendingDelete?.visibility === "project"
            ? ["项目内所有人都将无法再从选择器中找到它。"]
            : ["它将从你的选择器中移除。"]
        }
        confirmLabel="删除"
        pending={remove.isPending}
        error={remove.error}
        onConfirm={() => {
          if (!pendingDelete) {
            return;
          }
          remove.mutate(pendingDelete.id, { onSuccess: () => setPendingDelete(null) });
        }}
      />
    </>
  );
}
