import { useEffect, useRef, useState } from "react";
import {
  ArrowDown,
  Brain,
  ChevronRight,
  CircleAlert,
  GitBranch,
  Layers,
  LoaderCircle,
  ShieldQuestion,
  Sparkles,
  SquareArrowOutUpRight,
  Wrench,
} from "lucide-react";

import type { AIEvidence, AISession, AITrajectoryEntry } from "@/api/types";
import type { AILiveOutput } from "@/api/queries/aiops";
import { Button } from "@/components/ui/button";
import { HintTooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/cn";

import { openEvidence } from "../evidence-link";

import { CopyIconButton } from "./copy";
import { Opening } from "./opening";
import {
  compactionTriggerLabel,
  conversationItems,
  entryText,
  failureLabel,
  formatDuration,
  prettyBody,
  subtaskLabel,
  type ConversationBranch,
  type ConversationItem,
} from "./entries";
import { Markdown } from "./markdown";

export function Conversation({
  session,
  clusterName,
  entries,
  live,
  onDecide,
  onPick,
  deciding,
}: {
  session: AISession;
  clusterName: string;
  entries: AITrajectoryEntry[];
  live: AILiveOutput;
  onDecide: (callId: string, decision: "approved" | "denied") => void;
  /** Writes a suggested question into the composer's draft. */
  onPick: (prompt: string) => void;
  deciding: boolean;
}) {
  const items = conversationItems(entries);
  const scroller = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);
  const [atBottom, setAtBottom] = useState(true);

  // Following the answer is only helpful while the reader is already at the
  // bottom. Scrolling somebody back down while they are reading an earlier tool
  // result is the single most irritating thing a streaming view can do.
  //
  // Whether the view is pinned is also what the reader is told: while a turn is
  // streaming, everything new lands below the fold, and without a way back the
  // only route to the answer is to scroll a growing document by hand.
  useEffect(() => {
    const node = scroller.current;
    if (!node) return;
    if (pinned.current) node.scrollTop = node.scrollHeight;
    setAtBottom(node.scrollHeight - node.scrollTop - node.clientHeight < BOTTOM_SLACK);
  }, [items.length, live.text, live.reasoning]);

  const toBottom = () => {
    const node = scroller.current;
    if (!node) return;
    pinned.current = true;
    setAtBottom(true);
    node.scrollTo({ top: node.scrollHeight, behavior: "smooth" });
  };

  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div
        ref={scroller}
        onScroll={(event) => {
          const node = event.currentTarget;
          const bottom = node.scrollHeight - node.scrollTop - node.clientHeight < BOTTOM_SLACK;
          pinned.current = bottom;
          setAtBottom(bottom);
        }}
        className="min-h-0 flex-1 overflow-auto px-6 py-5"
      >
        {/* `min-h-full` rather than `h-full`: the column has to fill the panel so
          an empty conversation can centre its opening prompt, but a fixed
          height would let flex shrink real messages to fit instead. */}
        <div className="mx-auto flex min-h-full max-w-3xl flex-col gap-4">
          {items.length === 0 && !live.text && !live.reasoning && session.status !== "working" ? (
            <Opening
              clusterName={clusterName}
              ready
              disabled={Boolean(session.archived_at)}
              onPick={onPick}
            />
          ) : null}
          {items.map((item) => (
            <ConversationRow
              key={item.id}
              item={item}
              onDecide={onDecide}
              deciding={deciding}
              working={session.status === "working"}
            />
          ))}
          {live.text || live.reasoning ? <LiveOutput live={live} /> : null}
          {/* Only when nothing else already says the turn is busy. A tool card
            with its own spinner plus a second "working on it" line reads as two
            things happening when there is one. */}
          {session.status === "working" &&
          !live.text &&
          !live.reasoning &&
          !items.some((item) => item.type === "activity" && !item.result) &&
          !items.some((item) => item.type === "approval" && !item.decided) ? (
            <Thinking />
          ) : null}
        </div>
      </div>

      {/* Only while there is something below the fold to go back to. A control
          that is always there would sit over the last line of every answer for
          no reason — and even then it is a mark rather than a sentence: it
          appears over the answer being read, so it has to be small enough to be
          ignored and obvious enough to be aimed at. */}
      {!atBottom ? (
        <HintTooltip label="回到底部">
          <Button
            type="button"
            variant="secondary"
            size="icon-sm"
            aria-label="回到底部"
            onClick={toBottom}
            className="shadow-e2 zke-rise absolute bottom-3 left-1/2 size-7 -translate-x-1/2 rounded-full"
          >
            <ArrowDown aria-hidden />
          </Button>
        </HintTooltip>
      ) : null}
    </div>
  );
}

