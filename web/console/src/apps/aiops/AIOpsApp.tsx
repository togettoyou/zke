import { useMemo, useRef, useState } from "react";
import {
  downloadAISession,
  useAISessions,
  useAITools,
  useCreateAISession,
  useDeleteAISession,
  useStartAITurn,
  useUpdateAISession,
} from "@/api/queries/aiops";
import { useClusters, type ClusterListResult } from "@/api/queries/clusters";
import type { AISession, AISkill, AITool } from "@/api/types";
import { ScopeRequired } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { notifyFailure } from "@/components/common/notify";
import { SensitiveActionDialog } from "@/components/common/sensitive-action-dialog";
import { EmptyState, LoadingState } from "@/components/common/state";
import { cn } from "@/lib/cn";
import { useDebouncedValue } from "@/lib/use-debounced-value";
import { useScopeStore } from "@/scope/scope-store";

import { Composer } from "./composer";
import { Opening } from "./opening";
import { SessionList, type SessionActions } from "./session-list";
import { SessionView } from "./session-view";
import type { AppComponentProps } from "../types";

/**
 * AIOps: a cloud workspace whose subject is one Kubernetes Cluster.
 *
 * The shape is a conversation with a rail of past conversations, because that
 * is what the work is — an operator asks something, the agent investigates by
 * reading the Cluster, and both the answer and every read it made stay on the
 * record. The Cluster is fixed per session on purpose: a global view is for
 * observing, and anything that reads or acts has to say which Cluster it meant.
 *
 * Acting on a conversation — renaming, archiving, exporting, deleting — is
 * owned here rather than by the open session, because the rail offers those on
 * every row and most of those rows are not the open one.
 */
