import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";

import { cn } from "@/lib/cn";

export type AppNavItem = {
  id: string;
  label: string;
  icon: LucideIcon;
  hidden?: boolean;
};

/**
 * Each application owns its navigation inside its window: a narrow rail on the
 * left, a toolbar row on top and a scrollable work area.
 */
export function AppShell({
  nav,
  activeId,
  onNavigate,
  toolbar,
  statusBar,
  children,
}: {
  nav: AppNavItem[];
  activeId: string;
  onNavigate: (id: string) => void;
  toolbar?: ReactNode;
  statusBar?: ReactNode;
  children: ReactNode;
}) {
  const visible = nav.filter((item) => !item.hidden);

  return (
    <div className="flex h-full min-h-0">
      {visible.length > 1 ? (
        <nav
          aria-label="应用导航"
          className="border-border bg-surface-muted/60 flex w-40 shrink-0 flex-col gap-0.5 border-r p-2"
        >
          {visible.map((item) => {
            const Icon = item.icon;
            const active = item.id === activeId;
            return (
              <button
                key={item.id}
                type="button"
                aria-current={active ? "page" : undefined}
                onClick={() => onNavigate(item.id)}
                className={cn(
                  "zke-focus rounded-control relative flex items-center gap-2 border border-transparent px-2 py-1.5 text-left text-[13px] transition-colors",
                  active
                    ? "border-border bg-surface text-foreground shadow-e1 font-medium"
                    : "text-muted-foreground hover:bg-surface/70 hover:text-foreground",
                )}
              >
                {/* A rail on the active item: on a surface this quiet, a fill
                    change alone is a weak signal. */}
                {active ? (
                  <span
                    aria-hidden
                    className="bg-primary absolute inset-y-1.5 left-0 w-0.5 rounded-full"
                  />
                ) : null}
                <Icon
                  className={cn("size-4 shrink-0", active && "text-primary")}
                  strokeWidth={1.75}
                  aria-hidden
                />
                <span className="truncate">{item.label}</span>
              </button>
            );
          })}
        </nav>
      ) : null}

      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        {toolbar ? (
          <div className="border-border bg-surface-muted/30 flex shrink-0 flex-wrap items-center gap-2 border-b px-3 py-2">
            {toolbar}
          </div>
        ) : null}
        <div className="min-h-0 flex-1 overflow-auto p-4">{children}</div>
        {statusBar ? (
          <div className="border-border text-subtle-foreground shrink-0 border-t px-3 py-1.5 text-xs">
            {statusBar}
          </div>
        ) : null}
      </div>
    </div>
  );
}

export function SectionTitle({
  title,
  description,
  actions,
}: {
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0">
        <h3 className="text-foreground text-sm font-semibold tracking-tight">{title}</h3>
        {description ? (
          <p className="text-muted-foreground mt-1 text-xs leading-relaxed">{description}</p>
        ) : null}
      </div>
      {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
    </div>
  );
}

/** Shown when an application needs a scope the operator has not selected yet. */
export function ScopeRequired() {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-1.5 text-center">
      <p className="text-foreground text-sm font-medium">请先选择项目</p>
      <p className="text-muted-foreground max-w-sm text-[13px]">
        该视图按项目定域执行。在顶部状态栏的项目选择器中选择一个项目后即可使用。
      </p>
    </div>
  );
}
