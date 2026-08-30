import type { AISession, AITrajectoryEntry, AITrajectoryKind } from "@/api/types";

/**
 * Derivations over the trajectory.
 *
 * The trail is the only source: the conversation, the timeline and the run
 * statistics are all projections of the same append-only list, so what an
 * operator reads in one tab cannot disagree with the other. Nothing here holds
 * state, which is what lets a reconnect rebuild every view from the entries it
 * replays.
 */

export const KIND_LABELS: Record<AITrajectoryKind, string> = {
  system: "运行时上下文",
  input: "用户输入",
  context: "附件上下文",
  model: "模型步骤",
  reasoning: "思考",
  tool_call: "工具调用",
  tool_result: "工具结果",
  approval_request: "等待批准",
  approval_decision: "批准结果",
  compaction: "上下文压缩",
  conclusion: "结论",
  error: "运行结束",
};

export const KIND_CODES: Record<AITrajectoryKind, string> = {
  system: "SYS",
  input: "USR",
  context: "CTX",
  model: "LLM",
  reasoning: "THK",
  tool_call: "CALL",
  tool_result: "TOOL",
  approval_request: "ASK",
  approval_decision: "ACK",
  compaction: "CMP",
  conclusion: "END",
  error: "ERR",
};

const KIND_ORDER: AITrajectoryKind[] = [
  "system",
  "input",
  "context",
  "reasoning",
  "model",
  "tool_call",
  "tool_result",
  "approval_request",
  "approval_decision",
  "compaction",
  "conclusion",
  "error",
];

export function failureLabel(failure: string): string {
  return (
    {
      model_unavailable: "模型不可用",
      model_timeout: "模型超时",
      model_rate_limited: "模型端点持续限流",
      model_quota_exhausted: "模型账户额度已用尽",
      model_rejected: "模型端点拒绝了请求或凭证",
      permission_revoked: "权限已撤销",
      session_ended: "运行已取消",
      budget_exceeded: "上下文预算不足",
      agent_offline: "Cluster Agent 离线",
      context_compaction_failed: "上下文压缩失败",
      step_budget_exhausted: "步骤预算已用尽，未收敛到结论",
      tool_budget_exhausted: "工具调用预算已用尽",
      approval_timeout: "等待批准超时",
      interrupted: "Server 重启导致运行中断",
    }[failure] ?? failure
  );
}

/**
 * Why a compaction ran.
 *
 * Reaching the threshold and being refused by the endpoint look the same in the
 * trail — a checkpoint appeared — but they mean different things about the
 * deployment: the first is the policy working, and the second is the endpoint's
 * accounting disagreeing with ours.
 */
export function compactionTriggerLabel(trigger?: string): string {
  return (
    { pressure: "自动压缩", context_overflow: "端点拒绝过大请求后压缩" }[trigger ?? "pressure"] ??
    "自动压缩"
  );
}

/** How the checkpoint was written. */
export function compactionMethodLabel(method: string): string {
  return { model_summary: "模型摘要", summary: "机械摘要" }[method] ?? method;
}

export function streamLabel(state: string): string {
  return (
    { connecting: "连接中", open: "已连接", reconnecting: "重连中", closed: "已断开" }[state] ??
    state
  );
}

export function approvalModeLabel(mode: AISession["approval_mode"]): string {
  return { ask: "请求批准", assisted: "帮我批准", full: "完全访问" }[mode] ?? mode;
}

/**
 * One delegated branch, folded out of the main line.
 *
 * A branch writes its own steps into the same trail as the turn that spawned
 * it, which is what makes the run reviewable — and what would make the
 * conversation unreadable if it were rendered inline: three branches reading a
 * Cluster at once produce three interleaved streams of tool calls under a
 * question nobody asked three times. The stamp on each entry is what lets the
 * conversation put them back where they belong, under the one call that
 * delegated them.
 */
