import { useEffect, useState } from "react";

/** How long a search box waits before the typed value reaches a query key. */
export const SEARCH_DEBOUNCE_MS = 250;

/**
 * Returns `value` after it has stopped changing for `delayMs`.
 *
 * Search boxes feed query keys directly, so without this every keystroke is a
 * request: typing a nine-character cluster name asked the Server nine questions
 * and threw eight of the answers away. The input itself stays uncontrolled by
 * the delay — only the value that reaches the query is held back — so typing
 * never feels laggy.
 *
 * The delay is short on purpose. This is not throttling the Server; it is
 * waiting for a word to finish.
 */
export function useDebouncedValue<T>(value: T, delayMs: number = SEARCH_DEBOUNCE_MS): T {
  const [debounced, setDebounced] = useState(value);

  useEffect(() => {
    if (value === debounced) {
      return;
    }
    const timer = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(timer);
  }, [value, debounced, delayMs]);

  return debounced;
}
