import { useRef, useState } from "react";

import {
  useAIAttachments,
  useAIContextUsage,
  useAIEventStream,
  useAITrajectory,
  useCancelAITurn,
  useCreateAIAttachment,
  useDecideAIApproval,
  useDeleteAIAttachment,
  useStartAITurn,
} from "@/api/queries/aiops";
import type { AISession, AISkill, AITool } from "@/api/types";
import { notifyFailure } from "@/components/common/notify";
import { ErrorState, LoadingState } from "@/components/common/state";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/cn";

import { Composer } from "./composer";
import { Conversation } from "./conversation";
import { pendingApprovals, streamLabel } from "./entries";
import { Trajectory } from "./trajectory";
import { useViewIntents } from "./view-intents";

/**
 * One session: the conversation, its trajectory, and the composer that drives it.
 *
 * Keyed by session id at the call site, so switching conversations discards the
 * draft, the open tab and the live stream rather than carrying one session's
 * half-typed question into another.
 *
 * The header carries only what this view is — which conversation, in which
 * Cluster, and whether the stream is live — plus the one switch between reading
 * the answer and reading the run. Acting on the conversation as an object is
 * the rail's job, on its own row.
 */
export function SessionView({
  session,
  clusterName,
  windowId,
  tools,
  skills,
  onUpdate,
}: {
  session: AISession;
  clusterName: string;
  /** Which window this conversation is in, so an intent can ask whether the operator is looking at it. */
  windowId: string;
  tools: AITool[];
  skills: AISkill[];
  onUpdate: (input: {
    title?: string;
    archived?: boolean;
    approvalMode?: AISession["approval_mode"];
  }) => Promise<void>;
}) {
  const trajectory = useAITrajectory(session.id);
  const entries = trajectory.data?.entries ?? [];
  const stream = useAIEventStream(session.id, true);
  // `isSuccess` rather than "not pending": a failed read leaves the entries
  // empty too, and priming on that would hand every intent in the trail to the
  // desktop the moment a retry succeeds. `!isFetching` on top of it covers the
  // conversation whose trail is cached but stale — the entries on screen at
  // mount are then a prefix of the real trail, and the rest of it, including
  // whatever ran while the operator was reading another conversation, lands a
  // moment later. Live entries arrive through the stream's `setQueryData`
  // rather than a fetch, so waiting for a quiet query never delays one.
  const trailRead = trajectory.isSuccess && !trajectory.isFetching;
  const openedViews = useViewIntents(session.id, entries, windowId, trailRead);
  // Measured once while a turn runs and once when it settles, rather than after
  // every appended entry: the Server replays the whole trajectory to answer,
  // and the reading nobody watches mid-step is not worth a request per tool
  // result. What an operator wants to see is where the conversation stands
  // before they ask the next question.
  const context = useAIContextUsage(
    session.id,
    session.status === "working" ? "working" : String(entries.at(-1)?.sequence ?? 0),
  );
  const attachmentsQuery = useAIAttachments(session.id);
  const attachments = attachmentsQuery.data?.attachments ?? [];
  const createAttachment = useCreateAIAttachment();
  const deleteAttachment = useDeleteAIAttachment();
  const startTurn = useStartAITurn();
  const cancelTurn = useCancelAITurn();
  const decide = useDecideAIApproval();

  const [view, setView] = useState<"conversation" | "trajectory">("conversation");
  // The draft lives here rather than in the composer because the empty
  // conversation's suggestions write into it: picking one is a way to start
  // typing a question, not a way to send one.
  const [draft, setDraft] = useState("");
  const field = useRef<HTMLTextAreaElement | null>(null);

  const archived = Boolean(session.archived_at);
  const waiting = pendingApprovals(entries, session);

  const send = (text: string) =>
    void startTurn
      .mutateAsync({
        sessionId: session.id,
        text,
        attachmentIds: attachments.map((attachment) => attachment.id),
      })
      .catch((error) => notifyFailure("启动 AIOps 运行失败", error));

  const attachFile = async (file: File) => {
    if (file.size > 256 * 1024) {
      notifyFailure("添加附件失败", new Error("附件不能超过 256 KiB"));
      return;
    }
    try {
      await createAttachment.mutateAsync({
        sessionId: session.id,
        name: file.name,
        mediaType: attachmentMediaType(file),
        content: await file.text(),
      });
    } catch (error) {
      notifyFailure("添加附件失败", error);
    }
  };

  return (
    <>
      <header className="border-border flex min-h-13 shrink-0 items-center gap-3 border-b px-4 py-2">
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-center gap-2">
            <h2 className="text-foreground truncate text-sm font-semibold">{session.title}</h2>
            {archived ? (
              <span className="border-border bg-surface-muted text-subtle-foreground rounded-inline shrink-0 border px-1.5 py-px text-[11px]">
                已归档
              </span>
            ) : null}
          </div>
          <p className="text-subtle-foreground mt-0.5 flex min-w-0 items-center gap-1.5 truncate text-[11px]">
            <span className="truncate">目标集群 {clusterName}</span>
            <span aria-hidden>·</span>
            {/* The dot is the stream, not decoration: it is lit while entries
                are arriving and unlit while the view is only as fresh as its
                last fetch. */}
            <span
              aria-hidden
              className={cn(
                "size-1.5 shrink-0 rounded-full",
                stream.state === "open" ? "bg-success" : "bg-subtle-foreground/60",
              )}
            />
            <span className="shrink-0">实时流 {streamLabel(stream.state)}</span>
          </p>
        </div>

        <Tabs
          value={view}
          onValueChange={(value) => setView(value as typeof view)}
          className="shrink-0"
        >
          <TabsList className="h-8 gap-0.5 p-0.5">
            <TabsTrigger value="conversation" className="h-7 px-3 text-xs">
              对话
            </TabsTrigger>
            <TabsTrigger value="trajectory" className="h-7 px-3 text-xs">
              轨迹
              <span className="text-subtle-foreground ml-1.5 text-[11px]">{entries.length}</span>
            </TabsTrigger>
          </TabsList>
        </Tabs>
      </header>

      {waiting.length > 0 ? (
        <div
          role="status"
          className="border-warning/40 bg-warning-surface text-warning shrink-0 border-b px-4 py-1.5 text-xs"
        >
          {waiting.length === 1
            ? `运行已暂停，等待你批准 ${waiting[0]?.content.tool}。`
            : `运行已暂停，有 ${waiting.length} 个调用等待你批准。`}
        </div>
      ) : null}

      <div className={cn("flex min-h-0 flex-1 flex-col", view !== "conversation" && "hidden")}>
        {trajectory.isPending ? <LoadingState label="加载会话" /> : null}
        {trajectory.isError ? (
          <ErrorState error={trajectory.error} onRetry={() => void trajectory.refetch()} />
        ) : null}
        {!trajectory.isPending && !trajectory.isError ? (
          <Conversation
            session={session}
            clusterName={clusterName}
            entries={entries}
            live={stream.live}
            opened={openedViews}
            deciding={decide.isPending}
            onPick={(prompt) => {
              setDraft(prompt);
              field.current?.focus();
            }}
            onDecide={(callId, decision) =>
              void decide
                .mutateAsync({ sessionId: session.id, callId, decision })
                .catch((error) => notifyFailure("提交审批结果失败", error))
            }
          />
        ) : null}
      </div>

      <div className={cn("min-h-0 flex-1", view !== "trajectory" && "hidden")}>
        <Trajectory
          entries={entries}
          pending={trajectory.isPending}
          error={trajectory.error}
          onRetry={() => void trajectory.refetch()}
        />
      </div>

      {/* The composer belongs to the conversation. The trajectory is a record
          of what already ran, and it needs every row of height it can get: a
          box asking for the next question under a timeline is both cramped and
          beside the point. Unmounting it rather than hiding it would throw away
          a half-typed question every time the tab is looked at. */}
      <div className={cn("shrink-0", view !== "conversation" && "hidden")}>
        <Composer
          approvalMode={session.approval_mode}
          working={session.status === "working"}
          draft={draft}
          onDraft={setDraft}
          inputRef={field}
          attachments={attachments}
          tools={tools}
          skills={skills}
          context={context.data}
          disabled={archived}
          pending={startTurn.isPending}
          onSend={send}
          onStop={() =>
            void cancelTurn
              .mutateAsync(session.id)
              .catch((error) => notifyFailure("停止运行失败", error))
          }
          onAttach={(file) => void attachFile(file)}
          onRemoveAttachment={(attachmentId) =>
            void deleteAttachment
              .mutateAsync({ sessionId: session.id, attachmentId })
              .catch((error) => notifyFailure("删除附件失败", error))
          }
          onApprovalMode={(mode) => void onUpdate({ approvalMode: mode })}
        />
      </div>
    </>
  );
}

function attachmentMediaType(
  file: File,
): "text/plain" | "text/markdown" | "application/json" | "application/yaml" {
  const lower = file.name.toLowerCase();
  if (lower.endsWith(".md")) return "text/markdown";
  if (lower.endsWith(".json")) return "application/json";
  if (lower.endsWith(".yaml") || lower.endsWith(".yml")) return "application/yaml";
  return "text/plain";
}