export type ConversationBranch = {
  id: string;
  index: number;
  goal: string;
  /** The branch's own trail, in order. */
  entries: AITrajectoryEntry[];
  /** What it came back with, absent while it is still working. */
  conclusion: AITrajectoryEntry | null;
  calls: number;
};

/** One line of the conversation tab. */
export type ConversationItem =
  | { id: string; type: "question"; entry: AITrajectoryEntry }
  | { id: string; type: "answer"; entry: AITrajectoryEntry }
  | { id: string; type: "narration"; entry: AITrajectoryEntry }
  | { id: string; type: "reasoning"; entry: AITrajectoryEntry }
  | { id: string; type: "note"; entry: AITrajectoryEntry }
  | { id: string; type: "error"; entry: AITrajectoryEntry }
  | { id: string; type: "approval"; entry: AITrajectoryEntry; decided: string | null }
  /** A view AIOps opened, or offered to open, on the operator's desktop. */
  | { id: string; type: "view"; entry: AITrajectoryEntry }
  | {
      id: string;
      type: "activity";
      call: AITrajectoryEntry;
      result: AITrajectoryEntry | null;
      /** Empty for every call except a delegation. */
      branches: ConversationBranch[];
    };

/**
 * The conversation, as a person reads it.
 *
 * A tool call and its result are one line rather than two: what an operator
 * wants to see is "it read the Pod logs, here is what came back", and splitting
 * that across two bubbles turns a short investigation into a wall. The full
 * pair is still two entries in the trajectory tab, which is where the record
 * lives.
 */
export function conversationItems(entries: AITrajectoryEntry[]): ConversationItem[] {
  const resultsByCall = new Map<string, AITrajectoryEntry>();
  const decisionsByCall = new Map<string, string>();
  for (const entry of entries) {
    const callId = entry.content.call_id;
    if (!callId) continue;
    if (entry.kind === "tool_result") resultsByCall.set(callId, entry);
    if (entry.kind === "approval_decision" && entry.content.decision) {
      decisionsByCall.set(callId, entry.content.decision);
    }
  }
  const branchesByCall = groupBranches(entries);
  const items: ConversationItem[] = [];
  for (const entry of entries) {
    const id = String(entry.sequence);
    // A branch's own steps belong under the call that delegated them, not in
    // the conversation. The exception is an approval: a branch parked on a
    // person has to be answerable where the person is looking, and folding the
    // request away would stall the run behind a disclosure triangle.
    if (entry.content.subtask && entry.kind !== "approval_request") continue;
    switch (entry.kind) {
      case "input":
        items.push({ id, type: "question", entry });
        break;
      case "reasoning":
        items.push({ id, type: "reasoning", entry });
        break;
      case "conclusion":
        items.push({ id, type: "answer", entry });
        break;
      case "model":
        // A step that explained itself before calling a tool is worth showing;
        // a step that only produced the final answer is not, because the
        // `conclusion` entry right after it carries the same text and rendering
        // both would print the answer twice.
        if (entry.content.tools?.length && entry.content.text?.trim()) {
          items.push({ id, type: "narration", entry });
        }
        break;
      case "compaction":
        items.push({ id, type: "note", entry });
        break;
      case "error":
        items.push({ id, type: "error", entry });
        break;
      case "approval_request":
        items.push({
          id,
          type: "approval",
          entry,
          decided: decisionsByCall.get(entry.content.call_id ?? "") ?? null,
        });
        break;
      case "tool_result":
        // Results are otherwise folded into the call above them. A view intent
        // is the exception because it is not a reading: it is something that
        // happened on the operator's own screen, and an account of that cannot
        // sit behind a disclosure triangle nobody opened.
        if (entry.content.view) items.push({ id, type: "view", entry });
        break;
      case "tool_call": {
        const result = resultsByCall.get(entry.content.call_id ?? "") ?? null;
        // The card below carries the call, the target and the reason for it.
        // Drawing the generic tool line as well would say the same thing twice,
        // less clearly. A refused open — no permission, or the turn's one move
        // already spent — carries no view and stays an ordinary tool line.
        if (result?.content.view) break;
        items.push({
          id,
          type: "activity",
          call: entry,
          result,
          branches: branchesByCall.get(entry.content.call_id ?? "") ?? [],
        });
        break;
      }
      default:
        break;
    }
  }
  return items;
}

