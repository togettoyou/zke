import { memo, useEffect, useRef, useState } from "react";
import {
  Archive,
  ArchiveRestore,
  Check,
  Download,
  LoaderCircle,
  MoreHorizontal,
  PanelLeftClose,
  PanelLeftOpen,
  Pencil,
  Plus,
  Search,
  SlidersHorizontal,
  Trash2,
  X,
} from "lucide-react";

import type { AISession } from "@/api/types";
import type { ClusterListResult } from "@/api/queries/clusters";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/state";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { HintTooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/cn";

import { groupSessions } from "./entries";

/**
 * How the New Chat control refuses.
 *
 * `aria-disabled` rather than `disabled`, so the button keeps the appearance it
 * has everywhere else. It is refused most of the time an operator is looking at
 * it — the workspace opens on an empty conversation, which is already the thing
 * the button would create — and a faded button in that resting state made the
 * top of the rail look broken next to a perfectly usable one. The reason still
 * reaches the operator, and does so better than before: a truly disabled button
 * fires no pointer events, so neither its tooltip nor its title ever appeared.
 *
 * The click is refused in the handler; only the two promises a pointer makes —
 * the cursor and the press — are taken back here.
 */
const NEW_CHAT_REFUSED = "aria-disabled:cursor-default aria-disabled:active:scale-100";

export type SessionActions = {
  onRename: (session: AISession, title: string) => Promise<void>;
  onArchive: (session: AISession, archived: boolean) => void;
  onExport: (session: AISession) => void;
  onDelete: (session: AISession) => void;
};

/**
 * The rail: which Cluster this workspace is, and the conversations inside it.
 *
 * The Cluster picker sits at the top rather than in the header because it is
 * not a filter — it selects the workspace, and every session below it belongs to
 * that one Cluster and cannot be moved. Putting it beside the session title
 * would suggest an open conversation could be pointed somewhere else.
 *
 * Below the rule the rail is one list, not two shelves. Archived conversations
 * are reached through the list's own view options rather than through a second
 * tab across the top: archiving is what an operator does to a conversation they
 * have stopped reading, and a permanent control for it spends the widest, most
 * read part of the rail on the shelf nobody is looking at. Search is the same
 * bargain — an icon until it is wanted, then a field.
 *
 * Everything that acts on a conversation as an object — rename, archive,
 * export, delete — lives on its own row here rather than in the header of the
 * one that happens to be open. A row is where the operator is already pointing
 * at the conversation they mean; the header is about reading the answer, and
 * four icon buttons across it turned the answer into a toolbar.
 */
