import { CircleDot, LayoutGrid, LogOut, Moon, Settings, Sun, UserRound } from "lucide-react";

import type { StreamState } from "@/api/events";
import { Badge, StatusDot } from "@/components/ui/badge";
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
        "border-border bg-surface-overlay absolute inset-x-0 top-0 z-0 flex h-11 items-center gap-3 border-b px-3 backdrop-blur-md",
        className,
      )}
    >
      <div className="flex shrink-0 items-center gap-2">
        <span className="bg-primary text-primary-foreground grid size-6 place-items-center rounded-md text-[11px] font-bold">
          Z
        </span>
        <span className="text-foreground text-[13px] font-semibold">ZKE Console</span>
      </div>

      <div className="flex min-w-0 items-center gap-2">
        <span className="text-subtle-foreground shrink-0 text-xs">项目</span>
        <ScopeSelector scope={scope} onChange={setScope} />
      </div>

      <div className="flex flex-1 items-center justify-end gap-2">
        <HintTooltip
          label={
            lastEventAt
              ? `Cluster 状态事件流：${stream.label}，最近事件 ${formatRelative(lastEventAt)}`
              : `Cluster 状态事件流：${stream.label}`
          }
        >
          <Badge tone={stream.tone} className="hidden sm:inline-flex">
            <StatusDot tone={stream.tone} />
            {stream.label}
          </Badge>
        </HintTooltip>

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
            <Button variant="ghost" size="sm" className="gap-2">
              <UserRound />
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