/**
 * Every branch in the trail, filed under the call that delegated it.
 *
 * Ordered by the stamp's index rather than by when entries arrived: branches
 * run concurrently, so arrival order is the scheduler's answer and not the
 * model's — and a list that reorders itself between two renders of the same
 * finished run is one nobody can point at.
 */
function groupBranches(entries: AITrajectoryEntry[]): Map<string, ConversationBranch[]> {
  const byCall = new Map<string, Map<string, ConversationBranch>>();
  for (const entry of entries) {
    const stamp = entry.content.subtask;
    if (!stamp) continue;
    let branches = byCall.get(stamp.call_id);
    if (!branches) {
      branches = new Map();
      byCall.set(stamp.call_id, branches);
    }
    let branch = branches.get(stamp.id);
    if (!branch) {
      branch = {
        id: stamp.id,
        index: stamp.index,
        goal: "",
        entries: [],
        conclusion: null,
        calls: 0,
      };
      branches.set(stamp.id, branch);
    }
    // The goal is carried only on the entry that opens the branch, so it is
    // taken wherever it appears rather than assumed to be on the first row.
    if (stamp.goal) branch.goal = stamp.goal;
    branch.entries.push(entry);
    if (entry.kind === "tool_call") branch.calls += 1;
    if (entry.kind === "conclusion") branch.conclusion = entry;
  }
  const grouped = new Map<string, ConversationBranch[]>();
  for (const [callId, branches] of byCall) {
    grouped.set(
      callId,
      [...branches.values()].sort((left, right) => left.index - right.index),
    );
  }
  return grouped;
}

/**
 * The calls the turn is currently parked on.
 *
 * Plural because delegation made it plural: three branches may each reach a
 * sensitive read at once, and a banner that named only the newest would leave
 * the operator answering one request while the run waits on two more they were
 * never told about.
 *
 * Derived from the pair of entries rather than from a session status field: a
 * request answered by a decision is finished, and everything else the runtime
 * could store about it would be a second copy of that fact able to disagree.
 */
export function pendingApprovals(
  entries: AITrajectoryEntry[],
  session: AISession,
): AITrajectoryEntry[] {
  if (session.status !== "working") return [];
  const decided = new Set(
    entries
      .filter((entry) => entry.kind === "approval_decision")
      .map((entry) => entry.content.call_id ?? ""),
  );
  return entries.filter(
    (entry) => entry.kind === "approval_request" && !decided.has(entry.content.call_id ?? ""),
  );
}

/** How a branch is named wherever one entry has to say which it came from. */
export function subtaskLabel(entry: AITrajectoryEntry): string | null {
  const stamp = entry.content.subtask;
  return stamp ? `子任务 ${stamp.index}` : null;
}

export type RunStats = {
  turns: number;
  steps: number;
  calls: number;
  durationMs: number;
  modelMs: number;
  toolMs: number;
  firstTokenMs: number;
  tokensPerSecond: number;
  cacheRatio: number;
  inputTokens: number;
  outputTokens: number;
};

/**
 * What the run cost, measured from the entries rather than from a counter.
 *
 * A counter would have to survive a reconnect, a Server restart and a session
 * reopened days later; recomputing from the trail cannot drift from it.
 */