/** How close to the end still counts as reading the end of the conversation. */
const BOTTOM_SLACK = 80;

function ConversationRow({
  item,
  onDecide,
  deciding,
  working,
}: {
  item: ConversationItem;
  onDecide: (callId: string, decision: "approved" | "denied") => void;
  deciding: boolean;
  working: boolean;
}) {
  switch (item.type) {
    case "question":
      return <Question entry={item.entry} />;
    case "reasoning":
      return <Collapsible icon={Brain} title="思考过程" body={entryText(item.entry)} />;
    case "activity":
      return <Activity call={item.call} result={item.result} branches={item.branches} />;
    case "approval":
      return (
        <Approval
          entry={item.entry}
          decided={item.decided}
          working={working}
          deciding={deciding}
          onDecide={onDecide}
        />
      );
    case "answer":
      return <Answer entry={item.entry} />;
    case "narration":
      return <Narration entry={item.entry} />;
    case "note":
      return <Note entry={item.entry} />;
    case "error":
      return <Failure entry={item.entry} />;
    default:
      return null;
  }
}

function Question({ entry }: { entry: AITrajectoryEntry }) {
  const text = entryText(entry);
  return (
    // Hover-revealed rather than permanent: a question is usually one line, and
    // an icon parked under every one of them turns the operator's own half of
    // the conversation into a column of controls.
    <div className="group flex flex-col items-end">
      <div className="border-primary/25 bg-primary-surface text-foreground rounded-panel max-w-[85%] border px-3.5 py-2.5 text-[13px] leading-relaxed whitespace-pre-wrap">
        {text}
      </div>
      <div className="hoverless:opacity-100 -mr-1 opacity-0 transition-opacity duration-150 group-focus-within:opacity-100 group-hover:opacity-100">
        <CopyIconButton value={text} label="复制这条提问" />
      </div>
    </div>
  );
}

function Answer({ entry }: { entry: AITrajectoryEntry }) {
  const text = entryText(entry);
  return (
    <article className="flex gap-3">
      <Sparkles aria-hidden className="text-primary mt-0.5 size-4 shrink-0" />
      <div className="min-w-0 flex-1">
        <Markdown text={text} />
        {entry.content.evidence?.length ? <Evidence evidence={entry.content.evidence} /> : null}
        {/* Under the answer rather than beside it, and always drawn: an answer
            is what gets carried into a ticket or a handover, and a control that
            only appears on hover is one an operator has to already know is
            there. What lands on the clipboard is the Markdown the model wrote,
            not the rendered text — it is going somewhere that renders it. */}
        <div className="mt-1 -ml-1.5 flex items-center">
          <CopyIconButton value={text} label="复制这条回答" />
        </div>
      </div>
    </article>
  );
}

/**
 * One tool call and what it returned, folded into a single line.
 *
 * Collapsed by default and expanded on demand: the operator wants to know that
 * AIOps read the Deployment, and only sometimes wants the object. Leaving it
 * open would bury the answer under evidence.
 */
