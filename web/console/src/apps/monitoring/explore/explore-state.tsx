import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import { useMetricsExplore } from "@/api/queries/observability";
import type { MetricsExploreOutcome, MetricsExploreResult } from "@/api/types";
import { notifyFailure } from "@/components/common/notify";

import { useMetricsScope } from "../metrics-scope";
import {
  ExploreContext,
  type ExploreKind,
  type ExploreValue,
  type ExpressionRow,
} from "./explore-context";
import { EXPRESSION_REFS, MAX_EXPRESSIONS } from "./metricsql";

/**
 * How long a rerun triggered by hiding, showing or deleting a row waits.
 *
 * Long enough to collapse a burst of clicks — hiding two rows in a row is one
 * question — and short enough that a single click reads as immediate. Every one
 * of these requests lands on storage every Cluster shares, so the coalescing is
 * not a nicety.
 */
const AUTO_RUN_DELAY_MS = 220;

function nextRef(rows: ExpressionRow[]): string {
  const taken = new Set(rows.map((row) => row.ref));
  return EXPRESSION_REFS.find((ref) => !taken.has(ref)) ?? EXPRESSION_REFS[0];
}

/** What the rows say, as one string, so a change to any of them is one compare. */
function signatureOf(rows: ExpressionRow[], kind: ExploreKind): string {
  return `${kind} ${rows
    .filter((row) => !row.hidden)
    .map((row) => `${row.ref}${row.expression.trim()}`)
    .join(" ")}`;
}

/**
 * The state of the Explore screen, kept above the section that renders it.
 *
 * It lives here rather than inside the section for one reason: the navigation
 * rail unmounts a section when the operator moves to another one, and an
 * expression somebody spent five minutes writing must survive a glance at
 * 计算资源. It is per window rather than a module-level store, so two monitoring
 * windows are two independent workbenches.
 */
