import { RotateCw } from "lucide-react";

import { Button } from "@/components/ui/button";

/**
 * Reloads what a section is showing.
 *
 * Kubernetes objects change without the Console being told and these lists are
 * read once and cached, so every section needs a way to ask again. Without one
 * the only way to see current state was to leave the section and come back,
 * which reloads for a reason — the cache went stale — that has nothing to do
 * with what the operator was trying to do.
 *
 * It lives in the toolbar so it is in the same place in every section, and it
 * spins while the request is in flight because a list that is already current
 * looks identical before and after.
 */
export function RefreshAction({
  isFetching,
  onRefresh,
}: {
  isFetching?: boolean;
  onRefresh: () => void;
}) {
  return (
    <Button size="sm" variant="secondary" disabled={isFetching} onClick={onRefresh}>
      <RotateCw className={isFetching ? "animate-spin" : undefined} />
      刷新
    </Button>
  );
}