export function AIOpsApp(_props: AppComponentProps) {
  const { permissions } = useSessionContext();
  const scope = useScopeStore((state) => state.scope);
  const clustersQuery = useClusters(scope.projectId, { limit: 100, status: "active" });
  const clusters = useMemo(
    () => (clustersQuery.data as ClusterListResult | undefined)?.clusters ?? [],
    [clustersQuery.data],
  );
  const online = useMemo(
    () => clusters.filter((cluster) => cluster.connection.status === "online"),
    [clusters],
  );

  const canRun = Boolean(
    scope.tenantId &&
    scope.projectId &&
    permissions.can("ai.run", {
      type: "project",
      tenantId: scope.tenantId,
      projectId: scope.projectId,
    }),
  );

  const [chosenCluster, setChosenCluster] = useState("");
  const [search, setSearch] = useState("");
  const [archived, setArchived] = useState(false);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [railCollapsed, setRailCollapsed] = useState(false);
  // The mode the next conversation will be created with. It lives here rather
  // than in the composer because it is a property of the session about to
  // exist, and the composer is discarded the moment that session opens.
  const [nextApprovalMode, setNextApprovalMode] = useState<AISession["approval_mode"]>("ask");
  const [deleting, setDeleting] = useState<AISession | null>(null);
  const debouncedSearch = useDebouncedValue(search);

  // A Cluster that went offline while its sessions were open should not leave
  // the workspace pointing at nothing; falling back to the first online one
  // keeps the rail usable rather than empty.
  const clusterId = online.some((cluster) => cluster.id === chosenCluster)
    ? chosenCluster
    : (online[0]?.id ?? "");

  const sessionsQuery = useAISessions(
    canRun ? scope.tenantId : null,
    canRun ? scope.projectId : null,
    clusterId || null,
    debouncedSearch,
    archived,
  );
  const sessions = useMemo(() => sessionsQuery.data?.sessions ?? [], [sessionsQuery.data]);
  const toolsQuery = useAITools();
  const tools = toolsQuery.data?.tools ?? [];
  const skills = toolsQuery.data?.skills ?? [];

  const selected = sessions.find((session) => session.id === selectedId) ?? null;
  const createSession = useCreateAISession();
  const updateSession = useUpdateAISession();
  const deleteSession = useDeleteAISession();
  const startTurn = useStartAITurn();

  /**
   * Whether "新对话" would create anything.
   *
   * The welcome screen already is a new conversation, and a session nobody has
   * asked anything in yet (`current_turn === 0`) is one too. Without this the
   * button answers every click with another empty session, and the rail fills
   * up with conversations that differ only by the minute in their title.
   */
  const atNewSession = selected === null || (selected.current_turn === 0 && !selected.archived_at);

  const clusterName = clusters.find((cluster) => cluster.id === clusterId)?.name ?? clusterId ?? "";
  const deletingClusterName =
    clusters.find((cluster) => cluster.id === deleting?.cluster_id)?.name ??
    deleting?.cluster_id ??
    "";

  /**
   * Creating a session and asking the first question are one act when the
   * question is already written. Splitting them would make the hero cards open
   * an empty conversation and then need a second send.
   */
  const openSession = async (question?: string) => {
    if (!scope.tenantId || !scope.projectId || !clusterId) return;
    try {
      const created = await createSession.mutateAsync({
        tenantId: scope.tenantId,
        projectId: scope.projectId,
        clusterId,
        title: question
          ? question.slice(0, 60)
          : `新对话 ${new Date().toLocaleString("zh-CN", { hour: "2-digit", minute: "2-digit" })}`,
        approvalMode: nextApprovalMode,
      });
      setArchived(false);
      setSearch("");
      setSelectedId(created.id);
      if (question) {
        await startTurn.mutateAsync({ sessionId: created.id, text: question, attachmentIds: [] });
      }
    } catch (error) {
      notifyFailure(question ? "开始 AIOps 对话失败" : "创建 AIOps 会话失败", error);
    }
  };

  const update = async (input: {
    sessionId: string;
    title?: string;
    archived?: boolean;
    approvalMode?: AISession["approval_mode"];
  }) => {
    try {
      await updateSession.mutateAsync(input);
    } catch (error) {
      notifyFailure("更新 AIOps 会话失败", error);
    }
  };

  const actions: SessionActions = {
    onRename: (session, title) => update({ sessionId: session.id, title }),
    // Archiving takes the session out of the shelf being read, so the rail must
    // not stay pointed at a row that is no longer in it.
    onArchive: (session, next) => {
      void update({ sessionId: session.id, archived: next }).then(() => {
        if (session.id === selectedId) setSelectedId(null);
      });
    },
    onExport: (session) => {
      void downloadAISession(session.id).catch((error) => notifyFailure("导出会话失败", error));
    },
    onDelete: (session) => setDeleting(session),
  };

  if (!scope.projectId) return <ScopeRequired />;
  if (!canRun) {
    return <EmptyState title="无权使用 AIOps" description="当前项目未授予 ai.run 权限。" />;
  }
  // A window restored from a previous desktop can outlive the platform switch
  // that made it available, so the application says so rather than offering a
  // workspace whose every turn the Server would refuse.
  if (toolsQuery.isPending) return <LoadingState label="加载 AIOps" />;
  if (toolsQuery.data && !toolsQuery.data.enabled) {
    return (
      <EmptyState
        title="AIOps 未启用"
        description="平台尚未启用 AIOps 或未配置模型接入。请联系全局管理员在「平台配置 · 模型接入」中完成配置。"
      />
    );
  }

  return (
    <div
      className={cn(
        // The rail slides rather than snaps: it is the only column that changes
        // width, and a track transition moves it without reflowing anything the
        // conversation has already laid out.
        "bg-surface grid h-full min-h-0 transition-[grid-template-columns] duration-200 ease-[var(--ease-reveal)]",
        railCollapsed
          ? "grid-cols-[52px_minmax(420px,1fr)]"
          : "grid-cols-[256px_minmax(420px,1fr)] max-[860px]:grid-cols-[204px_minmax(320px,1fr)]",
      )}
    >
      <SessionList
        clusters={clusters}
        clusterId={clusterId}
        onClusterChange={(value) => {
          setChosenCluster(value);
          setSelectedId(null);
        }}
        clustersPending={clustersQuery.isPending}
        clustersError={clustersQuery.isError ? clustersQuery.error : null}
        onRetryClusters={() => void clustersQuery.refetch()}
        sessions={sessions}
        selectedId={selected?.id ?? null}
        onSelect={setSelectedId}
        onCreate={() => void openSession()}
        creating={createSession.isPending}
        atNewSession={atNewSession}
        collapsed={railCollapsed}
        onCollapsedChange={setRailCollapsed}
        search={search}
        onSearch={setSearch}
        archived={archived}
        onArchived={setArchived}
        // Without a Cluster there is no session query to be pending: it is
        // disabled, and a disabled query stays `isPending` forever. Reporting
        // that as loading is what left the rail spinning in a Project whose
        // Clusters are all offline.
        pending={Boolean(clusterId) && sessionsQuery.isPending}
        error={sessionsQuery.isError ? sessionsQuery.error : null}
        onRetry={() => void sessionsQuery.refetch()}
        actions={actions}
      />

      <main className="flex min-h-0 min-w-0 flex-col">
        {selected ? (
          <SessionView
            key={selected.id}
            session={selected}
            clusterName={clusterName}
            tools={tools}
            skills={skills}
            onUpdate={(input) => update({ ...input, sessionId: selected.id })}
          />
        ) : (
          <Welcome
            clusterName={clusterName}
            ready={Boolean(clusterId)}
            busy={createSession.isPending || startTurn.isPending}
            tools={tools}
            skills={skills}
            approvalMode={nextApprovalMode}
            onApprovalMode={setNextApprovalMode}
            onAsk={(question) => void openSession(question)}
          />
        )}
      </main>

      {/* No typed name here, unlike the Console's other destructive dialogs.
          Archiving is the gate: the Server refuses to delete a session that was
          never archived, so the operator has already taken this conversation
          out of the list once, deliberately, before this dialog can exist. A
          second act of typing its title guards nothing the first did not, and a
          confirmation everybody learns to type without reading is worse than
          one asked only where it counts. The target and the impact are still
          stated — that is what the dialog is for. */}
      <SensitiveActionDialog
        open={deleting !== null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null);
        }}
        title="删除已归档会话"
        description="删除后无法恢复。未归档会话不能删除。"
        scopeLines={
          deleting
            ? [
                { label: "Tenant", name: deleting.tenant_id },
                { label: "Project", name: deleting.project_id },
                { label: "Cluster", name: deletingClusterName, id: deleting.cluster_id },
                { label: "会话", name: deleting.title, id: deleting.id },
              ]
            : []
        }
        impacts={["永久删除会话、完整运行轨迹和全部附件。"]}
        confirmLabel="确认删除"
        destructive
        pending={deleteSession.isPending}
        error={deleteSession.error}
        onConfirm={() => {
          if (!deleting) return;
          const target = deleting.id;
          void deleteSession
            .mutateAsync(target)
            .then(() => {
              if (selectedId === target) setSelectedId(null);
              setDeleting(null);
            })
            .catch(() => undefined);
        }}
      />
    </div>
  );
}

