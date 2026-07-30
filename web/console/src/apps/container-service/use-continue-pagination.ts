import { useState } from "react";

/**
 * Paging over Kubernetes continuation tokens.
 *
 * Kubernetes pages forward only: a token names the position after the page that
 * produced it, and there is no way to ask for the page before one. So the tokens
 * already visited are kept in order, and going back means re-requesting an
 * earlier token rather than computing an offset.
 *
 * `resetKey` discards the trail. A token is only meaningful for the list that
 * issued it, so changing Cluster — or any filter — has to start over at page one
 * instead of replaying a token the new list has never seen.
 */
export function useContinuePagination(resetKey: string) {
  const [tokens, setTokens] = useState<string[]>([""]);
  const [pageIndex, setPageIndex] = useState(0);
  const [appliedKey, setAppliedKey] = useState(resetKey);
  // Reset during render, not in an effect: one render showing page three of the
  // previous Cluster is one render too many.
  if (appliedKey !== resetKey) {
    setAppliedKey(resetKey);
    setTokens([""]);
    setPageIndex(0);
  }

  return {
    token: tokens[pageIndex] ?? "",
    pageIndex,
    goPrevious: () => setPageIndex((value) => Math.max(0, value - 1)),
    goNext: (nextToken: string) => {
      if (!nextToken) {
        return;
      }
      setTokens((current) => [...current.slice(0, pageIndex + 1), nextToken]);
      setPageIndex((value) => value + 1);
    },
  };
}
