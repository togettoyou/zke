import { useRef, useState, type FormEvent, type KeyboardEvent, type RefObject } from "react";
import {
  BookOpen,
  Check,
  FileText,
  Hand,
  Paperclip,
  Send,
  ShieldCheck,
  Square,
  Wrench,
  X,
  Zap,
} from "lucide-react";

import type { AIAttachment, AIContextUsage, AISession, AISkill, AITool } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/cn";

import { approvalModeLabel } from "./entries";

const APPROVAL_MODES: {
  value: AISession["approval_mode"];
  icon: typeof Hand;
  description: string;
}[] = [
  { value: "ask", icon: Hand, description: "敏感操作始终请求批准" },
  { value: "assisted", icon: ShieldCheck, description: "仅对检测到的敏感操作请求批准" },
  { value: "full", icon: Zap, description: "不再停下来询问，权限边界不变" },
];

/**
 * The composer.
 *
 * Everything that decides what the next turn is allowed to do sits on one row
 * under the box: the attachments it will read, the approval mode it will run
 * under, and what it can call. Putting the mode anywhere else would mean an
 * operator watching AIOps work has to leave the conversation to change how far
 * it may go — which is exactly the moment they want to change it.
 *
 * It takes the mode and the run state rather than the session, because the box
 * exists before the session does: an empty workspace is a conversation nobody
 * has started yet, and the first thing it has to offer is somewhere to type.
 * There the send is what creates the session, and the two controls that need a
 * session id — the attachments — are simply absent rather than present and
 * refusing.
 */
