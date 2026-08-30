import { useLayoutEffect, useRef } from "react";
import { AlertTriangle, BookmarkPlus, Eraser, Eye, EyeOff, Play, Plus, Trash2 } from "lucide-react";

import type { MetricsExploreOutcome } from "@/api/types";
import { Button } from "@/components/ui/button";
import { HintTooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/cn";

import { SegmentedTabs } from "../MetricsViews";
import type { SaveQueryDraft } from "./SaveQueryDialog";
import { useExplore, type ExploreKind, type ExpressionRow } from "./explore-context";
import { MAX_EXPRESSIONS } from "./metricsql";

const KIND_CHOICES: readonly { id: ExploreKind; label: string }[] = [
  { id: "range", label: "区间" },
  { id: "instant", label: "瞬时" },
];

/** How tall one expression box may grow before it starts scrolling instead. */
const MAX_FIELD_HEIGHT = 200;

/**
 * The expression rows and the controls that run them.
 *
 * Rows rather than one box holding several lines: each expression is answered,
 * timed, hidden and saved on its own, and all four of those need somewhere to
 * live beside the text they belong to.
 */
export function ExpressionEditor({ onSave }: { onSave: (draft: SaveQueryDraft) => void }) {
  const explore = useExplore();
  const { rows, kind, setKind, addRow, reset, run, runnable, running, stale, untouched, outcomes } =
    explore;

  const clearable = !untouched || rows.length > 1 || rows.some((row) => row.expression !== "");

  return (
    <section className="border-border bg-surface rounded-panel flex flex-col gap-3 border p-4">
      <div className="flex flex-col gap-2">
        {rows.map((row) => (
          <ExpressionField
            key={row.ref}
            reference={row.ref}
            expression={row.expression}
            hidden={row.hidden}
            outcome={outcomes.get(row.ref)}
            removable={rows.length > 1 || row.expression !== ""}
            onChange={(value) => explore.setExpression(row.ref, value)}
            onFocus={() => explore.focusRow(row.ref)}
            onToggleHidden={() => explore.toggleHidden(row.ref)}
            onRemove={() => explore.removeRow(row.ref)}
            onSave={() => onSave({ expression: row.expression })}
            onRun={run}
          />
        ))}
      </div>

      {/* Wraps rather than scrolls: this row sits inside a window an operator
          can drag down to a phone's width, and a toolbar that clipped its own
          primary action would put 执行查询 out of reach exactly there.

          The saved-query picker and the syntax reference used to live here and
          are now in the application toolbar: they belong to the screen rather
          than to this card, and moving them left this row holding only the
          kind of question being asked and the three actions that act on the
          expressions above it. */}
      <div className="border-border flex flex-wrap items-center justify-between gap-2 border-t pt-3">
        <SegmentedTabs
          items={KIND_CHOICES}
          activeId={kind}
          onSelect={(id) => setKind(id as ExploreKind)}
          label="查询类型"
        />
        <div className="flex flex-wrap items-center gap-2">
          <HintTooltip label="清空全部表达式与结果">
            <span>
              <Button
                variant="ghost"
                size="sm"
                className="gap-1.5"
                disabled={!clearable || running}
                onClick={reset}
              >
                <Eraser />
                清空
              </Button>
            </span>
          </HintTooltip>
          <HintTooltip
            label={
              rows.length >= MAX_EXPRESSIONS
                ? `一次最多执行 ${MAX_EXPRESSIONS} 条表达式`
                : "添加一条表达式"
            }
          >
            <span>
              <Button
                variant="secondary"
                size="sm"
                className="gap-1.5"
                disabled={rows.length >= MAX_EXPRESSIONS}
                onClick={() => addRow()}
              >
                <Plus />
                添加表达式
              </Button>
            </span>
          </HintTooltip>
          <Button size="sm" className="gap-1.5" disabled={!runnable || running} onClick={run}>
            <Play />
            {running ? "执行中…" : "执行查询"}
          </Button>
        </div>
      </div>

      {stale && !running ? (
        <p className="text-warning text-xs" role="status">
          表达式已修改，下方结果仍是上一次执行的。
        </p>
      ) : null}
      {/* Always mounted and positioned off-screen by `sr-only`, so a screen
          reader announces a failed expression when the answer arrives. A live
          region inserted at that moment may never be read at all, and the
          per-row messages above are inserted exactly then. */}
      <p aria-live="polite" aria-atomic="true" className="sr-only">
        {announcement(rows, outcomes, running)}
      </p>
    </section>
  );
}

/**
 * What a screen reader is told once a run lands.
 *
 * The count rather than the messages: several rows can fail at once, and
 * reading four parse errors aloud in sequence buries the one the operator is
 * looking at. The messages themselves are beside their own fields.
 */
function announcement(
  rows: ExpressionRow[],
  outcomes: Map<string, MetricsExploreOutcome>,
  running: boolean,
): string {
  if (running) {
    return "正在执行查询";
  }
  const failed = rows.filter((row) => outcomes.get(row.ref)?.error);
  if (failed.length === 0) {
    return "";
  }
  if (failed.length === 1) {
    return `表达式 ${failed[0]?.ref} 执行失败`;
  }
  return `${failed.length} 条表达式执行失败`;
}

/**
 * One expression, with everything that belongs to it.
 *
 * The controls sit on the label line above the field rather than beside it. A
 * column of icon buttons to the right of a text box costs about a hundred
 * pixels of writing width, and this window is regularly dragged down to four
 * hundred — which is where an expression is hardest to read, not easiest.
 */
function ExpressionField({
  reference,
  expression,
  hidden,
  outcome,
  removable,
  onChange,
  onFocus,
  onToggleHidden,
  onRemove,
  onSave,
  onRun,
}: {
  reference: string;
  expression: string;
  hidden: boolean;
  outcome: MetricsExploreOutcome | undefined;
  removable: boolean;
  onChange: (value: string) => void;
  onFocus: () => void;
  onToggleHidden: () => void;
  onRemove: () => void;
  onSave: () => void;
  onRun: () => void;
}) {
  const field = useRef<HTMLTextAreaElement | null>(null);

  // Grown to fit rather than fixed at one line: a `sum by (...)` over two rate
  // windows is three lines wide in a narrow window, and a box that hides two of
  // them makes an expression impossible to check before running it.
  useLayoutEffect(() => {
    const element = field.current;
    if (!element) {
      return;
    }
    element.style.height = "auto";
    element.style.height = `${Math.min(element.scrollHeight, MAX_FIELD_HEIGHT)}px`;
  }, [expression]);

  const fieldId = `explore-expression-${reference}`;
  const failed = outcome?.error;
  const describedBy = failed ? `${fieldId}-error` : undefined;

  return (
    <div className={cn("flex flex-col gap-1", hidden && "opacity-60")}>
      <div className="flex items-center justify-between gap-2">
        <label
          htmlFor={fieldId}
          className="text-muted-foreground flex min-w-0 items-center gap-2 text-xs"
        >
          <span className="text-foreground font-medium">表达式 {reference}</span>
          {outcome ? (
            <span className="zke-tnum text-subtle-foreground">{outcome.duration_ms} ms</span>
          ) : null}
          {hidden ? <span className="text-subtle-foreground">已隐藏</span> : null}
        </label>
        <div className="flex shrink-0 items-center gap-0.5">
          <HintTooltip label={hidden ? "重新纳入查询" : "暂时不执行这条表达式"}>
            <Button
              size="icon-sm"
              variant="ghost"
              role="switch"
              aria-checked={!hidden}
              aria-label={hidden ? `启用表达式 ${reference}` : `隐藏表达式 ${reference}`}
              onClick={onToggleHidden}
            >
              {hidden ? <EyeOff /> : <Eye />}
            </Button>
          </HintTooltip>
          <HintTooltip label="保存这条表达式">
            <span>
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={`保存表达式 ${reference}`}
                disabled={expression.trim() === ""}
                onClick={onSave}
              >
                <BookmarkPlus />
              </Button>
            </span>
          </HintTooltip>
          <HintTooltip label="删除这一行">
            <span>
              <Button
                size="icon-sm"
                variant="ghost"
                className="text-danger hover:text-danger"
                aria-label={`删除表达式 ${reference}`}
                disabled={!removable}
                onClick={onRemove}
              >
                <Trash2 />
              </Button>
            </span>
          </HintTooltip>
        </div>
      </div>
      <textarea
        id={fieldId}
        ref={field}
        value={expression}
        rows={1}
        spellCheck={false}
        autoComplete="off"
        autoCorrect="off"
        autoCapitalize="off"
        aria-describedby={describedBy}
        aria-invalid={failed ? true : undefined}
        placeholder="例如：sum by (node) (rate(node_cpu_usage_seconds_total[5m]))"
        className={cn(
          "zke-focus zke-mono border-border bg-surface text-foreground rounded-control placeholder:text-subtle-foreground w-full resize-none border px-2.5 py-1.5 text-xs leading-relaxed",
          failed && "border-danger",
        )}
        onFocus={onFocus}
        onChange={(event) => onChange(event.target.value)}
        onKeyDown={(event) => {
          // Enter inserts a newline, as it does in any text area. Running is
          // the modifier chord every query editor uses, so the muscle memory
          // an operator brings from one works here.
          if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
            event.preventDefault();
            onRun();
          }
        }}
      />
      {failed ? (
        <p id={describedBy} className="text-danger flex items-start gap-1.5 text-xs" role="alert">
          <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden />
          <span className="min-w-0 break-words">{errorText(failed)}</span>
        </p>
      ) : null}
      {!failed && outcome?.warning === "likely_invalid" ? (
        <p className="text-warning flex items-start gap-1.5 text-xs">
          <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden />
          <span className="min-w-0">
            该表达式包含隐式转换（例如把瞬时向量当作区间向量），结果多半不是想问的那个。
          </span>
        </p>
      ) : null}
    </div>
  );
}

/**
 * What to show for a failed expression.
 *
 * The two codes that carry a detail get it verbatim: one is the Server
 * explaining the operator's own text back to them, the other is the storage
 * doing the same. The two that do not are about the Server rather than about
 * the expression, and the Console owns those words.
 */
function errorText(error: NonNullable<MetricsExploreOutcome["error"]>): string {
  if (error.detail) {
    return error.detail;
  }
  return error.code === "timeout"
    ? "查询超时，请缩小时间范围或简化表达式。"
    : "指标存储当前不可用。";
}
