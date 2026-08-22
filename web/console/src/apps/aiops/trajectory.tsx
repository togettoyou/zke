import {
  memo,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type PointerEvent as ReactPointerEvent,
} from "react";
import { ChevronDown, ChevronRight, Clock, ListTree, Search, Wrench, X } from "lucide-react";

import type { AITrajectoryEntry, AITrajectoryKind } from "@/api/types";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { HintTooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/cn";

import { Evidence } from "./conversation";
import { CopyIconButton } from "./copy";
import {
  compactionMethodLabel,
  compactionTriggerLabel,
  entryText,
  entryTitle,
  filterLedger,
  formatClock,
  formatDuration,
  formatTokens,
  KIND_CODES,
  KIND_LABELS,
  ledgerTurns,
  prettyBody,
  presentKinds,
  runStats,
  sequencesInRange,
  timelineModel,
  type LedgerRow,
  type LedgerTurn,
  type TimelineMode,
  type TimelineModel,
  type TimelineRange,
  type TimelineSpan,
} from "./entries";
import { Markdown } from "./markdown";

/**
 * The trajectory: what actually ran, in order, with the evidence and the cost.
 *
 * Three readings of one list rather than three lists. The strip at the top is
 * the shape of the run in time and the instrument for narrowing it; the ledger
 * is the sequence, folded the way the run was structured — a model step and the
 * calls it made are one act; the panel is one entry in full. Every filter —
 * a dragged window, a kind, a search — narrows all three together, because a
 * timeline that disagreed with the list beneath it would be worse than none.
 */
export function Trajectory({
  entries,
  pending,
  error,
  onRetry,
}: {
  entries: AITrajectoryEntry[];
  pending: boolean;
  error: Error | null;
  onRetry: () => void;
}) {
  const [mode, setMode] = useState<TimelineMode>("duration");
  const [search, setSearch] = useState("");
  const [kind, setKind] = useState<"all" | AITrajectoryKind>("all");
  const [range, setRange] = useState<TimelineRange | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [foldedTurns, setFoldedTurns] = useState<ReadonlySet<number>>(() => new Set());
  const [foldedRows, setFoldedRows] = useState<ReadonlySet<number>>(() => new Set());
  const ledger = useRef<HTMLDivElement | null>(null);
  // Starts unpinned: a trajectory is opened to be read from the beginning of
  // the run, and jumping a finished session to its last row on open would hide
  // the question that started it.
  const pinned = useRef(false);

  const model = useMemo(() => timelineModel(entries, mode), [entries, mode]);
  const turns = useMemo(() => ledgerTurns(entries), [entries]);

  const needle = search.trim().toLocaleLowerCase();
  const matches = useMemo(() => {
    if (!needle) return null;
    return new Set(
      entries
        .filter((entry) =>
          `${KIND_LABELS[entry.kind]} ${entry.content.tool ?? ""} ${entryText(entry)}`
            .toLocaleLowerCase()
            .includes(needle),
        )
        .map((entry) => entry.sequence),
    );
  }, [entries, needle]);

  const focused = useMemo(
    () => (model && range ? sequencesInRange(model, range) : null),
    [model, range],
  );

  const visible = useMemo(
    () =>
      filterLedger(turns, (entry) => {
        if (kind !== "all" && entry.kind !== kind) return false;
        if (matches && !matches.has(entry.sequence)) return false;
        if (focused && !focused.has(entry.sequence)) return false;
        return true;
      }),
    [turns, kind, matches, focused],
  );

  const stats = useMemo(() => runStats(entries), [entries]);
  const detail = useMemo(
    () => entries.find((entry) => entry.sequence === selected) ?? null,
    [entries, selected],
  );
  const shown = visible.reduce((total, turn) => total + turn.count, 0);
  const filtering = kind !== "all" || matches !== null || focused !== null;

  /** Where a sequence sits in the ledger, so selecting it can open its folds. */
  const placement = useMemo(() => {
    const map = new Map<number, { turn: number; parent: number | null }>();
    for (const turn of turns) {
      for (const row of turn.rows) {
        map.set(row.entry.sequence, { turn: turn.turn, parent: null });
        for (const child of row.children) {
          map.set(child.sequence, { turn: turn.turn, parent: row.entry.sequence });
        }
      }
    }
    return map;
  }, [turns]);

  /**
   * Selecting from the timeline has to be able to reach a row that is folded
   * away — otherwise clicking a block answers with a detail panel for something
   * the ledger does not show, which reads as the two views disagreeing.
   */
  const reveal = useCallback(
    (sequence: number) => {
      const at = placement.get(sequence);
      if (at) {
        setFoldedTurns((current) => {
          if (!current.has(at.turn)) return current;
          const next = new Set(current);
          next.delete(at.turn);
          return next;
        });
        if (at.parent !== null) {
          const parent = at.parent;
          setFoldedRows((current) => {
            if (!current.has(parent)) return current;
            const next = new Set(current);
            next.delete(parent);
            return next;
          });
        }
      }
      // After the folds have opened, so the row exists to be scrolled to.
      requestAnimationFrame(() => {
        ledger.current
          ?.querySelector(`[data-sequence="${sequence}"]`)
          ?.scrollIntoView({ block: "nearest", behavior: "smooth" });
      });
    },
    [placement],
  );

  const select = useCallback(
    (sequence: number) => {
      setSelected(sequence);
      reveal(sequence);
    },
    [reveal],
  );

  // Once the operator has scrolled to the end, the ledger follows a live run
  // from there. A run that is still going appends rows under whatever is being
  // read, and yanking them back down mid-read is the same mistake the
  // conversation refuses to make.
  useEffect(() => {
    const node = ledger.current;
    if (node && pinned.current) node.scrollTop = node.scrollHeight;
  }, [shown]);

  const foldableTurns = turns.filter((turn) => turn.rows.length > 0).map((turn) => turn.turn);
  const foldableRows = turns.flatMap((turn) =>
    turn.rows.filter((row) => row.children.length > 0).map((row) => row.entry.sequence),
  );
  const turnsFolded =
    foldableTurns.length > 0 && foldableTurns.every((turn) => foldedTurns.has(turn));
  const callsFolded =
    foldableRows.length > 0 && foldableRows.every((sequence) => foldedRows.has(sequence));

  if (pending) return <LoadingState label="加载会话轨迹" />;
  if (error) return <ErrorState error={error} onRetry={onRetry} />;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="border-border flex shrink-0 items-center gap-1 border-b px-3 py-1.5">
        <ToolbarToggle
          icon={Clock}
          label="时长"
          hint={
            mode === "duration"
              ? "按真实耗时排布：切换为等宽，让短事件也点得到"
              : "等宽排布：切换回真实耗时，看这次运行的实际形状"
          }
          pressed={mode === "duration"}
          onClick={() => setMode(mode === "duration" ? "sequence" : "duration")}
        />
        <ToolbarToggle
          icon={ListTree}
          label="轮次"
          hint={turnsFolded ? "展开全部轮次" : "折叠全部轮次"}
          pressed={turnsFolded}
          disabled={foldableTurns.length === 0}
          onClick={() => setFoldedTurns(turnsFolded ? new Set() : new Set(foldableTurns))}
        />
        <ToolbarToggle
          icon={Wrench}
          label="调用"
          hint={callsFolded ? "展开每个步骤下的工具调用" : "折叠每个步骤下的工具调用"}
          pressed={callsFolded}
          disabled={foldableRows.length === 0}
          onClick={() => setFoldedRows(callsFolded ? new Set() : new Set(foldableRows))}
        />

        <Select value={kind} onValueChange={(value) => setKind(value as typeof kind)}>
          <SelectTrigger className="ml-auto h-8 w-32 text-xs" aria-label="轨迹类型">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">全部类型</SelectItem>
            {presentKinds(entries).map((item) => (
              <SelectItem key={item} value={item}>
                {KIND_LABELS[item]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="relative w-52">
          <Search
            aria-hidden
            className="text-subtle-foreground pointer-events-none absolute top-2 left-2.5 size-4"
          />
          <Input
            aria-label="搜索轨迹"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder="搜索轨迹"
            className="h-8 pl-8 text-[13px]"
          />
        </div>
      </div>

      <Timeline
        model={model}
        matches={matches}
        selected={selected}
        range={range}
        onRangeChange={setRange}
        onSelect={select}
      />

      {/* Only while something is actually narrowed. The line says what the three
          views are currently agreeing on, and offers the one way back. */}
      {filtering ? (
        <div className="border-border bg-primary-surface/40 text-muted-foreground flex shrink-0 items-center gap-2 border-b px-3 py-1 text-[11px]">
          <span>
            已筛选 {shown} / {entries.length} 条
            {range && model ? ` · 窗口 ${rangeLabel(model, range)}` : ""}
          </span>
          <Button
            size="sm"
            variant="ghost"
            className="ml-auto h-6 px-2 text-[11px]"
            onClick={() => {
              setRange(null);
              setKind("all");
              setSearch("");
            }}
          >
            清除筛选
          </Button>
        </div>
      ) : null}

      <div
        className={cn(
          "grid min-h-0 flex-1",
          detail
            ? "grid-cols-[minmax(300px,1fr)_minmax(340px,1fr)] max-[900px]:grid-cols-1 max-[900px]:grid-rows-[minmax(160px,1fr)_minmax(220px,1.4fr)]"
            : "grid-cols-1",
        )}
      >
        <div
          ref={ledger}
          onScroll={(event) => {
            const node = event.currentTarget;
            pinned.current = node.scrollHeight - node.scrollTop - node.clientHeight < 40;
          }}
          className="border-border min-h-0 overflow-auto border-r max-[900px]:border-r-0"
        >
          {visible.length === 0 ? (
            <EmptyState title="没有匹配的轨迹" description="调整时间窗口、类型或搜索条件后重试。" />
          ) : (
            visible.map((turn) => (
              <TurnSection
                key={turn.turn}
                turn={turn}
                folded={foldedTurns.has(turn.turn)}
                foldedRows={foldedRows}
                selected={selected}
                onToggleTurn={() => setFoldedTurns((current) => toggled(current, turn.turn))}
                onToggleRow={(sequence) => setFoldedRows((current) => toggled(current, sequence))}
                onSelect={setSelected}
              />
            ))
          )}
        </div>
        {detail ? (
          <Detail key={detail.sequence} entry={detail} onClose={() => setSelected(null)} />
        ) : null}
      </div>

      <StatusBar stats={stats} count={entries.length} />
    </div>
  );
}

function toggled(current: ReadonlySet<number>, value: number): ReadonlySet<number> {
  const next = new Set(current);
  if (!next.delete(value)) next.add(value);
  return next;
}

function ToolbarToggle({
  icon: Icon,
  label,
  hint,
  pressed,
  disabled,
  onClick,
}: {
  icon: typeof Clock;
  label: string;
  hint: string;
  pressed: boolean;
  disabled?: boolean;
  onClick: () => void;
}) {
  return (
    <HintTooltip label={hint}>
      {/* A disabled button fires no pointer events of its own, and this row's
          controls disable themselves whenever the run has nothing to fold. */}
      <span>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          aria-pressed={pressed}
          disabled={disabled}
          onClick={onClick}
          className={cn("h-8 px-2 text-xs", pressed && "bg-surface-muted text-foreground")}
        >
          <Icon aria-hidden /> {label}
        </Button>
      </span>
    </HintTooltip>
  );
}

/* ------------------------------------------------------------------ timeline */

/** Lane geometry, in the strip's own pixels. Three lanes at 8px, 6px apart. */
const LANE_HEIGHT = 14;
const BAR_HEIGHT = 8;
const TRACK_HEIGHT = 48;
const TRACK_PAD = 7;
/** Below this a press is a click, not a drag — the same slack a chart uses. */
const CLICK_SLACK_PX = 3;
/** Never zoom past four entries: past that the strip stops being a shape. */
const MIN_ZOOM_ENTRIES = 4;

/**
 * The strip: the run's shape, and the instrument for narrowing it.
 *
 * Dragging across it selects a window the way dragging across a metrics chart
 * selects one, and for the same reason — the interesting part of a run is a
 * stretch of time, and naming it by entry number means reading the list first.
 * A window here narrows the ledger rather than zooming it, because the record
 * is what is being read: the strip answers "when", the ledger answers "what".
 *
 * The wheel zooms and the right button pans, so a forty-second model call
 * cannot bury the twelve tool calls beside it; both are viewport-only and
 * change nothing about what the ledger shows.
 */
const Timeline = memo(function Timeline({
  model,
  matches,
  selected,
  range,
  onRangeChange,
  onSelect,
}: {
  model: TimelineModel | null;
  matches: ReadonlySet<number> | null;
  selected: number | null;
  range: TimelineRange | null;
  onRangeChange: (range: TimelineRange | null) => void;
  onSelect: (sequence: number) => void;
}) {
  const track = useRef<HTMLDivElement | null>(null);
  const drag = useRef<{ pointerId: number; anchor: number; clientX: number } | null>(null);
  const pan = useRef<{ pointerId: number; clientX: number; start: number; moved: boolean } | null>(
    null,
  );
  const [draft, setDraft] = useState<TimelineRange | null>(null);
  const [hover, setHover] = useState<{ fraction: number; sequence: number | null } | null>(null);
  const [viewport, setViewport] = useState<TimelineRange | null>(null);
  // Panning lives in state as well as in the ref because the cursor is part of
  // it: a ref read during render never repaints the grab hand.
  const [panning, setPanning] = useState(false);

  const full = model ? Math.max(1, model.end - model.start) : 1;
  const span = viewport ? Math.min(full, Math.max(1, viewport.end - viewport.start)) : full;
  const origin = model
    ? viewport
      ? Math.min(Math.max(viewport.start, model.start), model.end - span)
      : model.start
    : 0;

  // A window that no longer overlaps the run — the strip rebuilt under a new
  // projection, or entries arrived — is a filter nobody can see, hiding the
  // ledger for a reason the view no longer shows.
  useEffect(() => {
    if (model && range && (range.end < model.start || range.start > model.end)) onRangeChange(null);
  }, [model, range, onRangeChange]);

  // Adjusted during render rather than in an effect: a zoom belongs to the
  // projection it was made in, and carrying one across the switch would paint a
  // window over a domain it was never measured against — for one frame, which is
  // exactly the frame the operator is watching.
  const [projection, setProjection] = useState(model?.mode);
  if (model && model.mode !== projection) {
    setProjection(model.mode);
    setViewport(null);
  }

  // Non-passive, because zooming the strip must not scroll the ledger behind
  // it. React's onWheel is passive and cannot call preventDefault.
  useEffect(() => {
    const node = track.current;
    if (!node || !model) return;
    const onWheel = (event: WheelEvent) => {
      event.preventDefault();
      const rect = node.getBoundingClientRect();
      const fraction = clamp((event.clientX - rect.left) / Math.max(1, rect.width), 0, 1);
      const floor = Math.min(
        full,
        model.mode === "sequence" ? MIN_ZOOM_ENTRIES : Math.max(20, full / 400),
      );
      const next = clamp(span * Math.exp(event.deltaY * 0.0015), floor, full);
      if (next >= full * 0.999) {
        setViewport(null);
        return;
      }
      const anchor = origin + fraction * span;
      const start = clamp(anchor - fraction * next, model.start, model.end - next);
      setViewport({ start, end: start + next });
    };
    node.addEventListener("wheel", onWheel, { passive: false });
    return () => node.removeEventListener("wheel", onWheel);
  }, [model, full, span, origin]);

  if (!model) {
    return (
      <div className="border-border bg-surface-muted/30 text-subtle-foreground flex shrink-0 items-center justify-center border-b text-[11px]">
        <span style={{ height: TRACK_HEIGHT }} className="flex items-center">
          还没有可以绘制的时间数据
        </span>
      </div>
    );
  }

  const fractionAt = (event: ReactPointerEvent<HTMLDivElement>) => {
    const rect = event.currentTarget.getBoundingClientRect();
    return clamp((event.clientX - rect.left) / Math.max(1, rect.width), 0, 1);
  };
  const sequenceAt = (event: ReactPointerEvent<HTMLDivElement>) => {
    const target = event.target instanceof HTMLElement ? event.target : null;
    const value = target?.closest<HTMLElement>("[data-span]")?.dataset.span;
    return value === undefined ? null : Number(value);
  };

  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button === 2) {
      pan.current = {
        pointerId: event.pointerId,
        clientX: event.clientX,
        start: origin,
        moved: false,
      };
      setPanning(true);
      event.currentTarget.setPointerCapture?.(event.pointerId);
      return;
    }
    if (event.button !== 0) return;
    const at = origin + fractionAt(event) * span;
    drag.current = { pointerId: event.pointerId, anchor: at, clientX: event.clientX };
    event.currentTarget.setPointerCapture?.(event.pointerId);
    setDraft({ start: at, end: at });
  };

  const onPointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const fraction = fractionAt(event);
    setHover({ fraction, sequence: sequenceAt(event) });
    const dragPan = pan.current;
    if (dragPan && dragPan.pointerId === event.pointerId) {
      if (Math.abs(event.clientX - dragPan.clientX) >= CLICK_SLACK_PX) dragPan.moved = true;
      if (!viewport) return;
      const rect = event.currentTarget.getBoundingClientRect();
      const delta = ((event.clientX - dragPan.clientX) / Math.max(1, rect.width)) * span;
      const start = clamp(dragPan.start - delta, model.start, model.end - span);
      setViewport({ start, end: start + span });
      return;
    }
    const dragging = drag.current;
    if (!dragging || dragging.pointerId !== event.pointerId) return;
    const at = origin + fraction * span;
    setDraft(ordered(dragging.anchor, at));
  };

  const onPointerUp = (event: ReactPointerEvent<HTMLDivElement>) => {
    const dragPan = pan.current;
    if (dragPan && dragPan.pointerId === event.pointerId) {
      pan.current = null;
      setPanning(false);
      return;
    }
    const dragging = drag.current;
    if (!dragging || dragging.pointerId !== event.pointerId) return;
    drag.current = null;
    setDraft(null);
    const at = origin + fractionAt(event) * span;
    const click = Math.abs(event.clientX - dragging.clientX) < CLICK_SLACK_PX;
    if (click) {
      const sequence = sequenceAt(event);
      // A click on a block selects it; a click on the whitespace between blocks
      // is how a window is cleared without hunting for a control.
      if (sequence !== null) onSelect(sequence);
      else onRangeChange(null);
      return;
    }
    const selection = ordered(dragging.anchor, at);
    const smallest = full / Math.max(1, model.spans.length);
    onRangeChange(
      selection.end - selection.start < smallest
        ? centered((selection.start + selection.end) / 2, smallest, model.start, model.end)
        : selection,
    );
  };

  const window = draft ?? range;
  const active = draft ?? range;
  const selectedSpan =
    selected === null ? null : model.spans.find((item) => item.sequence === selected);

  return (
    <div
      className="border-border bg-surface-muted/30 relative z-10 flex shrink-0 border-b select-none"
      style={{ height: TRACK_HEIGHT }}
    >
      <div
        aria-hidden
        className="border-border text-subtle-foreground relative w-11 shrink-0 border-r text-[10px] leading-none"
      >
        {["输入", "模型", "工具"].map((label, lane) => (
          <span
            key={label}
            className="absolute right-1.5 flex items-center"
            style={{ top: TRACK_PAD + lane * LANE_HEIGHT, height: BAR_HEIGHT }}
          >
            {label}
          </span>
        ))}
      </div>
      <div
        ref={track}
        role="application"
        aria-label="运行时间轴：横向拖动可框选时间窗口，滚轮缩放，右键拖动平移"
        tabIndex={0}
        className={cn(
          // Not `overflow-hidden`: the hover readout hangs below the strip, over
          // the ledger. Only the drawing layer inside clips, which is what a
          // zoomed viewport needs.
          "zke-focus relative min-w-0 flex-1 cursor-crosshair",
          panning && "cursor-grabbing",
        )}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={() => {
          drag.current = null;
          pan.current = null;
          setPanning(false);
          setDraft(null);
          setHover(null);
        }}
        onPointerLeave={() => {
          if (!drag.current && !pan.current) setHover(null);
        }}
        onDoubleClick={(event) => {
          event.preventDefault();
          onRangeChange(null);
          setViewport(null);
        }}
        onContextMenu={(event) => event.preventDefault()}
        onKeyDown={(event) => {
          if (event.key !== "Escape") return;
          if (!range && !viewport) return;
          event.preventDefault();
          onRangeChange(null);
          setViewport(null);
        }}
      >
        <div className="absolute inset-0 overflow-hidden">
          {/* Everything outside the window dims rather than disappears: the shape
            of the whole run is the context the selection is read against. */}
          {window ? (
            <>
              <div
                aria-hidden
                className="bg-chart-selection pointer-events-none absolute inset-y-0"
                style={box(window, origin, span)}
              />
              <div
                aria-hidden
                className="border-primary pointer-events-none absolute inset-y-0 border-x-2"
                style={box(window, origin, span)}
              />
            </>
          ) : null}

          {hover && !draft && hover.sequence === null ? (
            <div
              aria-hidden
              className="bg-primary/50 pointer-events-none absolute inset-y-0 w-px"
              style={{ left: `${hover.fraction * 100}%` }}
            />
          ) : null}

          {model.boundaries
            .filter((boundary) => boundary.at > model.start + 0.5)
            .map((boundary) => (
              <span
                key={boundary.turn}
                aria-hidden
                className="bg-border-strong pointer-events-none absolute inset-y-0 w-px"
                style={{ left: `${((boundary.at - origin) / span) * 100}%` }}
              />
            ))}

          {/* A full-height column under the selected block.
              Blocks are eight pixels tall and can be two pixels wide, so a ring
              around one of them is a detail the eye has to be told where to
              look for. The column is what makes "which one is selected" a
              glance rather than a search. */}
          {selectedSpan ? (
            <span
              aria-hidden
              className="bg-foreground/10 border-foreground/40 pointer-events-none absolute inset-y-0 border-x"
              style={{
                left: `${((selectedSpan.start - origin) / span) * 100}%`,
                width: `max(3px, ${((selectedSpan.end - selectedSpan.start) / span) * 100}%)`,
              }}
            />
          ) : null}

          {model.spans.map((item) => {
            const left = (item.start - origin) / span;
            const width = (item.end - item.start) / span;
            // Off-screen after a zoom, and cheaper to skip than to paint: the
            // strip is redrawn on every pointer move while a window is dragged.
            if (left > 1.02 || left + width < -0.02) return null;
            return (
              <Span
                key={item.sequence}
                span={item}
                left={left}
                width={width}
                current={item.sequence === selected}
                matched={matches === null ? null : matches.has(item.sequence)}
                inside={
                  active === null ? null : item.start <= active.end && item.end >= active.start
                }
              />
            );
          })}
        </div>

        {hover && hover.sequence !== null ? (
          <Readout model={model} sequence={hover.sequence} fraction={hover.fraction} />
        ) : null}
      </div>
    </div>
  );
});

function box(range: TimelineRange, origin: number, span: number): CSSProperties {
  const left = (range.start - origin) / span;
  const width = (range.end - range.start) / span;
  return { left: `${left * 100}%`, width: `${Math.max(width, 0.001) * 100}%` };
}

const Span = memo(function Span({
  span,
  left,
  width,
  current,
  matched,
  inside,
}: {
  span: TimelineSpan;
  left: number;
  width: number;
  current: boolean;
  matched: boolean | null;
  inside: boolean | null;
}) {
  return (
    <span
      data-span={span.sequence}
      aria-hidden
      className={cn(
        "rounded-inline absolute transition-opacity duration-150",
        spanTone(span),
        // The selected block keeps its full colour whatever the search and the
        // window would have done to it: dimming the one block the reader just
        // picked is how a selection becomes impossible to find again.
        current
          ? "ring-foreground ring-offset-surface z-2 opacity-100 ring-2 ring-offset-1"
          : cn(matched === false && "opacity-15", inside === false && "opacity-25"),
      )}
      style={{
        left: `${left * 100}%`,
        width: `max(2px, ${width * 100}%)`,
        top: TRACK_PAD + span.lane * LANE_HEIGHT,
        height: BAR_HEIGHT,
      }}
    >
      {/* The solid part of a streamed step is the part that produced tokens;
          the lighter head is the wait before the first one. */}
      {span.ttft !== null ? (
        <span
          className="bg-chart-7 rounded-inline absolute inset-y-0 right-0"
          style={{ width: `${(1 - span.ttft) * 100}%` }}
        />
      ) : null}
    </span>
  );
});

/** What the pointer is over, named without a tooltip per block. */
function Readout({
  model,
  sequence,
  fraction,
}: {
  model: TimelineModel;
  sequence: number | null;
  fraction: number;
}) {
  const span = model.spans.find((item) => item.sequence === sequence);
  if (!span) return null;
  const started = model.mode === "duration" ? model.origin + span.start : null;
  return (
    <div
      className={cn(
        "border-border bg-surface shadow-e3 rounded-control text-muted-foreground pointer-events-none absolute top-full z-20 mt-1 border px-2 py-1 text-[11px] whitespace-nowrap",
        fraction > 0.6 ? "-translate-x-full" : "",
      )}
      style={{ left: `calc(${fraction * 100}% + ${fraction > 0.6 ? "-6px" : "6px"})` }}
    >
      <span className="text-foreground font-medium">{KIND_LABELS[span.kind]}</span>
      <span className="text-subtle-foreground"> #{span.sequence}</span>
      {model.mode === "duration" ? (
        <>
          {" · "}
          {formatDuration(span.end - span.start)}
        </>
      ) : null}
      {started !== null ? <> · {formatClock(started)}</> : null}
    </div>
  );
}

function rangeLabel(model: TimelineModel, range: TimelineRange): string {
  if (model.mode !== "duration") {
    return `第 ${Math.max(1, Math.round(range.start) + 1)}–${Math.round(range.end)} 条`;
  }
  return `${formatClock(model.origin + range.start)} → ${formatClock(model.origin + range.end)}（${formatDuration(range.end - range.start)}）`;
}

/* -------------------------------------------------------------------- ledger */

function TurnSection({
  turn,
  folded,
  foldedRows,
  selected,
  onToggleTurn,
  onToggleRow,
  onSelect,
}: {
  turn: LedgerTurn;
  folded: boolean;
  foldedRows: ReadonlySet<number>;
  selected: number | null;
  onToggleTurn: () => void;
  onToggleRow: (sequence: number) => void;
  onSelect: (sequence: number) => void;
}) {
  return (
    <section>
      {/* Sticky, because the one thing that stops being obvious while scrolling
          a long run is which turn the rows belong to. */}
      <button
        type="button"
        onClick={onToggleTurn}
        aria-expanded={!folded}
        className="zke-focus border-border bg-surface-muted/70 text-subtle-foreground sticky top-0 z-2 flex w-full items-center gap-1.5 border-b px-3 py-1 text-left text-[11px] backdrop-blur-sm"
      >
        <ChevronDown
          aria-hidden
          className={cn("size-3 transition-transform duration-150", folded && "-rotate-90")}
        />
        第 {turn.turn} 轮<span className="text-subtle-foreground/70 ml-auto">{turn.count} 条</span>
      </button>
      {folded ? null : (
        <ol>
          {turn.rows.map((row) => (
            <LedgerRowView
              key={row.entry.sequence}
              row={row}
              folded={foldedRows.has(row.entry.sequence)}
              selected={selected}
              onToggle={() => onToggleRow(row.entry.sequence)}
              onSelect={onSelect}
            />
          ))}
        </ol>
      )}
    </section>
  );
}

const LedgerRowView = memo(function LedgerRowView({
  row,
  folded,
  selected,
  onToggle,
  onSelect,
}: {
  row: LedgerRow;
  folded: boolean;
  selected: number | null;
  onToggle: () => void;
  onSelect: (sequence: number) => void;
}) {
  return (
    <li>
      <EntryRow
        entry={row.entry}
        selected={selected === row.entry.sequence}
        onSelect={onSelect}
        fold={
          row.children.length > 0 ? { folded, count: row.children.length, onToggle } : undefined
        }
      />
      {folded
        ? null
        : row.children.map((child) => (
            <EntryRow
              key={child.sequence}
              entry={child}
              nested
              selected={selected === child.sequence}
              onSelect={onSelect}
            />
          ))}
    </li>
  );
});

const EntryRow = memo(function EntryRow({
  entry,
  nested,
  selected,
  onSelect,
  fold,
}: {
  entry: AITrajectoryEntry;
  nested?: boolean;
  selected: boolean;
  onSelect: (sequence: number) => void;
  fold?: { folded: boolean; count: number; onToggle: () => void };
}) {
  return (
    <div
      data-sequence={entry.sequence}
      className={cn(
        "group border-border relative flex items-center border-b transition-colors duration-100",
        selected ? "bg-primary-surface" : "hover:bg-surface-muted/60",
      )}
    >
      {/* The accent bar rather than a border on the row: a border would move
          every row by a pixel as the selection travels down the list. */}
      <span
        aria-hidden
        className={cn(
          "bg-primary absolute inset-y-0 left-0 w-0.5",
          selected ? "opacity-100" : "opacity-0",
        )}
      />
      {fold ? (
        <button
          type="button"
          aria-label={fold.folded ? "展开该步骤的工具调用" : "折叠该步骤的工具调用"}
          aria-expanded={!fold.folded}
          onClick={fold.onToggle}
          className="zke-focus text-subtle-foreground hover:text-foreground ml-1 flex size-5 shrink-0 items-center justify-center rounded-full"
        >
          <ChevronRight
            aria-hidden
            className={cn(
              "size-3.5 transition-transform duration-150",
              !fold.folded && "rotate-90",
            )}
          />
        </button>
      ) : (
        <span aria-hidden className="ml-1 w-5 shrink-0" />
      )}
      <button
        type="button"
        onClick={() => onSelect(entry.sequence)}
        className={cn(
          "zke-focus flex min-w-0 flex-1 items-center gap-2 py-1.5 pr-3 text-left",
          nested ? "pl-6" : "pl-1",
        )}
      >
        <span
          className={cn(
            "rounded-inline w-14 shrink-0 py-0.5 text-center font-mono text-[10px] font-semibold",
            badgeTone(entry.kind),
          )}
        >
          {KIND_CODES[entry.kind]}
        </span>
        <span className="text-foreground min-w-0 flex-1 truncate text-xs">{entryTitle(entry)}</span>
        {fold?.folded ? (
          <span className="text-subtle-foreground shrink-0 text-[11px]">+{fold.count} 次调用</span>
        ) : null}
        {entry.duration_ms > 0 ? (
          <span className="text-subtle-foreground zke-tnum shrink-0 text-[11px]">
            {formatDuration(entry.duration_ms)}
          </span>
        ) : null}
        <span className="text-subtle-foreground/70 zke-tnum w-10 shrink-0 text-right text-[11px]">
          #{entry.sequence}
        </span>
      </button>
    </div>
  );
});

/* -------------------------------------------------------------------- detail */

/**
 * One entry in full.
 *
 * Keyed by sequence at the call site, so selecting another entry opens on its
 * summary: a panel that kept the previous tab would answer a click on a tool
 * result with the empty 预览 pane the model step before it had open.
 */
function Detail({ entry, onClose }: { entry: AITrajectoryEntry; onClose: () => void }) {
  const [tab, setTab] = useState("summary");
  const content = entry.content;
  const body = prettyBody(entry);
  const readable = entry.kind === "conclusion" || entry.kind === "model" || entry.kind === "input";

  return (
    <Tabs value={tab} onValueChange={setTab} className="flex min-h-0 min-w-0 flex-col">
      <div className="border-border shrink-0 border-b px-3 py-2">
        <div className="flex items-center gap-2">
          <span
            className={cn(
              "rounded-inline shrink-0 px-1.5 py-0.5 font-mono text-[10px] font-semibold",
              badgeTone(entry.kind),
            )}
          >
            {KIND_CODES[entry.kind]}
          </span>
          <span className="text-muted-foreground truncate text-xs">
            第 {entry.turn} 轮{content.step ? ` · 第 ${content.step} 步` : ""} · #{entry.sequence}
          </span>
          <div className="ml-auto flex shrink-0 items-center">
            <CopyIconButton value={() => body} label="复制内容" />
            <Button size="icon-sm" variant="ghost" aria-label="关闭详情" onClick={onClose}>
              <X aria-hidden />
            </Button>
          </div>
        </div>
        <h3 className="text-foreground mt-1 truncate text-sm font-semibold">{entryTitle(entry)}</h3>
        <TabsList className="mt-2 h-7 gap-0.5 p-0.5">
          <TabsTrigger value="summary" className="h-6 px-2.5 text-[11px]">
            概要
          </TabsTrigger>
          <TabsTrigger value="preview" className="h-6 px-2.5 text-[11px]">
            预览
          </TabsTrigger>
          <TabsTrigger value="raw" className="h-6 px-2.5 text-[11px]">
            原文
          </TabsTrigger>
        </TabsList>
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-3">
        <TabsContent value="summary" className="mt-0">
          <dl className="grid grid-cols-2 gap-x-5 gap-y-2 text-xs">
            <Field label="时间" value={new Date(entry.occurred_at).toLocaleString("zh-CN")} />
            <Field label="耗时" value={formatDuration(entry.duration_ms)} />
            {content.tool ? <Field label="工具" value={content.tool} /> : null}
            {content.call_id ? <Field label="调用 ID" value={content.call_id} /> : null}
            {content.authorized !== undefined ? (
              <Field label="是否执行" value={content.authorized ? "已执行" : "未执行"} />
            ) : null}
            {content.decision ? (
              <Field
                label="批准结果"
                value={content.decision === "approved" ? "已批准" : "已拒绝"}
              />
            ) : null}
            {content.failed ? <Field label="结果" value="未取得预期内容" /> : null}
            {content.mode ? <Field label="审批模式" value={content.mode} /> : null}
            {content.target ? (
              <Field
                label="目标"
                value={[content.target.cluster, content.target.namespace, content.target.name]
                  .filter(Boolean)
                  .join(" / ")}
              />
            ) : null}
            {content.untrusted ? <Field label="信任边界" value="集群返回的不可信数据" /> : null}
            {content.tokens ? (
              <Field
                label="Token"
                value={`${content.tokens.input} 入 / ${content.tokens.output} 出 / ${content.tokens.context} 上下文`}
              />
            ) : null}
            {content.timing ? (
              <Field
                label="模型时延"
                value={
                  content.timing.streamed
                    ? `首 token ${formatDuration(content.timing.first_token_ms ?? 0)} · 总计 ${formatDuration(content.timing.elapsed_ms ?? 0)}`
                    : `未流式，总计 ${formatDuration(content.timing.elapsed_ms ?? 0)}`
                }
              />
            ) : null}
            {content.compaction ? (
              <>
                <Field
                  label="压缩"
                  value={`${compactionMethodLabel(content.compaction.method)} · ${content.compaction.before_tokens} → ${content.compaction.after_tokens} tokens`}
                />
                <Field
                  label="压缩触发"
                  value={`${compactionTriggerLabel(content.compaction.trigger)} · 阈值 ${content.compaction.threshold_tokens} tokens`}
                />
                <Field
                  label="替换区间"
                  value={`#${content.compaction.shadowed_from} – #${content.compaction.shadowed_to}，其后原样保留 ${content.compaction.retained_tokens ?? 0} tokens`}
                />
              </>
            ) : null}
          </dl>
          {content.tools?.length ? (
            <section className="mt-3">
              <h4 className="text-subtle-foreground mb-1 text-[11px]">
                {entry.kind === "system" ? "本轮可用工具" : "该步骤请求的工具"}
              </h4>
              <div className="flex flex-wrap gap-1.5">
                {content.tools.map((tool) => (
                  <span
                    key={tool}
                    className="border-border bg-surface-muted text-muted-foreground rounded-control border px-2 py-0.5 font-mono text-[11px]"
                  >
                    {tool}
                  </span>
                ))}
              </div>
            </section>
          ) : null}
          {content.evidence?.length ? <Evidence evidence={content.evidence} /> : null}
          {entry.truncated ? (
            <p className="text-warning mt-3 text-xs">
              该条目只保留有界摘录，完整对象请通过证据入口查看。
            </p>
          ) : null}
        </TabsContent>

        <TabsContent value="preview" className="mt-0">
          {readable ? (
            <Markdown text={entryText(entry)} />
          ) : (
            <pre className="text-muted-foreground font-mono text-xs whitespace-pre-wrap">
              {body}
            </pre>
          )}
        </TabsContent>

        <TabsContent value="raw" className="mt-0">
          {content.arguments ? <Body title="参数" value={content.arguments} /> : null}
          <Body title="内容" value={body} />
        </TabsContent>
      </div>
    </Tabs>
  );
}

/**
 * The bottom line: what this session cost.
 *
 * Every number is derived from the entries above it, so an operator comparing
 * two runs is comparing the same measurements and not two differently
 * instrumented counters.
 */
function StatusBar({ stats, count }: { stats: ReturnType<typeof runStats>; count: number }) {
  const parts = [
    `${stats.turns} 轮 · ${stats.steps} 步 · ${stats.calls} 次调用`,
    `耗时 ${formatDuration(stats.durationMs)}`,
    `模型 ${formatDuration(stats.modelMs)} · 工具 ${formatDuration(stats.toolMs)}`,
    stats.firstTokenMs > 0
      ? `首 token 平均 ${formatDuration(stats.firstTokenMs)} · ${Math.round(stats.tokensPerSecond)} tok/s`
      : null,
    stats.cacheRatio > 0 ? `缓存命中 ${Math.round(stats.cacheRatio * 100)}%` : null,
    `输入 ${formatTokens(stats.inputTokens)} tok · 输出 ${formatTokens(stats.outputTokens)} tok`,
    `append-only ${count} 条`,
  ].filter(Boolean);
  return (
    <div className="border-border text-subtle-foreground flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-t px-4 py-1.5 text-[11px]">
      {parts.map((part, index) => (
        <span key={index} className="flex items-center gap-3">
          {index > 0 ? <span aria-hidden>·</span> : null}
          {part}
        </span>
      ))}
    </div>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-subtle-foreground text-[11px]">{label}</dt>
      <dd className="text-foreground mt-0.5 break-all">{value || "—"}</dd>
    </div>
  );
}

function Body({ title, value }: { title: string; value: string }) {
  return (
    <section className="mt-3 first:mt-0">
      <div className="mb-1 flex items-center gap-1">
        <h4 className="text-subtle-foreground text-[11px]">{title}</h4>
        <CopyIconButton value={value} label={`复制${title}`} className="-my-1 ml-auto size-6" />
      </div>
      <pre className="border-border bg-surface-muted/40 rounded-control overflow-auto border p-3 font-mono text-xs whitespace-pre-wrap">
        {value}
      </pre>
    </section>
  );
}

function badgeTone(kind: AITrajectoryKind): string {
  switch (kind) {
    case "error":
      return "bg-danger-surface text-danger";
    case "approval_request":
      return "bg-warning-surface text-warning";
    case "approval_decision":
    case "conclusion":
      return "bg-success-surface text-success";
    case "model":
    case "reasoning":
      return "bg-primary-surface text-primary";
    case "tool_call":
    case "tool_result":
      return "bg-info-surface text-info";
    default:
      return "bg-surface-muted text-muted-foreground";
  }
}

/**
 * What colour one block is.
 *
 * Distinct hues from the categorical chart palette rather than opacity steps of
 * one hue. The earlier scheme paired every kind with a faded version of its
 * neighbour — a model step against its reasoning, a call against its result —
 * and at eight pixels tall a 45% tint of the same colour is not a second
 * colour, it is a smudge.
 *
 * Hues repeat across lanes and never inside one. The lanes are labelled rows,
 * so a block's row already says whether it is input, model or tool work; what
 * the colour has to answer is which kind it is *within* that row, and holding
 * twelve mutually distinct hues would spend the whole palette to answer a
 * question nobody asks.
 */
function spanTone(span: TimelineSpan): string {
  if (span.failed) return "bg-danger";
  switch (span.kind) {
    // Lane 0 — what went in.
    case "input":
      return "bg-chart-1";
    case "context":
      return "bg-chart-5";
    case "system":
      return "bg-border-strong";
    // Lane 1 — what the model did. A streamed step draws its wait as the
    // lighter body and its generation as the solid tail over the top.
    case "model":
      return span.ttft === null ? "bg-chart-7" : "bg-chart-7/30";
    case "reasoning":
      return "bg-chart-4";
    case "compaction":
      return "bg-chart-5";
    case "conclusion":
      return "bg-chart-3";
    // Lane 2 — what it read, and who allowed it. Nothing here borrows the red a
    // failed block already owns: an approval waiting on a person is not a
    // failure, and at this size one red is the only red a reader can name.
    case "tool_call":
      return "bg-chart-2";
    case "tool_result":
      return "bg-chart-3";
    case "approval_request":
      return "bg-chart-1";
    case "approval_decision":
      return "bg-chart-4";
    default:
      return "bg-border-strong";
  }
}

function clamp(value: number, low: number, high: number): number {
  return Math.min(Math.max(value, low), Math.max(low, high));
}

function ordered(a: number, b: number): TimelineRange {
  return a <= b ? { start: a, end: b } : { start: b, end: a };
}

function centered(center: number, width: number, low: number, high: number): TimelineRange {
  const size = Math.min(high - low, width);
  const start = clamp(center - size / 2, low, high - size);
  return { start, end: start + size };
}