export function Composer({
  approvalMode,
  working,
  attachments,
  tools,
  skills,
  context,
  disabled,
  pending,
  draft,
  onDraft,
  inputRef,
  disabledPlaceholder,
  onSend,
  onStop,
  onAttach,
  onRemoveAttachment,
  onApprovalMode,
}: {
  approvalMode: AISession["approval_mode"];
  working: boolean;
  /** Omitted before the session exists, which is what the Server hangs them on. */
  attachments?: AIAttachment[];
  tools: AITool[];
  /**
   * The playbooks the runtime offers. Shown next to the tools because they are
   * the same kind of fact about what the next turn can do — with the difference
   * stated in the panel: a skill decides an order, never a permission.
   */
  skills: AISkill[];
  /**
   * How full the model context is. Absent before the session exists and on a
   * deployment whose endpoint is not configured, where there is no window to
   * measure against and the meter simply does not appear.
   */
  context?: AIContextUsage;
  disabled: boolean;
  pending: boolean;
  /**
   * Held by the owner, because a suggestion picked from the empty conversation
   * has to land in this box as text the operator can still change before it is
   * sent.
   */
  draft: string;
  onDraft: (draft: string) => void;
  /** Lets the owner put the caret here after filling the draft for them. */
  inputRef?: RefObject<HTMLTextAreaElement | null>;
  /** Why the box is refusing input, when it is not the archived session. */
  disabledPlaceholder?: string;
  onSend: (text: string) => void;
  onStop?: () => void;
  onAttach?: (file: File) => void;
  onRemoveAttachment?: (attachmentId: string) => void;
  onApprovalMode: (mode: AISession["approval_mode"]) => void;
}) {
  const fileInput = useRef<HTMLInputElement | null>(null);
  const files = attachments ?? [];

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const text = draft.trim();
    if (!text || working || disabled) return;
    onSend(text);
    onDraft("");
  };

  // Enter sends and Shift+Enter breaks the line, because this box is a message
  // and not a document. A newline is still reachable, which a plain Enter-only
  // form would not leave.
  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault();
      submit(event);
    }
  };

  return (
    <form onSubmit={submit} className="shrink-0 px-6 pt-2 pb-4">
      {/* The halo belongs to the whole box: the textarea has no chrome of its
          own here, so focus has to land on the container the operator sees. */}
      <div className="zke-focus-within border-border bg-surface rounded-panel shadow-e1 mx-auto max-w-3xl border transition-[border-color,box-shadow] duration-150">
        {files.length > 0 ? (
          <div className="border-border flex flex-wrap gap-1.5 border-b px-3 py-2">
            {files.map((attachment) => (
              <span
                key={attachment.id}
                className="border-border bg-surface-muted text-muted-foreground rounded-control inline-flex items-center gap-1 border px-2 py-1 text-[11px]"
              >
                <FileText aria-hidden className="size-3.5" />
                <span className="max-w-40 truncate">{attachment.name}</span>
                <Button
                  type="button"
                  size="icon-sm"
                  variant="ghost"
                  className="-mr-1 size-5"
                  aria-label={`移除附件 ${attachment.name}`}
                  disabled={working}
                  onClick={() => onRemoveAttachment?.(attachment.id)}
                >
                  <X aria-hidden />
                </Button>
              </span>
            ))}
          </div>
        ) : null}
        {/* A bare textarea rather than the shared `Textarea`, which carries
            `zke-focus` — the halo is `box-shadow` set by a utility class, so a
            `focus-visible:shadow-none` override does not remove it, and the
            focused field drew a 3px ring inside the panel that read as a rule
            across the middle of the box. The chrome belongs to the container
            here; the field itself must have none.

            The height is fixed rather than grown from the content: the box sits
            at the bottom of the conversation, so every line it grows pushes the
            answer being read upwards. A long question scrolls inside the field
            instead of moving everything above it. */}
        <textarea
          ref={inputRef}
          value={draft}
          onChange={(event) => onDraft(event.target.value)}
          onKeyDown={onKeyDown}
          aria-label="向 AIOps 发送消息"
          placeholder={
            disabled
              ? (disabledPlaceholder ?? "会话已归档，无法继续对话。")
              : "描述要排查的问题，或直接问它集群现在怎么样。"
          }
          disabled={working || disabled}
          rows={3}
          className="text-foreground placeholder:text-subtle-foreground h-24 w-full resize-none overflow-y-auto border-0 bg-transparent px-3.5 pt-3 pb-1 text-sm leading-relaxed outline-none disabled:cursor-not-allowed disabled:opacity-60"
        />
        <div className="flex items-center gap-2 px-2.5 pb-2.5">
          {onAttach ? (
            <>
              <input
                ref={fileInput}
                className="hidden"
                type="file"
                accept=".txt,.md,.json,.yaml,.yml,text/plain,text/markdown,application/json,application/yaml"
                onChange={(event) => {
                  const file = event.target.files?.[0];
                  if (file) onAttach(file);
                  event.target.value = "";
                }}
              />
              <Button
                type="button"
                size="icon-sm"
                variant="ghost"
                aria-label="添加文本附件"
                title="文本 / Markdown / JSON / YAML，最多 256 KiB"
                disabled={working || disabled}
                onClick={() => fileInput.current?.click()}
              >
                <Paperclip aria-hidden />
              </Button>
            </>
          ) : null}

          <ApprovalModePicker mode={approvalMode} onChange={onApprovalMode} />
          <ToolsChip tools={tools} />
          <SkillsChip skills={skills} />

          {/* The keyboard contract, stated where the keys are used. It is the
              first thing the box gets wrong for somebody who expects Enter to
              be a newline, and the last place they would go looking for it is
              documentation. Dropped at narrow widths, where the row has to
              stay usable before it can be helpful. */}
          <span className="text-subtle-foreground ml-auto text-[11px] max-[760px]:hidden">
            Enter 发送 · Shift + Enter 换行
          </span>
          <span className="max-[760px]:ml-auto">
            <ContextMeter context={context} />
          </span>
          <div className="ml-1">
            {working && onStop ? (
              <Button type="button" variant="danger" size="sm" onClick={onStop}>
                <Square aria-hidden /> 停止
              </Button>
            ) : (
              <Button
                type="submit"
                variant="primary"
                size="sm"
                disabled={!draft.trim() || disabled || pending}
              >
                <Send aria-hidden /> 发送
              </Button>
            )}
          </div>
        </div>
      </div>
    </form>
  );
}

/** Ring geometry: a 14px viewBox with a 2px stroke. */
const RING_RADIUS = 5.5;
const RING_CIRCUMFERENCE = 2 * Math.PI * RING_RADIUS;

/**
 * The context meter: how much of the model's context window this conversation
 * currently occupies.
 *
 * It sits beside the send button because that is where the next request is
 * about to be made, and the thing it warns about — a long investigation being
 * compacted — happens between one question and the next. Without it the first
 * an operator knows about compaction is a checkpoint appearing in the trail.
 *
 * The ring's fill is the Server's measurement, which prefers the endpoint's own
 * reported usage. The coloured parts of the panel's bar are the local
 * heuristic: no endpoint reports how its input divided between the instruction,
 * the tool schemas and the conversation, so the split explains the total rather
 * than deciding anything.
 *
 * It opens on hover rather than on click. Nothing in the panel is actionable —
 * it is a reading, and a reading that costs a click and a second click to
 * dismiss is one nobody takes. The trigger stays a focusable button so the same
 * panel is reachable from the keyboard.
 */
