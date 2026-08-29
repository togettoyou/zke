import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { ArrowLeft, PanelLeftClose, PanelLeftOpen, type LucideIcon } from "lucide-react";

import { useNarrowSurface } from "@/apps/sidebar";
import { Button } from "@/components/ui/button";
import { HintTooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/cn";
import { useScopeStore } from "@/scope/scope-store";

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
 * A drill-down overlay keeps the view below it mounted so returning restores its
 * filters and whatever it had already loaded. Not its scroll position: the work
 * area is one scroll container shared with the overlay, and a new view is put
 * back to its top either way. Portal contributions need a separate visibility
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

  // Before paint, not after: the shell puts the work area back to the top when
  // the open view changes, and it learns that a view changed from this
  // registration. Run late and the new page is painted once at the old page's
  // scroll offset before it jumps.
  useLayoutEffect(() => {
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
      {/* `max-w-full` with `flex-wrap`, not `shrink-0` alone. Unshrinkable was
          right — the actions are why anyone reads a header — but unshrinkable
          and unwrapping means a header three buttons wide simply runs off a
          390px window. Capped at the row it sits in, the buttons stack instead. */}
      {actions ? (
        <div className="flex max-w-full shrink-0 flex-wrap items-center gap-2">{actions}</div>
      ) : null}
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

  /*
   * The rail collapses to its icons, and the operator's own answer outranks the
   * layout's — but only once they have given one.
   *
   * `null` means they have not, and the rail follows the surface: expanded while
   * there is room for a column, collapsed once there is not. An explicit toggle
   * replaces that with a fixed answer which then holds at every width, because a
   * rail put away on purpose should not come back on a resize.
   *
   * Navigating while the rail is covering the work area releases the override
   * rather than adding one. Dismissing the panel that way costs nothing later:
   * widening the window still returns the column.
   */
  const surface = useRef<HTMLDivElement | null>(null);
  const narrow = useNarrowSurface(surface);
  const [railChoice, setRailChoice] = useState<boolean | null>(null);
  const railCollapsed = railChoice ?? narrow;
  const railOverlays = narrow && !railCollapsed;
  const navigate = useCallback(
    (id: string) => {
      onNavigate(id);
      if (railOverlays) {
        setRailChoice(null);
      }
    },
    [onNavigate, railOverlays],
  );

  const [actionSlot, setActionSlot] = useState<HTMLElement | null>(null);
  const [pageHeaderSlot, setPageHeaderSlot] = useState<HTMLElement | null>(null);
  /*
   * The drill-down headers standing on top of the current section, each with an
   * id of its own rather than counted.
   *
   * The count was enough to know whether any is open, which is all the toolbar
   * needed. It is not enough to know *which* is open, and that is the question
   * the work area's scroll position turns on: opening a second object from the
   * first swaps one header for another and leaves a count exactly where it was.
   */
  const [pageHeaderStack, setPageHeaderStack] = useState<number[]>([]);
  const nextPageHeaderId = useRef(0);
  const registerPageHeader = useCallback(() => {
    nextPageHeaderId.current += 1;
    const id = nextPageHeaderId.current;
    setPageHeaderStack((current) => (current.includes(id) ? current : [...current, id]));
    // Removal by id, so a cleanup that runs twice is the same as running once.
    return () => setPageHeaderStack((current) => current.filter((item) => item !== id));
  }, []);
  const entered = pageHeaderStack.length > 0;
  const pageHeader = useMemo(
    () => ({ element: pageHeaderSlot, scope, register: registerPageHeader }),
    [pageHeaderSlot, registerPageHeader, scope],
  );

  /*
   * A new view starts at its top.
   *
   * The work area is one scroll container that every section and every object
   * opened from one is rendered into, so its offset outlives whatever put it
   * there: read a list to the bottom, open a row, and the object arrives
   * already scrolled past its own heading — with no way to tell that from a
   * page that simply begins in the middle. The reader scrolls back up to start
   * reading, which is the position they should have been handed.
   *
   * Keyed on the section plus the drill-down stack, so it fires on every kind
   * of move between views — rail section, entering an object, returning from
   * one, and each step of a form that replaces the header as it advances — and
   * on nothing else. Re-renders within a view, including the ones that arrive
   * when a query resolves, leave the reader where they were.
   *
   * `useLayoutEffect` and an instant jump: this is not a movement to be
   * followed but the starting position of a page that was not on screen a
   * moment ago, and animating it would draw the eye to the wrong end of it.
   */
  const workArea = useRef<HTMLDivElement | null>(null);
  const openView = `${activeId}\u0000${pageHeaderStack.join(",")}`;
  useLayoutEffect(() => {
    workArea.current?.scrollTo({ top: 0, left: 0 });
  }, [openView]);

  return (
    /*
     * The rail is a column beside the work area while the window can afford one,
     * and a panel over it when it cannot.
     *
     * 160px of permanent navigation is cheap in a 1060px window and ruinous in a
     * 390px one, where it takes 40% of the width and leaves the forms behind it
     * roughly 190px to lay out in. Collapsed to its icons it costs 52px at any
     * width, which even a phone can spare, and the labels are one tap away.
     *
     * The question is asked of the window, through `@container` and through the
     * width this element reports, never of the screen. A window dragged down to
     * 500px on a wide display has exactly the problem a phone has and gets
     * exactly the same answer.
     */
    <div ref={surface} className="@container relative flex h-full min-h-0">
      {visible.length > 1 ? (
        <nav
          aria-label="应用导航"
          className={cn(
            "border-border bg-surface-muted/60 z-20 flex shrink-0 flex-col border-r p-2 transition-[width] duration-200 ease-[var(--ease-lift)]",
            railCollapsed
              ? "w-13"
              : // Out of flow rather than squeezed once it no longer fits: a
                // 160px column in a 390px window leaves the work area unusable,
                // and the rail is open for a moment while that work area is what
                // the operator came for.
                "@max-2xl:bg-surface-muted @max-2xl:shadow-e3 w-40 @max-2xl:absolute @max-2xl:inset-y-0 @max-2xl:left-0 @max-2xl:w-[min(15rem,80%)]",
          )}
        >
          {/* The list scrolls, the toggle under it does not. A landscape phone
              leaves the window about 260px tall, which is four of these items —
              losing the way back out of a collapsed rail to reach the fifth is
              not a trade worth making. */}
          <div className="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto">
            {visible.map((item) => {
              const Icon = item.icon;
              const active = item.id === activeId;
              const entry = (
                <button
                  key={item.id}
                  type="button"
                  aria-current={active ? "page" : undefined}
                  aria-label={railCollapsed ? item.label : undefined}
                  onClick={() => navigate(item.id)}
                  className={cn(
                    // `zke-control` for the same reason every control in
                    // `components/ui` carries it: on a coarse pointer this is a
                    // primary target, and 28px of it is a miss. AIOps' own rail is
                    // built from `Button`, so without this the two rails hand a
                    // finger two different sizes of the same thing.
                    "zke-focus zke-control rounded-control relative flex shrink-0 items-center gap-2 py-1.5 text-left text-[13px] transition-colors",
                    /*
                     * `w-full`, never a fixed square.
                     *
                     * The collapsed rail is 52px, and `border-box` spends that on
                     * a 1px right border and 8px of padding either side — leaving
                     * 35px, not 36. A 36px item is one pixel too wide, and since
                     * `overflow-y-auto` computes the other axis to `auto` as well,
                     * that one pixel is a horizontal scrollbar across the rail.
                     * Sized against the box it is in, the item cannot disagree
                     * with it — including when a vertical scrollbar takes a slice
                     * of that box at run time.
                     */
                    railCollapsed ? "w-full justify-center px-0" : "px-2",
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
                  {railCollapsed ? null : <span className="truncate">{item.label}</span>}
                </button>
              );

              // The tooltip is the label while the label is not drawn, and noise
              // the moment it is.
              return railCollapsed ? (
                <HintTooltip key={item.id} label={item.label}>
                  {entry}
                </HintTooltip>
              ) : (
                entry
              );
            })}
          </div>

          {/*
           * The toggle sits under the list, drawn as one more item on the same
           * icon column, behind the hairline that says it is not one.
           *
           * It had a row of its own above the list, which is where a header
           * would go — except this rail has no header, so it read as a control
           * dropped into an empty band, and the band pushed the first section
           * out of line with the toolbar row beside it. Down here it costs the
           * rail nothing it was using and lines up with everything above it.
           */}
          <div className="border-border -mx-2 mt-1 shrink-0 border-t px-2 pt-1">
            <HintTooltip label={railCollapsed ? "展开导航" : "收起导航"}>
              <button
                type="button"
                aria-label={railCollapsed ? "展开导航" : "收起导航"}
                aria-expanded={!railCollapsed}
                onClick={() => setRailChoice(!railCollapsed)}
                className={cn(
                  "zke-focus zke-control rounded-control text-subtle-foreground hover:bg-surface/70 hover:text-foreground flex items-center py-1.5 transition-colors",
                  railCollapsed ? "w-full justify-center px-0" : "px-2",
                )}
              >
                {railCollapsed ? (
                  <PanelLeftOpen className="size-4 shrink-0" strokeWidth={1.75} aria-hidden />
                ) : (
                  <PanelLeftClose className="size-4 shrink-0" strokeWidth={1.75} aria-hidden />
                )}
              </button>
            </HintTooltip>
          </div>
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
            <div ref={setActionSlot} className="ml-auto flex flex-wrap items-center gap-2" />
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
        {/*
         * The working area is a container of its own, so a view inside it reads
         * the width it actually has — the window less the rail and the padding —
         * rather than the window's. Nested inside the shell's container: a
         * container query resolves against the nearest container ancestor, which
         * is this one for everything an application renders.
         */}
        <div ref={workArea} className="@container min-h-0 flex-1 overflow-auto p-3 @2xl:p-4">
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
      {actions ? (
        <div className="flex max-w-full shrink-0 flex-wrap items-center gap-2">{actions}</div>
      ) : null}
    </div>
  );
}

/**
 * Shown when an application needs a scope the operator has not selected yet.
 *
 * It also points at the control that resolves it. The picker is in the top bar,
 * which is nowhere near the window this text is in, and an operator who has just
 * signed in has no reason to have noticed it — so the sentence naming it is not
 * enough on its own, and the picker is highlighted as this view appears. The
 * button repeats the gesture, because the highlight is over in a couple of
 * seconds and the reader may have been looking elsewhere.
 *
 * Nothing here changes the scope itself: choosing a Project is the operator's
 * decision, and a view that picked one for them would be answering a question
 * about authority with a guess.
 */
export function ScopeRequired() {
  const requestAttention = useScopeStore((state) => state.requestAttention);

  useEffect(() => {
    requestAttention();
  }, [requestAttention]);

  return (
    <div className="flex h-full flex-col items-center justify-center gap-1.5 text-center">
      <p className="text-foreground text-sm font-medium">请先选择项目</p>
      <p className="text-muted-foreground max-w-sm text-[13px]">
        该视图按项目定域执行。在顶部状态栏的项目选择器中选择一个项目后即可使用。
      </p>
      <Button size="sm" variant="ghost" className="mt-1.5" onClick={requestAttention}>
        定位项目选择器
      </Button>
    </div>
  );
}