export function ExploreProvider({
  enabled,
  initialExpression = "",
  children,
}: {
  /**
   * Whether the Explore screen is the one on screen.
   *
   * The provider stays mounted while the operator is looking at another
   * section, so the auto-rerun below has to be told to stop: re-running an
   * expression on every tick of a clock nobody is watching is load on storage
   * every Cluster shares, for an answer no one will read.
   */
  enabled: boolean;
  /** Expression carried by an AIOps evidence deep link. It is loaded but not run automatically. */
  initialExpression?: string;
  children: ReactNode;
}) {
  const { clusterId, readWindow, refreshToken } = useMetricsScope();
  const [rows, setRows] = useState<ExpressionRow[]>(() => [
    { ref: EXPRESSION_REFS[0], expression: initialExpression, hidden: false },
  ]);
  const [kind, setKind] = useState<ExploreKind>("range");
  const [activeRef, setActiveRef] = useState<string>(EXPRESSION_REFS[0]);
  const [result, setResult] = useState<MetricsExploreResult | null>(null);
  const [ranSignature, setRanSignature] = useState<string | null>(null);
  // Bumped by an edit that changes *which* expressions run, as opposed to what
  // one of them says. The auto-rerun effect below watches it rather than the
  // rows themselves, so typing never fires a query and hiding one always does.
  const [selectionToken, setSelectionToken] = useState(0);
  const { mutate: runExplore, isPending: running } = useMetricsExplore();

  // Computed from the current rows rather than from inside a state updater: an
  // updater has to be pure, and both of these also have to move the focused
  // row, which is a second piece of state.
  const addRow = useCallback(
    (expression = "") => {
      if (rows.length >= MAX_EXPRESSIONS) {
        return;
      }
      const row = { ref: nextRef(rows), expression, hidden: false };
      setRows([...rows, row]);
      setActiveRef(row.ref);
    },
    [rows],
  );

  const setExpression = useCallback((ref: string, expression: string) => {
    setRows((current) => current.map((row) => (row.ref === ref ? { ...row, expression } : row)));
  }, []);

  const toggleHidden = useCallback((ref: string) => {
    setRows((current) =>
      current.map((row) => (row.ref === ref ? { ...row, hidden: !row.hidden } : row)),
    );
    setSelectionToken((token) => token + 1);
  }, []);

  const removeRow = useCallback((ref: string) => {
    // The last row is emptied rather than removed: an editor with no rows has
    // no way back to having one except a button that would only ever be
    // pressed immediately.
    setRows((current) =>
      current.length === 1
        ? [{ ref: current[0]?.ref ?? EXPRESSION_REFS[0], expression: "", hidden: false }]
        : current.filter((row) => row.ref !== ref),
    );
    setSelectionToken((token) => token + 1);
  }, []);

  const insertExpression = useCallback(
    (expression: string) => {
      const empty =
        rows.find((row) => row.ref === activeRef && row.expression.trim() === "") ??
        rows.find((row) => row.expression.trim() === "");
      if (empty) {
        setRows(
          rows.map((row) => (row.ref === empty.ref ? { ...row, expression, hidden: false } : row)),
        );
        setActiveRef(empty.ref);
        return;
      }
      if (rows.length >= MAX_EXPRESSIONS) {
        return;
      }
      const row = { ref: nextRef(rows), expression, hidden: false };
      setRows([...rows, row]);
      setActiveRef(row.ref);
    },
    [activeRef, rows],
  );

  const reset = useCallback(() => {
    setRows([{ ref: EXPRESSION_REFS[0], expression: "", hidden: false }]);
    setActiveRef(EXPRESSION_REFS[0]);
    setResult(null);
    // Back to `untouched`, which is what stops the auto-rerun below from firing
    // on an editor that now has nothing in it.
    setRanSignature(null);
    // Bumped so a run already waiting out its delay — 清空 pressed within a
    // moment of hiding a row — is cancelled rather than landing afterwards and
    // taking the screen back out of its opening state.
    setSelectionToken((token) => token + 1);
  }, []);

  // Held in a ref so the auto-rerun effect below does not have to list every
  // piece of state the run reads, and so it never re-runs merely because the
  // operator typed a character.
  const runRef = useRef<() => void>(() => {});
  const run = useCallback(() => {
    if (!clusterId) {
      return;
    }
    const active = rows.filter((row) => !row.hidden && row.expression.trim() !== "");
    setRanSignature(signatureOf(rows, kind));
    if (active.length === 0) {
      // Everything was hidden or deleted. The answer on screen describes
      // expressions that are no longer being asked, so it goes with them —
      // leaving it up would be a chart labelled with a question nobody put.
      setResult(null);
      return;
    }
    const chartWindow = readWindow();
    runExplore(
      {
        cluster_id: clusterId,
        kind,
        ...(kind === "range"
          ? {
              start: new Date(chartWindow.startMs).toISOString(),
              step_seconds: chartWindow.stepSeconds,
            }
          : {}),
        end: new Date(chartWindow.endMs).toISOString(),
        queries: active.map((row) => ({
          ref_id: row.ref,
          expression: row.expression.trim(),
        })),
      },
      {
        onSuccess: (data) => setResult(data),
        onError: (error) => notifyFailure("执行查询", error),
      },
    );
  }, [clusterId, kind, readWindow, rows, runExplore]);
  // Written from an effect rather than during render, and declared before the
  // effect that reads it so the ref is already current when that one fires.
  useEffect(() => {
    runRef.current = run;
  }, [run]);

  // Only once something has been run: opening the view must not fire a query
  // for an empty editor, and neither should the first tick of the clock.
  const hasRun = ranSignature !== null;

  // The question moved out from under the answer: a new range, the refresh
  // button, a tick of the auto refresh, a different target Cluster, or the
  // query kind switched. Asked again immediately — each of these is one
  // deliberate act, and the Cluster switch has already dropped the previous
  // answer, so any delay here is a gap with nothing in it on screen.
  //
  // The kind belongs here rather than on the 执行查询 button because it is a
  // property of the question and not an edit to it: reading a range answer
  // under the 瞬时 label is the one outcome that toggle must never produce.
  useEffect(() => {
    if (!hasRun || !enabled) {
      return;
    }
    runRef.current();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshToken, clusterId, kind]);

  // Which expressions are being asked changed: one was hidden, shown or
  // deleted. Also asked again — a curve that stays on the chart after its row
  // was hidden is the chart disagreeing with the editor above it.
  //
  // Deferred by a moment, unlike the effect above. Hiding three rows in a row
  // is one question rather than three, and the cleanup cancels a pending run
  // whenever another change lands first, so what reaches storage is the state
  // the operator stopped on. Editing the *text* of an expression is
  // deliberately not a trigger at all: that fires on every keystroke, and the
  // 执行查询 button plus the 表达式已修改 warning are what cover it.
  useEffect(() => {
    if (!hasRun || !enabled) {
      return;
    }
    const timer = window.setTimeout(() => runRef.current(), AUTO_RUN_DELAY_MS);
    return () => window.clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectionToken]);

  // A Cluster switch drops the answer immediately rather than when the re-run
  // lands: the old result describes a different Cluster, and showing it under
  // the new one's name even for a second is showing the wrong thing.
  //
  // Adjusted during render rather than from an effect, which is what React
  // prescribes for state derived from a prop change: an effect would paint the
  // previous Cluster's numbers once under the new Cluster's name first.
  const [lastCluster, setLastCluster] = useState(clusterId);
  if (lastCluster !== clusterId) {
    setLastCluster(clusterId);
    if (result) {
      setResult(null);
    }
  }

  // Whether there is a question to ask at all. Shared with the editor and the
  // results panel so the button, the empty state and the run path all agree on
  // what "nothing to run" means.
  const runnable = rows.some((row) => !row.hidden && row.expression.trim() !== "");

  const outcomes = useMemo(() => {
    const byRef = new Map<string, MetricsExploreOutcome>();
    for (const outcome of result?.queries ?? []) {
      byRef.set(outcome.ref_id, outcome);
    }
    return byRef;
  }, [result]);

  const value = useMemo<ExploreValue>(
    () => ({
      rows,
      kind,
      setKind,
      addRow,
      setExpression,
      toggleHidden,
      removeRow,
      insertExpression,
      reset,
      focusRow: setActiveRef,
      activeRef,
      run,
      running,
      runnable,
      result,
      outcomes,
      stale: ranSignature !== null && ranSignature !== signatureOf(rows, kind),
      untouched: ranSignature === null,
    }),
    [
      activeRef,
      addRow,
      insertExpression,
      kind,
      outcomes,
      ranSignature,
      removeRow,
      reset,
      result,
      rows,
      run,
      running,
      runnable,
      setExpression,
      toggleHidden,
    ],
  );

  return <ExploreContext.Provider value={value}>{children}</ExploreContext.Provider>;
}
