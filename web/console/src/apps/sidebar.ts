import { useEffect, useState, type RefObject } from "react";

/**
 * The surface width at which a sidebar can still be a column of its own.
 *
 * It mirrors the `@2xl` container query the two shells that have a sidebar —
 * `AppShell` and AIOps — lay themselves out with. The query owns the layout;
 * this constant exists so that the behaviour which has to follow it, a
 * navigation dismissing a panel that is covering what it just changed, follows
 * the same number rather than a second guess at it.
 */
export const SIDEBAR_COLUMN_MIN_WIDTH = 672;

/**
 * Whether the measured surface is too narrow to carry a sidebar beside its
 * content — which is when the sidebar stops being a column and becomes a panel
 * over it.
 *
 * Measured from the element, never from the screen. A window dragged down to
 * 500px on a 2560px display is in exactly the situation a phone is in, and the
 * answer has to be the same one.
 */
export function useNarrowSurface(ref: RefObject<HTMLElement | null>): boolean {
  const [narrow, setNarrow] = useState(false);

  useEffect(() => {
    const node = ref.current;
    if (!node) {
      return;
    }
    const observer = new ResizeObserver(() => {
      setNarrow(node.clientWidth < SIDEBAR_COLUMN_MIN_WIDTH);
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, [ref]);

  return narrow;
}