/**
 * The empty workspace: the same empty conversation the rail's newest row shows.
 *
 * It renders the shared opening and the same composer, in the same place and at
 * the same width, so opening the application and opening a conversation nobody
 * has asked anything in are one screen rather than two that happen to do the
 * same thing. Sending here is what creates the session — there is nothing to
 * confirm first.
 */
function Welcome({
  clusterName,
  ready,
  busy,
  tools,
  skills,
  approvalMode,
  onApprovalMode,
  onAsk,
}: {
  clusterName: string;
  ready: boolean;
  busy: boolean;
  tools: AITool[];
  skills: AISkill[];
  approvalMode: AISession["approval_mode"];
  onApprovalMode: (mode: AISession["approval_mode"]) => void;
  onAsk: (question: string) => void;
}) {
  const [draft, setDraft] = useState("");
  const field = useRef<HTMLTextAreaElement | null>(null);
  return (
    <>
      <div className="flex min-h-0 flex-1 items-center justify-center overflow-auto px-6 py-5">
        <Opening
          clusterName={clusterName}
          ready={ready}
          onPick={(prompt) => {
            setDraft(prompt);
            field.current?.focus();
          }}
        />
      </div>
      <Composer
        approvalMode={approvalMode}
        working={false}
        tools={tools}
        skills={skills}
        disabled={!ready}
        pending={busy}
        draft={draft}
        onDraft={setDraft}
        inputRef={field}
        disabledPlaceholder="先让当前项目里的某个 Cluster 上线，再开始提问。"
        onSend={onAsk}
        onApprovalMode={onApprovalMode}
      />
    </>
  );
}
