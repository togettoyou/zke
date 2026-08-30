import { createContext, useContext } from "react";

import type { MetricsExploreOutcome, MetricsExploreResult } from "@/api/types";

export type ExploreKind = "range" | "instant";

export type ExpressionRow = {
  /**
   * The row's identity, and the `ref_id` the Server echoes back. Assigned once
   * and never reused while the row exists, so an answer always lands under the
   * expression that asked for it even after a row above it was deleted.
   */
  ref: string;
  expression: string;
  /**
   * A hidden row keeps its text and is not executed. It is the cheap way to ask
   * "what does this look like without that curve" without losing the curve.
   */
  hidden: boolean;
};

export type ExploreValue = {
  rows: ExpressionRow[];
  kind: ExploreKind;
  setKind: (kind: ExploreKind) => void;
  addRow: (expression?: string) => void;
  setExpression: (ref: string, expression: string) => void;
  toggleHidden: (ref: string) => void;
  removeRow: (ref: string) => void;
  /**
   * Puts an expression where the operator was last typing, or in a new row when
   * that one already holds something. Used by the saved-query picker: replacing
   * whatever was in the box would throw away work that was never saved.
   */
  insertExpression: (expression: string) => void;
  /** Empties the editor and drops the answer, back to how the screen opened. */
  reset: () => void;
  focusRow: (ref: string) => void;
  activeRef: string;
  run: () => void;
  running: boolean;
  /** Whether any expression is both written and not hidden. */
  runnable: boolean;
  result: MetricsExploreResult | null;
  outcomes: Map<string, MetricsExploreOutcome>;
  /** The expressions changed since the answer on screen was produced. */
  stale: boolean;
  /** Nothing has been run yet in this session of the view. */
  untouched: boolean;
};

export const ExploreContext = createContext<ExploreValue | null>(null);

export function useExplore(): ExploreValue {
  const value = useContext(ExploreContext);
  if (!value) {
    throw new Error("useExplore must be used inside ExploreProvider");
  }
  return value;
}
