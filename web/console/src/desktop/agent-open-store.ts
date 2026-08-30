import { create } from "zustand";

/**
 * Whether AIOps may open applications on this desktop by itself.
 *
 * A preference rather than a permission: opening a window grants nothing, and
 * the Server has already refused any intent whose view the operator may not
 * read. What this decides is who controls the screen — and that is the one part
 * of the arrangement a person can reasonably want back without giving up the
 * agent, which is why it is a switch and not an approval prompt. Turning it off
 * does not silence anything: every intent still lands in the conversation as an
 * entry with a button on it.
 *
 * Local to the browser, like the theme and the saved layout. It describes this
 * screen, and the same operator on a wall display and on a laptop is reasonably
 * answering the question differently.
 */
export const AGENT_OPEN_STORAGE_KEY = "zke.desktop.agent-open";

function readInitial(): boolean {
  try {
    // Default on: the capability is the point, and an agent that has to be
    // switched on before it can show anybody a chart is one nobody discovers.
    return localStorage.getItem(AGENT_OPEN_STORAGE_KEY) !== "off";
  } catch {
    return true;
  }
}

type AgentOpenState = {
  autoOpen: boolean;
  setAutoOpen: (allowed: boolean) => void;
};

export const useAgentOpenStore = create<AgentOpenState>((set) => ({
  autoOpen: readInitial(),
  setAutoOpen: (allowed) => {
    try {
      localStorage.setItem(AGENT_OPEN_STORAGE_KEY, allowed ? "on" : "off");
    } catch {
      // Storage may be unavailable; the choice still holds for this session.
    }
    set({ autoOpen: allowed });
  },
}));
