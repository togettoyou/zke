import { createContext, useContext } from "react";

/**
 * Whether the window an application is running in is currently drawn.
 *
 * Minimizing a window does not unmount it — a live terminal, a log stream and a
 * half-filled form all have to survive being put away — so every polling query
 * inside it kept firing at a screen nobody was looking at. With several windows
 * open and a couple of them minimized, that is a steady stream of requests, each
 * one executed by a Cluster's Agent, producing data that is thrown away before
 * it is ever painted.
 *
 * React Query already stops polling a background *tab*; this is the same idea one
 * level down, for a desktop where a window can be as hidden as a tab is.
 *
 * It reports drawn-ness, not focus. An unfocused window sitting beside the one
 * being worked in is still being read, and a list that stopped refreshing
 * whenever it was not the active window would be worse than one that never
 * refreshed at all — it would be right only while looked at directly.
 *
 * Defaults to `true` so anything rendered outside a window — sign-in, the shell
 * itself, a test — behaves exactly as it did before.
 */
export const WindowVisibilityContext = createContext(true);

export function useWindowVisible(): boolean {
  return useContext(WindowVisibilityContext);
}

/**
 * A React Query `refetchInterval` that pauses while the window is put away.
 *
 * `false` is how React Query is told to hold a poll without touching anything
 * else about the query: what is already cached stays cached and stays readable,
 * and restoring the window starts the interval again from that point.
 */
export function pollWhileVisible(visible: boolean, intervalMs: number): number | false {
  return visible ? intervalMs : false;
}