function ContextMeter({ context }: { context?: AIContextUsage }) {
  if (!context || context.context_window_tokens <= 0) return null;
  const percent = Math.min(
    100,
    Math.round((context.used_tokens / context.context_window_tokens) * 100),
  );
  const threshold = Math.min(
    100,
    Math.round((context.threshold_tokens / context.context_window_tokens) * 100),
  );
  const parts = [
    { key: "system", label: "系统提示词", tokens: context.system_tokens, color: "bg-primary" },
    { key: "tools", label: "工具 Schema", tokens: context.tools_tokens, color: "bg-info" },
    { key: "messages", label: "对话消息", tokens: context.message_tokens, color: "bg-success" },
  ];
  const breakdown = parts.reduce((total, part) => total + part.tokens, 0);
  const nearing = percent >= threshold;
  return (
    <Tooltip delayDuration={120}>
      <TooltipTrigger asChild>
        <Button type="button" size="icon-sm" variant="ghost" aria-label={`上下文已用 ${percent}%`}>
          <svg viewBox="0 0 14 14" className="size-3.5" aria-hidden>
            <circle
              cx="7"
              cy="7"
              r={RING_RADIUS}
              fill="none"
              strokeWidth="2"
              className="stroke-border"
            />
            <circle
              cx="7"
              cy="7"
              r={RING_RADIUS}
              fill="none"
              strokeWidth="2"
              strokeLinecap="round"
              strokeDasharray={`${(RING_CIRCUMFERENCE * percent) / 100} ${RING_CIRCUMFERENCE}`}
              transform="rotate(-90 7 7)"
              className={nearing ? "stroke-warning" : "stroke-muted-foreground"}
            />
          </svg>
        </Button>
      </TooltipTrigger>
      <TooltipContent
        align="end"
        side="top"
        className="rounded-panel shadow-e3 w-80 max-w-none p-3 text-[11px]"
      >
        <div className="flex items-baseline justify-between">
          <span className="text-foreground text-[13px] font-medium">上下文已用 {percent}%</span>
          <span className="text-subtle-foreground text-[11px]">
            {formatTokens(context.used_tokens)} / {formatTokens(context.context_window_tokens)}
          </span>
        </div>
        <div className="bg-surface-muted rounded-inline relative mt-2 flex h-2 overflow-hidden">
          {parts
            .filter((part) => part.tokens > 0 && breakdown > 0)
            .map((part) => (
              <div
                key={part.key}
                className={part.color}
                style={{ width: `${(percent * part.tokens) / breakdown}%` }}
              />
            ))}
        </div>
        <dl className="mt-2.5 space-y-1">
          {parts.map((part) => (
            <div key={part.key} className="flex items-center gap-2 text-[11px]">
              <span aria-hidden className={cn("rounded-inline size-2 shrink-0", part.color)} />
              <dt className="text-muted-foreground flex-1">{part.label}</dt>
              <dd className="text-foreground tabular-nums">~{formatTokens(part.tokens)}</dd>
            </div>
          ))}
        </dl>
        <p className="text-subtle-foreground border-border mt-2.5 border-t pt-2 text-[11px]">
          达到 {formatTokens(context.threshold_tokens)}（{threshold}%）时，下一次模型调用前会自动把
          较早的对话压缩成一份检查点，最近的步骤原样保留。
          {context.measured ? "总量来自端点报告的用量。" : "端点尚未报告用量，总量为本地估算。"}
        </p>
      </TooltipContent>
    </Tooltip>
  );
}

