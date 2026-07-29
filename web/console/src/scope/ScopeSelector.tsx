import { memo, useMemo, useState, type ReactNode } from "react";
import { ChevronDown, ChevronLeft, ChevronRight, Check, Layers, Search } from "lucide-react";

import { useProject, useProjects, useTenant, useTenants } from "@/api/queries/resources";
import type { Project, ScopeSelection, Tenant } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/cn";

const NONE_LABEL = "选择项目";
const LIST_PARAMS = { limit: 100, status: "active" } as const;

/** First grapheme of a name, for the workspace token. */
function initial(name: string): string {
  return ([...name.trim()][0] ?? "?").toUpperCase();
}

function matches(name: string, filter: string): boolean {
  return name.toLowerCase().includes(filter.trim().toLowerCase());
}

/**
 * The Project picker: the Console's single scope control.
 *
 * A scope is one Project, which implies its Tenant — the two RoleBinding levels
 * below Global. It is the most consequential piece of context in the product, so
 * the trigger is built as an identity rather than as a form control: a workspace
 * token and a two-line Tenant/Project lockup, with no field border. A bordered
 * box with a chevron reads as "pick a value from a list"; this has to read as
 * "you are working here".
 *
 * The two levels are still resolved in cascade — the Projects endpoint is scoped
 * to a Tenant, so a flat list of every Project would mean a request per Tenant —
 * but they are presented as one column that pushes, not as two side by side. A
 * cascade laid out as two lists always has a dead half waiting to be told what
 * to show; pushing has one place to look at every step.
 *
 * Options come from the scoped list APIs, so an operator only ever sees what
 * their bindings already allow; choosing one can never widen access.
 *
 * Memoized: it is the heaviest thing in the top bar — four query subscriptions
 * and a filtered list — and it sits beside a Cluster event indicator that
 * re-renders the bar on every event arriving from every Agent. Both of its
 * props come from the scope store and hold their identity.
 */
