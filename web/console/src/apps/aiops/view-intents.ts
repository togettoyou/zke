import { useEffect, useRef } from "react";
import { create } from "zustand";

import type { AITrajectoryEntry } from "@/api/types";
import { openConsoleView } from "@/apps/evidence-link";
import { useAgentOpenStore } from "@/desktop/agent-open-store";
import { useWindowStore } from "@/desktop/window-store";

/**
 * Which view intents this desktop actually followed, by session and sequence.
 *
 * A store rather than component state because it is a record of something that
 * happened outside React — a window opened — and because the conversation has
 * to be able to say so honestly: whether the screen moved depends on where the
 * operator was looking at that moment, which the trail does not record and must
 * not be guessed at from it.
 *
 * Session-scoped and never cleared: it holds a handful of numbers per
 * conversation for as long as the tab is open, and a reload correctly starts
 * empty, because after a reload nothing on this desktop was opened by anyone.
 */
type FollowedViews = {
  bySession: Record<string, number[]>;
  markFollowed: (sessionId: string, sequence: number) => void;
};

const useFollowedViews = create<FollowedViews>((set) => ({
  bySession: {},
  markFollowed: (sessionId, sequence) =>
    set((state) => {
      const current = state.bySession[sessionId] ?? [];
      if (current.includes(sequence)) return state;
      return { bySession: { ...state.bySession, [sessionId]: [...current, sequence] } };
    }),
}));

const NONE: readonly number[] = [];

/**
 * Acting on the views AIOps asked the desktop to open.
 *
 * The intent travels on a durable trajectory entry rather than on a live-only
 * signal, which is what makes the record and the action the same fact: what
 * moved the operator's screen is in the trail, exportable, and still there when
 * the conversation is reopened next week. That choice is also the reason this
 * hook exists — a durable entry is replayed on every load, and replaying one
 * would open windows for an investigation that finished days ago.
 *
 * So three things have to be true before the desktop moves, and each of them
 * failing leaves the same fallback: the intent renders in the conversation as a
 * card with a button the operator presses when they want it.
 *
 * 1. The intent arrived while this session was on screen. Everything already in
 *    the trail when the view mounted is history, including a turn that ran to
 *    completion while the window was closed.
 * 2. The operator is actually here — the browser tab is visible and the AIOps
 *    window is not minimized. Somebody who parked a long investigation and went
 *    to work in another window has not agreed to have that window covered.
 * 3. Once per turn. The Server enforces the same bound, so a run cannot walk
 *    the operator through four applications; this is the half that holds even if
 *    a future Server allows more.
 *
 * Per session by construction: the view that hosts it is keyed by session id,
 * so switching conversations remounts it and the watermark is taken again.
 */
export function useViewIntents(
  sessionId: string,
  entries: AITrajectoryEntry[],
  windowId: string,
): readonly number[] {
  const autoOpen = useAgentOpenStore((state) => state.autoOpen);
  const minimized = useWindowStore((state) => state.windows[windowId]?.mode === "minimized");
  const markFollowed = useFollowedViews((state) => state.markFollowed);
  const followed = useFollowedViews((state) => state.bySession[sessionId]);
  // Which intents this view has already decided about, so a re-render or a
  // refetch cannot open the same window twice.
  const settled = useRef<Set<number>>(new Set());
  const primed = useRef(false);
  const openedTurn = useRef<number | null>(null);

  useEffect(() => {
    if (!primed.current) {
      primed.current = true;
      for (const entry of entries) {
        if (entry.content.view) settled.current.add(entry.sequence);
      }
      return;
    }
    for (const entry of entries) {
      const view = entry.content.view;
      if (!view || settled.current.has(entry.sequence)) continue;
      // Settled either way: an intent the operator did not see happen must not
      // open later, when they have moved on to something else entirely.
      settled.current.add(entry.sequence);
      if (!autoOpen || minimized || document.visibilityState !== "visible") continue;
      if (openedTurn.current === entry.turn) continue;
      openedTurn.current = entry.turn;
      openConsoleView(view);
      markFollowed(sessionId, entry.sequence);
    }
  }, [autoOpen, entries, markFollowed, minimized, sessionId]);

  return followed ?? NONE;
}
