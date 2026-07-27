import { useState, type ReactNode } from "react";
import { Check, ChevronRight, ChevronsUpDown } from "lucide-react";

import { useProject, useProjects, useTenant, useTenants } from "@/api/queries/resources";
import type { Project, ScopeSelection, Tenant } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/cn";

const NONE_LABEL = "未选择项目";
const LIST_PARAMS = { limit: 100, status: "active" } as const;

/**
 * The Project picker: the Console's single scope control.
 *
 * A scope is one Project, which implies its Tenant — the two RoleBinding levels
 * below Global. The two levels are picked in cascade rather than from one flat
 * list, so the Tenant a Project belongs to is never ambiguous and only the
 * Projects of the Tenant being inspected are fetched.
 *
 * Options come from the scoped list APIs, so an operator only ever sees what
 * their bindings already allow; choosing one can never widen access.
 */
export function ScopeSelector({
  scope,
  onChange,
  className,
}: {
  scope: ScopeSelection;
  onChange: (scope: ScopeSelection) => void;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const [activeTenant, setActiveTenant] = useState<{ id: string; name: string } | null>(null);

  const tenants = useTenants(LIST_PARAMS, open);
  const tenantId = activeTenant?.id ?? null;
  const projects = useProjects(open ? tenantId : null, LIST_PARAMS);

  // A deep link carries ids only. Resolve the labels so the trigger shows a
  // name rather than a raw identifier.
  const unlabelled = Boolean(scope.projectId && !scope.projectName);
  const linkedTenant = useTenant(unlabelled ? scope.tenantId : null);
  const linkedProject = useProject(unlabelled ? scope.projectId : null);
  const tenantLabel = scope.tenantName ?? linkedTenant.data?.name ?? null;
  const projectLabel = scope.projectName ?? linkedProject.data?.name ?? null;

  const openPicker = (next: boolean) => {
    setOpen(next);
    if (next) {
      // Start on the Tenant already in scope so the current Project is in view.
      setActiveTenant(
        scope.tenantId && tenantLabel ? { id: scope.tenantId, name: tenantLabel } : null,
      );
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
            // Translucent on purpose: it sits on the top bar's glass, and an
            // opaque fill would read as a chip stuck onto it.
            "zke-focus border-border/70 bg-surface/55 hover:bg-surface/85 rounded-control flex max-w-72 min-w-0 items-center gap-1.5 border px-2 py-1 text-left transition-colors",
            className,
          )}
        >
          {projectLabel ? (
            <span className="flex min-w-0 items-baseline gap-1.5">
              <span className="text-subtle-foreground shrink-0 text-xs">{tenantLabel}</span>
              <span className="text-subtle-foreground shrink-0 text-xs">/</span>
              <span className="text-foreground truncate text-[13px] font-medium">
                {projectLabel}
              </span>
            </span>
          ) : (
            <span className="text-subtle-foreground truncate text-[13px]">{NONE_LABEL}</span>
          )}
          <ChevronsUpDown
            className="text-subtle-foreground ml-auto size-3.5 shrink-0"
            aria-hidden
          />
        </button>
      </PopoverTrigger>

      <PopoverContent align="start" className="w-[26rem] p-0">
        <div className="border-border flex border-b">
          <CascadeColumn title="租户">
            <CascadeState
              query={tenants}
              count={tenants.data?.tenants.length ?? 0}
              empty="没有可见的租户"
            >
              {tenants.data?.tenants.map((tenant: Tenant) => (
                <CascadeItem
                  key={tenant.id}
                  label={tenant.name}
                  active={tenant.id === tenantId}
                  selected={tenant.id === scope.tenantId}
                  hasChildren
                  onSelect={() => setActiveTenant({ id: tenant.id, name: tenant.name })}
                />
              ))}
            </CascadeState>
          </CascadeColumn>

          <div className="border-border w-1/2 border-l">
            <CascadeColumn title="项目">
              {activeTenant ? (
                <CascadeState
                  query={projects}
                  count={projects.data?.projects.length ?? 0}
                  empty="该租户下没有可见的项目"
                >
                  {projects.data?.projects.map((project: Project) => (
                    <CascadeItem
                      key={project.id}
                      label={project.name}
                      selected={project.id === scope.projectId}
                      onSelect={() => selectProject(activeTenant, project)}
                    />
                  ))}
                </CascadeState>
              ) : (
                <CascadeHint>先选择左侧的租户</CascadeHint>
              )}
            </CascadeColumn>
          </div>
        </div>

        <div className="flex items-center justify-between gap-2 px-2 py-1.5">
          <p className="text-subtle-foreground text-xs">选择项目即确定当前作用域</p>
          <Button size="sm" variant="ghost" disabled={!scope.projectId} onClick={clear}>
            清除
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}

function CascadeColumn({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="min-w-0 flex-1">
      <p className="text-subtle-foreground px-2.5 pt-2 pb-1 text-xs font-medium">{title}</p>
      <ul className="max-h-64 overflow-y-auto p-1" role="listbox" aria-label={title}>
        {children}
      </ul>
    </div>
  );
}

/** Loading, failure and empty are all reported; a short list is never faked. */
function CascadeState({
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
    return <CascadeHint>加载中…</CascadeHint>;
  }
  if (query.error) {
    return (
      <li className="px-2 py-3 text-center">
        <p className="text-danger text-xs">加载失败</p>
        <Button size="sm" variant="ghost" className="mt-1" onClick={() => void query.refetch()}>
          重试
        </Button>
      </li>
    );
  }
  if (count === 0) {
    return <CascadeHint>{empty}</CascadeHint>;
  }
  return <>{children}</>;
}

function CascadeHint({ children }: { children: ReactNode }) {
  return <li className="text-muted-foreground px-2 py-3 text-center text-[13px]">{children}</li>;
}

function CascadeItem({
  label,
  active = false,
  selected,
  hasChildren = false,
  onSelect,
}: {
  label: string;
  /** Highlighted because its children are showing in the next column. */
  active?: boolean;
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
        className={cn(
          "hover:bg-surface-muted flex w-full items-center gap-1.5 rounded-md px-2 py-1.5 text-left",
          active && "bg-surface-muted",
        )}
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
