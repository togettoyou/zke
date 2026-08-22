import { create } from "zustand";

import { CONTAINER_EVIDENCE_KEY } from "@/apps/evidence-link";

type SelectionState = {
  /** The chosen value per owning scope, so returning to a scope restores it. */
  selections: Record<string, string>;
  select: (scopeKey: string, value: string) => void;
};

export type ContainerEvidenceTarget = {
  projectId?: string;
  clusterId?: string;
  namespace?: string;
  evidenceKind?: string;
  gvk?: string;
  resource?: string;
};

export function readContainerEvidenceTarget(): ContainerEvidenceTarget | null {
  try {
    const raw = sessionStorage.getItem(CONTAINER_EVIDENCE_KEY);
    if (!raw) return null;
    const value: unknown = JSON.parse(raw);
    return value && typeof value === "object" && !Array.isArray(value)
      ? (value as ContainerEvidenceTarget)
      : null;
  } catch {
    return null;
  }
}

function load(storageKey: string): Record<string, string> {
  const result: Record<string, string> = {};
  try {
    const raw = localStorage.getItem(storageKey);
    if (raw) {
      const parsed: unknown = JSON.parse(raw);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        for (const [scopeKey, value] of Object.entries(parsed)) {
          if (typeof value === "string") {
            result[scopeKey] = value;
          }
        }
      }
    }
    const target = readContainerEvidenceTarget();
    if (target) {
      if (storageKey === "zke.container-service.cluster" && target.projectId && target.clusterId) {
        result[target.projectId] = target.clusterId;
      }
      if (
        storageKey === "zke.container-service.namespace" &&
        target.clusterId &&
        target.namespace
      ) {
        result[target.clusterId] = target.namespace;
      }
    }
  } catch {
    // Local or session storage may be unavailable; the in-memory default works.
  }
  return result;
}

function save(storageKey: string, selections: Record<string, string>): void {
  try {
    localStorage.setItem(storageKey, JSON.stringify(selections));
  } catch {
    // Storage may be unavailable or full; the selection keeps working in memory.
  }
}

/**
 * A remembered navigation choice, keyed by the scope that owns it.
 *
 * Only identifiers are stored, and only so that a window closed and reopened
 * comes back where it was. Nothing here is authoritative: every stored value is
 * re-resolved against what the Server currently returns, so a stale or no longer
 * reachable one selects nothing.
 */
function createSelectionStore(storageKey: string) {
  return create<SelectionState>((set) => ({
    selections: load(storageKey),
    select: (scopeKey, value) =>
      set((state) => {
        const selections = { ...state.selections, [scopeKey]: value };
        save(storageKey, selections);
        return { selections };
      }),
  }));
}

/**
 * The target Cluster of the container service window, keyed by Project.
 *
 * Container service is a single-cluster application, so it needs a current
 * Cluster the way the rest of the Console needs a current Project. It is
 * deliberately not part of {@link useScopeStore}: a Cluster is a resource inside
 * a Project rather than an authorization scope, and the multi-cluster
 * applications must not inherit a scope that would silently narrow them.
 */
export const useTargetClusterStore = createSelectionStore("zke.container-service.cluster");

/**
 * The target Namespace of the namespaced sections, keyed by Cluster.
 *
 * Keyed by Cluster rather than by Project because that is the boundary the name
 * is meaningful in — two Clusters in one Project have unrelated Namespaces that
 * happen to share names.
 */
export const useTargetNamespaceStore = createSelectionStore("zke.container-service.namespace");
