import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { ArrowLeft, type LucideIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/cn";

export type AppNavItem = {
  id: string;
  label: string;
  icon: LucideIcon;
  hidden?: boolean;
};

/**
 * The end of the toolbar row, where the active section puts its own actions.
 *
 * The shell owns the row because the scope pickers live there, but the button
 * that creates a Namespace belongs to the section that knows what creating one
 * means. Passing the element down and letting the section portal into it keeps
 * that ownership without the shell having to know about any section's dialogs.
 */
const ToolbarActionSlot = createContext<HTMLElement | null>(null);

/*
 * A drill-down overlay keeps the view below it mounted so returning can restore
 * filters and scroll position. Portal contributions need a separate visibility
 * boundary: CSS hiding the view does not hide nodes portaled into the shell.
 */
const AppShellContributionEnabled = createContext(true);

export function AppShellContributionScope({
  enabled,
  children,
}: {
  enabled: boolean;
  children: ReactNode;
}) {
  return (
    <AppShellContributionEnabled.Provider value={enabled}>
      {children}
    </AppShellContributionEnabled.Provider>
  );
}

/** Renders its children at the right end of the enclosing {@link AppShell} toolbar. */
export function SectionToolbarActions({ children }: { children: ReactNode }) {
  const slot = useContext(ToolbarActionSlot);
  const enabled = useContext(AppShellContributionEnabled);
  return enabled && slot ? createPortal(children, slot) : null;
}

/**
 * The row between the toolbar and the work area, owned by the open view.
 *
 * `scope` is what the toolbar's pickers were saying, as text: while a page
 * header is up the pickers are gone, and a Console that manages many Clusters
 * must not show one Cluster's object without saying which Cluster it is.
 */
const PageHeaderSlot = createContext<{
  element: HTMLElement | null;
  scope: ReactNode;
  register: () => () => void;
}>({ element: null, scope: null, register: () => () => {} });

/**
 * The header of a view that was entered from another one: what is open, the way
 * out of it, and what can be done to it.
 *
 * Its own row above the work area rather than the first thing inside it. A
 * detail page is as long as the object it describes, and a header that scrolls
 * takes the way back and every action with it — the operator who has read to the
 * bottom is the one most likely to want them. It is not the toolbar either: the
 * toolbar says which Cluster and Namespace is being looked at, and it is already
 * as wide as two pickers, so a fifth button there wraps it onto a second line
 * and puts 返回 further away than it was on the page.
 *
 * The way back is an arrow before the name, where reading starts, rather than a
 * labelled button at the far end: it is the same gesture on every screen and
 * needs saying once, quietly.
 *
 * While one is open the shell hides the toolbar. Its pickers change what the
 * list below is showing, and there is no list below — switching Namespace under
 * an open object would leave the page asking a different Cluster for the same
 * name. The scope they were displaying moves into this row as text.
 */
export function PageHeader({
  title,
  actions,
  onBack,
  backDisabled,
}: {
  title: string;
  actions?: ReactNode;
  /** Omitted by a view that was not entered from another one. */
  onBack?: () => void;
  backDisabled?: boolean;
}) {
  const { element, scope, register } = useContext(PageHeaderSlot);
  const enabled = useContext(AppShellContributionEnabled);

  useEffect(() => {
    if (!enabled) {
      return;
    }
    return register();
  }, [enabled, register]);

  if (!enabled || !element) {
    return null;
  }
  return createPortal(
    <>
      <div className="flex min-w-0 items-center gap-1.5">
        {onBack ? (
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="返回"
            title="返回"
            disabled={backDisabled}
            onClick={onBack}
            className="-ml-1 shrink-0"
          >
            <ArrowLeft />
          </Button>
        ) : null}
        {/* The name and the scope, and nothing else. A header that also explains
            the view spends a permanent row on a sentence read once. */}
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-2">
          <h3 className="text-foreground truncate text-sm font-semibold tracking-tight">{title}</h3>
          {scope ? <span className="text-subtle-foreground text-xs">{scope}</span> : null}
        </div>
      </div>
      {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
    </>,
    element,
  );
}

/**
 * Each application owns its navigation inside its window: a narrow rail on the
 * left, a toolbar row on top and a scrollable work area.
 */
export function AppShell({
  nav,
  activeId,
  onNavigate,
  toolbar,
  scope,
  statusBar,
  children,
}: {
  nav: AppNavItem[];
  activeId: string;
  onNavigate: (id: string) => void;
  toolbar?: ReactNode;
  /** What the toolbar's pickers say, in words, for when they are not shown. */
  scope?: ReactNode;
  statusBar?: ReactNode;
  children: ReactNode;
}) {
  const visible = nav.filter((item) => !item.hidden);
  const [actionSlot, setActionSlot] = useState<HTMLElement | null>(null);
  const [pageHeaderSlot, setPageHeaderSlot] = useState<HTMLElement | null>(null);
  const [openPageHeaders, setOpenPageHeaders] = useState(0);
  const registerPageHeader = useCallback(() => {
    let registered = true;
    setOpenPageHeaders((current) => current + 1);
    return () => {
      if (!registered) {
        return;
      }
      registered = false;
      setOpenPageHeaders((current) => Math.max(0, current - 1));
    };
  }, []);
  const entered = openPageHeaders > 0;
  const pageHeader = useMemo(
    () => ({ element: pageHeaderSlot, scope, register: registerPageHeader }),
    [pageHeaderSlot, registerPageHeader, scope],
  );

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
                  "zke-focus rounded-control relative flex items-center gap-2 px-2 py-1.5 text-left text-[13px] transition-colors",
                  active
                    ? "bg-surface text-foreground font-medium"
                    : "text-muted-foreground hover:bg-surface/70 hover:text-foreground",
                )}
              >
                {/*
                 * A rail plus a fill, and that is all. The active item used to
                 * carry a border and an elevation as well — six signals for one
                 * piece of state, on a rail eight items long. Weight is only
                 * legible against something lighter, so spending all of it at
                 * once leaves nothing to spend.
                 */}
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
        {/*
         * One chrome row at a time. The toolbar belongs to a list — its pickers
         * choose what the list shows, and its slot carries that list's actions —
         * so an open object replaces it rather than stacking under it.
         */}
        {toolbar && !entered ? (
          <div className="border-border bg-surface-muted/30 flex shrink-0 flex-wrap items-center gap-2 border-b px-3 py-2">
            {toolbar}
            {/* `ml-auto` on an empty div costs nothing and keeps section actions
                pinned to the far end whether or not any are portaled in. */}
            <div ref={setActionSlot} className="ml-auto flex items-center gap-2" />
          </div>
        ) : null}
        {/* `empty:hidden` keeps a list view, which has no header of its own, from
            paying for the row and its border. */}
        {/* Same padding and the same content height as the toolbar it stands in
            for, so entering an object does not shift the page under the cursor.
            `h-9` is the height of a picker, which is what sets the other row. */}
        <div
          ref={setPageHeaderSlot}
          className="border-border bg-surface-muted/30 flex shrink-0 flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b px-3 py-2 empty:hidden [&>*]:min-h-9"
        />
        <div className="min-h-0 flex-1 overflow-auto p-4">
          <PageHeaderSlot.Provider value={pageHeader}>
            <ToolbarActionSlot.Provider value={actionSlot}>{children}</ToolbarActionSlot.Provider>
          </PageHeaderSlot.Provider>
        </div>
        {statusBar ? (
          <div className="border-border text-subtle-foreground shrink-0 border-t px-3 py-1.5 text-xs">
            {statusBar}
          </div>
        ) : null}
      </div>
    </div>
  );
}

/**
 * Heading row of a section: an optional title and description on the left, the
 * section's actions on the right.
 *
 * The title is optional because a list whose category is already named by the
 * navigation rail and whose target is already named by the toolbar has nothing
 * left to say in a heading — but it may still own a button, and the button
 * belongs on this row rather than floating above the table on its own.
 */
export function SectionTitle({
  title,
  description,
  actions,
}: {
  title?: string;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0">
        {title ? (
          <h3 className="text-foreground text-sm font-semibold tracking-tight">{title}</h3>
        ) : null}
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
