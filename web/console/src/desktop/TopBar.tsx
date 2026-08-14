import { CircleDot, LayoutGrid, LogOut, Moon, Settings, Sun, UserRound } from "lucide-react";

import type { StreamState } from "@/api/events";
import { ZkeMark } from "@/components/brand/zke-mark";
import { OpenSourceLink } from "@/components/common/open-source-link";
import { StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { HintTooltip } from "@/components/ui/tooltip";
import { useSessionContext } from "@/auth/session-context";
import { cn } from "@/lib/cn";
import { formatRelative } from "@/lib/time";
import { ScopeSelector } from "@/scope/ScopeSelector";
import { useScopeStore } from "@/scope/scope-store";
import { useThemeStore } from "@/theme/theme-store";

const STREAM_LABELS: Record<
  StreamState,
  { label: string; tone: "success" | "warning" | "neutral" }
> = {
  open: { label: "实时同步", tone: "success" },
  connecting: { label: "连接中", tone: "warning" },
  reconnecting: { label: "重连中", tone: "warning" },
  closed: { label: "未连接", tone: "neutral" },
};

export function TopBar({
  streamState,
  lastEventAt,
  onOpenApp,
  onResetDesktop,
  className,
}: {
  streamState: StreamState;
  lastEventAt: Date | null;
  onOpenApp: (appId: string) => void;
  onResetDesktop: () => void;
  /** Lets the desktop slide the bar off screen while a window is full screen. */
  className?: string;
}) {
  const { session, logout, logoutPending } = useSessionContext();
  const scope = useScopeStore((state) => state.scope);
  const setScope = useScopeStore((state) => state.setScope);
  const theme = useThemeStore((state) => state.theme);
  const toggleTheme = useThemeStore((state) => state.toggleTheme);

  const stream = STREAM_LABELS[streamState];

  return (
    <header
      className={cn(
        // Sits at the very back: windows dragged upwards pass over it. Its own
        // popovers and menus are portalled, so they still open above windows.
        //
        // The bar names nothing and frames nothing. No wordmark beside the mark,
        // no rule between the mark and the scope, no "项目" caption in front of a
        // control that already says which Project it holds: a bar that keeps
        // announcing itself is the opposite of an immersive one.
        "absolute inset-x-0 top-0 z-0 flex h-10 items-center gap-1.5 px-2.5",
        className,
      )}
    >
      {/* The glass is its own layer and reaches past the bar, so the blur has
          room to fall off below the content instead of ending at its edge. */}
      <div
        aria-hidden
        className="zke-topbar pointer-events-none absolute inset-x-0 top-0 -z-10 h-16"
      />

      <ZkeMark className="size-6 rounded-[8px]" />

      <ScopeSelector scope={scope} onChange={setScope} />

      <div className="flex flex-1 items-center justify-end gap-1">
        {/* Ambient status, so it is drawn as ambient: a dot and a quiet word.
            The pill it used to wear gave a thing that changes on its own the
            same weight as the controls beside it. */}
        <HintTooltip
          label={
            lastEventAt
              ? `集群状态事件流：${stream.label}，最近事件 ${formatRelative(lastEventAt)}`
              : `集群状态事件流：${stream.label}`
          }
        >
          {/* A step up from `subtle`: the bar no longer lifts the ground under
              it, so the faintest ink in it now stands on the wallpaper itself. */}
          <span className="text-muted-foreground mr-1 hidden items-center gap-1.5 text-[11px] sm:inline-flex">
            <StatusDot tone={stream.tone} />
            {stream.label}
          </span>
        </HintTooltip>

        <OpenSourceLink />

        <Button
          size="icon-sm"
          variant="ghost"
          onClick={toggleTheme}
          aria-label={theme === "dark" ? "切换到浅色主题" : "切换到深色主题"}
        >
          {theme === "dark" ? <Sun /> : <Moon />}
        </Button>

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="sm" className="gap-1.5 px-2 text-xs">
              <UserRound className="text-subtle-foreground" />
              <span className="max-w-32 truncate">{session?.user.display_name ?? "未登录"}</span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-56">
            <DropdownMenuLabel>
              <span className="text-foreground block text-[13px] font-medium">
                {session?.user.display_name}
              </span>
              <span className="zke-mono text-subtle-foreground block text-xs">
                {session?.user.username}
              </span>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => onOpenApp("settings")}>
              <Settings />
              系统设置与改密
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={onResetDesktop}>
              <LayoutGrid />
              重置桌面布局
            </DropdownMenuItem>
            <DropdownMenuItem disabled>
              <CircleDot />
              会话到期：{session ? formatRelative(session.expires_at) : "—"}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              variant="danger"
              disabled={logoutPending}
              onSelect={() => {
                void logout();
              }}
            >
              <LogOut />
              退出登录
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </header>
  );
}