function formatTokens(tokens: number): string {
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(1)}M`;
  if (tokens >= 1_000) return `${(tokens / 1_000).toFixed(1)}k`;
  return String(tokens);
}

function ApprovalModePicker({
  mode,
  onChange,
}: {
  mode: AISession["approval_mode"];
  onChange: (mode: AISession["approval_mode"]) => void;
}) {
  const [open, setOpen] = useState(false);
  const active = APPROVAL_MODES.find((item) => item.value === mode);
  const ActiveIcon = active?.icon ?? Hand;
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          aria-label="审批模式"
          className={cn(mode === "full" && "text-warning")}
        >
          <ActiveIcon aria-hidden /> {approvalModeLabel(mode)}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-80 p-1.5">
        <p className="text-muted-foreground px-2 py-1.5 text-xs">
          AIOps 执行敏感操作前应该如何处理？
        </p>
        {APPROVAL_MODES.map((item) => {
          const Icon = item.icon;
          return (
            <button
              key={item.value}
              type="button"
              onClick={() => {
                onChange(item.value);
                setOpen(false);
              }}
              className={cn(
                "zke-focus rounded-control hover:bg-surface-muted flex w-full items-start gap-2.5 px-2 py-2 text-left transition-colors duration-150",
                item.value === "full" && "text-warning",
              )}
            >
              <Icon aria-hidden className="mt-0.5 size-4 shrink-0" />
              <span className="min-w-0 flex-1">
                <span className="block text-[13px] font-medium">
                  {approvalModeLabel(item.value)}
                </span>
                <span className="text-muted-foreground block text-[11px]">{item.description}</span>
              </span>
              {item.value === mode ? (
                <Check aria-hidden className="text-primary mt-0.5 size-4 shrink-0" />
              ) : null}
            </button>
          );
        })}
        <p className="text-subtle-foreground border-border mt-1 border-t px-2 pt-2 text-[11px]">
          任何模式都不会扩大权限：上限始终是你自己的 RBAC。模式只决定谁来按下确认。
        </p>
      </PopoverContent>
    </Popover>
  );
}

/**
 * The playbooks, for a person.
 *
 * Worth a chip of its own rather than a line in the tool panel: an operator
 * reading "12 个工具" learns what AIOps may touch, and an operator reading
 * "7 个技能" learns what it already knows how to do. The panel says the part
 * that is easy to get wrong — a skill is a procedure and grants nothing — where
 * somebody is actually looking at the list.
 */
function SkillsChip({ skills }: { skills: AISkill[] }) {
  const [open, setOpen] = useState(false);
  if (skills.length === 0) return null;
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button type="button" size="sm" variant="ghost" aria-label="可用技能">
          <BookOpen aria-hidden /> {skills.length} 个技能
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="max-h-96 w-96 overflow-auto p-1.5">
        <p className="text-muted-foreground px-2 py-1.5 text-xs">
          技能是 ZKE
          提供的排查流程：模型在需要时读取它，按其中的顺序取证。技能不新增工具，也不扩大权限。
        </p>
        <ul className="space-y-1">
          {skills.map((skill) => (
            <li key={skill.id} className="rounded-control px-2 py-1.5">
              <p className="text-foreground text-[13px] font-medium">{skill.title}</p>
              <p className="text-muted-foreground mt-0.5 text-[11px]">{skill.summary}</p>
              <p className="text-subtle-foreground mt-0.5 font-mono text-[11px]">
                {skill.id} · {skill.tools.join(" / ")}
              </p>
            </li>
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  );
}

function ToolsChip({ tools }: { tools: AITool[] }) {
  const [open, setOpen] = useState(false);
  if (tools.length === 0) return null;
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button type="button" size="sm" variant="ghost" aria-label="可用工具">
          <Wrench aria-hidden /> {tools.length} 个工具
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="max-h-96 w-96 overflow-auto p-1.5">
        <p className="text-muted-foreground px-2 py-1.5 text-xs">
          模型自行决定调用哪些工具、按什么顺序调用。每次调用前都会重新校验下列权限。
        </p>
        <ul className="space-y-1">
          {tools.map((tool) => (
            <li key={tool.name} className="rounded-control px-2 py-1.5">
              <p className="text-foreground flex items-center gap-1.5 font-mono text-xs">
                {tool.name}
                {tool.sensitive || tool.conditionally_sensitive ? (
                  <span className="bg-warning-surface text-warning rounded-inline px-1 py-px font-sans text-[11px]">
                    {tool.sensitive ? "敏感" : "按目标敏感"}
                  </span>
                ) : null}
                {tool.mutating ? (
                  <span className="bg-warning-surface text-warning rounded-inline px-1 py-px font-sans text-[11px]">
                    写入
                  </span>
                ) : null}
              </p>
              <p className="text-muted-foreground mt-0.5 text-[11px]">{tool.description}</p>
              <p className="text-subtle-foreground mt-0.5 font-mono text-[11px]">
                {tool.permissions.join(" + ")}
                {tool.conditional_permissions.length > 0
                  ? `；按目标：${tool.conditional_permissions.join(" / ")}`
                  : ""}
              </p>
            </li>
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  );
}