export function runStats(entries: AITrajectoryEntry[]): RunStats {
  const stats: RunStats = {
    turns: new Set(entries.map((entry) => entry.turn)).size,
    steps: 0,
    calls: 0,
    durationMs: 0,
    modelMs: 0,
    toolMs: 0,
    firstTokenMs: 0,
    tokensPerSecond: 0,
    cacheRatio: 0,
    inputTokens: 0,
    outputTokens: 0,
  };
  let firstTokenSamples = 0;
  let firstTokenTotal = 0;
  let cachedTokens = 0;
  for (const entry of entries) {
    if (entry.kind === "model") {
      stats.steps += 1;
      stats.modelMs += entry.content.timing?.elapsed_ms ?? entry.duration_ms;
      const firstToken = entry.content.timing?.first_token_ms ?? 0;
      if (firstToken > 0) {
        firstTokenSamples += 1;
        firstTokenTotal += firstToken;
      }
      const tokens = entry.content.tokens;
      if (tokens) {
        stats.inputTokens += tokens.input;
        stats.outputTokens += tokens.output;
        cachedTokens += tokens.cached_input ?? 0;
      }
    }
    if (entry.kind === "tool_call") stats.calls += 1;
    if (entry.kind === "tool_result") stats.toolMs += entry.duration_ms;
  }
  const first = entries.at(0);
  const last = entries.at(-1);
  if (first && last) {
    stats.durationMs = Math.max(
      0,
      new Date(last.occurred_at).getTime() +
        last.duration_ms -
        new Date(first.occurred_at).getTime(),
    );
  }
  if (firstTokenSamples > 0) stats.firstTokenMs = firstTokenTotal / firstTokenSamples;
  if (stats.modelMs > 0) stats.tokensPerSecond = (stats.outputTokens * 1_000) / stats.modelMs;
  if (stats.inputTokens > 0) stats.cacheRatio = cachedTokens / stats.inputTokens;
  return stats;
}

/** How the timeline lays the run out horizontally. */
export type TimelineMode = "duration" | "sequence";

/** 0 输入 · 1 模型 · 2 工具. Fixed, so a lane means the same thing in every run. */
export type TimelineLane = 0 | 1 | 2;

export type TimelineSpan = {
  sequence: number;
  /** Domain units: milliseconds since the run started, or the step index. */
  start: number;
  end: number;
  kind: AITrajectoryKind;
  lane: TimelineLane;
  failed: boolean;
  /**
   * Where the first token landed inside a streamed model step, as a fraction of
   * the span. It is the one measurement that splits waiting from generating,
   * and a single flat bar cannot show which of the two a slow step was.
   */
  ttft: number | null;
};

export type TimelineModel = {
  mode: TimelineMode;
  start: number;
  end: number;
  spans: TimelineSpan[];
  /** Where each turn begins, in the same domain as the spans. */
  boundaries: { turn: number; at: number }[];
  /** Wall-clock origin, so a domain offset can be named as a time of day. */
  origin: number;
};

export type TimelineRange = { start: number; end: number };

function laneOf(kind: AITrajectoryKind): TimelineLane {
  switch (kind) {
    case "system":
    case "input":
    case "context":
      return 0;
    case "tool_call":
    case "tool_result":
    case "approval_request":
    case "approval_decision":
      return 2;
    default:
      return 1;
  }
}

/**
 * The run as three tracks, in one of two projections.
 *
 * Separating input, model and tools is what makes the shape of a turn legible
 * at a glance: a long model bar next to an empty tool track is a model thinking
 * without evidence, and a row of tool bars with short model bars between them is
 * an investigation. A single merged track cannot show either.
 *
 * `duration` places every entry on the wall clock at its recorded length, which
 * is what a run is actually shaped like — and which turns a run whose cost is
 * one 40-second model call into one wide bar and a crowd of slivers. `sequence`
 * gives every entry the same width, which is the only projection in which the
 * slivers can be pointed at. Neither is a default the other can replace.
 */
