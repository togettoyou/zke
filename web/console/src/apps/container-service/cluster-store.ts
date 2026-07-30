import { create } from "zustand";

const STORAGE_KEY = "zke.container-service.cluster";

type TargetClusterState = {
  /** Selected Cluster per Project, so switching Projects and back restores it. */
  byProject: Record<string, string>;
  select: (projectId: string, clusterId: string) => void;
};

function load(): Record<string, string> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) {
      return {};
    }
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return {};
    }
    const result: Record<string, string> = {};
    for (const [projectId, clusterId] of Object.entries(parsed)) {
      if (typeof clusterId === "string") {
        result[projectId] = clusterId;
      }
    }
    return result;
  } catch {
    return {};
  }
}

function save(byProject: Record<string, string>): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(byProject));
  } catch {
    // Storage may be unavailable or full; the selection keeps working in memory.
  }
}

/**
 * The target Cluster of the container service window.
 *
 * Container service is a single-cluster application, so it needs a current
 * Cluster the way the rest of the Console needs a current Project. It is
 * deliberately not part of {@link useScopeStore}: a Cluster is a resource inside
 * a Project rather than an authorization scope, and the multi-cluster
 * applications must not inherit a scope that would silently narrow them.
 *
 * Only identifiers are stored, and only to survive a window being closed and
 * reopened. A stored Cluster is still re-resolved against the Project's online
 * Clusters on every render, so a stale or unreachable identifier selects
 * nothing.
 */
export const useTargetClusterStore = create<TargetClusterState>((set) => ({
  byProject: load(),
  select: (projectId, clusterId) =>
    set((state) => {
      const byProject = { ...state.byProject, [projectId]: clusterId };
      save(byProject);
      return { byProject };
    }),
}));
