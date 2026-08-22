import { useWindowStore } from "@/desktop/window-store";
import { useScopeStore } from "@/scope/scope-store";

/**
 * Opening the ZKE view that backs one piece of AIOps evidence.
 *
 * A conclusion is only checkable if the operator can get to the object it rests
 * on, and the application that shows that object is already on this desktop.
 * Evidence therefore opens a window here rather than a second browser tab: a
 * tab reloads the Console, loses every other window that was open and leaves
 * the operator to find their way back.
 *
 * The target travels through session storage because the two applications that
 * receive it read it when they mount, which is the same handoff a deep link
 * from outside the Console uses. One module owns both the keys and the
 * application each kind of evidence belongs to, so the two entry points cannot
 * drift apart.
 */
export type EvidenceTarget = {
  kind: "resource" | "event" | "metric" | "log";
  cluster: string;
  tenantId?: string | null;
  projectId?: string | null;
  namespace?: string | null;
  gvk?: string | null;
  name?: string | null;
  query?: string | null;
};

export const CONTAINER_EVIDENCE_KEY = "zke.ai-evidence.container-target";
export const METRICS_EVIDENCE_CLUSTER_KEY = "zke.ai-evidence.metrics-cluster";
export const METRICS_EVIDENCE_QUERY_KEY = "zke.ai-evidence.metrics-query";

export function evidenceAppId(kind: EvidenceTarget["kind"]): string {
  return kind === "metric" ? "observability" : "container-service";
}

/** Stores what the receiving application should open with. */
export function stashEvidenceTarget(input: {
  appId: string;
  projectId: string;
  clusterId: string;
  namespace?: string | null;
  evidenceKind?: string | null;
  gvk?: string | null;
  resource?: string | null;
  query?: string | null;
}): void {
  try {
    if (input.appId === "container-service") {
      sessionStorage.setItem(
        CONTAINER_EVIDENCE_KEY,
        JSON.stringify({
          projectId: input.projectId,
          clusterId: input.clusterId,
          namespace: input.namespace,
          evidenceKind: input.evidenceKind,
          gvk: input.gvk,
          resource: input.resource,
        }),
      );
    }
    if (input.appId === "observability") {
      sessionStorage.setItem(METRICS_EVIDENCE_CLUSTER_KEY, input.clusterId);
      if (input.query) sessionStorage.setItem(METRICS_EVIDENCE_QUERY_KEY, input.query);
    }
  } catch {
    // Session storage may be unavailable; the window still opens, on whatever
    // the receiving application was last looking at.
  }
}

/**
 * Switches to the Project the evidence belongs to and opens its application.
 *
 * The window is restarted rather than merely focused: the target is read when
 * the application mounts, and an application already open on another Cluster
 * would otherwise come forward showing something else entirely — which is worse
 * than not following the link, because it looks like it worked.
 */
export function openEvidence(target: EvidenceTarget): void {
  const scope = useScopeStore.getState().scope;
  const tenantId = target.tenantId ?? scope.tenantId;
  const projectId = target.projectId ?? scope.projectId;
  const appId = evidenceAppId(target.kind);
  if (tenantId && projectId) {
    if (tenantId !== scope.tenantId || projectId !== scope.projectId) {
      useScopeStore
        .getState()
        .setScope({ tenantId, tenantName: null, projectId, projectName: null });
    }
    stashEvidenceTarget({
      appId,
      projectId,
      clusterId: target.cluster,
      namespace: target.namespace,
      evidenceKind: target.kind,
      gvk: target.gvk,
      resource: target.name,
      query: target.query,
    });
  }
  useWindowStore.getState().openWindow(appId, { restart: true });
}
