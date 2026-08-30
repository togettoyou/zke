import { useState } from "react";

import { SectionToolbarActions } from "@/apps/AppShell";
import { useSessionContext } from "@/auth/session-context";
import { useScopeStore } from "@/scope/scope-store";

import { ExploreResults } from "./explore/ExploreResults";
import { ExpressionEditor } from "./explore/ExpressionEditor";
import { MetricsQLHelpLink } from "./explore/MetricsQLHelp";
import { SaveQueryDialog, type SaveQueryDraft } from "./explore/SaveQueryDialog";
import { SavedQueryLibrary } from "./explore/SavedQueryLibrary";
import { useExplore } from "./explore/explore-context";

/**
 * Ad-hoc queries: the section where an operator writes the question instead of
 * choosing it.
 *
 * The other sections answer the questions somebody wrote a panel for. This one
 * exists because an incident is usually not one of them, and the alternative to
 * having it here is an operator opening a second tool with its own credentials,
 * its own view of which Clusters exist, and no audit trail shared with this one.
 *
 * The expressions themselves live above this component, in the provider the
 * application wraps its shell with: the navigation rail unmounts a section when
 * the operator moves to another one, and an expression somebody spent five
 * minutes on must survive a glance at 计算资源.
 *
 * What makes it safe to offer is that the expression decides which series to
 * read and never which Cluster to read them from. The target is the one in the
 * toolbar above, and the Server rewrites every selector in every expression to
 * name it before the query reaches storage — so an expression pasted from
 * somebody else's runbook, `zke_cluster_id` filter and all, answers about this
 * Cluster or not at all.
 */
export function ExploreSection() {
  const projectId = useScopeStore((state) => state.scope.projectId);
  const tenantId = useScopeStore((state) => state.scope.tenantId);
  const { permissions } = useSessionContext();
  const [draft, setDraft] = useState<SaveQueryDraft | null>(null);
  const { rows, activeRef, insertExpression } = useExplore();
  const activeExpression = rows.find((row) => row.ref === activeRef)?.expression ?? "";

  // Sharing an expression into the Project is curation rather than access, and
  // it is the metrics management permission that grants it. The check only
  // decides whether the option is offered; the Server refuses the write either
  // way.
  const canShare = permissions.can("cluster.metrics.manage", {
    type: "project",
    tenantId,
    projectId,
  });

  return (
    <>
      {/* The library and the syntax reference belong to the screen rather than
          to the editor card: they are reached while writing, they do not act on
          any one expression, and the toolbar is where this application already
          puts everything that is true of the whole view. */}
      <SectionToolbarActions>
        <SavedQueryLibrary
          projectId={projectId ?? ""}
          currentExpression={activeExpression}
          onInsert={insertExpression}
          onEdit={setDraft}
        />
        <MetricsQLHelpLink />
      </SectionToolbarActions>
      <div className="flex flex-col gap-4">
        <ExpressionEditor onSave={setDraft} />
        <ExploreResults />
      </div>
      <SaveQueryDialog
        projectId={projectId ?? ""}
        draft={draft}
        canShare={canShare}
        onClose={() => setDraft(null)}
      />
    </>
  );
}