export function timelineModel(
  entries: AITrajectoryEntry[],
  mode: TimelineMode,
): TimelineModel | null {
  const first = entries[0];
  if (!first) return null;
  const origin = new Date(first.occurred_at).getTime();
  const spans: TimelineSpan[] = [];
  const boundaries: { turn: number; at: number }[] = [];
  const seenTurns = new Set<number>();

  entries.forEach((entry, index) => {
    const timing = entry.content.timing;
    const elapsed = timing?.elapsed_ms ?? entry.duration_ms;
    const ttft =
      entry.kind === "model" && timing?.streamed && timing.first_token_ms && elapsed > 0
        ? Math.min(1, Math.max(0, timing.first_token_ms / elapsed))
        : null;
    const start = mode === "duration" ? new Date(entry.occurred_at).getTime() - origin : index;
    const end = mode === "duration" ? start + Math.max(0, entry.duration_ms) : index + 1;
    if (!seenTurns.has(entry.turn)) {
      seenTurns.add(entry.turn);
      boundaries.push({ turn: entry.turn, at: start });
    }
    spans.push({
      sequence: entry.sequence,
      start,
      end,
      kind: entry.kind,
      lane: laneOf(entry.kind),
      failed: entry.kind === "error" || entry.content.failed === true,
      ttft,
    });
  });

  const start = Math.min(...spans.map((span) => span.start));
  // A run whose entries all landed in the same millisecond still has to be
  // drawable, so the domain never collapses to a point.
  const end = Math.max(start + 1, ...spans.map((span) => span.end));
  return { mode, start, end, spans, boundaries, origin };
}

/** The entries a selected window touches, at any point inside it. */
export function sequencesInRange(model: TimelineModel, range: TimelineRange): Set<number> {
  return new Set(
    model.spans
      .filter((span) => span.start <= range.end && span.end >= range.start)
      .map((span) => span.sequence),
  );
}

/**
 * One ledger row: an entry, plus the calls it made.
 *
 * A model step and the tools it requested are one act of the run, and the
 * ledger folds them accordingly — otherwise a five-step investigation is forty
 * rows, and the shape of the reasoning is lost between them. Only calls fold:
 * a `reasoning` entry is what the model thought before choosing, not something
 * a step performed, so it keeps its own row.
 */
export type LedgerRow = { entry: AITrajectoryEntry; children: AITrajectoryEntry[] };
export type LedgerTurn = { turn: number; rows: LedgerRow[]; count: number };

const CALL_KINDS = new Set<AITrajectoryKind>([
  "tool_call",
  "tool_result",
  "approval_request",
  "approval_decision",
]);

export function ledgerTurns(entries: AITrajectoryEntry[]): LedgerTurn[] {
  const turns: LedgerTurn[] = [];
  let turn: LedgerTurn | null = null;
  let parent: LedgerRow | null = null;
  for (const entry of entries) {
    if (!turn || turn.turn !== entry.turn) {
      turn = { turn: entry.turn, rows: [], count: 0 };
      turns.push(turn);
      parent = null;
    }
    turn.count += 1;
    if (CALL_KINDS.has(entry.kind) && parent) {
      parent.children.push(entry);
      continue;
    }
    const row: LedgerRow = { entry, children: [] };
    turn.rows.push(row);
    // Only a model step owns calls. Anything else closes the fold, so a tool
    // result that arrives after the conclusion is not filed under it.
    parent = entry.kind === "model" ? row : null;
  }
  return turns;
}

/** Applies a row-level predicate, keeping a parent whose calls still match. */
export function filterLedger(
  turns: LedgerTurn[],
  keep: (entry: AITrajectoryEntry) => boolean,
): LedgerTurn[] {
  const result: LedgerTurn[] = [];
  for (const turn of turns) {
    const rows: LedgerRow[] = [];
    for (const row of turn.rows) {
      const children = row.children.filter(keep);
      if (keep(row.entry) || children.length > 0) rows.push({ entry: row.entry, children });
    }
    if (rows.length > 0) {
      result.push({
        turn: turn.turn,
        rows,
        count: rows.reduce((total, row) => total + 1 + row.children.length, 0),
      });
    }
  }
  return result;
}