export function SessionList({
  clusters,
  clusterId,
  onClusterChange,
  clustersPending,
  clustersError,
  onRetryClusters,
  sessions,
  selectedId,
  onSelect,
  onCreate,
  creating,
  atNewSession,
  collapsed,
  onCollapsedChange,
  search,
  onSearch,
  archived,
  onArchived,
  pending,
  error,
  onRetry,
  actions,
}: {
  clusters: ClusterListResult["clusters"];
  clusterId: string;
  onClusterChange: (clusterId: string) => void;
  clustersPending: boolean;
  clustersError: Error | null;
  onRetryClusters: () => void;
  sessions: AISession[];
  selectedId: string | null;
  onSelect: (sessionId: string) => void;
  onCreate: () => void;
  creating: boolean;
  atNewSession: boolean;
  collapsed: boolean;
  onCollapsedChange: (collapsed: boolean) => void;
  search: string;
  onSearch: (value: string) => void;
  archived: boolean;
  onArchived: (value: boolean) => void;
  pending: boolean;
  error: Error | null;
  onRetry: () => void;
  actions: SessionActions;
}) {
  const online = clusters.filter((cluster) => cluster.connection.status === "online");
  const groups = groupSessions(sessions);
  const [renaming, setRenaming] = useState<string | null>(null);
  // A query that survived the field being put away is still a filter, so the
  // field comes back with it rather than leaving the list narrowed by something
  // invisible.
  const [searching, setSearching] = useState(false);
  const field = useRef<HTMLInputElement | null>(null);
  const searchOpen = searching || search.length > 0;
  const noWorkspace = !clustersPending && !clustersError && online.length === 0;
  const loading = clustersPending || pending;
  // One rule for both shapes of the rail, so the collapsed strip cannot offer a
  // conversation the expanded rail refuses to create.
  const createDisabled = !clusterId || creating || atNewSession;
  const createTitle = atNewSession
    ? "当前已经是一个还没提问的新对话"
    : clusterId
      ? undefined
      : "先选择一个在线的目标集群";

  useEffect(() => {
    if (searching) field.current?.focus();
  }, [searching]);

  // Collapsed, the rail keeps only what the conversation cannot do for itself:
  // getting the list back, starting another conversation, and reaching the one
  // through search. Squeezing the Cluster picker and the titles into a strip
  // would keep all of them and make none of them readable.
  if (collapsed) {
    return (
      <aside className="border-border bg-surface-muted/40 flex min-h-0 flex-col items-center gap-1 border-r px-2 pt-2.5">
        <HintTooltip label="展开会话列表">
          <Button
            size="icon"
            variant="ghost"
            aria-label="展开会话列表"
            onClick={() => onCollapsedChange(false)}
          >
            <PanelLeftOpen aria-hidden />
          </Button>
        </HintTooltip>
        <HintTooltip label={createTitle ?? "新对话"}>
          <Button
            size="icon"
            variant="ghost"
            aria-label="新对话"
            aria-disabled={createDisabled || undefined}
            className={NEW_CHAT_REFUSED}
            onClick={() => {
              if (!createDisabled) onCreate();
            }}
          >
            <Plus aria-hidden />
          </Button>
        </HintTooltip>
        <HintTooltip label="搜索会话">
          <Button
            size="icon"
            variant="ghost"
            aria-label="搜索会话"
            onClick={() => {
              onCollapsedChange(false);
              setSearching(true);
            }}
          >
            <Search aria-hidden />
          </Button>
        </HintTooltip>
      </aside>
    );
  }

  return (
    <aside className="border-border bg-surface-muted/40 flex min-h-0 flex-col border-r">
      <div className="space-y-2 px-2.5 pt-2.5 pb-2">
        <div className="flex items-center justify-between gap-2 pl-1">
          <label htmlFor="aiops-workspace" className="text-subtle-foreground text-[11px]">
            目标集群
          </label>
          <HintTooltip label="收起会话列表">
            <Button
              size="icon-sm"
              variant="ghost"
              className="-mr-1 size-6"
              aria-label="收起会话列表"
              onClick={() => onCollapsedChange(true)}
            >
              <PanelLeftClose aria-hidden />
            </Button>
          </HintTooltip>
        </div>
        <Select value={clusterId} onValueChange={onClusterChange}>
          <SelectTrigger id="aiops-workspace" aria-label="AIOps 目标集群">
            <SelectValue placeholder="选择 Cluster" />
          </SelectTrigger>
          <SelectContent>
            {clusters.map((cluster) => (
              <SelectItem
                key={cluster.id}
                value={cluster.id}
                disabled={cluster.connection.status !== "online"}
              >
                {cluster.name}
                {cluster.connection.status === "online" ? "" : "（离线）"}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {/* Secondary rather than primary: the rail is where an operator reads
            the conversation they already have, and a filled button at the top
            of it was the loudest thing in a workspace whose subject is the
            answer on the right. It is also refused while the open conversation
            is itself an empty new one — otherwise every click leaves behind
            another untouched session in the list. */}
        <Button
          variant="secondary"
          className={cn("rounded-panel h-9 w-full", NEW_CHAT_REFUSED)}
          aria-disabled={createDisabled || undefined}
          onClick={() => {
            if (!createDisabled) onCreate();
          }}
          title={createTitle}
        >
          <Plus aria-hidden /> 新对话
        </Button>
        {clustersError ? <ErrorState error={clustersError} onRetry={onRetryClusters} /> : null}
      </div>

      {/* The rule is the seam between "which workspace" and "which conversation
          in it": above it every control changes the subject, below it every
          control only narrows the list. */}
      <div className="border-border space-y-1.5 border-t px-2.5 pt-2 pb-1.5">
        <div className="flex items-center gap-0.5 pl-1">
          <h3 className="text-subtle-foreground min-w-0 flex-1 truncate text-[11px]">
            {archived ? "已归档会话" : "会话"}
          </h3>
          {/* Leaving the archive is offered where being in it is stated, so the
              way back is one control rather than a second trip through the
              menu that opened it. */}
          {archived ? (
            <Button
              size="sm"
              variant="ghost"
              className="h-6 px-1.5 text-[11px]"
              onClick={() => onArchived(false)}
            >
              <X aria-hidden /> 退出归档
            </Button>
          ) : null}
          <HintTooltip label="搜索会话">
            <Button
              size="icon-sm"
              variant="ghost"
              className="size-6"
              aria-label="搜索会话"
              aria-expanded={searchOpen}
              onClick={() => {
                if (!searchOpen) {
                  setSearching(true);
                  return;
                }
                setSearching(false);
                onSearch("");
              }}
            >
              <Search aria-hidden />
            </Button>
          </HintTooltip>
          <ViewOptions archived={archived} onArchived={onArchived} />
        </div>
        {searchOpen ? (
          <div className="relative">
            <Search
              aria-hidden
              className="text-subtle-foreground pointer-events-none absolute top-2 left-2.5 size-4"
            />
            <Input
              ref={field}
              aria-label="搜索会话"
              value={search}
              onChange={(event) => onSearch(event.target.value)}
              onKeyDown={(event) => {
                if (event.key !== "Escape") return;
                event.preventDefault();
                onSearch("");
                setSearching(false);
              }}
              onBlur={() => {
                if (!search) setSearching(false);
              }}
              placeholder="搜索会话"
              className="h-8 pl-8 text-[13px]"
            />
          </div>
        ) : null}
      </div>

      <div className="min-h-0 flex-1 overflow-auto px-2 pb-3">
        {/* The Cluster list decides which sessions are even asked for, so the
            rail is still loading while it is: reporting "还没有会话" in that gap
            would answer a question nobody has asked yet. */}
        {error ? <ErrorState error={error} onRetry={onRetry} /> : null}
        {!error && loading ? <LoadingState label="加载会话" /> : null}
        {/* An empty list is not a problem to announce.
            The shared EmptyState draws an icon and a heading, which is the right
            weight for exactly one of these states: no Cluster to ask in, where
            the operator has to go somewhere else before this application does
            anything at all. The other three are ordinary answers to a question
            the rail was just asked — nothing yet, nothing matched, nothing
            archived — and a heading for each of them turns a quiet rail into
            three announcements of things that are fine. */}
        {!error && !loading && sessions.length === 0 ? (
          noWorkspace ? (
            <EmptyState
              className="gap-1.5 px-3 py-8"
              title="没有可用集群"
              // The one place the empty rail explains itself. Said here rather
              // than under the disabled button as well: two sentences about the
              // same absence, a control apart, read as two different problems.
              description="当前项目没有在线集群，暂时不能新建会话。先让某个 Cluster 上线，或切换项目。"
            />
          ) : (
            <p className="text-subtle-foreground px-3 py-6 text-center text-xs">
              {search ? "没有匹配的会话" : archived ? "没有已归档会话" : "还没有会话"}
            </p>
          )
        ) : null}
        {groups.map((group) => (
          <section key={group.label} className="mb-1.5">
            <h4 className="text-subtle-foreground bg-surface-muted/40 sticky top-0 z-1 px-2 py-1 text-[11px] backdrop-blur-sm">
              {group.label}
            </h4>
            <ul className="space-y-0.5">
              {group.sessions.map((session) => (
                <li key={session.id}>
                  {renaming === session.id ? (
                    <RenameRow
                      session={session}
                      onCancel={() => setRenaming(null)}
                      onSubmit={async (title) => {
                        await actions.onRename(session, title);
                        setRenaming(null);
                      }}
                    />
                  ) : (
                    <SessionRow
                      session={session}
                      selected={session.id === selectedId}
                      onSelect={onSelect}
                      onRename={() => setRenaming(session.id)}
                      actions={actions}
                    />
                  )}
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>
    </aside>
  );
}

/**
 * What the list shows, rather than what it does.
 *
 * The archive lives here because it is a different shelf of the same list, and
 * a shelf is a view. It is one menu rather than two persistent tabs: the recent
 * conversations are what the rail is for, and the archive is where an operator
 * goes deliberately, once.
 */
function ViewOptions({
  archived,
  onArchived,
}: {
  archived: boolean;
  onArchived: (value: boolean) => void;
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button size="icon-sm" variant="ghost" className="size-6" aria-label="列表显示选项">
          <SlidersHorizontal aria-hidden />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-48">
        <DropdownMenuLabel>显示</DropdownMenuLabel>
        <DropdownMenuItem onSelect={() => onArchived(false)}>
          <Check aria-hidden className={cn(!archived ? "opacity-100" : "opacity-0")} />
          最近会话
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => onArchived(true)}>
          <Check aria-hidden className={cn(archived ? "opacity-100" : "opacity-0")} />
          已归档会话
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <p className="text-subtle-foreground px-2 pt-1 pb-1.5 text-[11px] leading-relaxed">
          归档不会中断已在运行的会话，只把它移出这份列表。
        </p>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/**
 * One conversation.
 *
 * A div holding a button rather than one large button: the menu is a control
 * inside the row, and a button may not contain another one. Selecting still
 * takes the whole row — only the trailing corner belongs to the menu.
 *
 * Memoised because the rail re-renders on every keystroke in the search field
 * and on every trajectory event that touches the open session, and a row's
 * appearance depends on nothing else than the props below.
 */
const SessionRow = memo(function SessionRow({
  session,
  selected,
  onSelect,
  onRename,
  actions,
}: {
  session: AISession;
  selected: boolean;
  onSelect: (sessionId: string) => void;
  onRename: () => void;
  actions: SessionActions;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  const archived = Boolean(session.archived_at);
  const working = session.status === "working";

  return (
    <div
      className={cn(
        "group rounded-control relative flex items-center transition-colors duration-150",
        selected ? "bg-primary-surface" : "hover:bg-surface-muted",
      )}
    >
      <button
        type="button"
        onClick={() => onSelect(session.id)}
        className="zke-focus rounded-control flex min-w-0 flex-1 items-center gap-2 py-1.5 pr-9 pl-2 text-left"
      >
        <span
          className={cn(
            "min-w-0 flex-1 truncate text-[13px]",
            selected ? "text-foreground font-medium" : "text-muted-foreground",
          )}
        >
          {session.title}
        </span>
        {/* A run in progress outranks both the age and the menu affordance:
            it is the one thing about a row that is changing while it is read. */}
        {working ? (
          <LoaderCircle
            aria-label="运行中"
            className="text-primary size-3.5 shrink-0 animate-spin"
          />
        ) : (
          <span
            className={cn(
              "text-subtle-foreground shrink-0 text-[11px] transition-opacity duration-150",
              "group-focus-within:opacity-0 group-hover:opacity-0",
              menuOpen && "opacity-0",
            )}
          >
            {relativeAge(session.last_activity_at)}
          </span>
        )}
      </button>

      <DropdownMenu open={menuOpen} onOpenChange={setMenuOpen}>
        <DropdownMenuTrigger asChild>
          <Button
            size="icon-sm"
            variant="ghost"
            aria-label={`会话操作：${session.title}`}
            className={cn(
              "absolute top-1/2 right-1 size-6 -translate-y-1/2 opacity-0 transition-opacity duration-150",
              "group-focus-within:opacity-100 group-hover:opacity-100 focus-visible:opacity-100",
              menuOpen && "opacity-100",
            )}
          >
            <MoreHorizontal aria-hidden />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-44">
          <DropdownMenuItem onSelect={onRename}>
            <Pencil aria-hidden /> 重命名
          </DropdownMenuItem>
          <DropdownMenuItem
            disabled={working}
            onSelect={() => actions.onArchive(session, !archived)}
          >
            {archived ? <ArchiveRestore aria-hidden /> : <Archive aria-hidden />}
            {archived ? "取消归档" : "归档"}
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => actions.onExport(session)}>
            <Download aria-hidden /> 导出对话与轨迹
          </DropdownMenuItem>
          {/* Deleting is offered only where it is possible: the Server refuses
              to delete a session that was never archived, and a menu item that
              always fails is worse than one that is not there. */}
          {archived ? (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="danger" onSelect={() => actions.onDelete(session)}>
                <Trash2 aria-hidden /> 删除
              </DropdownMenuItem>
            </>
          ) : null}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
});

/**
 * Renaming happens in place, on the row.
 *
 * The title is what the rail shows, so the edit belongs where the operator can
 * see the result against its neighbours — and it stays reachable for a
 * conversation that is not the open one.
 */
function RenameRow({
  session,
  onSubmit,
  onCancel,
}: {
  session: AISession;
  onSubmit: (title: string) => Promise<void>;
  onCancel: () => void;
}) {
  const [title, setTitle] = useState(session.title);
  const [saving, setSaving] = useState(false);
  const field = useRef<HTMLInputElement | null>(null);

  // Twice, and the second one is the load-bearing one: this row replaces the
  // menu item that opened it, and Radix moves focus back towards that trigger
  // as the menu closes. Selecting again on the next frame lands after it.
  useEffect(() => {
    field.current?.select();
    const frame = requestAnimationFrame(() => field.current?.select());
    return () => cancelAnimationFrame(frame);
  }, []);

  return (
    <form
      className="flex items-center gap-1 px-1 py-0.5"
      onSubmit={(event) => {
        event.preventDefault();
        const next = title.trim();
        if (!next || saving) return;
        setSaving(true);
        void onSubmit(next).finally(() => setSaving(false));
      }}
    >
      <Input
        ref={field}
        value={title}
        onChange={(event) => setTitle(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Escape") onCancel();
        }}
        aria-label="会话标题"
        className="h-7 px-2 text-[13px]"
      />
      <Button
        type="submit"
        size="icon-sm"
        variant="ghost"
        aria-label="保存标题"
        disabled={!title.trim() || saving}
        className="size-6"
      >
        <Check aria-hidden />
      </Button>
      <Button
        type="button"
        size="icon-sm"
        variant="ghost"
        aria-label="取消重命名"
        onClick={onCancel}
        className="size-6"
      >
        <X aria-hidden />
      </Button>
    </form>
  );
}

function relativeAge(timestamp: string): string {
  const minutes = Math.floor((Date.now() - new Date(timestamp).getTime()) / 60_000);
  if (minutes < 1) return "刚刚";
  if (minutes < 60) return `${minutes} 分钟`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时`;
  return `${Math.floor(hours / 24)} 天`;
}