function Activity({
  call,
  result,
  branches,
}: {
  call: AITrajectoryEntry;
  result: AITrajectoryEntry | null;
  branches: ConversationBranch[];
}) {
  const [open, setOpen] = useState(false);
  const denied = call.content.authorized === false;
  const failed = result?.content.failed ?? false;
  const target = [call.content.target?.namespace, call.content.target?.name]
    .filter(Boolean)
    .join("/");
  return (
    <div className="border-border bg-surface rounded-panel border">
      <button
        type="button"
        onClick={() => setOpen((current) => !current)}
        aria-expanded={open}
        className="zke-focus rounded-panel flex w-full items-center gap-2 px-3 py-2 text-left"
      >
        <Wrench
          aria-hidden
          className={cn(
            "size-3.5 shrink-0",
            denied || failed ? "text-warning" : "text-muted-foreground",
          )}
        />
        <span className="text-foreground truncate font-mono text-xs">{call.content.tool}</span>
        {target ? (
          <span className="text-subtle-foreground truncate text-[11px]">{target}</span>
        ) : null}
        {result ? null : (
          <LoaderCircle aria-hidden className="text-primary size-3.5 shrink-0 animate-spin" />
        )}
        {branches.length > 0 ? (
          <span className="text-subtle-foreground shrink-0 text-[11px]">
            {branches.length} 个子任务
          </span>
        ) : null}
        <span className="text-subtle-foreground ml-auto flex shrink-0 items-center gap-2 text-[11px]">
          {denied ? "未执行" : failed ? "未取得结果" : result ? "已完成" : "执行中"}
          {result ? formatDuration(result.duration_ms) : null}
          <ChevronRight
            aria-hidden
            className={cn("size-3.5 transition-transform duration-150", open && "rotate-90")}
          />
        </span>
      </button>
      {open ? (
        <div className="border-border space-y-3 border-t px-3 py-2.5">
          <Block title="参数" body={call.content.arguments ?? "{}"} />
          {branches.map((branch) => (
            <Branch key={branch.id} branch={branch} />
          ))}
          {result ? (
            <Block title="结果" body={prettyBody(result)} truncated={result.truncated} />
          ) : null}
          {result?.content.evidence?.length ? (
            <Evidence evidence={result.content.evidence} />
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/**
 * One delegated branch, inside the call that delegated it.
 *
 * It shows the goal, what the branch read and what it concluded — the three
 * things somebody checking a folded answer actually wants — and deliberately
 * not the branch's model text step by step. A branch is a means to the folded
 * result above it; a reader who wants every step of it has the trajectory tab,
 * where the branch's entries sit in the same list as everything else.
 */
function Branch({ branch }: { branch: ConversationBranch }) {
  const [open, setOpen] = useState(false);
  const failure = branch.entries.find((entry) => entry.kind === "error");
  return (
    <div className="border-border rounded-control border">
      <button
        type="button"
        onClick={() => setOpen((current) => !current)}
        aria-expanded={open}
        className="zke-focus rounded-control flex w-full items-center gap-2 px-2.5 py-1.5 text-left"
      >
        <GitBranch aria-hidden className="text-muted-foreground size-3.5 shrink-0" />
        <span className="text-subtle-foreground shrink-0 text-[11px]">子任务 {branch.index}</span>
        <span className="text-foreground min-w-0 flex-1 truncate text-xs">{branch.goal}</span>
        {branch.conclusion || failure ? null : (
          <LoaderCircle aria-hidden className="text-primary size-3.5 shrink-0 animate-spin" />
        )}
        <span className="text-subtle-foreground shrink-0 text-[11px]">{branch.calls} 次调用</span>
        <ChevronRight
          aria-hidden
          className={cn(
            "text-subtle-foreground size-3.5 shrink-0 transition-transform duration-150",
            open && "rotate-90",
          )}
        />
      </button>
      {open ? (
        <div className="border-border space-y-2 border-t px-2.5 py-2">
          <ul className="space-y-1">
            {branch.entries
              .filter((entry) => entry.kind === "tool_call")
              .map((entry) => (
                <li
                  key={entry.sequence}
                  className="text-subtle-foreground flex items-center gap-2 font-mono text-[11px]"
                >
                  <Wrench aria-hidden className="size-3 shrink-0" />
                  <span className="truncate">{entry.content.tool}</span>
                  <span className="truncate">
                    {[entry.content.target?.namespace, entry.content.target?.name]
                      .filter(Boolean)
                      .join("/")}
                  </span>
                </li>
              ))}
          </ul>
          {branch.conclusion ? (
            <div className="text-foreground text-[13px]">
              <Markdown text={entryText(branch.conclusion)} />
            </div>
          ) : failure ? (
            <p className="text-warning text-[11px]">
              未完成：{failureLabel(failure.content.failure ?? "")}
            </p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function Approval({
  entry,
  decided,
  working,
  deciding,
  onDecide,
}: {
  entry: AITrajectoryEntry;
  decided: string | null;
  working: boolean;
  deciding: boolean;
  onDecide: (callId: string, decision: "approved" | "denied") => void;
}) {
  const callId = entry.content.call_id ?? "";
  // A parked branch has to say which branch it is: three of them asking at once
  // is a supported shape, and three identical cards would be unanswerable.
  const branch = subtaskLabel(entry);
  const target = [entry.content.target?.namespace, entry.content.target?.name]
    .filter(Boolean)
    .join("/");
  return (
    <div className="border-warning/40 bg-warning-surface rounded-panel border p-3">
      <div className="flex items-start gap-2">
        <ShieldQuestion aria-hidden className="text-warning mt-0.5 size-4 shrink-0" />
        <div className="min-w-0 flex-1">
          <p className="text-foreground text-[13px] font-medium">
            {branch ? `${branch}的 AIOps 请求执行敏感操作 ` : "AIOps 请求执行敏感操作 "}
            <span className="font-mono">{entry.content.tool}</span>
          </p>
          {target ? (
            <p className="text-muted-foreground mt-0.5 text-[11px]">目标 {target}</p>
          ) : null}
          <pre className="border-border bg-surface rounded-control mt-2 max-h-40 overflow-auto border p-2 font-mono text-xs whitespace-pre-wrap">
            {entry.content.arguments ?? "{}"}
          </pre>
        </div>
      </div>
      {decided ? (
        <p className="text-muted-foreground mt-2 text-[11px]">
          {decided === "approved" ? "已批准，运行继续。" : "已拒绝，运行在不执行它的前提下继续。"}
        </p>
      ) : working ? (
        <div className="mt-3 flex items-center gap-2">
          <Button
            size="sm"
            variant="primary"
            disabled={deciding}
            onClick={() => onDecide(callId, "approved")}
          >
            批准这次调用
          </Button>
          <Button size="sm" disabled={deciding} onClick={() => onDecide(callId, "denied")}>
            拒绝
          </Button>
          <span className="text-subtle-foreground text-[11px]">
            批准只在本次调用生效，不改变账户权限。
          </span>
        </div>
      ) : (
        <p className="text-muted-foreground mt-2 text-[11px]">运行已结束，这次请求不再等待答复。</p>
      )}
    </div>
  );
}

/** What the model said on its way to a tool call, rather than as an answer. */
function Narration({ entry }: { entry: AITrajectoryEntry }) {
  return (
    <p className="text-muted-foreground pl-7 text-[13px] leading-relaxed">{entryText(entry)}</p>
  );
}

function Note({ entry }: { entry: AITrajectoryEntry }) {
  const compaction = entry.content.compaction;
  return (
    <div className="text-subtle-foreground flex items-center gap-2 text-[11px]">
      <Layers aria-hidden className="size-3.5" />
      {compactionTriggerLabel(compaction?.trigger)}
      {compaction
        ? ` ${compaction.before_tokens} → ${compaction.after_tokens} tokens，` +
          `最近的步骤原样保留，更早的原文仍在轨迹与导出中`
        : null}
    </div>
  );
}

function Failure({ entry }: { entry: AITrajectoryEntry }) {
  return (
    <div className="border-danger/30 bg-danger-surface text-danger rounded-panel flex items-start gap-2 border px-3 py-2.5 text-[13px]">
      <CircleAlert aria-hidden className="mt-0.5 size-4 shrink-0" />
      <span>本轮运行结束：{failureLabel(entry.content.failure ?? "")}</span>
    </div>
  );
}

function LiveOutput({ live }: { live: AILiveOutput }) {
  return (
    <article className="flex gap-3">
      <Sparkles aria-hidden className="text-primary mt-0.5 size-4 shrink-0 animate-pulse" />
      <div className="min-w-0 flex-1 space-y-2">
        {live.reasoning ? (
          <p className="text-subtle-foreground text-xs leading-relaxed whitespace-pre-wrap">
            {live.reasoning}
          </p>
        ) : null}
        {live.text ? <Markdown text={live.text} /> : null}
      </div>
    </article>
  );
}

function Thinking() {
  return (
    <p className="text-subtle-foreground flex items-center gap-2 text-xs">
      <LoaderCircle aria-hidden className="size-3.5 animate-spin" />
      AIOps 正在查证…
    </p>
  );
}

function Collapsible({
  icon: Icon,
  title,
  body,
}: {
  icon: typeof Brain;
  title: string;
  body: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen((current) => !current)}
        aria-expanded={open}
        className="zke-focus rounded-inline text-subtle-foreground hover:text-muted-foreground inline-flex items-center gap-1.5 text-xs"
      >
        <Icon aria-hidden className="size-3.5" />
        {title}
        <ChevronRight
          aria-hidden
          className={cn("size-3.5 transition-transform duration-150", open && "rotate-90")}
        />
      </button>
      {open ? (
        <p className="border-border text-muted-foreground mt-1.5 border-l-2 pl-3 text-xs leading-relaxed whitespace-pre-wrap">
          {body}
        </p>
      ) : null}
    </div>
  );
}

function Block({ title, body, truncated }: { title: string; body: string; truncated?: boolean }) {
  return (
    <section>
      <div className="mb-1 flex items-center gap-1">
        <h4 className="text-subtle-foreground text-[11px]">{title}</h4>
        <CopyIconButton value={body} label={`复制${title}`} className="-my-1 ml-auto size-6" />
      </div>
      <pre className="border-border bg-surface-muted rounded-control max-h-72 overflow-auto border p-2.5 font-mono text-xs whitespace-pre-wrap">
        {body}
      </pre>
      {truncated ? (
        <p className="text-warning mt-1 text-[11px]">
          该条目只保留有界摘录，完整对象请通过下方证据入口查看。
        </p>
      ) : null}
    </section>
  );
}

/**
 * The references a claim rests on, each one a way into the view that shows it.
 *
 * A button rather than a link: the target is an application on this desktop,
 * and opening it in a second browser tab would reload the Console and leave the
 * operator to find their way back to the conversation they were reading.
 */
export function Evidence({ evidence }: { evidence: AIEvidence[] }) {
  return (
    <div className="mt-2 flex flex-wrap gap-1.5">
      {evidence.map((item, index) => (
        <button
          key={`${item.kind}-${item.cluster}-${item.name ?? item.query ?? index}`}
          type="button"
          className="zke-focus border-border bg-surface-muted text-primary rounded-control inline-flex items-center gap-1 border px-2 py-1 text-[11px] hover:underline"
          onClick={() =>
            openEvidence({
              kind: item.kind,
              cluster: item.cluster,
              tenantId: item.tenant_id,
              projectId: item.project_id,
              namespace: item.namespace,
              gvk: item.gvk,
              name: item.name,
              query: item.query,
            })
          }
          title={`在 ${item.kind === "metric" ? "可观测性" : "容器服务"}中打开 · 集群 ${item.cluster}`}
        >
          <SquareArrowOutUpRight aria-hidden className="size-3" />
          {evidenceLabel(item)}
        </button>
      ))}
    </div>
  );
}

function evidenceLabel(evidence: AIEvidence): string {
  // A reference without an object name points at a listing rather than a
  // thing. Naming the Kind is what tells "所有 Node" apart from the Cluster
  // snapshot — both are nameless, and calling both of them 概览 put two chips
  // with the same label and different destinations side by side.
  if (evidence.kind === "resource" && !evidence.name) {
    const kind = evidence.gvk?.split("/").pop();
    return kind ? `${kind} 列表` : "Cluster 概览";
  }
  const prefix = { resource: "", event: "Event ", metric: "指标 ", log: "日志 " }[evidence.kind];
  return `${prefix}${evidence.name ?? evidence.query ?? "证据"}`;
}