export function presentKinds(entries: AITrajectoryEntry[]): AITrajectoryKind[] {
  const present = new Set(entries.map((entry) => entry.kind));
  return KIND_ORDER.filter((kind) => present.has(kind));
}

export function entryTitle(entry: AITrajectoryEntry): string {
  if (entry.content.tool) {
    const suffix = {
      tool_call: "调用",
      tool_result: "结果",
      approval_request: "等待批准",
      approval_decision: "批准结果",
    }[entry.kind as string];
    return `${entry.content.tool} ${suffix ?? ""}`.trim();
  }
  const text = entryText(entry).replaceAll(/\s+/g, " ").trim();
  return text || KIND_LABELS[entry.kind];
}

export function entryText(entry: AITrajectoryEntry): string {
  if (entry.content.text) return entry.content.text;
  if (entry.kind === "approval_decision") {
    return entry.content.decision === "approved" ? "用户已批准" : "用户已拒绝";
  }
  if (entry.kind === "compaction" && entry.content.compaction) {
    const compaction = entry.content.compaction;
    return `${compactionTriggerLabel(compaction.trigger)}：${compaction.before_tokens} → ${compaction.after_tokens} tokens`;
  }
  if (entry.kind === "error") return failureLabel(entry.content.failure ?? "");
  if (entry.kind === "model" && entry.content.tools?.length) {
    return `请求调用 ${entry.content.tools.join("、")}`;
  }
  return KIND_LABELS[entry.kind];
}

/** Pretty-prints a tool result that happens to be JSON, and leaves the rest alone. */
export function prettyBody(entry: AITrajectoryEntry): string {
  const text = entryText(entry);
  if (entry.kind !== "tool_result" && entry.kind !== "tool_call") return text;
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

export function formatDuration(milliseconds: number): string {
  if (milliseconds < 1_000) return `${Math.round(milliseconds)} ms`;
  if (milliseconds < 60_000) return `${(milliseconds / 1_000).toFixed(1)} s`;
  return `${Math.floor(milliseconds / 60_000)}m ${Math.round((milliseconds % 60_000) / 1_000)}s`;
}

/** A moment in the run, named as a wall clock time. */
export function formatClock(timestamp: number): string {
  return new Date(timestamp).toLocaleTimeString("zh-CN", { hour12: false });
}

export function formatTokens(value: number): string {
  if (value < 1_000) return String(value);
  if (value < 1_000_000) return `${(value / 1_000).toFixed(1)}K`;
  return `${(value / 1_000_000).toFixed(1)}M`;
}

/**
 * Buckets the session list the way a person remembers their own work: what they
 * did today, this week, and everything older.
 *
 * There is no bucket for the last hour. The list is already newest first, so
 * the conversation an operator was just in is the row under the header either
 * way — and splitting one recent hour out of today gave the top of the rail a
 * heading that changed meaning as the hour passed, above a group of one.
 */
export function groupSessions(sessions: AISession[]): { label: string; sessions: AISession[] }[] {
  const now = Date.now();
  const buckets: { label: string; limit: number; sessions: AISession[] }[] = [
    { label: "今天", limit: 24 * 60 * 60 * 1_000, sessions: [] },
    { label: "本周", limit: 7 * 24 * 60 * 60 * 1_000, sessions: [] },
    { label: "更早", limit: Number.POSITIVE_INFINITY, sessions: [] },
  ];
  const fallback = buckets[buckets.length - 1];
  for (const session of sessions) {
    const age = now - new Date(session.last_activity_at).getTime();
    const bucket = buckets.find((candidate) => age < candidate.limit) ?? fallback;
    bucket?.sessions.push(session);
  }
  return buckets.filter((bucket) => bucket.sessions.length > 0);
}