export const ScopeSelector = memo(function ScopeSelector({
  scope,
  onChange,
  className,
}: {
  scope: ScopeSelection;
  onChange: (scope: ScopeSelection) => void;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const [drilled, setDrilled] = useState<{ id: string; name: string } | null>(null);
  const [filter, setFilter] = useState("");

  const tenants = useTenants(LIST_PARAMS, open);
  const projects = useProjects(open ? (drilled?.id ?? null) : null, LIST_PARAMS);

  // A deep link carries ids only. Resolve the labels so the trigger shows a
  // name rather than a raw identifier.
  const unlabelled = Boolean(scope.projectId && !scope.projectName);
  const linkedTenant = useTenant(unlabelled ? scope.tenantId : null);
  const linkedProject = useProject(unlabelled ? scope.projectId : null);
  const tenantLabel = scope.tenantName ?? linkedTenant.data?.name ?? null;
  const projectLabel = scope.projectName ?? linkedProject.data?.name ?? null;

  const visibleTenants = useMemo(
    () => (tenants.data?.tenants ?? []).filter((tenant: Tenant) => matches(tenant.name, filter)),
    [filter, tenants.data],
  );
  const visibleProjects = useMemo(
    () =>
      (projects.data?.projects ?? []).filter((project: Project) => matches(project.name, filter)),
    [filter, projects.data],
  );

  const openPicker = (next: boolean) => {
    setOpen(next);
    setFilter("");
    if (next) {
      // Open on the Tenant already in scope, so the current Project is the first
      // thing in view rather than something to navigate back to.
      setDrilled(scope.tenantId && tenantLabel ? { id: scope.tenantId, name: tenantLabel } : null);
    }
  };

  const selectProject = (tenant: { id: string; name: string }, project: Project) => {
    onChange({
      tenantId: tenant.id,
      tenantName: tenant.name,
      projectId: project.id,
      projectName: project.name,
    });
    setOpen(false);
  };

  const clear = () => {
    onChange({ tenantId: null, tenantName: null, projectId: null, projectName: null });
    setOpen(false);
  };

  return (
    <Popover open={open} onOpenChange={openPicker}>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label="选择项目"
          className={cn(
            // No field border: it sits on the top bar's glass, and a box drawn
            // around it would read as a control dropped onto the bar rather than
            // as part of it.
            "zke-focus rounded-control hover:bg-surface/60 flex max-w-[17rem] min-w-0 items-center gap-2 py-1 pr-1.5 pl-1 text-left transition-colors",
            className,
          )}
        >
          <span
            aria-hidden
            className={cn(
              "grid size-6 shrink-0 place-items-center rounded-[8px] text-[11px] font-semibold",
              tenantLabel
                ? "bg-primary-surface text-primary"
                : "bg-surface-muted text-subtle-foreground",
            )}
          >
            {tenantLabel ? initial(tenantLabel) : <Layers className="size-3.5" />}
          </span>

          {projectLabel ? (
            <span className="flex min-w-0 flex-col">
              <span className="text-subtle-foreground truncate text-[10.5px] leading-tight">
                {tenantLabel}
              </span>
              <span className="text-foreground truncate text-[12.5px] leading-tight font-medium">
                {projectLabel}
              </span>
            </span>
          ) : (
            <span className="text-subtle-foreground truncate text-[13px]">{NONE_LABEL}</span>
          )}

          <ChevronDown className="text-subtle-foreground size-3 shrink-0" aria-hidden />
        </button>
      </PopoverTrigger>

      <PopoverContent align="start" className="w-[17.5rem] p-0">
        {/* Where you are, and the way back. One line, so the column below it is
            never explaining itself. */}
        <div className="flex h-9 items-center gap-1 px-2">
          {drilled ? (
            <>
              <button
                type="button"
                onClick={() => {
                  setDrilled(null);
                  setFilter("");
                }}
                className="zke-focus text-subtle-foreground hover:text-foreground -ml-1 flex shrink-0 items-center gap-0.5 rounded-md px-1 py-0.5 text-[11px] transition-colors"
              >
                <ChevronLeft className="size-3.5" aria-hidden />
                租户
              </button>
              <span className="text-subtle-foreground shrink-0 text-[11px]">/</span>
              <span className="text-foreground min-w-0 truncate text-[11px] font-medium">
                {drilled.name}
              </span>
            </>
          ) : (
            <span className="text-subtle-foreground text-[11px] font-medium">选择租户</span>
          )}
        </div>

        <div className="px-2 pb-2">
          <div className="relative">
            <Search
              className="text-subtle-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2"
              aria-hidden
            />
            <Input
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
              placeholder={drilled ? "筛选项目" : "筛选租户"}
              aria-label={drilled ? "筛选项目" : "筛选租户"}
              className="bg-surface-muted h-8 pl-8 text-[13px]"
            />
          </div>
        </div>

        <ul
          className="max-h-60 overflow-y-auto px-1.5 pb-1.5"
          role="listbox"
          aria-label={drilled ? "项目" : "租户"}
        >
          {drilled ? (
            <ListState
              query={projects}
              count={visibleProjects.length}
              empty={filter ? "没有匹配的项目" : "该租户下没有可见的项目"}
            >
              {visibleProjects.map((project: Project) => (
                <ScopeOption
                  key={project.id}
                  label={project.name}
                  selected={project.id === scope.projectId}
                  onSelect={() => selectProject(drilled, project)}
                />
              ))}
            </ListState>
          ) : (
            <ListState
              query={tenants}
              count={visibleTenants.length}
              empty={filter ? "没有匹配的租户" : "没有可见的租户"}
            >
              {visibleTenants.map((tenant: Tenant) => (
                <ScopeOption
                  key={tenant.id}
                  label={tenant.name}
                  selected={tenant.id === scope.tenantId}
                  hasChildren
                  onSelect={() => {
                    setDrilled({ id: tenant.id, name: tenant.name });
                    setFilter("");
                  }}
                />
              ))}
            </ListState>
          )}
        </ul>

        {/* Only when there is something to clear. A permanently disabled button
            is a control that spends most of its life saying no. */}
        {scope.projectId ? (
          <div className="border-border border-t p-1.5">
            <Button size="sm" variant="ghost" className="w-full justify-start" onClick={clear}>
              清除当前作用域
            </Button>
          </div>
        ) : null}
      </PopoverContent>
    </Popover>
  );
});

/** Loading, failure and empty are all reported; a short list is never faked. */
function ListState({
  query,
  count,
  empty,
  children,
}: {
  query: { isLoading: boolean; error: unknown; refetch: () => unknown };
  count: number;
  empty: string;
  children: ReactNode;
}) {
  if (query.isLoading) {
    return <ListHint>加载中…</ListHint>;
  }
  if (query.error) {
    return (
      <li className="px-2 py-4 text-center">
        <p className="text-danger text-xs">加载失败</p>
        <Button size="sm" variant="ghost" className="mt-1" onClick={() => void query.refetch()}>
          重试
        </Button>
      </li>
    );
  }
  if (count === 0) {
    return <ListHint>{empty}</ListHint>;
  }
  return <>{children}</>;
}

function ListHint({ children }: { children: ReactNode }) {
  return <li className="text-subtle-foreground px-2 py-4 text-center text-[13px]">{children}</li>;
}

function ScopeOption({
  label,
  selected,
  hasChildren = false,
  onSelect,
}: {
  label: string;
  /** Part of the scope currently in effect. */
  selected: boolean;
  hasChildren?: boolean;
  onSelect: () => void;
}) {
  return (
    <li>
      <button
        type="button"
        role="option"
        aria-selected={selected}
        onClick={onSelect}
        className="zke-focus hover:bg-surface-muted flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left transition-colors"
      >
        <span className="text-foreground min-w-0 flex-1 truncate text-[13px]">{label}</span>
        {selected ? <Check className="text-primary size-3.5 shrink-0" aria-hidden /> : null}
        {hasChildren ? (
          <ChevronRight className="text-subtle-foreground size-3.5 shrink-0" aria-hidden />
        ) : null}
      </button>
    </li>
  );
}
